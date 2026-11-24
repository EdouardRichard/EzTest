package executionpg

import (
	"context"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// AssetRegistrationRepository persists the append-only asset_registration fact
// (v0.2.4-F P17, SC-F3). It exposes only Append/List — never Update/Delete
// (append-only fact, constitution art.5.7; the version_fact / sut_registration /
// sut_resolution precedent). Registering the same asset again (new evidence, new
// gate outcome) appends a second row (latest wins); there is no unique owner key.
type AssetRegistrationRepository struct{ base }

var _ execution.AssetRegistrationRepository = (*AssetRegistrationRepository)(nil)

const assetRegistrationColumns = `id, sut_id, asset_kind, asset_id, source,
	evidence_refs, gate_result, created_at, created_by`

// AppendAssetRegistration inserts one registration fact. sut_id/asset_id are
// logical BIGINT references into the resource context (no cross-context FK); the
// application evidence gate validates the proof before appending an approved fact.
// evidence_refs is a JSONB array of content-addressed evidence references.
func (r *AssetRegistrationRepository) AppendAssetRegistration(ctx context.Context, registration execution.AssetRegistration) (execution.AssetRegistration, error) {
	registration.Normalize()
	if err := registration.Validate(); err != nil {
		return execution.AssetRegistration{}, err
	}
	registration.ID = r.insertID(registration.ID)
	refs, err := encodeJSONArray("evidence_refs", registration.EvidenceRefs)
	if err != nil {
		return execution.AssetRegistration{}, err
	}
	row := r.pool.QueryRow(ctx, `
		INSERT INTO asset_registration (id, sut_id, asset_kind, asset_id, source, evidence_refs, gate_result, created_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, COALESCE($8, now()), $9)
		RETURNING created_at
	`, registration.ID, registration.SUTID, registration.AssetKind, registration.AssetID,
		registration.Source, refs, registration.GateResult, nullableTime(registration.CreatedAt), registration.CreatedBy)
	if err := row.Scan(&registration.CreatedAt); err != nil {
		return execution.AssetRegistration{}, wrapDB("append asset registration", err)
	}
	return registration, nil
}

// ListAssetRegistrations returns one SUT's registration facts in append order
// (id ascending). It is the production read path of the fact (the knowledge
// dimension projection and catalog-enrich audit).
func (r *AssetRegistrationRepository) ListAssetRegistrations(ctx context.Context, sutID int64) ([]execution.AssetRegistration, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+assetRegistrationColumns+`
		FROM asset_registration
		WHERE sut_id = $1
		ORDER BY id ASC
	`, sutID)
	if err != nil {
		return nil, wrapDB("list asset registrations", err)
	}
	defer rows.Close()
	items := make([]execution.AssetRegistration, 0)
	for rows.Next() {
		var item execution.AssetRegistration
		var refs []byte
		if err := rows.Scan(&item.ID, &item.SUTID, &item.AssetKind, &item.AssetID,
			&item.Source, &refs, &item.GateResult, &item.CreatedAt, &item.CreatedBy); err != nil {
			return nil, wrapDB("scan asset registration", err)
		}
		if err := decodeJSONObject("evidence_refs", refs, &item.EvidenceRefs); err != nil {
			return nil, err
		}
		item.Normalize()
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDB("list asset registrations", err)
	}
	return items, nil
}
