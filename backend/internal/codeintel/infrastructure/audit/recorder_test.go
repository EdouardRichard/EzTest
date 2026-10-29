package audit_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	auditdomain "github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
	codeintelaudit "github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/audit"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// fakeRecorder captures every audit entry the adapter records.
type fakeRecorder struct {
	entries []auditdomain.Entry
	err     error
}

// Record stores the entry and returns the configured failure.
func (f *fakeRecorder) Record(_ context.Context, entry auditdomain.Entry) error {
	f.entries = append(f.entries, entry)
	return f.err
}

// auditDetailKeys is the identifier/status allowlist the adapter may persist.
var auditDetailKeys = map[string]struct{}{
	"repoId":     {},
	"versionId":  {},
	"commit":     {},
	"baseCommit": {},
	"headCommit": {},
	"path":       {},
	"status":     {},
	"analysisId": {},
}

// TestAuditRecorderOmitsCodeContent is the core audit contract: an entry names
// the actor, action and subject, keeps only identifiers/status in its metadata
// and never persists code content, diffs or prompts.
func TestAuditRecorderOmitsCodeContent(t *testing.T) {
	recorder := &fakeRecorder{}
	adapter := codeintelaudit.NewRecorder(recorder)
	detail := map[string]any{
		"repoId":     int64(42),
		"commit":     strings.Repeat("a", 40),
		"path":       "src/api/refund.ts",
		"status":     "done",
		"analysisId": int64(9001),
		"content":    "export const secret = \"TOP_SECRET_CODE\";",
		"diff":       "+const secret = 'TOP_SECRET_CODE'",
		"prompt":     "PROMPT_SECRET",
		"title":      "TITLE_SECRET",
	}

	err := adapter.Record(context.Background(), 7, "code.read", "code_repository:42", detail)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if len(recorder.entries) != 1 {
		t.Fatalf("entries=%d want 1", len(recorder.entries))
	}
	entry := recorder.entries[0]
	if entry.Action != "code.read" {
		t.Fatalf("action=%q want code.read", entry.Action)
	}
	if entry.ActorUserID == nil || *entry.ActorUserID != 7 {
		t.Fatalf("actor=%v want 7", entry.ActorUserID)
	}
	if entry.TargetType != "code_repository" || entry.TargetID != "42" {
		t.Fatalf("target=%q/%q want code_repository/42", entry.TargetType, entry.TargetID)
	}
	if len(entry.Metadata) != 5 {
		t.Fatalf("metadata=%v want exactly the five allowlisted keys", entry.Metadata)
	}
	for key := range entry.Metadata {
		if _, ok := auditDetailKeys[key]; !ok {
			t.Fatalf("metadata contains non-allowlisted key %q: %v", key, entry.Metadata)
		}
	}
	raw, err := json.Marshal(entry.Metadata)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	for _, secret := range []string{"TOP_SECRET_CODE", "PROMPT_SECRET", "TITLE_SECRET"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("metadata leaks %q: %s", secret, raw)
		}
	}

	// Review R23 / D29 / INV-9: identifier values must be decimal strings in the
	// persisted JSON, never numbers, so a snowflake id cannot lose precision.
	var persisted map[string]any
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	for _, key := range []string{"repoId", "analysisId"} {
		if _, ok := persisted[key].(string); !ok {
			t.Fatalf("metadata %s=%v (%T) want a decimal string", key, persisted[key], persisted[key])
		}
	}
	if persisted["analysisId"] != "9001" || persisted["repoId"] != "42" {
		t.Fatalf("metadata ids=%v want analysisId=\"9001\", repoId=\"42\"", persisted)
	}

	if entry.Metadata["path"] != "src/api/refund.ts" ||
		entry.Metadata["commit"] != strings.Repeat("a", 40) ||
		entry.Metadata["status"] != "done" ||
		entry.Metadata["repoId"] != "42" ||
		entry.Metadata["analysisId"] != "9001" {
		t.Fatalf("metadata=%v", entry.Metadata)
	}
}

// TestAuditRecorderMapsAnalyzeEntry pins the second action: an anonymous
// analysis trigger keeps a nil actor, and version/base/head identifiers travel
// in metadata.
func TestAuditRecorderMapsAnalyzeEntry(t *testing.T) {
	recorder := &fakeRecorder{}
	adapter := codeintelaudit.NewRecorder(recorder)

	err := adapter.Record(context.Background(), 0, "code.analyze", "code_analysis:9001", map[string]any{
		"versionId":  int64(5),
		"baseCommit": strings.Repeat("b", 40),
		"headCommit": strings.Repeat("c", 40),
		"status":     "queued",
		"analysisId": int64(9001),
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	entry := recorder.entries[0]
	if entry.Action != "code.analyze" {
		t.Fatalf("action=%q want code.analyze", entry.Action)
	}
	if entry.ActorUserID != nil {
		t.Fatalf("actor=%v want nil for actor id 0", entry.ActorUserID)
	}
	if entry.TargetType != "code_analysis" || entry.TargetID != "9001" {
		t.Fatalf("target=%q/%q", entry.TargetType, entry.TargetID)
	}
	if entry.Metadata["versionId"] != "5" || entry.Metadata["status"] != "queued" {
		t.Fatalf("metadata=%v", entry.Metadata)
	}
	if entry.Metadata["analysisId"] != "9001" {
		t.Fatalf("metadata.analysisId=%v want the decimal string \"9001\"", entry.Metadata["analysisId"])
	}
}

// TestAuditRecorderNormalizesIDKeys pins review R23: whatever numeric shape a
// caller uses for an identifier, the recorded metadata carries the decimal
// string. Values that are not decimal identifiers are dropped rather than
// persisted under an id key.
func TestAuditRecorderNormalizesIDKeys(t *testing.T) {
	record := func(t *testing.T, detail map[string]any) auditdomain.Entry {
		t.Helper()
		recorder := &fakeRecorder{}
		adapter := codeintelaudit.NewRecorder(recorder)
		if err := adapter.Record(context.Background(), 7, "code.analyze", "code_analysis:9001", detail); err != nil {
			t.Fatalf("Record: %v", err)
		}
		if len(recorder.entries) != 1 {
			t.Fatalf("entries=%d want 1", len(recorder.entries))
		}
		return recorder.entries[0]
	}

	t.Run("numeric ids become decimal strings", func(t *testing.T) {
		entry := record(t, map[string]any{
			"analysisId": int64(9001),
			"versionId":  7,
			"repoId":     json.Number("42"),
		})
		if entry.Metadata["analysisId"] != "9001" || entry.Metadata["versionId"] != "7" || entry.Metadata["repoId"] != "42" {
			t.Fatalf("metadata=%v want decimal string ids", entry.Metadata)
		}
	})

	t.Run("decimal strings pass through unchanged", func(t *testing.T) {
		entry := record(t, map[string]any{
			"analysisId": "9001",
			"versionId":  "7",
			"repoId":     "42",
		})
		if entry.Metadata["analysisId"] != "9001" || entry.Metadata["versionId"] != "7" || entry.Metadata["repoId"] != "42" {
			t.Fatalf("metadata=%v want the caller's decimal strings", entry.Metadata)
		}
	})

	t.Run("non-decimal id values are dropped", func(t *testing.T) {
		entry := record(t, map[string]any{
			"analysisId": true,
			"versionId":  "not-an-id",
			"repoId":     -1,
			"path":       "src/main.go",
		})
		if len(entry.Metadata) != 1 || entry.Metadata["path"] != "src/main.go" {
			t.Fatalf("metadata=%v want only the path key", entry.Metadata)
		}
	})
}

// TestAuditRecorderMapsReviewEntry pins the P8 fact-review audit: the action is
// code.review, the fact/commit/verdict identifiers travel in metadata, and the
// human note body is dropped even when a caller puts it in the detail map.
func TestAuditRecorderMapsReviewEntry(t *testing.T) {
	recorder := &fakeRecorder{}
	adapter := codeintelaudit.NewRecorder(recorder)
	note := "证据行号偏移，实际在第 80 行"

	err := adapter.Record(context.Background(), 7, "code.review", "code_repository:42", map[string]any{
		"repoId":  int64(42),
		"factId":  int64(9301),
		"commit":  strings.Repeat("b", 40),
		"verdict": "incorrect",
		"reason":  note,
		"content": "TOP_SECRET_CODE",
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if len(recorder.entries) != 1 {
		t.Fatalf("entries=%d want 1", len(recorder.entries))
	}
	entry := recorder.entries[0]
	if entry.Action != "code.review" {
		t.Fatalf("action=%q want code.review", entry.Action)
	}
	if entry.ActorUserID == nil || *entry.ActorUserID != 7 {
		t.Fatalf("actor=%v want 7", entry.ActorUserID)
	}
	if entry.TargetType != "code_repository" || entry.TargetID != "42" {
		t.Fatalf("target=%q/%q want code_repository/42", entry.TargetType, entry.TargetID)
	}
	if entry.Metadata["verdict"] != "incorrect" || entry.Metadata["commit"] != strings.Repeat("b", 40) {
		t.Fatalf("metadata=%v", entry.Metadata)
	}
	// D29: identifier values are recorded as decimal strings.
	if entry.Metadata["factId"] != "9301" || entry.Metadata["repoId"] != "42" {
		t.Fatalf("metadata ids=%v want decimal strings", entry.Metadata)
	}
	raw, err := json.Marshal(entry.Metadata)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	if strings.Contains(string(raw), note) || strings.Contains(string(raw), "TOP_SECRET_CODE") {
		t.Fatalf("review audit leaks the note/code body: %s", raw)
	}
	for key := range entry.Metadata {
		if key != "repoId" && key != "factId" && key != "commit" && key != "verdict" {
			t.Fatalf("metadata contains unexpected key %q: %v", key, entry.Metadata)
		}
	}
}

// TestAuditRecorderErrors pins the failure contract: an unsupported action is
// rejected without writing, an empty subject falls back to the code target type,
// and a recorder failure is returned to the caller.
func TestAuditRecorderErrors(t *testing.T) {
	t.Run("unsupported action", func(t *testing.T) {
		recorder := &fakeRecorder{}
		adapter := codeintelaudit.NewRecorder(recorder)
		if err := adapter.Record(context.Background(), 7, "code.delete", "code_repository:42", nil); err == nil {
			t.Fatal("Record(unsupported action)=nil want an error")
		}
		if len(recorder.entries) != 0 {
			t.Fatalf("entries=%d want 0", len(recorder.entries))
		}
	})

	t.Run("subject without a type falls back to code", func(t *testing.T) {
		recorder := &fakeRecorder{}
		adapter := codeintelaudit.NewRecorder(recorder)
		if err := adapter.Record(context.Background(), 7, "code.read", "src/main.go", nil); err != nil {
			t.Fatalf("Record: %v", err)
		}
		entry := recorder.entries[0]
		if entry.TargetType != "code" || entry.TargetID != "src/main.go" {
			t.Fatalf("target=%q/%q want code/src/main.go", entry.TargetType, entry.TargetID)
		}
	})

	t.Run("recorder failure is returned", func(t *testing.T) {
		adapter := codeintelaudit.NewRecorder(&fakeRecorder{err: errors.New("audit database down")})
		if err := adapter.Record(context.Background(), 7, "code.read", "code_repository:42", nil); err == nil {
			t.Fatal("Record error=nil want the audit failure")
		}
	})

	t.Run("a nil recorder is a disabled no-op", func(t *testing.T) {
		if err := codeintelaudit.NewRecorder(nil).Record(context.Background(), 7, "code.read", "code_repository:42", nil); err != nil {
			t.Fatalf("Record(nil recorder)=%v want nil", err)
		}
	})
}

// TestAuditRecorderStampsRequestCorrelation pins the P0 correlation contract for
// code audits: an entry recorded on an HTTP request path carries that request's
// metadata, and an entry recorded by the async analysis worker keeps the request
// id the trigger copied into the run context (no IP/User-Agent there).
func TestAuditRecorderStampsRequestCorrelation(t *testing.T) {
	recorder := &fakeRecorder{}
	adapter := codeintelaudit.NewRecorder(recorder)

	httpCtx := auditapp.WithMeta(context.Background(), auditapp.RequestMeta{
		RequestID: "req_http_1234",
		IP:        "203.0.113.7",
		UserAgent: "eztest-e2e",
	})
	if err := adapter.Record(httpCtx, 7, "code.analyze", "analysis:1", map[string]any{"status": "queued"}); err != nil {
		t.Fatalf("Record(http): %v", err)
	}

	workerCtx := platform.WithRequestID(context.Background(), "req_worker_5678")
	if err := adapter.Record(workerCtx, 7, "code.analyze", "analysis:2", map[string]any{"status": "done"}); err != nil {
		t.Fatalf("Record(worker): %v", err)
	}

	if err := adapter.Record(context.Background(), 7, "code.read", "code_repository:42", nil); err != nil {
		t.Fatalf("Record(background): %v", err)
	}

	if len(recorder.entries) != 3 {
		t.Fatalf("entries=%d want 3", len(recorder.entries))
	}
	httpEntry := recorder.entries[0]
	if httpEntry.RequestID != "req_http_1234" || httpEntry.IP != "203.0.113.7" ||
		httpEntry.UserAgent != "eztest-e2e" {
		t.Fatalf("http entry correlation=(%q,%q,%q) want the request metadata",
			httpEntry.RequestID, httpEntry.IP, httpEntry.UserAgent)
	}
	workerEntry := recorder.entries[1]
	if workerEntry.RequestID != "req_worker_5678" {
		t.Fatalf("worker entry request id=%q want req_worker_5678", workerEntry.RequestID)
	}
	if workerEntry.IP != "" || workerEntry.UserAgent != "" {
		t.Fatalf("worker entry ip/ua=(%q,%q) want empty", workerEntry.IP, workerEntry.UserAgent)
	}
	backgroundEntry := recorder.entries[2]
	if backgroundEntry.RequestID != "" || backgroundEntry.IP != "" || backgroundEntry.UserAgent != "" {
		t.Fatalf("background entry correlation=(%q,%q,%q) want empty",
			backgroundEntry.RequestID, backgroundEntry.IP, backgroundEntry.UserAgent)
	}
}
