package chainapp

import (
	"context"
	"fmt"
	"time"

	chain "github.com/EdouardRichard/EzTest/backend/internal/chain/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// validateEventBounds rejects negative event sequences before ChainFilter
// normalization. Zero is the documented "unset" value (the HTTP parser accepts
// minimum 0 and Normalize drops it), so only values below zero are invalid.
func validateEventBounds(f chain.ChainFilter) error {
	if f.EventFrom != nil && *f.EventFrom < 0 {
		return platform.Invalid("event_from", "must be a non-negative decimal event sequence")
	}
	if f.EventTo != nil && *f.EventTo < 0 {
		return platform.Invalid("event_to", "must be a non-negative decimal event sequence")
	}
	return nil
}

// eventTimeBounds maps the normalized event_from/event_to seqs to the inclusive
// created_at bounds of the channel's events. A channel seq without a visible
// event row is rejected as an invalid range instead of silently widening the
// replay. Both bounds are optional; nil means unbounded on that side.
func (s *ChainService) eventTimeBounds(ctx context.Context, versionID int64, f chain.ChainFilter) (lower, upper *time.Time, err error) {
	if f.EventFrom == nil && f.EventTo == nil {
		return nil, nil, nil
	}
	if s.events == nil {
		return nil, nil, fmt.Errorf("chain: event_from/event_to requires an event time reader")
	}
	channel := versionChannel(versionID)
	if f.EventFrom != nil {
		at, found, err := s.events.EventTime(ctx, channel, *f.EventFrom)
		if err != nil {
			return nil, nil, providerError("map event_from", err)
		}
		if !found {
			return nil, nil, platform.Invalid("event_from", "event sequence was not found in the channel snapshot")
		}
		lower = &at
	}
	if f.EventTo != nil {
		at, found, err := s.events.EventTime(ctx, channel, *f.EventTo)
		if err != nil {
			return nil, nil, providerError("map event_to", err)
		}
		if !found {
			return nil, nil, platform.Invalid("event_to", "event sequence was not found in the channel snapshot")
		}
		upper = &at
	}
	return lower, upper, nil
}

// filterSourceGraphByEventRange drops provider nodes whose CreatedAt falls
// outside the inclusive event-time window. Nodes without a timestamp are always
// kept: P4 is an approximate created_at replay, not an as-of snapshot, so a
// missing provider timestamp must not erase an entity from the graph. Edges are
// rebuilt by BuildChain after this filter, so only induced edges survive.
func filterSourceGraphByEventRange(src chain.SourceGraph, lower, upper *time.Time) chain.SourceGraph {
	if lower == nil && upper == nil {
		return src
	}
	src.Requirements = filterNodesByCreatedAt(src.Requirements, lower, upper)
	src.Scenarios = filterNodesByCreatedAt(src.Scenarios, lower, upper)
	src.Cases = filterNodesByCreatedAt(src.Cases, lower, upper)
	src.Waves = filterNodesByCreatedAt(src.Waves, lower, upper)
	src.CaseRuns = filterNodesByCreatedAt(src.CaseRuns, lower, upper)
	src.Issues = filterNodesByCreatedAt(src.Issues, lower, upper)
	src.Reports = filterNodesByCreatedAt(src.Reports, lower, upper)
	src.Closures = filterNodesByCreatedAt(src.Closures, lower, upper)
	return src
}

func filterNodesByCreatedAt(nodes []chain.Node, lower, upper *time.Time) []chain.Node {
	filtered := make([]chain.Node, 0, len(nodes))
	for _, node := range nodes {
		if createdAtInEventRange(node.CreatedAt, lower, upper) {
			filtered = append(filtered, node)
		}
	}
	return filtered
}

func createdAtInEventRange(createdAt time.Time, lower, upper *time.Time) bool {
	if createdAt.IsZero() {
		return true
	}
	if lower != nil && createdAt.Before(*lower) {
		return false
	}
	if upper != nil && createdAt.After(*upper) {
		return false
	}
	return true
}
