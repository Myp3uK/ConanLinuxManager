package httpapi

import (
	"context"
	"net/http"

	"github.com/example/conan-manager/internal/panel"
)

type userKey struct{}

// currentUser is the administrator who made the request.
func currentUser(r *http.Request) string {
	user, _ := r.Context().Value(userKey{}).(string)
	return user
}

// BasicAuth is an initial protection layer for the API. It is intended to be
// placed behind HTTPS/reverse proxy. The administrator from the environment
// file always works; the ones added in the panel are checked against SQLite.
func BasicAuth(admins *panel.Admins) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/healthz" {
				next.ServeHTTP(w, r)
				return
			}
			if !admins.Configured(r.Context()) {
				// Never expose the management API without credentials. This also
				// makes a broken or incomplete environment file fail closed.
				writeStatus(w, http.StatusServiceUnavailable)
				return
			}
			user, pass, ok := r.BasicAuth()
			if !ok || !admins.Check(r.Context(), user, pass) {
				w.Header().Set("WWW-Authenticate", `Basic realm="Conan Manager"`)
				writeStatus(w, http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, user)))
		})
	}
}
