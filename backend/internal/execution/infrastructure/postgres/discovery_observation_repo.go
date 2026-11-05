package executionpg

import (
	"context"
	"log/slog"
	"net/url"
	"strings"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
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
		mime = EXCLUDED.mime,
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
