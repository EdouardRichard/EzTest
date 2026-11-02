package workspaceapi

import (
	"strconv"
	"time"

	workspaceapp "github.com/EdouardRichard/EzTest/backend/internal/workspace/application"
)

// todoItemDTO is the frozen P6 TodoItem schema (openapi workspace tag). Optional
// expires_at is always present and null for sources without a deadline.
type todoItemDTO struct {
	Kind         string  `json:"kind"`
	Title        string  `json:"title"`
	VersionID    string  `json:"version_id"`
	VersionTitle string  `json:"version_title"`
	Severity     string  `json:"severity"`
	CreatedAt    string  `json:"created_at"`
	ExpiresAt    *string `json:"expires_at"`
	URL          string  `json:"url"`
}

// todoCountsDTO carries all six pre-limit kind counts of spec §4.3.2.
type todoCountsDTO struct {
	RequirementConfirmation int `json:"requirement_confirmation"`
	CaseConfirmation        int `json:"case_confirmation"`
	IssueConfirmation       int `json:"issue_confirmation"`
	DesignPending           int `json:"design_pending"`
	HumanRequest            int `json:"human_request"`
	DeadWorkItem            int `json:"dead_work_item"`
}

// todoSnapshotDTO is the GET /api/todos data object.
type todoSnapshotDTO struct {
	Items  []todoItemDTO `json:"items"`
	Counts todoCountsDTO `json:"counts"`
}

func toTodoSnapshotDTO(snapshot workspaceapp.Snapshot) todoSnapshotDTO {
	items := make([]todoItemDTO, 0, len(snapshot.Items))
	for _, item := range snapshot.Items {
		items = append(items, todoItemDTO{
			Kind:         item.Kind,
			Title:        item.Title,
			VersionID:    formatID(item.VersionID),
			VersionTitle: item.VersionTitle,
			Severity:     item.Severity,
			CreatedAt:    formatTime(item.CreatedAt),
			ExpiresAt:    formatOptionalTime(item.ExpiresAt),
			URL:          item.URL,
		})
	}
	return todoSnapshotDTO{
		Items: items,
		Counts: todoCountsDTO{
			RequirementConfirmation: snapshot.Counts.RequirementConfirmation,
			CaseConfirmation:        snapshot.Counts.CaseConfirmation,
			IssueConfirmation:       snapshot.Counts.IssueConfirmation,
			DesignPending:           snapshot.Counts.DesignPending,
			HumanRequest:            snapshot.Counts.HumanRequest,
			DeadWorkItem:            snapshot.Counts.DeadWorkItem,
		},
	}
}

func formatID(id int64) string {
	return strconv.FormatInt(id, 10)
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
}

func formatOptionalTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := formatTime(*value)
	return &formatted
}
