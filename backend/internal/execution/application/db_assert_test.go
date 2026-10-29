package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"strings"
	"sync"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

const (
	dbAssertSQL      = "SELECT count(*) FROM orders"
	dbAssertExpected = `[[3]]`
)

// dbAssertSnapshot is a one-step case with a single db assertion.
func dbAssertSnapshot(expected string) domain.CaseSnapshot {
	return domain.CaseSnapshot{
		Node: domain.CaseSnapshotNode{ID: 10, Title: "db case", Priority: "P0"},
		Steps: []domain.CaseSnapshotStep{
			{Seq: 1, Action: "open orders", Expected: "orders shown"},
		},
		Assertions: []domain.CaseSnapshotAssertion{{
			ID: 21, StepSeq: 1, Kind: "db", Target: "orders",
			Expression: dbAssertSQL, Expected: expected,
		}},
		GraphHash: testBaselineHash,
	}
}

// recDBQuery is an in-memory DBQueryPort double.
type recDBQuery struct {
	target    DBTargetRef
	targetErr error
	result    DBQueryResult
	queryErr  error

	calls  int
	envID  int64
	sql    string
	limits DBQueryLimits
}

func (q *recDBQuery) Target(_ context.Context, environmentID int64) (DBTargetRef, error) {
	q.envID = environmentID
	if q.targetErr != nil {
		return DBTargetRef{}, q.targetErr
	}
	return q.target, nil
}

func (q *recDBQuery) Query(_ context.Context, environmentID int64, sql string, limits DBQueryLimits) (DBQueryResult, error) {
	q.calls++
	q.envID = environmentID
	q.sql = sql
	q.limits = limits
	if q.queryErr != nil {
		return DBQueryResult{}, q.queryErr
	}
	return q.result, nil
}

// recEvidenceStore is an in-memory EvidenceStore double.
type recEvidenceStore struct {
	mu     sync.Mutex
	files  map[string][]byte
	writes int
}

func (s *recEvidenceStore) Write(_ context.Context, rel string, data []byte) (string, string, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.files == nil {
		s.files = map[string][]byte{}
	}
	copied := append([]byte(nil), data...)
	s.files[rel] = copied
	s.writes++
	sum := sha256.Sum256(copied)
	return rel, hex.EncodeToString(sum[:]), int64(len(copied)), nil
}

func (s *recEvidenceStore) Open(_ context.Context, rel string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.files[rel]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return io.NopCloser(strings.NewReader(string(data))), nil
}

func (s *recEvidenceStore) content(rel string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.files[rel]...)
}

// newDBAssertTest wires a recorder to the db ports plus a real EvidenceService
// (fake repositories/store) so the db_result evidence and refs are exercised.
func newDBAssertTest(t *testing.T) (*CaseRecorder, *recWorld, *recDBQuery, *recEvidenceStore) {
	t.Helper()
	recorder, world := newRecorderTest(t)
	// Reserve the id range the seeded evidence rows already use.
	world.ids.next = 1000
	query := &recDBQuery{
		target: DBTargetRef{
			EnvironmentID: schedEnvID, Name: "orders", MaxRows: 100, StatementTimeoutMS: 5000,
		},
		result: DBQueryResult{
			Columns: []string{"count"}, Rows: [][]string{{"3"}}, RowCount: 1, ElapsedMS: 7,
		},
	}
	store := &recEvidenceStore{}
	recorder.deps.DBQuery = query
	recorder.deps.DBCapture = NewEvidenceService(world.evidence, world.refs, nil, store, world.ids)
	return recorder, world, query, store
}

// recordDBAssertStep records the snapshot's only step without evidence; the db
// evidence is what the finish gate must count.
func recordDBAssertStep(t *testing.T, recorder *CaseRecorder, run domain.CaseRun) {
	t.Helper()
	if _, err := recorder.RecordStep(context.Background(), run.ID, StepRecordInput{
		Seq: 1, Status: domain.StepStatusPassed, Actual: "done",
	}); err != nil {
		t.Fatalf("RecordStep: %v", err)
	}
}

// dbPreview is the decoded ≤4 KiB actual preview of a db assertion. ElapsedMS
// is a pointer so a missing field is distinguishable from a zero duration.
type dbPreview struct {
	RowCount  int    `json:"rowCount"`
	Truncated bool   `json:"truncated"`
	ElapsedMS *int64 `json:"elapsedMs"`
}

// parseDBPreview decodes a recorded actual preview.
func parseDBPreview(t *testing.T, preview string) dbPreview {
	t.Helper()
	var decoded dbPreview
	if err := json.Unmarshal([]byte(preview), &decoded); err != nil {
		t.Fatalf("db preview is not JSON: %v (%q)", err, preview)
	}
	return decoded
}

// wantDBPreviewElapsed asserts one actual preview carries its execution time.
func wantDBPreviewElapsed(t *testing.T, preview string, want int64) {
	t.Helper()
	decoded := parseDBPreview(t, preview)
	if decoded.ElapsedMS == nil || *decoded.ElapsedMS != want {
		t.Fatalf("db preview elapsedMs = %v, want %d (preview %q)",
			decoded.ElapsedMS, want, preview)
	}
}

// TestDBAssertPassedRecordsEvidenceAndResult pins the successful path: the
// server reads the SQL/expected/target from the snapshot, executes once, writes
// a db_result evidence artifact linked to the assertion result and a passed
// assertion_result, and the db evidence satisfies the finish evidence gate with
// zero step evidence.
func TestDBAssertPassedRecordsEvidenceAndResult(t *testing.T) {
	recorder, world, query, store := newDBAssertTest(t)
	run := world.seedRunningRun(dbAssertSnapshot(dbAssertExpected))
	recordDBAssertStep(t, recorder, run)

	outcome, err := recorder.DBAssert(context.Background(), run.ID, 21, DBAssertLimits{})
	if err != nil {
		t.Fatalf("DBAssert: %v", err)
	}
	if outcome.Verdict != "passed" || outcome.Repeated {
		t.Fatalf("outcome = %+v", outcome)
	}
	if outcome.RowCount != 1 || outcome.Truncated || outcome.ElapsedMS != 7 {
		t.Fatalf("outcome counts = %+v", outcome)
	}
	if query.calls != 1 || query.sql != dbAssertSQL {
		t.Fatalf("query calls/sql = %d/%q", query.calls, query.sql)
	}
	if query.envID != schedEnvID {
		t.Fatalf("query environment = %d, want %d", query.envID, schedEnvID)
	}

	results := world.assertions.rows[run.ID]
	if len(results) != 1 {
		t.Fatalf("assertion results = %+v", results)
	}
	assertion := results[0]
	if assertion.Kind != "db" || assertion.Verdict != "passed" ||
		assertion.Target != "orders" || assertion.Expected != dbAssertExpected ||
		assertion.Actual != outcome.ActualPreview || assertion.FailureReason != "" {
		t.Fatalf("assertion result = %+v", assertion)
	}
	preview := parseDBPreview(t, outcome.ActualPreview)
	if preview.RowCount != 1 || preview.Truncated ||
		preview.ElapsedMS == nil || *preview.ElapsedMS != 7 {
		t.Fatalf("preview = %+v (%q), want rowCount 1, not truncated and elapsedMs 7",
			preview, outcome.ActualPreview)
	}

	evidenceRows, err := world.evidence.ListByCaseRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidenceRows) != 1 {
		t.Fatalf("db evidence rows = %d, want 1", len(evidenceRows))
	}
	dbEvidence := evidenceRows[0]
	if dbEvidence.Kind != "db_result" || dbEvidence.Mime != "application/json" ||
		dbEvidence.SizeBytes <= 0 || dbEvidence.SHA256 == "" {
		t.Fatalf("db evidence = %+v", dbEvidence)
	}
	if dbEvidence.CaseRunID == nil || *dbEvidence.CaseRunID != run.ID {
		t.Fatalf("db evidence scope = %+v", dbEvidence)
	}
	for _, ref := range world.refs.all() {
		if ref.RefKind == "assertion_result" && ref.RefID == assertion.ID && ref.EvidenceID == dbEvidence.ID {
			goto refsChecked
		}
	}
	t.Fatalf("assertion_result evidence ref missing: %+v", world.refs.all())
refsChecked:

	data := store.content(dbEvidence.Path)
	var document struct {
		SQL       string     `json:"sql"`
		Columns   []string   `json:"columns"`
		Rows      [][]string `json:"rows"`
		RowCount  int        `json:"row_count"`
		Truncated bool       `json:"truncated"`
		ElapsedMS int64      `json:"elapsed_ms"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("db evidence is not JSON: %v (%s)", err, data)
	}
	if document.SQL != dbAssertSQL || document.RowCount != 1 || document.Truncated ||
		document.ElapsedMS != 7 || len(document.Rows) != 1 || document.Rows[0][0] != "3" {
		t.Fatalf("db evidence document = %+v", document)
	}
	lower := strings.ToLower(string(data))
	for _, secret := range []string{"password", "authorization", "secret"} {
		if strings.Contains(lower, secret) {
			t.Fatalf("db evidence leaked %q: %s", secret, data)
		}
	}

	// Zero step evidence: the captured db_result artifact alone satisfies the
	// spec §6.6.2 evidence gate for a passed finish.
	finished, err := recorder.FinishCase(context.Background(), run.ID, FinishInput{
		Decision: domain.FinishDecision{Verdict: domain.VerdictPassed},
	})
	if err != nil {
		t.Fatalf("FinishCase passed with db evidence: %v", err)
	}
	if finished.Status != domain.CaseRunStatusPassed {
		t.Fatalf("status = %s, want passed", finished.Status)
	}
	// The terminal event counts the db_result artifact even though the finish
	// submission carried no evidence ids.
	event := world.events.firstOf("run.finished")
	if event == nil {
		t.Fatal("run.finished event missing")
	}
	if got := event.Detail["evidence_count"]; got != 1 {
		t.Fatalf("run.finished evidence_count = %#v, want 1 (db evidence)", got)
	}
}

// TestDBAssertPayloadTruncationKeepsPreviewConsistent pins that a db_result
// evidence document truncated to the payload cap also reports truncated=true in
// the recorded actual preview and the not_run verdict.
func TestDBAssertPayloadTruncationKeepsPreviewConsistent(t *testing.T) {
	recorder, world, query, _ := newDBAssertTest(t)
	big := strings.Repeat("x", 4000)
	query.result = DBQueryResult{
		Columns: []string{"value"}, Rows: [][]string{{big}}, RowCount: 1, ElapsedMS: 2,
	}
	// Keep the expected small and valid: the truncation override, not the value
	// comparison, decides the verdict.
	run := world.seedRunningRun(dbAssertSnapshot(dbAssertExpected))
	recordDBAssertStep(t, recorder, run)

	outcome, err := recorder.DBAssert(context.Background(), run.ID, 21, DBAssertLimits{
		MaxResultBytes: 1000,
	})
	wantToolError(t, err, "tool_error", DBReasonResultTruncated)
	if !outcome.Truncated || outcome.Verdict != dbAssertNotRun ||
		outcome.FailureReason != DBReasonResultTruncated {
		t.Fatalf("outcome = %+v", outcome)
	}
	var preview dbPreview
	if err := json.Unmarshal([]byte(outcome.ActualPreview), &preview); err != nil {
		t.Fatalf("preview is not JSON: %v (%q)", err, outcome.ActualPreview)
	}
	if preview.RowCount != 1 || !preview.Truncated ||
		preview.ElapsedMS == nil || *preview.ElapsedMS != 2 {
		t.Fatalf("preview = %+v (%q), want rowCount 1, truncated and elapsedMs 2",
			preview, outcome.ActualPreview)
	}
	results := world.assertions.rows[run.ID]
	if len(results) != 1 || results[0].Verdict != dbAssertNotRun ||
		results[0].FailureReason != DBReasonResultTruncated {
		t.Fatalf("recorded result = %+v", results)
	}
}

// TestDBAssertChecksEvidenceCaptureBeforeExecutingSQL pins that a mis-wired
// recorder fails closed before any SQL reaches the target.
func TestDBAssertChecksEvidenceCaptureBeforeExecutingSQL(t *testing.T) {
	recorder, world, query, _ := newDBAssertTest(t)
	recorder.deps.DBCapture = nil
	run := world.seedRunningRun(dbAssertSnapshot(dbAssertExpected))
	recordDBAssertStep(t, recorder, run)

	_, err := recorder.DBAssert(context.Background(), run.ID, 21, DBAssertLimits{})
	wantToolError(t, err, "internal", "evidence capture")
	if query.calls != 0 {
		t.Fatalf("query calls = %d, want 0 when the evidence capture is not configured", query.calls)
	}
}

// TestDBAssertRepeatReturnsRecordedResult pins idempotency: the query runs once
// and the repeated call returns the recorded outcome instead of re-executing.
func TestDBAssertRepeatReturnsRecordedResult(t *testing.T) {
	recorder, world, query, _ := newDBAssertTest(t)
	run := world.seedRunningRun(dbAssertSnapshot(dbAssertExpected))
	recordDBAssertStep(t, recorder, run)

	first, err := recorder.DBAssert(context.Background(), run.ID, 21, DBAssertLimits{})
	if err != nil {
		t.Fatalf("first DBAssert: %v", err)
	}
	second, err := recorder.DBAssert(context.Background(), run.ID, 21, DBAssertLimits{})
	if err != nil {
		t.Fatalf("repeat DBAssert: %v", err)
	}
	if !second.Repeated || second.Verdict != first.Verdict ||
		second.ActualPreview != first.ActualPreview || second.FailureReason != first.FailureReason {
		t.Fatalf("repeat outcome = %+v, first = %+v", second, first)
	}
	// A repeated assertion replays the recorded execution metadata too, so the
	// model sees the same rowCount/elapsed/truncated values as the first call.
	if second.RowCount != first.RowCount || second.ElapsedMS != first.ElapsedMS ||
		second.Truncated != first.Truncated {
		t.Fatalf("repeat stats = rowCount %d elapsed %d truncated %v, want %d/%d/%v",
			second.RowCount, second.ElapsedMS, second.Truncated,
			first.RowCount, first.ElapsedMS, first.Truncated)
	}
	wantDBPreviewElapsed(t, second.ActualPreview, 7)
	if query.calls != 1 {
		t.Fatalf("query calls = %d, want 1 (db assertion must execute once)", query.calls)
	}
	if results := world.assertions.rows[run.ID]; len(results) != 1 {
		t.Fatalf("assertion results = %+v, want one row", results)
	}
}

// TestDBAssertRepeatReturnsRecordedTruncatedStats pins that a repeated
// truncated assertion replays the recorded rowCount/elapsed/truncated metadata
// from the captured db_result evidence, not zero values.
func TestDBAssertRepeatReturnsRecordedTruncatedStats(t *testing.T) {
	recorder, world, query, _ := newDBAssertTest(t)
	query.result = DBQueryResult{
		Columns: []string{"count"}, Rows: [][]string{{"3"}},
		RowCount: 1, Truncated: true, ElapsedMS: 9,
	}
	run := world.seedRunningRun(dbAssertSnapshot(dbAssertExpected))
	recordDBAssertStep(t, recorder, run)

	first, err := recorder.DBAssert(context.Background(), run.ID, 21, DBAssertLimits{})
	wantToolError(t, err, "tool_error", DBReasonResultTruncated)
	second, err := recorder.DBAssert(context.Background(), run.ID, 21, DBAssertLimits{})
	wantToolError(t, err, "tool_error", DBReasonResultTruncated)
	if !second.Repeated || second.Verdict != dbAssertNotRun ||
		second.RowCount != 1 || second.ElapsedMS != 9 || !second.Truncated ||
		second.FailureReason != DBReasonResultTruncated {
		t.Fatalf("repeat outcome = %+v, first = %+v", second, first)
	}
	wantDBPreviewElapsed(t, first.ActualPreview, 9)
	wantDBPreviewElapsed(t, second.ActualPreview, 9)
	if query.calls != 1 {
		t.Fatalf("query calls = %d, want 1", query.calls)
	}
}

// TestDBAssertErrorRepeatIsIdempotent pins that a failed assertion is not
// re-executed: the recorded not_run reason is replayed.
func TestDBAssertErrorRepeatIsIdempotent(t *testing.T) {
	recorder, world, query, _ := newDBAssertTest(t)
	query.queryErr = &DBQueryError{Reason: DBReasonConnectionFailed, Message: "dial tcp: refused"}
	run := world.seedRunningRun(dbAssertSnapshot(dbAssertExpected))
	recordDBAssertStep(t, recorder, run)

	_, firstErr := recorder.DBAssert(context.Background(), run.ID, 21, DBAssertLimits{})
	wantToolError(t, firstErr, "tool_error", DBReasonConnectionFailed)
	_, secondErr := recorder.DBAssert(context.Background(), run.ID, 21, DBAssertLimits{})
	wantToolError(t, secondErr, "tool_error", DBReasonConnectionFailed)
	if query.calls != 1 {
		t.Fatalf("query calls = %d, want 1", query.calls)
	}
	results := world.assertions.rows[run.ID]
	if len(results) != 1 || results[0].Verdict != "not_run" ||
		results[0].FailureReason != DBReasonConnectionFailed {
		t.Fatalf("recorded result = %+v", results)
	}
}

// TestDBAssertRejectsNonDBAndForeignAssertions pins that only snapshot db
// assertions may be executed, and a rejected call executes no SQL.
func TestDBAssertRejectsNonDBAndForeignAssertions(t *testing.T) {
	recorder, world, query, _ := newDBAssertTest(t)

	uiSnapshot := dbAssertSnapshot(dbAssertExpected)
	uiSnapshot.Assertions[0].Kind = "ui"
	uiRun := world.seedRunningRun(uiSnapshot)
	_, err := recorder.DBAssert(context.Background(), uiRun.ID, 21, DBAssertLimits{})
	wantToolError(t, err, "invalid_arguments", "db assertion")

	dbRun := world.seedRunningRun(dbAssertSnapshot(dbAssertExpected))
	_, err = recorder.DBAssert(context.Background(), dbRun.ID, 999, DBAssertLimits{})
	wantToolError(t, err, "invalid_arguments", "not part of")
	if query.calls != 0 {
		t.Fatalf("rejected db_assert calls executed %d queries", query.calls)
	}
	for _, rows := range world.assertions.rows {
		if len(rows) != 0 {
			t.Fatalf("rejected db_assert wrote assertion results: %+v", rows)
		}
	}
}

// TestDBAssertFailurePathsRecordNotRunAndPermitPrescribedFinish pins T8 review
// fix 2 (spec §6.6/§7.11): every db_assert failure records a not_run result with
// the stable reason before the agent finishes, so ValidateFinish accepts the
// finish the prompt prescribes (blocked environment or inconclusive case).
func TestDBAssertFailurePathsRecordNotRunAndPermitPrescribedFinish(t *testing.T) {
	blockedEnvironment := domain.FinishDecision{
		Verdict: domain.VerdictBlocked, FailureClass: failureClassPointer(domain.FailureClassEnvironment),
	}
	inconclusiveCase := domain.FinishDecision{
		Verdict: domain.VerdictInconclusive, FailureClass: failureClassPointer(domain.FailureClassCase),
	}
	tests := []struct {
		name         string
		setup        func(snapshot *domain.CaseSnapshot, query *recDBQuery, limits *DBAssertLimits)
		wantReason   string
		wantFinish   domain.FinishDecision
		wantEvidence int
		wantQueries  int
	}{
		{
			name: "missing target is environment",
			setup: func(_ *domain.CaseSnapshot, query *recDBQuery, _ *DBAssertLimits) {
				query.targetErr = &DBQueryError{Reason: DBReasonTargetMissing, Message: "no enabled target"}
			},
			wantReason: DBReasonTargetMissing, wantFinish: blockedEnvironment,
		},
		{
			name: "connection failure is environment",
			setup: func(_ *domain.CaseSnapshot, query *recDBQuery, _ *DBAssertLimits) {
				query.queryErr = &DBQueryError{Reason: DBReasonConnectionFailed, Message: "dial tcp: refused"}
			},
			wantReason: DBReasonConnectionFailed, wantFinish: blockedEnvironment, wantQueries: 1,
		},
		{
			name: "query failure is case",
			setup: func(_ *domain.CaseSnapshot, query *recDBQuery, _ *DBAssertLimits) {
				query.queryErr = &DBQueryError{Reason: DBReasonQueryFailed, Message: "syntax error"}
			},
			wantReason: DBReasonQueryFailed, wantFinish: inconclusiveCase, wantQueries: 1,
		},
		{
			name: "read-only violation is case",
			setup: func(_ *domain.CaseSnapshot, query *recDBQuery, _ *DBAssertLimits) {
				query.queryErr = &DBQueryError{Reason: DBReasonReadonlyViolation, Message: "write keyword"}
			},
			wantReason: DBReasonReadonlyViolation, wantFinish: inconclusiveCase, wantQueries: 1,
		},
		{
			name: "truncated result is case and keeps evidence",
			setup: func(_ *domain.CaseSnapshot, query *recDBQuery, _ *DBAssertLimits) {
				query.result = DBQueryResult{
					Columns: []string{"count"}, Rows: [][]string{{"3"}},
					RowCount: 1, Truncated: true, ElapsedMS: 9,
				}
			},
			wantReason: DBReasonResultTruncated, wantFinish: inconclusiveCase,
			wantEvidence: 1, wantQueries: 1,
		},
		{
			name: "invalid expected is case and executes nothing",
			setup: func(snapshot *domain.CaseSnapshot, _ *recDBQuery, _ *DBAssertLimits) {
				snapshot.Assertions[0].Expected = `{"rows":[`
			},
			wantReason: DBReasonExpectedInvalid, wantFinish: inconclusiveCase,
		},
		{
			name: "SQL above the configured cap is a read-only violation",
			setup: func(_ *domain.CaseSnapshot, _ *recDBQuery, limits *DBAssertLimits) {
				limits.MaxSQLBytes = 4
			},
			wantReason: DBReasonReadonlyViolation, wantFinish: inconclusiveCase,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder, world, query, _ := newDBAssertTest(t)
			snapshot := dbAssertSnapshot(dbAssertExpected)
			limits := DBAssertLimits{}
			if tt.setup != nil {
				tt.setup(&snapshot, query, &limits)
			}
			run := world.seedRunningRun(snapshot)
			recordDBAssertStep(t, recorder, run)

			_, err := recorder.DBAssert(context.Background(), run.ID, 21, limits)
			wantToolError(t, err, "tool_error", tt.wantReason)
			if query.calls != tt.wantQueries {
				t.Fatalf("query calls = %d, want %d", query.calls, tt.wantQueries)
			}
			results := world.assertions.rows[run.ID]
			if len(results) != 1 {
				t.Fatalf("assertion results = %+v, want one not_run row", results)
			}
			if results[0].Verdict != "not_run" || results[0].FailureReason != tt.wantReason ||
				results[0].Kind != "db" {
				t.Fatalf("recorded result = %+v", results[0])
			}
			evidenceRows, err := world.evidence.ListByCaseRun(context.Background(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(evidenceRows) != tt.wantEvidence {
				t.Fatalf("db evidence rows = %d, want %d", len(evidenceRows), tt.wantEvidence)
			}

			finished, err := recorder.FinishCase(context.Background(), run.ID, FinishInput{
				Decision: tt.wantFinish,
			})
			if err != nil {
				t.Fatalf("FinishCase %s: %v", tt.wantFinish.Verdict, err)
			}
			if *finished.FailureClass != *tt.wantFinish.FailureClass {
				t.Fatalf("finished class = %s, want %s", *finished.FailureClass, *tt.wantFinish.FailureClass)
			}
		})
	}
}

// TestDBAssertMismatchFailsApplication pins spec §6.6.2: a value mismatch is a
// failed assertion result (application failure), not a tool error, and the
// db evidence it captured justifies the failed finish.
func TestDBAssertMismatchFailsApplication(t *testing.T) {
	recorder, world, query, _ := newDBAssertTest(t)
	query.result = DBQueryResult{
		Columns: []string{"count"}, Rows: [][]string{{"4"}}, RowCount: 1, ElapsedMS: 3,
	}
	run := world.seedRunningRun(dbAssertSnapshot(dbAssertExpected))
	recordDBAssertStep(t, recorder, run)

	outcome, err := recorder.DBAssert(context.Background(), run.ID, 21, DBAssertLimits{})
	if err != nil {
		t.Fatalf("DBAssert mismatch returned a tool error: %v", err)
	}
	if outcome.Verdict != "failed" || outcome.FailureReason != "assertion_mismatch" {
		t.Fatalf("outcome = %+v", outcome)
	}
	results := world.assertions.rows[run.ID]
	if len(results) != 1 || results[0].Verdict != "failed" {
		t.Fatalf("recorded result = %+v", results)
	}
	if preview := parseDBPreview(t, results[0].Actual); preview.ElapsedMS == nil || *preview.ElapsedMS != 3 {
		t.Fatalf("failed assertion preview elapsedMs = %v, want 3 (%q)",
			preview.ElapsedMS, results[0].Actual)
	}

	finished, err := recorder.FinishCase(context.Background(), run.ID, FinishInput{
		Decision: domain.FinishDecision{
			Verdict: domain.VerdictFailed, FailureClass: failureClassPointer(domain.FailureClassApplication),
			Reason: "assertion mismatch",
		},
	})
	if err != nil {
		t.Fatalf("FinishCase failed application: %v", err)
	}
	if finished.Status != domain.CaseRunStatusFailed {
		t.Fatalf("status = %s, want failed", finished.Status)
	}
}

// TestDBAssertAuditsExecution pins spec §11.2: every recorded db assertion
// result emits execution.db_assert.executed with ids/verdict/counts only.
func TestDBAssertAuditsExecution(t *testing.T) {
	t.Run("passed", func(t *testing.T) {
		recorder, world, _, _ := newDBAssertTest(t)
		audit := &recAudit{}
		recorder.deps.Audit = audit
		run := world.seedRunningRun(dbAssertSnapshot(dbAssertExpected))
		recordDBAssertStep(t, recorder, run)

		outcome, err := recorder.DBAssert(context.Background(), run.ID, 21, DBAssertLimits{})
		if err != nil {
			t.Fatalf("DBAssert: %v", err)
		}
		events := audit.dbAssertAudits()
		if len(events) != 1 {
			t.Fatalf("db audit events = %+v, want one", events)
		}
		event := events[0]
		if event.AssertionResultID <= 0 || event.CaseRunID != run.ID ||
			event.Verdict != outcome.Verdict || event.RowCount != 1 || event.ElapsedMS != 7 {
			t.Fatalf("db audit event = %+v", event)
		}
	})
	t.Run("failure still audits the not_run result", func(t *testing.T) {
		recorder, world, query, _ := newDBAssertTest(t)
		audit := &recAudit{}
		recorder.deps.Audit = audit
		query.queryErr = &DBQueryError{Reason: DBReasonQueryFailed, Message: "syntax error"}
		run := world.seedRunningRun(dbAssertSnapshot(dbAssertExpected))
		recordDBAssertStep(t, recorder, run)

		if _, err := recorder.DBAssert(context.Background(), run.ID, 21, DBAssertLimits{}); err == nil {
			t.Fatal("DBAssert succeeded despite the query failure")
		}
		events := audit.dbAssertAudits()
		if len(events) != 1 || events[0].Verdict != "not_run" || events[0].CaseRunID != run.ID {
			t.Fatalf("db audit events = %+v", events)
		}
	})
}

// TestDBAssertPreviewBounded pins the tool preview ceiling: the stored actual
// preview is valid JSON and never exceeds 4 KiB, even for a large cell.
func TestDBAssertPreviewBounded(t *testing.T) {
	recorder, world, query, _ := newDBAssertTest(t)
	big := strings.Repeat("x", 16<<10)
	expected, err := json.Marshal([][]string{{big}})
	if err != nil {
		t.Fatal(err)
	}
	query.result = DBQueryResult{
		Columns: []string{"value"}, Rows: [][]string{{big}}, RowCount: 1, ElapsedMS: 2,
	}
	run := world.seedRunningRun(dbAssertSnapshot(string(expected)))
	recordDBAssertStep(t, recorder, run)

	outcome, err := recorder.DBAssert(context.Background(), run.ID, 21, DBAssertLimits{})
	if err != nil {
		t.Fatalf("DBAssert: %v", err)
	}
	if int64(len(outcome.ActualPreview)) > DefaultDBPreviewMaxBytes {
		t.Fatalf("preview = %d bytes, want at most %d", len(outcome.ActualPreview), DefaultDBPreviewMaxBytes)
	}
	if !json.Valid([]byte(outcome.ActualPreview)) {
		t.Fatalf("preview is not valid JSON: %q", outcome.ActualPreview)
	}
	if preview := parseDBPreview(t, outcome.ActualPreview); preview.ElapsedMS == nil || *preview.ElapsedMS != 2 {
		t.Fatalf("bounded preview elapsedMs = %v, want 2 (%q)",
			preview.ElapsedMS, outcome.ActualPreview)
	}
}

// TestDBResultPreviewOmittedCarriesElapsed pins the fallback preview: when the
// metadata alone exceeds the cap, the omitted preview is still valid JSON and
// carries the elapsed time so the results tab can render 耗时.
func TestDBResultPreviewOmittedCarriesElapsed(t *testing.T) {
	columns := make([]string, 8)
	for i := range columns {
		columns[i] = strings.Repeat("c", 1024)
	}
	preview := dbResultPreview(DBQueryResult{
		Columns: columns, RowCount: 5, ElapsedMS: 11,
	}, DefaultDBPreviewMaxBytes)
	if int64(len(preview)) > DefaultDBPreviewMaxBytes {
		t.Fatalf("omitted preview = %d bytes, want at most %d", len(preview), DefaultDBPreviewMaxBytes)
	}
	decoded := parseDBPreview(t, preview)
	if decoded.RowCount != 5 || !decoded.Truncated ||
		decoded.ElapsedMS == nil || *decoded.ElapsedMS != 11 {
		t.Fatalf("omitted preview = %+v (%q), want rowCount 5, truncated and elapsedMs 11",
			decoded, preview)
	}
}

// TestDBExpectedComparison pins the §6.6.1 normalization: null, booleans,
// numbers (trailing zeros removed), RFC3339 strings, ordered and unordered
// semantics.
func TestDBExpectedComparison(t *testing.T) {
	tests := []struct {
		name     string
		expected string
		columns  []string
		rows     [][]string
		want     bool
	}{
		{name: "naked array is ordered", expected: `[["a"],["b"]]`, columns: []string{"v"}, rows: [][]string{{"a"}, {"b"}}, want: true},
		{name: "ordered mismatch", expected: `[["a"],["b"]]`, columns: []string{"v"}, rows: [][]string{{"b"}, {"a"}}, want: false},
		{name: "unordered match", expected: `{"rows":[["a"],["b"]],"ordered":false}`, columns: []string{"v"}, rows: [][]string{{"b"}, {"a"}}, want: true},
		{name: "unordered multiset", expected: `{"rows":[["a"],["a"]],"ordered":false}`, columns: []string{"v"}, rows: [][]string{{"a"}}, want: false},
		{name: "null normalized", expected: `[[null]]`, columns: []string{"v"}, rows: [][]string{{"null"}}, want: true},
		{name: "boolean normalized", expected: `[[true],[false]]`, columns: []string{"v"}, rows: [][]string{{"true"}, {"false"}}, want: true},
		{name: "number trailing zeros normalized", expected: `[[1.50]]`, columns: []string{"v"}, rows: [][]string{{"1.5"}}, want: true},
		{name: "high precision decimal preserved", expected: `[[12345678901234567890.1200]]`, columns: []string{"v"}, rows: [][]string{{"12345678901234567890.12"}}, want: true},
		{name: "zero normalized", expected: `[[-0.0]]`, columns: []string{"v"}, rows: [][]string{{"0"}}, want: true},
		{name: "integer normalized", expected: `[[3]]`, columns: []string{"v"}, rows: [][]string{{"3"}}, want: true},
		{name: "timestamp string", expected: `[["2026-10-28T12:00:00Z"]]`, columns: []string{"v"}, rows: [][]string{{"2026-10-28T12:00:00Z"}}, want: true},
		{name: "column count mismatch", expected: `[["a","b"]]`, columns: []string{"v"}, rows: [][]string{{"a"}}, want: false},
		{name: "row count mismatch", expected: `[["a"],["b"]]`, columns: []string{"v"}, rows: [][]string{{"a"}}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expected, err := parseDBExpected(tt.expected)
			if err != nil {
				t.Fatalf("parseDBExpected(%q): %v", tt.expected, err)
			}
			result := DBQueryResult{Columns: tt.columns, Rows: tt.rows, RowCount: len(tt.rows)}
			if got := dbRowsMatch(expected, result); got != tt.want {
				t.Fatalf("dbRowsMatch = %v, want %v", got, tt.want)
			}
		})
	}

	for _, invalid := range []string{``, `{`, `{"rows":[["a"]]`, `{"rows":"x"}`, `{"rows":[[{"a":1}]]}`, `[1]`, `{}`} {
		if _, err := parseDBExpected(invalid); err == nil {
			t.Fatalf("parseDBExpected accepted invalid expected %q", invalid)
		}
	}
}
