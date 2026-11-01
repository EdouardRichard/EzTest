package providers

import (
	"context"
	"strconv"

	chainapp "github.com/EdouardRichard/EzTest/backend/internal/chain/application"
	chain "github.com/EdouardRichard/EzTest/backend/internal/chain/domain"
	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// deliveryPageSize is the page size used while draining delivery pagination
// (the delivery repositories cap a page at 100).
const deliveryPageSize = 100

// Report ref kinds that are not exposed as delivery-domain constants (the
// delivery constants cover the issue ref kinds only). They mirror the
// delivery_report_ref kind check of the P4 migration.
const (
	reportRefKindIssue      = "issue"
	reportRefKindCaseRun    = "case_run"
	reportRefKindVersionSUT = "version_sut"
)

// IssueReader is the delivery application issue read surface: the list page
// plus the issue's logical refs (case_run/wave/requirement/version_sut).
type IssueReader interface {
	List(ctx context.Context, versionID int64, f deliveryapp.IssueFilter) ([]delivery.Issue, error)
	ListRefs(ctx context.Context, issueID int64) ([]delivery.IssueRef, error)
}

// ReportReader is the delivery application report read surface: the list page,
// the revision history and the revision refs.
type ReportReader interface {
	ListReports(ctx context.Context, versionID int64, f deliveryapp.ReportFilter) ([]delivery.Report, error)
	ListRevisions(ctx context.Context, reportID int64) ([]delivery.ReportRevision, error)
	ListRefs(ctx context.Context, reportID int64) ([]delivery.ReportRef, error)
}

// ClosureSummaryReader is the version application closure read surface consumed
// by the chain delivery provider. The version context owns requirement closures;
// the batch application read keeps the provider out of the closure repository
// and supplies the reopened-aware status of the newest closure round.
type ClosureSummaryReader interface {
	SummariesByRequirementIDs(ctx context.Context, versionID int64, requirementIDs []int64) (map[int64]versionapp.ClosureSummary, error)
}

var _ ClosureSummaryReader = (*versionapp.ClosureService)(nil)

// DeliveryProvider supplies issue, report and closure nodes. Issues/reports are
// read through the delivery application ports; closures are resolved through
// the version closure summary application read (spec §8.1).
type DeliveryProvider struct {
	issues       IssueReader
	reports      ReportReader
	requirements RequirementLister
	closures     ClosureSummaryReader
}

// NewDeliveryProvider wires the issue/report readers, the version requirement
// lister (closure enumeration) and the version closure summary reader.
func NewDeliveryProvider(
	issues IssueReader,
	reports ReportReader,
	requirements RequirementLister,
	closures ClosureSummaryReader,
) *DeliveryProvider {
	return &DeliveryProvider{issues: issues, reports: reports, requirements: requirements, closures: closures}
}

var _ chainapp.DeliveryChainProvider = (*DeliveryProvider)(nil)

// Issues returns every issue of the version with its refs projected onto the
// typed edge endpoints (case run, wave, requirement, report).
func (p *DeliveryProvider) Issues(ctx context.Context, versionID int64) ([]chain.Node, error) {
	issues, err := p.listIssues(ctx, versionID)
	if err != nil {
		return nil, err
	}
	nodes := make([]chain.Node, 0, len(issues))
	for _, issue := range issues {
		refs, err := p.issues.ListRefs(ctx, issue.ID)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, issueNode(issue, refs))
	}
	return nodes, nil
}

func (p *DeliveryProvider) listIssues(ctx context.Context, versionID int64) ([]delivery.Issue, error) {
	issues := make([]delivery.Issue, 0)
	var before int64
	for {
		page, err := p.issues.List(ctx, versionID, deliveryapp.IssueFilter{
			Before: before,
			Limit:  deliveryPageSize,
		})
		if err != nil {
			return nil, err
		}
		issues = append(issues, page...)
		if len(page) < deliveryPageSize {
			break
		}
		lastID := page[len(page)-1].ID
		if lastID <= 0 || lastID == before {
			break
		}
		before = lastID
	}
	return issues, nil
}

func issueNode(issue delivery.Issue, refs []delivery.IssueRef) chain.Node {
	node := chain.Node{
		ID:           chain.NodeID(chain.NodeIssue, issue.ID),
		Kind:         chain.NodeIssue,
		RefID:        strconv.FormatInt(issue.ID, 10),
		Label:        issue.Title,
		Status:       string(issue.Status),
		MergedIntoID: issue.MergedIntoID,
		CreatedAt:    issue.CreatedAt,
		UpdatedAt:    issue.UpdatedAt,
		Meta: map[string]any{
			"category":       issue.Category,
			"severity":       issue.Severity,
			"issue_status":   string(issue.Status),
			"source":         issue.Source,
			"reopen_count":   issue.ReopenCount,
			"evidence_count": 0,
			"code_ref_count": 0,
		},
	}
	if issue.MergedIntoID != nil {
		node.Meta["merged_into_id"] = strconv.FormatInt(*issue.MergedIntoID, 10)
	}
	sutIDs := make([]int64, 0)
	refRows := make([]map[string]any, 0, len(refs))
	evidenceCount := 0
	for _, ref := range refs {
		switch ref.Kind {
		case delivery.IssueRefKindCaseRun:
			setRefOnce(&node.CaseRunID, ref.RefID)
		case delivery.IssueRefKindCaseVersion:
			setRefOnce(&node.CaseVersionID, ref.RefID)
		case delivery.IssueRefKindWave:
			setRefOnce(&node.WaveID, ref.RefID)
		case delivery.IssueRefKindRequirement:
			setRefOnce(&node.RequirementID, ref.RefID)
		case delivery.IssueRefKindReport:
			setRefOnce(&node.ReportID, ref.RefID)
		case delivery.IssueRefKindEvidence:
			evidenceCount++
		}
		if ref.VersionSUTID != nil {
			sutIDs = append(sutIDs, *ref.VersionSUTID)
		}
		refRows = append(refRows, map[string]any{
			"kind":   ref.Kind,
			"ref_id": strconv.FormatInt(ref.RefID, 10),
		})
	}
	node.Meta["evidence_count"] = evidenceCount
	node.VersionSUTIDs = sortUniqueIDs(sutIDs)
	if len(node.VersionSUTIDs) == 1 {
		node.VersionSUTID = int64Ptr(node.VersionSUTIDs[0])
	}
	if len(refRows) > 0 {
		node.Meta["refs"] = refRows
	}
	return node
}

// Reports returns every report of the version. Report refs feed the
// included_in edges from issues/case runs and the SUT filter; the current
// revision supplies the as-of timestamp.
func (p *DeliveryProvider) Reports(ctx context.Context, versionID int64) ([]chain.Node, error) {
	reports, err := p.listReports(ctx, versionID)
	if err != nil {
		return nil, err
	}
	nodes := make([]chain.Node, 0, len(reports))
	for _, report := range reports {
		refs, err := p.reports.ListRefs(ctx, report.ID)
		if err != nil {
			return nil, err
		}
		revisions, err := p.reports.ListRevisions(ctx, report.ID)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, reportNode(report, refs, revisions))
	}
	return nodes, nil
}

func (p *DeliveryProvider) listReports(ctx context.Context, versionID int64) ([]delivery.Report, error) {
	reports := make([]delivery.Report, 0)
	var before int64
	for {
		page, err := p.reports.ListReports(ctx, versionID, deliveryapp.ReportFilter{
			Before: before,
			Limit:  deliveryPageSize,
		})
		if err != nil {
			return nil, err
		}
		reports = append(reports, page...)
		if len(page) < deliveryPageSize {
			break
		}
		lastID := page[len(page)-1].ID
		if lastID <= 0 || lastID == before {
			break
		}
		before = lastID
	}
	return reports, nil
}

func reportNode(report delivery.Report, refs []delivery.ReportRef, revisions []delivery.ReportRevision) chain.Node {
	reportID := report.ID
	node := chain.Node{
		ID:        chain.NodeID(chain.NodeReport, reportID),
		Kind:      chain.NodeReport,
		RefID:     strconv.FormatInt(reportID, 10),
		Label:     reportKindLabel(report.Kind),
		Status:    string(report.Status),
		WaveID:    report.WaveID,
		ReportID:  int64Ptr(reportID),
		CreatedAt: report.CreatedAt,
		UpdatedAt: report.UpdatedAt,
		Meta: map[string]any{
			"kind":          string(report.Kind),
			"report_kind":   string(report.Kind),
			"report_status": string(report.Status),
		},
	}
	issueIDs := make([]int64, 0)
	caseRunIDs := make([]int64, 0)
	sutIDs := make([]int64, 0)
	refRows := make([]map[string]any, 0, len(refs))
	for _, ref := range refs {
		switch ref.Kind {
		case reportRefKindIssue:
			issueIDs = append(issueIDs, ref.RefID)
		case reportRefKindCaseRun:
			caseRunIDs = append(caseRunIDs, ref.RefID)
		case reportRefKindVersionSUT:
			sutIDs = append(sutIDs, ref.RefID)
		}
		refRows = append(refRows, map[string]any{
			"kind":   ref.Kind,
			"ref_id": strconv.FormatInt(ref.RefID, 10),
		})
	}
	if report.CurrentRevisionID != nil {
		for _, revision := range revisions {
			if revision.ID != *report.CurrentRevisionID {
				continue
			}
			node.Meta["current_revision"] = int(revision.Revision)
			node.Meta["revision"] = int(revision.Revision)
			if !revision.AsOf.IsZero() {
				node.Meta["as_of"] = revision.AsOf
			}
			break
		}
	}
	if len(issueIDs) > 0 {
		node.Meta["issue_ids"] = sortUniqueIDs(issueIDs)
	}
	if len(caseRunIDs) > 0 {
		node.Meta["case_run_ids"] = sortUniqueIDs(caseRunIDs)
	}
	if len(sutIDs) > 0 {
		node.VersionSUTIDs = sortUniqueIDs(sutIDs)
		if len(node.VersionSUTIDs) == 1 {
			node.VersionSUTID = int64Ptr(node.VersionSUTIDs[0])
		}
	}
	if len(refRows) > 0 {
		node.Meta["refs"] = refRows
	}
	return node
}

func reportKindLabel(kind delivery.ReportKind) string {
	switch kind {
	case delivery.ReportKindWave:
		return "Wave report"
	case delivery.ReportKindVersion:
		return "Version report"
	default:
		return "Report"
	}
}

// Closures returns the newest closure round per requirement of the version. The
// version application batch read owns both the round and its reopened-aware
// status, so the chain never touches the version closure repository.
func (p *DeliveryProvider) Closures(ctx context.Context, versionID int64) ([]chain.Node, error) {
	requirementIDs, err := p.listRequirements(ctx, versionID)
	if err != nil {
		return nil, err
	}
	if len(requirementIDs) == 0 {
		return []chain.Node{}, nil
	}
	summaries, err := p.closures.SummariesByRequirementIDs(ctx, versionID, requirementIDs)
	if err != nil {
		return nil, err
	}
	nodes := make([]chain.Node, 0, len(summaries))
	for _, requirementID := range requirementIDs {
		summary, found := summaries[requirementID]
		if !found || summary.ID <= 0 {
			continue
		}
		node := chain.Node{
			ID:            chain.NodeID(chain.NodeClosure, summary.ID),
			Kind:          chain.NodeClosure,
			RefID:         strconv.FormatInt(summary.ID, 10),
			Label:         "Closure round " + strconv.Itoa(summary.CloseRound),
			Status:        summary.Status,
			RequirementID: optionalInt64Ptr(summary.RequirementID),
			CreatedAt:     summary.ClosedAt,
			UpdatedAt:     summary.ClosedAt,
			Meta: map[string]any{
				"close_round":    summary.CloseRound,
				"reopen_count":   summary.ReopenCount,
				"closure_status": summary.Status,
			},
		}
		if !summary.ClosedAt.IsZero() {
			node.Meta["closed_at"] = summary.ClosedAt
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

func (p *DeliveryProvider) listRequirements(ctx context.Context, versionID int64) ([]int64, error) {
	ids := make([]int64, 0)
	var before *int64
	for {
		page, hasMore, err := p.requirements.List(ctx, versionID, "", deliveryPageSize, before)
		if err != nil {
			return nil, err
		}
		for _, view := range page {
			if view.ID > 0 {
				ids = append(ids, view.ID)
			}
		}
		if !hasMore || len(page) == 0 {
			break
		}
		lastID := page[len(page)-1].ID
		if lastID <= 0 {
			break
		}
		before = &lastID
	}
	return ids, nil
}
