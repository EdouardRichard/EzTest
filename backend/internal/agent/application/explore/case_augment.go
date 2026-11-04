package explore

// This file owns spec §11 capability #6: autonomous case augmentation. It turns
// recorded `gap` facts into deduplicated, executable case drafts that reference
// the fact they came from.
//
// Hard boundary (spec §11, D25): augmentation only ever produces draft cases.
// It has no confirm, requirement-write or execution port; the optional writer
// contract is "create a draft" and a writer that reports any other status is
// rejected. Cases therefore always reach a human confirmation gate.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// DefaultMaxCaseDrafts bounds the number of draft cases one run may produce.
const DefaultMaxCaseDrafts = 20

// CaseDraftStep is one step of an augmented case draft.
type CaseDraftStep struct {
	Seq      int
	Action   string
	Expected string
}

// CaseDraftAssertion is one assertion of an augmented case draft.
type CaseDraftAssertion struct {
	StepSeq  int
	Kind     string
	Target   string
	Expected string
}

// CaseDraft is one deterministic draft case produced from one gap fact. It
// references the fact ids/keys so the design side can trace the draft back to
// the exploration finding (no requirement or case is modified here).
type CaseDraft struct {
	DedupeKey            string
	RequirementID        int64
	RequirementVersionID int64
	Dimension            GapDimension
	Title                string
	Description          string
	Preconditions        string
	Priority             string
	SourceFactIDs        []int64
	SourceFactKeys       []string
	Steps                []CaseDraftStep
	Assertions           []CaseDraftAssertion
}

// CaseDraftResult is the writer response. Status must be "draft"; any other
// status (confirmed/stale) is a contract violation for this flow.
type CaseDraftResult struct {
	CaseID        int64
	CaseVersionID int64
	Status        string
}

// DraftCaseWriter persists one draft case. Implementations must create a draft
// only; they are never allowed to confirm, delete or execute a case.
type DraftCaseWriter interface {
	CreateDraftCase(ctx context.Context, versionID int64, draft CaseDraft) (CaseDraftResult, error)
}

// CaseAugmentConfig bounds one augmentation run.
type CaseAugmentConfig struct {
	MaxDrafts int
}

// CaseAugmentDeps are the narrow ports of the augmentation. Writer may be nil:
// the drafts are then returned to a caller that persists them. Requirements may
// be nil: the eligibility filter is then skipped and the writer stays the
// authority on which drafts it accepts (failures are isolated per draft).
type CaseAugmentDeps struct {
	Facts        FactReader
	Cases        CaseSource
	Writer       DraftCaseWriter
	Requirements RequirementSource
}

// CaseDraftFailure is one writer failure isolated to its draft. One rejected
// draft must never abort the remaining drafts of the batch: the run continues
// and the caller reports the partial result.
type CaseDraftFailure struct {
	RequirementID int64
	Dimension     GapDimension
	Reason        string
}

// CaseAugmentResult is the deterministic output of one run.
type CaseAugmentResult struct {
	VersionID  int64
	Drafts     []CaseDraft
	Created    []CaseDraftResult
	Duplicates int
	Truncated  bool
	// Skipped counts requirement-gap drafts that were not eligible for the
	// design writer (not clarified, stale version, or no longer current). They
	// are skipped before the writer is called, so they cannot abort the batch.
	Skipped int
	// Ungrounded counts requirement gaps that have no concrete discovered
	// target (page/API/URL). Those are requirement-completeness or discovery
	// gaps that need clarification; turning them into templated "[补用例]"
	// drafts only pollutes the graph with placeholder cases (Task 77).
	Ungrounded int
	// Failed holds the per-draft writer failures that were isolated. A non-empty
	// list is a partial success: the drafts in Created were persisted.
	Failed []CaseDraftFailure
}

// CaseAugment produces deduplicated case drafts from the current gap facts.
type CaseAugment struct {
	cfg  CaseAugmentConfig
	deps CaseAugmentDeps
}

// NewCaseAugment wires one augmentation run.
func NewCaseAugment(cfg CaseAugmentConfig, deps CaseAugmentDeps) *CaseAugment {
	return &CaseAugment{cfg: cfg, deps: deps}
}

// Run reads the gap facts, skips drafts whose requirement is no longer current
// or clarified, skips drafts that already exist (by deterministic title) and
// writes the remainder as draft cases when a writer is configured. It never
// confirms, modifies a requirement or executes a case.
func (a *CaseAugment) Run(ctx context.Context, versionID int64) (CaseAugmentResult, error) {
	result := CaseAugmentResult{
		VersionID: versionID,
		Drafts:    []CaseDraft{},
		Created:   []CaseDraftResult{},
		Failed:    []CaseDraftFailure{},
	}
	if a == nil || a.deps.Facts == nil {
		return result, errors.New("case augmentation has no fact reader")
	}
	if versionID <= 0 {
		return result, errors.New("case augmentation requires a positive version id")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}

	gapFacts, err := a.deps.Facts.Current(ctx, versionID, KindGap)
	if err != nil {
		return result, fmt.Errorf("read case augmentation gap facts: %w", err)
	}
	sort.SliceStable(gapFacts, func(i, j int) bool { return gapFacts[i].ID < gapFacts[j].ID })

	// The design writer only accepts a draft whose requirement is still current
	// and clarified. A gap recorded for a requirement that has since been
	// reviewed, closed, re-versioned or deleted is skipped here instead of
	// being sent to a writer that must reject it, so one stale requirement can
	// never abort the remaining drafts.
	eligible, err := a.eligibleRequirements(ctx, versionID)
	if err != nil {
		return result, err
	}

	existing, err := a.existingTitles(ctx, versionID)
	if err != nil {
		return result, err
	}
	maxDrafts := boundedConfig(a.cfg.MaxDrafts, DefaultMaxCaseDrafts)
	produced := make(map[string]bool, maxDrafts)
	for _, fact := range gapFacts {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		draft, ok := draftFromGapFact(fact)
		if !ok {
			// A version-level dependency gap (requirement_id=0) is not a
			// requirement gap: it must not silently become a case.
			continue
		}
		// A gap without a concrete target has nothing executable to verify:
		// it is a completeness/clarification gap, not a case. Persisting a
		// templated placeholder here is how the "[补用例] ... · 依赖" stubs
		// entered the graph with no delete/merge path (Task 77).
		if strings.TrimSpace(factString(fact, "target")) == "" {
			result.Ungrounded++
			continue
		}
		if !draftEligible(draft, eligible) {
			result.Skipped++
			continue
		}
		// Existing cases are deduped by their deterministic title; produced
		// drafts additionally use their stable requirement/dimension key so two
		// same-titled requirements do not swallow each other.
		if existing[normalizeDraftTitle(draft.Title)] || produced[draft.DedupeKey] {
			result.Duplicates++
			continue
		}
		if len(result.Drafts) >= maxDrafts {
			result.Truncated = true
			break
		}
		produced[draft.DedupeKey] = true
		result.Drafts = append(result.Drafts, draft)
	}

	if a.deps.Writer == nil {
		return result, nil
	}
	for _, draft := range result.Drafts {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		stored, writeErr := a.deps.Writer.CreateDraftCase(ctx, versionID, draft)
		if writeErr != nil {
			// Ineligibility (a requirement that is no longer clarified or whose
			// version went stale) is a per-draft condition, not a batch failure:
			// isolate it and keep writing the other drafts.
			result.Failed = append(result.Failed, CaseDraftFailure{
				RequirementID: draft.RequirementID,
				Dimension:     draft.Dimension,
				Reason:        clipString(writeErr.Error(), 300),
			})
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(stored.Status), coverageCaseDraft) {
			// A non-draft writer result is a contract violation, not an
			// eligibility problem: stop before more unsafe writes happen.
			return result, fmt.Errorf("case augment: writer returned status %q; only draft cases are allowed", stored.Status)
		}
		result.Created = append(result.Created, stored)
	}
	return result, nil
}

// eligibleRequirements reads the current requirement source once per run. A nil
// source disables the filter (the writer stays the authority and its per-draft
// failures are isolated); a nil map returned here also disables it. A read
// error fails closed: without the current requirement state the run cannot tell
// an eligible draft from a stale one.
func (a *CaseAugment) eligibleRequirements(ctx context.Context, versionID int64) (map[int64]RequirementSnapshot, error) {
	if a.deps.Requirements == nil {
		return nil, nil
	}
	requirements, err := a.deps.Requirements.Requirements(ctx, versionID)
	if err != nil {
		return nil, fmt.Errorf("read case augmentation requirements: %w", err)
	}
	current := make(map[int64]RequirementSnapshot, len(requirements))
	for _, requirement := range requirements {
		if requirement.ID > 0 {
			current[requirement.ID] = requirement
		}
	}
	return current, nil
}

// draftEligible applies the design writer's admission rules before the writer
// is called: the requirement must still exist in the current source, it must be
// clarified, and when the gap fact recorded a requirement version that version
// must still be current. A nil map disables the filter.
func draftEligible(draft CaseDraft, requirements map[int64]RequirementSnapshot) bool {
	if requirements == nil {
		return true
	}
	requirement, ok := requirements[draft.RequirementID]
	if !ok {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(requirement.Status), "clarified") {
		return false
	}
	if draft.RequirementVersionID > 0 && requirement.VersionID > 0 && draft.RequirementVersionID != requirement.VersionID {
		return false
	}
	return true
}

func (a *CaseAugment) existingTitles(ctx context.Context, versionID int64) (map[string]bool, error) {
	titles := map[string]bool{}
	if a.deps.Cases == nil {
		return titles, nil
	}
	cases, err := a.deps.Cases.Cases(ctx, versionID)
	if err != nil {
		return nil, fmt.Errorf("read case augmentation cases: %w", err)
	}
	for _, item := range cases {
		if title := normalizeDraftTitle(item.Title); title != "" {
			titles[title] = true
		}
	}
	return titles, nil
}

// draftFromGapFact projects one recorded gap fact onto an executable draft. Only
// facts that reference a requirement become cases.
func draftFromGapFact(fact version.Fact) (CaseDraft, bool) {
	requirementID := factInt64(fact, "requirement_id")
	if requirementID <= 0 {
		return CaseDraft{}, false
	}
	dimension := GapDimension(factString(fact, "dimension"))
	if dimension == "" {
		return CaseDraft{}, false
	}
	title := DraftCaseTitle(factString(fact, "requirement_title"), dimension)
	reason := factString(fact, "reason")
	question := factString(fact, "question")
	steps, assertions := draftSteps(dimension, question)
	priority, preconditions := draftScope(dimension)
	return CaseDraft{
		DedupeKey:            draftDedupeKey(fact.VersionID, requirementID, dimension),
		RequirementID:        requirementID,
		RequirementVersionID: factInt64(fact, "requirement_version_id"),
		Dimension:            dimension,
		Title:                title,
		Description:          firstNonEmptyString(reason, question),
		Preconditions:        preconditions,
		Priority:             priority,
		SourceFactIDs:        []int64{fact.ID},
		SourceFactKeys:       []string{fact.Key},
		Steps:                steps,
		Assertions:           assertions,
	}, true
}

func draftSteps(dimension GapDimension, _ string) ([]CaseDraftStep, []CaseDraftAssertion) {
	switch dimension {
	case GapDimensionAcceptance:
		return []CaseDraftStep{
				{Seq: 1, Action: "打开需求涉及的页面并完成需求描述的主要操作", Expected: "操作完成且页面状态符合需求描述"},
				{Seq: 2, Action: "逐条核对需求补充后的验收标准", Expected: "每条验收标准均得到可观察的满足"},
			}, []CaseDraftAssertion{
				{StepSeq: 2, Kind: "ui", Target: "验收标准", Expected: "全部验收标准逐条满足"},
			}
	case GapDimensionBoundary:
		return []CaseDraftStep{
				{Seq: 1, Action: "使用边界输入（最小值、最大值、超长文本）执行主流程", Expected: "系统按声明的边界规则处理输入"},
				{Seq: 2, Action: "检查边界外的输入被拒绝并给出可理解的提示", Expected: "越界输入被明确拒绝，无静默截断"},
			}, []CaseDraftAssertion{
				{StepSeq: 2, Kind: "ui", Target: "边界校验", Expected: "边界输入被正确处理并给出可验证反馈"},
			}
	case GapDimensionPermission:
		return []CaseDraftStep{
				{Seq: 1, Action: "使用未授权角色访问需求涉及的页面/API", Expected: "访问被拒绝且不泄露受保护数据"},
				{Seq: 2, Action: "使用已授权角色执行同一操作", Expected: "授权角色可以完成操作"},
			}, []CaseDraftAssertion{
				{StepSeq: 1, Kind: "api", Target: "未授权访问", Expected: "返回拒绝（401/403）"},
				{StepSeq: 2, Kind: "ui", Target: "授权访问", Expected: "授权角色正常完成操作"},
			}
	case GapDimensionException:
		return []CaseDraftStep{
				{Seq: 1, Action: "注入需求可能遇到的异常（服务不可用/超时/非法输入）", Expected: "系统给出明确的错误提示或降级行为"},
				{Seq: 2, Action: "在异常恢复后重试同一操作", Expected: "操作可重试且数据保持一致"},
			}, []CaseDraftAssertion{
				{StepSeq: 1, Kind: "ui", Target: "异常提示", Expected: "错误提示与需求约定一致"},
				{StepSeq: 2, Kind: "api", Target: "重试一致性", Expected: "重试不产生重复数据或脏状态"},
			}
	case GapDimensionData:
		return []CaseDraftStep{
				{Seq: 1, Action: "准备需求所需的前置数据与测试账号", Expected: "前置数据可复现且不含真实密钥"},
				{Seq: 2, Action: "使用准备好的数据执行主流程", Expected: "主流程可以稳定复跑"},
			}, []CaseDraftAssertion{
				{StepSeq: 2, Kind: "ui", Target: "数据准备", Expected: "前置数据满足需求且可复现"},
			}
	default:
		return []CaseDraftStep{
				{Seq: 1, Action: "确认需求依赖的页面/API 可用", Expected: "依赖项可用且版本匹配"},
				{Seq: 2, Action: "执行需求主流程并观察依赖失败的提示", Expected: "依赖不可用时给出明确失败信息"},
			}, []CaseDraftAssertion{
				{StepSeq: 1, Kind: "api", Target: "依赖可用性", Expected: "依赖页面/API 可达且返回成功"},
			}
	}
}

func draftScope(dimension GapDimension) (priority, preconditions string) {
	switch dimension {
	case GapDimensionPermission:
		return "P1", "已绑定有权限与无权限的两个测试账号"
	case GapDimensionException:
		return "P1", "可注入异常或可切换到不可用依赖的环境"
	case GapDimensionData:
		return "P2", "前置数据与测试账号可重复准备"
	case GapDimensionDependency:
		return "P1", "依赖系统/页面/API 已部署且可达"
	case GapDimensionBoundary:
		return "P2", "可构造边界输入（最小/最大/超长）"
	default:
		return "P2", "需求当前版本已确认"
	}
}

func draftDedupeKey(versionID, requirementID int64, dimension GapDimension) string {
	identity := fmt.Sprintf("%d|%d|%s", versionID, requirementID, dimension)
	sum := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(sum[:])
}

func normalizeDraftTitle(title string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(title))), " ")
}

func factInt64(fact version.Fact, key string) int64 {
	switch value := fact.Payload[key].(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err == nil {
			return parsed
		}
	}
	return 0
}
