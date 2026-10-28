package codeintel

import (
	"context"
	"errors"

	codeinteldomain "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	mindmapapp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
)

// AnchorService is the narrow slice of the P4 anchor service the adapter needs.
// *codeintel/application.AnchorService satisfies it.
type AnchorService interface {
	Create(ctx context.Context, versionID, actorID int64, params codeinteldomain.CreateAnchorParams) (codeinteldomain.CodeAnchor, error)
	List(ctx context.Context, versionID int64, filter codeinteldomain.AnchorFilter) ([]codeinteldomain.CodeAnchor, error)
	Delete(ctx context.Context, versionID, anchorID int64) error
}

// AnchorAdapter implements application.CodeAnchorGateway on top of the P4
// anchor service. Every write is stamped with subject_kind='case' (D37); the
// node id is the case subject id and its ownership is validated by the mindmap
// context, not by codeintel.
type AnchorAdapter struct {
	anchors AnchorService
}

// NewAnchorAdapter wires the adapter to the P4 anchor service.
func NewAnchorAdapter(anchors AnchorService) *AnchorAdapter {
	return &AnchorAdapter{anchors: anchors}
}

var _ mindmapapp.CodeAnchorGateway = (*AnchorAdapter)(nil)

// Create validates and persists one case anchor through the P4 anchor service.
// The confidence field is not forwarded: the P4 service currently fixes the
// stored confidence to its domain default, so the adapter keeps the request
// shape stable and reports whatever the P4 service persisted.
func (a *AnchorAdapter) Create(ctx context.Context, versionID, actorID int64, params mindmapapp.AnchorParams) (mindmapapp.Anchor, error) {
	if a == nil || a.anchors == nil {
		return mindmapapp.Anchor{}, errors.New("mindmap: anchor service is not configured")
	}
	anchor, err := a.anchors.Create(ctx, versionID, actorID, codeinteldomain.CreateAnchorParams{
		SubjectKind: codeinteldomain.SubjectCase,
		SubjectID:   params.SubjectID,
		FilePath:    params.FilePath,
		StartLine:   cloneInt(params.StartLine),
		EndLine:     cloneInt(params.EndLine),
	})
	if err != nil {
		return mindmapapp.Anchor{}, mapCodeIntelError(err)
	}
	return anchorSnapshot(anchor), nil
}

// List returns the case anchors of one case node inside the version.
func (a *AnchorAdapter) List(ctx context.Context, versionID int64, nodeID int64) ([]mindmapapp.Anchor, error) {
	if a == nil || a.anchors == nil {
		return nil, errors.New("mindmap: anchor service is not configured")
	}
	subjectID := nodeID
	anchors, err := a.anchors.List(ctx, versionID, codeinteldomain.AnchorFilter{
		SubjectKind: codeinteldomain.SubjectCase,
		SubjectID:   &subjectID,
	})
	if err != nil {
		return nil, mapCodeIntelError(err)
	}
	mapped := make([]mindmapapp.Anchor, 0, len(anchors))
	for _, anchor := range anchors {
		mapped = append(mapped, anchorSnapshot(anchor))
	}
	return mapped, nil
}

// Delete removes one anchor of the version.
func (a *AnchorAdapter) Delete(ctx context.Context, versionID, anchorID int64) error {
	if a == nil || a.anchors == nil {
		return errors.New("mindmap: anchor service is not configured")
	}
	return mapCodeIntelError(a.anchors.Delete(ctx, versionID, anchorID))
}

// anchorSnapshot projects one P4 anchor onto the mindmap read model, cloning the
// optional values so the projection never aliases P4 state.
func anchorSnapshot(anchor codeinteldomain.CodeAnchor) mindmapapp.Anchor {
	return mindmapapp.Anchor{
		ID:         anchor.ID,
		VersionID:  anchor.VersionID,
		RepoID:     anchor.RepoID,
		SubjectID:  anchor.SubjectID,
		FilePath:   anchor.FilePath,
		StartLine:  cloneInt(anchor.StartLine),
		EndLine:    cloneInt(anchor.EndLine),
		CommitSHA:  cloneStringPtr(anchor.CommitSHA),
		Confidence: anchor.Confidence,
		CreatedBy:  anchor.CreatedBy,
	}
}

// cloneInt copies an optional line number so the caller cannot mutate the
// service's stored range.
func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

// cloneStringPtr copies an optional string.
func cloneStringPtr(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
