package webserver

import "net/http"

func usersPageHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/users" {
		http.NotFound(w, r)
		return
	}

	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)

		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(usersPageHTML))
}

// usersPageHTML is served at GET /users (see web/users.html), reachable
// only by superusers (see requireSuperuser in webserver.go). Like
// configPageHTML, it's a static page driven entirely by fetch calls to
// /api/users, with no server-side templating.
var usersPageHTML = renderPage("users.html")
