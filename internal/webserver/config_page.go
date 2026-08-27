package webserver

// configPageHTML is the config editor page served at GET /config (see
// web/config.html and configPageHandler in config.go). It is a static page
// (unlike the status page, it doesn't need server-side templating: all data
// comes from and goes to /api/config via fetch), styled to match the status
// page via the shared baseCSS. The script builds DOM nodes with
// createElement/textContent rather than innerHTML for any server-supplied
// value, since that value is attacker-controllable if configPath is ever
// edited by something untrusted.
var configPageHTML = renderPage("config.html")
