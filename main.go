package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"

	"github.com/saqibsk0724-glitch/chirpy/internal/auth"
	"github.com/saqibsk0724-glitch/chirpy/internal/database"
)

type apiConfig struct {
	fileserverHits atomic.Int32
	dbQueries      *database.Queries
	platform       string
	jwtSecret      string
	polkaKey       string
}

type createUserParams struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type createChirpParams struct {
	Body string `json:"body"`
}

type User struct {
	ID          uuid.UUID `json:"id"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Email       string    `json:"email"`
	IsChirpyRed bool      `json:"is_chirpy_red"`
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

type loginParams struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginResponse struct {
	ID           uuid.UUID `json:"id"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	Email        string    `json:"email"`
	Token        string    `json:"token"`
	RefreshToken string    `json:"refresh_token"`
	IsChirpyRed  bool      `json:"is_chirpy_red"`
}

type refreshResponse struct {
	Token string `json:"token"`
}

type updateUserParams struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func main() {
	godotenv.Load()

	dbURL := os.Getenv("DB_URL")
	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		log.Fatal(err)
	}

	dbQueries := database.New(db)

	cfg := &apiConfig{
		dbQueries: dbQueries,
		platform:  os.Getenv("PLATFORM"),
		jwtSecret: os.Getenv("JWT_SECRET"),
		polkaKey:  os.Getenv("POLKA_KEY"),
	}

	mux := http.NewServeMux()

	// Health and metrics.
	mux.HandleFunc("GET /api/healthz", handlerReadiness)
	mux.HandleFunc("GET /admin/metrics", cfg.handlerMetrics)
	mux.HandleFunc("POST /admin/reset", cfg.handlerReset)
	mux.HandleFunc("POST /api/refresh", cfg.handlerRefresh)
	mux.HandleFunc("POST /api/revoke", cfg.handlerRevoke)
	mux.HandleFunc("POST /api/polka/webhooks", cfg.handlerPolkaWebhooks)

	// Users.
	mux.HandleFunc("POST /api/users", cfg.handlerCreateUser)
	mux.HandleFunc("POST /api/login", cfg.handlerLogin)
	mux.HandleFunc("PUT /api/users", cfg.handlerUpdateUser)

	// Chirps.
	mux.HandleFunc("POST /api/chirps", cfg.handlerCreateChirp)
	mux.HandleFunc("GET /api/chirps", cfg.handlerGetChirps)
	mux.HandleFunc("GET /api/chirps/{chirpID}", cfg.handlerGetChirp)
	mux.HandleFunc("DELETE /api/chirps/{chirpID}", cfg.handlerDeleteChirp)

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

//secure the webhook handler

//webhook handler

func (cfg *apiConfig) handlerPolkaWebhooks(
	w http.ResponseWriter,
	r *http.Request,
) {

	// Check Api Key First

	apiKey, err := auth.GetAPIKey(r.Header)
	if err != nil || apiKey != cfg.polkaKey {
		respondWithJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "Unauthorized",
		})
		return
	}

	type webhookRequest struct {
		Event string `json:"event"`
		Data  struct {
			UserID string `json:"user_id"`
		} `json:"data"`
	}

	// Decode the webhook request body.
	var req webhookRequest

	err = json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		respondWithError(
			w,
			http.StatusBadRequest,
			"Invalid request body",
		)
		return
	}

	// Ignore events we don't care about.
	if req.Event != "user.upgraded" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Parse the user ID.
	userID, err := uuid.Parse(req.Data.UserID)
	if err != nil {
		respondWithError(
			w,
			http.StatusBadRequest,
			"Invalid user ID",
		)
		return
	}

	// Upgrade the user to Chirpy Red.
	rowsAffected, err := cfg.dbQueries.UpgradeUserToChirpyRed(
		r.Context(),
		userID,
	)

	if err != nil {
		respondWithError(
			w,
			http.StatusInternalServerError,
			"Database error",
		)
		return
	}

	// Check whether the user exists.
	if rowsAffected == 0 {
		respondWithError(
			w,
			http.StatusNotFound,
			"User not found",
		)
		return
	}

	// Successfully upgraded.
	w.WriteHeader(http.StatusNoContent)
}

func (cfg *apiConfig) handlerDeleteChirp(
	w http.ResponseWriter,
	r *http.Request,
) {
	// 1. Get the access token from the Authorization header.
	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(
			w,
			http.StatusUnauthorized,
			"Missing or invalid authorization token",
		)
		return
	}

	// 2. Validate the JWT and extract the authenticated user's ID.
	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(
			w,
			http.StatusUnauthorized,
			"Invalid or expired token",
		)
		return
	}

	// 3. Get the chirp ID from the URL.
	chirpID := r.PathValue("chirpID")

	// 4. Convert the chirp ID into a UUID.
	id, err := uuid.Parse(chirpID)
	if err != nil {
		respondWithError(
			w,
			http.StatusBadRequest,
			"Invalid chirp ID",
		)
		return
	}

	// 5. Fetch the chirp from the database.
	chirp, err := cfg.dbQueries.GetChirp(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			respondWithError(
				w,
				http.StatusNotFound,
				"Chirp not found",
			)
			return
		}

		respondWithError(
			w,
			http.StatusInternalServerError,
			"Couldn't get chirp",
		)
		return
	}

	// 6. Check whether the authenticated user is the author.
	if chirp.UserID != userID {
		respondWithError(
			w,
			http.StatusForbidden,
			"You are not authorized to delete this chirp",
		)
		return
	}

	// 7. Delete the chirp.
	err = cfg.dbQueries.DeleteChirp(r.Context(), id)
	if err != nil {
		respondWithError(
			w,
			http.StatusInternalServerError,
			"Couldn't delete chirp",
		)
		return
	}

	// 8. Return 204 No Content.
	w.WriteHeader(http.StatusNoContent)
}

// handler update user
func (cfg *apiConfig) handlerUpdateUser(
	w http.ResponseWriter,
	r *http.Request,
) {
	// Get the access token from the Authorization header.
	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(
			w,
			http.StatusUnauthorized,
			"Missing or invalid authorization token",
		)
		return
	}

	// Validate the JWT and extract the authenticated user's ID.
	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(
			w,
			http.StatusUnauthorized,
			"Invalid or expired token",
		)
		return
	}

	// Decode the new email and password.
	params := updateUserParams{}

	err = json.NewDecoder(r.Body).Decode(&params)
	if err != nil {
		respondWithError(
			w,
			http.StatusBadRequest,
			"Invalid JSON",
		)
		return
	}

	// Hash the new password.
	hashedPassword, err := auth.HashPassword(params.Password)
	if err != nil {
		respondWithError(
			w,
			http.StatusInternalServerError,
			"Couldn't hash password",
		)
		return
	}

	// Update the authenticated user's email and password.
	user, err := cfg.dbQueries.UpdateUser(
		r.Context(),
		database.UpdateUserParams{
			Email:          params.Email,
			HashedPassword: hashedPassword,
			ID:             userID,
		},
	)
	if err != nil {
		respondWithError(
			w,
			http.StatusInternalServerError,
			"Couldn't update user",
		)
		return
	}

	// Return the updated user without the password hash.
	apiUser := User{
		ID:          user.ID,
		CreatedAt:   user.CreatedAt,
		UpdatedAt:   user.UpdatedAt,
		Email:       user.Email,
		IsChirpyRed: user.IsChirpyRed,
	}

	respondWithJSON(w, http.StatusOK, apiUser)
}

func (cfg *apiConfig) handlerRefresh(
	w http.ResponseWriter,
	r *http.Request,
) {
	refreshToken, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(
			w,
			http.StatusUnauthorized,
			"Invalid refresh token",
		)
		return
	}

	user, err := cfg.dbQueries.GetUserFromRefreshToken(
		r.Context(),
		refreshToken,
	)
	if err != nil {
		respondWithError(
			w,
			http.StatusUnauthorized,
			"Invalid refresh token",
		)
		return
	}

	accessToken, err := auth.MakeJWT(
		user.ID,
		cfg.jwtSecret,
		time.Hour,
	)
	if err != nil {
		respondWithError(
			w,
			http.StatusInternalServerError,
			"Couldn't create access token",
		)
		return
	}

	respondWithJSON(
		w,
		http.StatusOK,
		refreshResponse{Token: accessToken},
	)
}

func handlerReadiness(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

func (cfg *apiConfig) handlerRevoke(
	w http.ResponseWriter,
	r *http.Request,
) {
	refreshToken, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(
			w,
			http.StatusUnauthorized,
			"Invalid refresh token",
		)
		return
	}

	err = cfg.dbQueries.RevokeRefreshToken(
		r.Context(),
		refreshToken,
	)
	if err != nil {
		respondWithError(
			w,
			http.StatusInternalServerError,
			"Couldn't revoke refresh token",
		)
		return
	}

	w.WriteHeader(http.StatusNoContent)
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
	hits := cfg.fileserverHits.Load()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	w.Write([]byte(
		"<html>" +
			"<body>" +
			"<h1>Welcome, Chirpy Admin</h1>" +
			"<p>Chirpy has been visited " +
			strconv.FormatInt(int64(hits), 10) +
			" times!</p>" +
			"</body>" +
			"</html>",
	))
}

func (cfg *apiConfig) handlerGetChirp(
	w http.ResponseWriter,
	r *http.Request,
) {
	// Get the chirp ID from the URL.
	chirpID := r.PathValue("chirpID")

	// Convert the string ID into a UUID.
	id, err := uuid.Parse(chirpID)
	if err != nil {
		respondWithError(
			w,
			http.StatusBadRequest,
			"Invalid chirp ID",
		)
		return
	}

	// Fetch the chirp from the database.
	chirp, err := cfg.dbQueries.GetChirp(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			respondWithError(
				w,
				http.StatusNotFound,
				"Chirp not found",
			)
			return
		}

		respondWithError(
			w,
			http.StatusInternalServerError,
			"Couldn't get chirp",
		)
		return
	}

	// Convert the database chirp into the API response.
	apiChirp := Chirp{
		ID:        chirp.ID,
		CreatedAt: chirp.CreatedAt,
		UpdatedAt: chirp.UpdatedAt,
		Body:      chirp.Body,
		UserID:    chirp.UserID,
	}

	respondWithJSON(w, http.StatusOK, apiChirp)
}

func (cfg *apiConfig) handlerGetChirps(
	w http.ResponseWriter,
	r *http.Request,
) {
	// Get the optional author_id query parameter.
	authorID := r.URL.Query().Get("author_id")

	var chirps []database.Chirp
	var err error

	// If author_id is provided, fetch chirps by author.
	if authorID != "" {
		id, err := uuid.Parse(authorID)
		if err != nil {
			respondWithError(
				w,
				http.StatusBadRequest,
				"Invalid author ID",
			)
			return
		}

		chirps, err = cfg.dbQueries.GetChirpsByAuthor(
			r.Context(),
			id,
		)
	} else {
		// Otherwise, fetch all chirps.
		chirps, err = cfg.dbQueries.GetChirps(r.Context())
	}

	if err != nil {
		respondWithError(
			w,
			http.StatusInternalServerError,
			"Couldn't get chirps",
		)
		return
	}

	sortOrder := r.URL.Query().Get("sort")

	// Default sorting is ascending.
	if sortOrder == "" {
		sortOrder = "asc"
	}

	// Sort chirps in memory.
	sort.Slice(chirps, func(i, j int) bool {
		if sortOrder == "desc" {
			return chirps[i].CreatedAt.After(chirps[j].CreatedAt)
		}

		return chirps[i].CreatedAt.Before(chirps[j].CreatedAt)
	})

	// Convert database chirps into API chirps.
	apiChirps := make([]Chirp, 0, len(chirps))

	for _, chirp := range chirps {
		apiChirp := Chirp{
			ID:        chirp.ID,
			CreatedAt: chirp.CreatedAt,
			UpdatedAt: chirp.UpdatedAt,
			Body:      chirp.Body,
			UserID:    chirp.UserID,
		}

		apiChirps = append(apiChirps, apiChirp)
	}

	respondWithJSON(w, http.StatusOK, apiChirps)
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

	// Hash the password before storing it.
	hashedPassword, err := auth.HashPassword(params.Password)
	if err != nil {
		respondWithError(
			w,
			http.StatusInternalServerError,
			"Couldn't hash password",
		)
		return
	}

	// Create the user in the database.
	user, err := cfg.dbQueries.CreateUser(
		r.Context(),
		database.CreateUserParams{
			Email:          params.Email,
			HashedPassword: hashedPassword,
		},
	)
	if err != nil {

		log.Printf("Couldn't create user: %v", err)

		respondWithError(
			w,
			http.StatusInternalServerError,
			"Couldn't create user",
		)
		return
	}

	// Return the user without the password hash.
	apiUser := User{
		ID:          user.ID,
		CreatedAt:   user.CreatedAt,
		UpdatedAt:   user.UpdatedAt,
		Email:       user.Email,
		IsChirpyRed: user.IsChirpyRed,
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

	// Get the Bearer token from the Authorization header.
	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(
			w,
			http.StatusUnauthorized,
			"Missing or invalid authorization token",
		)
		return
	}

	// Validate the JWT and extract the user ID.
	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(
			w,
			http.StatusUnauthorized,
			"Invalid or expired token",
		)
		return
	}

	// Validate the chirp length.
	if utf8.RuneCountInString(params.Body) > 140 {
		respondWithError(
			w,
			http.StatusBadRequest,
			"Chirp is too long",
		)
		return
	}

	// Clean profanity.
	cleanedBody := cleanProfaneWords(params.Body)

	// Create the chirp using the authenticated user's ID.
	chirp, err := cfg.dbQueries.CreateChirp(
		r.Context(),
		database.CreateChirpParams{
			Body:   cleanedBody,
			UserID: userID,
		},
	)
	if err != nil {
		log.Printf("Error creating chirp: %v", err)

		respondWithError(
			w,
			http.StatusInternalServerError,
			"Couldn't create chirp",
		)
		return
	}

	// Return the created chirp.
	apiChirp := Chirp{
		ID:        chirp.ID,
		CreatedAt: chirp.CreatedAt,
		UpdatedAt: chirp.UpdatedAt,
		Body:      chirp.Body,
		UserID:    chirp.UserID,
	}

	respondWithJSON(w, http.StatusCreated, apiChirp)
}

// func (cfg *apiConfig) handlerGetChirps(
// 	w http.ResponseWriter,
// 	r *http.Request,
// ) {
// 	chirps, err := cfg.dbQueries.GetChirps(r.Context())
// 	if err != nil {
// 		respondWithError(
// 			w,
// 			http.StatusInternalServerError,
// 			"Couldn't get chirps",
// 		)
// 		return
// 	}

// 	apiChirps := make([]Chirp, 0, len(chirps))

// 	for _, chirp := range chirps {
// 		apiChirp := Chirp{
// 			ID:        chirp.ID,
// 			CreatedAt: chirp.CreatedAt,
// 			UpdatedAt: chirp.UpdatedAt,
// 			Body:      chirp.Body,
// 			UserID:    chirp.UserID,
// 		}

// 		apiChirps = append(apiChirps, apiChirp)
// 	}

// 	respondWithJSON(w, http.StatusOK, apiChirps)
// }

func (cfg *apiConfig) handlerLogin(
	w http.ResponseWriter,
	r *http.Request,
) {
	params := loginParams{}

	err := json.NewDecoder(r.Body).Decode(&params)
	if err != nil {
		respondWithError(
			w,
			http.StatusBadRequest,
			"Invalid JSON",
		)
		return
	}

	// Find the user by email.
	user, err := cfg.dbQueries.GetUserByEmail(
		r.Context(),
		params.Email,
	)
	if err != nil {
		respondWithError(
			w,
			http.StatusUnauthorized,
			"Incorrect email or password",
		)
		return
	}

	// Check whether the password matches the stored hash.
	match, err := auth.CheckPasswordHash(
		params.Password,
		user.HashedPassword,
	)
	if err != nil || !match {
		respondWithError(
			w,
			http.StatusUnauthorized,
			"Incorrect email or password",
		)
		return
	}

	// Create the access token (valid for 1 hour).
	accessToken, err := auth.MakeJWT(
		user.ID,
		cfg.jwtSecret,
		time.Hour,
	)
	if err != nil {
		respondWithError(
			w,
			http.StatusInternalServerError,
			"Couldn't create access token",
		)
		return
	}

	// Create the refresh token.
	refreshToken := auth.MakeRefreshToken()

	// Store the refresh token (valid for 60 days).
	_, err = cfg.dbQueries.CreateRefreshToken(
		r.Context(),
		database.CreateRefreshTokenParams{
			Token:     refreshToken,
			UserID:    user.ID,
			ExpiresAt: time.Now().UTC().Add(60 * 24 * time.Hour),
		},
	)
	if err != nil {
		respondWithError(
			w,
			http.StatusInternalServerError,
			"Couldn't create refresh token",
		)
		return
	}

	// Return the user, access token, and refresh token.
	response := loginResponse{
		ID:           user.ID,
		CreatedAt:    user.CreatedAt,
		UpdatedAt:    user.UpdatedAt,
		Email:        user.Email,
		Token:        accessToken,
		RefreshToken: refreshToken,
		IsChirpyRed:  user.IsChirpyRed,
	}

	respondWithJSON(w, http.StatusOK, response)
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
