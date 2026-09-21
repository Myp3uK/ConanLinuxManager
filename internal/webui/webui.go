// Package webui serves the browser interface: a single page that drives the
// JSON API. It is embedded in the binary, so a deployment has nothing extra
// to install, and it loads nothing from outside the manager.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed static
var files embed.FS

// contentPolicy allows only the manager's own scripts and styles. The page
// shows text from game logs, player names and RCON answers; with inline
// scripts forbidden, such text cannot become code even if escaping failed.
const contentPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"connect-src 'self'; form-action 'none'; frame-ancestors 'none'; base-uri 'none'"

// Handler serves index.html at / and the assets under /ui/.
func Handler() http.Handler {
	static, err := fs.Sub(files, "static")
	if err != nil {
		panic(err)
	}
	assets := http.StripPrefix("/ui/", http.FileServer(http.FS(static)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Security-Policy", contentPolicy)
		switch {
		case r.URL.Path == "/":
			http.ServeFileFS(w, r, static, "index.html")
		case strings.HasPrefix(r.URL.Path, "/ui/") && !strings.HasSuffix(r.URL.Path, "/"):
			assets.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}
