// Package report adapts the delivery context's own reads to the report and
// analysis consumer ports (spec §6.5/§5.6): the issues that belong in a batch
// report and the open-issue summaries injected into a triage run. It reads the
// delivery issue store (ownership by the delivery context) and an optional
// code-anchor projection supplied by the codeintel adapter.
package report

import (
	"context"
	"errors"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// issueReadLimit bounds each issue read of a report. The report service takes no
// cursor, so the three reads (batch, open, confirmed) are single pages.
const issueReadLimit = 100

// IssueStore is the delivery issue persistence slice the readers need.
// *delivery/infrastructure/postgres.IssueRepository satisfies it.
type IssueStore interface {
	List(ctx context.Context, versionID int64, filter deliveryapp.IssueFilter) (issues []domain.Issue, hasMore bool, err error)
	ListRefs(ctx context.Context, issueID int64) ([]domain.IssueRef, error)
}

// CodeRefSource lists the code locations of the issues of a version, grouped by
// issue id. It is optional: a nil source leaves the codeRefs arrays empty.
type CodeRefSource interface {
	IssueCodeRefs(ctx context.Context, versionID int64) (map[int64][]domain.ReportCodeRef, error)
}

// IssueSources implements delivery/application.ReportIssueReader and
// delivery/application.AnalysisIssueReader.
type IssueSources struct {
	issues  IssueStore
	codeRef CodeRefSource
}

// NewIssueSources wires the delivery report/analysis issue readers.
func NewIssueSources(issues IssueStore, codeRef CodeRefSource) *IssueSources {
	return &IssueSources{issues: issues, codeRef: codeRef}
}

var (
	_ deliveryapp.ReportIssueReader   = (*IssueSources)(nil)
	_ deliveryapp.AnalysisIssueReader = (*IssueSources)(nil)
)

// ListReportIssues implements delivery/application.ReportIssueReader: the issues
// linked to the batch merged with every open/confirmed issue of the version,
// deduplicated by id. Code refs are filled best-effort from the optional anchor
// source.
func (s *IssueSources) ListReportIssues(ctx context.Context, versionID, batchID int64) ([]deliveryapp.ReportIssueSource, error) {
	if s == nil || s.issues == nil {
		return nil, errors.New("delivery: issue store is not configured")
	}
	// Three deterministic reads: batch-linked first, then the open and confirmed
	// issues of the version. The dedupe keeps the first occurrence, so a
	// batch-linked issue is reported once even when it is also open.
	batch := batchID
	pages := []deliveryapp.IssueFilter{
		{BatchID: &batch, Limit: issueReadLimit},
		{Status: domain.StatusOpen, Limit: issueReadLimit},
		{Status: domain.StatusConfirmed, Limit: issueReadLimit},
	}
	seen := make(map[int64]struct{})
	issues := make([]domain.Issue, 0, issueReadLimit)
	for _, filter := range pages {
		page, _, err := s.issues.List(ctx, versionID, filter)
		if err != nil {
			return nil, err
		}
		for _, issue := range page {
			if _, exists := seen[issue.ID]; exists {
				continue
			}
			seen[issue.ID] = struct{}{}
			issues = append(issues, issue)
		}
	}

	codeRefs := map[int64][]domain.ReportCodeRef(nil)
	if s.codeRef != nil {
		resolved, err := s.codeRef.IssueCodeRefs(ctx, versionID)
		if err != nil {
			// Code locations are best-effort (spec §6.5): a broken anchor read
			// never blocks the report.
			codeRefs = nil
		} else {
			codeRefs = resolved
		}
	}

	sources := make([]deliveryapp.ReportIssueSource, 0, len(issues))
	for _, issue := range issues {
		sources = append(sources, deliveryapp.ReportIssueSource{
			IssueID:    issue.ID,
			Title:      issue.Title,
			Severity:   issue.Severity,
			Status:     issue.Status,
			Confidence: issue.Confidence,
			Category:   issue.Category,
			CaseNodeID: issue.CaseNodeID,
			CodeRefs:   append([]domain.ReportCodeRef(nil), codeRefs[issue.ID]...),
			Revision:   issue.Revision,
			UpdatedAt:  issue.UpdatedAt,
		})
	}
	return sources, nil
}

// ListOpen implements delivery/application.AnalysisIssueReader: the open issues
// of the version injected into the triage input and used to validate an explicit
// merge target.
func (s *IssueSources) ListOpen(ctx context.Context, versionID int64, limit int) ([]domain.OpenIssue, error) {
	if s == nil || s.issues == nil {
		return nil, errors.New("delivery: issue store is not configured")
	}
	if limit <= 0 {
		return []domain.OpenIssue{}, nil
	}
	issues, _, err := s.issues.List(ctx, versionID, deliveryapp.IssueFilter{
		Status: domain.StatusOpen,
		Limit:  limit,
	})
	if err != nil {
		return nil, err
	}
	open := make([]domain.OpenIssue, 0, len(issues))
	for _, issue := range issues {
		open = append(open, domain.OpenIssue{
			IssueID:  issue.ID,
			Title:    issue.Title,
			Severity: issue.Severity,
		})
	}
	return open, nil
}
