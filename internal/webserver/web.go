package webserver

import (
	"embed"
	"strings"
)

// This file wires up the web server's pages, which live as real HTML/CSS/JS
// files under web/ (embedded at compile time via go:embed) rather than as Go
// string constants — the historical approach here, before every page's
// markup/script had to be hand-escaped into a Go raw string literal. Pages
// are still composed the same way they always were, by textual substitution
// of a handful of shared partials rather than html/template's contextual
// autoescaping: autoescaping would treat baseCSS/the *JS partials as
// plain-text template data and re-encode them for their surrounding
// <style>/<script> context, corrupting the CSS/JS they actually contain.

//go:embed web/base.css
var baseCSS string

//go:embed web/theme_init.js
var themeInitScript string

//go:embed web/theme_toggle.js
var themeToggleJS string

//go:embed web/account_nav.js
var accountNavJS string

//go:embed web/footer.html
var footerTemplate string

//go:embed web/status.html web/config.html web/login.html web/setup.html web/users.html web/security_log.html
var pageFS embed.FS

// pageReplacer splices the shared CSS/JS partials into a page's
// {{BASE_CSS}}/{{THEME_INIT_JS}}/{{THEME_TOGGLE_JS}}/{{ACCOUNT_NAV_JS}}
// placeholders. Not every page uses every placeholder (e.g. login.html and
// setup.html have no account nav, so they don't contain
// {{ACCOUNT_NAV_JS}}) — Replace leaves markers that aren't present alone, so
// one shared replacer works for all pages. Every page also contains a
// {{FOOTER}} marker, deliberately left untouched here — see buildFooter
// below for why. Status.html additionally contains real html/template
// actions (e.g. {{.StartedAt}}); those are untouched here since they don't
// match any of these four exact tokens, and are parsed as a template
// afterward by the caller (see webserver.go's New).
var pageReplacer = strings.NewReplacer(
	"{{BASE_CSS}}", baseCSS,
	"{{THEME_INIT_JS}}", themeInitScript,
	"{{THEME_TOGGLE_JS}}", themeToggleJS,
	"{{ACCOUNT_NAV_JS}}", accountNavJS,
)

// renderPage reads the named file out of the embedded web/ directory and
// splices in the shared partials via pageReplacer. name is a filename under
// web/ (e.g. "login.html") baked in by every caller, so a missing file is a
// build-time bug, not a runtime condition to handle gracefully.
func renderPage(name string) string {
	b, err := pageFS.ReadFile("web/" + name)
	if err != nil {
		panic(err)
	}

	return pageReplacer.Replace(string(b))
}

// buildFooter renders the shared copyright footer (web/footer.html) with
// the running build's version/commit spliced in. Unlike BASE_CSS/THEME_INIT_JS/
// THEME_TOGGLE_JS/ACCOUNT_NAV_JS, this isn't part of pageReplacer: those are
// fixed at compile time, but version/commit are only known once New() is
// called with the values main.go read from its -ldflags-set package vars —
// so each page's {{FOOTER}} marker (left untouched by renderPage/pageReplacer)
// is resolved separately, once, in New().
func buildFooter(version, commit string) string {
	r := strings.NewReplacer("{{VERSION}}", version, "{{COMMIT}}", commit)

	return r.Replace(footerTemplate)
}
