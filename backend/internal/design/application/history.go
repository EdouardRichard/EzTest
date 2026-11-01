package designapp

import (
	"context"
)

// CaseVersionHistoryView is one case-version row read by ID regardless of its
// current/confirmation status (spec §8.1). Title is never empty because the
// row keeps the historical title; Status may be draft/confirmed/stale/
// superseded. Revision is int32 to match the wire/report projection.
type CaseVersionHistoryView struct {
	ID       int64
	Revision int32
	Title    string
	Status   string
}

// ReadCaseVersionsByIDs is the historical case-version reader of the
// exploration chain. Unlike ListExecutable it does not require the version to
// be the case's current version nor to be confirmed, so superseded rows keep
// their title. Every row must belong to versionID: a foreign, missing or
// non-positive ID is simply skipped. The method does not change the
// current+confirmed semantics of the P3 execution readers.
func (s *GraphService) ReadCaseVersionsByIDs(ctx context.Context, versionID int64, caseVersionIDs []int64) ([]CaseVersionHistoryView, error) {
	if err := requirePositiveID("version_id", versionID); err != nil {
		return nil, err
	}
	ids := uniquePositiveIDs(caseVersionIDs)
	if len(ids) == 0 {
		return []CaseVersionHistoryView{}, nil
	}
	rows, err := s.repo.ReadCaseVersionsByIDs(ctx, versionID, ids)
	if err != nil {
		return nil, mapDesignError(err, "version")
	}
	views := make([]CaseVersionHistoryView, 0, len(rows))
	for _, row := range rows {
		if row.ID <= 0 || row.Title == "" {
			// A malformed/historical row without a title would surface as an
			// anonymous node in the chain; skip it instead of leaking it.
			continue
		}
		views = append(views, CaseVersionHistoryView{
			ID:       row.ID,
			Revision: int32(row.Revision),
			Title:    row.Title,
			Status:   row.Status,
		})
	}
	return views, nil
}

// uniquePositiveIDs dedupes the requested case-version IDs, drops non-positive
// values and preserves the caller order. The repository query is version
// scoped, so authorization never depends on this filter.
func uniquePositiveIDs(ids []int64) []int64 {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[int64]bool, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}
