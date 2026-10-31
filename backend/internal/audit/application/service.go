package auditapp

import (
	"context"

	"github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

const (
	// DefaultListLimit is used when a caller does not ask for a page size.
	DefaultListLimit = 20
	// MaxListLimit caps the page size so a client cannot request the whole log.
	MaxListLimit = 100
)

// Audit actions recorded in P0. Each name is stable because it is persisted and
// filtered on.
const (
	ActionLogin             = "auth.login"
	ActionLoginFailed       = "auth.login_failed"
	ActionLogout            = "auth.logout"
	ActionPasswordChanged   = "auth.password_changed"
	ActionUserCreated       = "user.created"
	ActionUserUpdated       = "user.updated"
	ActionUserPasswordReset = "user.password_reset"
)

// Filter narrows one audit page. It is an alias of the domain filter so
// callers only need this package.
type Filter = audit.Filter

// Service records and pages through audit entries.
type Service struct {
	repo  audit.Repository
	ids   id.Generator
	clock clock.Clock
}

// New wires an audit service.
func New(repo audit.Repository, ids id.Generator, clk clock.Clock) *Service {
	return &Service{repo: repo, ids: ids, clock: clk}
}

// Record appends one entry, filling the generated ID and timestamp. Recording
// is best-effort for callers: business services ignore the returned error and
// must not fail their operation because auditing failed.
func (s *Service) Record(ctx context.Context, event Event) error {
	metadata := event.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	return s.repo.Insert(ctx, audit.Entry{
		ID:         s.ids.Next(),
		ActorID:    event.ActorID,
		Action:     event.Action,
		TargetKind: event.TargetKind,
		TargetID:   event.TargetID,
		RequestID:  event.RequestID,
		IP:         event.IP,
		UserAgent:  event.UserAgent,
		Metadata:   metadata,
		CreatedAt:  s.clock.Now().UTC(),
	})
}

// List returns one page of entries newest first and reports whether an older
// page exists. Non-positive limits fall back to DefaultListLimit and oversized
// limits are capped at MaxListLimit.
func (s *Service) List(ctx context.Context, filter Filter, limit int, beforeID *int64) ([]audit.Entry, bool, error) {
	if limit <= 0 {
		limit = DefaultListLimit
	}
	if limit > MaxListLimit {
		limit = MaxListLimit
	}
	entries, err := s.repo.List(ctx, filter, limit+1, beforeID)
	if err != nil {
		return nil, false, err
	}
	hasMore := len(entries) > limit
	if hasMore {
		entries = entries[:limit]
	}
	return entries, hasMore, nil
}
