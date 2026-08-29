# Tile-Server-Sync-GO

Fetches geo objects from one or more [tileserve-go](https://github.com/Nils-witt/Tileserve-GO)
maps (at given versions) and writes them into a MariaDB database.

Talks to the API described in
[`openapi.yaml`](https://github.com/Nils-witt/Tileserve-GO/blob/main/internal/handler/openapi.yaml):

1. `POST /login` — obtain a JWT (unless a token is configured directly).
2. `GET /maps/{id}/version/{version}/geo-objects` — once per configured map/version pair.
3. Upserts each `GeoObject` into a MariaDB table (`geo_objects` by default, created automatically if missing; both the table and its column names are configurable).

## Setup

```sh
cp config.example.yaml config.yaml
```

Edit `config.yaml`:

```yaml
api:
  baseUrl: "http://localhost:8085"
  username: "admin"
  password: "changeme"
  # token: ""   # alternative to username/password

database:
  dsn: "user:password@tcp(127.0.0.1:3306)/tileserve?parseTime=true"
  # table: "geo_objects"        # optional, defaults to "geo_objects"
  # columns:                    # optional, maps GeoObject fields to columns
  #   uuid: "uuid"
  #   name: "name"
  #   # ... see config.example.yaml for every field and its default column

maps:
  - id: "<map-uuid>"
    versions: ["current"]
  - id: "<other-map-uuid>"
    versions: ["3", "4", "current"]
```

`version` may be a real numeric version, the literal `"current"`, or a
user-defined alias.

## Run

```sh
go run ./cmd/Tile-Server-Sync-GO -config config.yaml
```

Or build a binary:

```sh
go build -o Tile-Server-Sync-GO ./cmd/Tile-Server-Sync-GO
./Tile-Server-Sync-GO -config config.yaml
```

## Running as a Windows service

On Windows, Tile-Server-Sync-GO can install itself as a service instead of
running in a console window (most useful together with each map's own
`interval`, so it keeps syncing in the background across reboots):

```
Tile-Server-Sync-GO -service install -config C:\path\to\config.yaml
Tile-Server-Sync-GO -service start
```

`-service install` registers the service (as `Tile-Server-Sync-GO`, start type
Automatic) using the current executable's path and an absolute path to the
given `-config`, and registers an event log source so start/stop/error
messages show up in the Windows Event Log (Application log, source
`Tile-Server-Sync-GO`). Manage it afterwards with `-service start`,
`-service stop`, `-service uninstall`, or the regular `services.msc` /
`sc.exe` tools. `-service run` is used internally — it's what the SCM
invokes to actually start the process — and isn't normally run by hand.
This flag only works in binaries built for Windows (`GOOS=windows`); on
other platforms it fails with an explanatory error.

## Database schema

By default, geo objects are synced into a `geo_objects` table using the
column names shown below. Both the table name (`database.table`) and each
column name (`database.columns`) are configurable — see
[`config.example.yaml`](config.example.yaml). A field mapped to `""` is
skipped entirely, which lets sync target an existing table with a narrower
or differently-named schema; `uuid` is the exception, since it's the column
upserts match existing rows on and so may not be skipped.

The table is created automatically on first run if it doesn't already
exist:

```sql
CREATE TABLE IF NOT EXISTS geo_objects (
    uuid        CHAR(36)     NOT NULL PRIMARY KEY,
    map_uuid    CHAR(36)     NOT NULL,
    version     VARCHAR(64)  NOT NULL,
    name        VARCHAR(255) NOT NULL,
    external_id VARCHAR(255) NOT NULL DEFAULT '',
    latitude    DOUBLE       NOT NULL,
    longitude   DOUBLE       NOT NULL,
    street      VARCHAR(255) NOT NULL DEFAULT '',
    housenumber VARCHAR(64)  NOT NULL DEFAULT '',
    postcode    VARCHAR(32)  NOT NULL DEFAULT '',
    created_at  DATETIME     NULL,
    updated_at  DATETIME     NULL,
    created_by  VARCHAR(255) NOT NULL DEFAULT '',
    updated_by  VARCHAR(255) NOT NULL DEFAULT '',
    synced_at   DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_geo_objects_map_version (map_uuid, version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```

Rows are upserted by the `uuid` column, so re-running the sync updates
existing rows rather than duplicating them.

## Layout

- `cmd/Tile-Server-Sync-GO/main.go` — CLI entry point / orchestration.
- `cmd/Tile-Server-Sync-GO/service_windows.go` / `service_other.go` — Windows service install/start/stop/uninstall (`-service ...`); no-op stubs on non-Windows builds.
- `internal/config` — YAML config loading and validation.
- `internal/tileserve` — minimal tileserve-go API client (login + geo-objects fetch).
- `internal/store` — MariaDB schema management and upserts.
