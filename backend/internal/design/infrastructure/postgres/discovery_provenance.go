package designpg

// W2-11 cross-context read projection: the discovery revision Critic needs the
// execution observation chain (observation -> case_run -> case_version ->
// case_source requirement), the approved resource catalog row and the
// duplicate-target lookup. These are read-only logical references (Agent.md
// §6.9.7); no execution package is imported.

import (
	"context"
	"errors"
	"fmt"

	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RevisionStore is the design PostgreSQL implementation of the W2-11
// discovery revision read port.
type RevisionStore struct {
	pool *pgxpool.Pool
}

var _ designapp.DiscoveryRevisionStore = (*RevisionStore)(nil)

// NewRevisionStore wraps the shared pool.
func NewRevisionStore(pool *pgxpool.Pool) *RevisionStore {
	return &RevisionStore{pool: pool}
}

// discoveryProvenanceSelect is the neutral projection of the observation chain.
// The requirement link is the case_source row of the executed case version; a
// version without a requirement source keeps requirement_id 0 so the Critic
// treats it as low confidence (spec R-EX-6 "requirement 关联缺失").
const discoveryProvenanceSelect = `
	SELECT o.version_id, o.id, COALESCE(o.resource_id, 0), o.kind,
	       COALESCE(o.case_run_id, 0), COALESCE(cr.case_version_id, 0),
	       COALESCE(req.requirement_id, 0), COALESCE(req.requirement_version_id, 0),
	       o.version_sut_id, COALESCE(o.fact_id, 0),
	       (SELECT count(*) FROM evidence e WHERE e.case_run_id = o.case_run_id)
	FROM execution_discovery_observation o
	LEFT JOIN case_run cr ON cr.id = o.case_run_id
	LEFT JOIN LATERAL (
		SELECT rv.requirement_id, rv.id AS requirement_version_id
		FROM case_source src
		JOIN requirement_version rv ON rv.id::text = src.ref_id
		WHERE src.case_version_id = cr.case_version_id
		  AND src.kind = 'requirement'
		ORDER BY src.id
		LIMIT 1
	) req ON TRUE
`

// ReadProvenanceByResource returns every observation of one approved catalog
// resource ordered by observation id (the observer processes one per version).
func (s *RevisionStore) ReadProvenanceByResource(ctx context.Context, resourceID int64, kind string) ([]designapp.DiscoveryProvenance, error) {
	rows, err := s.pool.Query(ctx, discoveryProvenanceSelect+`
		WHERE o.resource_id = $1 AND o.kind = $2
		ORDER BY o.id
	`, resourceID, kind)
	if err != nil {
		return nil, fmt.Errorf("read discovery provenance for resource: %w", err)
	}
	defer rows.Close()
	provenances := make([]designapp.DiscoveryProvenance, 0)
	for rows.Next() {
		provenance, err := scanDiscoveryProvenance(rows)
		if err != nil {
			return nil, err
		}
		provenances = append(provenances, provenance)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read discovery provenance for resource: %w", err)
	}
	return provenances, nil
}

// ReadProvenanceByObservation returns one observation-chain row.
func (s *RevisionStore) ReadProvenanceByObservation(ctx context.Context, versionID, observationID int64) (designapp.DiscoveryProvenance, error) {
	rows, err := s.pool.Query(ctx, discoveryProvenanceSelect+`
		WHERE o.version_id = $1 AND o.id = $2
	`, versionID, observationID)
	if err != nil {
		return designapp.DiscoveryProvenance{}, fmt.Errorf("read discovery provenance: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return designapp.DiscoveryProvenance{}, fmt.Errorf("read discovery provenance: %w", err)
		}
		return designapp.DiscoveryProvenance{}, design.ErrNotFound
	}
	provenance, err := scanDiscoveryProvenance(rows)
	if err != nil {
		return designapp.DiscoveryProvenance{}, err
	}
	return provenance, nil
}

func scanDiscoveryProvenance(row pgx.Row) (designapp.DiscoveryProvenance, error) {
	var provenance designapp.DiscoveryProvenance
	if err := row.Scan(
		&provenance.VersionID, &provenance.ObservationID, &provenance.ResourceID, &provenance.Kind,
		&provenance.CaseRunID, &provenance.CaseVersionID,
		&provenance.RequirementID, &provenance.RequirementVersionID,
		&provenance.VersionSUTID, &provenance.FactID, &provenance.EvidenceRows,
	); err != nil {
		return designapp.DiscoveryProvenance{}, fmt.Errorf("scan discovery provenance: %w", err)
	}
	return provenance, nil
}

// ApprovedResource returns the approved catalog row of one discovered target.
// A missing or non-approved row reports found=false (Critic predicate ①).
func (s *RevisionStore) ApprovedResource(ctx context.Context, resourceID int64, kind string) (designapp.DiscoveryApprovedResource, bool, error) {
	resource := designapp.DiscoveryApprovedResource{ID: resourceID, Kind: kind}
	var query string
	switch kind {
	case "page":
		query = `
			SELECT status, route_template, COALESCE(name, ''), ''
			FROM sut_page WHERE id = $1
		`
	case "api":
		query = `
			SELECT status, path_template, COALESCE(name, ''), method
			FROM sut_api WHERE id = $1
		`
	default:
		return designapp.DiscoveryApprovedResource{}, false, nil
	}
	var status, template, name, method string
	err := s.pool.QueryRow(ctx, query, resourceID).Scan(&status, &template, &name, &method)
	if errors.Is(err, pgx.ErrNoRows) {
		return designapp.DiscoveryApprovedResource{}, false, nil
	}
	if err != nil {
		return designapp.DiscoveryApprovedResource{}, false, fmt.Errorf("read approved discovery resource: %w", err)
	}
	resource.Status = status
	resource.Template = template
	resource.Method = method
	resource.Label = name
	if kind == "api" {
		resource.Label = apiTargetLabel(method, template)
	}
	return resource, true, nil
}

// FindConfirmedCaseByContent implements Critic predicate ④: a confirmed case
// of the same requirement with the same target and content hash makes the
// additive revision a duplicate, so it must stay a draft.
func (s *RevisionStore) FindConfirmedCaseByContent(ctx context.Context, versionID, requirementID int64, contentHash string, pageID, apiID int64) (int64, bool, error) {
	var caseVersionID int64
	err := s.pool.QueryRow(ctx, `
		SELECT cv.id
		FROM case_version cv
		JOIN case_entity ce ON ce.id = cv.case_id
		JOIN case_scenario s ON s.id = ce.scenario_id
		WHERE s.version_id = $1
		  AND s.requirement_id = $2
		  AND cv.status = 'confirmed'
		  AND cv.content_hash = $3
		  AND (
		      ($4::bigint > 0 AND cv.page_ids @> to_jsonb($4::bigint))
		      OR ($5::bigint > 0 AND cv.api_ids @> to_jsonb($5::bigint))
		  )
		ORDER BY cv.id
		LIMIT 1
	`, versionID, requirementID, contentHash, pageID, apiID).Scan(&caseVersionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("find duplicate confirmed discovery case: %w", err)
	}
	return caseVersionID, true, nil
}

func apiTargetLabel(method, template string) string {
	if method == "" {
		return template
	}
	return method + " " + template
}
