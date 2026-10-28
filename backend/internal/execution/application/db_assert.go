package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"

	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// db_assert defaults of spec §16.1. The composition root wires the configured
// EZTEST_DB_RESULT_MAX_BYTES / EZTEST_DB_SQL_MAX_BYTES values through
// DBAssertLimits; the constants below apply when a limit is not configured.
const (
	// DefaultDBResultMaxBytes caps one db_result evidence JSON document.
	DefaultDBResultMaxBytes int64 = 1 << 20
	// DefaultDBSQLMaxBytes caps the snapshot db assertion SQL text.
	DefaultDBSQLMaxBytes int64 = 16 << 10
	// DefaultDBPreviewMaxBytes caps the actualPreview returned to the model
	// (spec §6.6 step 2: ≤4KiB).
	DefaultDBPreviewMaxBytes int64 = 4 << 10

	// dbEvidenceKind is the P7 evidence kind of one db assertion result.
	dbEvidenceKind = "db_result"
	// dbEvidenceMime is the media type of the db_result JSON artifact.
	dbEvidenceMime = "application/json"
	// dbAssertionMismatchReason is the failure_reason of a value mismatch. It
	// matches the existing application-failure tool code of spec §7.11.
	dbAssertionMismatchReason = "assertion_mismatch"
	// dbAssertNotRun is the assertion-level verdict of an unexecuted/failed
	// assertion.
	dbAssertNotRun = "not_run"
)

// DBAssertLimits are the db_assert bounds. Zero fields fall back to the spec
// §16.1 defaults.
type DBAssertLimits struct {
	// MaxResultBytes caps the db_result evidence JSON document.
	MaxResultBytes int64
	// MaxSQLBytes caps the snapshot assertion SQL text.
	MaxSQLBytes int64
	// MaxPreviewBytes caps the actual preview stored on the assertion result and
	// returned to the model.
	MaxPreviewBytes int64
}

// withDefaults fills the non-positive limits with the spec defaults.
func (l DBAssertLimits) withDefaults() DBAssertLimits {
	if l.MaxResultBytes <= 0 {
		l.MaxResultBytes = DefaultDBResultMaxBytes
	}
	if l.MaxSQLBytes <= 0 {
		l.MaxSQLBytes = DefaultDBSQLMaxBytes
	}
	if l.MaxPreviewBytes <= 0 {
		l.MaxPreviewBytes = DefaultDBPreviewMaxBytes
	}
	return l
}

// DBAssertOutcome is the recorded outcome of one db assertion. On a repeated
// call Repeated is true and the values are the recorded row's values, so the
// assertion is never executed twice (spec §9.5 idempotency).
type DBAssertOutcome struct {
	AssertionID   int64
	Verdict       string
	RowCount      int
	Truncated     bool
	ElapsedMS     int64
	ActualPreview string
	FailureReason string
	Repeated      bool
}

// DBAssert executes one db assertion of a case run (spec §6.6/§9.5). The
// assertion must belong to the pinned snapshot and have kind=db; the SQL,
// expected value and target are read server-side, never from the agent. The
// query runs once through DBQueryPort; a successful execution captures a
// db_result evidence artifact and records the assertion_result, while every
// classified failure records a not_run result with the stable reason before
// returning the model-facing tool error. A value mismatch is not an error: it
// records verdict=failed so the agent can finish failed(application).
func (r *CaseRecorder) DBAssert(ctx context.Context, caseRunID, assertionID int64, limits DBAssertLimits) (DBAssertOutcome, error) {
	if err := r.ready(); err != nil {
		return DBAssertOutcome{}, err
	}
	if caseRunID <= 0 || assertionID <= 0 {
		return DBAssertOutcome{}, recorderToolError(toolCodeInvalidArguments,
			"case run and assertion ids must be positive")
	}
	limits = limits.withDefaults()

	run, err := r.loadCaseRun(ctx, caseRunID)
	if err != nil {
		return DBAssertOutcome{}, err
	}
	if err := requireRecordableRun(run); err != nil {
		return DBAssertOutcome{}, err
	}
	assertion, ok := findSnapshotAssertion(run.Snapshot, assertionID)
	if !ok {
		return DBAssertOutcome{}, recorderToolError(toolCodeInvalidArguments,
			"assertion %d is not part of case run %d snapshot", assertionID, caseRunID)
	}
	if !strings.EqualFold(strings.TrimSpace(assertion.Kind), "db") {
		return DBAssertOutcome{}, recorderToolError(toolCodeInvalidArguments,
			"assertion %d is kind %q; the execution.db_assert tool only executes db assertions",
			assertionID, assertion.Kind)
	}

	// Idempotency: the (case_run, assertion) pair may be recorded once. A
	// repeated call returns the recorded result instead of executing again.
	existing, err := r.deps.Assertions.ListByCaseRun(ctx, caseRunID)
	if err != nil {
		return DBAssertOutcome{}, recorderToolError(toolCodeInternal,
			"read recorded assertions: %v", err)
	}
	for _, recorded := range existing {
		if recorded.AssertionID == assertionID {
			return r.repeatDBAssertOutcome(ctx, caseRunID, recorded)
		}
	}

	if r.deps.Versions == nil {
		return DBAssertOutcome{}, recorderToolError(toolCodeInternal,
			"the version port is not configured")
	}
	version, err := r.deps.Versions.GetVersion(ctx, run.VersionID)
	if err != nil {
		return DBAssertOutcome{}, recorderToolError(toolCodeInternal,
			"read version %d: %v", run.VersionID, err)
	}
	if version.EnvironmentID == nil {
		return r.recordDBFailure(ctx, run, assertion, "", DBReasonTargetMissing,
			"the version is not bound to an environment")
	}
	if r.deps.DBQuery == nil {
		return DBAssertOutcome{}, recorderToolError(toolCodeInternal,
			"the read-only db query port is not configured")
	}
	target, err := r.deps.DBQuery.Target(ctx, *version.EnvironmentID)
	if err != nil {
		reason, ok := dbQueryReason(err)
		if !ok {
			return DBAssertOutcome{}, recorderToolError(toolCodeInternal,
				"read the environment db target: %v", err)
		}
		return r.recordDBFailure(ctx, run, assertion, "", reason, dbQueryMessage(err))
	}

	sql := strings.TrimSpace(assertion.Expression)
	if int64(len(sql)) > limits.MaxSQLBytes {
		return r.recordDBFailure(ctx, run, assertion, target.Name, DBReasonReadonlyViolation,
			"the SQL statement exceeds the configured byte limit")
	}
	expected, err := parseDBExpected(assertion.Expected)
	if err != nil {
		return r.recordDBFailure(ctx, run, assertion, target.Name, DBReasonExpectedInvalid,
			"the db assertion expected value is not valid rows JSON")
	}
	// A db assertion without evidence capture is mis-wired: fail closed before
	// executing SQL so no target is touched and no result is silently dropped.
	if r.deps.DBCapture == nil {
		return DBAssertOutcome{}, recorderToolError(toolCodeInternal,
			"the db_result evidence capture is not configured")
	}

	result, err := r.deps.DBQuery.Query(ctx, *version.EnvironmentID, sql, DBQueryLimits{
		MaxRows:   target.MaxRows,
		TimeoutMS: target.StatementTimeoutMS,
		MaxBytes:  int(limits.MaxResultBytes),
	})
	if err != nil {
		reason, ok := dbQueryReason(err)
		if !ok {
			return DBAssertOutcome{}, recorderToolError(toolCodeInternal,
				"execute the read-only db query: %v", err)
		}
		return r.recordDBFailure(ctx, run, assertion, target.Name, reason, dbQueryMessage(err))
	}

	document, payloadTruncated, err := dbEvidenceDocument(assertionID, sql, result, limits.MaxResultBytes)
	if err != nil {
		return DBAssertOutcome{}, recorderToolError(toolCodeInternal,
			"encode the db_result evidence: %v", err)
	}
	truncated := result.Truncated || payloadTruncated
	// The preview must describe the same result the verdict and evidence use:
	// a payload-truncated document is a truncated result too.
	result.Truncated = truncated
	preview := dbResultPreview(result, limits.MaxPreviewBytes)
	verdict := "failed"
	failureReason := dbAssertionMismatchReason
	if dbRowsMatch(expected, result) {
		verdict = "passed"
		failureReason = ""
	}
	if truncated {
		// A truncated result cannot be compared reliably: the assertion does not
		// run (spec §6.6.1) even though the evidence is still captured.
		verdict = dbAssertNotRun
		failureReason = DBReasonResultTruncated
	}
	evidence, err := r.deps.DBCapture.Capture(ctx, CaptureInput{
		BatchID:   run.BatchID,
		CaseRunID: run.ID,
		Kind:      dbEvidenceKind,
		Mime:      dbEvidenceMime,
		Data:      document,
		Metadata: map[string]any{
			"assertion_id": strconv.FormatInt(assertionID, 10),
			"target":       target.Name,
			"verdict":      verdict,
			"row_count":    result.RowCount,
			"truncated":    truncated,
			"elapsed_ms":   result.ElapsedMS,
		},
	})
	if err != nil {
		// The query succeeded but its evidence could not be committed. Record a
		// not_run result so the finish gate still accepts the prescribed
		// inconclusive(case) finish; the underlying failure is only logged.
		slog.WarnContext(ctx, "execution: db_result evidence capture failed",
			"case_run_id", run.ID, "assertion_id", assertionID, "error", err)
		return r.recordDBFailure(ctx, run, assertion, target.Name, DBReasonQueryFailed,
			"the db_result evidence could not be captured")
	}

	stored, err := r.persistDBAssertion(ctx, run, assertion, target.Name, verdict, preview, failureReason)
	if err != nil {
		return DBAssertOutcome{}, err
	}
	r.createRefs(ctx, stored.ID, []int64{evidence.ID}, "assertion_result")
	r.auditDBAssert(ctx, stored.ID, run.ID, verdict, result.RowCount, result.ElapsedMS)

	outcome := DBAssertOutcome{
		AssertionID:   assertionID,
		Verdict:       verdict,
		RowCount:      result.RowCount,
		Truncated:     truncated,
		ElapsedMS:     result.ElapsedMS,
		ActualPreview: preview,
		FailureReason: failureReason,
	}
	if truncated {
		return outcome, dbAssertToolError(DBReasonResultTruncated,
			"the read-only query result exceeded the configured limit", false)
	}
	return outcome, nil
}

// repeatDBAssertOutcome projects a recorded assertion result without executing
// the assertion again. A recorded not_run failure replays the same tool error so
// the agent keeps following the prescribed finish. The recorded db_result
// evidence metadata supplies the rowCount/elapsed/truncated values the first
// call returned.
func (r *CaseRecorder) repeatDBAssertOutcome(ctx context.Context, caseRunID int64, recorded domain.AssertionResult) (DBAssertOutcome, error) {
	outcome := DBAssertOutcome{
		AssertionID:   recorded.AssertionID,
		Verdict:       recorded.Verdict,
		ActualPreview: recorded.Actual,
		FailureReason: recorded.FailureReason,
		Repeated:      true,
	}
	outcome.RowCount, outcome.ElapsedMS, outcome.Truncated = r.recordedDBResultStats(ctx, caseRunID, recorded.AssertionID)
	if strings.EqualFold(strings.TrimSpace(recorded.Verdict), dbAssertNotRun) {
		reason := recorded.FailureReason
		if reason == "" {
			reason = DBReasonQueryFailed
		}
		return outcome, dbAssertToolError(reason,
			"the db assertion already failed and was not executed again",
			dbReasonIsEnvironment(reason))
	}
	return outcome, nil
}

// recordedDBResultStats reads the rowCount/elapsed/truncated metadata of the
// recorded db_result evidence of one assertion. It is best effort: a missing or
// unreadable evidence row leaves the zero values (the recorded verdict and
// preview are still replayed).
func (r *CaseRecorder) recordedDBResultStats(ctx context.Context, caseRunID, assertionID int64) (int, int64, bool) {
	if r.deps.Evidence == nil {
		return 0, 0, false
	}
	rows, err := r.deps.Evidence.ListByCaseRun(ctx, caseRunID)
	if err != nil {
		slog.WarnContext(ctx, "execution: read db_result evidence for a repeated assertion failed",
			"case_run_id", caseRunID, "assertion_id", assertionID, "error", err)
		return 0, 0, false
	}
	want := strconv.FormatInt(assertionID, 10)
	for _, evidence := range rows {
		if evidence.Kind != dbEvidenceKind || evidence.Metadata == nil {
			continue
		}
		if metadataString(evidence.Metadata["assertion_id"]) != want {
			continue
		}
		return metadataInt(evidence.Metadata["row_count"]),
			metadataInt64(evidence.Metadata["elapsed_ms"]),
			metadataBool(evidence.Metadata["truncated"])
	}
	return 0, 0, false
}

// metadataString projects one evidence metadata value onto its string form. The
// JSONB round-trip decodes integers as float64, while in-memory repositories may
// keep the original Go number.
func metadataString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	default:
		return fmt.Sprint(value)
	}
}

// metadataInt projects one evidence metadata value onto an int.
func metadataInt(value any) int {
	return int(metadataInt64(value))
}

// metadataInt64 projects one evidence metadata value onto an int64.
func metadataInt64(value any) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int64:
		return typed
	case int32:
		return int64(typed)
	case float64:
		return int64(typed)
	case float32:
		return int64(typed)
	case json.Number:
		value, err := typed.Int64()
		if err != nil {
			parsed, floatErr := typed.Float64()
			if floatErr != nil {
				return 0
			}
			return int64(parsed)
		}
		return value
	case string:
		parsed, err := strconv.ParseInt(typed, 10, 64)
		if err != nil {
			return 0
		}
		return parsed
	default:
		return 0
	}
}

// metadataBool projects one evidence metadata value onto a bool.
func metadataBool(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(typed, "true")
	default:
		return false
	}
}

// recordDBFailure records the not_run assertion result of one classified
// db_assert failure and returns the model-facing tool error. The write must
// succeed: without it the finish gate would reject the prescribed finish.
func (r *CaseRecorder) recordDBFailure(ctx context.Context, run domain.CaseRun, assertion domain.CaseSnapshotAssertion, targetName, reason, message string) (DBAssertOutcome, error) {
	stored, err := r.persistDBAssertion(ctx, run, assertion, targetName, dbAssertNotRun, "", reason)
	if err != nil {
		return DBAssertOutcome{}, err
	}
	r.auditDBAssert(ctx, stored.ID, run.ID, dbAssertNotRun, 0, 0)
	return DBAssertOutcome{
		AssertionID:   assertion.ID,
		Verdict:       dbAssertNotRun,
		FailureReason: reason,
	}, dbAssertToolError(reason, message, dbReasonIsEnvironment(reason))
}

// auditDBAssert records execution.db_assert.executed (spec §11.2) when the
// wired case-finish auditor also implements DBAssertAuditor. Auditing is
// best-effort: the assertion result is already durable.
func (r *CaseRecorder) auditDBAssert(ctx context.Context, assertionResultID, caseRunID int64, verdict string, rowCount int, elapsedMS int64) {
	auditor, ok := r.deps.Audit.(DBAssertAuditor)
	if !ok || auditor == nil {
		return
	}
	if err := auditor.DBAssertExecuted(ctx, DBAssertAuditEvent{
		AssertionResultID: assertionResultID,
		CaseRunID:         caseRunID,
		Verdict:           verdict,
		RowCount:          rowCount,
		ElapsedMS:         elapsedMS,
	}); err != nil {
		slog.WarnContext(ctx, "execution: audit db assertion failed",
			"assertion_result_id", assertionResultID, "case_run_id", caseRunID, "error", err)
	}
}

// persistDBAssertion writes the server-owned assertion_result of one db
// assertion. It never trusts agent-supplied expected/actual values.
func (r *CaseRecorder) persistDBAssertion(ctx context.Context, run domain.CaseRun, assertion domain.CaseSnapshotAssertion, targetName, verdict, actual, failureReason string) (domain.AssertionResult, error) {
	id, err := nextID(r.deps.IDs)
	if err != nil {
		return domain.AssertionResult{}, recorderToolError(toolCodeInternal,
			"mint db assertion result id: %v", err)
	}
	now := r.now()
	stored, err := r.deps.Assertions.Upsert(ctx, domain.AssertionResult{
		ID:            id,
		BatchID:       run.BatchID,
		CaseRunID:     run.ID,
		StepResultID:  r.stepResultIDForSeq(ctx, run.ID, assertion.StepSeq),
		AssertionID:   assertion.ID,
		Kind:          assertion.Kind,
		Target:        targetName,
		Expected:      assertion.Expected,
		Actual:        actual,
		Verdict:       verdict,
		FailureReason: failureReason,
		CreatedAt:     now,
		UpdatedAt:     now,
	})
	if err != nil {
		return domain.AssertionResult{}, recorderToolError(toolCodeInternal,
			"persist assertion %d: %v", assertion.ID, err)
	}
	return stored, nil
}

// dbQueryReason extracts the stable db reason of a classified port error.
func dbQueryReason(err error) (string, bool) {
	var queryErr *DBQueryError
	if errors.As(err, &queryErr) {
		reason := strings.TrimSpace(queryErr.Reason)
		if reason != "" {
			return reason, true
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return DBReasonConnectionFailed, true
	}
	return "", false
}

// dbQueryMessage returns a model-safe failure message. It never includes an
// arbitrary error value (which could carry a DSN or credentials).
func dbQueryMessage(err error) string {
	var queryErr *DBQueryError
	if errors.As(err, &queryErr) && strings.TrimSpace(queryErr.Message) != "" {
		return queryErr.Message
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "the database query timed out"
	}
	return "the read-only database query failed"
}

// dbReasonIsEnvironment reports whether a db failure blocks as environment.
func dbReasonIsEnvironment(reason string) bool {
	return reason == DBReasonTargetMissing || reason == DBReasonConnectionFailed
}

// dbAssertToolError builds the model-facing db assertion failure. The hint tells
// the agent which finish the server accepts (spec §6.6 steps 4-5).
func dbAssertToolError(reason, message string, environment bool) error {
	hint := "finish inconclusive(case)"
	if environment {
		hint = "finish blocked(environment)"
	}
	return &agentdomain.ToolError{Code: "tool_error",
		Message: reason + ": " + message + "; " + hint}
}

// dbExpected is the parsed expected value of a db assertion (spec §6.6.1).
type dbExpected struct {
	Rows    [][]string
	Ordered bool
}

// parseDBExpected parses the expected value: {"rows":[[...]],"ordered":true}
// or a naked two-dimensional array treated as ordered.
func parseDBExpected(raw string) (dbExpected, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return dbExpected{}, errors.New("db assertion expected value is empty")
	}
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return dbExpected{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return dbExpected{}, errors.New("db assertion expected has trailing data")
	}
	return dbExpectedFromValue(value)
}

// dbExpectedFromValue accepts the two expected shapes of spec §6.6.1.
func dbExpectedFromValue(value any) (dbExpected, error) {
	switch typed := value.(type) {
	case []any:
		rows, err := dbExpectedRows(typed)
		if err != nil {
			return dbExpected{}, err
		}
		return dbExpected{Rows: rows, Ordered: true}, nil
	case map[string]any:
		for key := range typed {
			if key != "rows" && key != "ordered" {
				return dbExpected{}, fmt.Errorf("unexpected expected field %q", key)
			}
		}
		rawRows, ok := typed["rows"]
		if !ok {
			return dbExpected{}, errors.New("expected object requires rows")
		}
		rowsValue, ok := rawRows.([]any)
		if !ok {
			return dbExpected{}, errors.New("expected rows must be an array")
		}
		rows, err := dbExpectedRows(rowsValue)
		if err != nil {
			return dbExpected{}, err
		}
		ordered := true
		if rawOrdered, present := typed["ordered"]; present {
			value, ok := rawOrdered.(bool)
			if !ok {
				return dbExpected{}, errors.New("expected ordered must be a boolean")
			}
			ordered = value
		}
		return dbExpected{Rows: rows, Ordered: ordered}, nil
	default:
		return dbExpected{}, errors.New("expected must be rows JSON")
	}
}

// dbExpectedRows normalizes every expected cell to the string form the resource
// querier produces.
func dbExpectedRows(values []any) ([][]string, error) {
	rows := make([][]string, 0, len(values))
	for _, rawRow := range values {
		cells, ok := rawRow.([]any)
		if !ok {
			return nil, errors.New("expected rows must be arrays")
		}
		row := make([]string, 0, len(cells))
		for _, rawCell := range cells {
			cell, err := normalizeDBExpectedValue(rawCell)
			if err != nil {
				return nil, err
			}
			row = append(row, cell)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// normalizeDBExpectedValue maps one JSON expected cell onto the querier's
// normalization: NULL→"null", booleans→"true"/"false", numbers→plain decimal
// without trailing zeros, strings as-is. Nested values are invalid.
func normalizeDBExpectedValue(value any) (string, error) {
	switch typed := value.(type) {
	case nil:
		return "null", nil
	case string:
		return typed, nil
	case bool:
		if typed {
			return "true", nil
		}
		return "false", nil
	case json.Number:
		text := typed.String()
		if !strings.ContainsAny(text, "eE") {
			// Plain decimals preserve precision: strip trailing fractional
			// zeros the same way the resource querier normalizes numeric text.
			if !strings.Contains(text, ".") {
				return text, nil
			}
			trimmed := strings.TrimRight(text, "0")
			trimmed = strings.TrimSuffix(trimmed, ".")
			switch trimmed {
			case "", "-", "+", "-0", "+0":
				return "0", nil
			default:
				return trimmed, nil
			}
		}
		parsed, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return "", fmt.Errorf("invalid expected number %q", text)
		}
		return strconv.FormatFloat(parsed, 'f', -1, 64), nil
	default:
		return "", errors.New("expected cells must be scalars")
	}
}

// dbRowsMatch reports whether the normalized result matches the expected rows
// (spec §6.6.1): column counts, row counts and the ordered/unordered semantics.
func dbRowsMatch(expected dbExpected, result DBQueryResult) bool {
	if len(expected.Rows) != len(result.Rows) {
		return false
	}
	for _, row := range expected.Rows {
		if len(row) != len(result.Columns) {
			return false
		}
	}
	if expected.Ordered {
		for i, row := range expected.Rows {
			if !dbRowsEqual(row, result.Rows[i]) {
				return false
			}
		}
		return true
	}
	counts := make(map[string]int, len(result.Rows))
	for _, row := range result.Rows {
		counts[dbRowKey(row)]++
	}
	for _, row := range expected.Rows {
		key := dbRowKey(row)
		if counts[key] == 0 {
			return false
		}
		counts[key]--
	}
	return true
}

// dbRowsEqual compares two cells slices.
func dbRowsEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// dbRowKey is the multiset key of one row.
func dbRowKey(row []string) string {
	encoded, err := json.Marshal(row)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// dbResultPreview builds the ≤limit actual preview of one result. It starts
// with the result metadata and appends whole rows while the JSON budget allows,
// so the preview is always valid JSON and never carries a partial cell. The
// camelCase elapsedMs matches the results-tab parser (db-result.ts), so a real
// assertion row can render its elapsed time.
func dbResultPreview(result DBQueryResult, limit int64) string {
	if limit <= 0 {
		limit = DefaultDBPreviewMaxBytes
	}
	type preview struct {
		Columns   []string   `json:"columns"`
		Rows      [][]string `json:"rows"`
		RowCount  int        `json:"rowCount"`
		Truncated bool       `json:"truncated"`
		ElapsedMS int64      `json:"elapsedMs"`
	}
	value := preview{
		Columns:   append([]string(nil), result.Columns...),
		RowCount:  result.RowCount,
		Truncated: result.Truncated,
		ElapsedMS: result.ElapsedMS,
	}
	encoded, err := json.Marshal(value)
	if err != nil || int64(len(encoded)) > limit {
		return dbOmittedPreview(result)
	}
	budget := limit - int64(len(encoded))
	for _, row := range result.Rows {
		cell, err := json.Marshal(row)
		if err != nil {
			break
		}
		// The row is preceded by a comma unless it is the first one, plus the
		// closing bracket accounting of the initial empty rows array.
		cost := int64(len(cell)) + 1
		if len(value.Rows) == 0 {
			cost = int64(len(cell)) - 2 // drop the empty "[]" for the first row
		}
		if budget-cost < 0 {
			value.Truncated = true
			break
		}
		value.Rows = append(value.Rows, row)
		budget -= cost
	}
	encoded, err = json.Marshal(value)
	if err != nil || int64(len(encoded)) > limit {
		return dbOmittedPreview(result)
	}
	return string(encoded)
}

// dbOmittedPreview is the fallback preview when even the column names do not fit
// the budget. It keeps the elapsed time so the results tab can still render it.
func dbOmittedPreview(result DBQueryResult) string {
	return fmt.Sprintf(`{"columns":[],"rows":[],"rowCount":%d,"truncated":true,"elapsedMs":%d}`,
		result.RowCount, result.ElapsedMS)
}

// dbEvidenceDocument renders the db_result evidence artifact of spec §6.6:
// sql/columns/rows/truncated/elapsed_ms, no credentials. It reports whether the
// document had to drop rows to fit maxBytes (which makes the result truncated).
func dbEvidenceDocument(assertionID int64, sql string, result DBQueryResult, maxBytes int64) ([]byte, bool, error) {
	type document struct {
		AssertionID string     `json:"assertion_id"`
		SQL         string     `json:"sql"`
		Columns     []string   `json:"columns"`
		Rows        [][]string `json:"rows"`
		RowCount    int        `json:"row_count"`
		Truncated   bool       `json:"truncated"`
		ElapsedMS   int64      `json:"elapsed_ms"`
	}
	if maxBytes <= 0 {
		maxBytes = DefaultDBResultMaxBytes
	}
	value := document{
		AssertionID: strconv.FormatInt(assertionID, 10),
		SQL:         sql,
		Columns:     append([]string(nil), result.Columns...),
		Rows:        append([][]string(nil), result.Rows...),
		RowCount:    result.RowCount,
		Truncated:   result.Truncated,
		ElapsedMS:   result.ElapsedMS,
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, false, err
	}
	payloadTruncated := false
	for int64(len(encoded)) > maxBytes && len(value.Rows) > 0 {
		value.Rows = value.Rows[:len(value.Rows)/2]
		value.Truncated = true
		payloadTruncated = true
		if encoded, err = json.Marshal(value); err != nil {
			return nil, false, err
		}
	}
	if int64(len(encoded)) > maxBytes {
		// Last resort: keep the SQL and counts, drop the column names.
		value.Columns = nil
		value.Truncated = true
		payloadTruncated = true
		if encoded, err = json.Marshal(value); err != nil {
			return nil, false, err
		}
	}
	if int64(len(encoded)) > maxBytes {
		return nil, false, fmt.Errorf("db result evidence is %d bytes, above the %d byte limit", len(encoded), maxBytes)
	}
	return encoded, payloadTruncated, nil
}
