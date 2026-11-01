package workspaceapp

import (
	"context"
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
}

// NewService wires the todo providers in any order.
func NewService(providers ...Provider) *Service {
	return &Service{providers: providers}
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
	return Snapshot{Items: items, Counts: counts}, nil
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
