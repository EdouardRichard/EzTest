// Package refs implements the delivery context's IssueRefValidator: it checks
// that every durable issue reference exists and belongs to the addressed version
// (spec §5.4). It reads each referenced context through that context's
// application ports only; the delivery service never reads their tables. A
// violation is the stable issue_ref_invalid domain error.
package refs

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	codeinteldomain "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	executiondomain "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	mindmapdomain "github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// CaseRuns is the execution case-run locator slice (execution application).
type CaseRuns interface {
	GetCaseRunByID(ctx context.Context, caseRunID int64) (executiondomain.CaseRun, error)
}

// Batches is the execution batch read slice (execution application).
type Batches interface {
	GetBatch(ctx context.Context, versionID, batchID int64) (executiondomain.RunBatch, error)
}

// Evidence is the execution evidence read slice (execution application).
type Evidence interface {
	Get(ctx context.Context, evidenceID int64) (executiondomain.Evidence, error)
}

// Steps resolves the case run owning one step_result id. It is implemented by
// the execution delivery read adapter.
type Steps interface {
	StepResultOwner(ctx context.Context, stepResultID int64) (caseRunID int64, found bool, err error)
}

// CaseNodes is the mindmap node read slice (mindmap application).
type CaseNodes interface {
	Get(ctx context.Context, versionID, nodeID int64) (mindmapdomain.CaseNode, error)
}

// Requirements is the version requirement read slice (version application).
type Requirements interface {
	Get(ctx context.Context, versionID, requirementID int64) (*versiondomain.Requirement, error)
}

// Anchors is the codeintel anchor read slice (codeintel domain repository).
type Anchors interface {
	Get(ctx context.Context, versionID, anchorID int64) (codeinteldomain.CodeAnchor, error)
}

// Validator implements delivery/application.IssueRefValidator over the owning
// contexts' read ports.
type Validator struct {
	CaseRuns     CaseRuns
	Batches      Batches
	Evidence     Evidence
	Steps        Steps
	CaseNodes    CaseNodes
	Requirements Requirements
	Anchors      Anchors
}

// NewValidator wires the issue-ref validator. A missing port fails closed for
// the ref kinds it owns.
func NewValidator(caseRuns CaseRuns, batches Batches, evidence Evidence, steps Steps,
	caseNodes CaseNodes, requirements Requirements, anchors Anchors) *Validator {
	return &Validator{
		CaseRuns:     caseRuns,
		Batches:      batches,
		Evidence:     evidence,
		Steps:        steps,
		CaseNodes:    caseNodes,
		Requirements: requirements,
		Anchors:      anchors,
	}
}

var _ deliveryapp.IssueRefValidator = (*Validator)(nil)

// ValidateRefs implements delivery/application.IssueRefValidator: every ref must
// exist and belong to versionID. A step_result must also belong to the referenced
// case_run when both are present. Commit refs are shape-only (validated by the
// domain) because a commit has no version before the Git read.
func (v *Validator) ValidateRefs(ctx context.Context, versionID int64, refs []domain.IssueRef) error {
	if len(refs) == 0 {
		return nil
	}
	caseRunRef := int64(0)
	for _, ref := range refs {
		if ref.RefKind == domain.RefKindCaseRun {
			if id, ok := parseRefID(ref.RefID); ok {
				caseRunRef = id
			}
		}
	}
	for _, ref := range refs {
		if err := v.validateRef(ctx, versionID, ref, caseRunRef); err != nil {
			return err
		}
	}
	return nil
}

// validateRef checks one reference against its owning context.
func (v *Validator) validateRef(ctx context.Context, versionID int64, ref domain.IssueRef, caseRunRef int64) error {
	switch ref.RefKind {
	case domain.RefKindCommit:
		return nil
	case domain.RefKindCaseRun:
		return v.validateCaseRun(ctx, versionID, ref)
	case domain.RefKindCaseNode:
		return v.validateCaseNode(ctx, versionID, ref)
	case domain.RefKindStepResult:
		return v.validateStepResult(ctx, versionID, ref, caseRunRef)
	case domain.RefKindEvidence:
		return v.validateEvidence(ctx, versionID, ref)
	case domain.RefKindRequirement:
		return v.validateRequirement(ctx, versionID, ref)
	case domain.RefKindBatch:
		return v.validateBatch(ctx, versionID, ref)
	case domain.RefKindCodeAnchor:
		return v.validateAnchor(ctx, versionID, ref)
	default:
		return refInvalid("%s is not a durable issue ref kind", ref.RefKind)
	}
}

// validateCaseRun checks that the run exists and belongs to the version.
func (v *Validator) validateCaseRun(ctx context.Context, versionID int64, ref domain.IssueRef) error {
	id, ok := parseRefID(ref.RefID)
	if !ok {
		return refInvalid("%s %q is not a positive decimal id", ref.RefKind, ref.RefID)
	}
	if v.CaseRuns == nil {
		return errors.New("delivery: case-run reader is not configured")
	}
	run, err := v.CaseRuns.GetCaseRunByID(ctx, id)
	if err != nil {
		if errors.Is(err, executionapp.ErrCaseRunNotFound) {
			return refInvalid("%s %d does not exist", ref.RefKind, id)
		}
		return fmt.Errorf("delivery: validate case_run ref %d: %w", id, err)
	}
	if run.VersionID != versionID {
		return refInvalid("%s %d belongs to version %d, not %d", ref.RefKind, id, run.VersionID, versionID)
	}
	return nil
}

// validateCaseNode checks that the node exists in the version.
func (v *Validator) validateCaseNode(ctx context.Context, versionID int64, ref domain.IssueRef) error {
	id, ok := parseRefID(ref.RefID)
	if !ok {
		return refInvalid("%s %q is not a positive decimal id", ref.RefKind, ref.RefID)
	}
	if v.CaseNodes == nil {
		return errors.New("delivery: case-node reader is not configured")
	}
	_, err := v.CaseNodes.Get(ctx, versionID, id)
	if err != nil {
		if isMindmapNotFound(err) {
			return refInvalid("%s %d does not belong to version %d", ref.RefKind, id, versionID)
		}
		return fmt.Errorf("delivery: validate case_node ref %d: %w", id, err)
	}
	return nil
}

// validateStepResult checks that the step exists, and that it belongs to the
// referenced case run when the issue also carries a case_run ref.
func (v *Validator) validateStepResult(ctx context.Context, versionID int64, ref domain.IssueRef, caseRunRef int64) error {
	id, ok := parseRefID(ref.RefID)
	if !ok {
		return refInvalid("%s %q is not a positive decimal id", ref.RefKind, ref.RefID)
	}
	if v.Steps == nil {
		return errors.New("delivery: step-result reader is not configured")
	}
	owner, found, err := v.Steps.StepResultOwner(ctx, id)
	if err != nil {
		return fmt.Errorf("delivery: validate step_result ref %d: %w", id, err)
	}
	if !found {
		return refInvalid("%s %d does not exist", ref.RefKind, id)
	}
	if caseRunRef > 0 && owner != caseRunRef {
		return refInvalid("%s %d belongs to case run %d, not %d",
			ref.RefKind, id, owner, caseRunRef)
	}
	// The owning case run decides the version ownership.
	return v.validateCaseRun(ctx, versionID, domain.IssueRef{
		RefKind: domain.RefKindCaseRun,
		RefID:   strconv.FormatInt(owner, 10),
	})
}

// validateEvidence checks that the evidence exists on a batch of the version.
func (v *Validator) validateEvidence(ctx context.Context, versionID int64, ref domain.IssueRef) error {
	id, ok := parseRefID(ref.RefID)
	if !ok {
		return refInvalid("%s %q is not a positive decimal id", ref.RefKind, ref.RefID)
	}
	if v.Evidence == nil || v.Batches == nil {
		return errors.New("delivery: evidence reader is not configured")
	}
	evidence, err := v.Evidence.Get(ctx, id)
	if err != nil {
		if errors.Is(err, executionapp.ErrEvidenceNotFound) {
			return refInvalid("%s %d does not exist", ref.RefKind, id)
		}
		return fmt.Errorf("delivery: validate evidence ref %d: %w", id, err)
	}
	if evidence.BatchID <= 0 {
		return refInvalid("%s %d is not linked to a batch", ref.RefKind, id)
	}
	if _, err := v.Batches.GetBatch(ctx, versionID, evidence.BatchID); err != nil {
		if errors.Is(err, executionapp.ErrBatchNotFound) {
			return refInvalid("%s %d belongs to another version", ref.RefKind, id)
		}
		return fmt.Errorf("delivery: validate evidence ref %d batch %d: %w", id, evidence.BatchID, err)
	}
	return nil
}

// validateRequirement checks that the requirement exists in the version.
func (v *Validator) validateRequirement(ctx context.Context, versionID int64, ref domain.IssueRef) error {
	id, ok := parseRefID(ref.RefID)
	if !ok {
		return refInvalid("%s %q is not a positive decimal id", ref.RefKind, ref.RefID)
	}
	if v.Requirements == nil {
		return errors.New("delivery: requirement reader is not configured")
	}
	if _, err := v.Requirements.Get(ctx, versionID, id); err != nil {
		if isVersionNotFound(err) {
			return refInvalid("%s %d does not belong to version %d", ref.RefKind, id, versionID)
		}
		return fmt.Errorf("delivery: validate requirement ref %d: %w", id, err)
	}
	return nil
}

// validateBatch checks that the batch exists in the version.
func (v *Validator) validateBatch(ctx context.Context, versionID int64, ref domain.IssueRef) error {
	id, ok := parseRefID(ref.RefID)
	if !ok {
		return refInvalid("%s %q is not a positive decimal id", ref.RefKind, ref.RefID)
	}
	if v.Batches == nil {
		return errors.New("delivery: batch reader is not configured")
	}
	if _, err := v.Batches.GetBatch(ctx, versionID, id); err != nil {
		if errors.Is(err, executionapp.ErrBatchNotFound) {
			return refInvalid("%s %d does not belong to version %d", ref.RefKind, id, versionID)
		}
		return fmt.Errorf("delivery: validate batch ref %d: %w", id, err)
	}
	return nil
}

// validateAnchor checks that the anchor exists in the version.
func (v *Validator) validateAnchor(ctx context.Context, versionID int64, ref domain.IssueRef) error {
	id, ok := parseRefID(ref.RefID)
	if !ok {
		return refInvalid("%s %q is not a positive decimal id", ref.RefKind, ref.RefID)
	}
	if v.Anchors == nil {
		return errors.New("delivery: anchor reader is not configured")
	}
	if _, err := v.Anchors.Get(ctx, versionID, id); err != nil {
		if errors.Is(err, codeinteldomain.ErrNotFound) {
			return refInvalid("%s %d does not belong to version %d", ref.RefKind, id, versionID)
		}
		return fmt.Errorf("delivery: validate code_anchor ref %d: %w", id, err)
	}
	return nil
}

// refInvalid builds the stable issue_ref_invalid domain error.
func refInvalid(format string, args ...any) *domain.Error {
	return domain.NewInvalid("refs", domain.ReasonIssueRefInvalid, fmt.Sprintf(format, args...))
}

// parseRefID parses a canonical positive decimal ref id.
func parseRefID(raw string) (int64, bool) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != raw {
		return 0, false
	}
	return id, true
}

// isMindmapNotFound reports a mindmap not-found domain error.
func isMindmapNotFound(err error) bool {
	var domainErr *mindmapdomain.Error
	return errors.As(err, &domainErr) && domainErr != nil && domainErr.Kind == mindmapdomain.KindNotFound
}

// isVersionNotFound reports a version not-found domain error.
func isVersionNotFound(err error) bool {
	var domainErr *versiondomain.Error
	return errors.As(err, &domainErr) && domainErr != nil && domainErr.Kind == versiondomain.KindNotFound
}
