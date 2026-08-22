# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A CLI tool that fetches geo objects from one or more [tileserve-go](https://github.com/Nils-witt/Tileserve-GO)
maps (at given versions) and upserts them into a MariaDB `geo_objects` table. It talks to the
API described in [`openapi.yaml`](https://github.com/Nils-witt/Tileserve-GO/blob/main/internal/handler/openapi.yaml):

1. `POST /login` — obtain a JWT (unless a token is configured directly).
2. `GET /maps/{id}/version/{version}/geo-objects` — once per configured map/version pair.
3. Upsert each `GeoObject` into `geo_objects` (schema created automatically if missing).

## Commands

```sh
go build -o go-sync-objects .          # build
go run . -config config.yaml           # run (config.yaml is git-ignored; copy config.example.yaml)
go vet ./...                           # vet
golangci-lint run                      # lint (see .golangci.yml — extensive linter set enabled)
govulncheck ./...                      # vulnerability scan
```

Both `golangci-lint run` and `govulncheck ./...` run in the Husky `pre-commit` hook
(`.husky/pre-commit`) — expect them to run on every commit.

There is no Go test suite yet (`go test ./...` will report "no test files"). The root
`package.json`/`npm` setup exists only to drive Husky; it is not a Node project.

## Architecture

Three-package pipeline, wired together in `main.go`'s `run()`:

```
config.Load()  →  tileserve.Client  →  store.Store
 (YAML → Config)   (HTTP API client)   (MariaDB upsert)
```

- **`internal/config`** — loads and validates `config.yaml`. `Config.Maps` is a list of
  `{id, versions[]}` pairs; a version string may be a real numeric version, the literal
  `"current"`, or a user-defined alias (see `PUT /maps/{id}/aliases/{alias}` in the
  tileserve-go API). Validation requires either `api.token` or both `api.username`/`api.password`.
- **`internal/tileserve`** — minimal synchronous HTTP client for tileserve-go. `Login()`
  exchanges username/password for a bearer token; `SetToken()` bypasses login when a token is
  already known. `GeoObjects(mapID, version)` fetches and JSON-decodes one map/version's objects
  (`GeoObject` struct mirrors the API's schema exactly — field-for-field, including JSON tags).
- **`internal/webserver`** — besides the status page (`/`), serves a config editor at `/config`
  (HTML+vanilla JS, no build step) backed by a JSON API at `/api/config`
  (`internal/webserver/config.go`). GET re-reads `configPath` off disk and returns it as
  `config.Config` JSON (via `config.Parse`, exported from `internal/config` alongside `Load` for
  exactly this reuse) plus the raw file text; POST accepts either a structured `config` object
  (marshaled to YAML server-side) or a `raw` YAML string, validates it the same way `Load` does,
  and only writes `configPath` (0600, no comments preserved) if that validation passes — an
  invalid save is rejected with the validation error and the file is left untouched. The page
  falls back to a raw-YAML textarea if the current file doesn't parse. Two action endpoints round
  it out: `POST /api/reload` calls the `reload func(context.Context) error` passed into
  `webserver.New` (wired to `runtime.reload` — see below) to make the *running* process pick up
  whatever is currently saved on disk immediately, without a restart — the config page's "Apply
  saved config now" button hits it after a save; `POST /api/sync` calls the
  `syncNow func(context.Context) (int, error)` passed alongside it (wired to `runtime.runSync`) to
  run a sync immediately rather than waiting for the next `interval` tick — the "Sync now" button.
  Like the status page, none of this has authentication — only enable `webServer` on a trusted
  network.
- **`internal/store`** — owns the MariaDB schema (`EnsureSchema`, idempotent
  `CREATE TABLE IF NOT EXISTS`) and writes (`UpsertGeoObjects`, one transaction per call, batched
  `INSERT ... ON DUPLICATE KEY UPDATE` keyed on `uuid`). Depends on `internal/tileserve` for the
  `GeoObject` type — the same struct flows from HTTP decode straight into SQL bind params with no
  intermediate model. Each map may also configure `staticColumns` (fixed extra column values
  written on every row synced from that map) and the database may enable `pruneMissing` (delete,
  within the same transaction, any previously-synced row for a map_uuid+version scope that the
  latest fetch no longer returned).

`main.go`'s `run(ctx, configPath)` orchestrates the whole flow and is the place to look first when
tracing behavior end-to-end: load config → authenticate → open DB → ensure schema, then hand off
to `syncAll(ctx, cfg, client, db)` for each map × version: fetch, overwrite each object's
`Version` with the configured version string (so an alias like `"current"` is what lands in the
database, not whatever concrete version the API resolved it to), then upsert (and prune, if
enabled), logging counts as it goes.

The client and database connection aren't held directly by `run`, though — they're wrapped in a
`*runtime` (`runtime.go`), a mutex-guarded holder of the current `{cfg, client, db}` triple, plus
a second mutex (`syncMu`) dedicated to serializing syncs. `runtime.runSync(ctx, rec)` is what both
`runLoop`'s scheduled ticks and the run-once path in `run` call: it locks `syncMu`, reads the
current `{cfg, client, db}` via `runtime.current()`, and calls `syncAll` — the `syncMu` lock is
what stops a manual "sync now" request from the web UI (`/api/sync`, wired to the same
`runSync`) from running concurrently with a scheduled tick against the same database, which could
otherwise race on `pruneMissing` deleting rows the other's insert just wrote.

`runtime.reload(ctx, configPath)` is what the config web UI's `/api/reload` endpoint calls (wired
in as a closure passed to `startWebServer`/`webserver.New`, alongside a `syncNow` closure over
`runSync` for `/api/sync`): it re-`Load`s configPath, builds a fresh client (re-logging in, unless
a token is configured) and database connection (`newClient`/`openStore`, factored out of `run` so
both it and `reload` share them), and only swaps them into the runtime — closing the old database
connection afterwards — if all of that succeeds, so an invalid edit or an unreachable API/DB
leaves the previous, still-working state in place. This is how config changes made through the web
UI (new maps, credentials, DB settings, interval) take effect without a process restart.
`webServer.enabled`/`address` are the one exception: changing those still needs a restart, since
the server the reload request arrives on can't safely restart itself mid-request.

If top-level `interval` is set in config (a Go duration string, e.g. `"5m"`), `run` calls
`runLoop` instead of running `runSync` once: it repeats `runSync` on that interval (re-reading the
interval from `runtime.current()` after every call, so a reload can change the cadence) until the
process receives SIGINT/SIGTERM (`main` wires a `signal.NotifyContext` for this), logging and
continuing past a sync error rather than exiting, since a transient failure shouldn't kill an
otherwise long-running process. No `interval` (the default) preserves the original run-once
behavior — and skips `runLoop` entirely; `run` returns as soon as that single `runSync` call
completes, so `webServer` (and therefore `/config`, `/api/reload`, `/api/sync`) is only actually
useful together with `interval`, as the config example file already says.

Sync is idempotent: rows are upserted by `uuid`, so re-running (whether manually or via
`interval`) updates existing rows rather than duplicating them.

### Windows service support

`-service install|uninstall|start|stop|run` (handled by `handleServiceCommand` in `main.go`)
lets the binary register/manage itself as a Windows service instead of running in a console
session — pairs naturally with `interval` for an unattended long-running sync. The real
implementation lives in `service_windows.go` (build-tagged `windows`), using
`golang.org/x/sys/windows/svc`/`svc/mgr`/`svc/eventlog`; `install` records the current exe path
plus an absolute `-config` path and `-service run` as the service's launch command, and registers
an event log source. `service_other.go` (build-tagged `!windows`) provides stub implementations
that return an explanatory error, so `go vet`/`golangci-lint`/builds stay green on
linux/darwin. `main.go` also calls `isWindowsService()` at startup (true only when actually built
for and running under Windows) as a fallback to route into service mode even without `-service
run` on the command line. Neither service file changes `run`/`runLoop`/`syncAll` — the service
wrapper just runs `run(ctx, configPath)` in a goroutine and cancels its context on a Stop/Shutdown
SCM request.

## Linting notes

`.golangci.yml` enables a deliberately broad linter set (correctness, style, complexity,
performance, security, SQL-resource-leak, and logging checks) and disables a curated set of
noisy/opinionated ones — see the `disable:` block's inline comments for the reasoning on each.
Notable enforced limits: `gocyclo` min-complexity 13, `funlen` 120 lines / 80 statements,
`dupl` threshold 100 tokens. Formatting uses `gofmt` + `gofumpt` (with `extra-rules`).
