package explore

// This file owns spec §11 capability #4: requirement gap analysis. It derives
// deterministic, bounded `gap` version facts across the six dimensions of a
// requirement (acceptance / boundary / permission / exception / data /
// dependency) and, when a runtime is configured, verifies the discovered pages
// through the shared P9 explore runtime.
//
// Boundaries (spec §11): the explorer only writes version_fact rows (and
// evidence). This analysis therefore has no requirement/case mutation port at
// all, and the only writer it holds is the append-only FactRecorder. Runtime
// consumption follows Task 49's constraints: Login runs before any page is
// opened, actions are serialized, and the flow closes exactly the pages it
// opened (page-scoped close), never every page of the shared executor runtime.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// KindGap is the version_fact kind of one requirement gap. It matches the
// `gap` fact kind declared by spec §11 capability #4.
const KindGap = "gap"

// GapDimension is one dimension of requirement completeness. The six values are
// frozen by spec §11 capability #4.
type GapDimension string

// The six requirement gap dimensions.
const (
	GapDimensionAcceptance GapDimension = "acceptance"
	GapDimensionBoundary   GapDimension = "boundary"
	GapDimensionPermission GapDimension = "permission"
	GapDimensionException  GapDimension = "exception"
	GapDimensionData       GapDimension = "data"
	GapDimensionDependency GapDimension = "dependency"
)

// gapDimensionOrder freezes the evaluation order so a run over the same
// requirements always produces the same fact order.
var gapDimensionOrder = []GapDimension{
	GapDimensionAcceptance,
	GapDimensionBoundary,
	GapDimensionPermission,
	GapDimensionException,
	GapDimensionData,
	GapDimensionDependency,
}

// Label returns the Chinese product label of the dimension. The label is used in
// clarification questions and draft case titles; it is not part of the fact key.
func (d GapDimension) Label() string {
	switch d {
	case GapDimensionAcceptance:
		return "验收"
	case GapDimensionBoundary:
		return "边界"
	case GapDimensionPermission:
		return "权限"
	case GapDimensionException:
		return "异常"
	case GapDimensionData:
		return "数据"
	case GapDimensionDependency:
		return "依赖"
	default:
		return string(d)
	}
}

// Gap analysis bounds. Each bound is independent so a large version cannot
// exhaust time, memory or fact storage; the configured value may only lower the
// corresponding default.
const (
	DefaultMaxGapRequirements = 100
	DefaultMaxGaps            = 200
	DefaultMaxGapProbes       = 3
)

// RequirementSnapshot is the deterministic requirement input of the exploration
// analysis. It is a projection only: the exploration layer never mutates a
// requirement (spec §11 boundary). TargetPages/TargetAPIs hold the knowledge
// keys (route template / `METHOD /path`) so fact matching stays name-based.
type RequirementSnapshot struct {
	ID                 int64
	VersionID          int64
	Title              string
	Description        string
	AcceptanceCriteria string
	Constraints        json.RawMessage
	Status             string
	TargetPages        []string
	TargetAPIs         []string
}

// RequirementSource reads the current requirements of one version.
type RequirementSource interface {
	Requirements(ctx context.Context, versionID int64) ([]RequirementSnapshot, error)
}

// FactReader reads current version facts (versionapp.FactService implements it).
// The analysis only reads; recording goes through FactRecorder.
type FactReader interface {
	Current(ctx context.Context, versionID int64, kind string) ([]version.Fact, error)
}

// Runtime is the P9 explore runtime port consumed by the gap analysis. Both
// methods mirror versionadapter.ExploreWiring, so the composition root passes
// the one process-wide wiring without an adapter. A nil Runtime degrades to the
// static analysis (no page is opened).
type Runtime interface {
	Login(ctx context.Context, cfg LoginFlowConfig) (SessionFact, error)
	OpenWithSession(ctx context.Context, cfg LoginFlowConfig, url string) (Page, SessionFact, error)
}

// GapAnalysisConfig binds one gap analysis run to a version SUT environment and
// its bounds. LoginURL is the page used by the login flow; when empty the first
// probed page URL is used. ProbeURLs is optional: without it the flow probes the
// discovered page facts.
type GapAnalysisConfig struct {
	VersionSUTID    int64
	EnvironmentID   int64
	Role            string
	LoginURL        string
	ProbeURLs       []string
	MaxRequirements int
	MaxProbes       int
	MaxGaps         int
}

// GapAnalysisDeps are the narrow ports consumed by the analysis. Facts may be
// nil (a caller that only wants the result); Runtime/Pages may be nil (static
// analysis). RequirementSource is required.
type GapAnalysisDeps struct {
	Requirements RequirementSource
	Facts        FactRecorder
	FactReader   FactReader
	Runtime      Runtime
	Pages        PageCloser
}

// GapItem is one deterministic requirement gap. Target names the page/API/URL
// the gap is about when the dimension is fact-based.
type GapItem struct {
	Dimension            GapDimension
	RequirementID        int64
	RequirementVersionID int64
	RequirementTitle     string
	Target               string
	Reason               string
	Question             string
	FactID               int64
}

// GapAnalysisResult is the deterministic output of one run.
type GapAnalysisResult struct {
	VersionID       int64
	Gaps            []GapItem
	Requirements    int
	Probes          int
	LoginStatus     string
	LoginReason     string
	UnclosedPageIDs []string
	Truncated       bool
	FactIDs         []int64
}

// GapAnalysis is one bounded, deterministic requirement gap analysis.
type GapAnalysis struct {
	cfg  GapAnalysisConfig
	deps GapAnalysisDeps
}

// NewGapAnalysis wires one gap analysis.
func NewGapAnalysis(cfg GapAnalysisConfig, deps GapAnalysisDeps) *GapAnalysis {
	return &GapAnalysis{cfg: cfg, deps: deps}
}

// gapKeywords holds the deterministic keyword families of the free-text
// dimensions. A requirement passes a dimension when its title/description/
// acceptance/constraints mention at least one family member.
var gapKeywords = map[GapDimension][]string{
	GapDimensionBoundary: {
		"边界", "上限", "下限", "最多", "最少", "不超过", "超过", "长度", "最大", "最小", "范围", "分页",
		"boundary", "limit", "max", "min", "length", "threshold", "range", "page size",
	},
	GapDimensionPermission: {
		"权限", "角色", "管理员", "未登录", "越权", "鉴权", "授权", "登录用户", "访客", "用户组",
		"role", "permission", "admin", "unauthorized", "forbidden", "auth", "member", "owner",
	},
	GapDimensionException: {
		"异常", "错误", "失败", "超时", "重试", "回滚", "降级", "并发", "冲突", "重复",
		"exception", "error", "fail", "timeout", "retry", "rollback", "invalid", "conflict", "duplicate",
	},
	GapDimensionData: {
		"数据", "前置", "样例", "测试数据", "账号", "构造", "预置", "种子",
		"test data", "dataset", "precondition", "fixture", "seed", "sample", "account",
	},
}

// Run analyzes the version requirements and, when a runtime is configured,
// verifies the discovered pages. Facts are written after the whole analysis so
// a failed run never leaves partial gap facts; re-running the same inputs
// produces identical fact keys/payloads (idempotent append).
func (g *GapAnalysis) Run(ctx context.Context, versionID int64) (GapAnalysisResult, error) {
	result := GapAnalysisResult{VersionID: versionID, Gaps: []GapItem{}, UnclosedPageIDs: []string{}}
	if g == nil || g.deps.Requirements == nil {
		return result, errors.New("gap analysis has no requirement source")
	}
	if versionID <= 0 {
		return result, errors.New("gap analysis requires a positive version id")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}

	requirements, err := g.deps.Requirements.Requirements(ctx, versionID)
	if err != nil {
		return result, fmt.Errorf("read gap analysis requirements: %w", err)
	}
	requirements = sortedRequirements(requirements)
	// Only current requirements are eligible: a closed/obsolete requirement is
	// no longer a clarification target.
	eligible := make([]RequirementSnapshot, 0, len(requirements))
	for _, requirement := range requirements {
		switch strings.ToLower(strings.TrimSpace(requirement.Status)) {
		case "closed", "obsolete":
			continue
		}
		eligible = append(eligible, requirement)
	}
	requirements = eligible
	maxRequirements := boundedConfig(g.cfg.MaxRequirements, DefaultMaxGapRequirements)
	if len(requirements) > maxRequirements {
		requirements = requirements[:maxRequirements]
		result.Truncated = true
	}
	result.Requirements = len(requirements)

	pageFacts, apiFacts, factErr := g.currentFacts(ctx, versionID)
	if factErr != nil {
		return result, factErr
	}
	verifiedPages := factPageTargets(pageFacts)
	verifiedAPIs := factAPITargets(apiFacts)

	maxGaps := boundedConfig(g.cfg.MaxGaps, DefaultMaxGaps)
	for _, requirement := range requirements {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		for _, dimension := range gapDimensionOrder {
			gap, found := g.requirementGap(requirement, dimension, verifiedPages, verifiedAPIs)
			if !found {
				continue
			}
			if len(result.Gaps) >= maxGaps {
				result.Truncated = true
				break
			}
			result.Gaps = append(result.Gaps, gap)
		}
		if len(result.Gaps) >= maxGaps {
			result.Truncated = true
			break
		}
	}

	// Runtime verification (R93): login first, then probe discovered pages one
	// at a time and close exactly the pages this run opened. The probe is an
	// enhancement: a blocked login or an unreachable page is reported, never a
	// fatal analysis error.
	if g.deps.Runtime != nil && len(result.Gaps) < maxGaps {
		probeURLs := g.probeURLs(pageFacts)
		if len(probeURLs) > 0 {
			g.verifyPages(ctx, versionID, probeURLs, maxGaps, &result)
		}
	}

	if g.deps.Facts != nil {
		for index := range result.Gaps {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			stored, _, recordErr := g.deps.Facts.Record(ctx, gapFact(versionID, g.cfg, result.Gaps[index]))
			if recordErr != nil {
				return result, fmt.Errorf("record %s fact: %w", KindGap, recordErr)
			}
			result.Gaps[index].FactID = stored.ID
			result.FactIDs = append(result.FactIDs, stored.ID)
		}
	}
	return result, nil
}

/* ------------------------------ dimensions ------------------------------- */

// requirementGap evaluates one dimension of one requirement. Acceptance is
// text-presence based, dependency is fact based, and the other four dimensions
// pass when the requirement text names the concern at least once.
func (g *GapAnalysis) requirementGap(
	requirement RequirementSnapshot,
	dimension GapDimension,
	verifiedPages map[string]bool,
	verifiedAPIs map[string]bool,
) (GapItem, bool) {
	item := GapItem{
		Dimension:            dimension,
		RequirementID:        requirement.ID,
		RequirementVersionID: requirement.VersionID,
		RequirementTitle:     requirement.Title,
	}
	switch dimension {
	case GapDimensionAcceptance:
		if strings.TrimSpace(requirement.AcceptanceCriteria) != "" {
			return GapItem{}, false
		}
		item.Reason = "requirement has no verifiable acceptance criteria"
		item.Question = fmt.Sprintf("需求「%s」缺少可验证的验收标准，请补充验收条件。", requirementLabel(requirement))
		return item, true
	case GapDimensionDependency:
		pages := normalizeKeys(requirement.TargetPages)
		apis := normalizeKeys(requirement.TargetAPIs)
		if len(pages) == 0 && len(apis) == 0 {
			item.Reason = "requirement declares no dependent page or API"
			item.Question = fmt.Sprintf("需求「%s」未声明依赖的页面或 API，请补充系统/页面/接口依赖。", requirementLabel(requirement))
			return item, true
		}
		missingPages := missingTargets(pages, verifiedPages)
		missingAPIs := missingTargets(apis, verifiedAPIs)
		if len(missingPages) == 0 && len(missingAPIs) == 0 {
			return GapItem{}, false
		}
		targets := append(append([]string{}, missingPages...), missingAPIs...)
		item.Target = strings.Join(targets, ", ")
		item.Reason = "dependent page or API is not verified by discovery facts"
		item.Question = fmt.Sprintf("需求「%s」依赖的 %s 尚未被页面/API 发现验证，请确认依赖是否存在。",
			requirementLabel(requirement), item.Target)
		return item, true
	default:
		if gapTextMatches(requirement, gapKeywords[dimension]) {
			return GapItem{}, false
		}
		item.Reason = fmt.Sprintf("requirement does not state the %s concern", dimension)
		item.Question = gapQuestion(requirement, dimension)
		return item, true
	}
}

func gapQuestion(requirement RequirementSnapshot, dimension GapDimension) string {
	label := requirementLabel(requirement)
	switch dimension {
	case GapDimensionBoundary:
		return fmt.Sprintf("需求「%s」是否有边界条件（上限/下限/长度/分页等）？请补充。", label)
	case GapDimensionPermission:
		return fmt.Sprintf("需求「%s」涉及哪些角色与权限？未授权时的期望行为是什么？", label)
	case GapDimensionException:
		return fmt.Sprintf("需求「%s」在异常/失败/超时/回滚时应有怎样的行为？请补充。", label)
	case GapDimensionData:
		return fmt.Sprintf("需求「%s」需要哪些前置数据或测试账号？如何构造与清理？", label)
	default:
		return fmt.Sprintf("需求「%s」缺少%s信息，请补充。", label, dimension.Label())
	}
}

// gapTextMatches reports whether any keyword family member appears in the
// requirement free text (case-insensitive for Latin keywords).
func gapTextMatches(requirement RequirementSnapshot, keywords []string) bool {
	text := strings.ToLower(strings.Join([]string{
		requirement.Title,
		requirement.Description,
		requirement.AcceptanceCriteria,
		string(requirement.Constraints),
	}, "\n"))
	if strings.TrimSpace(text) == "" {
		return false
	}
	for _, keyword := range keywords {
		if keyword == "" {
			continue
		}
		if strings.Contains(text, strings.ToLower(keyword)) {
			return true
		}
	}
	return false
}

func requirementLabel(requirement RequirementSnapshot) string {
	if title := strings.TrimSpace(requirement.Title); title != "" {
		return clipString(title, 60)
	}
	return "需求 " + strconv.FormatInt(requirement.ID, 10)
}

/* -------------------------------- facts --------------------------------- */

func (g *GapAnalysis) currentFacts(ctx context.Context, versionID int64) ([]version.Fact, []version.Fact, error) {
	if g.deps.FactReader == nil {
		return nil, nil, nil
	}
	pages, err := g.deps.FactReader.Current(ctx, versionID, KindPage)
	if err != nil {
		return nil, nil, fmt.Errorf("read gap analysis page facts: %w", err)
	}
	apis, err := g.deps.FactReader.Current(ctx, versionID, KindAPI)
	if err != nil {
		return nil, nil, fmt.Errorf("read gap analysis api facts: %w", err)
	}
	return pages, apis, nil
}

// factPageTargets collects both the knowledge-facing route template and the
// origin-qualified page key so a requirement may reference either form.
func factPageTargets(facts []version.Fact) map[string]bool {
	targets := make(map[string]bool, len(facts)*2)
	for _, fact := range facts {
		for _, key := range []string{"route_template", "page_key"} {
			if value, ok := fact.Payload[key].(string); ok {
				if trimmed := strings.TrimSpace(value); trimmed != "" {
					targets[trimmed] = true
				}
			}
		}
	}
	return targets
}

func factAPITargets(facts []version.Fact) map[string]bool {
	targets := make(map[string]bool, len(facts))
	for _, fact := range facts {
		if value, ok := fact.Payload["api_key"].(string); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				targets[trimmed] = true
			}
		}
	}
	return targets
}

// probeURLs returns the bounded, deterministic page probe list: the configured
// URLs when present, otherwise the URLs of the discovered page facts.
func (g *GapAnalysis) probeURLs(pageFacts []version.Fact) []string {
	maxProbes := boundedConfig(g.cfg.MaxProbes, DefaultMaxGapProbes)
	seen := make(map[string]bool)
	urls := make([]string, 0, maxProbes)
	appendURL := func(raw string) bool {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || seen[trimmed] {
			return len(urls) >= maxProbes
		}
		seen[trimmed] = true
		urls = append(urls, trimmed)
		return len(urls) >= maxProbes
	}
	for _, probeURL := range g.cfg.ProbeURLs {
		if appendURL(probeURL) {
			return urls
		}
	}
	for _, fact := range pageFacts {
		url, _ := fact.Payload["url"].(string)
		if appendURL(url) {
			break
		}
	}
	return urls
}

// verifyPages logs in once and probes the URLs serially. A page that failed to
// open is never closed; a page this run opened is closed by its exact id
// (page-scoped close) or reported for the caller when no closer is configured.
// The probe gaps stay inside the run's gap budget.
func (g *GapAnalysis) verifyPages(ctx context.Context, versionID int64, urls []string, maxGaps int, result *GapAnalysisResult) {
	loginURL := strings.TrimSpace(g.cfg.LoginURL)
	if loginURL == "" && len(urls) > 0 {
		loginURL = urls[0]
	}
	cfg := LoginFlowConfig{
		VersionID:     versionID,
		VersionSUTID:  g.cfg.VersionSUTID,
		EnvironmentID: g.cfg.EnvironmentID,
		Role:          g.cfg.Role,
		LoginURL:      loginURL,
	}
	fact, err := g.deps.Runtime.Login(ctx, cfg)
	if err != nil {
		result.LoginStatus = "error"
		result.LoginReason = clipString(err.Error(), 300)
		return
	}
	result.LoginStatus = fact.Status
	if fact.Status != SessionActive {
		result.LoginReason = fact.Reason
		return
	}
	for _, url := range urls {
		if err := ctx.Err(); err != nil {
			return
		}
		page, _, openErr := g.deps.Runtime.OpenWithSession(ctx, cfg, url)
		result.Probes++
		if openErr != nil {
			if len(result.Gaps) >= maxGaps {
				result.Truncated = true
				continue
			}
			result.Gaps = append(result.Gaps, GapItem{
				Dimension: GapDimensionDependency,
				Target:    url,
				Reason:    "discovered page is not reachable: " + clipString(openErr.Error(), 200),
				Question:  fmt.Sprintf("页面 %s 在已登录会话下不可达，请确认依赖页面是否已下线或需要额外权限。", url),
			})
			continue
		}
		pageID := strings.TrimSpace(page.ID)
		if pageID == "" {
			continue
		}
		if g.deps.Pages == nil {
			result.UnclosedPageIDs = append(result.UnclosedPageIDs, pageID)
			continue
		}
		// Page-scoped, best-effort close: the analysis result is complete and a
		// cleanup failure must not fail (or abort) the analysis. Closing every
		// page would abort concurrent exploration on the shared runtime. The
		// detach lets a canceled run still release the page it opened.
		_ = g.deps.Pages.ClosePage(context.WithoutCancel(ctx), pageID)
	}
}

func gapFact(versionID int64, cfg GapAnalysisConfig, gap GapItem) version.Fact {
	confidence := gapConfidence(gap.Dimension)
	scope := "requirement"
	if gap.RequirementID <= 0 {
		scope = "version"
	}
	payload := map[string]any{
		"dimension":              string(gap.Dimension),
		"scope":                  scope,
		"requirement_id":         gap.RequirementID,
		"requirement_version_id": gap.RequirementVersionID,
		"requirement_title":      clipString(gap.RequirementTitle, 200),
		"target":                 gap.Target,
		"reason":                 gap.Reason,
		"question":               gap.Question,
		"source":                 "requirement_gap_analysis",
	}
	if cfg.VersionSUTID > 0 {
		payload["version_sut_id"] = cfg.VersionSUTID
	}
	if cfg.EnvironmentID > 0 {
		payload["environment_id"] = cfg.EnvironmentID
	}
	return version.Fact{
		VersionID:  versionID,
		Kind:       KindGap,
		Key:        gapFactKey(cfg.EnvironmentID, gap),
		Payload:    payload,
		Confidence: &confidence,
		Source:     "p9",
		SourceRef:  gap.Target,
	}
}

func gapFactKey(environmentID int64, gap GapItem) string {
	identity := fmt.Sprintf("gap:req:%d:%s", gap.RequirementID, gap.Dimension)
	if gap.RequirementID <= 0 {
		identity = fmt.Sprintf("gap:dep:%s", gap.Dimension)
		if target := strings.TrimSpace(gap.Target); target != "" {
			identity += ":" + target
		}
	}
	if environmentID > 0 {
		return fmt.Sprintf("gap:env:%d:%s", environmentID, strings.TrimPrefix(identity, "gap:"))
	}
	return identity
}

func gapConfidence(dimension GapDimension) float64 {
	switch dimension {
	case GapDimensionAcceptance:
		return 0.9
	case GapDimensionDependency:
		return 0.85
	case GapDimensionPermission, GapDimensionException:
		return 0.8
	default:
		return 0.7
	}
}

/* -------------------------------- helpers -------------------------------- */

func sortedRequirements(requirements []RequirementSnapshot) []RequirementSnapshot {
	sorted := make([]RequirementSnapshot, len(requirements))
	copy(sorted, requirements)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].ID != sorted[j].ID {
			return sorted[i].ID < sorted[j].ID
		}
		return sorted[i].VersionID < sorted[j].VersionID
	})
	return sorted
}

func boundedConfig(value, fallback int) int {
	if value <= 0 || value > fallback {
		return fallback
	}
	return value
}

func normalizeKeys(values []string) []string {
	seen := make(map[string]bool, len(values))
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		normalized = append(normalized, trimmed)
	}
	sort.Strings(normalized)
	return normalized
}

func missingTargets(targets []string, verified map[string]bool) []string {
	missing := make([]string, 0, len(targets))
	for _, target := range targets {
		if !verified[target] {
			missing = append(missing, target)
		}
	}
	return missing
}

// DraftCaseTitle returns the deterministic augmented-case title of one gap
// dimension. It is exported so a caller (or a design-side dedupe check) can
// recognize an already-produced augmentation draft without recreating it.
func DraftCaseTitle(requirementTitle string, dimension GapDimension) string {
	title := strings.TrimSpace(requirementTitle)
	if title == "" {
		title = "未命名需求"
	}
	return fmt.Sprintf("[补用例] %s · %s", clipString(title, 60), dimension.Label())
}
