// Package knowledgeapp implements the P7 knowledge use cases: the draft/review
// lifecycle, scoped full-text listing, append-only revisions, prompt injection
// with settings-driven budget and usage accounting. It depends only on the
// knowledge domain and on the ports declared in this file; adapters live in
// infrastructure/postgres and the assembly root.
package knowledgeapp

import (
	"context"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	knowledge "github.com/EdouardRichard/EzTest/backend/internal/knowledge/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// Settings keys consumed by the knowledge context (contracts/settings.md).
const (
	SettingInjectionMaxItems     = "knowledge.injection_max_items"
	SettingInjectionBudgetTokens = "knowledge.injection_budget_tokens"
	SettingReviewRequired        = "knowledge.review_required"
)

// Documented defaults used when the settings reader is unwired or errors.
const (
	DefaultInjectionMaxItems     = 5
	DefaultInjectionBudgetTokens = 4000
	MaxInjectionMaxItems         = 50
)

// Knowledge list limits of the frozen P7 contract.
const (
	DefaultListLimit = 20
	MaxListLimit     = 100
)

// Audit actions. The update action is contractually named knowledge.edit
// (contracts/events.md §16.2) even though the API verb is PATCH.
const (
	ActionCreate  = "knowledge.create"
	ActionUpdate  = "knowledge.edit"
	ActionApprove = "knowledge.approve"
	ActionReject  = "knowledge.reject"
	ActionRevoke  = "knowledge.revoke"
)

// Repository is the item persistence port (owned by the domain).
type Repository = knowledge.Repository

// UsageRepository appends knowledge_usage rows. Insert must be best-effort
// from the caller's point of view; the caller ignores the returned error.
type UsageRepository interface {
	Insert(ctx context.Context, usages []knowledge.Usage) error
}

// SettingsReader is the knowledge view of the settings centre.
type SettingsReader interface {
	Int(ctx context.Context, key string) (int64, error)
	Bool(ctx context.Context, key string) (bool, error)
}

// AuditRecorder is the best-effort audit sink. Implementations must not fail
// the caller's business operation; adapter metadata carries actor_id and the
// contract's knowledge metadata (never the item body).
type AuditRecorder interface {
	Record(ctx context.Context, action string, targetID int64, metadata map[string]any) error
}

// EventPublisher publishes the knowledge.* events on the system channel.
type EventPublisher interface {
	Publish(ctx context.Context, event eventingapp.Event) error
}

// SourceVersionReader answers whether a knowledge item's source version still
// exists. It is the cross-context read used for the read-time source_stale
// marker; a nil reader leaves everything not stale.
type SourceVersionReader interface {
	SourceVersionExists(ctx context.Context, versionID int64) (bool, error)
}

// Clock is the injectable time source.
type Clock = clock.Clock

// IDGenerator is the shared snowflake generator.
type IDGenerator = platformid.Generator

// ListFilter narrows one knowledge page. Zero-value string fields mean no
// filter; Limit is normalized by the service.
type ListFilter struct {
	Scope     knowledge.Scope
	Status    knowledge.Status
	SystemKey string
	Tag       string
	Query     string
	Cursor    int64
	Limit     int
}

// Page is one id-desc page. NextCursor is the last returned item id and is
// meaningful only when HasMore is true.
type Page struct {
	Items      []knowledge.Item
	NextCursor int64
	HasMore    bool
}

// CreateInput is one POST /api/knowledge/items request (or a knowledge.propose
// tool call after conversion). AgentRunID is optional audit metadata.
type CreateInput struct {
	Title                 string
	Content               string
	Scope                 knowledge.Scope
	SystemKey             *string
	Tags                  []string
	SourceVersionID       *int64
	PromotedFromVersionID *int64
	AgentRunID            *int64
}

// UpdateInput is one PATCH /api/knowledge/items/{id} request. Nil pointers
// keep the stored value; a nil SystemKey also keeps the stored key (switching
// to scope=global clears it automatically).
type UpdateInput struct {
	Title     *string
	Content   *string
	Tags      *[]string
	Scope     *knowledge.Scope
	SystemKey *string
}

// InjectInput is one prompt-injection request built by the version adapter.
// MaxItems/BudgetTokens override the settings when positive; zero reads the
// settings centre.
type InjectInput struct {
	VersionID    int64
	SystemKeys   []string
	Query        string
	MaxItems     int
	BudgetTokens int
}

// Injected is one approved knowledge item admitted into a prompt, with the
// reference ID, bounded snippet and token estimate (contracts/agent-profiles-
// tools.md §10.3).
type Injected struct {
	ID      int64
	Title   string
	Snippet string
	Scope   knowledge.Scope
	Tokens  int
}
