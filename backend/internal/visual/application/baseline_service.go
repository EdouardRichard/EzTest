package visualapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image/png"
	"math"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	visual "github.com/EdouardRichard/EzTest/backend/internal/visual/domain"
)

// BaselineServiceDeps wires the visual baseline lifecycle service.
type BaselineServiceDeps struct {
	Baselines  BaselineRepository
	Assertions AssertionRepository
	Evidence   EvidenceReader
	Settings   SettingsReader
	Clock      Clock
	IDs        IDGenerator
	Audit      AuditRecorder
	Events     EventPublisher
}

// BaselineService approves and lists visual baselines. Approval is admin-only
// at the API layer; the service enforces evidence ownership, size/PNG/config
// revision and supersedes the previous approved baseline.
type BaselineService struct {
	baselines  BaselineRepository
	assertions AssertionRepository
	evidence   EvidenceReader
	settings   SettingsReader
	clock      Clock
	ids        IDGenerator
	audit      AuditRecorder
	events     EventPublisher
}

// NewBaselineService wires the baseline service.
func NewBaselineService(deps BaselineServiceDeps) *BaselineService {
	return &BaselineService{
		baselines:  deps.Baselines,
		assertions: deps.Assertions,
		evidence:   deps.Evidence,
		settings:   deps.Settings,
		clock:      deps.Clock,
		ids:        deps.IDs,
		audit:      deps.Audit,
		events:     deps.Events,
	}
}

// Approve turns one visual_actual evidence into the approved baseline of an
// assertion (spec §7.2). The previous approved baseline becomes superseded in
// the same transaction; history is retained.
func (s *BaselineService) Approve(ctx context.Context, versionID, assertionID, evidenceID, actorID int64) (visual.Baseline, error) {
	if versionID <= 0 {
		return visual.Baseline{}, platform.Invalid("version_id", "must be a positive ID")
	}
	if assertionID <= 0 {
		return visual.Baseline{}, platform.Invalid("id", "must be a positive ID")
	}
	if evidenceID <= 0 {
		return visual.Baseline{}, platform.Invalid("evidence_id", "must be a positive ID")
	}
	if actorID <= 0 {
		return visual.Baseline{}, platform.Forbidden()
	}
	assertion, err := s.assertions.Get(ctx, assertionID)
	if err != nil {
		return visual.Baseline{}, mapVisualError(err, "visual_assertion")
	}
	if assertion.VersionID != versionID {
		return visual.Baseline{}, platform.NotFound("visual_assertion")
	}
	if s.evidence == nil {
		return visual.Baseline{}, errors.New("baseline service has no evidence reader")
	}
	evidence, err := s.evidence.VisualEvidence(ctx, versionID, evidenceID)
	if err != nil {
		return visual.Baseline{}, mapVisualError(err, "evidence")
	}

	if evidence.Kind != "visual_actual" {
		return visual.Baseline{}, baselineInvalid("evidence_id", "only a visual_actual evidence can become a baseline")
	}
	if evidence.CaseVersionID != assertion.CaseVersionID {
		return visual.Baseline{}, baselineInvalid("evidence_id", "evidence belongs to another case version")
	}
	if evidence.VersionSUTID == nil || *evidence.VersionSUTID != assertion.VersionSUTID {
		return visual.Baseline{}, baselineInvalid("evidence_id", "evidence belongs to another version_sut")
	}
	maxBytes := s.baselineMaxBytes(ctx)
	if evidence.SizeBytes > maxBytes || int64(len(evidence.Data)) > maxBytes {
		return visual.Baseline{}, baselineInvalid("evidence_id", "evidence exceeds visual.baseline_max_bytes")
	}
	width, height, err := baselineImageDimensions(evidence)
	if err != nil {
		return visual.Baseline{}, err
	}
	if evidence.SHA256 != "" || evidence.SizeBytes > 0 || len(evidence.Data) > 0 {
		sum := sha256.Sum256(evidence.Data)
		sha := hex.EncodeToString(sum[:])
		if evidence.SHA256 != "" && evidence.SHA256 != sha {
			return visual.Baseline{}, baselineInvalid("evidence_id", "evidence sha256 does not match its bytes")
		}
		if evidence.SizeBytes > 0 && evidence.SizeBytes != int64(len(evidence.Data)) {
			return visual.Baseline{}, baselineInvalid("evidence_id", "evidence size does not match its bytes")
		}
		evidence.SHA256 = sha
		evidence.SizeBytes = int64(len(evidence.Data))
	}
	if assertion.Selector == "" {
		wantWidth := int(math.Round(float64(assertion.ViewportWidth) * assertion.DeviceScaleFactor))
		wantHeight := int(math.Round(float64(assertion.ViewportHeight) * assertion.DeviceScaleFactor))
		if width != wantWidth || height != wantHeight {
			return visual.Baseline{}, baselineInvalid("evidence_id", "full-page actual dimensions must equal viewport × device_scale_factor")
		}
	}
	if evidence.ConfigRevision > 0 {
		if evidence.ConfigRevision != assertion.ConfigRevision {
			return visual.Baseline{}, visualConfigChanged("evidence_id", "the actual capture was produced under another config revision")
		}
	} else if !evidence.CreatedAt.IsZero() && evidence.CreatedAt.Before(assertion.UpdatedAt) {
		// Adapters unable to recover the capture revision fall back to the
		// timestamp of the last effective configuration change.
		return visual.Baseline{}, visualConfigChanged("evidence_id", "the visual configuration changed after this capture")
	}
	fingerprint, err := visual.AssertionFingerprint(assertion, "", "")
	if err != nil {
		return visual.Baseline{}, configInvalidFrom(err)
	}
	now := s.now()
	if s.ids == nil {
		return visual.Baseline{}, errors.New("baseline service has no ID generator")
	}
	baseline := visual.Baseline{
		ID:                     s.ids.Next(),
		VisualAssertionID:      assertion.ID,
		CaseVersionID:          assertion.CaseVersionID,
		VersionSUTID:           assertion.VersionSUTID,
		EvidenceID:             evidence.ID,
		SHA256:                 evidence.SHA256,
		SizeBytes:              evidence.SizeBytes,
		Width:                  width,
		Height:                 height,
		ViewportWidth:          assertion.ViewportWidth,
		ViewportHeight:         assertion.ViewportHeight,
		DeviceScaleFactor:      assertion.DeviceScaleFactor,
		ConfigRevision:         assertion.ConfigRevision,
		EnvironmentFingerprint: fingerprint,
		Status:                 visual.BaselineApproved,
		ApprovedBy:             actorID,
		ApprovedAt:             now,
		CreatedAt:              now,
		UpdatedAt:              now,
	}
	created, err := s.baselines.InsertApproved(ctx, baseline, now)
	if err != nil {
		return visual.Baseline{}, mapVisualError(err, "visual_baseline")
	}
	s.recordAudit(ctx, actorID, created)
	s.publishApproved(ctx, versionID, created, actorID)
	return created, nil
}

// List returns the baseline history of one assertion.
func (s *BaselineService) List(ctx context.Context, versionID, assertionID int64) ([]visual.Baseline, error) {
	assertion, err := s.requireAssertion(ctx, versionID, assertionID)
	if err != nil {
		return nil, err
	}
	list, err := s.baselines.List(ctx, assertion.ID)
	if err != nil {
		return nil, mapVisualError(err, "visual_baseline")
	}
	return list, nil
}

// Active returns the currently approved baseline of one assertion.
func (s *BaselineService) Active(ctx context.Context, assertionID int64) (visual.Baseline, bool, error) {
	if assertionID <= 0 {
		return visual.Baseline{}, false, platform.Invalid("id", "must be a positive ID")
	}
	baseline, ok, err := s.baselines.Active(ctx, assertionID)
	if err != nil {
		return visual.Baseline{}, false, mapVisualError(err, "visual_baseline")
	}
	return baseline, ok, nil
}

func (s *BaselineService) requireAssertion(ctx context.Context, versionID, assertionID int64) (visual.Assertion, error) {
	if versionID <= 0 {
		return visual.Assertion{}, platform.Invalid("version_id", "must be a positive ID")
	}
	if assertionID <= 0 {
		return visual.Assertion{}, platform.Invalid("id", "must be a positive ID")
	}
	assertion, err := s.assertions.Get(ctx, assertionID)
	if err != nil {
		return visual.Assertion{}, mapVisualError(err, "visual_assertion")
	}
	if assertion.VersionID != versionID {
		return visual.Assertion{}, platform.NotFound("visual_assertion")
	}
	return assertion, nil
}

func baselineImageDimensions(evidence VisualEvidence) (int, int, error) {
	width, height := evidence.Width, evidence.Height
	if width <= 0 || height <= 0 {
		if len(evidence.Data) == 0 {
			return 0, 0, baselineInvalid("evidence_id", "evidence has no PNG bytes")
		}
		config, err := png.DecodeConfig(bytes.NewReader(evidence.Data))
		if err != nil {
			return 0, 0, baselineInvalid("evidence_id", "evidence is not a decodable PNG")
		}
		width, height = config.Width, config.Height
	}
	if width <= 0 || height <= 0 {
		return 0, 0, baselineInvalid("evidence_id", "PNG dimensions must be positive")
	}
	return width, height, nil
}

func (s *BaselineService) baselineMaxBytes(ctx context.Context) int64 {
	const fallback = 5 << 20
	if s.settings == nil {
		return fallback
	}
	value, err := s.settings.Int(ctx, SettingBaselineMaxBytes)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func (s *BaselineService) recordAudit(ctx context.Context, actorID int64, baseline visual.Baseline) {
	if s.audit == nil {
		return
	}
	actor := actorID
	_ = s.audit.Record(ctx, AuditEntry{
		ActorID: &actor, Action: "visual.baseline.approve", TargetKind: "visual_baseline",
		TargetID: platformid.Format(baseline.ID),
		Metadata: map[string]any{
			"visual_assertion_id": platformid.Format(baseline.VisualAssertionID),
			"evidence_id":         platformid.Format(baseline.EvidenceID),
			"config_revision":     baseline.ConfigRevision,
		},
	})
}

func (s *BaselineService) publishApproved(ctx context.Context, versionID int64, baseline visual.Baseline, actorID int64) {
	if s.events == nil {
		return
	}
	actor := actorID
	_ = s.events.Publish(ctx, Event{
		Type:      "visual.baseline_approved",
		VersionID: versionID,
		Scope: map[string]any{
			"visual_assertion_id": platformid.Format(baseline.VisualAssertionID),
			"baseline_id":         platformid.Format(baseline.ID),
		},
		Payload: map[string]any{
			"baseline_id":         platformid.Format(baseline.ID),
			"visual_assertion_id": platformid.Format(baseline.VisualAssertionID),
			"evidence_id":         platformid.Format(baseline.EvidenceID),
			"approved_by":         platformid.Format(actorID),
		},
		ActorID: &actor,
	})
}

func (s *BaselineService) now() time.Time {
	if s.clock != nil {
		return s.clock.Now()
	}
	return time.Now()
}
