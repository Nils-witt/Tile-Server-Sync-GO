package configdb

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// ssoSchemaStatements creates the tables backing SSO/OpenID Connect login:
// sso_config is a singleton settings row (mirroring config_scalar's id=1
// pattern), and sso_identities links a verified (issuer, subject) pair from
// an OIDC provider to a local users row. Run through the same ensureSchema
// loop as schemaStatements/userSchemaStatements.
var ssoSchemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS sso_config (
		id                            INTEGER PRIMARY KEY CHECK (id = 1),
		enabled                       INTEGER NOT NULL DEFAULT 0,
		issuer_url                    TEXT NOT NULL DEFAULT '',
		client_id                     TEXT NOT NULL DEFAULT '',
		client_secret                 TEXT NOT NULL DEFAULT '',
		scopes                        TEXT NOT NULL DEFAULT '',
		button_label                  TEXT NOT NULL DEFAULT '',
		redirect_base_url             TEXT NOT NULL DEFAULT '',
		default_view_status           INTEGER NOT NULL DEFAULT 0,
		default_trigger_sync          INTEGER NOT NULL DEFAULT 0,
		default_view_config           INTEGER NOT NULL DEFAULT 0,
		default_edit_config_api       INTEGER NOT NULL DEFAULT 0,
		default_edit_config_database  INTEGER NOT NULL DEFAULT 0,
		default_edit_config_maps      INTEGER NOT NULL DEFAULT 0,
		default_edit_config_sso       INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE IF NOT EXISTS sso_identities (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		issuer     TEXT NOT NULL,
		subject    TEXT NOT NULL,
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		created_at TEXT NOT NULL,
		UNIQUE(issuer, subject)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_sso_identities_user_id ON sso_identities(user_id)`,
}

// SSOConfig is the stored OpenID Connect SSO configuration: whether it's
// enabled, the provider's connection details, and the permission set
// auto-granted to a newly provisioned SSO account (see FindOrCreateSSOUser).
// is_superuser is deliberately not part of this: SSO-provisioned accounts
// are never superusers automatically, matching every other account creation
// path (see allPermissions in internal/webserver/auth.go for the one
// exception, the very first account created at /setup).
type SSOConfig struct {
	Enabled bool
	// IssuerURL is the OIDC provider's issuer, e.g.
	// "https://accounts.example.com" — passed to oidc.NewProvider for
	// discovery.
	IssuerURL    string
	ClientID     string
	ClientSecret string
	// Scopes is a space-separated OAuth2 scope list, e.g.
	// "openid profile email".
	Scopes string
	// ButtonLabel is the text shown on the login page's SSO button.
	ButtonLabel string
	// RedirectBaseURL, if set, is used (plus "/login/sso/callback") as the
	// OAuth2 redirect URI instead of one derived from the incoming request's
	// scheme/host — needed behind a reverse proxy or TLS terminator where
	// the request seen by this process doesn't reflect the externally
	// visible URL.
	RedirectBaseURL    string
	DefaultPermissions Permissions
}

// LoadSSOConfig loads the stored SSO settings. No row yet (a fresh install)
// is not an error: it returns a zero-valued *SSOConfig (Enabled: false),
// exactly as Store.Load does for an empty config_scalar table.
func (s *Store) LoadSSOConfig(ctx context.Context) (*SSOConfig, error) {
	var (
		cfg         SSOConfig
		enabled     int64
		defView     int64
		defSync     int64
		defViewCfg  int64
		defEditAPI  int64
		defEditDB   int64
		defEditMaps int64
		defEditSSO  int64
	)

	row := s.db.QueryRowContext(ctx, `
		SELECT enabled, issuer_url, client_id, client_secret, scopes, button_label, redirect_base_url,
			default_view_status, default_trigger_sync, default_view_config,
			default_edit_config_api, default_edit_config_database, default_edit_config_maps, default_edit_config_sso
		FROM sso_config WHERE id = 1`)

	switch err := row.Scan(
		&enabled, &cfg.IssuerURL, &cfg.ClientID, &cfg.ClientSecret, &cfg.Scopes, &cfg.ButtonLabel, &cfg.RedirectBaseURL,
		&defView, &defSync, &defViewCfg, &defEditAPI, &defEditDB, &defEditMaps, &defEditSSO,
	); {
	case errors.Is(err, sql.ErrNoRows):
		return &cfg, nil
	case err != nil:
		return nil, fmt.Errorf("load sso config: %w", err)
	}

	cfg.Enabled = enabled != 0
	cfg.DefaultPermissions = Permissions{
		ViewStatus: defView != 0, TriggerSync: defSync != 0, ViewConfig: defViewCfg != 0,
		EditConfigAPI: defEditAPI != 0, EditConfigDatabase: defEditDB != 0,
		EditConfigMaps: defEditMaps != 0, EditConfigSSO: defEditSSO != 0,
	}

	return &cfg, nil
}

// SaveSSOConfig replaces the stored SSO settings wholesale (single-row
// upsert, matching config_scalar's).
func (s *Store) SaveSSOConfig(ctx context.Context, cfg *SSOConfig) error {
	p := cfg.DefaultPermissions

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sso_config (
			id, enabled, issuer_url, client_id, client_secret, scopes, button_label, redirect_base_url,
			default_view_status, default_trigger_sync, default_view_config,
			default_edit_config_api, default_edit_config_database, default_edit_config_maps, default_edit_config_sso
		) VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			enabled = excluded.enabled,
			issuer_url = excluded.issuer_url,
			client_id = excluded.client_id,
			client_secret = excluded.client_secret,
			scopes = excluded.scopes,
			button_label = excluded.button_label,
			redirect_base_url = excluded.redirect_base_url,
			default_view_status = excluded.default_view_status,
			default_trigger_sync = excluded.default_trigger_sync,
			default_view_config = excluded.default_view_config,
			default_edit_config_api = excluded.default_edit_config_api,
			default_edit_config_database = excluded.default_edit_config_database,
			default_edit_config_maps = excluded.default_edit_config_maps,
			default_edit_config_sso = excluded.default_edit_config_sso`,
		boolToInt(cfg.Enabled), cfg.IssuerURL, cfg.ClientID, cfg.ClientSecret, cfg.Scopes, cfg.ButtonLabel, cfg.RedirectBaseURL,
		boolToInt(p.ViewStatus), boolToInt(p.TriggerSync), boolToInt(p.ViewConfig),
		boolToInt(p.EditConfigAPI), boolToInt(p.EditConfigDatabase), boolToInt(p.EditConfigMaps), boolToInt(p.EditConfigSSO))
	if err != nil {
		return fmt.Errorf("save sso config: %w", err)
	}

	return nil
}

// FindOrCreateSSOUser resolves a verified OIDC identity (issuer + subject,
// from the ID token's iss/sub claims) to a local user, in three steps:
//
//  1. An sso_identities row already links this exact (issuer, subject) to a
//     user — return that user as-is (its permissions may since have been
//     edited via /users, and this must not overwrite that).
//  2. No link yet, but a local user already exists named username (e.g. an
//     admin pre-created an account for this person with different
//     permissions than the configured default) — link to it rather than
//     failing on users.username's UNIQUE constraint or creating a
//     duplicate account.
//  3. Neither exists — auto-provision a new local user with defaults
//     (never a superuser) and a random, never-revealed password (the
//     account simply has no known password until an admin sets one via
//     /users), then link it.
//
// Steps 2-3 run in a transaction; a UNIQUE-constraint race on either table
// (another concurrent login for the same identity/username) is retried once
// by re-reading rather than treated as an error.
func (s *Store) FindOrCreateSSOUser(
	ctx context.Context, issuer, subject, username string, defaults Permissions,
) (*User, error) {
	if userID, err := s.findSSOIdentity(ctx, issuer, subject); err != nil {
		return nil, err
	} else if userID != 0 {
		return s.GetUser(ctx, userID)
	}

	userID, err := s.linkOrCreateSSOUser(ctx, issuer, subject, username, defaults)
	if err != nil {
		if isUniqueConstraintErr(err) {
			if retryID, retryErr := s.findSSOIdentity(ctx, issuer, subject); retryErr == nil && retryID != 0 {
				return s.GetUser(ctx, retryID)
			}
		}

		return nil, err
	}

	return s.GetUser(ctx, userID)
}

func (s *Store) findSSOIdentity(ctx context.Context, issuer, subject string) (int64, error) {
	var userID int64

	err := s.db.QueryRowContext(ctx,
		`SELECT user_id FROM sso_identities WHERE issuer = ? AND subject = ?`, issuer, subject,
	).Scan(&userID)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("load sso identity: %w", err)
	}

	return userID, nil
}

func (s *Store) linkOrCreateSSOUser(
	ctx context.Context, issuer, subject, username string, defaults Permissions,
) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	userID, err := existingUserIDByUsername(ctx, tx, username)
	if err != nil {
		return 0, err
	}

	if userID == 0 {
		userID, err = createSSOUser(ctx, tx, username, defaults)
		if err != nil {
			return 0, err
		}
	}

	createdAt := time.Now().UTC().Format(timeFormat)
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO sso_identities (issuer, subject, user_id, created_at) VALUES (?, ?, ?, ?)`,
		issuer, subject, userID, createdAt); err != nil {
		return 0, fmt.Errorf("link sso identity: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit transaction: %w", err)
	}

	return userID, nil
}

func existingUserIDByUsername(ctx context.Context, tx *sql.Tx, username string) (int64, error) {
	var userID int64

	err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE username = ?`, username).Scan(&userID)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("load user %q: %w", username, err)
	}

	return userID, nil
}

// createSSOUser inserts a new local account for an auto-provisioned SSO
// user: defaults permissions, never a superuser, and a random password (32
// bytes from crypto/rand, base64-encoded) that's hashed and then discarded —
// nobody, including the person logging in, ever sees it. It stays that way
// until an admin sets a real one via /users.
func createSSOUser(ctx context.Context, tx *sql.Tx, username string, defaults Permissions) (int64, error) {
	randomPassword := make([]byte, 32)
	if _, err := rand.Read(randomPassword); err != nil {
		return 0, fmt.Errorf("generate random password: %w", err)
	}

	encoded := base64.RawURLEncoding.EncodeToString(randomPassword)

	hash, err := bcrypt.GenerateFromPassword([]byte(encoded), bcrypt.DefaultCost)
	if err != nil {
		return 0, fmt.Errorf("hash random password: %w", err)
	}

	createdAt := time.Now().UTC().Format(timeFormat)

	res, err := tx.ExecContext(ctx, `
		INSERT INTO users (
			username, password_hash, is_superuser,
			perm_view_status, perm_trigger_sync, perm_view_config,
			perm_edit_config_api, perm_edit_config_database, perm_edit_config_maps, perm_edit_config_sso,
			created_at
		) VALUES (?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?)`,
		username, string(hash),
		boolToInt(defaults.ViewStatus), boolToInt(defaults.TriggerSync), boolToInt(defaults.ViewConfig),
		boolToInt(defaults.EditConfigAPI), boolToInt(defaults.EditConfigDatabase), boolToInt(defaults.EditConfigMaps),
		boolToInt(defaults.EditConfigSSO),
		createdAt)
	if err != nil {
		return 0, fmt.Errorf("create sso user %q: %w", username, err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("create sso user %q: %w", username, err)
	}

	return id, nil
}
