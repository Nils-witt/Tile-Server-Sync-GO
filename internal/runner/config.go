package runner

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/config"
	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/store"
	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/tileserve"
)

// connectionTestTimeout bounds TestAPI/TestDatabase (and the test every
// section save runs first), so an unreachable host (whose packets are
// silently dropped) fails the test instead of hanging the request.
const connectionTestTimeout = 10 * time.Second

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
// the stored one) in three steps: test it on its own (as TestAPI does), and
// only if that passes persist it, record config_saved in the security log,
// and try to apply the whole config live. A failed test is returned wrapped
// in ErrInvalid or ErrTestFailed, with nothing saved. Otherwise it returns
// the new stored config (redacted, like Config).
//
// Applying is deliberately not a precondition of the save: during initial
// setup each section is saved on its own, so the whole config is
// necessarily incomplete until every section is filled in. An
// incomplete-but-persisted config is simply not applied, reported via
// ChangeResult.ApplyErr.
func (r *Runner) SaveAPI(ctx context.Context, actor Actor, api config.API) (config.Config, ChangeResult, error) {
	return r.saveSection(ctx, actor, "api",
		func(cfg *config.Config) []string { return mergeAPI(cfg, api) },
		func(cfg *config.Config) error { return r.testAPI(ctx, actor, cfg.API) })
}

// SaveDatabase is the Database analogue of SaveAPI.
func (r *Runner) SaveDatabase(
	ctx context.Context, actor Actor, db config.Database,
) (config.Config, ChangeResult, error) {
	return r.saveSection(ctx, actor, "database",
		func(cfg *config.Config) []string { return mergeDatabase(cfg, db) },
		func(cfg *config.Config) error { return r.testDatabase(ctx, actor, cfg.Database) })
}

// mergeAPI replaces cfg.API with api (a blank Password keeps cfg's) and
// reports what changed.
func mergeAPI(cfg *config.Config, api config.API) []string {
	if api.Password == "" {
		api.Password = cfg.API.Password
	}

	changes := diffAPI(cfg.API, api)
	cfg.API = api

	return changes
}

// mergeDatabase is the Database analogue of mergeAPI.
func mergeDatabase(cfg *config.Config, db config.Database) []string {
	if db.Password == "" {
		db.Password = cfg.Database.Password
	}

	changes := diffDatabase(cfg.Database, db)
	cfg.Database = db

	return changes
}

// TestAPI validates api on its own and checks it against the server —
// logging in with its username/password, or, for a configured token, just
// checking that the base URL answers — without saving or applying it. A
// blank Password means the stored one, as in SaveAPI. Invalid settings are
// reported wrapped in ErrInvalid, a failed check wrapped in ErrTestFailed.
// Every attempt is recorded in the security log, since it may send the
// stored password to a newly entered host.
func (r *Runner) TestAPI(ctx context.Context, actor Actor, api config.API) error {
	if api.Password == "" {
		api.Password = r.storedCopy().API.Password
	}

	return r.testAPI(ctx, actor, api)
}

// TestDatabase is the Database analogue of TestAPI: it validates db on its
// own (connection settings plus table/column mapping) and pings it, without
// creating or touching any table.
func (r *Runner) TestDatabase(ctx context.Context, actor Actor, db config.Database) error {
	if db.Password == "" {
		db.Password = r.storedCopy().Database.Password
	}

	return r.testDatabase(ctx, actor, db)
}

// testAPI is TestAPI with api's Password already resolved.
func (r *Runner) testAPI(ctx context.Context, actor Actor, api config.API) error {
	if err := api.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	testCtx, cancel := context.WithTimeout(ctx, connectionTestTimeout)
	defer cancel()

	var err error
	if api.Token != "" {
		err = tileserve.New(api.BaseURL).Ping(testCtx)
	} else {
		_, err = newClient(testCtx, api)
	}

	r.LogSecurityEvent(ctx, actor, "api_tested",
		fmt.Sprintf("baseUrl=%q user=%q token=%v result=%s",
			api.BaseURL, api.Username, api.Token != "", testResult(err)))

	if err != nil {
		return fmt.Errorf("%w: %w", ErrTestFailed, err)
	}

	return nil
}

// testDatabase is TestDatabase with db's Password already resolved.
func (r *Runner) testDatabase(ctx context.Context, actor Actor, db config.Database) error {
	if err := db.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	testCtx, cancel := context.WithTimeout(ctx, connectionTestTimeout)
	defer cancel()

	err := store.Ping(testCtx, db)

	r.LogSecurityEvent(ctx, actor, "database_tested",
		fmt.Sprintf("host=%q user=%q name=%q tls=%v tlsSkipVerify=%v customCa=%v result=%s",
			db.Host, db.User, db.Name, db.TLS, db.TLSSkipVerify, db.TLSCACert != "", testResult(err)))

	if err != nil {
		return fmt.Errorf("%w: %w", ErrTestFailed, err)
	}

	return nil
}

func testResult(err error) string {
	if err != nil {
		return "failed"
	}

	return "ok"
}

// saveSection is the shared body of SaveAPI/SaveDatabase: merge (which
// overwrites one section of a copy of the stored config and reports what it
// changed) is applied, the merged section tested by test, and — only if that
// passes — the result persisted, made the new stored config, then applied
// live. The test runs under writeMu, so it checks exactly what is saved.
func (r *Runner) saveSection(
	ctx context.Context, actor Actor, section string,
	merge func(cfg *config.Config) []string, test func(cfg *config.Config) error,
) (config.Config, ChangeResult, error) {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	cfg := r.storedCopy()
	changes := merge(cfg)

	if err := test(cfg); err != nil {
		return config.Config{}, ChangeResult{}, err
	}

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
