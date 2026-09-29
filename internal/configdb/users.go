package configdb

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

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
	EditConfigSSO      bool `json:"editConfigSSO"`
}

// User is a stored account. PasswordHash is exported only because GORM
// requires it (unexported fields aren't reachable by reflection) — nothing
// outside this package ever reads it, and json:"-" keeps it out of any
// accidental direct marshaling (every JSON response goes through a
// hand-built DTO instead, e.g. internal/webserver/users.go's userDTO).
// IsSuperuser is orthogonal to Permissions: it only gates user management
// (see internal/webserver/users.go), and is not implied by, nor implies,
// any of the seven feature permissions. Permissions is embedded with the
// "perm_" column prefix — see schema.go's migrate and SSOConfig's
// DefaultPermissions for the "default_"-prefixed counterpart.
type User struct {
	ID           int64       `gorm:"column:id;primaryKey;autoIncrement"        json:"-"`
	Username     string      `gorm:"column:username;not null;uniqueIndex"      json:"-"`
	PasswordHash string      `gorm:"column:password_hash;not null"             json:"-"`
	IsSuperuser  bool        `gorm:"column:is_superuser;not null;default:false" json:"-"`
	Permissions  Permissions `gorm:"embedded;embeddedPrefix:perm_"             json:"-"`
	CreatedAt    time.Time   `gorm:"column:created_at;not null"                json:"-"`
}

// TableName pins this model to its existing table name.
func (User) TableName() string { return "users" }

// UserCount reports how many accounts exist, used by the web server to
// decide whether to gate every request behind the one-time /setup page.
func (s *Store) UserCount(ctx context.Context) (int, error) {
	var n int64
	if err := s.db.WithContext(ctx).Model(&User{}).Count(&n).Error; err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}

	return int(n), nil
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

	u := User{
		Username:     username,
		PasswordHash: string(hash),
		IsSuperuser:  isSuperuser,
		Permissions:  perms,
		CreatedAt:    time.Now().UTC(),
	}

	if err := s.db.WithContext(ctx).Create(&u).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, fmt.Errorf("create user %q: %w", username, ErrUsernameTaken)
		}

		return nil, fmt.Errorf("create user %q: %w", username, err)
	}

	return &u, nil
}

// VerifyPassword loads the user named username and checks password against
// its stored hash. Any failure (no such user, wrong password) returns the
// same ErrInvalidCredentials so a caller can never distinguish which part
// was wrong.
func (s *Store) VerifyPassword(ctx context.Context, username, password string) (*User, error) {
	var u User

	switch err := s.db.WithContext(ctx).Where("username = ?", username).First(&u).Error; {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, ErrInvalidCredentials
	case err != nil:
		return nil, fmt.Errorf("load user %q: %w", username, err)
	}

	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		return nil, ErrInvalidCredentials
	}

	return &u, nil
}

// ListUsers returns every account, ordered by id (creation order).
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	var users []User
	if err := s.db.WithContext(ctx).Order("id ASC").Find(&users).Error; err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}

	return users, nil
}

// GetUser loads a single account by id, or ErrUserNotFound if none exists.
func (s *Store) GetUser(ctx context.Context, id int64) (*User, error) {
	var u User

	switch err := s.db.WithContext(ctx).First(&u, id).Error; {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, ErrUserNotFound
	case err != nil:
		return nil, fmt.Errorf("load user %d: %w", id, err)
	}

	return &u, nil
}

// UpdateUser replaces id's permissions/superuser flag, and its password
// hash too unless newPassword is empty — a blank password means "leave
// unchanged", mirroring the existing convention for API.Password/
// Database.DSN in the web server's own config save handling.
func (s *Store) UpdateUser(ctx context.Context, id int64, perms Permissions, isSuperuser bool, newPassword string) error {
	updates := map[string]any{
		"is_superuser":              isSuperuser,
		"perm_view_status":          perms.ViewStatus,
		"perm_trigger_sync":         perms.TriggerSync,
		"perm_view_config":          perms.ViewConfig,
		"perm_edit_config_api":      perms.EditConfigAPI,
		"perm_edit_config_database": perms.EditConfigDatabase,
		"perm_edit_config_maps":     perms.EditConfigMaps,
		"perm_edit_config_sso":      perms.EditConfigSSO,
	}

	if newPassword != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
		if err != nil {
			return fmt.Errorf("hash password: %w", err)
		}

		updates["password_hash"] = string(hash)
	}

	res := s.db.WithContext(ctx).Model(&User{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		return fmt.Errorf("update user %d: %w", id, res.Error)
	}

	if res.RowsAffected == 0 {
		return fmt.Errorf("update user %d: %w", id, ErrUserNotFound)
	}

	return nil
}

// DeleteUser removes an account; its sessions cascade via
// sessions.user_id's ON DELETE CASCADE.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	res := s.db.WithContext(ctx).Delete(&User{}, id)
	if res.Error != nil {
		return fmt.Errorf("delete user %d: %w", id, res.Error)
	}

	if res.RowsAffected == 0 {
		return fmt.Errorf("delete user %d: %w", id, ErrUserNotFound)
	}

	return nil
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

	sess := session{TokenHash: hashToken(token), UserID: userID, CreatedAt: now, ExpiresAt: expiresAt}
	if err := s.db.WithContext(ctx).Create(&sess).Error; err != nil {
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

	var sess session

	switch err := s.db.WithContext(ctx).Where("token_hash = ?", tokenHash).First(&sess).Error; {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, ErrSessionInvalid
	case err != nil:
		return nil, fmt.Errorf("load session: %w", err)
	}

	if time.Now().UTC().After(sess.ExpiresAt) {
		_ = s.db.WithContext(ctx).Where("token_hash = ?", tokenHash).Delete(&session{}).Error
		return nil, ErrSessionInvalid
	}

	u, err := s.GetUser(ctx, sess.UserID)
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
	if err := s.db.WithContext(ctx).Where("token_hash = ?", hashToken(token)).Delete(&session{}).Error; err != nil {
		return fmt.Errorf("delete session: %w", err)
	}

	return nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
