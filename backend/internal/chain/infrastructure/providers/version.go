// Package providers adapts the application read surfaces of the version,
// design, execution, delivery and resource contexts onto chain domain nodes
// (spec §8.1). Every adapter calls another context's application port and never
// reads that context's tables; the chain service owns filtering and budgets.
package providers

import (
	"context"
	"sort"
	"strconv"
	"strings"

	chainapp "github.com/EdouardRichard/EzTest/backend/internal/chain/application"
	chain "github.com/EdouardRichard/EzTest/backend/internal/chain/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/pagination"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// nodeKindSUT is the pseudo node kind of the SUT label rows. It is never part
// of the chain graph (the domain has no sut node kind); ChainService only
// consumes the RefID/Label pairs to resolve SUT labels.
const nodeKindSUT chain.NodeKind = "sut"

// RequirementLister is the version application requirement read surface. The
// version requirement service satisfies it.
type RequirementLister interface {
	List(ctx context.Context, versionID int64, status string, limit int, beforeID *int64) ([]versionapp.RequirementView, bool, error)
}

// VersionReader is the version application detail surface used to read the
// version's bound environment and its SUT label rows. The version service
// satisfies it.
type VersionReader interface {
	Get(ctx context.Context, id int64) (versionapp.VersionView, error)
}

// VersionProvider supplies requirement nodes and the bound environment's SUT
// label rows.
type VersionProvider struct {
	requirements RequirementLister
	versions     VersionReader
}

// NewVersionProvider wires the requirement list and the version detail readers.
func NewVersionProvider(requirements RequirementLister, versions VersionReader) *VersionProvider {
	return &VersionProvider{requirements: requirements, versions: versions}
}

var _ chainapp.VersionChainProvider = (*VersionProvider)(nil)

// Requirements lists up to limit version requirement roots. The node keeps the
// requirement status (draft/clarified/closed). v0.2.4-B requirements name their
// subject in prose rather than carrying target SUT ids, so the chain's SUT rows
// come from the version's bound environment (SUTs below), not the requirement.
//
// The version application clamps every list page to
// platform/pagination.MaxLimit, so one List call cannot satisfy a chain root
// budget above 100. The provider follows the List has_more/beforeID cursor until
// it collected limit roots or the dataset is exhausted; BuildChain then sees
// the extra MaxRequirements+1 root and reports requirement_budget truncation.
func (p *VersionProvider) Requirements(ctx context.Context, versionID int64, limit int) ([]chain.Node, error) {
	if limit <= 0 {
		limit = chain.DefaultMaxRequirements
	}
	nodes := make([]chain.Node, 0, limit)
	var beforeID *int64
	for len(nodes) < limit {
		pageSize := limit - len(nodes)
		if pageSize > pagination.MaxLimit {
			pageSize = pagination.MaxLimit
		}
		views, hasMore, err := p.requirements.List(ctx, versionID, "", pageSize, beforeID)
		if err != nil {
			return nil, err
		}
		if len(views) == 0 {
			break
		}
		if len(views) > pageSize {
			views = views[:pageSize]
			hasMore = true
		}
		for _, view := range views {
			if view.ID <= 0 {
				continue
			}
			requirementID := view.ID
			node := chain.Node{
				ID:            chain.NodeID(chain.NodeRequirement, requirementID),
				Kind:          chain.NodeRequirement,
				RefID:         strconv.FormatInt(requirementID, 10),
				Label:         view.Title,
				Status:        view.Status,
				RequirementID: int64Ptr(requirementID),
				CreatedAt:     view.UpdatedAt,
				UpdatedAt:     view.UpdatedAt,
				Meta:          map[string]any{"revision": view.Revision},
			}
			// v0.2.4-B (T8): a requirement no longer carries target SUT ids --
			// it names its subject in prose (requirement_version.sut_description,
			// design v1.3.1 §5.1) -- so a requirement node has no SUTIDs to
			// project. The chain's SUT rows come from the version's bound
			// environment (SUTs below), not from the requirement.
			nodes = append(nodes, node)
		}
		if !hasMore || len(nodes) >= limit {
			break
		}
		lastID := views[len(views)-1].ID
		if lastID <= 0 || (beforeID != nil && lastID >= *beforeID) {
			// A page that does not advance the id cursor would loop forever.
			break
		}
		beforeID = &lastID
	}
	return nodes, nil
}

// SUTs maps the SUTs of the version's bound environment onto label rows keyed
// by SUT id. The chain domain has no sut node, so these rows are only consumed
// by ChainService.mergeSUTNames.
func (p *VersionProvider) SUTs(ctx context.Context, versionID int64) ([]chain.Node, error) {
	view, err := p.versions.Get(ctx, versionID)
	if err != nil {
		return nil, err
	}
	suts := view.Binding.Environment.SUTs
	nodes := make([]chain.Node, 0, len(suts))
	for _, sut := range suts {
		id, parseErr := strconv.ParseInt(sut.ID, 10, 64)
		if parseErr != nil || id <= 0 {
			continue
		}
		nodes = append(nodes, chain.Node{
			ID:     chain.NodeID(nodeKindSUT, id),
			Kind:   nodeKindSUT,
			RefID:  strconv.FormatInt(id, 10),
			Label:  sut.Name,
			Status: "active",
			SUTID:  int64Ptr(id),
			Meta: map[string]any{
				"sut_id":           sut.ID,
				"environment_id":   view.Binding.Environment.ID,
				"environment_name": view.Binding.Environment.Name,
			},
		})
	}
	return nodes, nil
}

// sortUniqueIDs returns the sorted unique positive IDs.
func sortUniqueIDs(ids []int64) []int64 {
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
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// int64Ptr returns a pointer to value.
func int64Ptr(value int64) *int64 {
	return &value
}

// optionalInt64Ptr returns nil for non-positive IDs.
func optionalInt64Ptr(value int64) *int64 {
	if value <= 0 {
		return nil
	}
	return &value
}

// decimalID parses one decimal-string ID of the application views.
func decimalID(raw string) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// setRefOnce fills one typed node ref with the first positive ref id.
func setRefOnce(target **int64, value int64) {
	if *target == nil && value > 0 {
		*target = int64Ptr(value)
	}
}
