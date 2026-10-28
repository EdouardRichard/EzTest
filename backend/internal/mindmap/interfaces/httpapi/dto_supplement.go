package httpapi

import (
	"context"
	"encoding/json"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// The P7 mindmap DTOs of spec §7.6. Ids are decimal strings (D29) and every
// collection is a non-nil array so the frontend never has to special-case null.

// incrementalPlanDTO is IncrementalPlanView (spec §7.6).
type incrementalPlanDTO struct {
	VersionID       string                 `json:"versionId"`
	Phase           string                 `json:"phase"`
	ParentBatchID   *string                `json:"parentBatchId"`
	Run             *incrementalPlanRunDTO `json:"run"`
	NewCases        []planNewCaseDTO       `json:"newCases"`
	AffectedCases   []planAffectedCaseDTO  `json:"affectedCases"`
	UnmappedChanges []planChangeDTO        `json:"unmappedChanges"`
	DefaultCaseIDs  []string               `json:"defaultCaseIds"`
	Warnings        []planWarningDTO       `json:"warnings"`
	Degraded        bool                   `json:"degraded"`
}

// incrementalPlanRunDTO is the latest incremental_design run summary.
type incrementalPlanRunDTO struct {
	ID     string          `json:"id"`
	Kind   string          `json:"kind"`
	Status string          `json:"status"`
	Result json.RawMessage `json:"result"`
}

// planNewCaseDTO is one materialized supplement case of the plan.
type planNewCaseDTO struct {
	NodeID         string   `json:"nodeId"`
	Title          string   `json:"title"`
	Priority       string   `json:"priority"`
	Confidence     string   `json:"confidence"`
	RequirementIDs []string `json:"requirementIds"`
	Status         string   `json:"status"`
}

// planAffectedCaseDTO is one affected existing case proposal. Selected is true
// for the high/medium confidence default selection.
type planAffectedCaseDTO struct {
	NodeID     string `json:"nodeId"`
	Title      string `json:"title"`
	Confidence string `json:"confidence"`
	Reason     string `json:"reason"`
	Source     string `json:"source"`
	Selected   bool   `json:"selected"`
}

// planChangeDTO is one unmapped P4 change.
type planChangeDTO struct {
	Path    string `json:"path"`
	Summary string `json:"summary"`
}

// planWarningDTO is one stable plan warning ({code,message}).
type planWarningDTO struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// newIncrementalPlanDTO renders the application plan view.
func newIncrementalPlanDTO(view application.IncrementalPlanView) incrementalPlanDTO {
	dto := incrementalPlanDTO{
		VersionID:       idString(view.VersionID),
		Phase:           view.Phase,
		ParentBatchID:   optIDString(view.ParentBatchID),
		NewCases:        make([]planNewCaseDTO, 0, len(view.NewCases)),
		AffectedCases:   make([]planAffectedCaseDTO, 0, len(view.AffectedCases)),
		UnmappedChanges: make([]planChangeDTO, 0, len(view.UnmappedChanges)),
		DefaultCaseIDs:  idStrings(view.DefaultCaseIDs),
		Warnings:        make([]planWarningDTO, 0, len(view.Warnings)),
		Degraded:        view.Degraded,
	}
	if view.Run != nil {
		dto.Run = &incrementalPlanRunDTO{
			ID:     idString(view.Run.ID),
			Kind:   view.Run.Kind,
			Status: view.Run.Status,
			Result: rawJSONObject(view.Run.Result),
		}
	}
	for _, newCase := range view.NewCases {
		dto.NewCases = append(dto.NewCases, planNewCaseDTO{
			NodeID:         idString(newCase.NodeID),
			Title:          newCase.Title,
			Priority:       newCase.Priority,
			Confidence:     newCase.Confidence,
			RequirementIDs: idStrings(newCase.RequirementIDs),
			Status:         newCase.Status,
		})
	}
	for _, affectedCase := range view.AffectedCases {
		dto.AffectedCases = append(dto.AffectedCases, planAffectedCaseDTO{
			NodeID:     idString(affectedCase.NodeID),
			Title:      affectedCase.Title,
			Confidence: affectedCase.Confidence,
			Reason:     affectedCase.Reason,
			Source:     affectedCase.Source,
			Selected:   affectedCase.Selected,
		})
	}
	for _, change := range view.UnmappedChanges {
		dto.UnmappedChanges = append(dto.UnmappedChanges, planChangeDTO{Path: change.Path, Summary: change.Summary})
	}
	for _, warning := range view.Warnings {
		dto.Warnings = append(dto.Warnings, planWarningDTO{Code: warning.Code, Message: warning.Message})
	}
	return dto
}

// rawJSONObject renders one stored JSON document as an object, defaulting to {}
// for an empty or malformed value.
func rawJSONObject(raw []byte) json.RawMessage {
	if len(raw) == 0 || !json.Valid(raw) {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(raw)
}

// supplementDecisionDTO is the PATCH /nodes/{nodeId}/supplement response: the
// decided node id and its new status. Node content is never returned here.
type supplementDecisionDTO struct {
	NodeID string `json:"nodeId"`
	Status string `json:"status"`
}

// supplementStartRunRequest is the POST /runs body for the two P7 kinds (spec
// §7.6). batchId is the parent batch (a decimal string, D29); mode is optional
// and defaults to post_batch / incremental_design in the application service.
// nodeIds/confirmReplace are accepted for shape parity with the frozen P5 body
// but are not meaningful for the supplement kinds.
type supplementStartRunRequest struct {
	Kind           string   `json:"kind"`
	Mode           string   `json:"mode"`
	BatchID        string   `json:"batchId"`
	NodeIDs        []string `json:"nodeIds"`
	ConfirmReplace bool     `json:"confirmReplace"`
}

// incrementalPlanReader is the read slice behind GET /incremental-plan.
// *application.GraphService satisfies it.
type incrementalPlanReader interface {
	IncrementalPlan(ctx context.Context, versionID int64) (application.IncrementalPlanView, error)
}

// supplementNodeDecision is the write slice behind
// PATCH /nodes/{nodeId}/supplement. *application.GraphService satisfies it.
type supplementNodeDecision interface {
	TransitionNodeStatus(ctx context.Context, versionID, nodeID, actor int64, to string) (domain.CaseNode, error)
}
