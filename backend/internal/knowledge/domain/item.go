// Package knowledge is the P7 cross-version knowledge domain: scoped knowledge
// items, their review state machine, append-only content revisions, injection
// budget estimates and usage rows. It is pure: no I/O and no dependency on
// another bounded context.
package knowledge

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// Scope is the visibility scope of a knowledge item (contracts/db-schema.md
// §16, spec §4.3). global items are visible to every version; system items are
// visible only to versions bound to a SUT with the matching family.
type Scope string

// Knowledge scopes.
const (
	ScopeGlobal Scope = "global"
	ScopeSystem Scope = "system"
)

// Status is the review state of a knowledge item. `stale` is intentionally not
// a status: source staleness is computed at read time.
type Status string

// Knowledge review statuses.
const (
	StatusDraft    Status = "draft"
	StatusApproved Status = "approved"
	StatusRejected Status = "rejected"
	StatusRevoked  Status = "revoked"
)

// Physical limits of the frozen P7 contract.
const (
	MaxTitleRunes     = 200
	MaxContentRunes   = 20000
	MaxTags           = 20
	MaxTagRunes       = 40
	MaxSystemKeyRunes = 64
	MaxReasonRunes    = 2000

	// SnippetMaxRunes caps the excerpt handed to a prompt so one long item
	// cannot dominate the injected section.
	SnippetMaxRunes = 500
)

// Valid reports whether the value is a supported scope.
func (s Scope) Valid() bool { return s == ScopeGlobal || s == ScopeSystem }

// Valid reports whether the value is a supported review status.
func (s Status) Valid() bool {
	switch s {
	case StatusDraft, StatusApproved, StatusRejected, StatusRevoked:
		return true
	default:
		return false
	}
}

// CanTransition reports whether the review state machine allows from -> to
// (spec §4.3): draft -> approved|rejected, approved -> revoked,
// revoked -> approved (re-review) and rejected -> draft (edit and resubmit).
func CanTransition(from, to Status) bool {
	switch from {
	case StatusDraft:
		return to == StatusApproved || to == StatusRejected
	case StatusApproved:
		return to == StatusRevoked
	case StatusRevoked:
		return to == StatusApproved
	case StatusRejected:
		return to == StatusDraft
	default:
		return false
	}
}

// Item is one knowledge_item row plus the read-time SourceStale marker.
type Item struct {
	ID                    int64
	Scope                 Scope
	SystemKey             *string
	Title                 string
	Content               string
	Tags                  []string
	Status                Status
	Confidence            *float64
	SourceVersionID       *int64
	PromotedFromVersionID *int64
	Revision              int
	ReviewReason          *string
	ReviewedBy            *int64
	ReviewedAt            *time.Time
	RevokedAt             *time.Time
	CreatedBy             *int64
	CreatedAt             time.Time
	UpdatedAt             time.Time
	// SourceStale is computed by the application on reads: it is true when
	// SourceVersionID no longer exists. It is never persisted and never a
	// status.
	SourceStale bool
}

// Revision is one append-only knowledge_revision row.
type Revision struct {
	ID          int64
	KnowledgeID int64
	Revision    int
	Title       string
	Content     string
	Tags        []string
	ChangedBy   *int64
	CreatedAt   time.Time
}

// Usage is one knowledge_usage row written after a prompt injection.
type Usage struct {
	ID            int64
	KnowledgeID   int64
	AgentRunID    *int64
	WorkItemID    *int64
	TokenEstimate int
	InjectedAt    time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// TokenEstimate is the deterministic prompt budget estimate of the whole item:
// (len(title) + len(content) + sum(len(tag))) / 3, with a floor of one. Byte
// lengths make the estimate conservative for CJK content (three bytes ≈ one
// token) without requiring a tokenizer.
func (i Item) TokenEstimate() int {
	size := len(i.Title) + len(i.Content)
	for _, tag := range i.Tags {
		size += len(tag)
	}
	estimate := size / 3
	if estimate < 1 {
		return 1
	}
	return estimate
}

// Snippet returns the excerpt injected into a prompt. Long content is cut at
// SnippetMaxRunes and marked with an ellipsis; the full content never leaves
// the knowledge API through the injection path.
func (i Item) Snippet() string {
	runes := []rune(i.Content)
	if len(runes) <= SnippetMaxRunes {
		return i.Content
	}
	return string(runes[:SnippetMaxRunes]) + "…"
}

// NormalizeTitle trims and validates the title length (1–200 characters).
func NormalizeTitle(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	length := utf8.RuneCountInString(value)
	if length < 1 || length > MaxTitleRunes {
		return "", platform.Invalid("title", "title must be between 1 and 200 characters")
	}
	return value, nil
}

// NormalizeContent trims and validates the content length (1–20000
// characters).
func NormalizeContent(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	length := utf8.RuneCountInString(value)
	if length < 1 || length > MaxContentRunes {
		return "", platform.Invalid("content", "content must be between 1 and 20000 characters")
	}
	return value, nil
}

// NormalizeTags trims every tag, rejects empty or oversized tags, removes
// duplicates keeping the first occurrence and enforces the 20-tag cap after
// dedupe. It always returns a non-nil slice so the NOT NULL column stays an
// empty array rather than NULL.
func NormalizeTags(raw []string) ([]string, error) {
	tags := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		tag := strings.TrimSpace(item)
		if tag == "" {
			return nil, platform.Invalid("tags", "tags must not contain empty items")
		}
		if utf8.RuneCountInString(tag) > MaxTagRunes {
			return nil, platform.Invalid("tags", "each tag must be at most 40 characters")
		}
		if _, duplicate := seen[tag]; duplicate {
			continue
		}
		seen[tag] = struct{}{}
		tags = append(tags, tag)
	}
	if len(tags) > MaxTags {
		return nil, platform.Invalid("tags", "must contain at most 20 tags")
	}
	return tags, nil
}

// NormalizeScope validates the scope/system_key combination of spec §4.3 and
// returns the normalized key (nil for global items).
func NormalizeScope(scope Scope, systemKey *string) (Scope, *string, error) {
	if !scope.Valid() {
		return "", nil, platform.Invalid("scope", "scope must be global or system")
	}
	key := ""
	if systemKey != nil {
		key = strings.TrimSpace(*systemKey)
	}
	if scope == ScopeGlobal {
		if key != "" {
			return "", nil, platform.Invalid("system_key", "must be empty when scope is global")
		}
		return ScopeGlobal, nil, nil
	}
	if key == "" {
		return "", nil, platform.Invalid("system_key", "is required when scope is system")
	}
	if utf8.RuneCountInString(key) > MaxSystemKeyRunes {
		return "", nil, platform.Invalid("system_key", "must be at most 64 characters")
	}
	if key != strings.ToLower(key) {
		return "", nil, platform.Invalid("system_key", "must be lowercase")
	}
	if strings.ContainsFunc(key, unicode.IsSpace) {
		return "", nil, platform.Invalid("system_key", "must not contain whitespace")
	}
	return ScopeSystem, &key, nil
}

// Validate applies every physical rule of a knowledge item: the
// scope/system_key combination and the title/content/tags bounds.
func Validate(scope Scope, systemKey *string, title, content string, tags []string) error {
	if _, _, err := NormalizeScope(scope, systemKey); err != nil {
		return err
	}
	if _, err := NormalizeTitle(title); err != nil {
		return err
	}
	if _, err := NormalizeContent(content); err != nil {
		return err
	}
	_, err := NormalizeTags(tags)
	return err
}

// ValidateReason trims and validates a review reason (1–2000 characters).
func ValidateReason(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	length := utf8.RuneCountInString(value)
	if length < 1 {
		return "", platform.Invalid("reason", "reason is required")
	}
	if length > MaxReasonRunes {
		return "", platform.Invalid("reason", "must be at most 2000 characters")
	}
	return value, nil
}
