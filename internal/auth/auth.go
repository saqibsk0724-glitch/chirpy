func GetAPIKey(headers http.Header) (string, error) {
	authHeader := headers.Get("Authorization")

	parts := strings.Split(authHeader, " ")
	if len(parts) != 2 || parts[0] != "ApiKey" || parts[1] == "" {
		return "", errors.New("malformed authorization header")
	}

	return parts[1], nil
}