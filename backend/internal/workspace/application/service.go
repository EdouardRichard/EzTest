package workspaceapp

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// TodoItem is the workspace projection of one pending-work item. Its fields are
// exactly the TodoItem contract fields (spec §4.3.2); ID is deliberately not
// exposed, so providers keep their owning rows private and the feed stays
// read-only.
type TodoItem struct {
	Kind         string
	Title        string
	VersionID    int64
	VersionTitle string
	Severity     string
	CreatedAt    time.Time
	ExpiresAt    *time.Time
	URL          string
}

// Counts holds the pre-limit per-kind counts. The zero value is a valid empty
// feed and always contains all six contract keys.
type Counts struct {
	RequirementConfirmation int
	CaseConfirmation        int
	IssueConfirmation       int
	DesignPending           int
	HumanRequest            int
	DeadWorkItem            int
}

// Total returns the sum of all kind counts.
func (c Counts) Total() int {
	return c.RequirementConfirmation + c.CaseConfirmation + c.IssueConfirmation +
		c.DesignPending + c.HumanRequest + c.DeadWorkItem
}

func (c *Counts) add(kind string) {
	switch kind {
	case KindRequirementConfirmation:
		c.RequirementConfirmation++
	case KindCaseConfirmation:
		c.CaseConfirmation++
	case KindIssueConfirmation:
		c.IssueConfirmation++
	case KindDesignPending:
		c.DesignPending++
	case KindHumanRequest:
		c.HumanRequest++
	case KindDeadWorkItem:
		c.DeadWorkItem++
	}
}

// Snapshot is the GET /api/todos payload: items truncated to the requested
// limit and counts measured before the truncation.
type Snapshot struct {
	Items  []TodoItem
	Counts Counts
}

// kindOrder is the contract order of spec §4.3.2. Unknown kinds sort last so a
// future source cannot displace the frozen ordering.
var kindOrder = map[string]int{
	KindHumanRequest:            0,
	KindDesignPending:           1,
	KindIssueConfirmation:       2,
	KindRequirementConfirmation: 3,
	KindCaseConfirmation:        4,
	KindDeadWorkItem:            5,
}

// Service aggregates the todo providers into one read-only snapshot. It owns
// ordering, pre-limit counting, admin-only filtering and limit normalization;
// it never writes and never queries another context's storage directly.
type Service struct {
	providers []Provider
	titles    VersionTitleReader
}

// NewService wires the todo providers in any order.
func NewService(providers ...Provider) *Service {
	return &Service{providers: providers}
}

// WithVersionTitles attaches the version title resolver used to fill the items
// whose provider cannot see another context's version table. A nil resolver
// leaves the titles empty; the production assembly root must wire the version
// QueryService.
func (s *Service) WithVersionTitles(titles VersionTitleReader) *Service {
	if s != nil {
		s.titles = titles
	}
	return s
}

// Snapshot returns the aggregated todo feed for the viewer (spec §4.3.2).
//
// Counts are computed over every provider item before the limit is applied.
// A non-admin viewer never receives dead work items, even if a provider or a
// direct caller bypassed the execution-side admin gate: the workspace service
// enforces the admin-only rule as defense in depth. Provider failures are
// propagated instead of returning a silently partial feed.
func (s *Service) Snapshot(ctx context.Context, viewer platform.Actor, limit int) (Snapshot, error) {
	if s == nil {
		return Snapshot{}, platform.Unavailable("workspace service is not configured")
	}
	limit = normalizeLimit(limit)

	items := make([]TodoItem, 0)
	for _, provider := range s.providers {
		if provider == nil {
			continue
		}
		next, err := provider.Items(ctx, viewer)
		if err != nil {
			return Snapshot{}, err
		}
		items = append(items, next...)
	}

	if !viewer.IsAdmin() {
		filtered := make([]TodoItem, 0, len(items))
		for _, item := range items {
			if item.Kind == KindDeadWorkItem {
				continue
			}
			filtered = append(filtered, item)
		}
		items = filtered
	}

	counts := Counts{}
	for _, item := range items {
		counts.add(item.Kind)
	}

	sort.SliceStable(items, func(left, right int) bool {
		leftOrder, leftKnown := kindOrder[items[left].Kind]
		rightOrder, rightKnown := kindOrder[items[right].Kind]
		if !leftKnown {
			leftOrder = len(kindOrder)
		}
		if !rightKnown {
			rightOrder = len(kindOrder)
		}
		if leftOrder != rightOrder {
			return leftOrder < rightOrder
		}
		return items[left].CreatedAt.After(items[right].CreatedAt)
	})

	if len(items) > limit {
		items = items[:limit]
	}
	if err := s.decorateVersionTitles(ctx, items); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Items: items, Counts: counts}, nil
}

// decorateVersionTitles fills the missing version titles of the visible items
// in one batch read. Only positive version IDs are resolved, so the
// version-less human requests and dead work items are skipped. A reader failure
// is propagated, matching the feed's provider error semantics: a todo feed with
// silently empty titles would hide the regression this decoration fixes.
func (s *Service) decorateVersionTitles(ctx context.Context, items []TodoItem) error {
	if s == nil || s.titles == nil {
		return nil
	}
	ids := missingVersionTitleIDs(items)
	if len(ids) == 0 {
		return nil
	}
	titles, err := s.titles.VersionTitles(ctx, ids)
	if err != nil {
		return fmt.Errorf("resolve version titles: %w", err)
	}
	if len(titles) == 0 {
		return nil
	}
	for index := range items {
		if items[index].VersionTitle != "" || items[index].VersionID <= 0 {
			continue
		}
		if title, found := titles[items[index].VersionID]; found {
			items[index].VersionTitle = title
		}
	}
	return nil
}

// missingVersionTitleIDs returns the deduplicated positive version IDs of the
// items that still need a title, sorted so the single batch read is
// deterministic.
func missingVersionTitleIDs(items []TodoItem) []int64 {
	seen := make(map[int64]struct{})
	ids := make([]int64, 0)
	for _, item := range items {
		if item.VersionTitle != "" || item.VersionID <= 0 {
			continue
		}
		if _, duplicate := seen[item.VersionID]; duplicate {
			continue
		}
		seen[item.VersionID] = struct{}{}
		ids = append(ids, item.VersionID)
	}
	sort.Slice(ids, func(left, right int) bool { return ids[left] < ids[right] })
	return ids
}

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return DefaultLimit
	}
	if limit > MaxLimit {
		return MaxLimit
	}
	return limit
}
