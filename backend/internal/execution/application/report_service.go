package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// reportCaseRunPageSize bounds the page drained from CaseRunRepository while
// building a report. The report reads every attempt of the batch, so the page is
// large; the loop still handles pagination and never relies on a single page.
const reportCaseRunPageSize = 200

// ReportView is the derived Batch 1 report of one execution batch (spec §6.3,
// D42). Every field is derived from stored batch/case-run/step/assertion/
// evidence rows; P6 has no report table and deliberately excludes issue,
// report-revision and stale fields (those belong to P7).
type ReportView struct {
	Batch       ReportBatchView
	Summary     ReportSummary
	Cases       []ReportCase
	Environment map[string]string
	GeneratedAt time.Time
}

// ReportBatchView is the batch header of a report.
type ReportBatchView struct {
	ID                 int64
	BatchNo            int
	Status             domain.BatchStatus
	BaselineHash       string
	CodeBaselineCommit string
}

// ReportSummary is the derived tally of a report. ByStatus and ByFailureClass
// only carry non-zero buckets, FlakyCaseIDs is deduplicated and ascending, and
// DurationMS is the sum of the case run durations.
type ReportSummary struct {
	CaseCount      int
	ByStatus       map[string]int
	ByFailureClass map[string]int
	FlakyCaseIDs   []int64
	DurationMS     int64
	EvidenceCount  int
}

// ReportCase is one row of the report's case table. It describes one case run
// (one attempt), so a flaky case appears once per attempt.
type ReportCase struct {
	CaseRunID    int64
	CaseNodeID   int64
	Title        string
	Status       domain.CaseRunStatus
	FailureClass string
	Steps        int
	Assertions   int
	Evidence     int
	DurationMS   int64
}

// ReportEnvironment is the environment projection rendered in the report
// header. The execution context does not own systems/environments, so a source
// adapter resolves them from the batch's version.
type ReportEnvironment struct {
	System      string
	Environment string
	BaseURL     string
}

// ReportEnvironmentSource resolves the environment of a version for a report.
// It is a narrow read-only port; the composition root adapts the version and
// resource services to it.
type ReportEnvironmentSource interface {
	ReportEnvironment(ctx context.Context, versionID int64) (ReportEnvironment, error)
}

// ReportBatchReader is the narrow batch read the report needs. BatchRepository
// satisfies it.
type ReportBatchReader interface {
	GetBatch(ctx context.Context, versionID, batchID int64) (domain.RunBatch, error)
}

// ReportCaseRunLister is the narrow case run read the report needs, including
// every attempt. CaseRunRepository satisfies it.
type ReportCaseRunLister interface {
	ListCaseRuns(ctx context.Context, batchID int64, status domain.CaseRunStatus, beforeID int64, limit int) ([]domain.CaseRun, bool, error)
}

// ReportStepLister is the narrow step read the report needs. StepRepository
// satisfies it.
type ReportStepLister interface {
	ListByCaseRun(ctx context.Context, caseRunID int64) ([]domain.StepResult, error)
}

// ReportAssertionLister is the narrow assertion read the report needs.
// AssertionRepository satisfies it.
type ReportAssertionLister interface {
	ListByCaseRun(ctx context.Context, caseRunID int64) ([]domain.AssertionResult, error)
}

// ReportEvidenceLister is the narrow evidence read the report needs.
// EvidenceRepository satisfies it.
type ReportEvidenceLister interface {
	ListByCaseRun(ctx context.Context, caseRunID int64) ([]domain.Evidence, error)
}

// ReportService builds the derived Batch 1 report. It never writes: the report
// is recomputed from the stored transcript on every request.
type ReportService struct {
	batches      ReportBatchReader
	caseRuns     ReportCaseRunLister
	steps        ReportStepLister
	assertions   ReportAssertionLister
	evidence     ReportEvidenceLister
	environments ReportEnvironmentSource
	now          func() time.Time
}

// NewReportService wires the report service. The repositories are narrowed to
// the read methods the report uses; environments may be nil, in which case the
// report renders an unbound environment. now may be nil, in which case the wall
// clock is used.
func NewReportService(
	batches ReportBatchReader,
	caseRuns ReportCaseRunLister,
	steps ReportStepLister,
	assertions ReportAssertionLister,
	evidence ReportEvidenceLister,
	environments ReportEnvironmentSource,
	now func() time.Time,
) *ReportService {
	if now == nil {
		now = time.Now
	}
	return &ReportService{
		batches:      batches,
		caseRuns:     caseRuns,
		steps:        steps,
		assertions:   assertions,
		evidence:     evidence,
		environments: environments,
		now:          now,
	}
}

// BatchReport derives the report of one batch of versionID. A missing or
// foreign batch is reported as ErrBatchNotFound, unchanged from the repository.
func (s *ReportService) BatchReport(ctx context.Context, versionID, batchID int64) (ReportView, error) {
	batch, err := s.batches.GetBatch(ctx, versionID, batchID)
	if err != nil {
		return ReportView{}, err
	}
	runs, err := s.listCaseRuns(ctx, batchID)
	if err != nil {
		return ReportView{}, err
	}
	// The report must not depend on the repository's page order: sort by case
	// then attempt so the JSON and the Markdown are reproducible.
	sort.SliceStable(runs, func(i, j int) bool {
		if runs[i].CaseNodeID != runs[j].CaseNodeID {
			return runs[i].CaseNodeID < runs[j].CaseNodeID
		}
		if runs[i].Attempt != runs[j].Attempt {
			return runs[i].Attempt < runs[j].Attempt
		}
		return runs[i].ID < runs[j].ID
	})

	counts := domain.Aggregate(runs)
	summary := ReportSummary{
		CaseCount:      counts.CaseCount,
		ByStatus:       reportStatusCounts(counts),
		ByFailureClass: make(map[string]int),
		FlakyCaseIDs:   domain.FlakyCaseIDs(runs),
	}
	if summary.FlakyCaseIDs == nil {
		summary.FlakyCaseIDs = make([]int64, 0)
	}

	reportCases := make([]ReportCase, 0, len(runs))
	for _, run := range runs {
		steps, err := s.steps.ListByCaseRun(ctx, run.ID)
		if err != nil {
			return ReportView{}, err
		}
		assertions, err := s.assertions.ListByCaseRun(ctx, run.ID)
		if err != nil {
			return ReportView{}, err
		}
		artifacts, err := s.evidence.ListByCaseRun(ctx, run.ID)
		if err != nil {
			return ReportView{}, err
		}

		failureClass := ""
		if run.FailureClass != nil {
			failureClass = string(*run.FailureClass)
			summary.ByFailureClass[failureClass]++
		}
		summary.DurationMS += run.DurationMS
		summary.EvidenceCount += len(artifacts)
		reportCases = append(reportCases, ReportCase{
			CaseRunID:    run.ID,
			CaseNodeID:   run.CaseNodeID,
			Title:        run.Snapshot.Node.Title,
			Status:       run.Status,
			FailureClass: failureClass,
			Steps:        len(steps),
			Assertions:   len(assertions),
			Evidence:     len(artifacts),
			DurationMS:   run.DurationMS,
		})
	}

	environment, err := s.reportEnvironment(ctx, versionID)
	if err != nil {
		return ReportView{}, err
	}

	return ReportView{
		Batch: ReportBatchView{
			ID:                 batch.ID,
			BatchNo:            batch.BatchNo,
			Status:             batch.Status,
			BaselineHash:       batch.BaselineHash,
			CodeBaselineCommit: reportStringValue(batch.CodeBaselineCommit),
		},
		Summary:     summary,
		Cases:       reportCases,
		Environment: environment,
		GeneratedAt: s.now().UTC(),
	}, nil
}

// Markdown renders the deterministic Batch 1 Markdown export and returns it
// together with its lowercase hex SHA-256. The output carries batch identity,
// baseline/commit, status and failure-class counts, the per-case table with
// evidence counts, the environment header and generated_at; it never contains
// issue, revision or stale fields, and it never contains secrets.
func (s *ReportService) Markdown(ctx context.Context, versionID, batchID int64) ([]byte, string, error) {
	view, err := s.BatchReport(ctx, versionID, batchID)
	if err != nil {
		return nil, "", err
	}
	body := reportRenderMarkdown(view)
	sum := sha256.Sum256(body)
	return body, hex.EncodeToString(sum[:]), nil
}

// listCaseRuns drains every page of the batch, newest first as returned by the
// repository, and reports the rows in repository order. The caller sorts them.
func (s *ReportService) listCaseRuns(ctx context.Context, batchID int64) ([]domain.CaseRun, error) {
	runs := make([]domain.CaseRun, 0)
	beforeID := int64(0)
	for {
		page, hasMore, err := s.caseRuns.ListCaseRuns(ctx, batchID, "", beforeID, reportCaseRunPageSize)
		if err != nil {
			return nil, err
		}
		runs = append(runs, page...)
		if !hasMore {
			return runs, nil
		}
		if len(page) == 0 {
			return nil, fmt.Errorf("execution report: case run page after %d is empty but reports more", beforeID)
		}
		next := page[len(page)-1].ID
		if next == beforeID {
			return nil, fmt.Errorf("execution report: case run page did not advance past %d", beforeID)
		}
		beforeID = next
	}
}

// reportEnvironment resolves the header environment. The three keys are always
// present so the JSON shape is stable even when the source is not configured.
func (s *ReportService) reportEnvironment(ctx context.Context, versionID int64) (map[string]string, error) {
	environment := map[string]string{"system": "", "environment": "", "base_url": ""}
	if s.environments == nil {
		return environment, nil
	}
	resolved, err := s.environments.ReportEnvironment(ctx, versionID)
	if err != nil {
		return nil, err
	}
	environment["system"] = resolved.System
	environment["environment"] = resolved.Environment
	environment["base_url"] = resolved.BaseURL
	return environment, nil
}

// reportStatusCounts maps the terminal tallies to the report's by_status map,
// dropping zero buckets to match the spec's example shape.
func reportStatusCounts(counts domain.BatchCounts) map[string]int {
	byStatus := make(map[string]int)
	buckets := []struct {
		status domain.CaseRunStatus
		count  int
	}{
		{domain.CaseRunStatusPassed, counts.PassedCount},
		{domain.CaseRunStatusFailed, counts.FailedCount},
		{domain.CaseRunStatusBlocked, counts.BlockedCount},
		{domain.CaseRunStatusInconclusive, counts.InconclusiveCount},
		{domain.CaseRunStatusSkipped, counts.SkippedCount},
		{domain.CaseRunStatusInterrupted, counts.InterruptedCount},
	}
	for _, bucket := range buckets {
		if bucket.count > 0 {
			byStatus[string(bucket.status)] = bucket.count
		}
	}
	return byStatus
}

// reportStringValue unwraps an optional string, mapping nil to the empty string.
func reportStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// reportRenderMarkdown renders the deterministic Markdown report. Map keys are
// always sorted before being printed so the bytes do not depend on Go's map
// iteration order.
func reportRenderMarkdown(view ReportView) []byte {
	var builder strings.Builder

	fmt.Fprintf(&builder, "# 执行报告：批次 #%d\n\n", view.Batch.BatchNo)
	builder.WriteString("## 摘要\n\n")
	fmt.Fprintf(&builder, "- 批次 ID：%d\n", view.Batch.ID)
	fmt.Fprintf(&builder, "- 批次号：%d\n", view.Batch.BatchNo)
	fmt.Fprintf(&builder, "- 状态：%s\n", view.Batch.Status)
	fmt.Fprintf(&builder, "- 基线哈希：%s\n", reportPlaceholder(view.Batch.BaselineHash))
	fmt.Fprintf(&builder, "- 代码提交：%s\n", reportPlaceholder(view.Batch.CodeBaselineCommit))
	fmt.Fprintf(&builder, "- 用例数：%d\n", view.Summary.CaseCount)
	fmt.Fprintf(&builder, "- 通过：%d\n", view.Summary.ByStatus[string(domain.CaseRunStatusPassed)])
	fmt.Fprintf(&builder, "- 失败：%d\n", view.Summary.ByStatus[string(domain.CaseRunStatusFailed)])
	fmt.Fprintf(&builder, "- 阻塞：%d\n", view.Summary.ByStatus[string(domain.CaseRunStatusBlocked)])
	fmt.Fprintf(&builder, "- 不确定：%d\n", view.Summary.ByStatus[string(domain.CaseRunStatusInconclusive)])
	fmt.Fprintf(&builder, "- 跳过：%d\n", view.Summary.ByStatus[string(domain.CaseRunStatusSkipped)])
	fmt.Fprintf(&builder, "- 中断：%d\n", view.Summary.ByStatus[string(domain.CaseRunStatusInterrupted)])
	fmt.Fprintf(&builder, "- 失败分类：%s\n", reportFailureClassSummary(view.Summary.ByFailureClass))
	fmt.Fprintf(&builder, "- 不稳定用例：%s\n", reportFlakyCaseIDs(view.Summary.FlakyCaseIDs))
	fmt.Fprintf(&builder, "- 证据数：%d\n", view.Summary.EvidenceCount)
	fmt.Fprintf(&builder, "- 总耗时：%d 毫秒\n", view.Summary.DurationMS)
	fmt.Fprintf(&builder, "- 系统：%s\n", reportPlaceholder(view.Environment["system"]))
	fmt.Fprintf(&builder, "- 环境：%s\n", reportPlaceholder(view.Environment["environment"]))
	fmt.Fprintf(&builder, "- 基础 URL：%s\n", reportPlaceholder(view.Environment["base_url"]))
	fmt.Fprintf(&builder, "- 生成时间：%s\n", view.GeneratedAt.UTC().Format(time.RFC3339))

	builder.WriteString("\n## 用例明细\n\n")
	builder.WriteString("| 用例 | 用例节点 ID | 状态 | 失败分类 | 步骤 | 断言 | 证据 | 耗时（毫秒） |\n")
	builder.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- |\n")
	if len(view.Cases) == 0 {
		builder.WriteString("| （无用例） | — | — | — | — | — | — | — |\n")
	}
	for _, reportCase := range view.Cases {
		fmt.Fprintf(&builder, "| %s | %d | %s | %s | %d | %d | %d | %d |\n",
			reportMarkdownCell(reportCase.Title),
			reportCase.CaseNodeID,
			reportCase.Status,
			reportMarkdownCell(reportCase.FailureClass),
			reportCase.Steps,
			reportCase.Assertions,
			reportCase.Evidence,
			reportCase.DurationMS,
		)
	}

	return []byte(builder.String())
}

// reportFailureClassSummary renders the sorted "class=count" list.
func reportFailureClassSummary(counts map[string]int) string {
	if len(counts) == 0 {
		return "（无）"
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, counts[key]))
	}
	return strings.Join(parts, "、")
}

// reportFlakyCaseIDs renders the sorted case node ids.
func reportFlakyCaseIDs(ids []int64) string {
	if len(ids) == 0 {
		return "（无）"
	}
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	return strings.Join(parts, "、")
}

// reportInline folds newlines into spaces so one bullet stays on one line.
func reportInline(value string) string {
	replacer := strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ")
	return strings.TrimSpace(replacer.Replace(value))
}

// reportPlaceholder returns value or the Chinese "（未绑定）" placeholder.
func reportPlaceholder(value string) string {
	trimmed := reportInline(value)
	if trimmed == "" {
		return "（未绑定）"
	}
	return trimmed
}

// reportMarkdownCell prepares one table cell and renders empty values as a dash.
func reportMarkdownCell(value string) string {
	trimmed := strings.ReplaceAll(reportInline(value), "|", `\|`)
	if trimmed == "" {
		return "—"
	}
	return trimmed
}
