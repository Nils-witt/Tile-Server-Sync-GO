package webserver

// setupPageHTML is served at GET /setup (see web/setup.html), only
// reachable while no account exists yet (see setupGate in auth.go).
// Creating an account here always grants every permission plus superuser,
// since there's no one else yet to have granted anything more selectively
// (see allPermissions/setupHandler).
var setupPageHTML = renderPage("setup.html")
