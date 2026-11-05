package executionpg

import (
	"context"
	"log/slog"
	"net/url"
	"strings"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DiscoveryObservationRepository implements executionapp.DiscoveryObservationStore.
type DiscoveryObservationRepository struct{ base }

// discoveryObservationUpsert writes only the DB-29 columns. The natural key
// dedupe is the ON CONFLICT clause; query/header/cookie/body have no column at
// all. version_id is resolved from run_wave when the observation does not carry
// it and environment_id from version_sut; a row with no resolvable version is
// skipped (best effort) and logged, never a panic.
const discoveryObservationUpsert = `
	INSERT INTO execution_discovery_observation
		(id, version_id, wave_id, case_run_id, version_sut_id, environment_id,
		 kind, method, host, resource_template, status_code, mime,
		 source_kind, occurrences, decision)
	SELECT $1,
	       COALESCE(NULLIF($2::BIGINT, 0), wave.version_id),
	       NULLIF($3::BIGINT, 0),
	       NULLIF($4::BIGINT, 0),
	       $5::BIGINT,
	       COALESCE(NULLIF($6::BIGINT, 0), binding.environment_id),
	       $7, $8, $9, $10, $11, $12,
	       'execution', $13, 'new'
	FROM (SELECT 1) AS anchor
	LEFT JOIN run_wave wave ON wave.id = NULLIF($3::BIGINT, 0)
	LEFT JOIN version_sut binding ON binding.id = $5::BIGINT
	WHERE COALESCE(NULLIF($2::BIGINT, 0), wave.version_id) IS NOT NULL
	ON CONFLICT (version_id, version_sut_id, kind, method, resource_template)
	DO UPDATE SET
		occurrences = execution_discovery_observation.occurrences + EXCLUDED.occurrences,
		last_seen_at = now(),
		mime = COALESCE(NULLIF(EXCLUDED.mime, ''), execution_discovery_observation.mime),
		status_code = COALESCE(EXCLUDED.status_code, execution_discovery_observation.status_code),
		updated_at = now()
`

// UpsertObservations writes one normalized batch. Each row is idempotent on the
// DB-29 unique key and the caller's occurrences are added to the stored count.
func (r *DiscoveryObservationRepository) UpsertObservations(ctx context.Context, observations []executionapp.DiscoveryObservation) error {
	ctx = contextOrBackground(ctx)
	for _, observation := range observations {
		if observation.VersionSUTID <= 0 || observation.ResourceTemplate == "" {
			slog.Warn("execution discovery: skip observation without identity",
				"version_sut_id", observation.VersionSUTID,
				"kind", observation.Kind, "template", observation.ResourceTemplate)
			continue
		}
		occurrences := observation.Occurrences
		if occurrences <= 0 {
			occurrences = 1
		}
		tag, err := r.pool.Exec(ctx, discoveryObservationUpsert,
			r.insertID(0),
			observation.VersionID,
			observation.WaveID,
			observation.CaseRunID,
			observation.VersionSUTID,
			observation.EnvironmentID,
			observation.Kind,
			observation.Method,
			observation.Host,
			observation.ResourceTemplate,
			observation.StatusCode,
			observation.MIME,
			occurrences,
		)
		if err != nil {
			return wrapDB("upsert discovery observation", err)
		}
		if tag.RowsAffected() == 0 {
			slog.Warn("execution discovery: skip observation without a resolvable version",
				"version_id", observation.VersionID, "wave_id", observation.WaveID,
				"version_sut_id", observation.VersionSUTID,
				"kind", observation.Kind, "template", observation.ResourceTemplate)
		}
	}
	return nil
}

// discoveryObservationDeltaQuery returns the rows the version-context delta
// evaluator reads (W1-12). A zero wave widens the query to the whole version.
// Rows first seen in the wave plus any still-unclassified ("new") backlog row
// are returned, so a retried/skipped delta pass backfills without re-emitting an
// already classified resource.
const discoveryObservationDeltaQuery = `
	SELECT id, version_id, COALESCE(wave_id, 0), COALESCE(case_run_id, 0),
	       version_sut_id, COALESCE(environment_id, 0), kind, method, host,
	       resource_template, decision, COALESCE(resource_id, 0), COALESCE(fact_id, 0)
	FROM execution_discovery_observation
	WHERE version_id = $1
	  AND ($2 = 0 OR wave_id = $2 OR decision = 'new')
	ORDER BY id ASC
`

// ListForDelta reads the DB-29 observations of one wave for the version
// context. It implements the execution side of the cross-context read port;
// version never touches this table directly (Agent.md §6.2).
func (r *DiscoveryObservationRepository) ListForDelta(ctx context.Context, versionID, waveID int64) ([]executionapp.DiscoveryDeltaObservation, error) {
	ctx = contextOrBackground(ctx)
	rows, err := r.pool.Query(ctx, discoveryObservationDeltaQuery, versionID, waveID)
	if err != nil {
		return nil, wrapDB("list discovery observations for delta", err)
	}
	defer rows.Close()

	observations := make([]executionapp.DiscoveryDeltaObservation, 0)
	for rows.Next() {
		var observation executionapp.DiscoveryDeltaObservation
		if err := rows.Scan(&observation.ObservationID, &observation.VersionID, &observation.WaveID,
			&observation.CaseRunID, &observation.VersionSUTID, &observation.EnvironmentID,
			&observation.Kind, &observation.Method, &observation.Host, &observation.ResourceTemplate,
			&observation.Decision, &observation.ResourceID, &observation.FactID); err != nil {
			return nil, wrapDB("scan discovery observation for delta", err)
		}
		observations = append(observations, observation)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDB("read discovery observations for delta", err)
	}
	return observations, nil
}

// MarkDecision persists the delta classification of one observation. A zero
// resource/fact id keeps the previously stored value, so marking a resource
// rejected later never erases its proposal provenance. It is idempotent.
const discoveryObservationDecisionUpdate = `
	UPDATE execution_discovery_observation
	SET decision = $2,
	    resource_id = CASE WHEN $3::BIGINT > 0 THEN $3::BIGINT ELSE resource_id END,
	    fact_id = CASE WHEN $4::BIGINT > 0 THEN $4::BIGINT ELSE fact_id END,
	    updated_at = now()
	WHERE id = $1
`

// MarkDiscoveryObservationDecision implements the execution side of the
// cross-context decision write port.
func (r *DiscoveryObservationRepository) MarkDiscoveryObservationDecision(ctx context.Context, observationID int64, decision string, resourceID, factID int64) error {
	ctx = contextOrBackground(ctx)
	if observationID <= 0 {
		return execution.ErrNotFound
	}
	if _, err := r.pool.Exec(ctx, discoveryObservationDecisionUpdate, observationID, decision, resourceID, factID); err != nil {
		return wrapDB("mark discovery observation decision", err)
	}
	return nil
}

// discoveryObservationResourceDecisionUpdate is the W1-14B write-back from the
// resource catalog: an admin confirm/reject of a discovered resource settles
// every observation of that resource that is still `proposed`. A row already
// settled (approved/rejected/out_of_scope) is never rewritten, so a replayed or
// duplicated resource event cannot flip an earlier decision.
const discoveryObservationResourceDecisionUpdate = `
	UPDATE execution_discovery_observation
	SET decision = $2,
	    updated_at = now()
	WHERE resource_id = $1
	  AND decision = 'proposed'
`

// MarkDecisionByResource implements executionapp.ResourceDecisionMarker. It
// returns the number of observations the decision update touched.
func (r *DiscoveryObservationRepository) MarkDecisionByResource(ctx context.Context, resourceID int64, decision string) (int64, error) {
	ctx = contextOrBackground(ctx)
	if resourceID <= 0 {
		return 0, execution.ErrNotFound
	}
	tag, err := r.pool.Exec(ctx, discoveryObservationResourceDecisionUpdate, resourceID, decision)
	if err != nil {
		return 0, wrapDB("mark discovery observation decision by resource", err)
	}
	return tag.RowsAffected(), nil
}

// discoveryObservationListQuery is the W1-14A read (API-36/37/39). A NULL wave
// filter widens the query to the whole version; an empty kind/decision means
// every value. LIMIT NULL is PostgreSQL's "no limit", which the coverage
// aggregation uses (it must count the whole version, not one page).
// source_kind/created_at/updated_at are part of the read projection; the
// fingerprint is deliberately not exposed.
const discoveryObservationListQuery = `
	SELECT id, version_sut_id, environment_id, wave_id, case_run_id, kind, method,
	       host, resource_template, status_code, mime, source_kind, occurrences,
	       decision, resource_id, fact_id, first_seen_at, last_seen_at,
	       created_at, updated_at
	FROM execution_discovery_observation
	WHERE version_id = $1
	  AND ($2::BIGINT IS NULL OR wave_id = $2)
	  AND ($3 = '' OR kind = $3)
	  AND ($4 = '' OR decision = $4)
	ORDER BY first_seen_at DESC, id DESC
	LIMIT $5
`

// ListByVersion reads the DB-29 projection of one version. It implements
// executionapp.DiscoveryQueryRepository and never writes.
func (r *DiscoveryObservationRepository) ListByVersion(ctx context.Context, versionID int64, waveID *int64, kind, decision string, limit int) ([]executionapp.DiscoveryObservationRow, error) {
	ctx = contextOrBackground(ctx)
	var limitArg *int
	if limit > 0 {
		limitArg = &limit
	}
	rows, err := r.pool.Query(ctx, discoveryObservationListQuery, versionID, waveID, kind, decision, limitArg)
	if err != nil {
		return nil, wrapDB("list discovery observations", err)
	}
	defer rows.Close()

	observations := make([]executionapp.DiscoveryObservationRow, 0)
	for rows.Next() {
		var observation executionapp.DiscoveryObservationRow
		if err := rows.Scan(&observation.ID, &observation.VersionSUTID, &observation.EnvironmentID,
			&observation.WaveID, &observation.CaseRunID, &observation.Kind, &observation.Method,
			&observation.Host, &observation.ResourceTemplate, &observation.StatusCode,
			&observation.MIME, &observation.SourceKind, &observation.Occurrences, &observation.Decision,
			&observation.ResourceID, &observation.FactID,
			&observation.FirstSeenAt, &observation.LastSeenAt,
			&observation.CreatedAt, &observation.UpdatedAt); err != nil {
			return nil, wrapDB("scan discovery observation", err)
		}
		observations = append(observations, observation)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDB("read discovery observations", err)
	}
	return observations, nil
}

// DiscoverySUTHostResolver resolves the allowed hosts of one version_sut
// binding: the SUT environment base URL host plus the hosts of its registered
// pages. It implements executionapp.SUTHostResolver.
type DiscoverySUTHostResolver struct{ pool *pgxpool.Pool }

// NewDiscoverySUTHostResolver wires the resolver over the shared pool.
func NewDiscoverySUTHostResolver(pool *pgxpool.Pool) *DiscoverySUTHostResolver {
	return &DiscoverySUTHostResolver{pool: pool}
}

// AllowedHosts returns the lowercased host set of the binding. The caller treats
// an empty set/error as "no filtering" (the capture is best effort).
func (r *DiscoverySUTHostResolver) AllowedHosts(ctx context.Context, versionSUTID int64) (map[string]bool, error) {
	ctx = contextOrBackground(ctx)
	rows, err := r.pool.Query(ctx, `
		SELECT environment.base_url, page.url
		FROM version_sut binding
		JOIN sut_environment environment ON environment.id = binding.environment_id
		LEFT JOIN sut_page page ON page.environment_id = binding.environment_id
		WHERE binding.id = $1
	`, versionSUTID)
	if err != nil {
		return nil, wrapDB("resolve version_sut hosts", err)
	}
	defer rows.Close()
	hosts := make(map[string]bool)
	for rows.Next() {
		var baseURL string
		var pageURL *string
		if err := rows.Scan(&baseURL, &pageURL); err != nil {
			return nil, wrapDB("scan version_sut host", err)
		}
		addDiscoveryHost(hosts, baseURL)
		if pageURL != nil {
			addDiscoveryHost(hosts, *pageURL)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDB("read version_sut hosts", err)
	}
	return hosts, nil
}

func addDiscoveryHost(hosts map[string]bool, rawURL string) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return
	}
	if host := strings.ToLower(parsed.Hostname()); host != "" {
		hosts[host] = true
	}
}
