package deliverypg

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

/* ------------------------------ filter builders ---------------------------- */

func TestGlobalIssueWhereBuildsEveryFilter(t *testing.T) {
	versionID := int64(42)
	where, args := globalIssueWhere(deliveryapp.GlobalIssueFilter{
		VersionID: &versionID,
		Status:    "open",
		Category:  "ui",
		Severity:  "major",
		Q:         "结算",
		Before:    900,
	})
	want := "version_id = $1 AND status = $2 AND category = $3 AND severity = $4 AND title ILIKE $5 AND id < $6"
	if where != want {
		t.Fatalf("where = %q, want %q", where, want)
	}
	if len(args) != 6 || args[0] != int64(42) || args[1] != "open" ||
		args[2] != "ui" || args[3] != "major" || args[4] != "%结算%" || args[5] != int64(900) {
		t.Fatalf("args = %#v", args)
	}
}

func TestGlobalIssueWhereWithoutFilters(t *testing.T) {
	where, args := globalIssueWhere(deliveryapp.GlobalIssueFilter{})
	if where != "TRUE" {
		t.Fatalf("where = %q, want TRUE", where)
	}
	if len(args) != 0 {
		t.Fatalf("args = %#v, want none", args)
	}
}

func TestGlobalReportWhereBuildsEveryFilter(t *testing.T) {
	versionID := int64(43)
	where, args := globalReportWhere(deliveryapp.GlobalReportFilter{
		VersionID: &versionID,
		Status:    "published",
		Kind:      "wave",
		Before:    800,
	})
	want := "r.version_id = $1 AND r.status = $2 AND r.kind = $3 AND r.id < $4"
	if where != want {
		t.Fatalf("where = %q, want %q", where, want)
	}
	if len(args) != 4 || args[0] != int64(43) || args[1] != "published" ||
		args[2] != "wave" || args[3] != int64(800) {
		t.Fatalf("args = %#v", args)
	}
}

func TestGlobalReportWhereWithoutFilters(t *testing.T) {
	where, args := globalReportWhere(deliveryapp.GlobalReportFilter{})
	if where != "TRUE" {
		t.Fatalf("where = %q, want TRUE", where)
	}
	if len(args) != 0 {
		t.Fatalf("args = %#v, want none", args)
	}
}

/* --------------------------------- queries --------------------------------- */

func TestGlobalQueriesAreReadOnlyAndWindowed(t *testing.T) {
	if !strings.Contains(globalIssueOrderSQL, "ORDER BY id DESC") {
		t.Fatalf("issues SQL has no descending page order: %s", globalIssueOrderSQL)
	}
	if !strings.Contains(globalReportsSQL, "LEFT JOIN delivery_report_revision rev ON rev.id = r.current_revision_id") {
		t.Fatalf("reports SQL does not join the current revision: %s", globalReportsSQL)
	}
	if !strings.Contains(globalReportOrderSQL, "ORDER BY r.id DESC") {
		t.Fatalf("reports SQL has no descending page order: %s", globalReportOrderSQL)
	}
	for name, query := range map[string]string{
		"issues":  globalIssuesSQL,
		"reports": globalReportsSQL,
	} {
		lower := strings.ToLower(query)
		for _, forbidden := range []string{"content_object", "storage_path", "html_ref AS", "password", "secret"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("%s SQL leaks %q: %s", name, forbidden, query)
			}
		}
	}
}

/* --------------------------------- scanning -------------------------------- */

// fakeGlobalRow feeds scanGlobalReport with typed values; nil maps to SQL NULL.
type fakeGlobalRow struct {
	values []any
	err    error
}

func (r fakeGlobalRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return fmt.Errorf("scanGlobalReport: got %d destinations, want %d", len(dest), len(r.values))
	}
	for index := range dest {
		if err := assignGlobalScan(dest[index], r.values[index]); err != nil {
			return fmt.Errorf("scanGlobalReport column %d: %w", index, err)
		}
	}
	return nil
}

// assignGlobalScan emulates pgx's nullable scan: a nil value clears the
// destination and a non-nil value fills a pointer destination from a value.
func assignGlobalScan(dest, value any) error {
	target := reflect.ValueOf(dest)
	if target.Kind() != reflect.Ptr || target.IsNil() {
		return errors.New("destination is not a non-nil pointer")
	}
	slot := target.Elem()
	if value == nil {
		slot.SetZero()
		return nil
	}
	source := reflect.ValueOf(value)
	if slot.Kind() == reflect.Ptr {
		pointer := reflect.New(slot.Type().Elem())
		pointer.Elem().Set(source)
		slot.Set(pointer)
		return nil
	}
	if source.Type().AssignableTo(slot.Type()) {
		slot.Set(source)
		return nil
	}
	if source.Type().ConvertibleTo(slot.Type()) {
		slot.Set(source.Convert(slot.Type()))
		return nil
	}
	return fmt.Errorf("cannot assign %T to %s", value, slot.Type())
}

func globalScanTime() time.Time { return time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC) }

func globalReportValues(includeRevision bool) []any {
	now := globalScanTime()
	values := []any{
		int64(701), int64(42), nil, "version", int64(1701), "published",
		strings.Repeat("a", 64), nil, now, now,
	}
	if !includeRevision {
		return append(values, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	}
	return append(values,
		int64(1701), int64(701), int32(3), "published", int64(500), nil,
		strings.Repeat("b", 64), now, "1", "done", nil, now, now,
	)
}

func TestScanGlobalReportWithCurrentRevision(t *testing.T) {
	row, err := scanGlobalReport(fakeGlobalRow{values: globalReportValues(true)})
	if err != nil {
		t.Fatalf("scanGlobalReport: %v", err)
	}
	if row.Report.ID != 701 || row.Report.VersionID != 42 || row.Report.Status != delivery.ReportStatusPublished {
		t.Fatalf("report = %#v", row.Report)
	}
	if row.CurrentRevision == nil {
		t.Fatal("current revision = nil, want the joined revision")
	}
	revision := row.CurrentRevision
	if revision.ID != 1701 || revision.ReportID != 701 || revision.Revision != 3 ||
		revision.HTMLRef == nil || *revision.HTMLRef != 500 || revision.RenderStatus != "done" {
		t.Fatalf("revision = %#v", revision)
	}
}

func TestScanGlobalReportWithoutRevision(t *testing.T) {
	row, err := scanGlobalReport(fakeGlobalRow{values: globalReportValues(false)})
	if err != nil {
		t.Fatalf("scanGlobalReport: %v", err)
	}
	if row.CurrentRevision != nil {
		t.Fatalf("current revision = %#v, want nil", row.CurrentRevision)
	}
}

func TestScanGlobalReportPropagatesScanErrors(t *testing.T) {
	boom := errors.New("scan boom")
	if _, err := scanGlobalReport(fakeGlobalRow{err: boom}); !errors.Is(err, boom) {
		t.Fatalf("error = %v, want the scan error", err)
	}
}
