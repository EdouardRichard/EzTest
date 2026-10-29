package application_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// fakeReportBatchReader serves the seeded batches and enforces the
// version-scoping contract of BatchRepository.GetBatch.
type fakeReportBatchReader struct {
	batches map[int64]domain.RunBatch
}

func (f *fakeReportBatchReader) GetBatch(_ context.Context, versionID, batchID int64) (domain.RunBatch, error) {
	batch, ok := f.batches[batchID]
	if !ok || batch.VersionID != versionID {
		return domain.RunBatch{}, executionapp.ErrBatchNotFound
	}
	return batch, nil
}

// fakeReportCaseRuns serves case runs newest first with the documented
// status/beforeID/limit semantics.
type fakeReportCaseRuns struct {
	runs []domain.CaseRun
}

func (f *fakeReportCaseRuns) ListCaseRuns(_ context.Context, batchID int64, status domain.CaseRunStatus, beforeID int64, limit int) ([]domain.CaseRun, bool, error) {
	matches := make([]domain.CaseRun, 0, len(f.runs))
	for _, run := range f.runs {
		if run.BatchID != batchID {
			continue
		}
		if status != "" && run.Status != status {
			continue
		}
		if beforeID != 0 && run.ID >= beforeID {
			continue
		}
		matches = append(matches, run)
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].ID > matches[j].ID })
	hasMore := false
	if len(matches) > limit {
		matches = matches[:limit]
		hasMore = true
	}
	return matches, hasMore, nil
}

type fakeReportSteps struct {
	byRun map[int64][]domain.StepResult
}

func (f *fakeReportSteps) ListByCaseRun(_ context.Context, caseRunID int64) ([]domain.StepResult, error) {
	return f.byRun[caseRunID], nil
}

type fakeReportAssertions struct {
	byRun map[int64][]domain.AssertionResult
}

func (f *fakeReportAssertions) ListByCaseRun(_ context.Context, caseRunID int64) ([]domain.AssertionResult, error) {
	return f.byRun[caseRunID], nil
}

type fakeReportEvidence struct {
	byRun map[int64][]domain.Evidence
}

func (f *fakeReportEvidence) ListByCaseRun(_ context.Context, caseRunID int64) ([]domain.Evidence, error) {
	return f.byRun[caseRunID], nil
}

type fakeReportEnvironments struct {
	environment executionapp.ReportEnvironment
	err         error
}

func (f *fakeReportEnvironments) ReportEnvironment(_ context.Context, _ int64) (executionapp.ReportEnvironment, error) {
	return f.environment, f.err
}

var (
	_ executionapp.ReportBatchReader       = (*fakeReportBatchReader)(nil)
	_ executionapp.ReportCaseRunLister     = (*fakeReportCaseRuns)(nil)
	_ executionapp.ReportStepLister        = (*fakeReportSteps)(nil)
	_ executionapp.ReportAssertionLister   = (*fakeReportAssertions)(nil)
	_ executionapp.ReportEvidenceLister    = (*fakeReportEvidence)(nil)
	_ executionapp.ReportEnvironmentSource = (*fakeReportEnvironments)(nil)
)

// reportDeps bundles the fakes a report test seeds through.
type reportDeps struct {
	batches      *fakeReportBatchReader
	caseRuns     *fakeReportCaseRuns
	steps        *fakeReportSteps
	assertions   *fakeReportAssertions
	evidence     *fakeReportEvidence
	environments *fakeReportEnvironments
	now          time.Time
}

// newReportServiceTest returns a service wired to in-memory repositories and a
// fixed clock so generated_at and the Markdown hash are reproducible.
func newReportServiceTest(t *testing.T) (*executionapp.ReportService, *reportDeps) {
	t.Helper()
	deps := &reportDeps{
		batches:      &fakeReportBatchReader{batches: map[int64]domain.RunBatch{}},
		caseRuns:     &fakeReportCaseRuns{},
		steps:        &fakeReportSteps{byRun: map[int64][]domain.StepResult{}},
		assertions:   &fakeReportAssertions{byRun: map[int64][]domain.AssertionResult{}},
		evidence:     &fakeReportEvidence{byRun: map[int64][]domain.Evidence{}},
		environments: &fakeReportEnvironments{},
		now:          time.Date(2026, 10, 28, 8, 0, 0, 0, time.UTC),
	}
	service := executionapp.NewReportService(
		deps.batches,
		deps.caseRuns,
		deps.steps,
		deps.assertions,
		deps.evidence,
		deps.environments,
		func() time.Time { return deps.now },
	)
	return service, deps
}

// seedBatch stores the batch the report is requested for. The stored counters
// are deliberately stale so the test proves the report derives its own counts.
func (d *reportDeps) seedBatch(versionID, batchID int64, batchNo int) {
	commit := "abc123"
	d.batches.batches[batchID] = domain.RunBatch{
		ID:                 batchID,
		VersionID:          versionID,
		BatchNo:            batchNo,
		Status:             domain.BatchStatusDone,
		BaselineHash:       strings.Repeat("a", 64),
		CodeBaselineCommit: &commit,
		CaseCount:          2,
		PassedCount:        2,
	}
}

// seedCaseRuns seeds one attempt of one case per supplied status, on case nodes
// 1..n.
func (d *reportDeps) seedCaseRuns(statuses ...domain.CaseRunStatus) {
	titles := []string{"下单成功", "支付失败", "库存阻塞", "搜索商品", "退出登录"}
	for i, status := range statuses {
		id := int64(i + 1)
		nodeID := int64(i + 1)
		run := domain.CaseRun{
			ID:         id,
			BatchID:    1,
			VersionID:  7,
			CaseNodeID: nodeID,
			Attempt:    1,
			Status:     status,
			DurationMS: int64(100 * (i + 1)),
			Snapshot: domain.CaseSnapshot{
				Node: domain.CaseSnapshotNode{ID: nodeID, Title: titles[i%len(titles)]},
			},
		}
		switch status {
		case domain.CaseRunStatusFailed:
			class := domain.FailureClassApplication
			run.FailureClass = &class
		case domain.CaseRunStatusBlocked:
			class := domain.FailureClassEnvironment
			run.FailureClass = &class
		}
		d.caseRuns.runs = append(d.caseRuns.runs, run)
	}
}

// seedAttempt appends one attempt of caseNodeID. An empty failureClass records a
// nil class, as a passed run must.
func (d *reportDeps) seedAttempt(caseNodeID int64, attempt int, status domain.CaseRunStatus, failureClass domain.FailureClass) {
	run := domain.CaseRun{
		ID:         int64(len(d.caseRuns.runs) + 1),
		BatchID:    1,
		VersionID:  7,
		CaseNodeID: caseNodeID,
		Attempt:    attempt,
		Status:     status,
		DurationMS: int64(attempt) * 10,
		Snapshot: domain.CaseSnapshot{
			Node: domain.CaseSnapshotNode{ID: caseNodeID, Title: fmt.Sprintf("重试用例 %d", caseNodeID)},
		},
	}
	if failureClass != "" {
		class := failureClass
		run.FailureClass = &class
	}
	d.caseRuns.runs = append(d.caseRuns.runs, run)
}

// seedArtifacts seeds the step/assertion/evidence rows the report counts.
func (d *reportDeps) seedArtifacts(caseRunID int64, steps, assertions, evidence int) {
	for i := 0; i < steps; i++ {
		d.steps.byRun[caseRunID] = append(d.steps.byRun[caseRunID], domain.StepResult{
			ID:        caseRunID*100 + int64(i) + 1,
			BatchID:   1,
			CaseRunID: caseRunID,
			Seq:       i + 1,
			Status:    domain.StepStatusPassed,
		})
	}
	for i := 0; i < assertions; i++ {
		d.assertions.byRun[caseRunID] = append(d.assertions.byRun[caseRunID], domain.AssertionResult{
			ID:          caseRunID*100 + int64(i) + 1,
			BatchID:     1,
			CaseRunID:   caseRunID,
			AssertionID: int64(i + 1),
			Verdict:     "passed",
		})
	}
	for i := 0; i < evidence; i++ {
		runID := caseRunID
		d.evidence.byRun[caseRunID] = append(d.evidence.byRun[caseRunID], domain.Evidence{
			ID:        caseRunID*100 + int64(i) + 1,
			BatchID:   1,
			CaseRunID: &runID,
			Kind:      "screenshot",
			Mime:      "image/png",
		})
	}
}

// seedReportFixture seeds the batch the aggregation and Markdown tests share:
// three first attempts (passed, failed(application), blocked(environment)) plus
// a flaky case 10 (failed(application) then passed).
func seedReportFixture(t *testing.T) (*executionapp.ReportService, *reportDeps) {
	t.Helper()
	service, deps := newReportServiceTest(t)
	deps.seedBatch(7, 1, 3)
	deps.seedCaseRuns(
		domain.CaseRunStatusPassed,
		domain.CaseRunStatusFailed,
		domain.CaseRunStatusBlocked,
	)
	deps.seedAttempt(10, 1, domain.CaseRunStatusFailed, domain.FailureClassApplication)
	deps.seedAttempt(10, 2, domain.CaseRunStatusPassed, "")

	deps.seedArtifacts(1, 2, 1, 2)
	deps.seedArtifacts(2, 1, 0, 1)
	deps.seedArtifacts(4, 1, 0, 1)

	deps.environments.environment = executionapp.ReportEnvironment{
		System:      "电商系统",
		Environment: "预发环境",
		BaseURL:     "https://staging.example.com",
	}
	return service, deps
}

func TestReportAggregatesCountsAndFlaky(t *testing.T) {
	service, deps := seedReportFixture(t)
	view, err := service.BatchReport(context.Background(), 7, 1)
	if err != nil {
		t.Fatalf("BatchReport: %v", err)
	}

	if view.Batch.ID != 1 || view.Batch.BatchNo != 3 || view.Batch.Status != domain.BatchStatusDone {
		t.Fatalf("batch = %+v, want id 1, batch_no 3, done", view.Batch)
	}
	if view.Batch.BaselineHash != strings.Repeat("a", 64) || view.Batch.CodeBaselineCommit != "abc123" {
		t.Fatalf("batch baseline/commit = %+v", view.Batch)
	}
	if !view.GeneratedAt.Equal(deps.now) {
		t.Fatalf("generated_at = %v, want %v", view.GeneratedAt, deps.now)
	}

	summary := view.Summary
	if summary.CaseCount != 5 {
		t.Fatalf("case_count = %d, want 5 (stored counters must be ignored)", summary.CaseCount)
	}
	wantStatus := map[string]int{"passed": 2, "failed": 2, "blocked": 1}
	if !reflect.DeepEqual(summary.ByStatus, wantStatus) {
		t.Fatalf("by_status = %v, want %v", summary.ByStatus, wantStatus)
	}
	wantClasses := map[string]int{"application": 2, "environment": 1}
	if !reflect.DeepEqual(summary.ByFailureClass, wantClasses) {
		t.Fatalf("by_failure_class = %v, want %v", summary.ByFailureClass, wantClasses)
	}
	if want := []int64{10}; !reflect.DeepEqual(summary.FlakyCaseIDs, want) {
		t.Fatalf("flaky_case_ids = %v, want %v", summary.FlakyCaseIDs, want)
	}
	if summary.DurationMS != 630 {
		t.Fatalf("duration_ms = %d, want 630", summary.DurationMS)
	}
	if summary.EvidenceCount != 4 {
		t.Fatalf("evidence_count = %d, want 4", summary.EvidenceCount)
	}

	if len(view.Cases) != 5 {
		t.Fatalf("cases = %d, want 5", len(view.Cases))
	}
	first := view.Cases[0]
	if first.CaseNodeID != 1 || first.Title != "下单成功" || first.Status != domain.CaseRunStatusPassed {
		t.Fatalf("first case = %+v", first)
	}
	if first.Steps != 2 || first.Assertions != 1 || first.Evidence != 2 || first.DurationMS != 100 {
		t.Fatalf("first case counts = %+v", first)
	}
	failedAttempt := view.Cases[3]
	if failedAttempt.CaseNodeID != 10 || failedAttempt.Title != "重试用例 10" ||
		failedAttempt.Status != domain.CaseRunStatusFailed || failedAttempt.FailureClass != "application" {
		t.Fatalf("failed attempt = %+v", failedAttempt)
	}
	if failedAttempt.Steps != 1 || failedAttempt.Evidence != 1 {
		t.Fatalf("failed attempt counts = %+v", failedAttempt)
	}
	if view.Cases[4].Status != domain.CaseRunStatusPassed {
		t.Fatalf("last attempt = %+v, want passed", view.Cases[4])
	}

	wantEnvironment := map[string]string{
		"system":      "电商系统",
		"environment": "预发环境",
		"base_url":    "https://staging.example.com",
	}
	if !reflect.DeepEqual(view.Environment, wantEnvironment) {
		t.Fatalf("environment = %v, want %v", view.Environment, wantEnvironment)
	}
}

func TestReportRejectsForeignBatch(t *testing.T) {
	service, _ := seedReportFixture(t)
	if _, err := service.BatchReport(context.Background(), 8, 1); !errors.Is(err, executionapp.ErrBatchNotFound) {
		t.Fatalf("foreign batch error = %v, want ErrBatchNotFound", err)
	}
}

func TestReportMarkdownContainsBatchAndCases(t *testing.T) {
	service, _ := seedReportFixture(t)
	body, hash, err := service.Markdown(context.Background(), 7, 1)
	if err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	text := string(body)
	for _, want := range []string{
		"# 执行报告：批次 #3",
		"## 摘要",
		"基线哈希：" + strings.Repeat("a", 64),
		"代码提交：abc123",
		"用例数：5",
		"通过：2",
		"失败：2",
		"阻塞：1",
		"失败分类：application=2、environment=1",
		"不稳定用例：10",
		"证据数：4",
		"生成时间：2026-10-28T08:00:00Z",
		"## 用例明细",
		"| 用例 | 用例节点 ID | 状态 | 失败分类 | 步骤 | 断言 | 证据 | 耗时（毫秒） |",
		"下单成功",
		"重试用例 10",
		"电商系统",
		"预发环境",
		"https://staging.example.com",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("markdown is missing %q:\n%s", want, text)
		}
	}

	sum := sha256.Sum256(body)
	if want := hex.EncodeToString(sum[:]); hash != want {
		t.Fatalf("sha256 = %q, want %q", hash, want)
	}
	if len(hash) != 64 || hash != strings.ToLower(hash) {
		t.Fatalf("hash %q must be 64 lowercase hex chars", hash)
	}
	if bytes.HasPrefix(body, []byte{0xEF, 0xBB, 0xBF}) {
		t.Fatal("markdown must not start with a UTF-8 BOM")
	}
	for _, forbidden := range []string{"issue", "stale", "report_id", "password", "Authorization", "Cookie"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("markdown must not contain %q:\n%s", forbidden, text)
		}
	}
}

func TestReportMarkdownEmptyBatch(t *testing.T) {
	service, deps := newReportServiceTest(t)
	deps.seedBatch(7, 1, 1)

	view, err := service.BatchReport(context.Background(), 7, 1)
	if err != nil {
		t.Fatalf("BatchReport: %v", err)
	}
	if view.Summary.CaseCount != 0 || len(view.Summary.ByStatus) != 0 ||
		len(view.Summary.ByFailureClass) != 0 || len(view.Summary.FlakyCaseIDs) != 0 ||
		view.Summary.DurationMS != 0 || view.Summary.EvidenceCount != 0 || len(view.Cases) != 0 {
		t.Fatalf("empty batch summary = %+v", view.Summary)
	}
	if view.Environment["system"] != "" || view.Environment["environment"] != "" || view.Environment["base_url"] != "" {
		t.Fatalf("empty environment = %v, want empty values", view.Environment)
	}

	body, hash, err := service.Markdown(context.Background(), 7, 1)
	if err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	text := string(body)
	for _, want := range []string{"用例数：0", "（无用例）", "生成时间：2026-10-28T08:00:00Z"} {
		if !strings.Contains(text, want) {
			t.Fatalf("empty markdown is missing %q:\n%s", want, text)
		}
	}
	sum := sha256.Sum256(body)
	if want := hex.EncodeToString(sum[:]); hash != want {
		t.Fatalf("sha256 = %q, want %q", hash, want)
	}
}

func TestReportMarkdownDeterministicHash(t *testing.T) {
	service, _ := seedReportFixture(t)
	first, firstHash, err := service.Markdown(context.Background(), 7, 1)
	if err != nil {
		t.Fatalf("first Markdown: %v", err)
	}
	second, secondHash, err := service.Markdown(context.Background(), 7, 1)
	if err != nil {
		t.Fatalf("second Markdown: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("markdown is not deterministic:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
	if firstHash != secondHash {
		t.Fatalf("hashes differ: %q != %q", firstHash, secondHash)
	}
}
