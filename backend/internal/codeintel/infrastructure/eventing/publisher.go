// Package eventing adapts the codeintel ImpactNotifier port to the P0 eventing
// use case (design spec §3.5/§6.9). It publishes the two version-scoped SSE
// events on the fixed channel version:<id> and carries identifiers, counters and
// path summaries only: never code content, diffs, facts or prompts.
package eventing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	eventingdomain "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
)

const (
	// EventTypeChanged is the SSE type of the change-set-ready event.
	EventTypeChanged = "code.changed"
	// EventTypeImpactComputed is the SSE type of the analysis-completed event.
	EventTypeImpactComputed = "code.impact_computed"

	// maxSummaryRunes is the envelope summary limit (P0 §7.2).
	maxSummaryRunes = 500
	// maxDetailBytes is the envelope detail limit (P0 §7.2).
	maxDetailBytes = 64 << 10
	// maxRefPaths is the design §6.9 refs.code cap.
	maxRefPaths = 20
	// maxRefPathChars is the documented path length bound (design §4.2).
	maxRefPathChars = 4096
)

// impactCountKeys are the only counts the impact event may expose. Extra keys a
// caller attaches are dropped so no content can ride along.
var impactCountKeys = []string{"requirements", "appModels", "unmapped", "candidates"}

// EventPublisher is the narrow slice of the eventing use case the adapter needs.
// *eventing/application.Service satisfies it.
type EventPublisher interface {
	Publish(ctx context.Context, event eventingdomain.NewEvent) (eventingdomain.Event, error)
}

// Publisher publishes codeintel events through the P0 event bus.
type Publisher struct {
	events EventPublisher
}

// NewPublisher returns the adapter. A nil publisher disables publishing (used by
// partially wired processes and tests).
func NewPublisher(events EventPublisher) *Publisher {
	return &Publisher{events: events}
}

var _ codeintelapp.ImpactNotifier = (*Publisher)(nil)

// Changed publishes code.changed after a change set is ready. The payload
// carries the analysis id (as a decimal string: D29/INV-9), both commits and the
// deterministic stats summary; the event summary and refs.code are bounded,
// deduplicated and sorted.
func (p *Publisher) Changed(ctx context.Context, versionID int64, e codeintelapp.ChangedEvent) error {
	if p == nil || p.events == nil {
		return nil
	}
	stats, err := changedStats(e.Stats)
	if err != nil {
		return err
	}
	detail, err := encodeDetail(map[string]any{
		"analysisId": strconv.FormatInt(e.AnalysisID, 10),
		"baseCommit": e.BaseCommit,
		"headCommit": e.HeadCommit,
		"stats":      stats,
	})
	if err != nil {
		return err
	}
	return p.publish(ctx, versionID, EventTypeChanged,
		changedSummary(e.Paths, stats), detail, normalizePaths(e.Paths))
}

// ImpactComputed publishes code.impact_computed after an analysis reaches its
// terminal state. The analysis id is a decimal string (D29/INV-9), the counts
// are allowlisted to the four documented counters and the evidence paths are
// deduplicated, sorted and capped in refs.code.
func (p *Publisher) ImpactComputed(ctx context.Context, versionID int64, e codeintelapp.ImpactEvent) error {
	if p == nil || p.events == nil {
		return nil
	}
	counts := impactCounts(e.Counts)
	detail, err := encodeDetail(map[string]any{
		"analysisId":      strconv.FormatInt(e.AnalysisID, 10),
		"analysisVersion": e.AnalysisVersion,
		"status":          e.Status,
		"degraded":        e.Degraded,
		"counts":          counts,
	})
	if err != nil {
		return err
	}
	return p.publish(ctx, versionID, EventTypeImpactComputed,
		impactSummary(e, counts), detail, normalizePaths(e.Paths))
}

// publish assembles the frozen version-channel envelope and sends it. Failures
// are returned: the caller logs them and the database remains authoritative.
func (p *Publisher) publish(ctx context.Context, versionID int64, eventType, summary string, detail json.RawMessage, refs []string) error {
	version := strconv.FormatInt(versionID, 10)
	_, err := p.events.Publish(ctx, eventingdomain.NewEvent{
		Channel:   fmt.Sprintf("version:%d", versionID),
		Type:      eventType,
		VersionID: &version,
		Summary:   truncateRunes(summary, maxSummaryRunes),
		Detail:    detail,
		Refs:      eventingdomain.Refs{Code: refs},
	})
	return err
}

// changedStats decodes the change set's stats JSON and keeps only the four
// documented counters. Any other field the caller put in the JSONB is dropped,
// and an empty stats value becomes the empty object.
func changedStats(raw []byte) (map[string]any, error) {
	stats := make(map[string]any)
	if len(bytes.TrimSpace(raw)) == 0 {
		return stats, nil
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("codeintel: decode change set stats: %w", err)
	}
	if decoded == nil {
		return stats, nil
	}
	for _, key := range []string{"changedFiles", "additions", "deletions"} {
		if value, ok := decoded[key].(float64); ok {
			stats[key] = value
		}
	}
	if truncated, ok := decoded["truncated"].(bool); ok {
		stats["truncated"] = truncated
	}
	return stats, nil
}

// impactCounts projects the caller's counts onto the four documented counters,
// defaulting missing counters to zero. Any extra key is dropped.
func impactCounts(counts map[string]int) map[string]int {
	projected := make(map[string]int, len(impactCountKeys))
	for _, key := range impactCountKeys {
		projected[key] = counts[key]
	}
	return projected
}

// changedSummary builds the bounded human summary of the change event.
func changedSummary(paths []string, stats map[string]any) string {
	files := 0
	if value, ok := stats["changedFiles"].(float64); ok {
		files = int(value)
	}
	if files == 0 {
		files = countUniquePaths(paths)
	}
	summary := fmt.Sprintf("code changed: %d files", files)
	if additions, ok := stats["additions"].(float64); ok {
		if deletions, ok := stats["deletions"].(float64); ok {
			summary += fmt.Sprintf(" (+%d/-%d)", int(additions), int(deletions))
		}
	}
	if truncated, ok := stats["truncated"].(bool); ok && truncated {
		summary += " (truncated)"
	}
	if path := firstPath(paths); path != "" {
		summary += " (" + path + ")"
	}
	return summary
}

// impactSummary builds the bounded human summary of the impact event.
func impactSummary(e codeintelapp.ImpactEvent, counts map[string]int) string {
	summary := "code impact computed: " + e.Status
	if e.Degraded {
		summary += " (degraded)"
	}
	summary += fmt.Sprintf(", candidates=%d", counts["candidates"])
	if path := firstPath(e.Paths); path != "" {
		summary += " (" + path + ")"
	}
	return summary
}

// encodeDetail marshals the allowlisted detail object and enforces the 64 KiB
// envelope bound before the eventing domain sees it.
func encodeDetail(detail map[string]any) (json.RawMessage, error) {
	encoded, err := json.Marshal(detail)
	if err != nil {
		return nil, fmt.Errorf("codeintel: encode event detail: %w", err)
	}
	if len(encoded) > maxDetailBytes {
		return nil, fmt.Errorf("codeintel: event detail is %d bytes, limit is %d", len(encoded), maxDetailBytes)
	}
	return encoded, nil
}

// normalizePaths filters empty paths, deduplicates, sorts and caps the refs at
// the design §6.9 limit. Paths longer than the documented bound are dropped
// rather than silently emitted.
func normalizePaths(paths []string) []string {
	unique := make(map[string]struct{}, len(paths))
	normalized := make([]string, 0, len(paths))
	for _, path := range paths {
		if strings.TrimSpace(path) == "" || utf8.RuneCountInString(path) > maxRefPathChars {
			continue
		}
		if _, ok := unique[path]; ok {
			continue
		}
		unique[path] = struct{}{}
		normalized = append(normalized, path)
	}
	sort.Strings(normalized)
	if len(normalized) > maxRefPaths {
		normalized = normalized[:maxRefPaths]
	}
	return normalized
}

// countUniquePaths counts distinct non-empty, bounded paths for the summary
// fallback when the stats JSON does not carry a file count.
func countUniquePaths(paths []string) int {
	unique := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if strings.TrimSpace(path) != "" && utf8.RuneCountInString(path) <= maxRefPathChars {
			unique[path] = struct{}{}
		}
	}
	return len(unique)
}

// firstPath returns the first non-empty path for the summary.
func firstPath(paths []string) string {
	for _, path := range paths {
		if strings.TrimSpace(path) != "" {
			return path
		}
	}
	return ""
}

// truncateRunes cuts value to at most limit runes without splitting a rune.
func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit])
}
