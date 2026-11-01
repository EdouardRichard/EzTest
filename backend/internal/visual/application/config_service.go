package visualapp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	visual "github.com/EdouardRichard/EzTest/backend/internal/visual/domain"
)

// ConfigServiceDeps wires the visual assertion configuration service.
type ConfigServiceDeps struct {
	Repo     AssertionRepository
	Cases    CaseReader
	Settings SettingsReader
	Clock    Clock
	IDs      IDGenerator
	Audit    AuditRecorder
}

// ConfigService owns visual_assertion configuration: validation, defaults,
// revision increments and the transactional baseline supersede.
type ConfigService struct {
	repo     AssertionRepository
	cases    CaseReader
	settings SettingsReader
	clock    Clock
	ids      IDGenerator
	audit    AuditRecorder
}

// NewConfigService wires the configuration service.
func NewConfigService(deps ConfigServiceDeps) *ConfigService {
	return &ConfigService{
		repo:     deps.Repo,
		cases:    deps.Cases,
		settings: deps.Settings,
		clock:    deps.Clock,
		ids:      deps.IDs,
		audit:    deps.Audit,
	}
}

// Upsert creates or updates the visual configuration of one case_assertion.
// Only an effective configuration change increments config_revision and
// supersedes the approved baseline; identical values are a no-op.
func (s *ConfigService) Upsert(ctx context.Context, versionID, caseAssertionID, actorID int64, in visual.ConfigInput) (visual.Assertion, error) {
	if versionID <= 0 {
		return visual.Assertion{}, platform.Invalid("version_id", "must be a positive ID")
	}
	if caseAssertionID <= 0 {
		return visual.Assertion{}, platform.Invalid("case_assertion_id", "must be a positive ID")
	}
	if s.cases == nil {
		return visual.Assertion{}, errors.New("visual config service has no case reader")
	}
	ref, err := s.cases.VisualAssertionContext(ctx, versionID, caseAssertionID)
	if err != nil {
		return visual.Assertion{}, mapVisualError(err, "case_assertion")
	}
	if ref.Kind != "visual" {
		return visual.Assertion{}, configInvalid("case_assertion_id", "the design assertion is not of kind visual")
	}
	versionSUTID := ref.TargetVersionSUTID
	if versionSUTID <= 0 {
		versionSUTID = ref.DefaultVersionSUTID
	}
	if in.VersionSUTID != nil {
		versionSUTID = *in.VersionSUTID
	}
	if versionSUTID <= 0 {
		return visual.Assertion{}, configInvalid("version_sut_id", "the case assertion has no bound version_sut")
	}

	current := visual.Assertion{
		VersionID:       versionID,
		CaseVersionID:   ref.CaseVersionID,
		CaseAssertionID: ref.CaseAssertionID,
		StepSeq:         ref.StepSeq,
		AssertionSeq:    ref.AssertionSeq,
		VersionSUTID:    versionSUTID,
		Status:          visual.AssertionActive,
	}
	existing, found, err := s.repo.GetByCaseAssertion(ctx, versionID, caseAssertionID)
	if err != nil {
		return visual.Assertion{}, mapVisualError(err, "visual_assertion")
	}
	if found {
		current = existing
	}
	if current.VersionSUTID <= 0 {
		current.VersionSUTID = versionSUTID
	}

	next, changed, err := current.Apply(in, s.defaults(ctx), found)
	if err != nil {
		return visual.Assertion{}, configInvalidFrom(err)
	}
	if found && !changed {
		return current, nil
	}
	now := s.now()
	next.UpdatedAt = now
	if !found {
		next.ConfigRevision = 1
		next.CreatedAt = now
		if actorID > 0 {
			next.CreatedBy = &actorID
		}
		if s.ids == nil {
			return visual.Assertion{}, errors.New("visual config service has no ID generator")
		}
		next.ID = s.ids.Next()
		created, createErr := s.repo.Create(ctx, next)
		if createErr != nil {
			return visual.Assertion{}, mapVisualError(createErr, "visual_assertion")
		}
		s.recordAudit(ctx, actorID, "visual.assertion.create", created.ID, map[string]any{
			"version_id":        platformid.Format(versionID),
			"case_assertion_id": platformid.Format(caseAssertionID),
			"config_revision":   created.ConfigRevision,
		})
		return created, nil
	}
	next.ID = existing.ID
	next.CreatedAt = existing.CreatedAt
	next.ConfigRevision = existing.ConfigRevision + 1
	updated, updateErr := s.repo.UpdateWithRevision(ctx, next, now)
	if updateErr != nil {
		return visual.Assertion{}, mapVisualError(updateErr, "visual_assertion")
	}
	s.recordAudit(ctx, actorID, "visual.assertion.update", updated.ID, map[string]any{
		"version_id":        platformid.Format(versionID),
		"case_assertion_id": platformid.Format(caseAssertionID),
		"config_revision":   updated.ConfigRevision,
	})
	return updated, nil
}

// List returns the visual assertions of one version with optional filters.
func (s *ConfigService) List(ctx context.Context, versionID int64, caseVersionID, versionSUTID *int64) ([]visual.Assertion, error) {
	if versionID <= 0 {
		return nil, platform.Invalid("version_id", "must be a positive ID")
	}
	if caseVersionID != nil && *caseVersionID <= 0 {
		return nil, platform.Invalid("case_version_id", "must be a positive ID")
	}
	if versionSUTID != nil && *versionSUTID <= 0 {
		return nil, platform.Invalid("version_sut_id", "must be a positive ID")
	}
	list, err := s.repo.List(ctx, versionID, caseVersionID, versionSUTID)
	if err != nil {
		return nil, mapVisualError(err, "visual_assertion")
	}
	return list, nil
}

// Get returns one visual assertion by ID.
func (s *ConfigService) Get(ctx context.Context, id int64) (visual.Assertion, error) {
	if id <= 0 {
		return visual.Assertion{}, platform.Invalid("id", "must be a positive ID")
	}
	assertion, err := s.repo.Get(ctx, id)
	if err != nil {
		return visual.Assertion{}, mapVisualError(err, "visual_assertion")
	}
	return assertion, nil
}

// GetByCaseAssertion returns the configuration of one design assertion.
func (s *ConfigService) GetByCaseAssertion(ctx context.Context, versionID, caseAssertionID int64) (visual.Assertion, bool, error) {
	if versionID <= 0 || caseAssertionID <= 0 {
		return visual.Assertion{}, false, platform.Invalid("case_assertion_id", "must be a positive ID")
	}
	return s.repo.GetByCaseAssertion(ctx, versionID, caseAssertionID)
}

func (s *ConfigService) defaults(ctx context.Context) visual.ConfigDefaults {
	defaults := visual.ConfigDefaults{
		Threshold:         visual.DefaultThreshold,
		ViewportWidth:     visual.DefaultViewportWidth,
		ViewportHeight:    visual.DefaultViewportHeight,
		DeviceScaleFactor: visual.DefaultDeviceScaleFactor,
	}
	if s.settings == nil {
		return defaults
	}
	if value, err := s.settings.Float(ctx, SettingDefaultThreshold); err == nil && value >= visual.MinThreshold && value <= visual.MaxThreshold {
		defaults.Threshold = value
	}
	if value, err := s.settings.Int(ctx, SettingDefaultViewportWidth); err == nil && value >= visual.MinViewportPx && value <= visual.MaxViewportPx {
		defaults.ViewportWidth = int(value)
	}
	if value, err := s.settings.Int(ctx, SettingDefaultViewportHeight); err == nil && value >= visual.MinViewportPx && value <= visual.MaxViewportPx {
		defaults.ViewportHeight = int(value)
	}
	if value, err := s.settings.Float(ctx, SettingDefaultDeviceScaleFactor); err == nil && value >= visual.MinDeviceScaleFactor && value <= visual.MaxDeviceScaleFactor {
		defaults.DeviceScaleFactor = value
	}
	return defaults
}

func (s *ConfigService) now() time.Time {
	if s.clock != nil {
		return s.clock.Now()
	}
	return time.Now()
}

func (s *ConfigService) recordAudit(ctx context.Context, actorID int64, action string, targetID int64, metadata map[string]any) {
	if s.audit == nil {
		return
	}
	var actor *int64
	if actorID > 0 {
		actor = &actorID
	}
	_ = s.audit.Record(ctx, AuditEntry{
		ActorID: actor, Action: action, TargetKind: "visual_assertion",
		TargetID: platformid.Format(targetID), Metadata: metadata,
	})
}

func configInvalid(field, message string) error {
	return &platform.Error{
		Status: 422, Code: "config_invalid", Message: message,
		Details: map[string]any{"field": field},
	}
}

func configInvalidFrom(err error) error {
	if errors.Is(err, visual.ErrInvalidConfig) {
		return configInvalid("", err.Error())
	}
	return err
}

func baselineInvalid(field, message string) error {
	return &platform.Error{
		Status: 422, Code: "baseline_evidence_invalid", Message: message,
		Details: map[string]any{"field": field},
	}
}

func visualConfigChanged(field, message string) error {
	return &platform.Error{
		Status: 422, Code: "visual_config_changed", Message: message,
		Details: map[string]any{"field": field},
	}
}

func diffInvalid(field, message string) error {
	return &platform.Error{
		Status: 422, Code: "diff_invalid", Message: message,
		Details: map[string]any{"field": field},
	}
}

// mapVisualError maps repository sentinels onto the shared API envelope and
// passes platform errors through untouched.
func mapVisualError(err error, kind string) error {
	if err == nil {
		return nil
	}
	var apiErr *platform.Error
	if errors.As(err, &apiErr) {
		return apiErr
	}
	switch {
	case errors.Is(err, visual.ErrNotFound):
		return platform.NotFound(kind)
	case errors.Is(err, visual.ErrDuplicate):
		return platform.Conflict(kind + " already exists")
	case errors.Is(err, visual.ErrStateConflict):
		return platform.StateConflict("the visual entity is not in a valid state for this operation")
	case errors.Is(err, visual.ErrConfigChanged):
		return visualConfigChanged("", err.Error())
	case errors.Is(err, visual.ErrInvalidConfig):
		return configInvalid("", err.Error())
	case errors.Is(err, visual.ErrInvalidBaseline):
		return baselineInvalid("", err.Error())
	case errors.Is(err, visual.ErrInvalidDiff):
		return diffInvalid("", err.Error())
	default:
		return fmt.Errorf("visual %s failure: %w", kind, err)
	}
}
