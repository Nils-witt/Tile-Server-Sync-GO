package webserver

// loginPageHTML is served at GET /login (see web/login.html). It works as a
// plain HTML form (no JavaScript required) and also progressively enhances
// itself with a fetch-based submit so a failed login doesn't need a full
// page reload.
var loginPageHTML = renderPage("login.html")
