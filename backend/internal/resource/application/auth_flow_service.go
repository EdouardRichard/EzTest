package application

import (
	"context"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// AuthFlowService implements the login recipe (auth_flow) use cases. Admin-only
// access is enforced by the HTTP guard; the service only validates the recipe so
// every entry is storable and executable (spec §3.4, §5.11).
type AuthFlowService struct {
	flows   domain.AuthFlowRepository
	systems domain.SystemRepository
}

// CreateAuthFlowInput is the create-recipe request. Kind defaults to "form"
// when omitted; an omitted enabled flag is adopted from the HTTP layer's DDL
// default.
type CreateAuthFlowInput struct {
	SystemID   int64
	Name       string
	Kind       string
	Definition domain.FormDefinition
	Enabled    bool
}

// UpdateAuthFlowInput is the partial update-recipe request. A nil field is left
// unchanged; system and ID are immutable.
type UpdateAuthFlowInput struct {
	Name       *string
	Kind       *string
	Definition *domain.FormDefinition
	Enabled    *bool
}

// NewAuthFlowService wires the login recipe use cases to their repositories.
func NewAuthFlowService(flows domain.AuthFlowRepository, systems domain.SystemRepository) *AuthFlowService {
	return &AuthFlowService{flows: flows, systems: systems}
}

// invalidAuthFlow returns the shared 422 auth_flow_invalid error.
func invalidAuthFlow(field, message string) *domain.Error {
	return domain.NewInvalid(field, "auth_flow_invalid", message)
}

// normalizeAuthFlowKind defaults an omitted kind to the only supported value
// and rejects everything else.
func normalizeAuthFlowKind(kind string) (string, error) {
	if kind == "" {
		return domain.AuthFlowKindForm, nil
	}
	if err := domain.ValidateAuthFlowKind(kind); err != nil {
		return "", err
	}
	return kind, nil
}

// validateAuthFlowName trims the recipe name and rejects an empty one.
func validateAuthFlowName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", invalidAuthFlow("name", "name must not be empty")
	}
	return trimmed, nil
}

// Get returns one login recipe by id.
func (s *AuthFlowService) Get(ctx context.Context, id int64) (*domain.AuthFlow, error) {
	return s.flows.GetAuthFlow(ctx, id)
}

// List returns one cursor page of login recipes.
func (s *AuthFlowService) List(ctx context.Context, f domain.AuthFlowFilter) ([]domain.AuthFlow, bool, error) {
	return s.flows.ListAuthFlows(ctx, f)
}

// Create validates and stores a login recipe of an existing system.
func (s *AuthFlowService) Create(ctx context.Context, in CreateAuthFlowInput) (*domain.AuthFlow, error) {
	if in.SystemID <= 0 {
		return nil, invalidAuthFlow("system_id", "system_id must be a positive integer")
	}
	name, err := validateAuthFlowName(in.Name)
	if err != nil {
		return nil, err
	}
	kind, err := normalizeAuthFlowKind(in.Kind)
	if err != nil {
		return nil, err
	}
	if err := in.Definition.Validate(); err != nil {
		return nil, err
	}
	if _, err := s.systems.GetSystem(ctx, in.SystemID); err != nil {
		return nil, err
	}

	flow := &domain.AuthFlow{
		SystemID:   in.SystemID,
		Name:       name,
		Kind:       kind,
		Definition: in.Definition,
		Enabled:    in.Enabled,
	}
	if err := s.flows.CreateAuthFlow(ctx, flow); err != nil {
		return nil, err
	}
	return flow, nil
}

// Update applies a partial update to an existing login recipe.
func (s *AuthFlowService) Update(ctx context.Context, id int64, in UpdateAuthFlowInput) (*domain.AuthFlow, error) {
	flow, err := s.flows.GetAuthFlow(ctx, id)
	if err != nil {
		return nil, err
	}
	if in.Name != nil {
		name, err := validateAuthFlowName(*in.Name)
		if err != nil {
			return nil, err
		}
		flow.Name = name
	}
	if in.Kind != nil {
		kind, err := normalizeAuthFlowKind(*in.Kind)
		if err != nil {
			return nil, err
		}
		flow.Kind = kind
	}
	if in.Definition != nil {
		if err := in.Definition.Validate(); err != nil {
			return nil, err
		}
		flow.Definition = *in.Definition
	}
	if in.Enabled != nil {
		flow.Enabled = *in.Enabled
	}
	if err := s.flows.UpdateAuthFlow(ctx, flow); err != nil {
		return nil, err
	}
	return flow, nil
}

// Delete removes one login recipe; accesses pointing at it fall back to NULL
// through the database ON DELETE SET NULL.
func (s *AuthFlowService) Delete(ctx context.Context, id int64) error {
	return s.flows.DeleteAuthFlow(ctx, id)
}
