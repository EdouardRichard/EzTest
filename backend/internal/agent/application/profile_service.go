package application

import (
	"context"
	"log/slog"
	"strconv"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// ActionProfileUpdated is the audit action recorded for a profile PATCH. The
// entry carries only the profile key and the names of the changed fields, never
// their values.
const ActionProfileUpdated = "agent.profile.updated"

// ProfileService is the profile use case: list, read and partially update the
// configured Agent profiles. Every update is re-validated with the domain rules
// before it reaches the repository, because the HTTP DTO is not the authority on
// what a valid profile is.
type ProfileService struct {
	profiles domain.ProfileRepository
	audit    AuditSink
}

// NewProfileService returns the profile service. A nil audit sink disables
// auditing (used by tests and partially wired processes).
func NewProfileService(profiles domain.ProfileRepository, audit AuditSink) *ProfileService {
	return &ProfileService{profiles: profiles, audit: audit}
}

// List returns every profile ordered by ID ascending.
func (s *ProfileService) List(ctx context.Context) ([]domain.AgentProfile, error) {
	return s.profiles.List(ctx)
}

// Get returns one profile or domain.ErrNotFound.
func (s *ProfileService) Get(ctx context.Context, id int64) (domain.AgentProfile, error) {
	return s.profiles.Get(ctx, id)
}

// Update applies a partial update to the stored profile. The merged profile is
// validated before the repository writes it; a failed validation leaves the
// stored row untouched. A successful update records agent.profile.updated with
// the profile key and the changed field names.
func (s *ProfileService) Update(ctx context.Context, id int64, params domain.UpdateParams, actor *int64) (domain.AgentProfile, error) {
	profile, err := s.profiles.Get(ctx, id)
	if err != nil {
		return domain.AgentProfile{}, err
	}
	if err := profile.Apply(params); err != nil {
		return domain.AgentProfile{}, err
	}
	updated, err := s.profiles.Update(ctx, id, params)
	if err != nil {
		return domain.AgentProfile{}, err
	}
	s.recordProfileAudit(ctx, updated, profileUpdatedFields(params), actor)
	return updated, nil
}

// recordProfileAudit appends the agent.profile.updated entry. Auditing must
// never fail the request, so a sink error is only logged. The metadata holds no
// prompt, model or tool values: only the stable key and the changed field names.
func (s *ProfileService) recordProfileAudit(ctx context.Context, profile domain.AgentProfile, fields []string, actor *int64) {
	if s.audit == nil {
		return
	}
	event := AuditEvent{
		Action:      ActionProfileUpdated,
		ActorUserID: actor,
		TargetType:  "agent_profile",
		TargetID:    strconv.FormatInt(profile.ID, 10),
		Metadata: map[string]any{
			"profile_key": profile.Key,
			"fields":      fields,
		},
	}
	if err := s.audit.Record(ctx, event); err != nil {
		slog.ErrorContext(ctx, "agent: record profile update audit entry",
			"profile_key", profile.Key, "error", err)
	}
}

// profileUpdatedFields returns the API field names carried by params, in the
// profile's field order. A nil pointer means "not part of this PATCH".
func profileUpdatedFields(params domain.UpdateParams) []string {
	fields := make([]string, 0, 10)
	if params.SystemPrompt != nil {
		fields = append(fields, "system_prompt")
	}
	if params.Model != nil {
		fields = append(fields, "model")
	}
	if params.AllowedTools != nil {
		fields = append(fields, "allowed_tools")
	}
	if params.MaxTurns != nil {
		fields = append(fields, "max_turns")
	}
	if params.RunSeconds != nil {
		fields = append(fields, "run_seconds")
	}
	if params.HardTimeoutSeconds != nil {
		fields = append(fields, "hard_timeout_seconds")
	}
	if params.Thinking != nil {
		fields = append(fields, "thinking")
	}
	if params.ReasoningEffort != nil {
		fields = append(fields, "reasoning_effort")
	}
	if params.MaxOutputTokens != nil {
		fields = append(fields, "max_output_tokens")
	}
	if params.Enabled != nil {
		fields = append(fields, "enabled")
	}
	return fields
}
