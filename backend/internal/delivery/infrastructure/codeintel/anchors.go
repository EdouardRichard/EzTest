// Package codeintel adapts the codeintel application services to the delivery
// context's consumer ports: the issue code anchors (create after AI
// materialization, list on the issue DTO and in the batch report) and the
// commit-pinned code window read of the delivery agent tools (spec §5.4/§7.3/
// §9.3). The delivery context never reads the code anchors or the checkout
// itself.
package codeintel

import (
	"context"
	"errors"
	"fmt"

	codeinteldomain "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// AnchorService is the codeintel anchor use-case slice the adapter needs.
// *codeintel/application.AnchorService satisfies it.
type AnchorService interface {
	Create(ctx context.Context, versionID, actorID int64, p codeinteldomain.CreateAnchorParams) (codeinteldomain.CodeAnchor, error)
	List(ctx context.Context, versionID int64, f codeinteldomain.AnchorFilter) ([]codeinteldomain.CodeAnchor, error)
}

// AnchorRef is one issue code anchor projected for the delivery HTTP DTO and
// the report snapshot. It holds no secret.
type AnchorRef struct {
	AnchorID int64
	Path     string
	Line     int
	Kind     string
	Label    string
}

// Anchors implements delivery/application.AnalysisCodeAnchorPort and the two
// issue-anchor read projections.
type Anchors struct {
	anchors AnchorService
}

// NewAnchors wires the issue-anchor adapter.
func NewAnchors(anchors AnchorService) *Anchors {
	return &Anchors{anchors: anchors}
}

var _ deliveryapp.AnalysisCodeAnchorPort = (*Anchors)(nil)

// CreateIssueAnchors implements delivery/application.AnalysisCodeAnchorPort. It
// is best-effort by contract (spec §5.4): a rejected ref becomes a warning and
// never fails the issue materialization. The returned warnings carry the path
// and the stable reason only.
func (a *Anchors) CreateIssueAnchors(ctx context.Context, versionID, issueID, actor int64, refs []domain.CodeRef) ([]string, error) {
	if a == nil || a.anchors == nil {
		return nil, errors.New("delivery: code anchors are not configured")
	}
	warnings := make([]string, 0)
	for _, ref := range refs {
		path := ref.Path
		line := ref.Line
		if path == "" || line <= 0 {
			warnings = append(warnings, fmt.Sprintf(
				"%s:%d: code anchor skipped (path and positive line are required)", path, line))
			continue
		}
		_, err := a.anchors.Create(ctx, versionID, actor, codeinteldomain.CreateAnchorParams{
			SubjectKind: codeinteldomain.SubjectIssue,
			SubjectID:   issueID,
			FilePath:    path,
			StartLine:   &line,
			EndLine:     &line,
		})
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s:%d: code anchor skipped (%s)",
				path, line, anchorReason(err)))
		}
	}
	return warnings, nil
}

// ListIssueCodeRefs returns the code anchors of one issue (issue DTO codeRefs).
func (a *Anchors) ListIssueCodeRefs(ctx context.Context, versionID, issueID int64) ([]AnchorRef, error) {
	if a == nil || a.anchors == nil {
		return nil, errors.New("delivery: code anchors are not configured")
	}
	subject := issueID
	anchors, err := a.anchors.List(ctx, versionID, codeinteldomain.AnchorFilter{
		SubjectKind: codeinteldomain.SubjectIssue,
		SubjectID:   &subject,
	})
	if err != nil {
		return nil, err
	}
	return anchorRefs(anchors), nil
}

// IssueCodeRefs lists every issue anchor of the version grouped by issue id. It
// serves the batch report's codeRefs projection; a version without anchors
// returns an empty map.
func (a *Anchors) IssueCodeRefs(ctx context.Context, versionID int64) (map[int64][]domain.ReportCodeRef, error) {
	if a == nil || a.anchors == nil {
		return nil, errors.New("delivery: code anchors are not configured")
	}
	anchors, err := a.anchors.List(ctx, versionID, codeinteldomain.AnchorFilter{
		SubjectKind: codeinteldomain.SubjectIssue,
	})
	if err != nil {
		return nil, err
	}
	byIssue := make(map[int64][]domain.ReportCodeRef)
	for _, anchor := range anchors {
		if anchor.SubjectID <= 0 || anchor.FilePath == "" {
			continue
		}
		line := 0
		if anchor.StartLine != nil {
			line = *anchor.StartLine
		}
		byIssue[anchor.SubjectID] = append(byIssue[anchor.SubjectID], domain.ReportCodeRef{
			Path: anchor.FilePath,
			Line: line,
		})
	}
	return byIssue, nil
}

// anchorRefs projects codeintel anchors onto the delivery DTO shape.
func anchorRefs(anchors []codeinteldomain.CodeAnchor) []AnchorRef {
	refs := make([]AnchorRef, 0, len(anchors))
	for _, anchor := range anchors {
		line := 0
		if anchor.StartLine != nil {
			line = *anchor.StartLine
		}
		refs = append(refs, AnchorRef{
			AnchorID: anchor.ID,
			Path:     anchor.FilePath,
			Line:     line,
			Kind:     "file_line",
			Label:    "",
		})
	}
	return refs
}

// anchorReason returns the stable reason of a codeintel error, or the generic
// reason when it is not a domain error. It never returns a message with content.
func anchorReason(err error) string {
	if reason := codeinteldomain.Reason(err); reason != "" {
		return reason
	}
	return "anchor_failed"
}
