package executionpg

// H-G4 T4 minors (v0.2.4-H T11): the evidence_ref joined read must not hand-mirror
// the single-table evidence scan, and ListByOwner must reject an owner_kind that
// is not a persisted evidence_ref owner kind (a typo would silently match zero
// rows and read as "no evidence bound" — constitution 七.6, no silent degradation).

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// stubRow feeds a fixed column tuple into a scan, assigning each destination by
// position and type. The destination-count check makes a column-order drift
// between scanEvidence and scanEvidenceRef a loud failure, not a silent mis-scan.
type stubRow struct{ cols []any }

func (s stubRow) Scan(dest ...any) error {
	if len(dest) != len(s.cols) {
		return &scanCountError{dest: len(dest), cols: len(s.cols)}
	}
	for i, d := range dest {
		v := s.cols[i]
		switch p := d.(type) {
		case *int64:
			*p = v.(int64)
		case *int:
			*p = v.(int)
		case *string:
			*p = v.(string)
		case *time.Time:
			*p = v.(time.Time)
		case **int64:
			x := v.(int64)
			*p = &x
		case **int:
			x := v.(int)
			*p = &x
		default:
			return &scanTypeError{index: i, dest: d}
		}
	}
	return nil
}

type scanCountError struct{ dest, cols int }

func (e *scanCountError) Error() string {
	return "scan destination count does not match column count"
}

type scanTypeError struct {
	index int
	dest  any
}

func (e *scanTypeError) Error() string { return "unsupported scan destination type" }

// TestScanEvidenceRefSharesEvidenceScanOrder pins the shared scan authority: the
// evidence fields of the joined evidence_ref read must map EXACTLY like the
// single-table scanEvidence read (both driven by evidenceColumns through one
// helper). A hand-mirrored field order that swaps two evidence columns fails the
// DeepEqual; a changed column count fails the stub's destination-count check.
func TestScanEvidenceRefSharesEvidenceScanOrder(t *testing.T) {
	now := time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC)
	sha := "b1e2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"
	evCols := []any{
		int64(700), int64(500), int64(5001), 1, 2, "screenshot",
		int64(42), sha, int64(100), "image/png", "shot.png", int64(900), now,
	}
	single, err := scanEvidence(stubRow{cols: evCols})
	if err != nil {
		t.Fatalf("scanEvidence: %v", err)
	}
	refCols := []any{int64(11), int64(700), execution.EvidenceOwnerCaseRun, int64(500), "primary", now}
	joined, err := scanEvidenceRef(stubRow{cols: append(append([]any{}, refCols...), evCols...)})
	if err != nil {
		t.Fatalf("scanEvidenceRef: %v", err)
	}
	if !reflect.DeepEqual(joined.Evidence, single) {
		t.Fatalf("joined evidence scan drifts from scanEvidence:\n joined=%+v\n single=%+v", joined.Evidence, single)
	}
	wantRef := execution.EvidenceRef{
		ID: 11, EvidenceID: 700, OwnerKind: execution.EvidenceOwnerCaseRun,
		OwnerID: 500, Role: "primary", CreatedAt: now,
	}
	if joined.Ref != wantRef {
		t.Fatalf("evidence_ref fields = %+v, want %+v", joined.Ref, wantRef)
	}
}

// TestListByOwnerRejectsUnknownOwnerKind pins the entry validation: an owner_kind
// outside the persisted evidence_ref vocabulary is a typed 400 field error, never
// a silent zero-row read. The valid kinds are exercised against real rows by
// TestEvidenceRefReadByOwner (integration).
func TestListByOwnerRejectsUnknownOwnerKind(t *testing.T) {
	repo := &EvidenceRepository{}
	for _, kind := range []string{"", "bogus", "CaseRun", "case_runs"} {
		_, err := repo.ListByOwner(context.Background(), kind, 500)
		if err == nil {
			t.Fatalf("ListByOwner(%q) = nil error, want a typed invalid owner_kind", kind)
		}
		typed := platform.AsError(err)
		if typed.Status != 400 || typed.Code != "invalid_argument" {
			t.Fatalf("ListByOwner(%q) error = %d/%s, want 400/invalid_argument", kind, typed.Status, typed.Code)
		}
		if typed.Details["field"] != "owner_kind" {
			t.Fatalf("ListByOwner(%q) field detail = %v, want owner_kind", kind, typed.Details["field"])
		}
	}
	// The port contract is unchanged: the reader still satisfies the application
	// port (compile-time pin).
	var _ executionapp.EvidenceRefReader = repo
}
