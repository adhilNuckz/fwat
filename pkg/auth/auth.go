package auth

import (
	"crypto/subtle"
	"net/http"
	"os"
)

// Middleware wraps an http.Handler with HTTP Basic Authentication.
// Credentials are read from the AUTH_USERNAME and AUTH_PASSWORD environment
// variables, falling back to "admin" / "adminpassword123" when unset.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expectedUser := os.Getenv("AUTH_USERNAME")
		if expectedUser == "" {
			expectedUser = "admin"
		}
		expectedPass := os.Getenv("AUTH_PASSWORD")
		if expectedPass == "" {
			expectedPass = "adminpassword123"
		}

		user, pass, ok := r.BasicAuth()
		if !ok ||
			subtle.ConstantTimeCompare([]byte(user), []byte(expectedUser)) != 1 ||
			subtle.ConstantTimeCompare([]byte(pass), []byte(expectedPass)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="WhatsApp Dashboard"`)
			http.Error(w, "Unauthorised", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}
