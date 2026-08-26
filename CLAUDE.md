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
                                        # config.yaml is now just a small bootstrap file (webServer +
                                        # configDb); api/database/maps/interval are entered via /config
go vet ./...                           # vet
golangci-lint run                      # lint (see .golangci.yml — extensive linter set enabled)
govulncheck ./...                      # vulnerability scan
```

Both `golangci-lint run` and `govulncheck ./...` run in the Husky `pre-commit` hook
(`.husky/pre-commit`) — expect them to run on every commit.

There is no Go test suite yet (`go test ./...` will report "no test files"). The root
`package.json`/`npm` setup exists only to drive Husky; it is not a Node project.

## Architecture

Config now comes from two places, wired together in `main.go`'s `run()`:

```
config.LoadBootstrap()  →  configdb.Store  →  tileserve.Client  →  store.Store
 (YAML → Bootstrap)        (SQLite → Config)   (HTTP API client)   (MariaDB upsert)
```

`Bootstrap` (just `webServer` + `configDb`) comes from the small YAML file at `-config`; every
other field of `config.Config` (`api`, `database`, `maps`, `interval`) lives in a SQLite database
at `Bootstrap.ConfigDB` instead, edited through the `/config` web UI. `webServer` stays
file/CLI-driven — see "why webServer isn't in SQLite" below.

- **`internal/config`** — defines `Config` (`API`, `Database`, `[]MapTarget`, `WebServer`,
  `Interval`) and its validation/defaulting (`Validate`, exported since callers other than `Parse`
  now assemble a `*Config` themselves — see `configdb.Store.Load`/`runtime.reload`). `Load`/`Parse`
  (YAML bytes → validated `*Config`) still exist and are used by the web UI's raw-YAML editing
  mode. `Bootstrap` (`bootstrap.go`) is the separate, minimal file-backed type — `LoadBootstrap`
  reads it, applies the same `webServer.enabled && address == ""` defaulting as `Validate` (shared
  via `WebServer.applyDefault`), and resolves `ConfigDB` (default `"config.db"`) relative to the
  bootstrap file's own directory. `Config.Maps` is a list of `{id, versions[]}` pairs; a version
  string may be a real numeric version, the literal `"current"`, or a user-defined alias (see
  `PUT /maps/{id}/aliases/{alias}` in the tileserve-go API). Validation requires either
  `api.token` or both `api.username`/`api.password`.
- **`internal/configdb`** — the new SQLite-backed store for everything in `Config` except
  `WebServer`, as a relational schema (not a serialized blob): a singleton `config_scalar` row for
  `api`/`database`'s scalar fields and `interval`, plus `database_columns`, `maps`,
  `map_versions`, and `map_static_columns` tables (ordered by a `sort_order` column, since
  `syncAll` iterates maps/versions in configured order). `Store.Load` assembles a `*config.Config`
  with `WebServer` left zero-valued — callers always overlay the bootstrap value before using or
  validating it. `Store.Save` replaces `database_columns`/`maps` wholesale inside one transaction
  (delete-then-reinsert, not diffed) and never reads or writes `WebServer`. Uses
  `modernc.org/sqlite` (pure Go, no cgo, to keep GoReleaser's cross-compiled and Windows service
  builds working) and pins the connection pool to one connection (`SetMaxOpenConns(1)`) so its
  `PRAGMA foreign_keys = ON` (needed for `ON DELETE CASCADE` on `maps` deletes) reliably applies —
  SQLite pragmas are per-connection, and `database/sql`'s pool would otherwise silently hand out a
  fresh, pragma-less one. Unrelated to `internal/store` (MariaDB geo-object storage); no shared
  code.
- **`internal/tileserve`** — minimal synchronous HTTP client for tileserve-go. `Login()`
  exchanges username/password for a bearer token; `SetToken()` bypasses login when a token is
  already known. `GeoObjects(mapID, version)` fetches and JSON-decodes one map/version's objects
  (`GeoObject` struct mirrors the API's schema exactly — field-for-field, including JSON tags).
- **`internal/webserver`** — besides the status page (`/`), serves a config editor at `/config`
  (HTML+vanilla JS, no build step) backed by a JSON API at `/api/config`
  (`internal/webserver/config.go`), now reading/writing a `*configdb.Store` instead of a file path.
  GET calls `cfgDB.Load` and overlays the fixed bootstrap `WebServer` value (for display only) —
  an empty/unconfigured database is not an error, so the structured form always renders (blank on
  a fresh install) rather than falling back to raw-YAML mode, which is now reserved for genuine
  load failures. POST accepts either a structured `config` object or a `raw` YAML string,
  discards/overwrites whatever `webServer` value was submitted with the fixed bootstrap one
  *before* validating (so a bad or irrelevant `webServer` edit can never even trigger a validation
  error), validates via `Config.Validate`, and only calls `cfgDB.Save` if that passes — an invalid
  save is rejected with the validation error and the database is left untouched. The config page's
  `webServer.enabled`/`address` inputs are disabled (read-only), since changing them isn't
  possible through this API at all — see below. Two action endpoints round it out:
  `POST /api/reload` calls the `reload func(context.Context) error` passed into `webserver.New`
  (wired to `runtime.reload` — see below) to make the *running* process pick up whatever is
  currently saved in `configdb` immediately, without a restart — the config page's "Apply saved
  config now" button hits it after a save; `POST /api/sync` calls the
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
tracing behavior end-to-end: load the bootstrap file → open `configdb` → attempt an initial
`reload` (see below) → hand off to `syncAll(ctx, cfg, client, db)` for each map × version: fetch,
overwrite each object's `Version` with the configured version string (so an alias like `"current"`
is what lands in the database, not whatever concrete version the API resolved it to), then upsert
(and prune, if enabled), logging counts as it goes.

The client and database connection aren't held directly by `run`, though — they're wrapped in a
`*runtime` (`runtime.go`), a mutex-guarded holder of the current `{cfg, client, db}` triple (which
starts out all-nil — see "starting unconfigured" below), plus a second mutex (`syncMu`) dedicated
to serializing syncs, and two fields fixed for the process's lifetime: `cfgDB` (the
`*configdb.Store`) and `webServer` (the bootstrap-sourced `config.WebServer`, overlaid onto every
loaded `Config` before it's validated or used). `runtime.runSync(ctx, rec)` is what both
`runLoop`'s scheduled ticks and the run-once path in `run` call: it locks `syncMu`, reads the
current `{cfg, client, db}` via `runtime.current()` — returning `errNotConfigured` instead of
calling `syncAll` if `db` is still nil — and calls `syncAll`; the `syncMu` lock is what stops a
manual "sync now" request from the web UI (`/api/sync`, wired to the same `runSync`) from running
concurrently with a scheduled tick against the same database, which could otherwise race on
`pruneMissing` deleting rows the other's insert just wrote.

`runtime.reload(ctx)` is what the config web UI's `/api/reload` endpoint calls (wired in as a
closure passed to `startWebServer`/`webserver.New`, alongside a `syncNow` closure over `runSync`
for `/api/sync`), and is also what `run` calls once at startup to do the initial configure: it
loads via `rt.cfgDB.Load`, overlays `rt.webServer`, calls `Config.Validate`, and — only if that
succeeds — builds a fresh client (re-logging in, unless a token is configured) and database
connection (`newClient`/`openStore`, factored out of `run` so both it and `reload` share them),
swapping them into the runtime (closing the old database connection afterwards, nil-guarded for
the first successful reload) only if all of that succeeds — so an invalid edit or an unreachable
API/DB leaves the previous, still-working state (which may be the initial unconfigured state) in
place. This is how config changes made through the web UI (new maps, credentials, DB settings,
interval) take effect without a process restart. `webServer.enabled`/`address` are the one
exception: changing those still needs a restart, since the server a reload request arrives on
can't safely restart itself mid-request — this is also why they live in the bootstrap file rather
than `configdb` at all: `configdb`-backed settings are exactly the ones `reload` can apply live,
and `webServer` structurally can't be.

**Starting unconfigured**: since there's no automatic migration of pre-SQLite `config.yaml`
content, a fresh install's `configdb` is empty, and `run`'s initial `reload` call fails validation
(missing `api.baseUrl` etc.) — expected, not a bug. If `webServer.enabled` is false at that point,
`run` fails hard (there'd be no way to fix it otherwise, same as an invalid `config.yaml` always
failed hard). If `webServer.enabled` is true, `run` logs the error and continues: the web server
starts regardless, `GET /config` renders an all-blank structured form (not the raw-YAML fallback —
see the `internal/webserver` bullet above), and the process falls into `runLoop` regardless of
whether there's a usable `interval` yet.

`runLoop` (`main.go`) checks `rt.configured()` (`db != nil`) at the top of every iteration: while
unconfigured, it skips `runSync` and just logs a wait message; once configured, it runs `runSync`
and tracks the last positive `SyncInterval()` it saw, exactly as before a reload could ever start
from nothing. It sleeps `lastInterval` if positive, else a fixed `pollInterval` (5s) — so an
unconfigured runtime, or one that's valid but has no `interval` set, polls for a future reload
instead of exiting. This means: whenever `webServer.enabled` is true, the process no longer ever
exits on its own (a deliberate behavior change from before SQLite-backed config — a valid,
interval-less config used to run once and exit even with `webServer` on); `run` only takes the old
"run once and exit if `SyncInterval() <= 0`" branch when `webServer.enabled` is false, where
config is guaranteed valid up front and there's no live-edit scenario to accommodate.

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
