// Package webui embeds the built admin single-page application.
//
// The Vite build writes to web/dist; `go build` embeds that directory so the
// bot and its admin panel ship as a single binary. A placeholder index.html is
// committed so the Go module still compiles before the frontend is built.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// Dist returns the embedded build output rooted at dist/.
func Dist() (fs.FS, error) {
	return fs.Sub(distFS, "dist")
}

// Handler serves the SPA: real files when they exist, index.html otherwise so
// client-side routes work on a hard refresh.
func Handler() http.Handler {
	sub, err := Dist()
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "admin bundle unavailable", http.StatusInternalServerError)
		})
	}
	fileServer := http.FileServerFS(sub)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}

		if f, err := sub.Open(name); err == nil {
			if info, statErr := f.Stat(); statErr == nil && !info.IsDir() {
				_ = f.Close()
				if strings.HasPrefix(name, "assets/") {
					// Hashed filenames are immutable.
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				fileServer.ServeHTTP(w, r)
				return
			}
			_ = f.Close()
		}

		serveIndex(w, r, sub)
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, sub fs.FS) {
	raw, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		http.Error(w, "admin bundle unavailable", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(raw)
}
