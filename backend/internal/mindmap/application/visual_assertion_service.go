package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image/png"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// DefaultVisualBaselineMaxBytes is the spec §9.3 baseline size cap (5 MiB) when
// EZTEST_VISUAL_BASELINE_MAX_BYTES is not configured.
const DefaultVisualBaselineMaxBytes int64 = 5 << 20

// UpdateVisualAssertionCommand is the PUT .../assertions/{assertionId}/visual
// body. NodeRevision is the node-level OCC token shared with every graph write.
type UpdateVisualAssertionCommand struct {
	NodeRevision      int
	Selector          string
	Region            *domain.VisualRegion
	Threshold         float64
	ViewportWidth     int
	ViewportHeight    int
	DeviceScaleFactor float64
}

// VisualAssertionDeps are the collaborators of the visual assertion use cases.
type VisualAssertionDeps struct {
	Store     VisualAssertionStore
	Evidence  VisualBaselineEvidenceReader
	Versions  VersionLifecycle
	Baselines BaselineRepository
	Events    EventPublisher
	Clock     Clock
	// MaxBaselineBytes caps the approved baseline artifact (default 5 MiB).
	MaxBaselineBytes int64
}

// VisualAssertionService implements the §9.3 visual-assertion use cases: editing
// the selector/region/threshold/viewport configuration under the node revision
// CAS, and the administrator-approved baseline.
type VisualAssertionService struct {
	deps             VisualAssertionDeps
	maxBaselineBytes int64
}

// NewVisualAssertionService wires the visual assertion use cases.
func NewVisualAssertionService(deps VisualAssertionDeps) *VisualAssertionService {
	maxBytes := deps.MaxBaselineBytes
	if maxBytes <= 0 {
		maxBytes = DefaultVisualBaselineMaxBytes
	}
	return &VisualAssertionService{deps: deps, maxBaselineBytes: maxBytes}
}

// now returns the injected clock's time, or the wall clock when no clock is
// wired (tests and partially wired processes).
func (s *VisualAssertionService) now() time.Time {
	if s.deps.Clock != nil {
		return s.deps.Clock.Now()
	}
	return time.Now()
}

// Update validates and persists the comparison parameters of a visual assertion.
// The version must still be writable (designing/reviewing with no frozen
// baseline) and the assertion must be kind=visual.
func (s *VisualAssertionService) Update(ctx context.Context, versionID, assertionID, actor int64, cmd UpdateVisualAssertionCommand) (domain.VisualAssertion, error) {
	if s == nil || s.deps.Store == nil {
		return domain.VisualAssertion{}, errors.New("mindmap: visual assertion store is not configured")
	}
	if versionID <= 0 || assertionID <= 0 {
		return domain.VisualAssertion{}, domain.NewInvalid("assertionId", "invalid_assertion_id",
			"version and assertion ids must be positive")
	}
	if err := s.ensureWritable(ctx, versionID); err != nil {
		return domain.VisualAssertion{}, err
	}

	params := domain.VisualAssertionParams{
		Selector:          cmd.Selector,
		Region:            normalizeVisualRegion(cmd.Region),
		Threshold:         cmd.Threshold,
		ViewportWidth:     cmd.ViewportWidth,
		ViewportHeight:    cmd.ViewportHeight,
		DeviceScaleFactor: cmd.DeviceScaleFactor,
	}
	applyVisualDefaults(&params)
	if err := params.Validate(); err != nil {
		return domain.VisualAssertion{}, err
	}

	target, err := s.deps.Store.Get(ctx, versionID, assertionID)
	if err != nil {
		return domain.VisualAssertion{}, err
	}
	if !strings.EqualFold(strings.TrimSpace(target.Kind), domain.AssertionKindVisual) {
		return domain.VisualAssertion{}, domain.NewInvalid("assertionId", "invalid_assertion_kind",
			"only kind=visual assertions accept visual comparison parameters")
	}
	return s.deps.Store.Save(ctx, versionID, assertionID, cmd.NodeRevision, params, actor, s.now())
}

// ApproveBaseline validates the evidence artifact against the assertion's
// viewport/device scale factor and records it as the approved baseline. The
// caller (HTTP handler) enforces the administrator role.
func (s *VisualAssertionService) ApproveBaseline(ctx context.Context, versionID, assertionID, actor, evidenceID int64) (domain.VisualAssertion, error) {
	if s == nil || s.deps.Store == nil || s.deps.Evidence == nil {
		return domain.VisualAssertion{}, errors.New("mindmap: visual baseline dependencies are not configured")
	}
	if versionID <= 0 || assertionID <= 0 {
		return domain.VisualAssertion{}, domain.NewInvalid("assertionId", "invalid_assertion_id",
			"version and assertion ids must be positive")
	}
	if evidenceID <= 0 {
		return domain.VisualAssertion{}, domain.NewInvalid("evidenceId", "invalid_evidence_id",
			"evidenceId must be a positive id")
	}
	if err := s.ensureWritable(ctx, versionID); err != nil {
		return domain.VisualAssertion{}, err
	}

	target, err := s.deps.Store.Get(ctx, versionID, assertionID)
	if err != nil {
		return domain.VisualAssertion{}, err
	}
	if !strings.EqualFold(strings.TrimSpace(target.Kind), domain.AssertionKindVisual) {
		return domain.VisualAssertion{}, domain.NewInvalid("assertionId", "invalid_assertion_kind",
			"only kind=visual assertions can approve a visual baseline")
	}
	visual := visualWithDefaults(target.Visual)
	evidence, err := s.deps.Evidence.ReadForBaseline(ctx, versionID, target.NodeID, evidenceID)
	if err != nil {
		return domain.VisualAssertion{}, err
	}
	if err := s.validateBaselineEvidence(visual, evidence); err != nil {
		return domain.VisualAssertion{}, err
	}

	now := s.now()
	stored, err := s.deps.Store.ApproveBaseline(ctx, versionID, assertionID, evidenceID, actor, now)
	if err != nil {
		return domain.VisualAssertion{}, err
	}
	s.publishBaselineApproved(ctx, versionID, target.NodeID, assertionID, evidenceID, actor)
	return stored, nil
}

// ensureWritable applies the same editability gate as the graph write path: the
// version must be designing/reviewing and must not have a frozen baseline.
func (s *VisualAssertionService) ensureWritable(ctx context.Context, versionID int64) error {
	if s.deps.Versions == nil {
		return errors.New("mindmap: version lifecycle is not configured")
	}
	version, err := s.deps.Versions.GetVersion(ctx, versionID)
	if err != nil {
		return err
	}
	baseline, err := loadBaseline(ctx, s.deps.Baselines, versionID)
	if err != nil {
		return err
	}
	if baseline != nil {
		return baselineFrozenError(versionID)
	}
	switch version.Status {
	case versionStatusDesigning, versionStatusReviewing:
		return nil
	default:
		return domain.NewStateConflict("version_state_conflict",
			"the version is not editable; visual assertions are writable only in designing or reviewing")
	}
}

// validateBaselineEvidence proves the artifact is a PNG whose device-pixel size
// matches the assertion viewport and whose bytes/hash are internally consistent.
func (s *VisualAssertionService) validateBaselineEvidence(visual domain.VisualAssertion, evidence VisualEvidence) error {
	if evidence.SizeBytes > s.maxBaselineBytes || int64(len(evidence.Data)) > s.maxBaselineBytes {
		return domain.NewInvalid("evidenceId", "visual_baseline_too_large",
			"the visual baseline exceeds the configured byte limit")
	}
	if evidence.SizeBytes > 0 && int64(len(evidence.Data)) != evidence.SizeBytes {
		return domain.NewInvalid("evidenceId", "visual_baseline_invalid",
			"the visual baseline bytes do not match the recorded size")
	}
	if evidence.SHA256 != "" {
		sum := sha256.Sum256(evidence.Data)
		if !strings.EqualFold(hex.EncodeToString(sum[:]), evidence.SHA256) {
			return domain.NewInvalid("evidenceId", "visual_baseline_invalid",
				"the visual baseline bytes do not match the recorded sha256")
		}
	}
	mime := strings.ToLower(strings.TrimSpace(evidence.Mime))
	if mime != "" && mime != "image/png" {
		return domain.NewInvalid("evidenceId", "visual_baseline_invalid",
			"the visual baseline must be a PNG image")
	}
	config, err := png.DecodeConfig(bytes.NewReader(evidence.Data))
	if err != nil {
		return domain.NewInvalid("evidenceId", "visual_baseline_invalid",
			"the visual baseline is not a valid PNG image")
	}
	wantWidth, wantHeight := visual.ExpectedImageSize()
	if config.Width != wantWidth || config.Height != wantHeight {
		return domain.NewInvalid("evidenceId", "visual_baseline_invalid",
			"the visual baseline image size does not match the configured viewport and device scale factor")
	}
	return nil
}

// publishBaselineApproved emits the §9.3 event. The detail carries ids and the
// approval status only: the baseline bytes never leave the evidence store.
func (s *VisualAssertionService) publishBaselineApproved(ctx context.Context, versionID, caseNodeID, assertionID, evidenceID, actor int64) {
	if s.deps.Events == nil {
		return
	}
	event := Event{
		VersionID:  versionID,
		Type:       "case.visual_baseline_approved",
		Summary:    "visual baseline approved",
		RefCaseIDs: []int64{caseNodeID},
		ActorID:    actor,
		Source:     "user",
		Detail: map[string]any{
			"caseNodeId":         caseNodeID,
			"assertionId":        assertionID,
			"baselineEvidenceId": evidenceID,
			"approvedBy":         actor,
			"status":             "approved",
		},
	}
	_ = s.deps.Events.Publish(ctx, event)
}

// applyVisualDefaults fills the zero viewport/device-scale-factor fields with
// the spec defaults so a PUT may omit them.
func applyVisualDefaults(params *domain.VisualAssertionParams) {
	if params.ViewportWidth == 0 && params.ViewportHeight == 0 {
		params.ViewportWidth = domain.VisualViewportDefaultWidth
		params.ViewportHeight = domain.VisualViewportDefaultHeight
	}
	if params.DeviceScaleFactor == 0 {
		params.DeviceScaleFactor = domain.VisualDeviceScaleFactorDefault
	}
}

// visualWithDefaults projects an optional stored configuration onto one with
// the spec defaults applied.
func visualWithDefaults(visual *domain.VisualAssertion) domain.VisualAssertion {
	if visual == nil {
		return domain.VisualAssertion{
			Threshold:         domain.VisualThresholdDefault,
			ViewportWidth:     domain.VisualViewportDefaultWidth,
			ViewportHeight:    domain.VisualViewportDefaultHeight,
			DeviceScaleFactor: domain.VisualDeviceScaleFactorDefault,
		}
	}
	copied := *visual
	if copied.ViewportWidth <= 0 {
		copied.ViewportWidth = domain.VisualViewportDefaultWidth
	}
	if copied.ViewportHeight <= 0 {
		copied.ViewportHeight = domain.VisualViewportDefaultHeight
	}
	if copied.DeviceScaleFactor <= 0 {
		copied.DeviceScaleFactor = domain.VisualDeviceScaleFactorDefault
	}
	return copied
}

// normalizeVisualRegion treats the all-zero default (JSON `{}`) as no crop: a
// zero-width region would otherwise fail validation on every omitted region.
func normalizeVisualRegion(region *domain.VisualRegion) *domain.VisualRegion {
	if region == nil {
		return nil
	}
	if region.X == 0 && region.Y == 0 && region.Width == 0 && region.Height == 0 {
		return nil
	}
	copied := *region
	return &copied
}
