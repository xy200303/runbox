// Package store persists identity metadata and UID assignments in SQLite.
// It deliberately does not track individual files: workspaces are plain
// directories owned by each identity's UID.
package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/xy200303/runbox/sandbox"
)

// Identity is one persisted caller identity.
type Identity struct {
	Key        string    `gorm:"primaryKey;size:64"`
	Mode       string    `gorm:"size:16;not null"`
	Namespace  string    `gorm:"size:128;index:idx_identity_scope"`
	Principal  string    `gorm:"size:128;index:idx_identity_scope"`
	UID        int64     `gorm:"uniqueIndex;not null"`
	CreatedAt  time.Time `gorm:"not null"`
	LastSeenAt time.Time `gorm:"not null"`
}

// Store wraps the SQLite database.
type Store struct {
	db        *gorm.DB
	closeOnce sync.Once
	closeErr  error
}

// Open creates or opens the database at path and applies the schema.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("store path is required")
	}
	db, err := gorm.Open(sqlite.Open(filepath.ToSlash(path)), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("sqlite handle: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
	} {
		if err := db.Exec(pragma).Error; err != nil {
			sqlDB.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}
	if err := db.AutoMigrate(&Identity{}); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		sqlDB, err := s.db.DB()
		if err != nil {
			s.closeErr = err
			return
		}
		s.closeErr = sqlDB.Close()
	})
	return s.closeErr
}

// All returns every identity, ordered by UID.
func (s *Store) All() ([]Identity, error) {
	var identities []Identity
	if err := s.db.Order("uid").Find(&identities).Error; err != nil {
		return nil, err
	}
	return identities, nil
}

// Get returns one identity by key. A missing row returns ErrNotFound.
func (s *Store) Get(key string) (Identity, error) {
	var identity Identity
	err := s.db.Where("key = ?", key).First(&identity).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Identity{}, ErrNotFound
	}
	return identity, err
}

// Create stores a new identity. The caller owns UID allocation.
func (s *Store) Create(identity Identity) error {
	if identity.CreatedAt.IsZero() {
		identity.CreatedAt = time.Now().UTC()
	}
	if identity.LastSeenAt.IsZero() {
		identity.LastSeenAt = identity.CreatedAt
	}
	return s.db.Create(&identity).Error
}

// Touch updates last_seen_at for one identity.
func (s *Store) Touch(key string) error {
	return s.db.Model(&Identity{}).
		Where("key = ?", key).
		Update("last_seen_at", time.Now().UTC()).Error
}

// Delete removes one identity row. Files are not touched.
func (s *Store) Delete(key string) error {
	return s.db.Where("key = ?", key).Delete(&Identity{}).Error
}

// ErrNotFound is returned by Get for an unknown identity.
var ErrNotFound = errors.New("identity not found")

// FromSandbox builds the persisted record for an identity.
func FromSandbox(identity sandbox.Identity, uid int, key string) Identity {
	now := time.Now().UTC()
	return Identity{
		Key:        key,
		Mode:       identity.Mode(),
		Namespace:  identity.Namespace,
		Principal:  identity.Principal,
		UID:        int64(uid),
		CreatedAt:  now,
		LastSeenAt: now,
	}
}
