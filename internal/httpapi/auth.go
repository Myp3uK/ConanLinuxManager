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
			if r.URL.Path == "/healthz" {
				next.ServeHTTP(w, r)
				return
			}
			if username == "" || password == "" {
				// Never expose the management API without credentials. This also
				// makes a broken or incomplete environment file fail closed.
				writeStatus(w, http.StatusServiceUnavailable)
				return
			}
			user, pass, ok := r.BasicAuth()
			if !ok || subtle.ConstantTimeCompare([]byte(user), []byte(username)) != 1 || subtle.ConstantTimeCompare([]byte(pass), []byte(password)) != 1 {
				w.Header().Set("WWW-Authenticate", `Basic realm="Conan Manager"`)
				writeStatus(w, http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
