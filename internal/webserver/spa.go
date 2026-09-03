package webserver

import (
	"Tile-Server-Sync-GO/frontend"
	"io/fs"
	"net/http"
	"strings"
)

// spaFS is the embedded Vite build (frontend.Dist) rooted at its "dist"
// subdirectory instead of "dist" itself, so a request for "/assets/foo.js"
// maps directly to the embedded "assets/foo.js" without every caller having
// to know about the embed's own directory layout.
func spaFS() (fs.FS, error) {
	return fs.Sub(frontend.Dist, "dist")
}

// spaHandler serves the built single-page app for every GET request not
// otherwise claimed by a more specific "/api/..." pattern (see New below): a
// request naming a real built asset (e.g. "/assets/index-abc123.js") is
// served as that file; anything else — "/", "/config", "/login", a deep
// client-side route like "/config#maps" — falls back to index.html so
// react-router (running client-side) can render it. There is deliberately no
// server-side auth/permission gate here (unlike the old per-page handlers
// this replaces): every route in the SPA renders the same bundle, which
// itself calls GET /api/me and GET /api/setup-status to decide what to show,
// exactly as the API's own 401/403 responses already gate every actual
// action.
func spaHandler(distFS fs.FS) http.HandlerFunc {
	fileServer := http.FileServer(http.FS(distFS))

	return func(w http.ResponseWriter, r *http.Request) {
		if isBuiltAsset(distFS, r.URL.Path) {
			// Vite content-hashes every file under /assets/, so a hit there
			// can never change without the URL itself changing too.
			if strings.HasPrefix(r.URL.Path, "/assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}

			fileServer.ServeHTTP(w, r)

			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")

		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		fileServer.ServeHTTP(w, r2)
	}
}

// isBuiltAsset reports whether path (a request's URL.Path, always
// "/"-prefixed) names a real file in distFS, so spaHandler can tell a static
// asset request apart from a client-side route that needs the SPA shell
// instead.
func isBuiltAsset(distFS fs.FS, path string) bool {
	name := strings.TrimPrefix(path, "/")
	if name == "" {
		return false
	}

	f, err := distFS.Open(name)
	if err != nil {
		return false
	}

	_ = f.Close()

	return true
}
