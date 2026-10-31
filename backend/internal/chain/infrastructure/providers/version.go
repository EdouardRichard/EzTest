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
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// nodeKindVersionSUT is the pseudo node kind of the version-SUT label rows. It
// is never part of the chain graph (the domain has no version_sut node kind);
// ChainService only consumes the RefID/Label pairs to resolve SUT labels.
const nodeKindVersionSUT chain.NodeKind = "version_sut"

// RequirementLister is the version application requirement read surface. The
// version requirement service satisfies it.
type RequirementLister interface {
	List(ctx context.Context, versionID int64, status string, limit int, beforeID *int64) ([]versionapp.RequirementView, bool, error)
}

// VersionReader is the version application detail surface used to resolve the
// version-SUT bindings (and, transitively, the resource labels). The version
// service satisfies it.
type VersionReader interface {
	Get(ctx context.Context, id int64) (versionapp.VersionView, error)
}

// VersionProvider supplies requirement nodes and the version-SUT label rows.
type VersionProvider struct {
	requirements RequirementLister
	versions     VersionReader
}

// NewVersionProvider wires the requirement list and the version detail readers.
func NewVersionProvider(requirements RequirementLister, versions VersionReader) *VersionProvider {
	return &VersionProvider{requirements: requirements, versions: versions}
}

var _ chainapp.VersionChainProvider = (*VersionProvider)(nil)

// Requirements lists one limited page of version requirements. The node keeps
// the requirement status (draft/clarified/closed) and its target version SUTs,
// which make the SUT filter match requirement roots.
func (p *VersionProvider) Requirements(ctx context.Context, versionID int64, limit int) ([]chain.Node, error) {
	if limit <= 0 {
		limit = chain.DefaultMaxRequirements
	}
	views, _, err := p.requirements.List(ctx, versionID, "", limit, nil)
	if err != nil {
		return nil, err
	}
	nodes := make([]chain.Node, 0, len(views))
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
		if targets := parseDecimalIDs(view.TargetVersionSUTs); len(targets) > 0 {
			node.VersionSUTIDs = targets
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

// VersionSUTs maps the version bindings onto label rows keyed by version-SUT
// id. The chain domain has no version_sut node, so these rows are only consumed
// by ChainService.mergeSUTNames.
func (p *VersionProvider) VersionSUTs(ctx context.Context, versionID int64) ([]chain.Node, error) {
	view, err := p.versions.Get(ctx, versionID)
	if err != nil {
		return nil, err
	}
	nodes := make([]chain.Node, 0, len(view.Bindings))
	for _, binding := range view.Bindings {
		if binding.ID <= 0 {
			continue
		}
		id := binding.ID
		nodes = append(nodes, chain.Node{
			ID:           chain.NodeID(nodeKindVersionSUT, id),
			Kind:         nodeKindVersionSUT,
			RefID:        strconv.FormatInt(id, 10),
			Label:        binding.SUT.Name,
			Status:       "active",
			VersionSUTID: int64Ptr(id),
			Meta: map[string]any{
				"sut_id":           binding.SUT.ID,
				"environment_id":   binding.Environment.ID,
				"environment_name": binding.Environment.Name,
			},
		})
	}
	return nodes, nil
}

// parseDecimalIDs parses the decimal-string ID arrays of the application views
// (requirement targets are strings on the version boundary), dropping
// malformed/non-positive values and returning sorted unique IDs.
func parseDecimalIDs(values []string) []int64 {
	ids := make([]int64, 0, len(values))
	for _, raw := range values {
		id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		ids = append(ids, id)
	}
	return sortUniqueIDs(ids)
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
