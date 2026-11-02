package chain

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// SourceGraph is the provider-supplied input of BuildChain (spec §8.1). Every
// slice holds chain nodes of the matching kind; SUTs and Pages resolve display
// labels for version SUT and page IDs. The builder never reads another
// context's tables.
type SourceGraph struct {
	Requirements []Node
	Scenarios    []Node
	Cases        []Node
	Waves        []Node
	CaseRuns     []Node
	Issues       []Node
	Reports      []Node
	Closures     []Node
	SUTs         map[int64]string
	Pages        map[int64]string
}

// Count keys of ChainView.Counts (spec §4.2).
const (
	countRequirements = "requirements"
	countScenarios    = "scenarios"
	countCases        = "cases"
	countWaves        = "waves"
	countCaseRuns     = "case_runs"
	countIssues       = "issues"
	countReports      = "reports"
	countEvidence     = "evidence"
)

var chainCountKeys = []string{
	countRequirements,
	countScenarios,
	countCases,
	countWaves,
	countCaseRuns,
	countIssues,
	countReports,
	countEvidence,
}

// BuildChain assembles the filtered, budgeted exploration-chain read model of
// one version from provider nodes (spec §8.2–§8.4).
//
// It never returns an error: malformed provider nodes are ignored, filtering is
// per-node, and truncation is reported through ChainView.Truncated/Truncation.
// The caller (ChainService) fills ChainView.VersionID because the version is
// not part of the node graph itself.
//
// Counts describe the filter (and focus) result before any budget truncation:
// they never shrink because a budget dropped nodes or edges. Focus-protected
// nodes and the focus ancestry path edges are exempt from the budgets, so they
// keep their place in Counts even when the configured maxima are smaller than
// the protected set.
func BuildChain(src SourceGraph, f ChainFilter, b Budget, snapshotCursor int64, now time.Time) ChainView {
	f = f.Normalize()
	b = ClampBudget(b)
	// The filter limit is a lower-only override: it may tighten the configured
	// node budget but never raises it above chain.max_nodes.
	if f.Limit > 0 && f.Limit < b.MaxNodes {
		b.MaxNodes = f.Limit
	}

	// P6: one latest case run per case version. The rule is frozen by spec
	// §4.3.6: newest (wave_id DESC, id DESC); a case version without a run has
	// no case_run node.
	src.CaseRuns = latestCaseRunsPerCaseVersion(src.CaseRuns)
	// P6: requirement_limit is a full override (not the lower-only Limit).
	if f.RequirementLimit > 0 {
		b.MaxRequirements = f.RequirementLimit
	}

	graph := newChainGraph(src)
	keep := graph.filter(f)
	protected := map[string]bool{}
	var focusEdges map[edgeIdentity]bool
	if f.FocusNode != "" {
		protected, focusEdges = graph.focus(f.FocusNode)
	}
	for id := range protected {
		keep[id] = true
	}

	// P6 typed meta and coverage describe the filtered graph before any budget
	// truncation, matching the legacy Counts contract.
	stats := graph.analyze(keep)
	graph.enrichTypedMeta(stats)

	// Counts describe the filtered graph before any budget truncation (spec
	// §8.3: counts are the filtered counts, truncation is reported separately).
	counts := graph.counts(keep)
	typedCounts := graph.typedCounts(keep, stats)
	preBudget := cloneSet(keep)

	requirementTruncated, keep := graph.applyRequirementBudget(keep, b.MaxRequirements, protected)
	nodeTruncated, keep := graph.applyNodeBudget(keep, b.MaxNodes, protected)
	truncatedLayers := graph.truncatedLayers(preBudget, keep)

	edges := graph.inducedEdges(keep)
	edgeTruncated, edges := applyEdgeBudget(edges, b.MaxEdges, focusEdges)

	reason := ""
	switch {
	case requirementTruncated:
		reason = TruncationReasonRequirementBudget
	case nodeTruncated:
		reason = TruncationReasonNodeBudget
	case edgeTruncated:
		reason = TruncationReasonEdgeBudget
	}

	return ChainView{
		GeneratedAt:    now.UTC(),
		SnapshotCursor: snapshotCursor,
		Filters:        f,
		Counts:         counts,
		Nodes:          graph.outputNodes(keep),
		Edges:          edges,
		Truncated:      reason != "",
		Truncation: Truncation{
			MaxNodes:        b.MaxNodes,
			MaxEdges:        b.MaxEdges,
			MaxRequirements: b.MaxRequirements,
			Reason:          reason,
		},
		Replay:              replayInfo(f),
		CaseRunStatusCounts: typedCounts.CaseRunStatusCounts,
		IssueSeverityCounts: typedCounts.IssueSeverityCounts,
		IssueStatusCounts:   typedCounts.IssueStatusCounts,
		Coverage:            typedCounts.Coverage,
		TruncatedLayers:     truncatedLayers,
	}
}

// latestCaseRunsPerCaseVersion applies the frozen P6 latest-attempt rule: one
// case_run per case_version, ordered by (wave_id DESC, id DESC). Runs without a
// case version cannot be grouped and are kept unchanged.
func latestCaseRunsPerCaseVersion(runs []Node) []Node {
	latest := make(map[int64]Node, len(runs))
	passthrough := make([]Node, 0, len(runs))
	for _, run := range runs {
		if run.CaseVersionID == nil || *run.CaseVersionID <= 0 {
			passthrough = append(passthrough, run)
			continue
		}
		current, ok := latest[*run.CaseVersionID]
		if !ok || caseRunIsNewer(run, current) {
			latest[*run.CaseVersionID] = run
		}
	}
	ids := make([]int64, 0, len(latest))
	for id := range latest {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]Node, 0, len(passthrough)+len(latest))
	out = append(out, passthrough...)
	for _, id := range ids {
		out = append(out, latest[id])
	}
	return out
}

func caseRunIsNewer(candidate, current Node) bool {
	candidateWave, currentWave := nodeWaveRank(candidate), nodeWaveRank(current)
	if candidateWave != currentWave {
		return candidateWave > currentWave
	}
	return nodeRefInt64(candidate) > nodeRefInt64(current)
}

func nodeWaveRank(node Node) int64 {
	if node.WaveID == nil {
		return 0
	}
	return *node.WaveID
}

// requirementLayerStats is the per-requirement case/execution tally used by the
// typed requirement meta and counts.coverage.
type requirementLayerStats struct {
	caseCount     int
	executedCount int
	failedCount   int
}

// chainLayerStats is the filtered-graph tally shared by typed meta and counts.
// It is computed before budget truncation.
type chainLayerStats struct {
	requirements  map[int64]*requirementLayerStats
	scenarioCases map[int64]int
	coverage      CoverageCounts
}

// analyze computes the P6 coverage/execution tallies over the filtered keep set.
func (g *chainGraph) analyze(keep map[string]bool) chainLayerStats {
	stats := chainLayerStats{
		requirements:  make(map[int64]*requirementLayerStats, len(g.byKind[NodeRequirement])),
		scenarioCases: make(map[int64]int, len(g.byKind[NodeScenario])),
	}
	for _, id := range g.byKind[NodeRequirement] {
		if !keep[id] {
			continue
		}
		if ref, ok := parsePositiveID(g.nodes[id].RefID); ok {
			stats.requirements[ref] = &requirementLayerStats{}
		}
	}
	for _, id := range g.byKind[NodeCaseVersion] {
		if !keep[id] {
			continue
		}
		node := g.nodes[id]
		if node.RequirementID != nil {
			if entry := stats.requirements[*node.RequirementID]; entry != nil {
				entry.caseCount++
			}
		}
		if node.ScenarioID != nil {
			stats.scenarioCases[*node.ScenarioID]++
		}
	}
	for _, id := range g.byKind[NodeCaseRun] {
		if !keep[id] {
			continue
		}
		node := g.nodes[id]
		if node.CaseVersionID == nil {
			continue
		}
		caseVersion, ok := g.nodes[NodeID(NodeCaseVersion, *node.CaseVersionID)]
		if !ok || !keep[NodeID(NodeCaseVersion, *node.CaseVersionID)] || caseVersion.RequirementID == nil {
			continue
		}
		entry := stats.requirements[*caseVersion.RequirementID]
		if entry == nil {
			continue
		}
		entry.executedCount++
		if strings.EqualFold(node.Status, "failed") {
			entry.failedCount++
		}
	}
	for _, entry := range stats.requirements {
		stats.coverage.Requirements++
		if entry.caseCount > 0 {
			stats.coverage.Covered++
		} else {
			stats.coverage.Uncovered++
		}
	}
	return stats
}

type chainTypedCounts struct {
	CaseRunStatusCounts CaseRunStatusCounts
	IssueSeverityCounts IssueSeverityCounts
	IssueStatusCounts   IssueStatusCounts
	Coverage            CoverageCounts
}

// typedCounts projects the filtered keep set onto the P6 counts objects.
func (g *chainGraph) typedCounts(keep map[string]bool, stats chainLayerStats) chainTypedCounts {
	out := chainTypedCounts{Coverage: stats.coverage}
	for id := range keep {
		node := g.nodes[id]
		switch node.Kind {
		case NodeCaseRun:
			switch strings.ToLower(strings.TrimSpace(node.Status)) {
			case "passed":
				out.CaseRunStatusCounts.Passed++
			case "failed":
				out.CaseRunStatusCounts.Failed++
			case "blocked":
				out.CaseRunStatusCounts.Blocked++
			case "inconclusive":
				out.CaseRunStatusCounts.Inconclusive++
			case "skipped":
				out.CaseRunStatusCounts.Skipped++
			case "interrupted":
				out.CaseRunStatusCounts.Interrupted++
			}
		case NodeIssue:
			switch strings.ToLower(strings.TrimSpace(metaString(node.Meta, "severity"))) {
			case "major":
				out.IssueSeverityCounts.Major++
			case "minor":
				out.IssueSeverityCounts.Minor++
			case "trivial":
				out.IssueSeverityCounts.Trivial++
			}
			switch strings.ToLower(strings.TrimSpace(node.Status)) {
			case "open":
				out.IssueStatusCounts.Open++
			case "confirmed":
				out.IssueStatusCounts.Confirmed++
			case "dismissed":
				out.IssueStatusCounts.Dismissed++
			case "merged":
				out.IssueStatusCounts.Merged++
			}
		}
	}
	return out
}

// enrichTypedMeta writes the P6 kind-specific meta fields documented by the
// frozen ChainNodeMeta schema. additionalProperties stays compatible with P4
// clients because existing keys are preserved.
func (g *chainGraph) enrichTypedMeta(stats chainLayerStats) {
	for id, node := range g.nodes {
		switch node.Kind {
		case NodeRequirement:
			entry := &requirementLayerStats{}
			if ref, ok := parsePositiveID(node.RefID); ok {
				if found, exists := stats.requirements[ref]; exists {
					entry = found
				}
			}
			setMetaInt(&node, "case_count", entry.caseCount)
			setMetaInt(&node, "executed_count", entry.executedCount)
			setMetaInt(&node, "failed_count", entry.failedCount)
			setMetaBool(&node, "uncovered", entry.caseCount == 0)
		case NodeScenario:
			if node.RequirementID != nil {
				setMetaString(&node, "requirement_id", strconv.FormatInt(*node.RequirementID, 10))
			}
			if ref, ok := parsePositiveID(node.RefID); ok {
				setMetaInt(&node, "case_count", stats.scenarioCases[ref])
			}
		case NodeCaseVersion:
			if node.ScenarioID != nil {
				setMetaString(&node, "scenario_id", strconv.FormatInt(*node.ScenarioID, 10))
			}
			setMetaBool(&node, "stale", node.Status == "stale" || node.Status == "superseded")
		case NodeWave:
			setMetaString(&node, "wave_status", node.Status)
		case NodeCaseRun:
			if value, ok := metaNumber(node.Meta, "attempt"); ok {
				setMetaInt(&node, "attempt", int(value))
			} else if value, ok := metaNumber(node.Meta, "attempts"); ok {
				setMetaInt(&node, "attempt", int(value))
			}
		case NodeIssue:
			setMetaString(&node, "issue_status", node.Status)
			if node.MergedIntoID != nil {
				setMetaString(&node, "merged_into_id", strconv.FormatInt(*node.MergedIntoID, 10))
			} else if value := metaString(node.Meta, "merged_into_id"); value != "" {
				setMetaString(&node, "merged_into_id", value)
			}
			// evidence_count/code_ref_count are provider tallies; keep them as
			// numbers and let the DTO preserve the type.
		case NodeReport:
			if kind := metaString(node.Meta, "report_kind"); kind != "" {
				setMetaString(&node, "report_kind", kind)
			} else if kind := metaString(node.Meta, "kind"); kind != "" {
				setMetaString(&node, "report_kind", kind)
			}
			setMetaString(&node, "report_status", node.Status)
			if value, ok := metaNumber(node.Meta, "revision"); ok {
				setMetaInt(&node, "revision", int(value))
			} else if value, ok := metaNumber(node.Meta, "current_revision"); ok {
				setMetaInt(&node, "revision", int(value))
			}
		case NodeClosure:
			setMetaString(&node, "closure_status", node.Status)
		}
		g.nodes[id] = node
	}
}

func setMetaString(node *Node, key, value string) {
	if value == "" {
		return
	}
	if node.Meta == nil {
		node.Meta = map[string]any{}
	}
	node.Meta[key] = value
}

func setMetaInt(node *Node, key string, value int) {
	if node.Meta == nil {
		node.Meta = map[string]any{}
	}
	node.Meta[key] = value
}

func setMetaBool(node *Node, key string, value bool) {
	if node.Meta == nil {
		node.Meta = map[string]any{}
	}
	node.Meta[key] = value
}

func metaNumber(meta map[string]any, key string) (int64, bool) {
	if meta == nil {
		return 0, false
	}
	value, ok := meta[key]
	if !ok || value == nil {
		return 0, false
	}
	return toInt64(value)
}

// truncatedLayers reports which P6 layers lost at least one node to a budget.
func (g *chainGraph) truncatedLayers(before, after map[string]bool) TruncatedLayers {
	var layers TruncatedLayers
	for id := range before {
		if after[id] {
			continue
		}
		switch g.nodes[id].Kind {
		case NodeRequirement:
			layers.Requirements = true
		case NodeCaseVersion:
			layers.Cases = true
		case NodeCaseRun:
			layers.CaseRuns = true
		case NodeIssue:
			layers.Issues = true
		case NodeReport:
			layers.Reports = true
		}
	}
	return layers
}

var nodeKindOrder = map[NodeKind]int{
	NodeRequirement: 0,
	NodeScenario:    1,
	NodeCaseVersion: 2,
	NodeWave:        3,
	NodeCaseRun:     4,
	NodeIssue:       5,
	NodeReport:      6,
	NodeClosure:     7,
}

var knownNodeKinds = map[NodeKind]bool{
	NodeRequirement: true,
	NodeScenario:    true,
	NodeCaseVersion: true,
	NodeWave:        true,
	NodeCaseRun:     true,
	NodeIssue:       true,
	NodeReport:      true,
	NodeClosure:     true,
}

type edgeIdentity struct {
	From string
	To   string
	Kind EdgeKind
}

type parentLink struct {
	parent  string
	edge    Edge
	hasEdge bool
}

type chainGraph struct {
	nodes   map[string]Node
	byKind  map[NodeKind][]string
	edges   []Edge
	edgeSet map[edgeIdentity]bool
	parents map[string][]parentLink
	suts    map[int64]string
	pages   map[int64]string
}

func newChainGraph(src SourceGraph) *chainGraph {
	graph := &chainGraph{
		nodes:   map[string]Node{},
		byKind:  map[NodeKind][]string{},
		edgeSet: map[edgeIdentity]bool{},
		parents: map[string][]parentLink{},
		suts:    src.SUTs,
		pages:   src.Pages,
	}
	lists := []struct {
		kind  NodeKind
		nodes []Node
	}{
		{NodeRequirement, src.Requirements},
		{NodeScenario, src.Scenarios},
		{NodeCaseVersion, src.Cases},
		{NodeWave, src.Waves},
		{NodeCaseRun, src.CaseRuns},
		{NodeIssue, src.Issues},
		{NodeReport, src.Reports},
		{NodeClosure, src.Closures},
	}
	for _, list := range lists {
		for _, node := range list.nodes {
			graph.addNode(node, list.kind)
		}
	}
	for id, node := range graph.nodes {
		graph.nodes[id] = enrichNodeLabels(node, graph.suts, graph.pages)
	}
	graph.buildEdges()
	return graph
}

func (g *chainGraph) addNode(raw Node, listKind NodeKind) {
	kind := raw.Kind
	if kind == "" {
		kind = listKind
	}
	if !knownNodeKinds[kind] {
		return
	}
	refID, ok := canonicalRefID(raw.RefID, raw.ID)
	if !ok {
		return
	}
	id := NodeID(kind, refID)
	if _, duplicate := g.nodes[id]; duplicate {
		return
	}
	node := raw
	node.ID = id
	node.Kind = kind
	node.RefID = strconv.FormatInt(refID, 10)
	node.Meta = copyMeta(raw.Meta)
	g.nodes[id] = node
	g.byKind[kind] = append(g.byKind[kind], id)
}

func canonicalRefID(refID, id string) (int64, bool) {
	if value, ok := parsePositiveID(refID); ok {
		return value, true
	}
	if index := strings.LastIndex(id, ":"); index >= 0 {
		if value, ok := parsePositiveID(id[index+1:]); ok {
			return value, true
		}
	}
	return 0, false
}

func parsePositiveID(raw string) (int64, bool) {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value <= 0 {
		return 0, false
	}
	return value, true
}

func copyMeta(meta map[string]any) map[string]any {
	if meta == nil {
		return nil
	}
	out := make(map[string]any, len(meta))
	for key, value := range meta {
		out[key] = value
	}
	return out
}

// nodeSUTIDs returns every version SUT ID a node is associated with. Providers
// put cross-SUT targets in Meta["version_sut_ids"] (case/scenario/run) or
// Meta["target_version_sut_ids"] (requirement).
func nodeSUTIDs(node Node) []int64 {
	ids := map[int64]bool{}
	if node.VersionSUTID != nil && *node.VersionSUTID > 0 {
		ids[*node.VersionSUTID] = true
	}
	for _, id := range node.VersionSUTIDs {
		if id > 0 {
			ids[id] = true
		}
	}
	for _, id := range metaIDList(node.Meta, "version_sut_ids", "target_version_sut_ids", "sut_ids") {
		ids[id] = true
	}
	return sortedIDs(ids)
}

func nodePageIDs(node Node) []int64 {
	ids := map[int64]bool{}
	if node.PageID != nil && *node.PageID > 0 {
		ids[*node.PageID] = true
	}
	for _, id := range metaIDList(node.Meta, "page_ids") {
		ids[id] = true
	}
	return sortedIDs(ids)
}

func sortedIDs(set map[int64]bool) []int64 {
	ids := make([]int64, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func metaIDList(meta map[string]any, keys ...string) []int64 {
	if meta == nil {
		return nil
	}
	seen := map[int64]bool{}
	var ids []int64
	add := func(value any) {
		if id, ok := toInt64(value); ok && id > 0 && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, key := range keys {
		value, ok := meta[key]
		if !ok || value == nil {
			continue
		}
		switch list := value.(type) {
		case []int64:
			for _, id := range list {
				add(id)
			}
		case []int:
			for _, id := range list {
				add(id)
			}
		case []int32:
			for _, id := range list {
				add(id)
			}
		case []float64:
			for _, id := range list {
				add(id)
			}
		case []string:
			for _, id := range list {
				add(id)
			}
		case []any:
			for _, id := range list {
				add(id)
			}
		default:
			add(value)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func toInt64(value any) (int64, bool) {
	switch number := value.(type) {
	case int:
		return int64(number), true
	case int32:
		return int64(number), true
	case int64:
		return number, true
	case uint:
		return int64(number), true
	case uint64:
		if number > uint64(^uint64(0)>>1) {
			return 0, false
		}
		return int64(number), true
	case float64:
		converted := int64(number)
		return converted, float64(converted) == number
	case float32:
		converted := int64(number)
		return converted, float32(converted) == number
	case string:
		return parsePositiveID(number)
	default:
		return 0, false
	}
}

func metaInt(meta map[string]any, key string) int {
	if meta == nil {
		return 0
	}
	value, ok := meta[key]
	if !ok {
		return 0
	}
	if converted, ok := toInt64(value); ok && converted > 0 {
		return int(converted)
	}
	return 0
}

func enrichNodeLabels(node Node, suts, pages map[int64]string) Node {
	sutIDs := nodeSUTIDs(node)
	if len(sutIDs) > 0 {
		labels := make([]string, 0, len(sutIDs))
		for _, id := range sutIDs {
			if label := suts[id]; label != "" {
				labels = append(labels, label)
			}
		}
		if len(labels) > 0 {
			if node.Meta == nil {
				node.Meta = map[string]any{}
			}
			node.Meta["sut_labels"] = labels
		}
	}
	pageIDs := nodePageIDs(node)
	if len(pageIDs) > 0 {
		labels := make([]string, 0, len(pageIDs))
		for _, id := range pageIDs {
			if label := pages[id]; label != "" {
				labels = append(labels, label)
			}
		}
		if len(labels) > 0 {
			if node.Meta == nil {
				node.Meta = map[string]any{}
			}
			node.Meta["page_labels"] = labels
		}
	}
	return node
}

func (g *chainGraph) buildEdges() {
	for _, id := range g.byKind[NodeScenario] {
		node := g.nodes[id]
		if node.RequirementID != nil {
			g.addEdge(NodeID(NodeRequirement, *node.RequirementID), id, EdgeHasScenario)
		}
	}
	for _, id := range g.byKind[NodeCaseVersion] {
		node := g.nodes[id]
		if node.ScenarioID != nil {
			g.addEdge(NodeID(NodeScenario, *node.ScenarioID), id, EdgeHasCase)
		} else if node.RequirementID != nil {
			// A case always reaches its requirement; when no scenario is
			// available keep the focus ancestry path without inventing an edge.
			g.addParentLink(NodeID(NodeRequirement, *node.RequirementID), id, nil)
		}
	}
	for _, id := range g.byKind[NodeCaseRun] {
		node := g.nodes[id]
		if node.CaseVersionID != nil {
			g.addEdge(NodeID(NodeCaseVersion, *node.CaseVersionID), id, EdgeExecutedBy)
		}
		if node.WaveID != nil {
			g.addEdge(NodeID(NodeWave, *node.WaveID), id, EdgeContains)
		}
	}
	for _, id := range g.byKind[NodeIssue] {
		node := g.nodes[id]
		raised := false
		if node.CaseRunID != nil {
			raised = g.addEdge(NodeID(NodeCaseRun, *node.CaseRunID), id, EdgeRaised)
		}
		if !raised && node.WaveID != nil {
			raised = g.addEdge(NodeID(NodeWave, *node.WaveID), id, EdgeRaised)
		}
		if !raised && node.RequirementID != nil {
			g.addParentLink(NodeID(NodeRequirement, *node.RequirementID), id, nil)
		}
		if node.MergedIntoID != nil {
			target := NodeID(NodeIssue, *node.MergedIntoID)
			if target != id {
				g.addEdge(id, target, EdgeMergedInto)
			}
		}
	}
	for _, id := range g.byKind[NodeReport] {
		node := g.nodes[id]
		if node.WaveID != nil {
			g.addEdge(NodeID(NodeWave, *node.WaveID), id, EdgeReports)
		}
		for _, issueID := range reportRefIDs(node, NodeIssue) {
			g.addEdge(NodeID(NodeIssue, issueID), id, EdgeIncludedIn)
		}
		for _, caseRunID := range reportRefIDs(node, NodeCaseRun) {
			g.addEdge(NodeID(NodeCaseRun, caseRunID), id, EdgeIncludedIn)
		}
	}
	for _, kind := range []NodeKind{NodeIssue, NodeCaseRun} {
		for _, id := range g.byKind[kind] {
			node := g.nodes[id]
			if node.ReportID != nil {
				g.addEdge(id, NodeID(NodeReport, *node.ReportID), EdgeIncludedIn)
			}
		}
	}
	for _, id := range g.byKind[NodeClosure] {
		node := g.nodes[id]
		if node.RequirementID != nil {
			g.addEdge(NodeID(NodeRequirement, *node.RequirementID), id, EdgeClosedBy)
		}
	}
}

// reportRefIDs reads the optional typed ref lists a delivery provider may attach
// to a report node. The report itself stays a generic Node.
func reportRefIDs(report Node, kind NodeKind) []int64 {
	if report.Meta == nil {
		return nil
	}
	keys := map[NodeKind][]string{
		NodeIssue:   {"issue_ids"},
		NodeCaseRun: {"case_run_ids"},
	}
	ids := metaIDList(report.Meta, keys[kind]...)
	for _, ref := range metaRefs(report.Meta) {
		if ref.kind == kind {
			ids = append(ids, ref.id)
		}
	}
	return ids
}

type metaRef struct {
	kind NodeKind
	id   int64
}

func metaRefs(meta map[string]any) []metaRef {
	raw, ok := meta["refs"]
	if !ok {
		return nil
	}
	var list []any
	switch refs := raw.(type) {
	case []any:
		list = refs
	case []map[string]any:
		list = make([]any, 0, len(refs))
		for _, ref := range refs {
			list = append(list, ref)
		}
	default:
		return nil
	}
	out := make([]metaRef, 0, len(list))
	for _, item := range list {
		ref, ok := item.(map[string]any)
		if !ok {
			continue
		}
		kindRaw, _ := ref["kind"].(string)
		kind := NodeKind(strings.TrimSpace(kindRaw))
		if kind != NodeIssue && kind != NodeCaseRun {
			continue
		}
		id, ok := toInt64(ref["ref_id"])
		if !ok || id <= 0 {
			continue
		}
		out = append(out, metaRef{kind: kind, id: id})
	}
	return out
}

func (g *chainGraph) addEdge(from, to string, kind EdgeKind) bool {
	if from == to {
		return false
	}
	if _, ok := g.nodes[from]; !ok {
		return false
	}
	if _, ok := g.nodes[to]; !ok {
		return false
	}
	key := edgeIdentity{From: from, To: to, Kind: kind}
	if g.edgeSet[key] {
		return true
	}
	edge := Edge{From: from, To: to, Kind: kind}
	g.edges = append(g.edges, edge)
	g.edgeSet[key] = true
	g.addParentLink(from, to, &g.edges[len(g.edges)-1])
	return true
}

func (g *chainGraph) addParentLink(parent, child string, edge *Edge) {
	if parent == child {
		return
	}
	link := parentLink{parent: parent}
	if edge != nil {
		link.edge = *edge
		link.hasEdge = true
	}
	g.parents[child] = append(g.parents[child], link)
}

func (g *chainGraph) filter(f ChainFilter) map[string]bool {
	keep := make(map[string]bool, len(g.nodes))
	for id, node := range g.nodes {
		if nodeMatchesFilter(node, f) {
			keep[id] = true
		}
	}
	return keep
}

func nodeMatchesFilter(node Node, f ChainFilter) bool {
	if f.VersionSUTID != nil && !containsID(nodeSUTIDs(node), *f.VersionSUTID) {
		return false
	}
	if f.PageID != nil && !containsID(nodePageIDs(node), *f.PageID) {
		return false
	}
	if f.WaveID != nil && !nodeInWave(node, *f.WaveID) {
		return false
	}
	if f.Status != "" && !strings.EqualFold(node.Status, f.Status) {
		return false
	}
	if f.Result != "" && !strings.EqualFold(node.Result, f.Result) {
		return false
	}
	// P6 title filter: q matches requirement/scenario/case-version labels only.
	// It never deletes context nodes of other kinds; induced edges are trimmed
	// later because one endpoint may disappear.
	if f.Q != "" && isTitleNodeKind(node.Kind) &&
		!strings.Contains(strings.ToLower(node.Label), strings.ToLower(f.Q)) {
		return false
	}
	// P6 abnormal-only: only case_run nodes are classified by result status.
	if f.AbnormalOnly && node.Kind == NodeCaseRun && !isAbnormalCaseRunStatus(node.Status) {
		return false
	}
	// P6 issue filters prune only issue nodes.
	if f.IssueStatus != "" && node.Kind == NodeIssue && !strings.EqualFold(node.Status, f.IssueStatus) {
		return false
	}
	if f.IssueSeverity != "" && node.Kind == NodeIssue &&
		!strings.EqualFold(metaString(node.Meta, "severity"), f.IssueSeverity) {
		return false
	}
	return true
}

func isTitleNodeKind(kind NodeKind) bool {
	switch kind {
	case NodeRequirement, NodeScenario, NodeCaseVersion:
		return true
	default:
		return false
	}
}

func isAbnormalCaseRunStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "failed", "blocked", "inconclusive", "interrupted":
		return true
	default:
		return false
	}
}

func metaString(meta map[string]any, key string) string {
	if meta == nil {
		return ""
	}
	value, _ := meta[key].(string)
	return value
}

func containsID(ids []int64, wanted int64) bool {
	for _, id := range ids {
		if id == wanted {
			return true
		}
	}
	return false
}

func nodeInWave(node Node, waveID int64) bool {
	if node.Kind == NodeWave {
		if id, ok := parsePositiveID(node.RefID); ok && id == waveID {
			return true
		}
	}
	if node.WaveID != nil && *node.WaveID == waveID {
		return true
	}
	for _, id := range metaIDList(node.Meta, "wave_ids") {
		if id == waveID {
			return true
		}
	}
	return false
}

// focus returns the target node plus every ancestor reachable through the
// semantic parent links, and the exact path edges used by the BFS. The
// protected set bypasses the requirement and node budgets; the path edges are
// prioritized by the edge budget.
func (g *chainGraph) focus(focusNode string) (map[string]bool, map[edgeIdentity]bool) {
	target, ok := normalizeFocusNode(focusNode)
	if !ok {
		return nil, nil
	}
	if _, ok := g.nodes[target]; !ok {
		return nil, nil
	}
	protected := map[string]bool{target: true}
	pathEdges := map[edgeIdentity]bool{}
	queue := []string{target}
	for len(queue) > 0 {
		child := queue[0]
		queue = queue[1:]
		for _, link := range g.parents[child] {
			if protected[link.parent] {
				continue
			}
			protected[link.parent] = true
			if link.hasEdge {
				pathEdges[edgeIdentity{From: link.edge.From, To: link.edge.To, Kind: link.edge.Kind}] = true
			}
			queue = append(queue, link.parent)
		}
	}
	return protected, pathEdges
}

func normalizeFocusNode(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	index := strings.Index(raw, ":")
	if index <= 0 || index == len(raw)-1 {
		return "", false
	}
	kind := NodeKind(raw[:index])
	if !knownNodeKinds[kind] {
		return "", false
	}
	id, ok := parsePositiveID(raw[index+1:])
	if !ok {
		return "", false
	}
	return NodeID(kind, id), true
}

// counts tallies the keep set after filtering and focus protection, before any
// budget truncation (spec §8.3: counts are the filtered counts, truncation is
// reported separately).
func (g *chainGraph) counts(keep map[string]bool) map[string]int {
	counts := make(map[string]int, len(chainCountKeys))
	for _, key := range chainCountKeys {
		counts[key] = 0
	}
	for id := range keep {
		node := g.nodes[id]
		switch node.Kind {
		case NodeRequirement:
			counts[countRequirements]++
		case NodeScenario:
			counts[countScenarios]++
		case NodeCaseVersion:
			counts[countCases]++
		case NodeWave:
			counts[countWaves]++
		case NodeCaseRun:
			counts[countCaseRuns]++
			counts[countEvidence] += metaInt(node.Meta, "evidence_count")
		case NodeIssue:
			counts[countIssues]++
		case NodeReport:
			counts[countReports]++
		}
	}
	return counts
}

func (g *chainGraph) applyRequirementBudget(keep map[string]bool, max int, protected map[string]bool) (bool, map[string]bool) {
	requirements := g.idsOfKind(keep, NodeRequirement)
	if len(requirements) <= max {
		return false, keep
	}
	g.sortRequirementsNewestFirst(requirements)
	selected := make(map[string]bool, max+len(protected))
	for index, id := range requirements {
		if index < max {
			selected[id] = true
		}
	}
	for id := range protected {
		if g.nodes[id].Kind == NodeRequirement {
			selected[id] = true
		}
	}

	out := cloneSet(keep)
	for id := range keep {
		if protected[id] {
			continue
		}
		node := g.nodes[id]
		switch node.Kind {
		case NodeRequirement:
			if !selected[id] {
				delete(out, id)
			}
		case NodeClosure, NodeScenario, NodeCaseVersion:
			if node.RequirementID != nil {
				// Only a requirement that is part of the filtered graph was a
				// budget candidate. A filtered-out root was never selected, but
				// the filter (not the budget) removed it, so its subtree must not
				// be cascaded away here.
				requirementID := NodeID(NodeRequirement, *node.RequirementID)
				if keep[requirementID] && !selected[requirementID] {
					delete(out, id)
				}
			}
		}
	}

	// Remove descendants that lost their parent because the requirement root
	// was dropped by the root budget. Iterate until stable: the chain is at
	// most five levels deep and this keeps the result independent of map order.
	for changed := true; changed; {
		changed = false
		for id := range out {
			if protected[id] {
				continue
			}
			node := g.nodes[id]
			drop := false
			switch node.Kind {
			case NodeCaseVersion:
				if node.ScenarioID != nil {
					scenarioID := NodeID(NodeScenario, *node.ScenarioID)
					drop = keep[scenarioID] && !out[scenarioID]
				}
			case NodeCaseRun:
				if node.CaseVersionID != nil {
					caseVersionID := NodeID(NodeCaseVersion, *node.CaseVersionID)
					drop = keep[caseVersionID] && !out[caseVersionID]
				}
			case NodeIssue:
				if node.CaseRunID != nil {
					caseRunID := NodeID(NodeCaseRun, *node.CaseRunID)
					drop = keep[caseRunID] && !out[caseRunID]
				} else if node.RequirementID != nil {
					requirementID := NodeID(NodeRequirement, *node.RequirementID)
					drop = keep[requirementID] && !selected[requirementID]
				}
			}
			if drop {
				delete(out, id)
				changed = true
			}
		}
	}
	return true, out
}

func (g *chainGraph) idsOfKind(keep map[string]bool, kind NodeKind) []string {
	ids := make([]string, 0, len(g.byKind[kind]))
	for _, id := range g.byKind[kind] {
		if keep[id] {
			ids = append(ids, id)
		}
	}
	return ids
}

func (g *chainGraph) sortRequirementsNewestFirst(ids []string) {
	sort.Slice(ids, func(i, j int) bool {
		left, right := g.nodes[ids[i]], g.nodes[ids[j]]
		leftTime, rightTime := nodeRecency(left), nodeRecency(right)
		if !leftTime.Equal(rightTime) {
			return leftTime.After(rightTime)
		}
		return nodeRefInt64(left) > nodeRefInt64(right)
	})
}

func nodeRecency(node Node) time.Time {
	if !node.UpdatedAt.IsZero() {
		return node.UpdatedAt
	}
	return node.CreatedAt
}

func nodeRefInt64(node Node) int64 {
	id, _ := parsePositiveID(node.RefID)
	return id
}

func (g *chainGraph) applyNodeBudget(keep map[string]bool, max int, protected map[string]bool) (bool, map[string]bool) {
	if len(keep) <= max {
		return false, keep
	}
	out := cloneSet(keep)
	total := len(out)
	protected = g.protectClosures(protected, out)

	// Drop low-priority layers first (spec §8.3). Within one layer keep the
	// newest nodes and the highest IDs, mirroring the requirement root order.
	for _, kind := range []NodeKind{NodeIssue, NodeCaseRun, NodeReport, NodeWave, NodeCaseVersion, NodeScenario} {
		if total <= max {
			break
		}
		candidates := make([]string, 0, len(out))
		for _, id := range g.byKind[kind] {
			if out[id] && !protected[id] {
				candidates = append(candidates, id)
			}
		}
		g.sortNodesOldestFirst(candidates)
		for _, id := range candidates {
			if total <= max {
				break
			}
			delete(out, id)
			total--
		}
	}

	// Requirements are dropped last, together with their closures and any
	// remaining descendants, so a closure never outlives its requirement.
	if total > max {
		units := g.requirementUnits(out, protected)
		for _, unit := range units {
			if total <= max {
				break
			}
			if unit.protected {
				continue
			}
			for _, member := range unit.members {
				if !out[member] || protected[member] {
					continue
				}
				delete(out, member)
				total--
			}
		}
	}
	return true, out
}

func (g *chainGraph) protectClosures(protected map[string]bool, keep map[string]bool) map[string]bool {
	out := make(map[string]bool, len(protected))
	for id := range protected {
		out[id] = true
	}
	for id := range keep {
		node := g.nodes[id]
		if node.Kind == NodeClosure && node.RequirementID != nil {
			if out[NodeID(NodeRequirement, *node.RequirementID)] {
				out[id] = true
			}
		}
	}
	return out
}

func (g *chainGraph) sortNodesOldestFirst(ids []string) {
	sort.Slice(ids, func(i, j int) bool {
		left, right := g.nodes[ids[i]], g.nodes[ids[j]]
		leftTime, rightTime := nodeRecency(left), nodeRecency(right)
		if !leftTime.Equal(rightTime) {
			return leftTime.Before(rightTime)
		}
		return nodeRefInt64(left) < nodeRefInt64(right)
	})
}

type requirementUnit struct {
	requirement string
	members     []string
	protected   bool
}

func (g *chainGraph) requirementUnits(keep map[string]bool, protected map[string]bool) []requirementUnit {
	unitsByID := map[string]*requirementUnit{}
	for id := range keep {
		if g.nodes[id].Kind == NodeRequirement {
			unitsByID[id] = &requirementUnit{
				requirement: id,
				members:     []string{id},
				protected:   protected[id],
			}
		}
	}
	addMember := func(requirementID, member string) {
		unit := unitsByID[requirementID]
		if unit == nil {
			return
		}
		unit.members = append(unit.members, member)
		if protected[member] {
			unit.protected = true
		}
	}
	for id := range keep {
		node := g.nodes[id]
		switch node.Kind {
		case NodeClosure, NodeScenario:
			if node.RequirementID != nil {
				addMember(NodeID(NodeRequirement, *node.RequirementID), id)
			}
		case NodeCaseVersion:
			if node.RequirementID != nil {
				addMember(NodeID(NodeRequirement, *node.RequirementID), id)
			}
		case NodeCaseRun:
			if node.CaseVersionID != nil {
				if caseVersion, ok := g.nodes[NodeID(NodeCaseVersion, *node.CaseVersionID)]; ok && caseVersion.RequirementID != nil {
					addMember(NodeID(NodeRequirement, *caseVersion.RequirementID), id)
				}
			}
		case NodeIssue:
			if node.RequirementID != nil {
				addMember(NodeID(NodeRequirement, *node.RequirementID), id)
			}
		}
	}
	units := make([]requirementUnit, 0, len(unitsByID))
	for _, unit := range unitsByID {
		units = append(units, *unit)
	}
	sort.Slice(units, func(i, j int) bool {
		left, right := g.nodes[units[i].requirement], g.nodes[units[j].requirement]
		leftTime, rightTime := nodeRecency(left), nodeRecency(right)
		if !leftTime.Equal(rightTime) {
			return leftTime.Before(rightTime)
		}
		return nodeRefInt64(left) < nodeRefInt64(right)
	})
	return units
}

func cloneSet(source map[string]bool) map[string]bool {
	out := make(map[string]bool, len(source))
	for id, value := range source {
		out[id] = value
	}
	return out
}

func (g *chainGraph) inducedEdges(keep map[string]bool) []Edge {
	edges := make([]Edge, 0, len(g.edges))
	for _, edge := range g.edges {
		if keep[edge.From] && keep[edge.To] {
			edges = append(edges, edge)
		}
	}
	sortEdgesForOutput(edges)
	return edges
}

func (g *chainGraph) outputNodes(keep map[string]bool) []Node {
	nodes := make([]Node, 0, len(keep))
	for id := range keep {
		nodes = append(nodes, g.nodes[id])
	}
	sort.Slice(nodes, func(i, j int) bool {
		leftOrder, rightOrder := nodeKindOrder[nodes[i].Kind], nodeKindOrder[nodes[j].Kind]
		if leftOrder != rightOrder {
			return leftOrder < rightOrder
		}
		return nodeRefInt64(nodes[i]) < nodeRefInt64(nodes[j])
	})
	return nodes
}

func applyEdgeBudget(edges []Edge, max int, protected map[edgeIdentity]bool) (bool, []Edge) {
	if len(edges) <= max {
		sortEdgesForOutput(edges)
		return false, edges
	}
	out := append([]Edge(nil), edges...)
	sort.SliceStable(out, func(i, j int) bool {
		left, right := edgeIdentity{From: out[i].From, To: out[i].To, Kind: out[i].Kind},
			edgeIdentity{From: out[j].From, To: out[j].To, Kind: out[j].Kind}
		if protected[left] != protected[right] {
			return protected[left]
		}
		leftPriority, rightPriority := edgePriority(out[i].Kind), edgePriority(out[j].Kind)
		if leftPriority != rightPriority {
			return leftPriority < rightPriority
		}
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		if out[i].To != out[j].To {
			return out[i].To < out[j].To
		}
		return out[i].Kind < out[j].Kind
	})
	kept := out[:max]
	sortEdgesForOutput(kept)
	return true, kept
}

func edgePriority(kind EdgeKind) int {
	switch kind {
	case EdgeHasScenario:
		return 0
	case EdgeHasCase:
		return 1
	case EdgeExecutedBy:
		return 2
	case EdgeContains:
		return 3
	case EdgeRaised:
		return 4
	case EdgeReports:
		return 5
	case EdgeClosedBy:
		return 6
	case EdgeIncludedIn:
		return 7
	case EdgeMergedInto:
		return 8
	default:
		return 9
	}
}

func sortEdgesForOutput(edges []Edge) {
	sort.Slice(edges, func(i, j int) bool {
		leftPriority, rightPriority := edgePriority(edges[i].Kind), edgePriority(edges[j].Kind)
		if leftPriority != rightPriority {
			return leftPriority < rightPriority
		}
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		if edges[i].To != edges[j].To {
			return edges[i].To < edges[j].To
		}
		return edges[i].Kind < edges[j].Kind
	})
}

func replayInfo(f ChainFilter) ReplayInfo {
	mode := ReplayModeLive
	if f.WaveID != nil {
		mode = ReplayModeWave
	}
	if f.EventFrom != nil || f.EventTo != nil {
		mode = ReplayModeRange
	}
	return ReplayInfo{
		Mode:        mode,
		WaveID:      f.WaveID,
		EventFrom:   f.EventFrom,
		EventTo:     f.EventTo,
		Approximate: true,
	}
}
