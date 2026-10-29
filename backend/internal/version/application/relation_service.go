package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// Relation is one typed edge between two requirements.
type Relation = domain.Relation

// maxRelationsPerVersion bounds one relation listing (spec §7.4: the relation
// list is not paginated but never returns more than 500 edges).
const maxRelationsPerVersion = 500

// CreateRelationCommand is the create-relation request (spec §7.4). Source
// records the caller: user for the HTTP API, ai for the agent relation tool.
type CreateRelationCommand struct {
	FromRequirementID int64
	ToRequirementID   int64
	RelationType      string
	Description       string
	Source            string
}

// RelationService implements the four relation types of spec §6.4. Relations are
// independent of requirement status (clarified requirements can be linked) and
// are validated for same-version endpoints, self-loops and normalized
// uniqueness; the SQL unique indexes are the race backstop.
type RelationService struct {
	repo   RequirementRepository
	events EventPublisher
	ids    IDGenerator
}

// RelationServiceOption customises the relation service dependencies.
type RelationServiceOption func(*RelationService)

// WithRelationIDGenerator injects the Snowflake id generator. Without it the
// package-wide DefaultIDGenerator is used (single-process safe).
func WithRelationIDGenerator(ids IDGenerator) RelationServiceOption {
	return func(s *RelationService) {
		if ids != nil {
			s.ids = ids
		}
	}
}

// WithRelationEventPublisher injects the version event publisher. T16 wires the
// shared publisher here so relation changes reach the version stream; without it
// the service still performs and persists the write.
func WithRelationEventPublisher(events EventPublisher) RelationServiceOption {
	return func(s *RelationService) {
		if events != nil {
			s.events = events
		}
	}
}

// NewRelationService wires the relation use cases to their repository.
func NewRelationService(repo RequirementRepository, opts ...RelationServiceOption) *RelationService {
	service := &RelationService{
		repo: repo,
		ids:  DefaultIDGenerator(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(service)
		}
	}
	return service
}

// Create inserts one relation between two requirements of the same version. The
// endpoints must exist and belong to versionID, links are never self-referential,
// and a duplicate of an existing edge (for undirected types regardless of
// direction) is a 409 duplicate_relation (spec §6.4/INV-7).
func (s *RelationService) Create(ctx context.Context, versionID int64, cmd CreateRelationCommand, actor int64) (*Relation, error) {
	relation := &Relation{
		ID:                s.ids.Next(),
		VersionID:         versionID,
		FromRequirementID: cmd.FromRequirementID,
		ToRequirementID:   cmd.ToRequirementID,
		RelationType:      cmd.RelationType,
		Description:       cmd.Description,
		Source:            requirementSourceOrDefault(cmd.Source),
		CreatedBy:         actor,
	}
	if relation.ID <= 0 {
		return nil, errors.New("version: id generator returned a non-positive id")
	}
	if err := relation.Validate(); err != nil {
		return nil, err
	}

	from, err := s.repo.Get(ctx, relation.FromRequirementID)
	if err != nil {
		return nil, err
	}
	to, err := s.repo.Get(ctx, relation.ToRequirementID)
	if err != nil {
		return nil, err
	}
	if from.VersionID != versionID || to.VersionID != versionID {
		return nil, domain.NewInvalid("", "relation_requirement_mismatch",
			"both requirements of a relation must belong to its version")
	}
	if err := s.ensureUniqueRelation(ctx, versionID, relation, 0); err != nil {
		return nil, err
	}

	if err := s.repo.CreateRelation(ctx, relation); err != nil {
		return nil, err
	}
	s.publish(ctx, VersionEvent{
		VersionID:        versionID,
		Type:             "requirement.relation_added",
		Summary:          "requirement relation added",
		RefRequirementID: requirementIDPtr(relation.FromRequirementID),
		ActorID:          actor,
		Source:           relation.Source,
		Detail: map[string]any{
			"relationId":        strconv.FormatInt(relation.ID, 10),
			"relationType":      relation.RelationType,
			"fromRequirementId": strconv.FormatInt(relation.FromRequirementID, 10),
			"toRequirementId":   strconv.FormatInt(relation.ToRequirementID, 10),
			"source":            relation.Source,
		},
	})
	return relation, nil
}

// Update rewrites a relation's type and/or description. Nil fields keep their
// current value (PATCH semantics: an omitted description does not clear it). An
// explicitly supplied empty or unknown type is rejected by validation. Edits are
// not limited by the status of the linked requirements. A change that collides
// with another edge is a duplicate_relation.
func (s *RelationService) Update(ctx context.Context, versionID, relID int64, relType, description *string, actor int64) (*Relation, error) {
	current, err := s.loadRelation(ctx, versionID, relID)
	if err != nil {
		return nil, err
	}

	candidate := *current
	if relType != nil {
		candidate.RelationType = *relType
	}
	if description != nil {
		candidate.Description = *description
	}
	if err := candidate.Validate(); err != nil {
		return nil, err
	}
	if err := s.ensureUniqueRelation(ctx, versionID, &candidate, relID); err != nil {
		return nil, err
	}

	updated, err := s.repo.UpdateRelation(ctx, relID, candidate.RelationType, candidate.Description)
	if err != nil {
		return nil, err
	}
	s.publish(ctx, VersionEvent{
		VersionID:        versionID,
		Type:             "requirement.relation_updated",
		Summary:          "requirement relation updated",
		RefRequirementID: requirementIDPtr(updated.FromRequirementID),
		ActorID:          actor,
		Source:           domain.SourceUser,
		Detail: map[string]any{
			"relationId":        strconv.FormatInt(updated.ID, 10),
			"relationType":      updated.RelationType,
			"fromRequirementId": strconv.FormatInt(updated.FromRequirementID, 10),
			"toRequirementId":   strconv.FormatInt(updated.ToRequirementID, 10),
		},
	})
	return updated, nil
}

// Delete removes one relation of the version.
func (s *RelationService) Delete(ctx context.Context, versionID, relID, actor int64) error {
	current, err := s.loadRelation(ctx, versionID, relID)
	if err != nil {
		return err
	}
	if err := s.repo.DeleteRelation(ctx, relID); err != nil {
		return err
	}
	s.publish(ctx, VersionEvent{
		VersionID:        versionID,
		Type:             "requirement.relation_removed",
		Summary:          "requirement relation removed",
		RefRequirementID: requirementIDPtr(current.FromRequirementID),
		ActorID:          actor,
		Source:           domain.SourceUser,
		Detail:           map[string]any{"relationId": strconv.FormatInt(relID, 10)},
	})
	return nil
}

// List returns the relations of one version ordered by id, capped at the
// spec §7.4 upper bound of 500.
func (s *RelationService) List(ctx context.Context, versionID int64) ([]Relation, error) {
	relations, err := s.repo.ListRelations(ctx, versionID)
	if err != nil {
		return nil, err
	}
	if len(relations) > maxRelationsPerVersion {
		relations = relations[:maxRelationsPerVersion]
	}
	return relations, nil
}

// loadRelation finds one relation inside its version. A relation of another
// version is reported as not found, matching the version-scoped path.
func (s *RelationService) loadRelation(ctx context.Context, versionID, relID int64) (*Relation, error) {
	relations, err := s.repo.ListRelations(ctx, versionID)
	if err != nil {
		return nil, err
	}
	for i := range relations {
		if relations[i].ID == relID {
			return &relations[i], nil
		}
	}
	return nil, domain.NewNotFound("relation", relID)
}

// ensureUniqueRelation rejects a relation that duplicates an existing edge of the
// version after normalization. The directed depends_on type compares (from, to,
// type); the other three types compare the order-independent pair plus type.
// excludeID skips the relation being updated.
func (s *RelationService) ensureUniqueRelation(ctx context.Context, versionID int64, relation *Relation, excludeID int64) error {
	relations, err := s.repo.ListRelations(ctx, versionID)
	if err != nil {
		return err
	}
	key := relationUniqueKey(relation)
	for i := range relations {
		if relations[i].ID == excludeID {
			continue
		}
		if relationUniqueKey(&relations[i]) == key {
			return domain.NewConflict("duplicate_relation", "an equivalent relation already exists")
		}
	}
	return nil
}

// relationUniqueKey normalizes a relation for duplicate detection: depends_on
// keeps its direction, undirected relation types sort the endpoints.
func relationUniqueKey(relation *Relation) string {
	from, to := relation.FromRequirementID, relation.ToRequirementID
	if relation.RelationType != domain.RelationDependsOn && from > to {
		from, to = to, from
	}
	return fmt.Sprintf("%s:%d:%d", relation.RelationType, from, to)
}

// publish emits one relation event after a committed write. Publication failures
// are logged and swallowed (spec §6.10).
func (s *RelationService) publish(ctx context.Context, event VersionEvent) {
	if s.events == nil {
		return
	}
	if err := s.events.Publish(ctx, event); err != nil {
		slog.WarnContext(ctx, "requirement relation event publish failed",
			"type", event.Type,
			"version_id", event.VersionID,
			"error", err,
		)
	}
}
