// Package settings is the settings-center domain: a typed key/value catalog
// with append-only revisions.
package settings

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Type is the JSON type of a setting value.
type Type string

const (
	TypeString Type = "string"
	TypeNumber Type = "number"
	TypeBool   Type = "boolean"
	TypeJSON   Type = "json"
)

// ErrNotFound is returned when a setting key is not in the catalog.
var ErrNotFound = errors.New("setting not found")

// Setting is one persisted setting value.
type Setting struct {
	Key             string
	Value           json.RawMessage
	Category        string
	Type            Type
	Secret          bool
	RestartRequired bool
	Description     string
	Version         int
	UpdatedBy       *int64
	UpdatedAt       time.Time
}

// Revision is one append-only settings change.
type Revision struct {
	ID        int64
	Key       string
	OldValue  json.RawMessage
	NewValue  json.RawMessage
	ActorID   *int64
	CreatedAt time.Time
}

// Definition describes a catalog key, its default and its validation range.
type Definition struct {
	Key             string
	Category        string
	Type            Type
	Default         json.RawMessage
	Secret          bool
	RestartRequired bool
	Description     string
	Integer         bool
	Min             *float64
	Max             *float64
}

// Repository persists settings and their revisions.
type Repository interface {
	List(ctx context.Context) ([]Setting, error)
	Get(ctx context.Context, key string) (Setting, error)
	Seed(ctx context.Context, definitions []Definition) error
	Update(ctx context.Context, key string, value json.RawMessage, revisionID, actorID int64, at time.Time) (Setting, error)
	Revisions(ctx context.Context, key string, limit int, beforeID *int64) ([]Revision, error)
}
