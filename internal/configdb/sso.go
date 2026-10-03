// This file holds the SSO side of the users table: linking a verified OIDC
// identity to a local account. The SSO settings themselves live in the
// bootstrap file (config.SSO), not here; older databases may still contain
// an unused sso_config table from when they did.

package configdb

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

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
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			if retryID, retryErr := s.findSSOIdentity(ctx, issuer, subject); retryErr == nil && retryID != 0 {
				return s.GetUser(ctx, retryID)
			}
		}

		return nil, err
	}

	return s.GetUser(ctx, userID)
}

func (s *Store) findSSOIdentity(ctx context.Context, issuer, subject string) (int64, error) {
	var identity ssoIdentity

	switch err := s.db.WithContext(ctx).
		Where("issuer = ? AND subject = ?", issuer, subject).First(&identity).Error; {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("load sso identity: %w", err)
	}

	return identity.UserID, nil
}

func (s *Store) linkOrCreateSSOUser(
	ctx context.Context, issuer, subject, username string, defaults Permissions,
) (int64, error) {
	var userID int64

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		id, err := existingUserIDByUsername(tx, username)
		if err != nil {
			return err
		}

		if id == 0 {
			id, err = createSSOUser(tx, username, defaults)
			if err != nil {
				return err
			}
		}

		identity := ssoIdentity{Issuer: issuer, Subject: subject, UserID: id, CreatedAt: time.Now().UTC()}
		if err := tx.Create(&identity).Error; err != nil {
			return fmt.Errorf("link sso identity: %w", err)
		}

		userID = id

		return nil
	})
	if err != nil {
		return 0, err
	}

	return userID, nil
}

func existingUserIDByUsername(tx *gorm.DB, username string) (int64, error) {
	var u User

	switch err := tx.Select("id").Where("username = ?", username).First(&u).Error; {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("load user %q: %w", username, err)
	}

	return u.ID, nil
}

// createSSOUser inserts a new local account for an auto-provisioned SSO
// user: defaults permissions, never a superuser, and a random password (32
// bytes from crypto/rand, base64-encoded) that's hashed and then discarded —
// nobody, including the person logging in, ever sees it. It stays that way
// until an admin sets a real one via /users.
func createSSOUser(tx *gorm.DB, username string, defaults Permissions) (int64, error) {
	randomPassword := make([]byte, 32)
	if _, err := rand.Read(randomPassword); err != nil {
		return 0, fmt.Errorf("generate random password: %w", err)
	}

	encoded := base64.RawURLEncoding.EncodeToString(randomPassword)

	hash, err := bcrypt.GenerateFromPassword([]byte(encoded), bcrypt.DefaultCost)
	if err != nil {
		return 0, fmt.Errorf("hash random password: %w", err)
	}

	u := User{
		Username:     username,
		PasswordHash: string(hash),
		IsSuperuser:  false,
		Permissions:  defaults,
		CreatedAt:    time.Now().UTC(),
	}

	if err := tx.Create(&u).Error; err != nil {
		return 0, fmt.Errorf("create sso user %q: %w", username, err)
	}

	return u.ID, nil
}
