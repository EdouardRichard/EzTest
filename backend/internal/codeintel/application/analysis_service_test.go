package codeintelapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

type analysisFixture struct {
	service   *AnalysisService
	analyses  *fakeAnalysisRepo
	facts     *fakeFactRepo
	entities  *fakeEntityRepo
	git       *fakeGit
	extractor *fakeExtractor
	versions  *fakeVersionReader
	resources *fakeResourceReader
	queue     *fakeWorkQueue
	events    *fakeEvents
	audit     *fakeAudit
}

const testBlobSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func newAnalysisFixture(t *testing.T) *analysisFixture {
	t.Helper()
	fixture := &analysisFixture{
		analyses: newFakeAnalysisRepo(),
		facts:    &fakeFactRepo{},
		entities: &fakeEntityRepo{},
		git: &fakeGit{
			resolveCommit: testCommitB,
			head:          testCommitB,
			ancestor:      true,
		},
		extractor: &fakeExtractor{model: "deepseek-flash", outputs: map[string]FactExtractOutput{}, errs: map[string]error{}},
		versions: &fakeVersionReader{bindings: []VersionSUTBinding{{
			VersionSUTID: 11, VersionID: 1, RepoID: 10, Branch: "main", CodeBaselineCommit: testCommitA,
		}}},
		resources: &fakeResourceReader{repos: map[int64]RepoRef{10: {ID: 10, Name: "repo", Path: "/tmp/repo", Branch: "main"}}},
		queue:     &fakeWorkQueue{},
		events:    &fakeEvents{},
		audit:     &fakeAudit{},
	}
	fixture.service = NewAnalysisService(AnalysisDeps{
		Analyses:  fixture.analyses,
		Facts:     fixture.facts,
		Entities:  fixture.entities,
		Git:       fixture.git,
		Extractor: fixture.extractor,
		Models:    fixture.extractor,
		Bulk:      fixture.git,
		Versions:  fixture.versions,
		Resources: fixture.resources,
		Queue:     fixture.queue,
		Events:    fixture.events,
		Audit:     fixture.audit,
		Settings: &fakeSettings{
			ints: map[string]int64{
				"codeintel.max_changed_files":        20,
				"codeintel.max_file_bytes":           65536,
				"codeintel.max_excerpt_lines":        40,
				"codeintel.analysis_timeout_seconds": 60,
			},
			strings: map[string]string{"codeintel.prompt_version": "p5.1"},
		},
		Clock: fakeClock{now: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)},
		IDs:   &fakeIDs{},
	})
	return fixture
}

func TestAnalysisStartRequiresBaseline(t *testing.T) {
	fixture := newAnalysisFixture(t)
	fixture.versions.bindings[0].CodeBaselineCommit = ""
	_, _, err := fixture.service.Start(context.Background(), 1, 10, "", false)
	requireAPIError(t, err, http.StatusUnprocessableEntity, "baseline_commit_missing")
}

func TestAnalysisStartRejectsNonAncestorHead(t *testing.T) {
	fixture := newAnalysisFixture(t)
	fixture.git.ancestor = false
	_, _, err := fixture.service.Start(context.Background(), 1, 10, testCommitB, false)
	requireAPIError(t, err, http.StatusUnprocessableEntity, "invalid_head")
}

func TestAnalysisStartRejectsConflictingRepoBaselines(t *testing.T) {
	fixture := newAnalysisFixture(t)
	fixture.versions.bindings = append(fixture.versions.bindings, VersionSUTBinding{
		VersionSUTID: 12, VersionID: 1, RepoID: 10, Branch: "main", CodeBaselineCommit: testCommitB,
	})
	_, _, err := fixture.service.Start(context.Background(), 1, 10, testCommitB, false)
	apiErr := requireAPIError(t, err, http.StatusConflict, "state_conflict")
	if apiErr.Details["reason"] != "repo_baseline_conflict" {
		t.Fatalf("conflict reason = %v", apiErr.Details["reason"])
	}
}

func TestAnalysisStartReusesDoneRange(t *testing.T) {
	fixture := newAnalysisFixture(t)
	fixture.analyses.runs[9] = codeintel.Analysis{
		ID: 9, VersionID: 1, RepoID: 10, BaseCommitSHA: testCommitA, HeadCommitSHA: testCommitB,
		Status: codeintel.AnalysisDone,
	}
	fixture.analyses.order = []int64{9}
	analysis, created, err := fixture.service.Start(context.Background(), 1, 10, testCommitB, false)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if created || analysis.ID != 9 {
		t.Fatalf("reused analysis = %+v created=%v", analysis, created)
	}
	if len(fixture.queue.ensured) != 0 {
		t.Fatalf("reuse enqueued %+v", fixture.queue.ensured)
	}
}

func TestAnalysisStartForceCreatesNextVersion(t *testing.T) {
	fixture := newAnalysisFixture(t)
	fixture.analyses.runs[9] = codeintel.Analysis{
		ID: 9, VersionID: 1, RepoID: 10, BaseCommitSHA: testCommitA, HeadCommitSHA: testCommitB,
		Status: codeintel.AnalysisDone, AnalysisVersion: 1,
	}
	fixture.analyses.order = []int64{9}
	analysis, created, err := fixture.service.Start(context.Background(), 1, 10, testCommitB, true)
	if err != nil {
		t.Fatalf("Start force: %v", err)
	}
	if !created || analysis.Status != codeintel.AnalysisQueued || analysis.AnalysisVersion != 2 {
		t.Fatalf("forced analysis = %+v created=%v", analysis, created)
	}
	if len(fixture.queue.ensured) != 1 {
		t.Fatalf("work items = %+v", fixture.queue.ensured)
	}
	work := fixture.queue.ensured[0].item
	if work.Team != "code" || work.Type != "code.analyze" || work.DedupeKey != "code:analyze:"+formatID(analysis.ID) {
		t.Fatalf("work item = %+v", work)
	}
	payload, ok := work.Payload.(map[string]any)
	if !ok || payload["analysis_id"] != formatID(analysis.ID) || payload["base_commit_sha"] != testCommitA || payload["head_commit_sha"] != testCommitB {
		t.Fatalf("payload = %+v", work.Payload)
	}
}

func TestAnalysisStartSameHeadIsDoneWithoutWork(t *testing.T) {
	fixture := newAnalysisFixture(t)
	fixture.git.resolveCommit = testCommitA
	analysis, created, err := fixture.service.Start(context.Background(), 1, 10, testCommitA, false)
	if err != nil {
		t.Fatalf("Start same head: %v", err)
	}
	if !created || analysis.Status != codeintel.AnalysisDone {
		t.Fatalf("analysis = %+v created=%v", analysis, created)
	}
	if len(fixture.queue.ensured) != 0 {
		t.Fatalf("same-head analysis enqueued work: %+v", fixture.queue.ensured)
	}
}

func queuedAnalysisFixture(t *testing.T) *analysisFixture {
	t.Helper()
	fixture := newAnalysisFixture(t)
	fixture.analyses.runs[7] = codeintel.Analysis{
		ID: 7, VersionID: 1, RepoID: 10, BaseCommitSHA: testCommitA, HeadCommitSHA: testCommitB,
		Status: codeintel.AnalysisQueued, AnalysisVersion: 1,
		ChangedFiles: json.RawMessage(`[]`), Stats: json.RawMessage(`{}`),
		Unmapped: json.RawMessage(`[]`), Errors: json.RawMessage(`[]`),
		FactVersions: json.RawMessage(`{}`),
	}
	fixture.analyses.order = []int64{7}
	return fixture
}

func TestAnalysisExecuteExtractsFactsAndValidatesEvidence(t *testing.T) {
	fixture := queuedAnalysisFixture(t)
	blob := testBlobSHA
	fixture.git.diff = codeintel.DiffResult{Files: []codeintel.ChangedFile{{
		Path: "src/routes.ts", Status: "modified", Additions: 3, Deletions: 1, HeadBlobSHA: &blob,
	}}}
	fixture.git.windows = map[string]codeintel.FileWindow{
		"src/routes.ts": {Path: "src/routes.ts", Commit: testCommitB, Lines: []string{
			"import { post } from './client';", "export const orders = '/orders';", "export function refund() {}", "export const status = 200;",
		}, StartLine: 1, EndLine: 4, TotalLines: 4},
	}
	fixture.extractor.outputs["src/routes.ts"] = FactExtractOutput{
		Facts: []FactDraft{
			{
				Kind: codeintel.EntityRoute, Name: "/orders", Signature: "orders",
				StartLine: 1, EndLine: 1, Confidence: codeintel.ConfidenceHigh,
				Evidence: []EvidenceDraft{{File: "src/routes.ts", StartLine: 1, EndLine: 2}},
			},
			{
				Kind: codeintel.EntityAPI, Name: "POST /api/refund", Confidence: codeintel.ConfidenceLow,
			},
		},
		InvalidFacts: 1,
	}
	if err := fixture.service.Execute(context.Background(), 7); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	stored := fixture.analyses.runs[7]
	if stored.Status != codeintel.AnalysisDone || stored.Degraded {
		t.Fatalf("analysis = %+v", stored)
	}
	if len(fixture.facts.inserted) != 2 {
		t.Fatalf("inserted facts = %+v", fixture.facts.inserted)
	}
	routeFact := fixture.facts.inserted[0]
	if routeFact.FactKey != "route:/orders" || routeFact.Confidence != codeintel.ConfidenceHigh {
		t.Fatalf("route fact = %+v", routeFact)
	}
	if routeFact.BlobHash != "git-sha1:"+testBlobSHA || routeFact.Model != "deepseek-flash" || routeFact.PromptVersion != "p5.1" {
		t.Fatalf("route fact provenance = %+v", routeFact)
	}
	var evidence []map[string]any
	if err := json.Unmarshal(routeFact.Evidence, &evidence); err != nil || len(evidence) != 1 {
		t.Fatalf("evidence = %s (%v)", routeFact.Evidence, err)
	}
	if evidence[0]["start_line"] != float64(1) || evidence[0]["end_line"] != float64(2) ||
		!strings.HasPrefix(evidence[0]["excerpt_hash"].(string), "sha256:") {
		t.Fatalf("evidence entry = %+v", evidence[0])
	}
	apiFact := fixture.facts.inserted[1]
	if apiFact.Confidence != codeintel.ConfidenceLow {
		t.Fatalf("no-evidence fact must be low: %+v", apiFact)
	}
	if !strings.Contains(string(apiFact.Body), "clue") {
		t.Fatalf("low fact body = %s", apiFact.Body)
	}
	if len(fixture.entities.upserts) != 2 {
		t.Fatalf("entity upserts = %+v", fixture.entities.upserts)
	}

	stats := decodeObject(stored.Stats)
	if stats["facts"] != float64(2) || stats["invalidFacts"] != float64(1) || stats["evidenceChecks"] != float64(1) {
		t.Fatalf("stats = %+v", stats)
	}
	var factVersions map[string]int
	if err := json.Unmarshal(stored.FactVersions, &factVersions); err != nil ||
		factVersions["route:/orders"] != 1 || factVersions["api:POST /api/refund"] != 1 {
		t.Fatalf("fact versions = %s (%v)", stored.FactVersions, err)
	}
	types := fixture.events.types()
	if len(types) != 2 || types[0] != "code.analysis_started" || types[1] != "code.analysis_finished" {
		t.Fatalf("events = %v", types)
	}
}

func TestAnalysisExecuteInvalidEvidenceDowngradesFact(t *testing.T) {
	fixture := queuedAnalysisFixture(t)
	blob := testBlobSHA
	fixture.git.diff = codeintel.DiffResult{Files: []codeintel.ChangedFile{{
		Path: "src/one.ts", Status: "modified", HeadBlobSHA: &blob,
	}}}
	fixture.git.windows = map[string]codeintel.FileWindow{
		"src/one.ts": {Path: "src/one.ts", Commit: testCommitB, Lines: []string{"one", "two"}, TotalLines: 2},
	}
	fixture.extractor.outputs["src/one.ts"] = FactExtractOutput{Facts: []FactDraft{{
		Kind: codeintel.EntitySymbol, Name: "parse", StartLine: 1, EndLine: 1,
		Confidence: codeintel.ConfidenceHigh,
		Evidence:   []EvidenceDraft{{File: "src/one.ts", StartLine: 1, EndLine: 9}},
	}}}
	if err := fixture.service.Execute(context.Background(), 7); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(fixture.facts.inserted) != 1 {
		t.Fatalf("inserted = %+v", fixture.facts.inserted)
	}
	fact := fixture.facts.inserted[0]
	if fact.Confidence != codeintel.ConfidenceLow {
		t.Fatalf("invalid evidence fact confidence = %q", fact.Confidence)
	}
	if strings.TrimSpace(string(fact.Evidence)) != "[]" {
		t.Fatalf("invalid evidence stored: %s", fact.Evidence)
	}
	if !strings.Contains(string(fact.Body), "invalidEvidence") {
		t.Fatalf("body = %s", fact.Body)
	}
	stats := decodeObject(fixture.analyses.runs[7].Stats)
	if stats["evidenceFailures"] != float64(1) {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestAnalysisExecuteDegradedWhenExtractorFails(t *testing.T) {
	fixture := queuedAnalysisFixture(t)
	fixture.git.diff = codeintel.DiffResult{Files: []codeintel.ChangedFile{{
		Path: "src/api.ts", Status: "modified",
	}}}
	fixture.git.windows = map[string]codeintel.FileWindow{
		"src/api.ts": {Path: "src/api.ts", Commit: testCommitB, Lines: []string{"line"}, TotalLines: 1},
	}
	fixture.extractor.errs["src/api.ts"] = errors.New("upstream unavailable")
	if err := fixture.service.Execute(context.Background(), 7); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	stored := fixture.analyses.runs[7]
	if stored.Status != codeintel.AnalysisDone || !stored.Degraded {
		t.Fatalf("degraded analysis = %+v", stored)
	}
	stats := decodeObject(stored.Stats)
	if stats["aiUnavailable"] != float64(1) {
		t.Fatalf("stats = %+v", stats)
	}
	var unmapped []map[string]any
	if err := json.Unmarshal(stored.Unmapped, &unmapped); err != nil || len(unmapped) != 1 ||
		unmapped[0]["reason"] != "ai_unavailable" {
		t.Fatalf("unmapped = %s (%v)", stored.Unmapped, err)
	}
	if types := fixture.events.types(); len(types) != 2 || types[1] != "code.analysis_finished" {
		t.Fatalf("events = %v", types)
	}
}

func TestAnalysisExecuteExtractorTimeoutFailsRun(t *testing.T) {
	fixture := queuedAnalysisFixture(t)
	fixture.git.diff = codeintel.DiffResult{Files: []codeintel.ChangedFile{{
		Path: "src/api.ts", Status: "modified",
	}}}
	fixture.git.windows = map[string]codeintel.FileWindow{
		"src/api.ts": {Path: "src/api.ts", Commit: testCommitB, Lines: []string{"line"}, TotalLines: 1},
	}
	fixture.extractor.errs["src/api.ts"] = context.DeadlineExceeded
	err := fixture.service.Execute(context.Background(), 7)
	requireAPIError(t, err, http.StatusServiceUnavailable, "analysis_timeout")
	stored := fixture.analyses.runs[7]
	if stored.Status != codeintel.AnalysisFailed {
		t.Fatalf("timeout analysis = %+v", stored)
	}
	if types := fixture.events.types(); len(types) != 2 || types[1] != "code.analysis_failed" {
		t.Fatalf("events = %v", types)
	}
}

func TestAnalysisExecuteCacheHitSkipsExtractor(t *testing.T) {
	fixture := queuedAnalysisFixture(t)
	blob := testBlobSHA
	fixture.git.diff = codeintel.DiffResult{Files: []codeintel.ChangedFile{{
		Path: "src/cached.ts", Status: "modified", HeadBlobSHA: &blob,
	}}}
	fixture.facts.current = []codeintel.Fact{{
		ID: 1, RepoID: 10, CommitSHA: testCommitB, FactKey: "route:/cached", Version: 1,
		Kind: codeintel.EntityRoute, Path: "src/cached.ts", Name: "/cached", Source: "ai",
		Confidence: codeintel.ConfidenceHigh, BlobHash: "git-sha1:" + testBlobSHA,
		Model: "deepseek-flash", PromptVersion: "p5.1", Body: json.RawMessage(`{}`),
		Evidence: json.RawMessage(`[]`),
	}}
	if err := fixture.service.Execute(context.Background(), 7); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(fixture.extractor.inputs) != 0 {
		t.Fatalf("cache hit still called extractor: %+v", fixture.extractor.inputs)
	}
	if len(fixture.facts.inserted) != 0 {
		t.Fatalf("cache hit inserted facts: %+v", fixture.facts.inserted)
	}
	stats := decodeObject(fixture.analyses.runs[7].Stats)
	if stats["cacheHits"] != float64(1) {
		t.Fatalf("stats = %+v", stats)
	}
	var factVersions map[string]int
	if err := json.Unmarshal(fixture.analyses.runs[7].FactVersions, &factVersions); err != nil || factVersions["route:/cached"] != 1 {
		t.Fatalf("fact versions = %s", fixture.analyses.runs[7].FactVersions)
	}
}

func TestAnalysisExecuteCrossStackContractEdge(t *testing.T) {
	fixture := queuedAnalysisFixture(t)
	fixture.git.diff = codeintel.DiffResult{Files: []codeintel.ChangedFile{{
		Path: "src/changed.ts", Status: "modified",
	}}}
	fixture.git.windows = map[string]codeintel.FileWindow{
		"src/changed.ts": {Path: "src/changed.ts", Commit: testCommitB, Lines: []string{"line"}, TotalLines: 1},
	}
	fixture.extractor.outputs["src/changed.ts"] = FactExtractOutput{Facts: []FactDraft{}}
	apiFact := codeintel.Fact{
		ID: 21, RepoID: 10, CommitSHA: testCommitB, FactKey: "api:POST /api/refund", Version: 1,
		Kind: codeintel.EntityAPI, Path: "src/server.go", Name: "POST /api/refund", Source: "ai",
		Confidence: codeintel.ConfidenceMedium, Body: json.RawMessage(`{}`), Evidence: json.RawMessage(`[]`),
	}
	clientFact := codeintel.Fact{
		ID: 22, RepoID: 10, CommitSHA: testCommitB, FactKey: "client:POST /api/refund", Version: 1,
		Kind: codeintel.EntityClientCall, Path: "src/client.ts", Name: "POST /api/refund", Source: "ai",
		Confidence: codeintel.ConfidenceHigh, Body: json.RawMessage(`{}`), Evidence: json.RawMessage(`[]`),
	}
	fixture.facts.current = []codeintel.Fact{apiFact, clientFact}
	fixture.entities.byFactKeys = map[string]codeintel.Entity{
		apiFact.FactKey:    {ID: 502, RepoID: 10, CommitSHA: testCommitB, Kind: codeintel.EntityAPI, Path: "src/server.go", FactKey: apiFact.FactKey},
		clientFact.FactKey: {ID: 501, RepoID: 10, CommitSHA: testCommitB, Kind: codeintel.EntityClientCall, Path: "src/client.ts", FactKey: clientFact.FactKey},
	}
	if err := fixture.service.Execute(context.Background(), 7); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(fixture.entities.edges) != 1 {
		t.Fatalf("edges = %+v", fixture.entities.edges)
	}
	edge := fixture.entities.edges[0]
	if edge.Rel != codeintel.EdgeRouteToAPI || edge.SrcEntityID != 501 || edge.DstEntityID != 502 ||
		edge.Source != "ai" || edge.Confidence != codeintel.ConfidenceMedium {
		t.Fatalf("contract edge = %+v", edge)
	}
	stats := decodeObject(fixture.analyses.runs[7].Stats)
	if stats["edges"] != float64(1) {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestAnalysisExecuteFailureMarksAnalysisFailed(t *testing.T) {
	fixture := queuedAnalysisFixture(t)
	fixture.git.diffErr = &platform.Error{Status: http.StatusServiceUnavailable, Code: "git_unavailable", Message: "git unavailable", Retryable: true}
	err := fixture.service.Execute(context.Background(), 7)
	requireAPIError(t, err, http.StatusServiceUnavailable, "git_unavailable")
	stored := fixture.analyses.runs[7]
	if stored.Status != codeintel.AnalysisFailed {
		t.Fatalf("analysis status = %q", stored.Status)
	}
	var recorded []map[string]any
	if err := json.Unmarshal(stored.Errors, &recorded); err != nil || len(recorded) != 1 || recorded[0]["code"] != "git_unavailable" {
		t.Fatalf("errors = %s", stored.Errors)
	}
	if types := fixture.events.types(); len(types) != 2 || types[1] != "code.analysis_failed" {
		t.Fatalf("events = %v", types)
	}
}

func TestAnalysisChangesPreview(t *testing.T) {
	fixture := newAnalysisFixture(t)
	fixture.git.diff = codeintel.DiffResult{Files: []codeintel.ChangedFile{{Path: "src/a.ts", Status: "modified", Additions: 2}}}
	changes, err := fixture.service.Changes(context.Background(), 1, 10, "")
	if err != nil {
		t.Fatalf("Changes: %v", err)
	}
	if changes.RepoID != 10 || changes.BaseCommitSHA != testCommitA || changes.HeadCommitSHA != testCommitB ||
		len(changes.Files) != 1 || changes.Files[0].Path != "src/a.ts" {
		t.Fatalf("changes = %+v", changes)
	}
	fixture.versions.bindings[0].CodeBaselineCommit = ""
	if _, err := fixture.service.Changes(context.Background(), 1, 10, ""); err == nil {
		t.Fatal("missing baseline accepted")
	}
}

func TestAnalysisFactsAndSearch(t *testing.T) {
	fixture := newAnalysisFixture(t)
	fixture.facts.current = []codeintel.Fact{{
		ID: 1, RepoID: 10, CommitSHA: testCommitB, FactKey: "route:/orders", Version: 1,
		Kind: codeintel.EntityRoute, Path: "src/routes.ts", Source: "ai",
		Confidence: codeintel.ConfidenceHigh, Body: json.RawMessage(`{}`), Evidence: json.RawMessage(`[]`),
	}}
	facts, cursor, more, err := fixture.service.Facts(context.Background(), 10, testCommitB, "", "", "", 10)
	if err != nil || len(facts) != 1 || cursor != "" || more {
		t.Fatalf("facts = %+v cursor=%q more=%v err=%v", facts, cursor, more, err)
	}
	if _, _, _, err := fixture.service.Facts(context.Background(), 10, "bad", "", "", "", 10); err == nil {
		t.Fatal("invalid commit accepted")
	}
	fixture.entities.search = []codeintel.Entity{{ID: 3, Kind: codeintel.EntityRoute, Path: "src/routes.ts"}}
	entities, err := fixture.service.Search(context.Background(), 10, testCommitB, "routes", "", 10)
	if err != nil || len(entities) != 1 {
		t.Fatalf("search = %+v err=%v", entities, err)
	}
}

func TestAnalysisWorkHandlerDecodesAnalysisID(t *testing.T) {
	fixture := queuedAnalysisFixture(t)
	fixture.git.diff = codeintel.DiffResult{}
	handler := AnalysisWorkHandler{Service: fixture.service}
	payload, _ := json.Marshal(map[string]any{"analysis_id": formatID(7), "repo_id": "10"})
	if err := handler.Handle(context.Background(), WorkItem{ID: 1, Type: "code.analyze", Payload: payload}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if fixture.analyses.runs[7].Status != codeintel.AnalysisDone {
		t.Fatalf("analysis not executed: %+v", fixture.analyses.runs[7])
	}
	if err := handler.Handle(context.Background(), WorkItem{Payload: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("missing analysis_id accepted")
	}
}
