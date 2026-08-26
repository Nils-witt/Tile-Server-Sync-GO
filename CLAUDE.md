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
                                        # configDb); api/database/maps (each with its own interval)
                                        # are entered via /config
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
other field of `config.Config` (`api`, `database`, `maps`) lives in a SQLite database at
`Bootstrap.ConfigDB` instead, edited through the `/config` web UI. `webServer` stays
file/CLI-driven — see "why webServer isn't in SQLite" below.

- **`internal/config`** — defines `Config` (`API`, `Database`, `[]MapTarget`, `WebServer`) and its
  validation/defaulting (`Validate`, exported since callers other than `Parse` now assemble a
  `*Config` themselves — see `configdb.Store.Load`/`runtime.reload`). `Load`/`Parse` (YAML bytes →
  validated `*Config`) still exist and are used by the web UI's raw-YAML editing mode. `Bootstrap`
  (`bootstrap.go`) is the separate, minimal file-backed type — `LoadBootstrap` reads it, applies
  the same `webServer.enabled && address == ""` defaulting as `Validate` (shared via
  `WebServer.applyDefault`), and resolves `ConfigDB` (default `"config.db"`) relative to the
  bootstrap file's own directory. `Config.Maps` is a list of `{id, versions[], interval,
  staticColumns}` entries; a version string may be a real numeric version, the literal `"current"`,
  or a user-defined alias (see `PUT /maps/{id}/aliases/{alias}` in the tileserve-go API). Each
  map's own optional `interval` (a Go duration string, parsed by `MapTarget.validateInterval` and
  read back via `MapTarget.SyncInterval()`) controls how often *that* map re-syncs — there is no
  longer a global interval; a map with no `interval` syncs once and isn't automatically repeated
  (see `runLoop` below). Validation requires either `api.token` or both
  `api.username`/`api.password`.
- **`internal/configdb`** — the new SQLite-backed store for everything in `Config` except
  `WebServer`, as a relational schema (not a serialized blob): a singleton `config_scalar` row for
  `api`/`database`'s scalar fields, plus `database_columns`, `maps` (which also holds each map's
  own `interval` column), `map_versions`, and `map_static_columns` tables (ordered by a
  `sort_order` column, since `syncAll` iterates maps/versions in configured order). `Store.Load`
  assembles a `*config.Config`
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
  (HTML+vanilla JS, no build step, sections split into tabs — API / Database / Maps) backed by a
  JSON API at `/api/config` (`internal/webserver/config.go`), now reading/writing a
  `*configdb.Store` instead of a file path. GET calls `cfgDB.Load` and overlays the fixed bootstrap
  `WebServer` value onto the returned `Config` (still present in the JSON response and used
  server-side for validation) — an empty/unconfigured database is not an error, so the structured
  form always renders (blank on a fresh install) rather than falling back to raw-YAML mode, which
  is now reserved for genuine load failures. POST accepts either a structured `config` object or a
  `raw` YAML string, discards/overwrites whatever `webServer` value was submitted with the fixed
  bootstrap one *before* validating (so a bad or irrelevant `webServer` edit can never even trigger
  a validation error), validates via `Config.Validate`, and only calls `cfgDB.Save` if that
  passes — an invalid save is rejected with the validation error and the database is left
  untouched, and also calls `reload` (see below) on every successful save so the change applies to
  the running process immediately. `webServer.enabled`/`address` have no inputs in the config page
  at all (removed entirely, not just disabled) since changing them isn't possible through this API
  and always needs a process restart — see below. The status page instead carries one action per
  configured map: a "Sync" button next to each map/version row in the results table posts to
  `POST /api/sync/{mapID}` (`syncMapAPIHandler`, with the ID taken from the URL path), wired to
  `runtime.runSyncMaps` with a single-ID set, to run that one map's sync immediately rather than
  waiting for its next `interval` tick. Like the status page, none of this has authentication —
  only enable `webServer` on a trusted network.
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
`reload` (see below) → hand off to `syncAll(ctx, maps, client, db, rec)` for each map × version in
`maps` (some subset of the configured maps — see `runLoop` below): fetch, overwrite each object's
`Version` with the configured version string (so an alias like `"current"` is what lands in the
database, not whatever concrete version the API resolved it to), then upsert (and prune, if
enabled), logging counts as it goes.

The client and database connection aren't held directly by `run`, though — they're wrapped in a
`*runtime` (`runtime.go`), a mutex-guarded holder of the current `{cfg, client, db}` triple (which
starts out all-nil — see "starting unconfigured" below), plus a second mutex (`syncMu`) dedicated
to serializing syncs, and two fields fixed for the process's lifetime: `cfgDB` (the
`*configdb.Store`) and `webServer` (the bootstrap-sourced `config.WebServer`, overlaid onto every
loaded `Config` before it's validated or used). `runtime.runSync(ctx, rec)` syncs every configured
map and is what the run-once path in `run` (no map has an `interval`, `webServer.enabled` is
false) calls at startup; `runtime.runSyncMaps(ctx, rec, ids)` syncs just the maps whose ID is in
`ids` and is what both `runLoop`'s per-map scheduler (see below) and the status page's per-map
"Sync" button (`POST /api/sync/{mapID}`, called with a single-ID set) call. Both lock `syncMu`,
read the current `{cfg, client, db}` via `runtime.current()` — returning `errNotConfigured`
instead of calling `syncAll` if `db` is still nil — and call `syncAll`; the `syncMu` lock is what
stops a manual per-map sync from running concurrently with a scheduled tick against the same
database, which could otherwise race on `pruneMissing` deleting rows the other's insert just
wrote.

`runtime.reload(ctx)` is what a successful `POST /api/config` save calls (wired in as a `reload`
closure passed to `startWebServer`/`webserver.New` — see above), and is also what `run` calls once
at startup to do the initial configure: it
loads via `rt.cfgDB.Load`, overlays `rt.webServer`, calls `Config.Validate`, and — only if that
succeeds — builds a fresh client (re-logging in, unless a token is configured) and database
connection (`newClient`/`openStore`, factored out of `run` so both it and `reload` share them),
swapping them into the runtime (closing the old database connection afterwards, nil-guarded for
the first successful reload) only if all of that succeeds — so an invalid edit or an unreachable
API/DB leaves the previous, still-working state (which may be the initial unconfigured state) in
place. This is how config changes made through the web UI (new/removed maps, per-map intervals,
credentials, DB settings) take effect without a process restart. `webServer.enabled`/`address` are
the one exception: changing those still needs a restart, since the server a reload request arrives on
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
whether any configured map has a usable `interval` yet.

`runLoop` (`main.go`) no longer runs one global interval loop — since `Interval` now lives per-map
(`config.MapTarget`), each map is scheduled independently. It tracks an in-memory
`lastSync map[string]time.Time` (map ID → last sync start), rebuilt from scratch on every process
start (nothing about scheduling state is persisted). Each tick: if `rt.configured()` is false, it
just logs a wait message and falls back to `pollInterval` (5s), same as before; otherwise
`dueMaps` computes the set of currently-due map IDs from the latest `rt.current()` config — a map
with no `lastSync` entry yet is always due once (covers both startup and a map added via a live
reload), and after that a map with a positive `Interval` is due again once that much time has
passed, while a map with no `Interval` is never due again automatically. Due maps (if any) are
synced together via `rt.runSyncMaps`, then `nextWake` computes how long to sleep: the shortest
remaining time until any already-synced, positive-`Interval` map next comes due, or `pollInterval`
if there's no such map (nothing configured, every map is one-shot, or nothing has synced yet).
This means: whenever `webServer.enabled` is true, the process no longer ever exits on its own (a
deliberate behavior change from before SQLite-backed config — a config with only one-shot maps
used to run once and exit even with `webServer` on); `run` only takes the old "run once and exit"
branch — now gated on `!cfg.HasRecurringMaps()` (true when no configured map has a positive
`Interval`) — when `webServer.enabled` is false, where config is guaranteed valid up front and
there's no live-edit scenario to accommodate.

Sync is idempotent: rows are upserted by `uuid`, so re-running (whether manually or via a map's own
`interval`) updates existing rows rather than duplicating them.

### Windows service support

`-service install|uninstall|start|stop|run` (handled by `handleServiceCommand` in `main.go`)
lets the binary register/manage itself as a Windows service instead of running in a console
session — pairs naturally with maps that have their own `interval` set, for an unattended
long-running sync. The real
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
