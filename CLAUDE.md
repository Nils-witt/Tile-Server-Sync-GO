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
enabled), logging counts as it goes. Login happens once in `run`, not per sync.

If top-level `interval` is set in config (a Go duration string, e.g. `"5m"`), `run` calls
`runLoop` instead of running `syncAll` once: it repeats `syncAll` on that interval until the
process receives SIGINT/SIGTERM (`main` wires a `signal.NotifyContext` for this), logging and
continuing past a sync error rather than exiting, since a transient failure shouldn't kill an
otherwise long-running process. No `interval` (the default) preserves the original run-once
behavior.

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
