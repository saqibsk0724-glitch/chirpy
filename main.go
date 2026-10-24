package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"

	"github.com/saqibsk0724-glitch/chirpy/internal/database"
)

type apiConfig struct {
	fileserverHits atomic.Int32
	dbQueries      *database.Queries
	platform       string
}

type createUserParams struct {
	Email string `json:"email"`
}

type createChirpParams struct {
	Body   string    `json:"body"`
	UserID uuid.UUID `json:"user_id"`
}

type User struct {
	ID        uuid.UUID `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Email     string    `json:"email"`
}

type Chirp struct {
	ID        uuid.UUID `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Body      string    `json:"body"`
	UserID    uuid.UUID `json:"user_id"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func main() {
	godotenv.Load()

	dbURL := os.Getenv("DB_URL")
	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		log.Fatal(err)
	}

	if err := db.Ping(); err != nil {
		log.Fatal(err)
	}

	dbQueries := database.New(db)

	cfg := &apiConfig{
		dbQueries: dbQueries,
		platform:  os.Getenv("PLATFORM"),
	}

	mux := http.NewServeMux()

	// Health and metrics.
	mux.HandleFunc("GET /api/healthz", handlerReadiness)
	mux.HandleFunc("GET /admin/metrics", cfg.handlerMetrics)
	mux.HandleFunc("POST /admin/reset", cfg.handlerReset)

	// Users.
	mux.HandleFunc("POST /api/users", cfg.handlerCreateUser)

	// Chirps.
	mux.HandleFunc("POST /api/chirps", cfg.handlerCreateChirp)

	// Fileserver.
	fileServer := http.FileServer(http.Dir("."))
	mux.Handle("/app/", cfg.middlewareMetricsInc(
		http.StripPrefix("/app", fileServer),
	))

	server := &http.Server{
		Addr:    ":8080",
		Handler: mux,
	}

	log.Println("Server listening on :8080")
	log.Fatal(server.ListenAndServe())
}

func handlerReadiness(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

func (cfg *apiConfig) middlewareMetricsInc(
	next http.Handler,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg.fileserverHits.Add(1)
		next.ServeHTTP(w, r)
	})
}

func (cfg *apiConfig) handlerMetrics(
	w http.ResponseWriter,
	r *http.Request,
) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	hits := cfg.fileserverHits.Load()

	w.Write([]byte(
		"<html>" +
			"<body>" +
			"<h1>Welcome, Chirpy Admin</h1>" +
			"<p>Chirpy has been visited " +
			strings.TrimSpace(string(rune(hits))) +
			" times!</p>" +
			"</body>" +
			"</html>",
	))
}

func (cfg *apiConfig) handlerReset(
	w http.ResponseWriter,
	r *http.Request,
) {
	if cfg.platform != "dev" {
		respondWithError(
			w,
			http.StatusForbidden,
			"Reset is only allowed in dev environment",
		)
		return
	}

	err := cfg.dbQueries.DeleteAllUsers(r.Context())
	if err != nil {
		respondWithError(
			w,
			http.StatusInternalServerError,
			"Couldn't delete users",
		)
		return
	}

	cfg.fileserverHits.Store(0)

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Hits reset to 0"))
}

func (cfg *apiConfig) handlerCreateUser(
	w http.ResponseWriter,
	r *http.Request,
) {
	params := createUserParams{}

	err := json.NewDecoder(r.Body).Decode(&params)
	if err != nil {
		respondWithError(
			w,
			http.StatusBadRequest,
			"Invalid JSON",
		)
		return
	}

	user, err := cfg.dbQueries.CreateUser(r.Context(), params.Email)
	if err != nil {
		respondWithError(
			w,
			http.StatusInternalServerError,
			"Couldn't create user",
		)
		return
	}

	apiUser := User{
		ID:        user.ID,
		CreatedAt: user.CreatedAt,
		UpdatedAt: user.UpdatedAt,
		Email:     user.Email,
	}

	respondWithJSON(w, http.StatusCreated, apiUser)
}

func (cfg *apiConfig) handlerCreateChirp(
	w http.ResponseWriter,
	r *http.Request,
) {
	params := createChirpParams{}

	err := json.NewDecoder(r.Body).Decode(&params)
	if err != nil {
		respondWithError(
			w,
			http.StatusBadRequest,
			"Invalid JSON",
		)
		return
	}

	// Chirps cannot be longer than 140 characters.
	if utf8.RuneCountInString(params.Body) > 140 {
		respondWithError(
			w,
			http.StatusBadRequest,
			"Chirp is too long",
		)
		return
	}

	// Replace profane words with asterisks.
	cleanedBody := cleanProfaneWords(params.Body)

	// Save the chirp in the database.
	chirp, err := cfg.dbQueries.CreateChirp(
		r.Context(),
		database.CreateChirpParams{
			Body:   cleanedBody,
			UserID: params.UserID,
		},
	)
	if err != nil {
		respondWithError(
			w,
			http.StatusInternalServerError,
			"Couldn't create chirp",
		)
		return
	}

	apiChirp := Chirp{
		ID:        chirp.ID,
		CreatedAt: chirp.CreatedAt,
		UpdatedAt: chirp.UpdatedAt,
		Body:      chirp.Body,
		UserID:    chirp.UserID,
	}

	respondWithJSON(w, http.StatusCreated, apiChirp)
}

func cleanProfaneWords(body string) string {
	profaneWords := map[string]bool{
		"kerfuffle": true,
		"sharbert":  true,
		"fornax":    true,
	}

	words := strings.Fields(body)

	for i, word := range words {
		if profaneWords[strings.ToLower(word)] {
			words[i] = "****"
		}
	}

	return strings.Join(words, " ")
}

func respondWithJSON(
	w http.ResponseWriter,
	code int,
	payload interface{},
) {
	data, err := json.Marshal(payload)
	if err != nil {
		respondWithError(
			w,
			http.StatusInternalServerError,
			"Couldn't encode response",
		)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write(data)
}

func respondWithError(
	w http.ResponseWriter,
	code int,
	msg string,
) {
	respondWithJSON(
		w,
		code,
		errorResponse{
			Error: msg,
		},
	)
}
