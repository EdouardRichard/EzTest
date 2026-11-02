// Package adapters implements the version context's requirement-closure and
// version-close consumer ports over the design, execution, delivery and agent
// application read surfaces (spec §2.1). The adapters only read; every write
// stays in the version services.
package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// Page size shared by the application readers (all repositories cap at 100).
const pageSize = 100

// WorkItemLimit is the work-item page scanned by the closure/close checks.
const WorkItemLimit = 500

// RequirementReader loads one version requirement by ID.
type RequirementReader interface {
	GetRequirement(ctx context.Context, requirementID int64) (version.Requirement, error)
}

// DesignReader is the design application current+confirmed case read surface.
type DesignReader interface {
	ListExecutable(ctx context.Context, versionID int64) ([]designapp.ExecutableCaseView, error)
}

// CaseRunReader is the execution case_run read surface.
type CaseRunReader interface {
	LatestByCaseVersions(ctx context.Context, versionID int64, caseVersionIDs []int64) (map[int64]execution.CaseRun, error)
	ListAllByWave(ctx context.Context, waveID int64) ([]execution.CaseRun, error)
}

// WaveReader is the execution wave read surface.
type WaveReader interface {
	GetActive(ctx context.Context, versionID int64) (*execution.RunWave, error)
	List(ctx context.Context, versionID int64, limit int, beforeID *int64) ([]execution.RunWave, bool, error)
}

// IntentReader is the execution intent read surface.
type IntentReader interface {
	ListActive(ctx context.Context, versionID int64) ([]execution.ExecutionIntent, error)
}

// HumanReader is the execution human_request read surface.
type HumanReader interface {
	ListByVersion(ctx context.Context, versionID int64, status string, limit int, beforeID *int64) ([]execution.HumanRequest, bool, error)
}

// WorkItemReader is the agent task-board read surface.
type WorkItemReader interface {
	ListByVersion(ctx context.Context, versionID int64, limit int) ([]agent.WorkItem, error)
}

// IssueReader is the delivery issue read surface used by the closure check
// (every issue status satisfies failed_case_has_issue).
type IssueReader interface {
	FindByCaseRun(ctx context.Context, versionID, caseRunID int64) (delivery.Issue, bool, error)
}

// Deps groups the application read surfaces of the closure adapters.
type Deps struct {
	Requirements RequirementReader
	Design       DesignReader
	CaseRuns     CaseRunReader
	Waves        WaveReader
	Intents      IntentReader
	Humans       HumanReader
	WorkItems    WorkItemReader
	Issues       IssueReader
}

// ClosureReaders implements the requirement-closure consumer ports and the
// version-close count readers. One object serves both services; the interfaces
// are disjoint so wiring passes the same value to both.
type ClosureReaders struct {
	deps Deps
}

// NewClosureReaders wires the closure/close count readers.
func NewClosureReaders(deps Deps) *ClosureReaders {
	return &ClosureReaders{deps: deps}
}

var (
	_ versionapp.ClosureCaseReader         = (*ClosureReaders)(nil)
	_ versionapp.ClosureRunReader          = (*ClosureReaders)(nil)
	_ versionapp.ClosureCoverageReader     = (*ClosureReaders)(nil)
	_ versionapp.ClosureIssueRefReader     = (*ClosureReaders)(nil)
	_ versionapp.ClosureWorkItemReader     = (*ClosureReaders)(nil)
	_ versionapp.VersionWorkItemReader     = (*ClosureReaders)(nil)
	_ versionapp.VersionExecutionReader    = (*ClosureReaders)(nil)
	_ versionapp.VersionHumanRequestReader = (*ClosureReaders)(nil)
)

// requirementCases resolves the requirement's current+confirmed case versions.
func (r *ClosureReaders) requirementCases(ctx context.Context, requirementID int64) (version.Requirement, []designapp.ExecutableCaseView, error) {
	requirement, err := r.deps.Requirements.GetRequirement(ctx, requirementID)
	if err != nil {
		return version.Requirement{}, nil, err
	}
	cases, err := r.deps.Design.ListExecutable(ctx, requirement.VersionID)
	if err != nil {
		return version.Requirement{}, nil, err
	}
	matched := make([]designapp.ExecutableCaseView, 0, len(cases))
	requirementVersionID := int64(0)
	if requirement.CurrentVersionID != nil {
		requirementVersionID = *requirement.CurrentVersionID
	}
	for _, item := range cases {
		if item.RequirementID != requirementID {
			continue
		}
		if requirementVersionID > 0 && item.RequirementVersionID != requirementVersionID {
			continue
		}
		matched = append(matched, item)
	}
	return requirement, matched, nil
}

// CurrentConfirmedCases implements versionapp.ClosureCaseReader.
func (r *ClosureReaders) CurrentConfirmedCases(ctx context.Context, requirementID int64) ([]version.CaseVersionSnapshot, error) {
	if requirementID <= 0 {
		return nil, platform.Invalid("requirement_id", "must be a positive decimal ID")
	}
	_, cases, err := r.requirementCases(ctx, requirementID)
	if err != nil {
		return nil, err
	}
	out := make([]version.CaseVersionSnapshot, 0, len(cases))
	for _, item := range cases {
		out = append(out, version.CaseVersionSnapshot{
			CaseVersionID:        item.CaseVersionID,
			CaseID:               item.CaseID,
			RequirementID:        item.RequirementID,
			RequirementVersionID: item.RequirementVersionID,
			Title:                item.Title,
		})
	}
	return out, nil
}

// LatestCaseRuns implements versionapp.ClosureRunReader.
func (r *ClosureReaders) LatestCaseRuns(ctx context.Context, requirementID int64) (map[int64]version.CaseRunSnapshot, error) {
	if requirementID <= 0 {
		return nil, platform.Invalid("requirement_id", "must be a positive decimal ID")
	}
	requirement, cases, err := r.requirementCases(ctx, requirementID)
	if err != nil {
		return nil, err
	}
	ids := caseVersionIDs(cases)
	latest := map[int64]execution.CaseRun{}
	if len(ids) > 0 {
		latest, err = r.deps.CaseRuns.LatestByCaseVersions(ctx, requirement.VersionID, ids)
		if err != nil {
			return nil, err
		}
	}
	out := make(map[int64]version.CaseRunSnapshot, len(latest))
	for caseVersionID, run := range latest {
		out[caseVersionID] = version.CaseRunSnapshot{
			CaseRunID:     run.ID,
			CaseVersionID: caseVersionID,
			Status:        string(run.Status),
			FailureClass:  string(run.FailureClass),
		}
	}
	return out, nil
}

// CountActiveIntents implements versionapp.ClosureRunReader.
func (r *ClosureReaders) CountActiveIntents(ctx context.Context, requirementID int64) (int, error) {
	if requirementID <= 0 {
		return 0, platform.Invalid("requirement_id", "must be a positive decimal ID")
	}
	requirement, cases, err := r.requirementCases(ctx, requirementID)
	if err != nil {
		return 0, err
	}
	caseSet := caseVersionIDSet(cases)
	active, err := r.deps.Intents.ListActive(ctx, requirement.VersionID)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, intent := range active {
		if caseSet[intent.CaseVersionID] {
			count++
		}
	}
	return count, nil
}

// CountInflightCaseRuns implements versionapp.ClosureRunReader.
func (r *ClosureReaders) CountInflightCaseRuns(ctx context.Context, requirementID int64) (int, error) {
	if requirementID <= 0 {
		return 0, platform.Invalid("requirement_id", "must be a positive decimal ID")
	}
	requirement, cases, err := r.requirementCases(ctx, requirementID)
	if err != nil {
		return 0, err
	}
	caseSet := caseVersionIDSet(cases)
	runs, err := r.versionRuns(ctx, requirement.VersionID)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, run := range runs {
		if !caseSet[run.CaseVersionID] {
			continue
		}
		if run.Status == execution.CaseRunPending || run.Status == execution.CaseRunRunning {
			count++
		}
	}
	return count, nil
}

// CoverageRecorded implements versionapp.ClosureCoverageReader: every current+
// confirmed case version must appear in the execution coverage projection
// (i.e. its latest case_run is readable). A requirement without confirmed cases
// reports false; the has_confirmed_case check carries the reason.
func (r *ClosureReaders) CoverageRecorded(ctx context.Context, requirementID int64) (bool, error) {
	if requirementID <= 0 {
		return false, platform.Invalid("requirement_id", "must be a positive decimal ID")
	}
	requirement, cases, err := r.requirementCases(ctx, requirementID)
	if err != nil {
		return false, err
	}
	if len(cases) == 0 {
		return false, nil
	}
	latest, err := r.deps.CaseRuns.LatestByCaseVersions(ctx, requirement.VersionID, caseVersionIDs(cases))
	if err != nil {
		return false, err
	}
	return len(latest) == len(cases), nil
}

// CaseRunIssueRefs implements versionapp.ClosureIssueRefReader: every issue
// reference of the requirement's case runs, regardless of issue status.
func (r *ClosureReaders) CaseRunIssueRefs(ctx context.Context, requirementID int64) (map[int64][]int64, error) {
	if requirementID <= 0 {
		return nil, platform.Invalid("requirement_id", "must be a positive decimal ID")
	}
	requirement, cases, err := r.requirementCases(ctx, requirementID)
	if err != nil {
		return nil, err
	}
	caseSet := caseVersionIDSet(cases)
	runs, err := r.versionRuns(ctx, requirement.VersionID)
	if err != nil {
		return nil, err
	}
	out := make(map[int64][]int64)
	for _, run := range runs {
		if !caseSet[run.CaseVersionID] {
			continue
		}
		issue, found, err := r.deps.Issues.FindByCaseRun(ctx, requirement.VersionID, run.ID)
		if err != nil {
			return nil, err
		}
		if found && issue.ID > 0 {
			out[run.ID] = append(out[run.ID], issue.ID)
		}
	}
	return out, nil
}

// OpenWorkItems implements versionapp.ClosureWorkItemReader for one
// requirement. Total counts open/claimed/running work items of the version
// whose payload references the requirement; RequirementClose counts the
// requirement.close task itself so the service can subtract it.
func (r *ClosureReaders) OpenWorkItems(ctx context.Context, requirementID int64) (versionapp.OpenWorkItemCounts, error) {
	if requirementID <= 0 {
		return versionapp.OpenWorkItemCounts{}, platform.Invalid("requirement_id", "must be a positive decimal ID")
	}
	requirement, err := r.deps.Requirements.GetRequirement(ctx, requirementID)
	if err != nil {
		return versionapp.OpenWorkItemCounts{}, err
	}
	items, err := r.deps.WorkItems.ListByVersion(ctx, requirement.VersionID, WorkItemLimit)
	if err != nil {
		return versionapp.OpenWorkItemCounts{}, err
	}
	counts := versionapp.OpenWorkItemCounts{}
	for _, item := range items {
		if !openWorkItemStatus(item.Status) {
			continue
		}
		payloadRequirementID := payloadRequirementID(item.Payload)
		if payloadRequirementID != requirementID {
			continue
		}
		counts.Total++
		if item.Type == "requirement.close" {
			counts.RequirementClose++
		}
	}
	return counts, nil
}

// BlockingWorkItems implements versionapp.VersionWorkItemReader: open/claimed/
// running work items of the version, including the version.close task itself
// (the service subtracts it).
func (r *ClosureReaders) BlockingWorkItems(ctx context.Context, versionID int64) (versionapp.BlockingWorkItemCounts, error) {
	if versionID <= 0 {
		return versionapp.BlockingWorkItemCounts{}, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	items, err := r.deps.WorkItems.ListByVersion(ctx, versionID, WorkItemLimit)
	if err != nil {
		return versionapp.BlockingWorkItemCounts{}, err
	}
	counts := versionapp.BlockingWorkItemCounts{}
	for _, item := range items {
		if !openWorkItemStatus(item.Status) {
			continue
		}
		counts.Total++
		if item.Type == "version.close" {
			counts.VersionClose++
		}
	}
	return counts, nil
}

// CountVersionActiveIntents implements versionapp.VersionExecutionReader.
func (r *ClosureReaders) CountVersionActiveIntents(ctx context.Context, versionID int64) (int, error) {
	if versionID <= 0 {
		return 0, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	active, err := r.deps.Intents.ListActive(ctx, versionID)
	if err != nil {
		return 0, err
	}
	return len(active), nil
}

// CountActiveWaves implements versionapp.VersionExecutionReader. P3 keeps at
// most one planned/running/paused wave per version.
func (r *ClosureReaders) CountActiveWaves(ctx context.Context, versionID int64) (int, error) {
	if versionID <= 0 {
		return 0, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	wave, err := r.deps.Waves.GetActive(ctx, versionID)
	if err != nil {
		return 0, err
	}
	if wave == nil {
		return 0, nil
	}
	return 1, nil
}

// CountVersionInflightCaseRuns implements versionapp.VersionExecutionReader.
func (r *ClosureReaders) CountVersionInflightCaseRuns(ctx context.Context, versionID int64) (int, error) {
	if versionID <= 0 {
		return 0, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	runs, err := r.versionRuns(ctx, versionID)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, run := range runs {
		if run.Status == execution.CaseRunPending || run.Status == execution.CaseRunRunning {
			count++
		}
	}
	return count, nil
}

// CountOpenHumanRequests implements versionapp.VersionHumanRequestReader.
func (r *ClosureReaders) CountOpenHumanRequests(ctx context.Context, versionID int64) (int, error) {
	if versionID <= 0 {
		return 0, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	count := 0
	var before *int64
	for {
		page, hasMore, err := r.deps.Humans.ListByVersion(ctx, versionID, string(execution.HumanOpen), pageSize, before)
		if err != nil {
			return 0, err
		}
		count += len(page)
		if !hasMore || len(page) == 0 {
			break
		}
		last := page[len(page)-1].ID
		if last <= 0 {
			break
		}
		before = &last
	}
	return count, nil
}

// versionRuns returns every case_run of a version through its waves.
func (r *ClosureReaders) versionRuns(ctx context.Context, versionID int64) ([]execution.CaseRun, error) {
	waves := make([]execution.RunWave, 0)
	var before *int64
	for {
		page, hasMore, err := r.deps.Waves.List(ctx, versionID, pageSize, before)
		if err != nil {
			return nil, err
		}
		waves = append(waves, page...)
		if !hasMore || len(page) == 0 {
			break
		}
		last := page[len(page)-1].ID
		if last <= 0 {
			break
		}
		before = &last
	}
	runs := make([]execution.CaseRun, 0)
	for _, wave := range waves {
		rows, err := r.deps.CaseRuns.ListAllByWave(ctx, wave.ID)
		if err != nil {
			return nil, err
		}
		runs = append(runs, rows...)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].ID < runs[j].ID })
	return runs, nil
}

// caseVersionIDs returns the case version IDs in ascending order.
func caseVersionIDs(cases []designapp.ExecutableCaseView) []int64 {
	ids := make([]int64, 0, len(cases))
	for _, item := range cases {
		if item.CaseVersionID > 0 {
			ids = append(ids, item.CaseVersionID)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// caseVersionIDSet indexes cases by case version ID.
func caseVersionIDSet(cases []designapp.ExecutableCaseView) map[int64]bool {
	set := make(map[int64]bool, len(cases))
	for _, item := range cases {
		if item.CaseVersionID > 0 {
			set[item.CaseVersionID] = true
		}
	}
	return set
}

// openWorkItemStatus reports the blocking work-item statuses (open/claimed/
// running; waiting_human is represented by paused waves/open human requests,
// P3-D-25).
func openWorkItemStatus(status agent.WorkItemStatus) bool {
	switch status {
	case agent.WorkItemOpen, agent.WorkItemClaimed, agent.WorkItemRunning:
		return true
	default:
		return false
	}
}

// payloadRequirementID parses requirement_id from a work-item payload (decimal
// string or JSON number). Missing/malformed payloads reference no requirement.
func payloadRequirementID(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return 0
	}
	switch value := payload["requirement_id"].(type) {
	case string:
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return 0
		}
		return parsed
	case float64:
		if value <= 0 {
			return 0
		}
		return int64(value)
	default:
		return 0
	}
}

// DeliveryReportStatusReader is the delivery application report status surface
// (*deliveryapp.ReportService satisfies it).
type DeliveryReportStatusReader interface {
	VersionReportStatus(ctx context.Context, versionID int64) (deliveryapp.VersionReportStatus, error)
}

// ReportStatusReader adapts the delivery report status projection onto the
// version-context close predicate (spec §7.3 check 8).
type ReportStatusReader struct {
	reader DeliveryReportStatusReader
}

// NewReportStatusReader wires the delivery report status reader.
func NewReportStatusReader(reader DeliveryReportStatusReader) *ReportStatusReader {
	return &ReportStatusReader{reader: reader}
}

var _ versionapp.ReportStatusReader = (*ReportStatusReader)(nil)

// VersionReportStatus implements versionapp.ReportStatusReader.
func (r *ReportStatusReader) VersionReportStatus(ctx context.Context, versionID int64) (version.ReportStatus, error) {
	if versionID <= 0 {
		return version.ReportStatus{}, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if r == nil || r.reader == nil {
		return version.ReportStatus{}, fmt.Errorf("version report status reader is not configured")
	}
	status, err := r.reader.VersionReportStatus(ctx, versionID)
	if err != nil {
		return version.ReportStatus{}, err
	}
	return version.ReportStatus{
		Status:             string(status.Status),
		Fingerprint:        status.Fingerprint,
		RevisionID:         status.RevisionID,
		CurrentFingerprint: status.CurrentFingerprint,
	}, nil
}
