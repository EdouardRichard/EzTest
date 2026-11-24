package execution

import (
	"errors"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// assertFactField asserts a 400 invalid_argument field error for the F fact
// entities (shared by the sut_resolution/asset_registration/session_fixture tests).
func assertFactField(t *testing.T, err error, field string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected a validation error for %q", field)
	}
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %v is not a *platform.Error", err)
	}
	if apiErr.Status != 400 || apiErr.Code != "invalid_argument" {
		t.Fatalf("error = %d/%s, want 400/invalid_argument", apiErr.Status, apiErr.Code)
	}
	if got := apiErr.Details["field"]; got != field {
		t.Fatalf("field = %v, want %q", got, field)
	}
}

func TestSUTResolutionValidate(t *testing.T) {
	at := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	valid := SUTResolution{
		ID: 1, CaseRunID: 7, SUTIDs: []int64{10},
		ResolutionMethod: "catalog_hit", TouchedPageIDs: []int64{20}, TouchedAPIIDs: []int64{30},
		CreatedAt: at, CreatedBy: 42,
	}
	cases := []struct {
		name    string
		mutate  func(*SUTResolution)
		wantErr bool
		field   string
	}{
		{name: "valid catalog hit", mutate: func(*SUTResolution) {}},
		{name: "valid exploration multi-sut", mutate: func(r *SUTResolution) {
			r.SUTIDs = []int64{10, 11}
			r.ResolutionMethod = "exploration"
			r.TouchedPageIDs = []int64{20, 21}
			r.TouchedAPIIDs = []int64{30, 31}
		}},
		{name: "no touched assets is valid", mutate: func(r *SUTResolution) {
			r.TouchedPageIDs = []int64{}
			r.TouchedAPIIDs = []int64{}
		}},
		{name: "missing case run", mutate: func(r *SUTResolution) { r.CaseRunID = 0 }, wantErr: true, field: "case_run_id"},
		{name: "empty resolution method", mutate: func(r *SUTResolution) { r.ResolutionMethod = "" }, wantErr: true, field: "resolution_method"},
		{name: "non-positive sut id", mutate: func(r *SUTResolution) { r.SUTIDs = []int64{0} }, wantErr: true, field: "sut_ids"},
		{name: "non-positive touched page id", mutate: func(r *SUTResolution) { r.TouchedPageIDs = []int64{-1} }, wantErr: true, field: "touched_page_ids"},
		{name: "non-positive touched api id", mutate: func(r *SUTResolution) { r.TouchedAPIIDs = []int64{0} }, wantErr: true, field: "touched_api_ids"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := valid
			tc.mutate(&res)
			err := res.Validate()
			if tc.wantErr != (err != nil) {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.field != "" {
				assertFactField(t, err, tc.field)
			}
		})
	}
}

func TestSUTResolutionNormalize(t *testing.T) {
	res := SUTResolution{ResolutionMethod: "  catalog_hit  "}
	res.Normalize()
	if res.ResolutionMethod != "catalog_hit" {
		t.Fatalf("resolution method = %q, want trimmed", res.ResolutionMethod)
	}
	if res.SUTIDs == nil || res.TouchedPageIDs == nil || res.TouchedAPIIDs == nil {
		t.Fatal("Normalize must allocate empty id slices, not leave nil")
	}
	// Idempotent.
	res.Normalize()
	if len(res.SUTIDs) != 0 || len(res.TouchedPageIDs) != 0 || len(res.TouchedAPIIDs) != 0 {
		t.Fatal("Normalize must be idempotent and keep empty slices empty")
	}
}
