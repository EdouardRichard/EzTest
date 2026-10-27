package application_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// svcFakeProfileRepository is an in-memory domain.ProfileRepository that
// records update calls and can return programmable errors.
type svcFakeProfileRepository struct {
	profiles  []domain.AgentProfile
	listErr   error
	getErr    error
	updateErr error
	updates   []domain.UpdateParams
}

// List returns the configured profiles.
func (r *svcFakeProfileRepository) List(context.Context) ([]domain.AgentProfile, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	return append([]domain.AgentProfile(nil), r.profiles...), nil
}

// Get returns the profile with id or ErrNotFound.
func (r *svcFakeProfileRepository) Get(_ context.Context, id int64) (domain.AgentProfile, error) {
	if r.getErr != nil {
		return domain.AgentProfile{}, r.getErr
	}
	for _, profile := range r.profiles {
		if profile.ID == id {
			return profile, nil
		}
	}
	return domain.AgentProfile{}, domain.ErrNotFound
}

// Update records params and applies them to the matching profile, mirroring the
// PostgreSQL repository's validate-on-write behavior.
func (r *svcFakeProfileRepository) Update(_ context.Context, id int64, params domain.UpdateParams) (domain.AgentProfile, error) {
	r.updates = append(r.updates, params)
	if r.updateErr != nil {
		return domain.AgentProfile{}, r.updateErr
	}
	for _, profile := range r.profiles {
		if profile.ID != id {
			continue
		}
		if err := profile.Apply(params); err != nil {
			return domain.AgentProfile{}, err
		}
		return profile, nil
	}
	return domain.AgentProfile{}, domain.ErrNotFound
}

// EnsureBuiltin is unused by the profile service.
func (r *svcFakeProfileRepository) EnsureBuiltin(context.Context, domain.AgentProfile) (bool, error) {
	return false, nil
}

// svcFakeAuditSink captures the audit events a service records.
type svcFakeAuditSink struct {
	mu     sync.Mutex
	events []application.AuditEvent
	err    error
}

// Record implements application.AuditSink.
func (s *svcFakeAuditSink) Record(_ context.Context, event application.AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
	return s.err
}

// recorded returns a copy of the captured events.
func (s *svcFakeAuditSink) recorded() []application.AuditEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]application.AuditEvent(nil), s.events...)
}

// svcTestProfile returns a valid stored profile with id.
func svcTestProfile(id int64) domain.AgentProfile {
	profile := domain.BuiltinProfile()
	profile.ID = id
	return profile
}

func TestProfileUpdateValidatesAndAudits(t *testing.T) {
	invalidTurns := 101
	t.Run("invalid params are rejected before the repository and the audit sink", func(t *testing.T) {
		repo := &svcFakeProfileRepository{profiles: []domain.AgentProfile{svcTestProfile(1)}}
		audit := &svcFakeAuditSink{}
		svc := application.NewProfileService(repo, audit)

		if _, err := svc.Update(context.Background(), 1, domain.UpdateParams{MaxTurns: &invalidTurns}, nil); err == nil {
			t.Fatal("Update(max_turns=101) returned nil error, want a validation error")
		} else if !strings.Contains(err.Error(), "max_turns") {
			t.Fatalf("Update error=%q, want it to name max_turns", err)
		}
		if len(repo.updates) != 0 {
			t.Fatalf("repository Update called %d times, want 0", len(repo.updates))
		}
		if events := audit.recorded(); len(events) != 0 {
			t.Fatalf("audit recorded %d events, want 0", len(events))
		}
	})

	t.Run("valid params reach the repository and audit only key and field names", func(t *testing.T) {
		repo := &svcFakeProfileRepository{profiles: []domain.AgentProfile{svcTestProfile(1)}}
		audit := &svcFakeAuditSink{}
		svc := application.NewProfileService(repo, audit)
		prompt := "更新后的系统提示词"
		maxTurns := 5
		actor := int64(7)

		updated, err := svc.Update(context.Background(), 1, domain.UpdateParams{
			SystemPrompt: &prompt,
			MaxTurns:     &maxTurns,
		}, &actor)
		if err != nil {
			t.Fatalf("Update returned error: %v", err)
		}
		if updated.SystemPrompt != prompt || updated.MaxTurns != maxTurns {
			t.Fatalf("Update returned %+v, want the applied values", updated)
		}
		if len(repo.updates) != 1 {
			t.Fatalf("repository Update called %d times, want 1", len(repo.updates))
		}

		events := audit.recorded()
		if len(events) != 1 {
			t.Fatalf("audit recorded %d events, want 1", len(events))
		}
		event := events[0]
		if event.Action != "agent.profile.updated" {
			t.Fatalf("audit action=%q want agent.profile.updated", event.Action)
		}
		if event.TargetType != "agent_profile" || event.TargetID != "1" {
			t.Fatalf("audit target=%q/%q want agent_profile/1", event.TargetType, event.TargetID)
		}
		if event.ActorUserID == nil || *event.ActorUserID != actor {
			t.Fatalf("audit actor=%v want %d", event.ActorUserID, actor)
		}
		if len(event.Metadata) != 2 {
			t.Fatalf("audit metadata=%v want exactly profile_key and fields", event.Metadata)
		}
		if got := event.Metadata["profile_key"]; got != "runtime-diagnostic" {
			t.Fatalf("metadata[profile_key]=%v want runtime-diagnostic", got)
		}
		fields, ok := event.Metadata["fields"].([]string)
		if !ok {
			t.Fatalf("metadata[fields]=%v want []string", event.Metadata["fields"])
		}
		if !reflect.DeepEqual(fields, []string{"system_prompt", "max_turns"}) {
			t.Fatalf("metadata[fields]=%v want [system_prompt max_turns]", fields)
		}
	})

	t.Run("missing profile is reported as not found", func(t *testing.T) {
		repo := &svcFakeProfileRepository{}
		svc := application.NewProfileService(repo, &svcFakeAuditSink{})
		prompt := "x"

		if _, err := svc.Update(context.Background(), 99, domain.UpdateParams{SystemPrompt: &prompt}, nil); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("Update(missing) error=%v want domain.ErrNotFound", err)
		}
		if len(repo.updates) != 0 {
			t.Fatalf("repository Update called %d times, want 0", len(repo.updates))
		}
	})
}
