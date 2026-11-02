package visualpg

import (
	"context"

	visualapp "github.com/EdouardRichard/EzTest/backend/internal/visual/application"
	visual "github.com/EdouardRichard/EzTest/backend/internal/visual/domain"
)

// Overview implements visualapp.DiffRepository.Overview: the baseline coverage,
// four-state counts, inconclusive reasons, diff-ratio samples and recent
// failures of one version or every version.
func (r *DiffRepository) Overview(ctx context.Context, versionID *int64) (visualapp.OverviewData, error) {
	ctx = contextOrBackground(ctx)
	data := visualapp.OverviewData{DiffCounts: map[visual.Classification]int{}}
	filter, args := versionFilter(versionID)

	if err := r.pool.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE EXISTS (
		           SELECT 1 FROM visual_baseline b
		           WHERE b.visual_assertion_id = a.id AND b.status = 'approved'
		       ))
		FROM visual_assertion a
		WHERE a.status = 'active'`+filter, args...).Scan(&data.AssertionTotal, &data.BaselineCovered); err != nil {
		return visualapp.OverviewData{}, wrapDB("overview assertion counts", err)
	}

	rows, err := r.pool.Query(ctx, `
		SELECT d.classification, count(*)
		FROM visual_diff d JOIN visual_assertion a ON a.id = d.visual_assertion_id
		WHERE true`+filter+`
		GROUP BY d.classification`, args...)
	if err != nil {
		return visualapp.OverviewData{}, wrapDB("overview classification counts", err)
	}
	for rows.Next() {
		var classification string
		var count int
		if scanErr := rows.Scan(&classification, &count); scanErr != nil {
			rows.Close()
			return visualapp.OverviewData{}, wrapDB("scan classification count", scanErr)
		}
		data.DiffCounts[visual.Classification(classification)] = count
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return visualapp.OverviewData{}, wrapDB("overview classification counts", err)
	}
	rows.Close()

	reasonRows, err := r.pool.Query(ctx, `
		SELECT d.reason, count(*)
		FROM visual_diff d JOIN visual_assertion a ON a.id = d.visual_assertion_id
		WHERE d.classification = 'inconclusive'`+filter+`
		GROUP BY d.reason ORDER BY count(*) DESC, d.reason LIMIT 20`, args...)
	if err != nil {
		return visualapp.OverviewData{}, wrapDB("overview inconclusive reasons", err)
	}
	for reasonRows.Next() {
		var reason visualapp.ReasonCount
		if scanErr := reasonRows.Scan(&reason.Reason, &reason.Count); scanErr != nil {
			reasonRows.Close()
			return visualapp.OverviewData{}, wrapDB("scan inconclusive reason", scanErr)
		}
		data.InconclusiveReasons = append(data.InconclusiveReasons, reason)
	}
	reasonRows.Close()

	ratioRows, err := r.pool.Query(ctx, `
		SELECT d.diff_ratio::float8
		FROM visual_diff d JOIN visual_assertion a ON a.id = d.visual_assertion_id
		WHERE d.baseline_id IS NOT NULL`+filter+`
		ORDER BY d.id DESC LIMIT 5000`, args...)
	if err != nil {
		return visualapp.OverviewData{}, wrapDB("overview diff ratios", err)
	}
	for ratioRows.Next() {
		var ratio float64
		if scanErr := ratioRows.Scan(&ratio); scanErr != nil {
			ratioRows.Close()
			return visualapp.OverviewData{}, wrapDB("scan diff ratio", scanErr)
		}
		data.DiffRatios = append(data.DiffRatios, ratio)
	}
	ratioRows.Close()

	failureRows, err := r.pool.Query(ctx, `
		SELECT `+diffColumns+`
		FROM visual_diff d JOIN visual_assertion a ON a.id = d.visual_assertion_id
		WHERE d.classification = 'failed'`+filter+`
		ORDER BY d.id DESC LIMIT 10`, args...)
	if err != nil {
		return visualapp.OverviewData{}, wrapDB("overview recent failures", err)
	}
	defer failureRows.Close()
	for failureRows.Next() {
		diff, scanErr := scanDiff(failureRows)
		if scanErr != nil {
			return visualapp.OverviewData{}, wrapDB("scan recent failure", scanErr)
		}
		data.RecentFailures = append(data.RecentFailures, diff)
	}
	return data, failureRows.Err()
}

func versionFilter(versionID *int64) (string, []any) {
	if versionID == nil || *versionID <= 0 {
		return "", nil
	}
	return " AND a.version_id = $1", []any{*versionID}
}
