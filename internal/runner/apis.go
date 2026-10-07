package runner

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Nils-witt/Tile-Server-Sync-GO/internal/config"
)

// APIs returns every stored API, in configured order, with passwords
// blanked (see Config).
func (r *Runner) APIs() []config.API {
	cfg := r.storedCopy()
	redactSecrets(cfg)

	return cfg.APIs
}

// CreateAPI tests api on its own (as TestAPI does) and only if that passes
// persists it, records api_created in the security log, and applies the
// whole config live. A failed test is returned wrapped in ErrInvalid or
// ErrTestFailed; a duplicate id returns ErrAPIIDTaken. The created API is
// returned with its password blanked.
func (r *Runner) CreateAPI(ctx context.Context, actor Actor, api config.API) (config.API, ChangeResult, error) {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	cfg := r.storedCopy()
	if cfg.API(api.ID) != nil {
		return config.API{}, ChangeResult{}, fmt.Errorf("create api %q: %w", api.ID, ErrAPIIDTaken)
	}

	if err := r.testAPI(ctx, actor, api); err != nil {
		return config.API{}, ChangeResult{}, err
	}

	if err := r.cfgDB.CreateAPI(ctx, api); err != nil {
		return config.API{}, ChangeResult{}, err
	}

	cfg.APIs = append(cfg.APIs, api)
	r.setStored(cfg)
	r.LogSecurityEvent(ctx, actor, "api_created",
		fmt.Sprintf("api %q created: %s", api.ID, strings.Join(diffAPI(config.API{}, api), ", ")))

	res := ChangeResult{ApplyErr: r.apply(ctx)}
	api.Password = ""

	return api, res, nil
}

// UpdateAPI replaces the API identified by id with api; id is authoritative
// (api.ID is ignored), and a blank Password keeps the stored one. Like
// CreateAPI it is tested first and saved only if the test passes. If the
// base URL changed, the EDP overlay rows of every map using this API are
// moved to the new URL (a failure there is only reported via
// ChangeResult.OverlayErr). Returns ErrAPINotFound for an unknown id.
func (r *Runner) UpdateAPI(
	ctx context.Context, actor Actor, id string, api config.API,
) (config.API, ChangeResult, error) {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	api.ID = id
	cfg := r.storedCopy()

	idx := slices.IndexFunc(cfg.APIs, func(a config.API) bool { return a.ID == id })
	if idx < 0 {
		return config.API{}, ChangeResult{}, fmt.Errorf("update api %q: %w", id, ErrAPINotFound)
	}

	before := cfg.APIs[idx]
	if api.Password == "" {
		api.Password = before.Password
	}

	if err := r.testAPI(ctx, actor, api); err != nil {
		return config.API{}, ChangeResult{}, err
	}

	if err := r.cfgDB.UpdateAPI(ctx, id, api); err != nil {
		return config.API{}, ChangeResult{}, err
	}

	cfg.APIs[idx] = api
	r.setStored(cfg)
	r.LogSecurityEvent(ctx, actor, "api_updated", fmt.Sprintf("api %q: %s", id, changesDetail(diffAPI(before, api))))

	var res ChangeResult

	if before.BaseURL != api.BaseURL {
		var errs []error

		for _, m := range cfg.Maps {
			if m.APIID == id {
				errs = append(errs, r.updateMapOverlays(ctx, before.BaseURL, api.BaseURL, m, m))
			}
		}

		res.OverlayErr = errors.Join(errs...)
	}

	res.ApplyErr = r.apply(ctx)
	api.Password = ""

	return api, res, nil
}

// DeleteAPI removes the API identified by id and applies the result live.
// It refuses (ErrAPIInUse) while any map is still fetched from it, and
// returns ErrAPINotFound for an unknown id.
func (r *Runner) DeleteAPI(ctx context.Context, actor Actor, id string) (ChangeResult, error) {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	cfg := r.storedCopy()

	idx := slices.IndexFunc(cfg.APIs, func(a config.API) bool { return a.ID == id })
	if idx < 0 {
		return ChangeResult{}, fmt.Errorf("delete api %q: %w", id, ErrAPINotFound)
	}

	var users []string

	for _, m := range cfg.Maps {
		if m.APIID == id {
			users = append(users, m.ID)
		}
	}

	if len(users) > 0 {
		return ChangeResult{}, fmt.Errorf("delete api %q: %w: %s", id, ErrAPIInUse, strings.Join(users, ", "))
	}

	if err := r.cfgDB.DeleteAPI(ctx, id); err != nil {
		return ChangeResult{}, err
	}

	cfg.APIs = slices.Delete(cfg.APIs, idx, idx+1)
	r.setStored(cfg)
	r.LogSecurityEvent(ctx, actor, "api_deleted", fmt.Sprintf("api %q deleted", id))

	return ChangeResult{ApplyErr: r.apply(ctx)}, nil
}
