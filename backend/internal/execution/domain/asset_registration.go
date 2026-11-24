package execution

import (
	"context"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// AssetKind is the registered asset vocabulary of an asset_registration fact
// (SC-F3). It matches the DB CHECK chk_asset_registration_asset_kind.
const (
	AssetKindPage = "page"
	AssetKindAPI  = "api"
	AssetKindDB   = "db"
)

// Asset registration source vocabulary mirrors sut_registration.source: the
// admin path needs no AI evidence, the ai path is the Agent exploration proposal.
const (
	AssetSourceAdmin  = "admin"
	AssetSourceAI     = "ai"
	AssetSourceImport = "import"
)

// AssetRegistration is one append-only fact recording an asset (page/api/db) the
// execution team explored, proved and registered for a SUT (v0.2.4-F P17,
// SC-F3). Rows are only ever inserted, never rewritten or deleted; registering
// the same asset again (new evidence, new gate outcome) appends a second row
// (constitution 五.7).
//
// The evidence gate (evidence exists + sha256 + linked step assertion passed) is
// enforced by the P17 registration service before it appends an approved fact;
// an unproven discovery is never registered. EvidenceRefs carries the
// content-addressed evidence references (same shape as
// sut_registration.evidence_refs); GateResult is the free-text gate outcome
// (approved/rejected/...), owned by the P17 service. SUTID and AssetID are
// logical BIGINT references into the resource context (no cross-context FK).
type AssetRegistration struct {
	ID           int64
	SUTID        int64
	AssetKind    string
	AssetID      int64
	Source       string
	EvidenceRefs []string
	GateResult   string
	CreatedAt    time.Time
	CreatedBy    int64
}

// MaxAssetGateResultRunes caps the gate_result label.
const MaxAssetGateResultRunes = 100

// Normalize installs the empty evidence slice a nil Go value cannot express (the
// DB column defaults to '[]'). It is idempotent.
func (r *AssetRegistration) Normalize() {
	if r.EvidenceRefs == nil {
		r.EvidenceRefs = []string{}
	}
}

// Validate checks one registration fact before it is appended. The evidence
// gate itself (presence + sha256 + linked assertion) is the P17 service's
// responsibility; this validates the fact's persisted shape.
func (r AssetRegistration) Validate() error {
	if r.SUTID <= 0 {
		return platform.Invalid("sut_id", "must be a positive decimal ID")
	}
	switch r.AssetKind {
	case AssetKindPage, AssetKindAPI, AssetKindDB:
	default:
		return platform.Invalid("asset_kind", "must be one of page, api, db")
	}
	if r.AssetID <= 0 {
		return platform.Invalid("asset_id", "must be a positive decimal ID")
	}
	switch r.Source {
	case AssetSourceAdmin, AssetSourceAI, AssetSourceImport:
	default:
		return platform.Invalid("source", "must be one of admin, ai, import")
	}
	if err := validation.String("gate_result", r.GateResult, 1, MaxAssetGateResultRunes); err != nil {
		return err
	}
	return nil
}

// AssetRegistrationRepository persists the append-only asset_registration fact
// store. It exposes only Append/List — never Update/Delete (append-only fact,
// constitution 五.7). ListAssetRegistrations returns one SUT's facts in append
// order (id ascending).
type AssetRegistrationRepository interface {
	AppendAssetRegistration(ctx context.Context, registration AssetRegistration) (AssetRegistration, error)
	ListAssetRegistrations(ctx context.Context, sutID int64) ([]AssetRegistration, error)
}
