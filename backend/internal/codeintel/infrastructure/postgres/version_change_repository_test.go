package postgres

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// versionChangeTestTime is a lossless TIMESTAMPTZ reading (UTC, whole seconds).
var versionChangeTestTime = time.Date(2026, 10, 29, 8, 30, 0, 0, time.UTC)

// versionChangeFakeRow answers scanVersionChange with fixed column values in
// versionChangeColumnList order, so the column order and the JSONB decode stay
// pinned without a database.
type versionChangeFakeRow struct {
	values []any
}

func (r versionChangeFakeRow) Scan(dest ...any) error {
	if len(dest) != len(r.values) {
		return &versionChangeScanError{message: "destination count does not match"}
	}
	for index, target := range dest {
		switch typed := target.(type) {
		case *int64:
			*typed = r.values[index].(int64)
		case *string:
			*typed = r.values[index].(string)
		case *bool:
			*typed = r.values[index].(bool)
		case *int:
			*typed = r.values[index].(int)
		case *[]byte:
			*typed = r.values[index].([]byte)
		case *time.Time:
			*typed = r.values[index].(time.Time)
		default:
			return &versionChangeScanError{message: "unsupported destination"}
		}
	}
	return nil
}

// versionChangeScanError is the fake row's scan failure.
type versionChangeScanError struct{ message string }

func (e *versionChangeScanError) Error() string { return e.message }

// TestVersionChangeItemJSONShape pins the JSONB document stored in
// version_change.items: camelCase summaries only, with subjects and decimal-string
// case ids, never a code body.
func TestVersionChangeItemJSONShape(t *testing.T) {
	items := []domain.VersionChangeItem{
		{
			File:       "src/api/refund.ts",
			ChangeKind: domain.VersionChangeModified,
			Subjects: []domain.VersionChangeSubject{
				{Kind: domain.KindAPI, Name: "POST /api/refund"},
			},
			CaseIDs:  []string{"501", "18446744073709551616"},
			Unmapped: false,
		},
		{
			File:       "src/deleted.ts",
			ChangeKind: domain.VersionChangeDeleted,
			Unmapped:   true,
		},
	}

	encoded, err := marshalVersionChangeItems(items)
	if err != nil {
		t.Fatalf("marshalVersionChangeItems: %v", err)
	}
	want := `[{"file":"src/api/refund.ts","changeKind":"modified","subjects":[{"kind":"api","name":"POST /api/refund"}],"caseIds":["501","18446744073709551616"],"unmapped":false},{"file":"src/deleted.ts","changeKind":"deleted","subjects":[],"caseIds":[],"unmapped":true}]`
	if encoded != want {
		t.Fatalf("items JSON = %s, want %s", encoded, want)
	}
	for _, forbidden := range []string{"content", "diff", "patch", "source"} {
		if strings.Contains(encoded, `"`+forbidden+`"`) {
			t.Fatalf("items JSON leaks %q: %s", forbidden, encoded)
		}
	}

	decoded, err := unmarshalVersionChangeItems([]byte(want))
	if err != nil {
		t.Fatalf("unmarshalVersionChangeItems: %v", err)
	}
	if len(decoded) != 2 || decoded[0].File != "src/api/refund.ts" ||
		decoded[0].Subjects[0].Name != "POST /api/refund" ||
		decoded[0].CaseIDs[1] != "18446744073709551616" ||
		!decoded[1].Unmapped {
		t.Fatalf("round trip items = %+v", decoded)
	}
	if decoded[1].Subjects == nil || decoded[1].CaseIDs == nil {
		t.Fatal("empty nested arrays must decode as non-nil slices")
	}

	empty, err := marshalVersionChangeItems(nil)
	if err != nil {
		t.Fatalf("marshal(nil): %v", err)
	}
	if empty != "[]" {
		t.Fatalf("marshal(nil) = %s, want []", empty)
	}
}

// TestScanVersionChangePinsColumnOrder feeds the shared column values into
// scanVersionChange and asserts the decoded domain row.
func TestScanVersionChangePinsColumnOrder(t *testing.T) {
	storedItems, err := marshalVersionChangeItems([]domain.VersionChangeItem{
		{File: "src/api/refund.ts", ChangeKind: domain.VersionChangeModified, CaseIDs: []string{"501"}},
	})
	if err != nil {
		t.Fatalf("marshal items: %v", err)
	}
	row := versionChangeFakeRow{values: []any{
		int64(7001), int64(9), int64(11), "base", "head-commit", true,
		3, 2, 1, []byte(storedItems), versionChangeTestTime, versionChangeTestTime.Add(time.Second),
	}}

	change, err := scanVersionChange(row)
	if err != nil {
		t.Fatalf("scanVersionChange: %v", err)
	}
	if change.ID != 7001 || change.VersionID != 9 || change.BatchID != 11 {
		t.Fatalf("ids = %d/%d/%d", change.ID, change.VersionID, change.BatchID)
	}
	if change.BaseCommit != "base" || change.HeadCommit != "head-commit" || !change.HistoryRewritten {
		t.Fatalf("commits/flags = %+v", change)
	}
	if change.ChangedFiles != 3 || change.ImpactedCases != 2 || change.UnmappedCount != 1 {
		t.Fatalf("counts = %+v", change)
	}
	if len(change.Items) != 1 || change.Items[0].CaseIDs[0] != "501" {
		t.Fatalf("items = %+v", change.Items)
	}
	if !change.ComputedAt.Equal(versionChangeTestTime) {
		t.Fatalf("computedAt = %v, want %v", change.ComputedAt, versionChangeTestTime)
	}
}

// TestVersionChangeRepositorySQLPinsUniqueUpsert pins the adapter's two
// non-negotiable SQL properties: the snapshot upsert is conflict-free on
// (version_id, batch_id) and the batch projection reads the execution
// context's run_batch rows without writing them.
func TestVersionChangeRepositorySQLPinsUniqueUpsert(t *testing.T) {
	for _, needle := range []string{
		"INSERT INTO version_change",
		"ON CONFLICT (version_id, batch_id) DO NOTHING",
	} {
		if !strings.Contains(upsertVersionChangeSQL, needle) {
			t.Fatalf("upsert SQL does not contain %q: %s", needle, upsertVersionChangeSQL)
		}
	}
	for _, needle := range []string{
		"FROM run_batch",
		"version_id = $1",
		"ORDER BY batch_no, id",
	} {
		if !strings.Contains(listVersionBatchesSQL, needle) {
			t.Fatalf("batch SQL does not contain %q: %s", needle, listVersionBatchesSQL)
		}
	}
	if strings.Contains(listVersionBatchesSQL, "INSERT") || strings.Contains(listVersionBatchesSQL, "UPDATE") {
		t.Fatalf("batch projection must be read-only: %s", listVersionBatchesSQL)
	}
}

// TestVersionChangeItemJSONIsValidJSON guards the stored document against a
// future field that cannot be encoded.
func TestVersionChangeItemJSONIsValidJSON(t *testing.T) {
	encoded, err := marshalVersionChangeItems([]domain.VersionChangeItem{{File: "a"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !json.Valid([]byte(encoded)) {
		t.Fatalf("items JSON is invalid: %s", encoded)
	}
}
