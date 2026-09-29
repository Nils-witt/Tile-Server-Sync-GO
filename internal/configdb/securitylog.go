package configdb

import (
	"context"
	"fmt"
	"time"
)

// SecurityLogEntry is one row of the audit trail.
type SecurityLogEntry struct {
	ID         int64     `gorm:"column:id;primaryKey;autoIncrement" json:"-"`
	At         time.Time `gorm:"column:at;not null"`
	EventType  string    `gorm:"column:event_type;not null"`
	Username   string    `gorm:"column:username;not null;default:''"`
	RemoteAddr string    `gorm:"column:remote_addr;not null;default:''"`
	Detail     string    `gorm:"column:detail;not null;default:''"`
}

// TableName pins this model to its existing table name.
func (SecurityLogEntry) TableName() string { return "security_log" }

// LogSecurityEvent appends one entry to the security log. Callers treat a
// failure here as best-effort (log and continue): the audit trail must never
// block or fail the login/save/user-edit action that triggered it.
func (s *Store) LogSecurityEvent(ctx context.Context, eventType, username, remoteAddr, detail string) error {
	entry := SecurityLogEntry{
		At: time.Now().UTC(), EventType: eventType, Username: username, RemoteAddr: remoteAddr, Detail: detail,
	}

	if err := s.db.WithContext(ctx).Create(&entry).Error; err != nil {
		return fmt.Errorf("log security event: %w", err)
	}

	return nil
}

// ListSecurityLog returns the most recent entries, newest first, capped at
// limit rows.
func (s *Store) ListSecurityLog(ctx context.Context, limit int) ([]SecurityLogEntry, error) {
	var entries []SecurityLogEntry

	err := s.db.WithContext(ctx).Order("id DESC").Limit(limit).Find(&entries).Error
	if err != nil {
		return nil, fmt.Errorf("list security log: %w", err)
	}

	return entries, nil
}
