package runner

import (
	"context"
	"maps"
	"slices"

	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/config"
)

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
