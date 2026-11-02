package chainapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	chain "github.com/EdouardRichard/EzTest/backend/internal/chain/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// Clock is the injectable time source of the chain service.
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// ChainService serves the read-only exploration-chain view. It assembles the
// provider nodes, applies the requested filter/budget in the domain builder,
// and stamps the version and replay metadata owned by the service.
type ChainService struct {
	version   VersionChainProvider
	design    DesignChainProvider
	execution ExecutionChainProvider
	delivery  DeliveryChainProvider
	resource  ResourceChainProvider
	cursor    CursorReader
	events    EventTimeReader
	clock     Clock
	budget    chain.Budget
	settings  SettingsReader
}

// NewChainService wires the five chain providers, the snapshot-cursor reader,
// the event-time reader, the clock and the configured chain budget. A nil clock
// falls back to the system clock. The event-time reader is only needed for
// event_from/event_to replay; an assembly that never serves a range may pass
// nil. The optional budget is the chain.max_nodes / chain.max_edges /
// chain.max_requirements tuple from settings (spec §4.5), clamped through
// domain.ClampBudget; omitting it keeps the spec defaults. At most one budget
// is read; extra values are ignored.
func NewChainService(
	version VersionChainProvider,
	design DesignChainProvider,
	execution ExecutionChainProvider,
	delivery DeliveryChainProvider,
	resource ResourceChainProvider,
	cursor CursorReader,
	events EventTimeReader,
	clock Clock,
	budgets ...chain.Budget,
) *ChainService {
	if clock == nil {
		clock = systemClock{}
	}
	configured := chain.Budget{}
	if len(budgets) > 0 {
		configured = budgets[0]
	}
	return &ChainService{
		version:   version,
		design:    design,
		execution: execution,
		delivery:  delivery,
		resource:  resource,
		cursor:    cursor,
		events:    events,
		clock:     clock,
		budget:    chain.ClampBudget(configured),
	}
}

// WithSettings attaches the hot settings cache read by every P6 request
// (chain.max_nodes/max_edges/max_requirements and chain.replay_event_limit).
// It returns the receiver for fluent wiring; a nil reader keeps the static
// budget. The P6 server wiring calls this once, and a settings PATCH is visible
// on the next Read without rebuilding the service.
func (s *ChainService) WithSettings(reader SettingsReader) *ChainService {
	if s != nil {
		s.settings = reader
	}
	return s
}

// Read returns the exploration chain of one version (spec §8.2–§8.4, P6
// §4.3.6). The filter is normalized once; event_from/event_to are mapped to
// inclusive created_at boundaries and provider nodes outside that window are
// dropped before BuildChain, so the induced edges only connect surviving
// nodes. Nodes without a timestamp are kept, and replay is reported as
// approximate because P4 never promises an arbitrary as-of snapshot.
//
// P6 increments: the chain.max_* and chain.replay_event_limit settings are read
// from the hot settings cache on every call; a failed provider layer is
// reported through DegradedLayers while healthy layers still return; all five
// layers failing is a 503 provider_unavailable; a wave outside the version is
// a 404. An invalid enum/limit is a 400, while a provider/transport failure is
// degraded (except for an explicit 4xx from the version/resource layer, which
// keeps the P4 404/400 semantics).
func (s *ChainService) Read(ctx context.Context, versionID int64, f chain.ChainFilter) (chain.ChainView, error) {
	if versionID <= 0 {
		return chain.ChainView{}, fmt.Errorf("chain: version_id must be positive: %d", versionID)
	}
	if err := validateEventBounds(f); err != nil {
		return chain.ChainView{}, err
	}
	f = f.Normalize()
	if err := validateP6Filter(f); err != nil {
		return chain.ChainView{}, err
	}
	eventLower, eventUpper, err := s.eventTimeBounds(ctx, versionID, f)
	if err != nil {
		return chain.ChainView{}, err
	}
	budget := s.effectiveBudget(ctx)
	requirementLimit := budget.MaxRequirements
	if f.RequirementLimit > 0 {
		requirementLimit = f.RequirementLimit
	}
	tracker := newLayerTracker()

	// Ask for one requirement root beyond the effective budget. The version
	// provider pages through RequirementService.List until it has that many
	// roots or the dataset is exhausted, so the extra root reaches BuildChain
	// even when one version list page is capped at 100. BuildChain truncates to
	// MaxRequirements with Truncated=true / reason=requirement_budget instead of
	// silently serving exactly the budget page. A provider that ignores the
	// limit and returns more than MaxRequirements+1 is still truncated
	// deterministically by BuildChain.
	requirements, err := s.version.Requirements(ctx, versionID, requirementLimit+1)
	if fatal := tracker.handle(chain.LayerRequirement, err); fatal != nil {
		return chain.ChainView{}, fatal
	}
	if err != nil {
		requirements = nil
	}
	versionSUTs, err := s.version.VersionSUTs(ctx, versionID)
	if fatal := tracker.handle(chain.LayerRequirement, err); fatal != nil {
		return chain.ChainView{}, fatal
	}
	if err != nil {
		versionSUTs = nil
	}

	var scenarios, cases []chain.Node
	if snapshot, ok := s.design.(DesignSnapshotProvider); ok {
		scenarios, cases, err = snapshot.DesignSnapshot(ctx, versionID)
		if fatal := tracker.handle(chain.LayerDesign, err); fatal != nil {
			return chain.ChainView{}, fatal
		}
		if err != nil {
			scenarios, cases = nil, nil
		}
	} else {
		scenarios, err = s.design.Scenarios(ctx, versionID)
		if fatal := tracker.handle(chain.LayerDesign, err); fatal != nil {
			return chain.ChainView{}, fatal
		}
		if err != nil {
			scenarios = nil
		}
		cases, err = s.design.Cases(ctx, versionID)
		if fatal := tracker.handle(chain.LayerDesign, err); fatal != nil {
			return chain.ChainView{}, fatal
		}
		if err != nil {
			cases = nil
		}
	}
	var waves, caseRuns []chain.Node
	wavesLoaded := false
	if snapshot, ok := s.execution.(ExecutionSnapshotProvider); ok {
		waves, caseRuns, err = snapshot.ExecutionSnapshot(ctx, versionID)
		if fatal := tracker.handle(chain.LayerExecution, err); fatal != nil {
			return chain.ChainView{}, fatal
		}
		if err != nil {
			waves, caseRuns = nil, nil
		} else {
			wavesLoaded = true
		}
	} else {
		waves, err = s.execution.Waves(ctx, versionID)
		if fatal := tracker.handle(chain.LayerExecution, err); fatal != nil {
			return chain.ChainView{}, fatal
		}
		if err != nil {
			waves = nil
		} else {
			wavesLoaded = true
		}
		caseRuns, err = s.execution.CaseRuns(ctx, versionID)
		if fatal := tracker.handle(chain.LayerExecution, err); fatal != nil {
			return chain.ChainView{}, fatal
		}
		if err != nil {
			caseRuns = nil
		}
	}

	// Historical case versions keep superseded rows reachable from case runs.
	// This design-layer read happens after the execution read because the
	// missing IDs come from the run set.
	if missing := missingCaseVersionIDs(cases, caseRuns); len(missing) > 0 {
		historical, err := s.design.CaseVersionsByIDs(ctx, versionID, missing)
		if fatal := tracker.handle(chain.LayerDesign, err); fatal != nil {
			return chain.ChainView{}, fatal
		}
		if err == nil {
			cases = append(cases, historical...)
		}
	}

	// P6: a wave filter must reference a wave of this version; a missing wave is
	// a 404, not an empty graph. This check is skipped while the execution layer
	// is degraded because absence cannot be proven then.
	if f.WaveID != nil && wavesLoaded && !containsWaveNode(waves, *f.WaveID) {
		return chain.ChainView{}, platform.NotFound("wave")
	}

	issues, err := s.delivery.Issues(ctx, versionID)
	if fatal := tracker.handle(chain.LayerDelivery, err); fatal != nil {
		return chain.ChainView{}, fatal
	}
	if err != nil {
		issues = nil
	}
	reports, err := s.delivery.Reports(ctx, versionID)
	if fatal := tracker.handle(chain.LayerDelivery, err); fatal != nil {
		return chain.ChainView{}, fatal
	}
	if err != nil {
		reports = nil
	}
	closures, err := s.delivery.Closures(ctx, versionID)
	if fatal := tracker.handle(chain.LayerDelivery, err); fatal != nil {
		return chain.ChainView{}, fatal
	}
	if err != nil {
		closures = nil
	}

	var sutNames, pageNames map[int64]string
	if snapshot, ok := s.resource.(ResourceSnapshotProvider); ok {
		sutNames, pageNames, err = snapshot.ResourceSnapshot(ctx, versionID)
		if fatal := tracker.handle(chain.LayerRequirement, err); fatal != nil {
			return chain.ChainView{}, fatal
		}
		if err != nil {
			sutNames, pageNames = nil, nil
		}
	} else {
		sutNames, err = s.resource.SUTNames(ctx, versionID)
		if fatal := tracker.handle(chain.LayerRequirement, err); fatal != nil {
			return chain.ChainView{}, fatal
		}
		if err != nil {
			sutNames = nil
		}
		pageNames, err = s.resource.PageNames(ctx, versionID)
		if fatal := tracker.handle(chain.LayerRequirement, err); fatal != nil {
			return chain.ChainView{}, fatal
		}
		if err != nil {
			pageNames = nil
		}
	}

	// The event cursor belongs to the execution/history layer; losing it only
	// costs the snapshot_cursor field, so it degrades rather than failing the
	// whole graph.
	cursor, err := s.cursor.LatestCursor(ctx, versionChannel(versionID))
	if fatal := tracker.handle(chain.LayerExecution, err); fatal != nil {
		return chain.ChainView{}, fatal
	}
	if err != nil {
		cursor = 0
	}

	if tracker.allFailed() {
		return chain.ChainView{}, platform.NewError(
			http.StatusServiceUnavailable,
			"provider_unavailable",
			"all exploration-chain provider layers are unavailable",
		)
	}

	source := chain.SourceGraph{
		Requirements: requirements,
		Scenarios:    scenarios,
		Cases:        cases,
		Waves:        waves,
		CaseRuns:     caseRuns,
		Issues:       issues,
		Reports:      reports,
		Closures:     closures,
		SUTs:         mergeSUTNames(versionSUTs, sutNames),
		Pages:        nonNilNames(pageNames),
	}
	view := chain.BuildChain(filterSourceGraphByEventRange(source, eventLower, eventUpper), f, budget, cursor, s.clock.Now())
	view.VersionID = strconv.FormatInt(versionID, 10)
	view.DegradedLayers = tracker.layers()
	view.Replay.EventLimit = s.replayEventLimit(ctx)
	view.Replay.Truncated = replayEventCount(f, cursor) > int64(view.Replay.EventLimit)
	return view, nil
}

func validateP6Filter(f chain.ChainFilter) error {
	if utf8.RuneCountInString(f.Q) > 100 {
		return platform.Invalid("q", "must be at most 100 characters")
	}
	if f.IssueStatus != "" && !validIssueStatus(f.IssueStatus) {
		return platform.Invalid("issue_status", "must be one of open/confirmed/dismissed/merged")
	}
	if f.IssueSeverity != "" && !validIssueSeverity(f.IssueSeverity) {
		return platform.Invalid("issue_severity", "must be one of major/minor/trivial")
	}
	return nil
}

func validIssueStatus(value string) bool {
	switch value {
	case "open", "confirmed", "dismissed", "merged":
		return true
	default:
		return false
	}
}

func validIssueSeverity(value string) bool {
	switch value {
	case "major", "minor", "trivial":
		return true
	default:
		return false
	}
}

// effectiveBudget reads the chain.max_* tuple from the hot settings cache on
// every request; a missing/failing cache falls back to the statically wired
// budget, which is itself clamped to the spec defaults.
func (s *ChainService) effectiveBudget(ctx context.Context) chain.Budget {
	budget := chain.ClampBudget(s.budget)
	if s.settings == nil {
		return budget
	}
	budget.MaxNodes = s.intSetting(ctx, "chain.max_nodes", budget.MaxNodes)
	budget.MaxEdges = s.intSetting(ctx, "chain.max_edges", budget.MaxEdges)
	budget.MaxRequirements = s.intSetting(ctx, "chain.max_requirements", budget.MaxRequirements)
	return chain.ClampBudget(budget)
}

func (s *ChainService) intSetting(ctx context.Context, key string, fallback int) int {
	if s.settings == nil {
		return fallback
	}
	value, err := s.settings.Int(ctx, key)
	if err != nil || value < 1 || value > int64(^uint(0)>>1) {
		return fallback
	}
	return int(value)
}

func (s *ChainService) replayEventLimit(ctx context.Context) int {
	limit := chain.DefaultReplayEventLimit
	if s.settings != nil {
		if value, err := s.settings.Int(ctx, "chain.replay_event_limit"); err == nil &&
			value >= 1 && value <= int64(^uint(0)>>1) {
			limit = int(value)
		}
	}
	return chain.ClampReplayEventLimit(limit)
}

// replayEventCount estimates the number of channel-local events covered by the
// requested event range. It is the chain.replay_event_limit consumption point:
// the chain read does not page the event log itself, so the bounded seq window
// is the quantity compared with the setting.
func replayEventCount(f chain.ChainFilter, cursor int64) int64 {
	if f.EventFrom == nil && f.EventTo == nil {
		return 0
	}
	from := int64(1)
	to := cursor
	if f.EventFrom != nil {
		from = *f.EventFrom
	}
	if f.EventTo != nil {
		to = *f.EventTo
	}
	if to < from {
		return 0
	}
	return to - from + 1
}

func containsWaveNode(waves []chain.Node, waveID int64) bool {
	for _, wave := range waves {
		if wave.Kind != chain.NodeWave {
			continue
		}
		if id, ok := nodeRefID(wave); ok && id == waveID {
			return true
		}
	}
	return false
}

// layerTracker records P6 degraded provider layers. Only unexpected/5xx
// failures degrade; explicit 4xx API errors (missing version, invalid request)
// keep their frozen P4 semantics and abort the read.
type layerTracker struct {
	failed map[string]bool
}

func newLayerTracker() *layerTracker {
	return &layerTracker{failed: map[string]bool{}}
}

func (t *layerTracker) handle(layer string, err error) error {
	if err == nil {
		return nil
	}
	if isClientError(err) {
		return err
	}
	t.failed[layer] = true
	return nil
}

func isClientError(err error) bool {
	var apiErr *platform.Error
	if errors.As(err, &apiErr) {
		return apiErr.Status >= 400 && apiErr.Status < 500
	}
	return false
}

func (t *layerTracker) allFailed() bool {
	for _, layer := range chain.DegradedLayerOrder {
		if layer == chain.LayerCodeIntel {
			// P6 does not consume a codeintel provider; the layer is reserved by
			// the contract and never marks the whole read unavailable.
			continue
		}
		if !t.failed[layer] {
			return false
		}
	}
	return true
}

func (t *layerTracker) layers() []string {
	out := make([]string, 0, len(t.failed))
	for _, layer := range chain.DegradedLayerOrder {
		if t.failed[layer] {
			out = append(out, layer)
		}
	}
	return out
}

func providerError(operation string, err error) error {
	return fmt.Errorf("chain: %s: %w", operation, err)
}

func versionChannel(versionID int64) string {
	return "version:" + strconv.FormatInt(versionID, 10)
}

// missingCaseVersionIDs lists the case versions referenced by case runs but not
// returned by the current-case reader. The design provider's historical reader
// resolves them so superseded rows keep their titles in the chain.
func missingCaseVersionIDs(cases, caseRuns []chain.Node) []int64 {
	known := make(map[int64]bool, len(cases))
	for _, node := range cases {
		if id, ok := nodeRefID(node); ok {
			known[id] = true
		}
	}
	missing := map[int64]bool{}
	for _, node := range caseRuns {
		if node.CaseVersionID == nil || *node.CaseVersionID <= 0 {
			continue
		}
		if !known[*node.CaseVersionID] {
			missing[*node.CaseVersionID] = true
		}
	}
	ids := make([]int64, 0, len(missing))
	for id := range missing {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// mergeSUTNames builds the version-SUT-ID label map. VersionSUT nodes provide
// the binding labels, resource SUT names refine them (they win on conflict).
func mergeSUTNames(versionSUTs []chain.Node, names map[int64]string) map[int64]string {
	out := make(map[int64]string, len(versionSUTs)+len(names))
	for _, node := range versionSUTs {
		id, ok := nodeRefID(node)
		if !ok || node.Label == "" {
			continue
		}
		out[id] = node.Label
	}
	for id, name := range names {
		if id > 0 && name != "" {
			out[id] = name
		}
	}
	return out
}

func nodeRefID(node chain.Node) (int64, bool) {
	if id, err := strconv.ParseInt(strings.TrimSpace(node.RefID), 10, 64); err == nil && id > 0 {
		return id, true
	}
	if index := strings.LastIndex(node.ID, ":"); index >= 0 {
		if id, err := strconv.ParseInt(strings.TrimSpace(node.ID[index+1:]), 10, 64); err == nil && id > 0 {
			return id, true
		}
	}
	return 0, false
}

func nonNilNames(names map[int64]string) map[int64]string {
	if names == nil {
		return map[int64]string{}
	}
	return names
}
