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
  code. The same database also holds `users` and `sessions` tables (`internal/configdb/users.go`),
  backing the web server's authentication — see the `internal/webserver` bullet below. Unlike
  `api`/`database`, `users` and (`internal/configdb/maps.go`) `maps` get real per-row CRUD methods
  instead of only going through the whole-graph `Load`/`Save` above: `ListMaps`/`GetMap`/
  `CreateMap`/`UpdateMap`/`DeleteMap` (returning `ErrMapNotFound`/`ErrMapIDTaken`, mirroring
  `users`' `ErrUserNotFound`/`ErrUsernameTaken`) back the `/api/maps` REST family in
  `internal/webserver/maps.go` — each map is edited independently rather than resubmitting the
  whole `maps` array. `map_id` has a `CREATE UNIQUE INDEX IF NOT EXISTS` (added in `ensureSchema`,
  since nothing enforced this before it became a per-map resource key) so `{id}`-addressed
  lookups are unambiguous; `CreateUser`, `VerifyPassword`, `ListUsers`,
  `GetUser`, `UpdateUser`, `DeleteUser`, `CreateSession`, `SessionUser`, `DeleteSession`,
  `UserCount` are `users`' equivalents. Passwords are hashed with
  `golang.org/x/crypto/bcrypt`; a session's random token is only ever stored as its SHA-256 hash
  (`sessions.token_hash`) — the raw token lives solely in the browser's session cookie. The same
  database also holds `sso_config` (a singleton settings row, `internal/configdb/sso.go`) and
  `sso_identities` (linking a verified OIDC `(issuer, subject)` pair to a `users` row) backing
  optional SSO login — see "Authentication & permissions" below. Finally it holds `security_log`
  (`internal/configdb/securitylog.go`), an append-only audit trail — see the security log bullet
  under "Authentication & permissions" below.
- **`internal/tileserve`** — minimal synchronous HTTP client for tileserve-go. `Login()`
  exchanges username/password for a bearer token; `SetToken()` bypasses login when a token is
  already known. `GeoObjects(mapID, version)` fetches and JSON-decodes one map/version's objects
  (`GeoObject` struct mirrors the API's schema exactly — field-for-field, including JSON tags).
- **`internal/webserver`** — besides the status page (`/`), serves a config editor at `/config`
  (HTML+vanilla JS, no build step, sections split into tabs — API / Database / Maps / SSO) backed
  by a REST-shaped JSON API, all registered in `webserver.go` using Go 1.22+ `http.ServeMux`'s
  native `"METHOD /path/{param}"` patterns (a wrong method on a registered path gets an automatic
  405 with an `Allow` header from the mux itself, not a per-handler check) — every route's required
  permission is declared right at its `mux.HandleFunc` call, not hidden in a handler. The one route
  needing care around this: the status page is registered as `"GET /{$}"` (exact-root-only), not a
  bare `"/"` — a bare `"/"` is a catch-all subtree pattern that would match *every* unmatched
  method on *every other* path too, silently suppressing the mux's automatic 405 for the whole
  `/api/...` family (a bug caught and fixed during the REST restructuring below — see
  `statusHandler`'s doc comment).

  `GET /api/config` (`internal/webserver/config.go`) returns the whole stored config as a bundle
  (`{config, raw}`, the latter a YAML rendering, both with secrets redacted — see `redactSecrets`)
  reading/writing a `*configdb.Store` instead of a file path; an empty/unconfigured database is not
  an error, so the structured form always renders (blank on a fresh install). The API and Database
  tabs are each their own sub-resource — `GET`/`PUT /api/config/api` and `GET`/`PUT
  /api/config/database` — as is SSO (`GET`/`PUT /api/config/sso`, `internal/webserver/sso.go`). A
  `PUT` loads the currently stored config, replaces just that one section, and saves — deliberately
  *not* gated on `Config.Validate()` passing for the whole merged config (see `finishConfigSave`'s
  doc comment in `config.go`), since that would make it impossible to ever save a single tab during
  initial setup (each tab alone is always "incomplete"). Instead every save calls `reload` (see
  below) immediately afterward and reports whether the *whole* config was valid enough to apply
  live via the response's `applied`/`applyError` fields — the same mechanism already used for a
  valid-but-unreachable API/database. `webServer.enabled`/`address` have no inputs in the config
  page at all (removed entirely, not just disabled) since changing them isn't possible through this
  API and always needs a process restart — see below.

  The Maps tab is not a config section at all but a first-class CRUD resource
  (`internal/webserver/maps.go`): `GET`/`POST /api/maps` (collection) and `GET`/`PUT`/`DELETE
  /api/maps/{id}` (one map), each independently addressable/mutable — adding, editing, or removing
  one map no longer means resubmitting every other configured map. `POST`/`PUT` validate the
  candidate map against the *rest* of the currently stored maps via the exported
  `config.Config.ValidateMaps()` (checking id/versions/interval/staticColumns, plus that no two
  maps share an `id` — nothing enforced that before `id` became a REST resource key) before
  persisting via `configdb.Store`'s per-map methods, then call `reload` the same way a config
  section save does. The status page's per-map "Sync" button posts to `POST
  /api/maps/{id}/sync` (`syncMapAPIHandler`, `id` from the native path value), wired to
  `runtime.runSyncMaps` with a single-ID set, to run that one map's sync immediately rather than
  waiting for its next `interval` tick — combined with `runtime.reload`'s `rt.wake` ping (see
  below), a map added via `POST /api/maps` starts syncing almost immediately rather than waiting
  out `runLoop`'s current sleep.

  A `/users` page (superuser-only) manages accounts via `GET`/`POST /api/users` and
  `GET`/`PUT`/`PATCH`/`DELETE /api/users/{id}`, and a `/security-log` page (superuser-only, backed
  by `GET /api/security-log`) shows the audit trail — see "Authentication & permissions" below for
  how every route in this package is now gated, and for the security log itself.
- **`internal/store`** — owns the MariaDB schema (`EnsureSchema`, idempotent
  `CREATE TABLE IF NOT EXISTS`) and writes (`UpsertGeoObjects`, one transaction per call, batched
  `INSERT ... ON DUPLICATE KEY UPDATE` keyed on `uuid`). Depends on `internal/tileserve` for the
  `GeoObject` type — the same struct flows from HTTP decode straight into SQL bind params with no
  intermediate model. Each map may also configure `staticColumns` (fixed extra column values
  written on every row synced from that map) and the database may enable `pruneMissing` (delete,
  within the same transaction, any previously-synced row for a map_uuid+version scope that the
  latest fetch no longer returned).

### Authentication & permissions

Every route the web server serves requires a logged-in account — there is no public route
anymore, including the status page. Accounts live in `configdb`'s `users`/`sessions` tables (see
above) and are managed at `/users` (superuser-only, backed by `/api/users`, `/api/users/{id}`).

Each account has seven independent boolean permissions (`configdb.Permissions`): `view_status`,
`trigger_sync`, `view_config`, and four config-editing permissions — `edit_config_api`,
`edit_config_database`, `edit_config_maps`, `edit_config_sso` — one per `/config` tab, enforced
independently at each tab's own save endpoint (see the `internal/webserver` bullet above). There
is deliberately no umbrella "edit config" flag. A separate `is_superuser` flag (not one of the
seven) gates `/users` only — it's orthogonal to the seven feature permissions, not a superset of
them, so a superuser account with none of them still can't see the status page or `/config`, and a
fully-permissioned non-superuser still can't reach `/users`.

Every security-relevant action also appends a row to `configdb`'s append-only `security_log` table
(`internal/configdb/securitylog.go`, `Store.LogSecurityEvent`/`Store.ListSecurityLog`) — local and
SSO logins (success and failure), logouts, account creation/update/deletion, every config section
save (`config_saved`, `section=api|database|sso`), and every map create/update/delete
(`map_created`/`map_updated`/`map_deleted`, distinct event types since maps are their own resource
— see the `internal/webserver` bullet above), each with a timestamp, event type, the acting
username (or attempted username, for a failed login), the request's `RemoteAddr`, and a short
free-form detail string (e.g. `section=api`, `target=<username>`, `map "town-centre" created`).
For every change event (a config save, a map create/update/delete, or a user create/update/delete),
that detail also records what actually changed —
built by the `diff*`/`changesDetail`/`grantedPermissions` helpers in
`internal/webserver/audit_diff.go`, which compare the before/after `config.Config`/
`config.MapTarget`/`configdb.SSOConfig`/`configdb.Permissions` field by field (e.g. `changed: baseUrl
"a"->"b", table changed`) — never in plaintext for a secret field (`API.Password`,
`Database.DSN`, SSO `ClientSecret`, account passwords), which are only ever reported as changed.
Writing a log entry is
best-effort — `internal/webserver/security_log.go`'s `logSecurityEvent` helper only logs a write
failure to stderr, never blocks or fails the action that triggered it. `GET /security-log`
(superuser-only, like `/users`) renders it via `GET /api/security-log?limit=N` (default 200, capped
at 1000, newest first) — there's no separate permission bit for it since the log can contain
account-management detail and remote addresses not meant for every logged-in user.

### SSO (OpenID Connect)

Optional, in addition to local username/password accounts (which are never disabled and remain
how the very first account is created at `/setup`). Configured on `/config`'s SSO tab
(`internal/webserver/sso.go`, `GET`/`PUT /api/config/sso` gated by `view_config`/
`edit_config_sso` respectively) and stored in `configdb`'s `sso_config` row — nothing about it is
cached in the running process: `internal/webserver/sso_login.go` re-resolves the provider's OIDC
discovery document (`github.com/coreos/go-oidc/v3/oidc`) and rebuilds the `oauth2.Config`
(`golang.org/x/oauth2`) fresh on every `/login/sso` and `/login/sso/callback` request, unlike
`runtime.reload`'s cached `{cfg, client, db}` — SSO logins are infrequent enough (interactive,
human-driven) that the extra discovery round-trip per attempt is cheap, and this avoids a second
live-reload path to maintain. The login page's unauthenticated `GET /api/sso/status` tells its
script whether to render an SSO button and with what label, without exposing provider details.

`GET /login/sso` starts the authorization-code-with-PKCE flow (state/nonce/verifier held in a
short-lived `gso_sso_flow` cookie, mirroring the session cookie's `HttpOnly`/`SameSite=Lax`/
conditional-`Secure` shape); `GET /login/sso/callback` verifies the ID token (audience, nonce) and
resolves the verified `(issuer, subject)` to a local user via
`configdb.Store.FindOrCreateSSOUser`: an existing link logs straight in; failing that, a local
account already named the claimed username (email, else `preferred_username`, else the subject
identifier) is linked to instead of duplicated; failing that, a new account is auto-provisioned
with the permission set configured on the SSO tab (`sso_config`'s `default_*` columns) and a
random, never-revealed password. An SSO-provisioned account is never a superuser automatically —
that stays a manual grant via `/users`, exactly like every other account-creation path.

`internal/webserver/auth.go` implements this: `requireUser`/`requirePermission`/`requireSuperuser`
are `http.HandlerFunc` wrappers, parameterized by `page bool` — `true` redirects an unauthenticated/
unauthorized browser request to `/login` (or 403s with a plain-text page), `false` writes a JSON
401/403 for the fetch-driven API. Sessions are a random token (in an `HttpOnly`, `SameSite=Lax`
cookie — `Secure` only when the request arrived over TLS, since the server is still meant to work
unencrypted on a trusted network) resolved via `configdb.Store.SessionUser`, which only ever sees
the token's SHA-256 hash.

While the `users` table is empty (a fresh install), `setupGate` (wrapping the whole mux) redirects
every request to a one-time `/setup` page instead; the account created there always gets every
permission plus superuser, since there's no one else yet to have granted anything more selectively.
Once at least one account exists, `/setup` redirects to `/login` forever after. `/api/me` returns
the logged-in user's username/permissions/superuser flag, and every page's shared inline script
(`accountNavJS` in `shared_script.go`) calls it to render the topbar's account/logout control and
hide nav links / disable form sections the user can't use — purely a UX nicety, since every actual
enforcement happens server-side per route.

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
"Sync" button (`POST /api/maps/{id}/sync`, called with a single-ID set) call. Both lock `syncMu`,
read the current `{cfg, client, db}` via `runtime.current()` — returning `errNotConfigured`
instead of calling `syncAll` if `db` is still nil — and call `syncAll`; the `syncMu` lock is what
stops a manual per-map sync from running concurrently with a scheduled tick against the same
database, which could otherwise race on `pruneMissing` deleting rows the other's insert just
wrote.

`runtime.reload(ctx)` is what every successful config/map save calls (wired in as a `reload`
closure passed to `startWebServer`/`webserver.New` — see above, and called directly by
`finishConfigSave` and `maps.go`'s create/update/delete handlers), and is also what `run` calls
once at startup to do the initial configure: it
loads via `rt.cfgDB.Load`, overlays `rt.webServer`, calls `Config.Validate`, and — only if that
succeeds — builds a fresh client (re-logging in, unless a token is configured) and database
connection (`newClient`/`openStore`, factored out of `run` so both it and `reload` share them),
swapping them into the runtime (closing the old database connection afterwards, nil-guarded for
the first successful reload) only if all of that succeeds — so an invalid edit or an unreachable
API/DB leaves the previous, still-working state (which may be the initial unconfigured state) in
place. On a successful swap it also pings `rt.wake` (a buffered `chan struct{}`, non-blocking send)
so `runLoop` (below) reacts immediately instead of finishing out whatever sleep it's already in.
This is how config changes made through the web UI (new/removed maps, per-map intervals,
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
if there's no such map (nothing configured, every map is one-shot, or nothing has synced yet). The
sleep itself (`select { ... case <-time.After(wait): case <-rt.wake: }`) is also woken early by
`rt.wake` (see `runtime.reload` above) — without it, a map added via `POST /api/maps` while the
loop was already sleeping out some other map's longer interval would sit unsynced until that
unrelated timer happened to fire, rather than starting on the next tick as intended.
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
