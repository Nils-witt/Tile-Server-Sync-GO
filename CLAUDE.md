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
cd frontend && npm ci && npm run build && cd ..   # build the SPA into frontend/dist (embedded — see below)
go build -o Tile-Server-Sync-GO ./cmd/Tile-Server-Sync-GO   # build (needs frontend/dist to exist first — see below)
go run ./cmd/Tile-Server-Sync-GO -config config.yaml     # run (config.yaml is git-ignored; copy config.example.yaml)
                                        # config.yaml is now just a small bootstrap file (webServer +
                                        # configDb); api/database/maps (each with its own interval)
                                        # are entered via the SPA's /config page
go vet ./...                           # vet
golangci-lint run                      # lint (see .golangci.yml — extensive linter set enabled)
govulncheck ./...                      # vulnerability scan
```

Both `golangci-lint run` and `govulncheck ./...` run in the Husky `pre-commit` hook
(`.husky/pre-commit`) — expect them to run on every commit. Neither needs a real frontend build
first (see the `frontend` bullet below for why a bare `go build`/`go vet` still succeeds without
one); the hook doesn't build the frontend itself.

There is no Go test suite for most packages (`go test ./...` reports "no test files" for
everything except `internal/configdb` (GORM-backed store/migration tests), `internal/syncer`
(`scheduleTick` table tests) and `internal/webserver` (SSO bearer helpers)). The root
`package.json`/`npm` setup exists only to drive Husky; it is not a Node project — the actual
frontend lives in `frontend/` as its own npm project (`frontend/package.json`), see below. In
`frontend/`, `npm run build` runs `tsc -b && vite build`; `npx oxlint` lints it (also warns-only in
CI, doesn't fail the build).

## Architecture

The module path is `github.com/Nils-witt/Tile-Server-Sync-GO` (matches the repo URL). `cmd/` holds
only entry-point code (flags, `-service` dispatch, log fan-out, wiring); the sync engine lives in
`internal/syncer` (see below).

Config now comes from two places, wired together in `cmd/Tile-Server-Sync-GO/main.go`'s `run()`:

```
config.LoadBootstrap()  →  configdb.Store  →  tileserve.Client  →  store.Store
 (YAML → Bootstrap)        (SQLite → Config)   (HTTP API client)   (MariaDB upsert)
```

`Bootstrap` (`webServer` + `configDb` + `oidc`) comes from the small YAML file at `-config`; every
other field of `config.Config` (`api`, `database`, `maps`) lives in a SQLite database at
`Bootstrap.ConfigDB` instead, edited through the `/config` web UI. `webServer` stays
file/CLI-driven — see "why webServer isn't in SQLite" below — and so does `oidc` (see "SSO" below).

- **`internal/config`** — defines `Config` (`API`, `Database`, `[]MapTarget`, `WebServer`) and its
  validation/defaulting (`Validate`, exported since callers other than `Parse` now assemble a
  `*Config` themselves — see `configdb.Store.Load`/`syncer.Engine.Reload`). `Load`/`Parse` (YAML bytes →
  validated `*Config`) still exist as a general-purpose YAML entry point, but nothing in this
  repo calls them anymore — the web config editor now reads/writes structured JSON only (its
  `raw` YAML view was removed; see the `internal/webserver` bullet below) and the bootstrap file
  goes through the separate `LoadBootstrap`/`Bootstrap` type instead. `Bootstrap`
  (`bootstrap.go`) is the separate, minimal file-backed type — `LoadBootstrap` reads it, applies
  the same `webServer.enabled && address == ""` defaulting as `Validate` (shared via
  `WebServer.applyDefault`), defaults/validates `SSO` (`sso.go`: blank `scopes`/`buttonLabel` get
  defaults; `issuerUrl`/`clientId` are required when `enabled`, failing startup otherwise), and
  resolves `ConfigDB` (default `"config.db"`) relative to the bootstrap file's own directory. `Config.Maps` is a list of `{id, versions[], interval,
  staticColumns, disabled}` entries; a version string may be a real numeric version, the literal
  `"current"`, or a user-defined alias (see `PUT /maps/{id}/aliases/{alias}` in the tileserve-go
  API). Each map's own optional `interval` (a Go duration string, parsed by
  `MapTarget.validateInterval` and read back via `MapTarget.SyncInterval()`) controls how often
  *that* map re-syncs — there is no longer a global interval; a map with no `interval` syncs once
  and isn't automatically repeated (see `RunLoop` below). `disabled`, if true, opts a map out of
  *automatic* syncing only — `scheduleTick` (see `RunLoop` below) never considers it due, and
  `Engine.RunSync`'s run-once path skips it too — while leaving its stored config untouched and
  still letting it be synced on demand via its own "Sync" button/`POST /api/maps/{id}/sync`, which
  passes its ID explicitly rather than relying on scheduling (see `Engine.SyncMap`).
  `Config.Maps` requiring at least one entry was dropped from `Validate` — an empty `maps` list is
  a valid (if idle) config now, not a validation error, so removing the last map (or none having
  been added yet on a fresh install) no longer blocks saving/applying the rest of the config.
  Validation requires either `api.token` or both `api.username`/`api.password`.
- **`internal/configdb`** — the new SQLite-backed store for everything in `Config` except
  `WebServer`, as a relational schema (not a serialized blob): a singleton `config_scalar` row for
  `api`/`database`'s scalar fields, plus `database_columns`, `maps` (which also holds each map's
  own `interval` column), `map_versions`, and `map_static_columns` tables (ordered by a
  `sort_order` column, since `syncAll` iterates maps/versions in configured order). `Store.Load`
  assembles a `*config.Config`
  with `WebServer` left zero-valued — callers always overlay the bootstrap value before using or
  validating it. `Store.Save` replaces `database_columns`/`maps` wholesale inside one transaction
  (delete-then-reinsert, not diffed) and never reads or writes `WebServer`. Backed by
  [GORM](https://gorm.io) (`gorm.io/gorm`) over `github.com/glebarez/sqlite` — a pure-Go, cgo-free
  SQLite driver/dialector (wrapping `modernc.org/sqlite`), to keep GoReleaser's cross-compiled and
  Windows service builds working the same way the old hand-written-SQL version did. `Open`
  (`configdb.go`) still pins the connection pool to one connection (`SetMaxOpenConns(1)`) so its
  `PRAGMA foreign_keys = ON` reliably applies — SQLite pragmas are per-connection, and
  `database/sql`'s pool would otherwise silently hand out a fresh, pragma-less one — and still runs
  its own schema/migration step (`schema.go`'s `migrate`) rather than relying purely on GORM's
  `AutoMigrate`: the two tables that need a real `ON DELETE CASCADE` foreign key
  (`map_versions`/`map_static_columns` → `maps`) are created
  via raw `CREATE TABLE IF NOT EXISTS` SQL with every Go-side association tagged `constraint:-` to
  keep GORM from ever trying to manage that FK itself. That's not just style: SQLite can only
  declare a foreign key at `CREATE TABLE` time (no `ALTER TABLE ADD CONSTRAINT`), so letting GORM
  reconcile one on a database created by the old raw-SQL schema makes it recreate the table, and
  `glebarez/sqlite`'s recreate-table DDL parser (as of v1.11.0) corrupts the column list when a
  table also has a composite inline `UNIQUE(a, b)` constraint next to a column literally named
  `column` (a SQL reserved word) — exactly `map_static_columns`' shape.
  `migrate` works around it by first rewriting (`dropLegacyInlineUniqueConstraint`, with an
  explicit column list rather than `SELECT *`) any such table still on the old inline-constraint
  shape into one with a separate named unique index instead, before `AutoMigrate` ever touches it.
  The underlying parser bug is broader: it treats a **tab** as a quote character, so any stored
  `CREATE TABLE` text containing a tab gets misread. Every hand-written DDL statement is therefore
  passed through `flatDDL` (whitespace collapsed to single spaces) before it's executed. `Open`
  runs `migrate` *before* enabling `PRAGMA foreign_keys`, because recreating a parent table
  (`maps`) with enforcement on would cascade-delete its children. The pragma is a no-op inside a transaction, so
  it can't be toggled per migration step.
  `AutoMigrate` itself is still what backfills a missing column/index on a database created by an
  older version of this schema (e.g. the old plain-`INTEGER` boolean/`TEXT` timestamp columns) —
  see `internal/configdb/legacy_migration_test.go` for a regression test that seeds a database
  using the old hand-written schema and asserts `Open` migrates it (including cascade deletes)
  without losing data. Unrelated to `internal/store` (MariaDB geo-object storage); no shared code.
  Unlike `api`/`database`, (`internal/configdb/maps.go`) `maps` get real per-row CRUD methods
  instead of only going through the whole-graph `Load`/`Save` above: `ListMaps`/`GetMap`/
  `CreateMap`/`UpdateMap`/`DeleteMap` (returning `ErrMapNotFound`/`ErrMapIDTaken`) back the
  `/api/maps` REST family in
  `internal/webserver/maps.go` — each map is edited independently rather than resubmitting the
  whole `maps` array. `map_id` has a unique index (`mapRecord`'s `uniqueIndex` struct tag in
  `schema.go`, added automatically by `AutoMigrate` — nothing enforced this before it became a
  per-map resource key) so `{id}`-addressed lookups are unambiguous. There are **no user tables**:
  every login is SSO and every permission comes from the token's groups (see "SSO" below), so
  `migrate`'s first step, `removeLocalUserTables`, drops the `users`/`sessions`/`sso_identities`
  tables an older database still has. The SSO *settings* live in the bootstrap file, not here
  (older databases may still contain an unused `sso_config` table, which nothing drops). Finally it holds `security_log`
  (`internal/configdb/securitylog.go`), an append-only audit trail — see the security log bullet
  under "Authentication & permissions" below.
- **`internal/tileserve`** — minimal synchronous HTTP client for tileserve-go. `Login()`
  exchanges username/password for a bearer token; `SetToken()` bypasses login when a token is
  already known. `GeoObjects(mapID, version)` fetches and JSON-decodes one map/version's objects
  (`GeoObject` struct mirrors the API's schema exactly — field-for-field, including JSON tags).
- **`frontend`** — the UI: a Vite + React + TypeScript SPA (client-routed with `react-router-dom`),
  entirely separate from the root `package.json`/Husky setup (its own `frontend/package.json`,
  `node_modules`, lockfile). Routes: `/` (status), `/config/{api,database,maps}` (tabs, each its
  own route rather than the old hash-fragment tab switcher), `/security-log`, `/login`,
  `/login/sso/callback` — all in `frontend/src/pages`. `frontend/src/auth/AuthContext.tsx`
  fetches `GET /api/sso/status` (to set up the browser-side OIDC client, see "SSO" below) and then
  `GET /api/me` once on load; `App.tsx`'s `AuthGate` client-side-redirects to `/login`/`/` based
  on that plus the current route, instead of the server ever 302ing a page request. Per-route permission checks
  (`frontend/src/auth/guards.tsx`'s `RequirePermission`/`RequireSuperuser`) render a plain
  "forbidden" message in place of a page the logged-in user lacks the permission for — a UX nicety
  only; every actual enforcement is still the server's `requirePermission`/`requireSuperuser` on each
  API call. `frontend/src/api/client.ts`'s `apiFetch` attaches the SSO access token (if any) as
  `Authorization: Bearer` to every call and retries once after a refresh-token renewal on a 401.
  `frontend/src/api/client.ts` + `types.ts` are the one place that knows every JSON DTO
  shape `internal/webserver` sends/expects — keep them in sync by hand when a Go DTO's fields change,
  there's no code generation between them.

  Building it (`npm ci && npm run build` inside `frontend/`, or `npm run dev` for a live-reloading
  dev server that proxies `/api` to a separately-running backend — see
  `vite.config.ts`'s `VITE_BACKEND` env var, default `http://localhost:8080`) produces
  `frontend/dist`, embedded into the Go binary by `frontend/embed.go`'s `//go:embed all:dist`
  (`frontend.Dist`) and served by `internal/webserver/spa.go` — see that bullet below. `frontend/dist`
  is git-ignored *except* for a force-added `frontend/dist/index.html` placeholder (a plain "run npm
  run build" message) kept just so `go:embed`, which needs at least one real file to match at compile
  time, doesn't fail a bare `go build`/`go vet`/`golangci-lint run` for someone who hasn't run the
  frontend build yet — CI (`.github/workflows/ci.yml`'s `frontend` job) and GoReleaser
  (`.goreleaser.yaml`'s `before.hooks`) always build the real thing first. Because that placeholder is
  a tracked file, running a real `npm run build` locally leaves it showing as modified in `git
  status` — expected, don't commit that back unless you're deliberately updating the placeholder
  itself.
- **`internal/webserver`** — a JSON API only (`internal/webserver/*.go`, no HTML templates or
  server-rendered pages of any kind anymore) plus `spa.go`'s static-file server for the `frontend`
  bullet's build output, all registered in `webserver.go` using Go 1.22+ `http.ServeMux`'s native
  `"METHOD /path/{param}"` patterns (a wrong method on a registered path gets an automatic 405 with
  an `Allow` header from the mux itself, not a per-handler check) — every route's required permission
  is declared right at its `mux.HandleFunc` call, not hidden in a handler. `spa.go`'s `spaHandler` is
  registered as `"GET /"` (a *method-qualified* catch-all, not a bare `"/"`) last, so every
  `/api/...` pattern above still wins for the paths it owns: a bare `"/"` would match every
  unmatched *method* on every other path too, silently suppressing the mux's automatic 405 for the
  whole `/api/...` family. Any non-`/api` route registered later must therefore be method-qualified
  too: `net/http.ServeMux.HandleFunc` *panics* at registration time on two patterns where neither
  dominates the other on both the method and path dimensions (a bare pattern is broader on method
  but narrower on path than `"GET /"`, which is the reverse) — see `webserver.go`'s comment at the
  `spaHandler` registration for the exact rule. There are currently no such routes:
  `/login/sso/callback` is a client-side route served by `spaHandler` like any other.
  `spaHandler` itself: a request naming a real file under the embedded `frontend/dist` (e.g.
  `/assets/index-<hash>.js`, long-cached since Vite content-hashes those names) is served as that
  file; anything else — `/`, `/config/maps`, a hard-reload on any client-side route — falls back to
  `index.html` so `react-router` (running client-side) can render it.

  `GET /api/config` (`internal/webserver/config.go`) returns the whole stored config as a bundle
  (`{config}`, secrets redacted — see `redactSecrets`), reading/writing a `*configdb.Store` instead
  of a file path; an empty/unconfigured database is not an error, so the SPA's structured form
  always has something to render (blank on a fresh install). The API and Database tabs are each
  their own sub-resource — `GET`/`PUT /api/config/api` and `GET`/`PUT /api/config/database`. A `PUT` loads the currently stored
  config, replaces just that one section, and saves — deliberately *not* gated on `Config.Validate()`
  passing for the whole merged config (see `finishConfigSave`'s doc comment in `config.go`), since
  that would make it impossible to ever save a single tab during initial setup (each tab alone is
  always "incomplete"). Instead every save calls `reload` (see below) immediately afterward and
  reports whether the *whole* config was valid enough to apply live via the response's
  `applied`/`applyError` fields — the same mechanism already used for a valid-but-unreachable
  API/database. `webServer.enabled`/`address` have no inputs in the config page at all (removed
  entirely, not just disabled) since changing them isn't possible through this API and always needs
  a process restart — see below.

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
  `Engine.SyncMap` (a single-ID `runSyncMaps`), to run that one map's sync immediately rather than
  waiting for its next `interval` tick — combined with `Engine.Reload`'s `wake` ping (see
  below), a map added via `POST /api/maps` starts syncing almost immediately rather than waiting
  out `RunLoop`'s current sleep.

  `GET /api/status` (`status_api.go`) is the status page's data source — a JSON version of
  `status.Recorder.Snapshot()` (timestamps as RFC3339 strings), polled by the SPA every 10s to match
  the old server-rendered page's `<meta http-equiv="refresh" content="10">`. `GET /api/version`
  (also `status_api.go`, deliberately unauthenticated since the footer it feeds is shown on
  `/login` too) replaces the old build-time-spliced `{{FOOTER}}` template marker with a
  runtime call.

  The security log (`GET /api/security-log`, superuser only) is otherwise unchanged from
  before the SPA rewrite — see "Authentication & permissions" below for how every route in this
  package is gated, and for the security log itself.
- **`internal/store`** — owns the MariaDB schema (`EnsureSchema`, idempotent
  `CREATE TABLE IF NOT EXISTS`) and writes (`UpsertGeoObjects`, one transaction per call, batched
  `INSERT ... ON DUPLICATE KEY UPDATE` keyed on `uuid`). Depends on `internal/tileserve` for the
  `GeoObject` type — the same struct flows from HTTP decode straight into SQL bind params with no
  intermediate model. Each map may also configure `staticColumns` (fixed extra column values
  written on every row synced from that map) and the database may enable `pruneMissing` (delete,
  within the same transaction, any previously-synced row for a map_uuid+version scope that the
  latest fetch no longer returned). Separately, `overlays.go`'s `CreateMapOverlays`/
  `UpdateMapOverlays`/`DeleteMapOverlays` (all no-ops unless `Database.SyncOverlays` is enabled) keep
  one row per configured map/version in sync in `map_src_overlays`, a table belonging entirely to a
  different, external application (EDP) that also lives in the same MariaDB database — unlike
  `geo_objects`, its schema is never created here (`EnsureSchema` doesn't touch it) since its
  EDP-specific columns (`LIZENZ`, `KONFIG`, `OFFLINE_CACHE_*`, ...) are out of scope for this tool;
  a deployment enabling `SyncOverlays` is expected to already have the table. Each row's `SOURCE`
  column (built from `API.BaseURL` plus the map's `id`/version, e.g.
  `"<baseUrl>/maps/<id>/version/<version>/"`) doubles as the natural per-map/version key used to
  find an existing row to update or delete, since `map_src_overlays` has no column of its own
  referencing this tool's map/version identity; `NAME`/`CACHE_LOKAL` come from the map's `Name`
  (`config.MapTarget.Name`, a human-readable label distinct from its `id` — required for a map to be
  synced into EDP, but not for the map itself: a map with `SyncOverlays` enabled and no `Name` is
  skipped, logged, rather than failing the sync). This is wired into map create/update/delete
  through the same `webserver.Engine` interface `DeleteMapObjects` already uses (see
  `internal/syncer/engine.go`'s `CreateMapOverlays`/`UpdateMapOverlays`/`DeleteMapOverlays` and
  `webserver/maps.go`'s handlers) — a failure here is reported back via each response's
  `overlayError` field but never fails the request, since the map change itself already succeeded.

### Authentication & permissions

Every API route the web server serves requires a logged-in account (an SSO bearer token — see
below; there are no local passwords or session cookies) — there is no public route
anymore, including status (`/api/status`). `internal/webserver/spa.go`'s static-file serving is the
one exception (see the `internal/webserver` bullet above): the SPA shell itself is always served
regardless of login, since it's the SPA's own `AuthGate` (see the `frontend` bullet above) that
now does what server-side page redirects used to. There are no local accounts: a request's
identity (`principal` in `auth.go`) is built from its access token alone, and its permissions are
`oidc.defaultPermissions` plus what the token's groups grant via `oidc.groupPermissions` (see "SSO"
below).

There are six independent boolean permissions (`config.SSOPermissions`): `viewStatus`,
`triggerSync`, `viewConfig`, and three config-editing permissions — `editConfigApi`,
`editConfigDatabase`, `editConfigMaps` — one per `/config` tab, enforced
independently at each tab's own save endpoint (see the `internal/webserver` bullet above). There
is deliberately no umbrella "edit config" flag. A separate `superuser` grant (not one of the
six) gates the security log only — it's orthogonal to the six feature permissions, not a superset
of them, so a superuser with none of them still can't see the status page or `/config`, and a
fully-permissioned non-superuser still can't reach `/security-log`.

Every security-relevant action also appends a row to `configdb`'s append-only `security_log` table
(`internal/configdb/securitylog.go`, `Store.LogSecurityEvent`/`Store.ListSecurityLog`) — SSO logins
(success, with the permissions the groups granted, and failure — see "SSO" below for when those
are recorded), every config section
save (`config_saved`, `section=api|database`), and every map create/update/delete
(`map_created`/`map_updated`/`map_deleted`, distinct event types since maps are their own resource
— see the `internal/webserver` bullet above), each with a timestamp, event type, the acting
username (or attempted username, for a failed login), the request's `RemoteAddr`, and a short
free-form detail string (e.g. `section=api`, `map "town-centre" created`).
For every change event (a config save or a map create/update/delete),
that detail also records what actually changed —
built by the `diff*`/`changesDetail`/`grantedPermissions` helpers in
`internal/webserver/audit_diff.go`, which compare the before/after `config.Config`/
`config.MapTarget` field by field (e.g. `changed: baseUrl
"a"->"b", table changed`) — never in plaintext for a secret field (`API.Password`,
`Database.DSN`), which are only ever reported as changed.
Writing a log entry is
best-effort — `internal/webserver/security_log.go`'s `logSecurityEvent` helper only logs a write
failure to stderr, never blocks or fails the action that triggered it. `GET /security-log`
(superuser-only) renders it via `GET /api/security-log?limit=N` (default 200, capped
at 1000, newest first) — there's no separate permission bit for it since the log can contain
remote addresses and permission detail not meant for every logged-in user.

### SSO (OpenID Connect)

The web server's only login method — there are no local username/password accounts any more, and
`LoadBootstrap` fails startup if `webServer.enabled` is true while `oidc.enabled` is false.
Configured only in the bootstrap file's `oidc:`
section (`config.SSO` — `enabled`, `issuerUrl`, `clientId`, `scopes`, `buttonLabel`,
`defaultPermissions`, `groupsClaim`, `groupPermissions`; see `config.example.yaml`), passed from `run` through `startWebServer` into
`webserver.New`, and fixed for the process's lifetime — changing it needs a restart, and there is no
SSO tab or `edit_config_sso` permission in the web UI. There is no client secret.

The server takes no part in the login itself. The SPA is a **public OIDC client**:
`frontend/src/auth/oidc.ts` wraps `oidc-client-ts`'s `UserManager`, built from the unauthenticated
`GET /api/sso/status` (which returns `issuerUrl`/`clientId`/`scopes` while SSO is enabled — none
secret). It runs authorization code + PKCE directly against the provider, with
`<origin>/login/sso/callback` (`frontend/src/pages/SsoCallbackPage.tsx`) as the redirect URI. The
provider must therefore register the client as public/SPA and allow this origin for CORS. Tokens
live in `sessionStorage`. Renewal is deliberately **refresh-token only**: oidc-client-ts's
`automaticSilentRenew` is off because, without a refresh token, it falls back to a hidden iframe,
which providers sending `X-Frame-Options: deny` (e.g. Authentik) refuse. `oidc.ts`'s
`renewAccessToken` runs on `accessTokenExpiring`, on an already-expired token, and on an API 401.
Concurrent callers share one in-flight refresh, since rotated refresh tokens are single-use. It uses
the refresh token if there is one (the `offline_access` scope, which must be in `oidc.scopes`);
otherwise, or if the refresh fails, it drops the user. `AuthContext` listens for that
(`onSsoSessionEnded`) and clears `me`, so `AuthGate` sends the browser back to `/login?next=...`.

The provider's **access token (a JWT)** is then the API credential: `client.ts` sends it as
`Authorization: Bearer` on every `/api/...` call, and `requireUser` (`internal/webserver/auth.go`)
requires that header — a missing or invalid one fails with 401. `internal/webserver/sso_bearer.go`'s
`ssoBearerUser` verifies the token with a cached `go-oidc` verifier per
issuer (`ssoVerifierCache`, built in `New` and handed to the auth wrappers via `authenticator`;
the JWKS is cached by go-oidc's `RemoteKeySet`), checking signature, issuer and expiry, with
`SkipClientIDCheck` because access-token `aud` is provider-specific. It then binds the token to
the configured client itself (`tokenIssuedTo`: `azp` or `aud` must name the client ID).

Permissions come solely from `oidc.defaultPermissions` (granted to every signed-in user; a plain
`SSOPermissions` with no superuser field) plus `oidc.groupPermissions` (group name →
`config.SSOGroupGrant`: the six permissions inline plus an optional `superuser`); nothing about
users is stored, and there is no `/users` page. On every bearer request, `ssoBearerUser` reads the token's
`oidc.groupsClaim` claim (default `groups`, a string array or a single string; see
`groupsFromClaim`) and ORs every matching group's grants together (`groupGrants`/
`unionPermissions` in `sso_bearer.go`) on top of `defaultPermissions` into the request's
`principal`, named by `usernameFromClaims` (`preferred_username`, else email, else the subject).
Superuser can only come from a group. Since nothing is cached, removing someone from a group at
the provider revokes its grants on their next request.

Since there's no server-side login step, `sso_login` is recorded by every `GET /api/me`. The SPA calls it once right after the provider callback and once per page
load, which avoids logging every API request. A rejected bearer token logs `sso_login_failed`,
except plain expiry (routine for an idle tab), which only goes to stderr. Logout is client-side:
the token is dropped and, if the provider advertises an `end_session_endpoint`, the
browser is redirected to it.

`internal/webserver/auth.go`'s `requireUser`/`requirePermission`/`requireSuperuser` (all taking
the shared `*authenticator`) are `http.HandlerFunc` wrappers that always write a JSON 401/403 —
every route here is JSON-only now that the SPA owns all page routing; see the `frontend` bullet
above. `/api/me` returns the logged-in user's
username/permissions/superuser flag; `frontend/src/components/TopBar.tsx` is what the old shared
`accountNavJS` inline script became — it renders the topbar's account/logout control and hides nav
links the user can't use, purely a UX nicety, since every actual enforcement happens server-side
per route.

`cmd/Tile-Server-Sync-GO/main.go`'s `run(ctx, configPath)` orchestrates the whole flow and is the place to look first when
tracing behavior end-to-end: load the bootstrap file → open `configdb` → attempt an initial
`Reload` (see below) → hand off to `internal/syncer/sync.go`'s `syncAll(ctx, maps, client, db, rec)`
for each map × version in `maps` (some subset of the configured maps — see `RunLoop` below): fetch, overwrite each object's
`Version` with the configured version string (so an alias like `"current"` is what lands in the
database, not whatever concrete version the API resolved it to), then upsert (and prune, if
enabled), logging counts as it goes.

The client and database connection aren't held directly by `run`, though — they're wrapped in a
`*syncer.Engine` (`internal/syncer/engine.go`), a mutex-guarded holder of the current `{cfg, client, db}` triple (which
starts out all-nil — see "starting unconfigured" below), plus a second mutex (`syncMu`) dedicated
to serializing syncs, and three fields fixed for the process's lifetime: `cfgDB` (the
`*configdb.Store`) and `webServer` (the bootstrap-sourced `config.WebServer`, overlaid onto every
loaded `Config` before it's validated or used), plus the `*status.Recorder` every sync reports to.
`Engine.RunSync(ctx)` syncs every configured
map and is what the run-once path in `run` (no map has an `interval`, `webServer.enabled` is
false) calls at startup; the unexported `runSyncMaps(ctx, ids)` syncs just the maps whose ID is in
`ids` and is what both `RunLoop`'s per-map scheduler (see below) and, via `Engine.SyncMap`, the
status page's per-map "Sync" button (`POST /api/maps/{id}/sync`) call. Both lock `syncMu`,
read the current `{cfg, client, db}` via `Engine.Current()` — returning `ErrNotConfigured`
instead of calling `syncAll` if `db` is still nil — and call `syncAll`; the `syncMu` lock is what
stops a manual per-map sync from running concurrently with a scheduled tick against the same
database, which could otherwise race on `pruneMissing` deleting rows the other's insert just
wrote.

`Engine.Reload(ctx)` is what every successful config/map save calls (the engine is passed to
`webserver.New` via `webserver.Options.Engine`, typed as the consumer-side `webserver.Engine`
interface so `internal/webserver` never imports `internal/syncer` — see above, and called directly by
`finishConfigSave` and `maps.go`'s create/update/delete handlers), and is also what `run` calls
once at startup to do the initial configure: it
loads via `e.cfgDB.Load`, overlays `e.webServer`, calls `Config.Validate`, and — only if that
succeeds — builds a fresh client (re-logging in, unless a token is configured) and database
connection (`newClient`/`openStore`),
swapping them into the engine (closing the old database connection afterwards, nil-guarded for
the first successful reload) only if all of that succeeds — so an invalid edit or an unreachable
API/DB leaves the previous, still-working state (which may be the initial unconfigured state) in
place. On a successful swap it also pings `e.wake` (a buffered `chan struct{}`, non-blocking send)
so `RunLoop` (below) reacts immediately instead of finishing out whatever sleep it's already in.
This is how config changes made through the web UI (new/removed maps, per-map intervals,
credentials, DB settings) take effect without a process restart. `webServer.enabled`/`address` are
the one exception: changing those still needs a restart, since the server a reload request arrives on
can't safely restart itself mid-request — this is also why they live in the bootstrap file rather
than `configdb` at all: `configdb`-backed settings are exactly the ones `Reload` can apply live,
and `webServer` structurally can't be.

**Starting unconfigured**: since there's no automatic migration of pre-SQLite `config.yaml`
content, a fresh install's `configdb` is empty, and `run`'s initial `Reload` call fails validation
(missing `api.baseUrl` etc.) — expected, not a bug. If `webServer.enabled` is false at that point,
`run` fails hard (there'd be no way to fix it otherwise, same as an invalid `config.yaml` always
failed hard). If `webServer.enabled` is true, `run` logs the error and continues: the web server
starts regardless, `GET /config` renders an all-blank structured form (see the `internal/webserver`
bullet above), and the process falls into `RunLoop` regardless of whether any configured map has a
usable `interval` yet.

`Engine.RunLoop` (`internal/syncer/schedule.go`) no longer runs one global interval loop — since `Interval` now lives per-map
(`config.MapTarget`), each map is scheduled independently. It tracks an in-memory
`lastSync map[string]time.Time` (map ID → last sync start), rebuilt from scratch on every process
start (nothing about scheduling state is persisted). Each tick: if the engine has no database yet (unconfigured), it
just logs a wait message and falls back to `pollInterval` (5s), same as before; otherwise
`scheduleTick` computes the set of currently-due map IDs from the latest `e.Current()` config — a map
with no `lastSync` entry yet is always due once (covers both startup and a map added via a live
reload), and after that a map with a positive `Interval` is due again once that much time has
passed, while a map with no `Interval` is never due again automatically. Due maps (if any) are
synced together via `e.runSyncMaps`, and `tick` folds their next-due times into how long to sleep: the shortest
remaining time until any already-synced, positive-`Interval` map next comes due, or `pollInterval`
if there's no such map (nothing configured, every map is one-shot, or nothing has synced yet). The
sleep itself (`select { ... case <-time.After(wait): case <-e.wake: }`) is also woken early by
`e.wake` (see `Engine.Reload` above) — without it, a map added via `POST /api/maps` while the
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

`-service install|uninstall|start|stop|run` (handled by `handleServiceCommand` in `cmd/Tile-Server-Sync-GO/main.go`)
lets the binary register/manage itself as a Windows service instead of running in a console
session — pairs naturally with maps that have their own `interval` set, for an unattended
long-running sync. The real
implementation lives in `cmd/Tile-Server-Sync-GO/service_windows.go` (build-tagged `windows`), using
`golang.org/x/sys/windows/svc`/`svc/mgr`/`svc/eventlog`; `install` records the current exe path
plus an absolute `-config` path and `-service run` as the service's launch command, and registers
an event log source. `cmd/Tile-Server-Sync-GO/service_other.go` (build-tagged `!windows`) provides stub implementations
that return an explanatory error, so `go vet`/`golangci-lint`/builds stay green on
linux/darwin. `cmd/Tile-Server-Sync-GO/main.go` also calls `isWindowsService()` at startup (true only when actually built
for and running under Windows) as a fallback to route into service mode even without `-service
run` on the command line. Neither service file changes `run` or anything in `internal/syncer` — the service
wrapper just runs `run(ctx, configPath)` in a goroutine and cancels its context on a Stop/Shutdown
SCM request.

## Linting notes

`.golangci.yml` enables a deliberately broad linter set (correctness, style, complexity,
performance, security, SQL-resource-leak, and logging checks) and disables a curated set of
noisy/opinionated ones — see the `disable:` block's inline comments for the reasoning on each.
Notable enforced limits: `gocyclo` min-complexity 13, `funlen` 120 lines / 80 statements,
`dupl` threshold 100 tokens. Formatting uses `gofmt` + `gofumpt` (with `extra.group-params`).
