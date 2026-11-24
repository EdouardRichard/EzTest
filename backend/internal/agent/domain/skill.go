package agent

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// Skill packages are an agent capability (constitution art.2.1). The runtime
// skill library in PostgreSQL is the authoritative store; skills/ files are
// only the first-start seed source. Skill bodies are never hardcoded in Go
// source: administrators edit them at runtime through the admin API and the
// next read observes the edit without a rebuild or restart.

// Skill revision sources. Seed rows are written once by the startup file
// seeding; every runtime write is an admin revision.
const (
	SkillSourceSeed  = "seed"
	SkillSourceAdmin = "admin"
)

// Skill content limits. The body limit mirrors MaxPromptRunes; the
// description is the metadata layer shown in listings.
const (
	MaxSkillBodyRunes        = 32768
	MaxSkillDescriptionRunes = 2000
)

// SkillNamePattern is the frozen skill package name shape (directory name of
// a skills/<name>/SKILL.md package, e.g. "playwright-cli").
var SkillNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// ErrSkillVersionConflict reports an expected_version mismatch on a skill
// update. The application maps it to 409 state_conflict.
var ErrSkillVersionConflict = errors.New("agent: skill version conflict")

// Skill is one row of the runtime skill library: the current revision of a
// SKILL.md package. Version is the current revision number (>= 1).
type Skill struct {
	ID          int64
	Name        string
	Description string
	Body        string
	Metadata    json.RawMessage
	Version     int
	Enabled     bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// SkillVersion is one append-only skill_version row. Rows are never updated
// or deleted; a skill delete hides the aggregate but keeps its history.
type SkillVersion struct {
	ID          int64
	SkillID     int64
	Version     int
	Name        string
	Description string
	Body        string
	Metadata    json.RawMessage
	Enabled     bool
	Source      string
	ActorID     *int64
	Note        string
	CreatedAt   time.Time
}

// SkillUpdate is one validated partial skill edit. Nil fields keep the
// stored value; ExpectedVersion is an optional CAS guard, not a writable
// field.
type SkillUpdate struct {
	Description     *string
	Body            *string
	Metadata        json.RawMessage
	Enabled         *bool
	ExpectedVersion *int
}

// Empty reports whether the update carries no writable field.
func (u SkillUpdate) Empty() bool {
	return u.Description == nil && u.Body == nil && u.Metadata == nil && u.Enabled == nil
}

// ValidateSkillName rejects names outside the frozen package-name shape.
func ValidateSkillName(name string) error {
	if !SkillNamePattern.MatchString(name) {
		return platform.Invalid("name", "must match ^[a-z][a-z0-9_-]{0,63}$")
	}
	return nil
}

// ValidateSkillDescription bounds the optional metadata-layer description.
func ValidateSkillDescription(description string) error {
	if utf8.RuneCountInString(description) > MaxSkillDescriptionRunes {
		return platform.Invalid("description", "must not exceed 2000 characters")
	}
	return nil
}

// ValidateSkillBody requires a non-empty body within the rune limit.
func ValidateSkillBody(body string) error {
	if strings.TrimSpace(body) == "" {
		return platform.Invalid("body", "must not be empty")
	}
	if utf8.RuneCountInString(body) > MaxSkillBodyRunes {
		return platform.Invalid("body", "must not exceed 32768 characters")
	}
	return nil
}

// ValidateSkillMetadata accepts nil (defaults to {}) or a JSON object.
func ValidateSkillMetadata(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return platform.Invalid("metadata", "must be a JSON object")
	}
	return nil
}
