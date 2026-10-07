package runner

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/config"
	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/store"
)

// databaseTestTimeout bounds TestDatabase, so an unreachable host (whose
// packets are silently dropped) fails the test instead of hanging the
// request.
const databaseTestTimeout = 10 * time.Second

// Config returns a copy of the stored config with its secrets
// (API.Password, Database.Password) blanked: stored secrets never leave the
// runner, and SaveAPI/SaveDatabase treat a blank secret as "unchanged". An
// unconfigured install returns an all-zero config, not an error.
func (r *Runner) Config() config.Config {
	cfg := r.storedCopy()
	redactSecrets(cfg)

	return *cfg
}

// SaveAPI replaces the stored API section with api (a blank Password keeps
// the stored one), persists it, records config_saved in the security log,
// and tries to apply the whole config live. It returns the new stored config
// (redacted, like Config).
//
// The save is deliberately not gated on the whole config validating: during
// initial setup each section is saved on its own, so the config is
// necessarily incomplete until every section is filled in. An
// incomplete-but-persisted config is simply not applied, reported via
// ChangeResult.ApplyErr — the same outcome as a valid config whose
// API/database is unreachable.
func (r *Runner) SaveAPI(ctx context.Context, actor Actor, api config.API) (config.Config, ChangeResult, error) {
	return r.saveSection(ctx, actor, "api", func(cfg *config.Config) []string {
		if api.Password == "" {
			api.Password = cfg.API.Password
		}

		changes := diffAPI(cfg.API, api)
		cfg.API = api

		return changes
	})
}

// SaveDatabase is the Database analogue of SaveAPI.
func (r *Runner) SaveDatabase(
	ctx context.Context, actor Actor, db config.Database,
) (config.Config, ChangeResult, error) {
	return r.saveSection(ctx, actor, "database", func(cfg *config.Config) []string {
		if db.Password == "" {
			db.Password = cfg.Database.Password
		}

		changes := diffDatabase(cfg.Database, db)
		cfg.Database = db

		return changes
	})
}

// TestDatabase tries to connect to db without saving or applying it, so the
// config page can check connection settings first. A blank Password means
// the stored one, as in SaveDatabase. Invalid settings are reported wrapped
// in ErrInvalid; any other error is the connection attempt's own. Every
// attempt is recorded in the security log, since it may send the stored
// password to a newly entered host.
func (r *Runner) TestDatabase(ctx context.Context, actor Actor, db config.Database) error {
	if db.Password == "" {
		db.Password = r.storedCopy().Database.Password
	}

	if err := db.ValidateConnection(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, databaseTestTimeout)
	defer cancel()

	err := store.Ping(pingCtx, db)

	result := "ok"
	if err != nil {
		result = "failed"
	}

	r.LogSecurityEvent(ctx, actor, "database_tested",
		fmt.Sprintf("host=%q user=%q name=%q tls=%v tlsSkipVerify=%v customCa=%v result=%s",
			db.Host, db.User, db.Name, db.TLS, db.TLSSkipVerify, db.TLSCACert != "", result))

	return err
}

// saveSection is the shared body of SaveAPI/SaveDatabase: merge (which
// overwrites one section of a copy of the stored config and reports what it
// changed) is applied, the result persisted and made the new stored config,
// then applied live.
func (r *Runner) saveSection(
	ctx context.Context, actor Actor, section string, merge func(cfg *config.Config) []string,
) (config.Config, ChangeResult, error) {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	cfg := r.storedCopy()
	changes := merge(cfg)

	if err := r.cfgDB.Save(ctx, cfg); err != nil {
		return config.Config{}, ChangeResult{}, err
	}

	r.setStored(cfg)
	r.LogSecurityEvent(ctx, actor, "config_saved", "section="+section+"; "+changesDetail(changes))

	res := ChangeResult{ApplyErr: r.apply(ctx)}

	out := cloneConfig(cfg)
	redactSecrets(out)

	return *out, res, nil
}

func redactSecrets(cfg *config.Config) {
	cfg.API.Password = ""
	cfg.Database.Password = ""
}

// cloneConfig deep-copies cfg's maps and slices, so the copy can be handed
// out or modified without racing whoever else holds the original.
func cloneConfig(cfg *config.Config) *config.Config {
	out := *cfg
	out.Database.Columns = maps.Clone(cfg.Database.Columns)
	out.Maps = make([]config.MapTarget, len(cfg.Maps))

	for i, m := range cfg.Maps {
		out.Maps[i] = cloneMap(m)
	}

	return &out
}

func cloneMap(m config.MapTarget) config.MapTarget {
	m.Versions = slices.Clone(m.Versions)
	m.StaticColumns = maps.Clone(m.StaticColumns)

	return m
}
