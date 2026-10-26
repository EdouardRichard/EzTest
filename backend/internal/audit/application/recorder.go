// Package application exposes the audit recorder port consumed by the other
// bounded contexts and the use case that records and pages through entries.
package application

import (
	"context"

	audit "github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
)

const (
	// defaultListLimit is used when a caller does not ask for a page size.
	defaultListLimit = 50
	// maxListLimit caps the page size so a client cannot request the whole
	// audit log at once.
	maxListLimit = 100
)

// Audit actions recorded in P0. Each name is stable because it is persisted and
// filtered on.
const (
	ActionLoginSucceeded    = "auth.login_succeeded"
	ActionLoginFailed       = "auth.login_failed"
	ActionLogout            = "auth.logout"
	ActionUserCreated       = "user.created"
	ActionUserUpdated       = "user.updated"
	ActionUserPasswordReset = "user.password_reset"

	// LLM configuration lifecycle. Secrets (API keys and header values) are
	// never part of these entries.
	ActionLLMConfigCreated = "llm.config_created"
	ActionLLMConfigUpdated = "llm.config_updated"
	ActionLLMConfigDeleted = "llm.config_deleted"
	// LLM connectivity tests. The entries carry the configuration id, model,
	// protocol, duration and (on failure) the public error code only; the key,
	// headers, prompt and image bytes are never recorded.
	ActionLLMTestSucceeded = "llm.test_succeeded"
	ActionLLMTestFailed    = "llm.test_failed"
)

// Recorder is the audit port consumed by the other bounded contexts. Recording
// is append-only; List reads newest first with an ID cursor. Implementations
// must not include credentials in an entry.
type Recorder interface {
	Record(ctx context.Context, entry audit.Entry) error
	List(ctx context.Context, limit int, afterID int64) ([]audit.Entry, bool, error)
}

// NopRecorder is a Recorder that discards every entry. It is used by tests and
// by wiring that does not record audits.
type NopRecorder struct{}

// Record implements Recorder.
func (NopRecorder) Record(context.Context, audit.Entry) error { return nil }

// List implements Recorder.
func (NopRecorder) List(context.Context, int, int64) ([]audit.Entry, bool, error) {
	return nil, false, nil
}

// Service is the Recorder backed by the PostgreSQL repository. It enforces the
// page-size bounds so a caller cannot bypass them.
type Service struct {
	repo audit.Repository
}

// NewService returns a recorder that persists entries through repo.
func NewService(repo audit.Repository) *Service {
	return &Service{repo: repo}
}

// Record appends one entry.
func (s *Service) Record(ctx context.Context, entry audit.Entry) error {
	return s.repo.Record(ctx, entry)
}

// List returns a page of entries newest first. Non-positive limits fall back to
// the default page size and oversized limits are capped.
func (s *Service) List(ctx context.Context, limit int, afterID int64) ([]audit.Entry, bool, error) {
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	return s.repo.List(ctx, limit, afterID)
}
