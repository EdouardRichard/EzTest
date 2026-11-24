package executionapp_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// v0.2.4-C T12 (P8 lift, GD-01): the http.request / db.query / account.credentials
// restrictions are LIFTED. These tests pin the NEW contract (expectations come
// from GD-01/P8, not from observed output):
//   - http.request reaches ANY host (absolute URL), not only the SUT host;
//   - a large response body spills the FULL body to the content ledger with an
//     explicit truncated flag and a content_ref that addresses the FULL content
//     (readable back through tool_result.read / the content store);
//   - large headers are size-bounded EXPLICITLY (Ruling T5-C), never a silent
//     scrub and never a 413 whole-call failure;
//   - db.query runs general read-write SQL and returns more than 200 rows;
//   - account.credentials results are evidence-eligible (no server-side barrier).

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// TestHTTPRequestAbsoluteURLCrossHostSucceeds: an absolute URL on a host that is
// NOT the SUT base URL must succeed (the old resolveSUTURL host gate rejected it).
func TestHTTPRequestAbsoluteURLCrossHostSucceeds(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	fixture.http.response = executionapp.HTTPResponseData{Status: 200, Body: []byte(`{"ok":true}`)}
	result, err := fixture.runner.HTTP(context.Background(), executionScope(), json.RawMessage(`{
		"sut_id":"7","path":"http://other-host.example.com:9999/api/orders","method":"GET"
	}`))
	if err != nil {
		t.Fatalf("cross-host HTTP request rejected: %v", err)
	}
	if fixture.http.lastReq.URL != "http://other-host.example.com:9999/api/orders" {
		t.Fatalf("request URL = %q, want the absolute cross-host URL verbatim", fixture.http.lastReq.URL)
	}
	payload := result.(map[string]any)
	if payload["status"] != 200 {
		t.Fatalf("payload = %+v", payload)
	}
}

// TestHTTPRequestRelativePathStillPrependsBaseURL: the relative-path convenience
// is retained (lifting the host gate does not remove base_url prepending).
func TestHTTPRequestRelativePathStillPrependsBaseURL(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	if _, err := fixture.runner.HTTP(context.Background(), executionScope(), json.RawMessage(`{
		"sut_id":"7","path":"/api/orders"
	}`)); err != nil {
		t.Fatalf("relative-path HTTP: %v", err)
	}
	if fixture.http.lastReq.URL != "http://127.0.0.1:13335/api/orders" {
		t.Fatalf("relative path URL = %q, want base_url prepended", fixture.http.lastReq.URL)
	}
}

// TestHTTPRequestLargeBodySpillsFullContentReadable: a 2MiB response spills the
// FULL body to the content ledger; content_ref addresses the FULL raw body (its
// sha256), the truncated flag is explicit, and the evidence envelope stays
// bounded (no 64KB silent loss, no 413).
func TestHTTPRequestLargeBodySpillsFullContentReadable(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	body := append([]byte("P8-FULL-BEGIN"), bytes.Repeat([]byte("x"), 2<<20)...)
	fixture.http.response = executionapp.HTTPResponseData{Status: 200, Body: body}
	result, err := fixture.runner.HTTP(context.Background(), executionScope(), json.RawMessage(`{
		"sut_id":"7","path":"/p8/large","method":"GET"
	}`))
	if err != nil {
		t.Fatalf("large-body HTTP: %v", err)
	}
	payload := result.(map[string]any)
	if payload["truncated"] != true {
		t.Fatalf("truncated = %v, want explicit true", payload["truncated"])
	}
	ref, _ := payload["content_ref"].(string)
	if ref != sha256Hex(body) {
		t.Fatalf("content_ref = %q, want the sha256 of the FULL body %q", ref, sha256Hex(body))
	}
	// The FULL raw body must have been written to the content ledger (spill), so
	// tool_result.read(content_ref) reads it back complete.
	var sawFullBody bool
	for _, put := range fixture.content.puts {
		if bytes.Equal(put, body) {
			sawFullBody = true
		}
	}
	if !sawFullBody {
		t.Fatalf("the full %d-byte body was not spilled to the content ledger", len(body))
	}
	// The evidence envelope is bounded and explicitly flags the body truncation,
	// carrying the full-body sha for traceability.
	rows, _ := fixture.evidence.ListByCaseRun(context.Background(), 3001, 0)
	if len(rows) != 1 || rows[0].Kind != execution.EvidenceHTTP {
		t.Fatalf("evidence = %+v", rows)
	}
	var envelopePut []byte
	for _, put := range fixture.content.puts {
		if bytes.Contains(put, []byte(`"request"`)) && bytes.Contains(put, []byte(`"response"`)) {
			envelopePut = put
		}
	}
	if envelopePut == nil {
		t.Fatalf("no http evidence envelope found in ledger puts")
	}
	var envelope map[string]any
	if err := json.Unmarshal(envelopePut, &envelope); err != nil {
		t.Fatalf("evidence envelope is not valid JSON: %v", err)
	}
	response, _ := envelope["response"].(map[string]any)
	if response["body_truncated"] != true {
		t.Fatalf("evidence response.body_truncated = %v, want true", response["body_truncated"])
	}
	if response["body_sha256"] != sha256Hex(body) {
		t.Fatalf("evidence response.body_sha256 = %v, want the full-body sha", response["body_sha256"])
	}
	if len(envelopePut) >= len(body) {
		t.Fatalf("evidence envelope (%d bytes) must stay bounded below the full body (%d bytes)", len(envelopePut), len(body))
	}
}

// TestHTTPRequestLargeHeadersExplicitTruncation (Ruling T5-C): a pathological
// large header value is size-bounded with an EXPLICIT headers_truncated flag in
// both the tool result and the evidence; the call succeeds (no 413) and the
// value is plaintext up to the size bound (a SIZE bound, not a security scrub).
func TestHTTPRequestLargeHeadersExplicitTruncation(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	bigValue := strings.Repeat("a", 10_000)
	fixture.http.response = executionapp.HTTPResponseData{
		Status:  200,
		Body:    []byte(`{"ok":true}`),
		Headers: map[string][]string{"X-Big": {bigValue}},
	}
	result, err := fixture.runner.HTTP(context.Background(), executionScope(), json.RawMessage(`{
		"sut_id":"7","path":"/api/orders","headers":{"X-Req":"`+bigValue+`"}
	}`))
	if err != nil {
		t.Fatalf("large-header HTTP must not fail the whole call: %v", err)
	}
	payload := result.(map[string]any)
	if payload["headers_truncated"] != true {
		t.Fatalf("headers_truncated = %v, want explicit true", payload["headers_truncated"])
	}
	outHeaders, ok := payload["headers"].(map[string][]string)
	if !ok {
		t.Fatalf("headers type = %T", payload["headers"])
	}
	got := outHeaders["X-Big"]
	if len(got) != 1 || len([]rune(got[0])) != 500 {
		t.Fatalf("X-Big value length = %d, want the 500-char size bound", len([]rune(got[0])))
	}
	if !strings.HasPrefix(got[0], "aaaa") {
		t.Fatalf("header value must stay plaintext up to the size bound, got %q", got[0])
	}
	// Evidence is still stored (no 413) and flags the header truncation.
	rows, _ := fixture.evidence.ListByCaseRun(context.Background(), 3001, 0)
	if len(rows) != 1 {
		t.Fatalf("evidence rows = %d, want 1 (large headers must not 413 the call)", len(rows))
	}
	var sawFlag bool
	for _, put := range fixture.content.puts {
		if bytes.Contains(put, []byte(`"headers_truncated":true`)) {
			sawFlag = true
		}
		if bytes.Contains(put, []byte(bigValue)) {
			t.Fatalf("evidence must size-bound the pathological header value")
		}
	}
	if !sawFlag {
		t.Fatalf("evidence envelope must carry headers_truncated:true")
	}
}

// TestHTTPRequestLargeRequestBodyFlagged: a request body larger than the evidence
// preview is sent verbatim (the 64KB send cap is lifted) and the evidence flags
// the request-body preview truncation explicitly (no silent truncation, art.7.6).
func TestHTTPRequestLargeRequestBodyFlagged(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	bigBody := strings.Repeat("y", 10_000)
	payload, _ := json.Marshal(map[string]any{"sut_id": "7", "path": "/api/orders", "method": "POST", "body": bigBody})
	if _, err := fixture.runner.HTTP(context.Background(), executionScope(), payload); err != nil {
		t.Fatalf("HTTP with large request body: %v", err)
	}
	if string(fixture.http.lastReq.Body) != bigBody {
		t.Fatalf("request body was not sent verbatim (len %d, want %d)", len(fixture.http.lastReq.Body), len(bigBody))
	}
	var sawFlag bool
	for _, put := range fixture.content.puts {
		if bytes.Contains(put, []byte(`"request"`)) && bytes.Contains(put, []byte(`"body_truncated":true`)) {
			sawFlag = true
		}
	}
	if !sawFlag {
		t.Fatalf("evidence request section must flag body_truncated:true for a large request body")
	}
}

// TestHTTPRequestTimeoutPassthrough: the per-call timeout_ms reaches the adapter
// (default 15s when omitted; configurable per call, exec.run precedent).
func TestHTTPRequestTimeoutPassthrough(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	if _, err := fixture.runner.HTTP(context.Background(), executionScope(), json.RawMessage(`{
		"sut_id":"7","path":"/api/orders","timeout_ms":3000
	}`)); err != nil {
		t.Fatalf("HTTP with timeout_ms: %v", err)
	}
	if fixture.http.lastReq.TimeoutMS != 3000 {
		t.Fatalf("adapter TimeoutMS = %d, want 3000", fixture.http.lastReq.TimeoutMS)
	}
}

// TestDBQueryWriteSQLSucceeds: general read-write SQL is accepted (the old
// validateReadOnlySQL gate rejected INSERT/UPDATE/multi-statement).
func TestDBQueryWriteSQLSucceeds(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	for _, sqlText := range []string{
		"INSERT INTO users (name) VALUES ('alice')",
		"UPDATE users SET admin = true",
		"DELETE FROM users WHERE id = 1",
		"SELECT 1; SELECT 2",
	} {
		fixture.db.queryResult = executionapp.DBQueryResult{CommandTag: "UPDATE 1", RowsAffected: 1}
		result, err := fixture.runner.DBQuery(context.Background(), executionScope(),
			json.RawMessage(`{"sut_id":"7","sql":`+jsonString(sqlText)+`}`))
		if err != nil {
			t.Fatalf("db.query rejected write SQL %q: %v", sqlText, err)
		}
		if fixture.db.lastSQL != sqlText {
			t.Fatalf("querier SQL = %q, want %q", fixture.db.lastSQL, sqlText)
		}
		payload := result.(map[string]any)
		if payload["command_tag"] != "UPDATE 1" {
			t.Fatalf("command_tag = %v, want UPDATE 1", payload["command_tag"])
		}
	}
	rows, _ := fixture.evidence.ListByCaseRun(context.Background(), 3001, 0)
	if len(rows) == 0 || rows[0].Kind != execution.EvidenceDB {
		t.Fatalf("db evidence = %+v", rows)
	}
}

// TestDBQueryReturnsMoreThan200Rows: with no max_rows the result is unlimited
// (the old hard 200-row cap is lifted).
func TestDBQueryReturnsMoreThan200Rows(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	rows := make([][]string, 500)
	for i := range rows {
		rows[i] = []string{strings.Repeat("v", 3)}
	}
	fixture.db.queryResult = executionapp.DBQueryResult{Columns: []string{"n"}, Rows: rows, RowCount: 500}
	result, err := fixture.runner.DBQuery(context.Background(), executionScope(), json.RawMessage(`{
		"sut_id":"7","sql":"SELECT n FROM big_table"
	}`))
	if err != nil {
		t.Fatalf("db.query: %v", err)
	}
	payload := result.(map[string]any)
	outRows, ok := payload["rows"].([][]string)
	if !ok || len(outRows) != 500 {
		t.Fatalf("rows len = %d, want 500 (the 200-row cap is lifted)", len(outRows))
	}
	if payload["truncated"] != false {
		t.Fatalf("truncated = %v, want false when no max_rows bounds the result", payload["truncated"])
	}
	if fixture.db.lastOpts.MaxRows != 0 {
		t.Fatalf("adapter MaxRows = %d, want 0 (unlimited) when max_rows is omitted", fixture.db.lastOpts.MaxRows)
	}
}

// TestDBQueryExplicitMaxRowsAndTimeout: an explicit max_rows still bounds the
// result, and statement_timeout_ms is configurable per call (default 5s).
func TestDBQueryExplicitMaxRowsAndTimeout(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	if _, err := fixture.runner.DBQuery(context.Background(), executionScope(), json.RawMessage(`{
		"sut_id":"7","sql":"SELECT 1","max_rows":1000,"statement_timeout_ms":2000
	}`)); err != nil {
		t.Fatalf("db.query with max_rows=1000: %v", err)
	}
	if fixture.db.lastOpts.MaxRows != 1000 {
		t.Fatalf("adapter MaxRows = %d, want 1000 (the 200 cap is lifted)", fixture.db.lastOpts.MaxRows)
	}
	if fixture.db.lastOpts.StatementTimeoutMS != 2000 {
		t.Fatalf("adapter StatementTimeoutMS = %d, want 2000", fixture.db.lastOpts.StatementTimeoutMS)
	}
}

// TestDBQueryLargeResultSpillsFullEvidence: when the full result exceeds the
// configured evidence byte budget, the FULL result spills to the content ledger
// and the evidence envelope stays bounded with an explicit truncation flag
// (the old 64KB silent evidence truncation is removed; no 413).
func TestDBQueryLargeResultSpillsFullEvidence(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	fixture.settings.ints["execution.evidence_max_bytes"] = 512
	rows := make([][]string, 200)
	for i := range rows {
		rows[i] = []string{strings.Repeat("value-", 20)}
	}
	fixture.db.queryResult = executionapp.DBQueryResult{Columns: []string{"c"}, Rows: rows, RowCount: 200}
	result, err := fixture.runner.DBQuery(context.Background(), executionScope(), json.RawMessage(`{
		"sut_id":"7","sql":"SELECT c FROM big_table"
	}`))
	if err != nil {
		t.Fatalf("large db.query must not 413: %v", err)
	}
	payload := result.(map[string]any)
	if outRows, ok := payload["rows"].([][]string); !ok || len(outRows) != 200 {
		t.Fatalf("tool result rows must stay complete (runner spills generically), got %d", len(payload["rows"].([][]string)))
	}
	// The FULL result JSON must be in the ledger; the envelope must be bounded.
	var sawFullResult, sawBoundedEnvelope bool
	for _, put := range fixture.content.puts {
		var decoded map[string]any
		if json.Unmarshal(put, &decoded) != nil {
			continue
		}
		if decoded["result_truncated"] == true {
			sawBoundedEnvelope = true
			if len(put) > 512 {
				t.Fatalf("bounded db evidence envelope = %d bytes, want <= the 512 budget", len(put))
			}
		} else if r, ok := decoded["rows"].([]any); ok && len(r) == 200 {
			sawFullResult = true
		}
	}
	if !sawFullResult {
		t.Fatalf("the full db result was not spilled to the content ledger")
	}
	if !sawBoundedEnvelope {
		t.Fatalf("the db evidence envelope must flag result_truncated:true")
	}
}

// TestCredentialsEvidenceEligible: the credential result faces no server-side
// barrier to entering evidence (GD-01/Q05=B). The prohibition was a prompt /
// description convention, now lifted; the server never blocked it.
func TestCredentialsEvidenceEligible(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	cred, err := fixture.runner.AccountCredentials(context.Background(), executionScope(), tools.AccountCredentialsInput{
		VersionSUTID: int64ptr(7), Role: "admin",
	})
	if err != nil {
		t.Fatalf("AccountCredentials: %v", err)
	}
	password := cred.(map[string]any)["password"].(string)
	stored, err := fixture.runner.EvidenceText(context.Background(), executionScope(), tools.EvidenceTextInput{
		Kind: "text", Text: "login password=" + password,
	})
	if err != nil {
		t.Fatalf("credential-bearing evidence must be accepted (no server barrier): %v", err)
	}
	if stored.(map[string]any)["evidence_id"] == nil {
		t.Fatalf("evidence not stored: %+v", stored)
	}
	var sawPassword bool
	for _, put := range fixture.content.puts {
		if bytes.Contains(put, []byte(password)) {
			sawPassword = true
		}
	}
	if !sawPassword {
		t.Fatalf("credential plaintext must be able to enter evidence by design")
	}
}
