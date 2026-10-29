package application

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"
)

// Item is one todo-centre entry (spec §6.3). Every id crosses the JSON boundary
// as a decimal string (D29).
type Item struct {
	Kind        string    `json:"kind"`
	Title       string    `json:"title"`
	VersionID   string    `json:"versionId"`
	VersionName string    `json:"versionName"`
	Severity    string    `json:"severity"`
	CreatedAt   time.Time `json:"createdAt"`
	URL         string    `json:"url"`
}

// Counts holds the per-group totals before the limit is applied (spec §6.3).
type Counts struct {
	Review   int `json:"review"`
	Takeover int `json:"takeover"`
	Gate     int `json:"gate"`
}

// Snapshot is the todo-centre read model. Items is always a non-nil slice so the
// JSON envelope renders `[]`, never `null`, for an empty centre.
type Snapshot struct {
	Items  []Item `json:"items"`
	Counts Counts `json:"counts"`
}

// Query is the todo-centre read request. Admin is resolved at the HTTP boundary
// from the authenticated identity; a regular user only receives the review
// group and never reaches the execution provider (spec §6.3/§11.5).
type Query struct {
	Limit int
	Admin bool
}

// Service aggregates the provider snapshots into the todo read model. It holds
// no state and performs no caching: every List call is independent.
type Service struct {
	providers Providers
}

// NewService builds the aggregation service from the todo providers.
func NewService(providers Providers) *Service {
	return &Service{providers: providers}
}

// List assembles the todo centre. A zero/negative limit defaults to 50; values
// above 200 are clamped. Provider failures abort the aggregation so the HTTP
// layer can answer the shared 500 envelope instead of serving partial data.
func (s *Service) List(ctx context.Context, q Query) (Snapshot, error) {
	if s == nil {
		return Snapshot{}, errors.New("workspace: service is not configured")
	}
	providers := s.providers
	if providers.Review == nil {
		return Snapshot{}, errors.New("workspace: review provider is not configured")
	}

	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}

	reviewRows, err := providers.Review.ReviewingVersions(ctx)
	if err != nil {
		return Snapshot{}, fmt.Errorf("workspace: load reviews: %w", err)
	}

	var pendingRows []PendingRow
	if q.Admin && providers.Execution != nil {
		pendingRows, err = providers.Execution.PendingTakeovers(ctx)
		if err != nil {
			return Snapshot{}, fmt.Errorf("workspace: load takeovers: %w", err)
		}
	}

	snapshot := Snapshot{Items: make([]Item, 0, len(reviewRows)+len(pendingRows))}

	seenVersions := make(map[int64]struct{}, len(reviewRows))
	for _, row := range reviewRows {
		if _, duplicate := seenVersions[row.VersionID]; duplicate {
			continue
		}
		seenVersions[row.VersionID] = struct{}{}
		snapshot.Counts.Review++
		snapshot.Items = append(snapshot.Items, Item{
			Kind:        KindReview,
			Title:       row.VersionName,
			VersionID:   strconv.FormatInt(row.VersionID, 10),
			VersionName: row.VersionName,
			Severity:    SeverityInfo,
			CreatedAt:   row.ReviewingAt,
			URL:         versionURL(row.VersionID, "mindmap"),
		})
	}

	seenRuns := make(map[int64]struct{}, len(pendingRows))
	for _, row := range pendingRows {
		if _, duplicate := seenRuns[row.CaseRunID]; duplicate {
			continue
		}
		seenRuns[row.CaseRunID] = struct{}{}

		kind := KindGate
		severity := SeverityMajor
		if row.TakeoverID != nil {
			kind = KindTakeover
			severity = SeverityWarning
		}
		switch kind {
		case KindTakeover:
			snapshot.Counts.Takeover++
		default:
			snapshot.Counts.Gate++
		}

		snapshot.Items = append(snapshot.Items, Item{
			Kind:        kind,
			Title:       pendingTitle(row, kind),
			VersionID:   strconv.FormatInt(row.VersionID, 10),
			VersionName: row.VersionName,
			Severity:    severity,
			CreatedAt:   row.CreatedAt,
			URL:         versionURL(row.VersionID, "sessions"),
		})
	}

	sortItems(snapshot.Items)
	if len(snapshot.Items) > limit {
		snapshot.Items = snapshot.Items[:limit]
	}
	return snapshot, nil
}

// sortItems orders the groups review → takeover → gate and, within a group, the
// newest entry first. The id tie-break keeps the order deterministic across
// repeated reads.
func sortItems(items []Item) {
	rank := func(kind string) int {
		switch kind {
		case KindReview:
			return 0
		case KindTakeover:
			return 1
		default:
			return 2
		}
	}
	sort.SliceStable(items, func(left, right int) bool {
		leftRank, rightRank := rank(items[left].Kind), rank(items[right].Kind)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		if !items[left].CreatedAt.Equal(items[right].CreatedAt) {
			return items[left].CreatedAt.After(items[right].CreatedAt)
		}
		if items[left].VersionID != items[right].VersionID {
			return items[left].VersionID > items[right].VersionID
		}
		return items[left].URL < items[right].URL
	})
}

// pendingTitle is the displayed case title; an empty snapshot title falls back
// to a stable group label so an item is never rendered blank.
func pendingTitle(row PendingRow, kind string) string {
	if row.CaseTitle != "" {
		return row.CaseTitle
	}
	if kind == KindTakeover {
		return "待人工接管的用例"
	}
	return "待升级的用例"
}

// versionURL is the deep link of one todo item: review opens the mindmap tab,
// execution items open the execution sessions tab (spec §6.3).
func versionURL(versionID int64, tab string) string {
	return "/versions/" + strconv.FormatInt(versionID, 10) + "?tab=" + tab
}
