package httpapi

import (
	"crypto/subtle"
	"net/http"
)

// BasicAuth is an initial protection layer for the API. It is intended to be
// placed behind HTTPS/reverse proxy; the password is supplied outside SQLite.
func BasicAuth(username, password string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if username == "" || password == "" || r.URL.Path == "/healthz" {
				next.ServeHTTP(w, r)
				return
			}
			user, pass, ok := r.BasicAuth()
			if !ok || subtle.ConstantTimeCompare([]byte(user), []byte(username)) != 1 || subtle.ConstantTimeCompare([]byte(pass), []byte(password)) != 1 {
				w.Header().Set("WWW-Authenticate", `Basic realm="Conan Manager"`)
				writeError(w, http.StatusUnauthorized, nil)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
