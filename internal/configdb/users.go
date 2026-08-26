package configdb

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// userSchemaStatements creates the users/sessions tables backing web server
// authentication. Run through the same ensureSchema loop as
// schemaStatements, so it stays idempotent CREATE TABLE IF NOT EXISTS on
// every startup like the rest of this package's schema.
var userSchemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS users (
		id                        INTEGER PRIMARY KEY AUTOINCREMENT,
		username                  TEXT NOT NULL UNIQUE,
		password_hash             TEXT NOT NULL,
		is_superuser              INTEGER NOT NULL DEFAULT 0,
		perm_view_status          INTEGER NOT NULL DEFAULT 0,
		perm_trigger_sync         INTEGER NOT NULL DEFAULT 0,
		perm_view_config          INTEGER NOT NULL DEFAULT 0,
		perm_edit_config_api      INTEGER NOT NULL DEFAULT 0,
		perm_edit_config_database INTEGER NOT NULL DEFAULT 0,
		perm_edit_config_maps     INTEGER NOT NULL DEFAULT 0,
		created_at                TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS sessions (
		token_hash TEXT PRIMARY KEY,
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		created_at TEXT NOT NULL,
		expires_at TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions(user_id)`,
}

// Sentinel errors returned by the user/session methods below. Callers use
// errors.Is to distinguish these from unexpected/internal failures.
var (
	ErrUsernameTaken      = errors.New("username already taken")
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrSessionInvalid     = errors.New("session invalid or expired")
	ErrUserNotFound       = errors.New("user not found")
)

// Permissions is the set of independently grantable feature permissions a
// user can hold. It deliberately has no umbrella "edit config" flag: editing
// is only ever granted per-section (API/Database/Maps), matching the
// section-specific save endpoints in internal/webserver.
type Permissions struct {
	ViewStatus         bool `json:"viewStatus"`
	TriggerSync        bool `json:"triggerSync"`
	ViewConfig         bool `json:"viewConfig"`
	EditConfigAPI      bool `json:"editConfigAPI"`
	EditConfigDatabase bool `json:"editConfigDatabase"`
	EditConfigMaps     bool `json:"editConfigMaps"`
}

// User is a stored account, without its password hash — never let that
// leak into a JSON response. IsSuperuser is orthogonal to Permissions: it
// only gates user management (see internal/webserver/users.go), and is not
// implied by, nor implies, any of the six feature permissions.
type User struct {
	ID          int64
	Username    string
	IsSuperuser bool
	Permissions
	CreatedAt time.Time
}

const timeFormat = time.RFC3339Nano

// UserCount reports how many accounts exist, used by the web server to
// decide whether to gate every request behind the one-time /setup page.
func (s *Store) UserCount(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}

	return n, nil
}

// CreateUser hashes password with bcrypt and inserts a new account. It
// returns ErrUsernameTaken (wrapped) if username is already in use.
func (s *Store) CreateUser(
	ctx context.Context, username, password string, perms Permissions, isSuperuser bool,
) (*User, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	createdAt := time.Now().UTC()

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO users (
			username, password_hash, is_superuser,
			perm_view_status, perm_trigger_sync, perm_view_config,
			perm_edit_config_api, perm_edit_config_database, perm_edit_config_maps,
			created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		username, string(hash), boolToInt(isSuperuser),
		boolToInt(perms.ViewStatus), boolToInt(perms.TriggerSync), boolToInt(perms.ViewConfig),
		boolToInt(perms.EditConfigAPI), boolToInt(perms.EditConfigDatabase), boolToInt(perms.EditConfigMaps),
		createdAt.Format(timeFormat))
	if err != nil {
		if isUniqueConstraintErr(err) {
			return nil, fmt.Errorf("create user %q: %w", username, ErrUsernameTaken)
		}

		return nil, fmt.Errorf("create user %q: %w", username, err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("create user %q: %w", username, err)
	}

	return &User{
		ID: id, Username: username, IsSuperuser: isSuperuser, Permissions: perms, CreatedAt: createdAt,
	}, nil
}

// isUniqueConstraintErr reports whether err looks like a SQLite UNIQUE
// constraint violation. modernc.org/sqlite doesn't export a typed
// sentinel/code for this, so this matches on the driver's error text, which
// is stable across modernc.org/sqlite releases ("UNIQUE constraint failed").
func isUniqueConstraintErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// VerifyPassword loads the user named username and checks password against
// its stored hash. Any failure (no such user, wrong password) returns the
// same ErrInvalidCredentials so a caller can never distinguish which part
// was wrong.
func (s *Store) VerifyPassword(ctx context.Context, username, password string) (*User, error) {
	var (
		u    User
		hash string
	)

	row := s.db.QueryRowContext(ctx, `
		SELECT id, username, password_hash, is_superuser,
			perm_view_status, perm_trigger_sync, perm_view_config,
			perm_edit_config_api, perm_edit_config_database, perm_edit_config_maps,
			created_at
		FROM users WHERE username = ?`, username)

	if err := scanUser(row, &u, &hash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrInvalidCredentials
		}

		return nil, fmt.Errorf("load user %q: %w", username, err)
	}

	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return nil, ErrInvalidCredentials
	}

	return &u, nil
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows, letting scanUser
// serve both a single-row lookup and a multi-row list without duplicating
// the column list.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanUser(row rowScanner, u *User, hash *string) error {
	var (
		isSuperuser, viewStatus, triggerSync, viewConfig  int
		editConfigAPI, editConfigDatabase, editConfigMaps int
		createdAt                                         string
	)

	if err := row.Scan(
		&u.ID, &u.Username, hash, &isSuperuser,
		&viewStatus, &triggerSync, &viewConfig,
		&editConfigAPI, &editConfigDatabase, &editConfigMaps,
		&createdAt,
	); err != nil {
		return err
	}

	u.IsSuperuser = isSuperuser != 0
	u.ViewStatus = viewStatus != 0
	u.TriggerSync = triggerSync != 0
	u.ViewConfig = viewConfig != 0
	u.EditConfigAPI = editConfigAPI != 0
	u.EditConfigDatabase = editConfigDatabase != 0
	u.EditConfigMaps = editConfigMaps != 0

	if parsed, err := time.Parse(timeFormat, createdAt); err == nil {
		u.CreatedAt = parsed
	}

	return nil
}

// ListUsers returns every account, ordered by id (creation order).
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, username, password_hash, is_superuser,
			perm_view_status, perm_trigger_sync, perm_view_config,
			perm_edit_config_api, perm_edit_config_database, perm_edit_config_maps,
			created_at
		FROM users ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var users []User

	for rows.Next() {
		var (
			u    User
			hash string
		)

		if err := scanUser(rows, &u, &hash); err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}

		users = append(users, u)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}

	return users, nil
}

// GetUser loads a single account by id, or ErrUserNotFound if none exists.
func (s *Store) GetUser(ctx context.Context, id int64) (*User, error) {
	var (
		u    User
		hash string
	)

	row := s.db.QueryRowContext(ctx, `
		SELECT id, username, password_hash, is_superuser,
			perm_view_status, perm_trigger_sync, perm_view_config,
			perm_edit_config_api, perm_edit_config_database, perm_edit_config_maps,
			created_at
		FROM users WHERE id = ?`, id)

	if err := scanUser(row, &u, &hash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}

		return nil, fmt.Errorf("load user %d: %w", id, err)
	}

	return &u, nil
}

// UpdateUser replaces id's permissions/superuser flag, and its password
// hash too unless newPassword is empty — a blank password means "leave
// unchanged", mirroring the existing convention for API.Password/
// Database.DSN in the web server's own config save handling.
func (s *Store) UpdateUser(ctx context.Context, id int64, perms Permissions, isSuperuser bool, newPassword string) error {
	if newPassword == "" {
		res, err := s.db.ExecContext(ctx, `
			UPDATE users SET
				is_superuser = ?, perm_view_status = ?, perm_trigger_sync = ?, perm_view_config = ?,
				perm_edit_config_api = ?, perm_edit_config_database = ?, perm_edit_config_maps = ?
			WHERE id = ?`,
			boolToInt(isSuperuser), boolToInt(perms.ViewStatus), boolToInt(perms.TriggerSync), boolToInt(perms.ViewConfig),
			boolToInt(perms.EditConfigAPI), boolToInt(perms.EditConfigDatabase), boolToInt(perms.EditConfigMaps),
			id)
		if err != nil {
			return fmt.Errorf("update user %d: %w", id, err)
		}

		return checkRowAffected(res, id)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	res, err := s.db.ExecContext(ctx, `
		UPDATE users SET
			password_hash = ?, is_superuser = ?, perm_view_status = ?, perm_trigger_sync = ?, perm_view_config = ?,
			perm_edit_config_api = ?, perm_edit_config_database = ?, perm_edit_config_maps = ?
		WHERE id = ?`,
		string(hash),
		boolToInt(isSuperuser), boolToInt(perms.ViewStatus), boolToInt(perms.TriggerSync), boolToInt(perms.ViewConfig),
		boolToInt(perms.EditConfigAPI), boolToInt(perms.EditConfigDatabase), boolToInt(perms.EditConfigMaps),
		id)
	if err != nil {
		return fmt.Errorf("update user %d: %w", id, err)
	}

	return checkRowAffected(res, id)
}

func checkRowAffected(res sql.Result, id int64) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update user %d: %w", id, err)
	}

	if n == 0 {
		return fmt.Errorf("update user %d: %w", id, ErrUserNotFound)
	}

	return nil
}

// DeleteUser removes an account; its sessions cascade via
// sessions.user_id's ON DELETE CASCADE.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete user %d: %w", id, err)
	}

	return checkRowAffected(res, id)
}

// CreateSession issues a new random session token for userID, valid for
// ttl. Only the token's SHA-256 hash is stored; the raw token (meant for the
// session cookie) is returned and never persisted.
func (s *Store) CreateSession(ctx context.Context, userID int64, ttl time.Duration) (string, time.Time, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, fmt.Errorf("generate session token: %w", err)
	}

	token := base64.RawURLEncoding.EncodeToString(raw)
	now := time.Now().UTC()
	expiresAt := now.Add(ttl)

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		hashToken(token), userID, now.Format(timeFormat), expiresAt.Format(timeFormat))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("create session: %w", err)
	}

	return token, expiresAt, nil
}

// SessionUser resolves a raw session token (as read from the session
// cookie) back to its owning user, or ErrSessionInvalid if the token is
// unknown or expired. An expired session row is opportunistically deleted
// when found.
func (s *Store) SessionUser(ctx context.Context, token string) (*User, error) {
	tokenHash := hashToken(token)

	var (
		userID    int64
		expiresAt string
	)

	err := s.db.QueryRowContext(ctx,
		`SELECT user_id, expires_at FROM sessions WHERE token_hash = ?`, tokenHash,
	).Scan(&userID, &expiresAt)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, ErrSessionInvalid
	case err != nil:
		return nil, fmt.Errorf("load session: %w", err)
	}

	expiry, parseErr := time.Parse(timeFormat, expiresAt)
	if parseErr != nil || time.Now().UTC().After(expiry) {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
		return nil, ErrSessionInvalid
	}

	u, err := s.GetUser(ctx, userID)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return nil, ErrSessionInvalid
		}

		return nil, err
	}

	return u, nil
}

// DeleteSession removes a session by its raw token (logout). Deleting an
// already-gone/unknown token is not an error.
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hashToken(token)); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}

	return nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func boolToInt(b bool) int {
	if b {
		return 1
	}

	return 0
}
