// Package frontend embeds the built Vite/React SPA (see this directory's
// package.json/vite.config.ts) so the Go binary can serve it without any
// external files at runtime — see internal/webserver/spa.go for how it's
// served.
//
// dist/ is git-ignored (it's a build artifact — run `npm ci && npm run
// build` in this directory to produce it) except for dist/index.html, kept
// as a placeholder purely so this go:embed directive has at least one file
// to match and a bare `go build`/`go vet` still succeeds for a contributor
// who hasn't run the frontend build yet; CI and GoReleaser always build the
// real thing first (see .github/workflows/ci.yml and .goreleaser.yml).
package frontend

import "embed"

// Dist is the embedded contents of dist/, the Vite build output.
//
//go:embed all:dist
var Dist embed.FS
