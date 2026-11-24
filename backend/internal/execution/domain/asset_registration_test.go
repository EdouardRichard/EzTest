package execution

import (
	"testing"
	"time"
)

func TestAssetRegistrationValidate(t *testing.T) {
	at := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	valid := AssetRegistration{
		ID: 1, SUTID: 7, AssetKind: AssetKindPage, AssetID: 20,
		Source: AssetSourceAI, EvidenceRefs: []string{"evidence:sha256:abc"}, GateResult: "approved",
		CreatedAt: at, CreatedBy: 42,
	}
	cases := []struct {
		name    string
		mutate  func(*AssetRegistration)
		wantErr bool
		field   string
	}{
		{name: "valid ai page", mutate: func(*AssetRegistration) {}},
		{name: "valid api", mutate: func(r *AssetRegistration) { r.AssetKind = AssetKindAPI }},
		{name: "valid db", mutate: func(r *AssetRegistration) { r.AssetKind = AssetKindDB }},
		{name: "admin source no evidence", mutate: func(r *AssetRegistration) {
			r.Source = AssetSourceAdmin
			r.EvidenceRefs = []string{}
		}},
		{name: "missing sut", mutate: func(r *AssetRegistration) { r.SUTID = 0 }, wantErr: true, field: "sut_id"},
		{name: "bad asset kind", mutate: func(r *AssetRegistration) { r.AssetKind = "screen" }, wantErr: true, field: "asset_kind"},
		{name: "empty asset kind", mutate: func(r *AssetRegistration) { r.AssetKind = "" }, wantErr: true, field: "asset_kind"},
		{name: "missing asset id", mutate: func(r *AssetRegistration) { r.AssetID = 0 }, wantErr: true, field: "asset_id"},
		{name: "bad source", mutate: func(r *AssetRegistration) { r.Source = "robot" }, wantErr: true, field: "source"},
		{name: "empty gate result", mutate: func(r *AssetRegistration) { r.GateResult = "" }, wantErr: true, field: "gate_result"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := valid
			tc.mutate(&reg)
			err := reg.Validate()
			if tc.wantErr != (err != nil) {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.field != "" {
				assertFactField(t, err, tc.field)
			}
		})
	}
}

// TestAssetKindVocabulary pins the registered asset kinds the DB CHECK accepts.
func TestAssetKindVocabulary(t *testing.T) {
	if AssetKindPage != "page" || AssetKindAPI != "api" || AssetKindDB != "db" {
		t.Fatalf("asset kind vocabulary = %q/%q/%q", AssetKindPage, AssetKindAPI, AssetKindDB)
	}
	if AssetSourceAdmin != "admin" || AssetSourceAI != "ai" || AssetSourceImport != "import" {
		t.Fatalf("asset source vocabulary = %q/%q/%q", AssetSourceAdmin, AssetSourceAI, AssetSourceImport)
	}
}

func TestAssetRegistrationNormalize(t *testing.T) {
	reg := AssetRegistration{EvidenceRefs: nil}
	reg.Normalize()
	if reg.EvidenceRefs == nil {
		t.Fatal("Normalize must allocate an empty evidence slice, not leave nil")
	}
	reg.Normalize()
	if len(reg.EvidenceRefs) != 0 {
		t.Fatal("Normalize must be idempotent")
	}
}
