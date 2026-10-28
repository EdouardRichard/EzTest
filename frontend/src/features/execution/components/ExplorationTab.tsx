"use client"

import { GitBranch, Loader2, RefreshCw } from "lucide-react"
import { useCallback, useEffect, useMemo, useRef, useState } from "react"

import { EmptyState } from "@/components/ui/EmptyState"
import { StatusBadge, type StatusBadgeVariant } from "@/components/ui/StatusBadge"
import { useIssues } from "@/features/delivery/hooks/useIssues"
import type { IssueSummary } from "@/features/delivery/types"
import { getGraph } from "@/features/mindmap/api"
import type { CaseNodeView, MindmapGraph } from "@/features/mindmap/types"
import { listRequirements } from "@/features/versions/api"
import type { Requirement } from "@/features/versions/types"
import type { VersionStreamState } from "@/features/versions/useVersionStream"

import {
  autoCollapsedRequirements,
  type ChainCaseInput,
  type ChainIssueInput,
  type ChainLayoutInput,
  type ChainNode,
  type ChainRequirementInput,
  type ChainResultFilter,
  type ChainResultInput,
  isAbnormalResultStatus,
  layoutChain,
  matchesResultFilter,
} from "../chain-layout"
import { useCaseRuns } from "../hooks/useExecution"
import type { CaseRun, CaseRunStatus } from "../types"
import { ChainGraph } from "./ChainGraph"
import { FailureClassBadge } from "./FailureClassBadge"

/** Requirements/issues fetched for the chain; 100 is the backend list cap. */
const REQUIREMENT_LIMIT = 100
const ISSUE_LIMIT = 100

const BUTTON_CLASS =
  "inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-1.5 text-xs font-medium text-foreground transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"

const FIELD_CLASS =
  "mt-1 w-full rounded-lg border border-border bg-input px-2.5 py-1.5 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"

const RESULT_STATUS_LABELS: Record<CaseRunStatus, string> = {
  pending: "待执行",
  running: "执行中",
  passed: "通过",
  failed: "失败",
  blocked: "阻塞",
  inconclusive: "不确定",
  skipped: "跳过",
  interrupted: "中断",
}

const RESULT_BADGE_VARIANTS: Record<CaseRunStatus, StatusBadgeVariant> = {
  pending: "info",
  running: "info",
  passed: "success",
  failed: "danger",
  blocked: "neutral",
  inconclusive: "warning",
  skipped: "neutral",
  interrupted: "danger",
}

const CASE_STATUS_LABELS: Record<string, string> = {
  draft: "草稿",
  reviewed: "已评审",
  executed: "已执行",
  supplemented: "已补充",
  merged: "已合并",
  rejected: "已驳回",
  needs_revalidation: "需重验",
}

const REQUIREMENT_STATUS_LABELS: Record<string, string> = {
  draft: "草稿",
  clarified: "已澄清",
  designed: "已设计",
  verified: "已验证",
  obsolete: "已废弃",
}

const ISSUE_STATUS_LABELS: Record<string, string> = {
  open: "待确认",
  confirmed: "已确认",
  dismissed: "已驳回",
}

const ISSUE_SEVERITY_LABELS: Record<string, string> = {
  blocker: "阻断",
  major: "严重",
  minor: "一般",
  info: "提示",
}

const ISSUE_CONFIDENCE_LABELS: Record<string, string> = {
  high: "高",
  medium: "中",
  low: "低",
}

const ISSUE_CATEGORY_LABELS: Record<string, string> = {
  defect: "应用缺陷",
  requirement_gap: "需求缺口",
  other: "其他",
}

const ISSUE_SEVERITY_BADGE_VARIANTS: Record<string, StatusBadgeVariant> = {
  blocker: "danger",
  major: "warning",
  minor: "info",
  info: "neutral",
}

const STATUS_FILTER_OPTIONS: readonly { value: ChainResultFilter; label: string }[] = [
  { value: "", label: "全部状态" },
  { value: "passed", label: "通过" },
  { value: "failed", label: "失败" },
  { value: "blocked", label: "阻塞" },
  { value: "inconclusive", label: "不确定" },
  { value: "skipped", label: "跳过" },
  { value: "interrupted", label: "中断" },
  { value: "pending", label: "待执行/执行中" },
]

/** Deep-link target of the node drawer (spec §10.5: 节点抽屉 + 深链). */
export type ExplorationDeepLink =
  | { kind: "requirement"; id: string }
  | { kind: "case"; id: string }
  | { kind: "result"; id: string }
  | { kind: "issue"; id: string }

export type ExplorationTabProps = {
  /** P3 version id; never re-parsed (spec §7.1). */
  versionId: string
  /**
   * Optional batch scope. Without it the chain still shows requirements and
   * cases but has no result layer; the results hook stays disabled.
   */
  batchId?: string
  /** Version stream owned by the tab container; the tab never opens its own. */
  stream?: VersionStreamState
  /** Opens one evidence item in the results tab's viewer. */
  onOpenEvidence?: (evidenceId: string) => void
  /** Results-tab deep link callback for requirement/case/result nodes. */
  onNavigate?: (target: ExplorationDeepLink) => void
}

function describeLoadError(error: unknown): string {
  if (error instanceof Error && error.message !== "") return error.message
  return "未知错误"
}

function formatDuration(durationMs: number | undefined): string {
  if (durationMs === undefined || durationMs <= 0) return ""
  if (durationMs < 1000) return `${durationMs} ms`
  return `${(durationMs / 1000).toFixed(1)} s`
}

function caseStatusLabel(status: string): string {
  return CASE_STATUS_LABELS[status] ?? (status === "" ? "—" : status)
}

function requirementStatusLabel(status: string): string {
  return REQUIREMENT_STATUS_LABELS[status] ?? (status === "" ? "—" : status)
}

function issueStatusLabel(status: string): string {
  return ISSUE_STATUS_LABELS[status] ?? (status === "" ? "—" : status)
}

function issueSeverityLabel(severity: string): string {
  return ISSUE_SEVERITY_LABELS[severity] ?? (severity === "" ? "—" : severity)
}

function issueConfidenceLabel(confidence: string): string {
  return ISSUE_CONFIDENCE_LABELS[confidence] ?? (confidence === "" ? "—" : confidence)
}

function issueCategoryLabel(category: string): string {
  return ISSUE_CATEGORY_LABELS[category] ?? (category === "" ? "—" : category)
}

/** Evidence refs of an issue summary; other ref kinds are not evidence. */
function issueEvidenceIds(issue: IssueSummary): string[] {
  return (issue.refs ?? [])
    .filter((ref) => ref.kind === "evidence" && ref.id !== "")
    .map((ref) => ref.id)
}

/** Maps one delivery issue to the chain issue layer (layout plus drawer data). */
function toChainIssue(issue: IssueSummary, index: number): ChainIssueInput {
  return {
    id: issue.id,
    title: issue.title,
    status: issue.status,
    severity: issue.severity,
    confidence: issue.confidence,
    category: issue.category,
    caseNodeId: issue.caseNodeId,
    caseRunId: issue.caseRunId,
    evidenceIds: issueEvidenceIds(issue),
    sortOrder: index,
  }
}

/** True when `candidate` is a newer attempt than `current` (tie-break by id). */
function isLaterResult(candidate: CaseRun, current: CaseRun | undefined): boolean {
  if (current === undefined) return true
  if (candidate.attempt !== current.attempt) return candidate.attempt > current.attempt
  return candidate.id > current.id
}

/** Latest case run per case node; retries do not stack in the chain. */
function latestResults(caseRuns: readonly CaseRun[]): CaseRun[] {
  const byCase = new Map<string, CaseRun>()
  for (const run of caseRuns) {
    const current = byCase.get(run.case_node_id)
    if (isLaterResult(run, current)) byCase.set(run.case_node_id, run)
  }
  return [...byCase.values()]
}

function requirementIdsByCase(
  graph: MindmapGraph | null,
  cases: readonly CaseNodeView[],
): Map<string, string[]> {
  const byCase = new Map<string, Set<string>>()
  function add(caseNodeId: string, requirementId: string): void {
    const ids = byCase.get(caseNodeId)
    if (ids === undefined) byCase.set(caseNodeId, new Set([requirementId]))
    else ids.add(requirementId)
  }

  for (const cell of graph?.coverage.matrix ?? []) {
    for (const caseId of cell.caseIds) add(caseId, cell.requirementId)
  }
  for (const node of cases) {
    for (const source of node.sources) {
      if (source.refKind === "requirement" && source.refId !== null && source.refId !== "") {
        add(node.id, source.refId)
      }
    }
  }

  const result = new Map<string, string[]>()
  for (const [caseId, ids] of byCase) result.set(caseId, [...ids].sort())
  return result
}

type ChainDrawerProps = {
  node: ChainNode
  onClose: () => void
  onOpenEvidence?: (evidenceId: string) => void
  onNavigate?: (target: ExplorationDeepLink) => void
}

/** Node drawer: summary, evidence and deep links (spec §10.5). */
function ChainDrawer({ node, onClose, onOpenEvidence, onNavigate }: ChainDrawerProps) {
  const kindLabel =
    node.kind === "requirement"
      ? "需求节点"
      : node.kind === "case"
        ? "用例节点"
        : node.kind === "issue"
          ? "问题节点"
          : "执行结果"
  const statusLabel =
    node.kind === "result"
      ? (RESULT_STATUS_LABELS[node.status as CaseRunStatus] ?? node.status)
      : node.kind === "case"
        ? caseStatusLabel(node.status)
        : node.kind === "issue"
          ? issueStatusLabel(node.status)
          : requirementStatusLabel(node.status)
  const badgeVariant =
    node.kind === "result"
      ? (RESULT_BADGE_VARIANTS[node.status as CaseRunStatus] ?? "neutral")
      : node.kind === "issue"
        ? (ISSUE_SEVERITY_BADGE_VARIANTS[node.issue?.severity ?? ""] ?? "neutral")
        : "neutral"
  const evidenceIds =
    node.kind === "result"
      ? (node.result?.evidenceIds ?? [])
      : node.kind === "issue"
        ? (node.issue?.evidenceIds ?? [])
        : []
  const resultCaseId = node.kind === "result" ? node.caseNodeId : null
  const summary =
    node.kind === "result" && (node.result?.summary ?? "") !== ""
      ? (node.result?.summary ?? "")
      : node.kind === "requirement"
        ? "该需求派生出的用例与执行结果如下。"
        : node.kind === "case"
          ? "该用例的执行结果与证据如下。"
          : node.kind === "issue"
            ? "该问题关联的用例执行结果可追溯证据；打开问题页查看复现步骤与处理记录。"
            : "该执行结果暂无摘要。"

  return (
    <aside
      data-testid="chain-drawer"
      data-kind={node.kind}
      role="dialog"
      aria-label={`${kindLabel}详情`}
      className="rounded-2xl border border-border bg-card p-4"
    >
      <header className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <p className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
            {kindLabel}
          </p>
          <h3 className="mt-1 break-words text-sm font-semibold">{node.title}</h3>
        </div>
        <button
          type="button"
          data-testid="chain-drawer-close"
          aria-label="关闭节点详情"
          onClick={onClose}
          className={BUTTON_CLASS}
        >
          关闭
        </button>
      </header>

      <div className="mt-3 flex flex-wrap items-center gap-2">
        <StatusBadge variant={badgeVariant}>{statusLabel}</StatusBadge>
        {node.kind === "result" && node.result?.failureClass != null ? (
          <FailureClassBadge failureClass={node.result.failureClass} />
        ) : null}
        {node.kind === "result" && formatDuration(node.result?.durationMs) !== "" ? (
          <span className="text-xs text-muted-foreground">
            {`耗时 ${formatDuration(node.result?.durationMs)}`}
          </span>
        ) : null}
        {node.kind === "issue" ? (
          <>
            <span className="text-xs text-muted-foreground">
              {`严重级：${issueSeverityLabel(node.issue?.severity ?? "")}`}
            </span>
            <span className="text-xs text-muted-foreground">
              {`置信度：${issueConfidenceLabel(node.issue?.confidence ?? "")}`}
            </span>
            <span className="text-xs text-muted-foreground">
              {`分类：${issueCategoryLabel(node.issue?.category ?? "")}`}
            </span>
          </>
        ) : null}
      </div>

      <p className="mt-3 text-sm leading-relaxed text-muted-foreground">{summary}</p>

      {node.kind === "requirement" ? (
        <p className="mt-2 text-xs text-muted-foreground">
          {`需求状态：${requirementStatusLabel(node.status)}`}
        </p>
      ) : null}
      {node.kind === "case" ? (
        <p className="mt-2 text-xs text-muted-foreground">
          {`用例状态：${caseStatusLabel(node.status)}`}
        </p>
      ) : null}

      <div className="mt-3 flex flex-wrap items-center gap-2">
        {node.kind === "requirement" ? (
          <button
            type="button"
            data-testid="chain-drawer-open-requirement"
            onClick={() => onNavigate?.({ kind: "requirement", id: node.id })}
            className={BUTTON_CLASS}
          >
            查看需求
          </button>
        ) : null}
        {node.kind === "case" ? (
          <button
            type="button"
            data-testid="chain-drawer-open-case"
            onClick={() => onNavigate?.({ kind: "case", id: node.id })}
            className={BUTTON_CLASS}
          >
            查看用例
          </button>
        ) : null}
        {node.kind === "issue" ? (
          <>
            <button
              type="button"
              data-testid="chain-drawer-open-issue"
              onClick={() => onNavigate?.({ kind: "issue", id: node.id })}
              className={BUTTON_CLASS}
            >
              查看问题
            </button>
            {node.caseNodeId === null ? null : (
              <button
                type="button"
                data-testid="chain-drawer-open-case"
                onClick={() => onNavigate?.({ kind: "case", id: node.caseNodeId ?? "" })}
                className={BUTTON_CLASS}
              >
                查看用例
              </button>
            )}
          </>
        ) : null}
        {node.kind === "result" && resultCaseId !== null ? (
          <>
            <button
              type="button"
              data-testid="chain-drawer-open-case"
              onClick={() => onNavigate?.({ kind: "case", id: resultCaseId })}
              className={BUTTON_CLASS}
            >
              查看用例
            </button>
            <button
              type="button"
              data-testid="chain-drawer-open-result"
              onClick={() => onNavigate?.({ kind: "result", id: node.id })}
              className={BUTTON_CLASS}
            >
              打开执行结果
            </button>
          </>
        ) : null}
      </div>

      <section data-testid="chain-drawer-evidence" className="mt-4">
        <h4 className="text-xs font-semibold">证据</h4>
        {evidenceIds.length === 0 ? (
          <p className="mt-1 text-xs text-muted-foreground">该节点暂无可查看证据。</p>
        ) : (
          <ul className="mt-1.5 flex flex-wrap gap-1.5">
            {evidenceIds.map((evidenceId) => (
              <li key={evidenceId}>
                <button
                  type="button"
                  data-testid={`chain-drawer-evidence-${evidenceId}`}
                  onClick={() => onOpenEvidence?.(evidenceId)}
                  className={BUTTON_CLASS}
                >
                  {`证据 ${evidenceId}`}
                </button>
              </li>
            ))}
          </ul>
        )}
      </section>

      <section
        data-testid="chain-drawer-issues"
        className="mt-4 rounded-xl border border-dashed border-border px-3 py-2"
      >
        <h4 className="text-xs font-semibold">问题</h4>
        <p className="mt-1 text-xs text-muted-foreground">
          {node.kind === "issue"
            ? "问题状态与处理结果以问题页为准。"
            : "该节点关联的问题节点会以红色菱形显示在链路图中。"}
        </p>
      </section>
    </aside>
  )
}

/**
 * Exploration chain tab (spec §10.5): requirement → case → result → issue
 * layers drawn as hand-written SVG, status colour plus glyph/text, AI-supplement
 * and needs_revalidation styles, requirement/status/abnormal/batch filters, a
 * node drawer with evidence and deep-link callbacks, and per-requirement
 * collapse (auto-collapsed above 200 nodes). Issue data comes from the delivery
 * `/issues` API (version-scoped, T18) and is merged into the same layout.
 */
export function ExplorationTab({
  versionId,
  batchId,
  stream,
  onOpenEvidence,
  onNavigate,
}: ExplorationTabProps) {
  const [requirements, setRequirements] = useState<Requirement[]>([])
  const [graph, setGraph] = useState<MindmapGraph | null>(null)
  const [metaLoading, setMetaLoading] = useState(true)
  const [requirementsError, setRequirementsError] = useState("")
  const [graphError, setGraphError] = useState("")
  const [requirementFilter, setRequirementFilter] = useState("")
  const [statusFilter, setStatusFilter] = useState<ChainResultFilter>("")
  const [abnormalOnly, setAbnormalOnly] = useState(false)
  const [issueBatchFilter, setIssueBatchFilter] = useState<"current" | "all">("current")
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [collapsed, setCollapsed] = useState<ReadonlySet<string> | null>(null)
  const requestRef = useRef(0)

  const loadMeta = useCallback(async (): Promise<void> => {
    const requestId = ++requestRef.current
    setMetaLoading(true)
    setRequirementsError("")
    setGraphError("")
    try {
      // Load the two layers independently: one failing request must not discard
      // the other layer's data. Each failure is surfaced by name below.
      const [requirementsResult, graphResult] = await Promise.allSettled([
        listRequirements(versionId, { limit: REQUIREMENT_LIMIT }),
        getGraph(versionId),
      ])
      if (requestRef.current !== requestId) return
      if (requirementsResult.status === "fulfilled") {
        setRequirements(requirementsResult.value.items)
      } else {
        setRequirementsError(describeLoadError(requirementsResult.reason))
      }
      if (graphResult.status === "fulfilled") {
        setGraph(graphResult.value)
      } else {
        setGraphError(describeLoadError(graphResult.reason))
      }
    } finally {
      if (requestRef.current === requestId) setMetaLoading(false)
    }
  }, [versionId])

  useEffect(() => {
    void loadMeta()
    return () => {
      // Invalidate a request still in flight for this mount/version.
      requestRef.current += 1
    }
  }, [loadMeta])

  const {
    caseRuns,
    loading: runsLoading,
    error: runsError,
    refresh,
  } = useCaseRuns(versionId, batchId ?? "", {}, stream)

  // Without a batch scope there is nothing to narrow to, so the filter is "all".
  const effectiveIssueBatchFilter: "current" | "all" =
    batchId === undefined || batchId === "" ? "all" : issueBatchFilter
  const issueQuery = useIssues(
    versionId,
    effectiveIssueBatchFilter === "current" && batchId !== undefined
      ? { batchId, limit: ISSUE_LIMIT }
      : { limit: ISSUE_LIMIT },
    stream,
  )

  // A version or batch switch starts a fresh view.
  // biome-ignore lint/correctness/useExhaustiveDependencies: switching scope must reset the view state even though no value is read.
  useEffect(() => {
    setRequirementFilter("")
    setStatusFilter("")
    setAbnormalOnly(false)
    setIssueBatchFilter("current")
    setSelectedId(null)
    setCollapsed(null)
  }, [versionId, batchId])

  const requirementInputs = useMemo<ChainRequirementInput[]>(
    () =>
      requirements.map((requirement, index) => ({
        id: requirement.id,
        title: requirement.title,
        status: requirement.status,
        sortOrder: index,
      })),
    [requirements],
  )

  const caseInputs = useMemo<ChainCaseInput[]>(() => {
    if (graph === null) return []
    const caseNodes = graph.nodes.filter((node) => node.kind === "case")
    const requirementIds = requirementIdsByCase(graph, caseNodes)
    return caseNodes.map((node, index) => ({
      id: node.id,
      title: node.title,
      requirementIds: requirementIds.get(node.id) ?? [],
      status: node.status,
      sortOrder: index,
      aiSupplemented: node.source === "ai_supplement",
    }))
  }, [graph])

  const resultInputs = useMemo<ChainResultInput[]>(
    () =>
      latestResults(caseRuns).map((run, index) => ({
        id: run.id,
        caseNodeId: run.case_node_id,
        status: run.status,
        title: run.title,
        summary: run.summary,
        durationMs: run.duration_ms,
        failureClass: run.failure_class,
        evidenceIds: run.evidence.map((evidence) => evidence.id),
        sortOrder: index,
      })),
    [caseRuns],
  )

  const issueInputs = useMemo<ChainIssueInput[]>(
    () => issueQuery.items.map((issue, index) => toChainIssue(issue, index)),
    [issueQuery.items],
  )

  const baseInput = useMemo<ChainLayoutInput>(
    () => ({
      requirements: requirementInputs,
      cases: caseInputs,
      results: resultInputs,
      issues: issueInputs,
    }),
    [requirementInputs, caseInputs, resultInputs, issueInputs],
  )

  const filteredInput = useMemo<ChainLayoutInput>(() => {
    let nextRequirements = baseInput.requirements
    let nextCases = baseInput.cases
    let nextResults = baseInput.results ?? []
    let nextIssues = baseInput.issues ?? []

    if (requirementFilter !== "") {
      nextRequirements = nextRequirements.filter((row) => row.id === requirementFilter)
      nextCases = nextCases.filter((row) => (row.requirementIds ?? []).includes(requirementFilter))
      const caseIds = new Set(nextCases.map((row) => row.id))
      nextResults = nextResults.filter((row) => caseIds.has(row.caseNodeId))
    }

    if (statusFilter !== "" || abnormalOnly) {
      nextResults = nextResults.filter(
        (row) =>
          matchesResultFilter(row.status, statusFilter) &&
          (!abnormalOnly || isAbnormalResultStatus(row.status)),
      )
      const resultCaseIds = new Set(nextResults.map((row) => row.caseNodeId))
      nextCases = nextCases.filter((row) => resultCaseIds.has(row.id))
      if (requirementFilter === "") {
        const remainingRequirementIds = new Set(
          nextCases.flatMap((row) => row.requirementIds ?? []),
        )
        nextRequirements = nextRequirements.filter((row) => remainingRequirementIds.has(row.id))
      }
    }

    if (requirementFilter !== "" || statusFilter !== "" || abnormalOnly) {
      // Graph issues follow their case; unlinked issues stay in the side list.
      const nextCaseIds = new Set(nextCases.map((row) => row.id))
      const resultsByRun = new Map((baseInput.results ?? []).map((row) => [row.id, row.caseNodeId]))
      nextIssues = nextIssues.filter((issue) => {
        const caseNodeId =
          issue.caseNodeId !== null && issue.caseNodeId !== undefined && issue.caseNodeId !== ""
            ? issue.caseNodeId
            : (resultsByRun.get(issue.caseRunId ?? "") ?? null)
        return caseNodeId === null ? true : nextCaseIds.has(caseNodeId)
      })
    }

    return {
      requirements: nextRequirements,
      cases: nextCases,
      results: nextResults,
      issues: nextIssues,
    }
  }, [baseInput, requirementFilter, statusFilter, abnormalOnly])

  const autoCollapsed = useMemo(() => autoCollapsedRequirements(filteredInput), [filteredInput])
  const collapsedSet = useMemo(
    () => collapsed ?? new Set(autoCollapsed),
    [collapsed, autoCollapsed],
  )

  const layout = useMemo(
    () => layoutChain(filteredInput, { collapsed: collapsedSet }),
    [filteredInput, collapsedSet],
  )

  const selectedNode =
    selectedId === null ? null : (layout.nodes.find((node) => node.id === selectedId) ?? null)

  const toggleRequirement = useCallback(
    (requirementId: string): void => {
      setCollapsed((previous) => {
        const next = new Set(previous ?? autoCollapsed)
        if (next.has(requirementId)) next.delete(requirementId)
        else next.add(requirementId)
        return next
      })
    },
    [autoCollapsed],
  )

  const handleSelect = useCallback((node: ChainNode): void => {
    setSelectedId(node.id)
  }, [])

  const loading = metaLoading || (batchId !== undefined && batchId !== "" && runsLoading)
  const chainLayerErrors = [
    { layer: "需求层", message: requirementsError },
    { layer: "用例层", message: graphError },
    { layer: "结果层", message: runsError },
  ].filter((entry) => entry.message !== "")
  const chainErrorText = chainLayerErrors
    .map((entry) => `${entry.layer}：${entry.message}`)
    .join("；")
  const hasChainError = chainLayerErrors.length > 0
  const issuesUnavailable = issueQuery.error !== ""
  const hasData = layout.nodes.length > 0
  const hasUnfilteredData =
    baseInput.requirements.length + baseInput.cases.length + (baseInput.results?.length ?? 0) > 0

  function retry(): void {
    void loadMeta()
    void refresh()
    void issueQuery.refresh()
  }

  return (
    <section data-testid="exploration-tab" className="space-y-3">
      <header className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <h2 className="text-base font-semibold tracking-tight">探索链路</h2>
          <p className="mt-0.5 text-xs text-muted-foreground">
            需求 → 用例 → 结果 → 问题的血缘图；红色菱形为问题，虚线 AI
            角标为补全用例，黄色描边为需重验用例。拖拽平移、滚轮缩放、按需求折叠。
          </p>
        </div>
        <div className="flex items-center gap-2">
          <p data-testid="exploration-visible-count" className="text-xs text-muted-foreground">
            {`可见 ${layout.visibleIds.length} 个节点${
              layout.hiddenIds.length > 0 ? `，折叠 ${layout.hiddenIds.length} 个` : ""
            }`}
          </p>
          <button
            type="button"
            data-testid="exploration-refresh"
            onClick={retry}
            disabled={loading}
            className={BUTTON_CLASS}
          >
            <RefreshCw className="h-3.5 w-3.5" aria-hidden="true" />
            刷新
          </button>
        </div>
      </header>

      <div className="grid gap-2 rounded-2xl border border-border bg-card p-3 sm:grid-cols-2 lg:grid-cols-5">
        <label className="text-xs font-medium text-muted-foreground">
          需求
          <select
            data-testid="exploration-filter-requirement"
            value={requirementFilter}
            onChange={(event) => setRequirementFilter(event.target.value)}
            className={FIELD_CLASS}
          >
            <option value="">全部需求</option>
            {baseInput.requirements.map((row) => (
              <option key={row.id} value={row.id}>
                {row.title}
              </option>
            ))}
          </select>
        </label>

        <label className="text-xs font-medium text-muted-foreground">
          结果状态
          <select
            data-testid="exploration-filter-status"
            value={statusFilter}
            onChange={(event) => setStatusFilter(event.target.value as ChainResultFilter)}
            className={FIELD_CLASS}
          >
            {STATUS_FILTER_OPTIONS.map((option) => (
              <option key={option.value} value={option.value}>
                {option.label}
              </option>
            ))}
          </select>
        </label>

        <label className="text-xs font-medium text-muted-foreground">
          批次
          <select
            data-testid="chain-batch-filter"
            value={effectiveIssueBatchFilter}
            onChange={(event) =>
              setIssueBatchFilter(event.target.value === "current" ? "current" : "all")
            }
            className={FIELD_CLASS}
          >
            <option value="current" disabled={batchId === undefined || batchId === ""}>
              当前批次
            </option>
            <option value="all">全部批次</option>
          </select>
        </label>

        <label className="flex items-end gap-2 pb-1.5 text-xs font-medium text-muted-foreground">
          <input
            type="checkbox"
            data-testid="exploration-filter-abnormal"
            checked={abnormalOnly}
            onChange={(event) => setAbnormalOnly(event.target.checked)}
            className="h-4 w-4 rounded border-border"
          />
          仅异常
        </label>
      </div>

      {hasChainError ? (
        <div
          data-testid={hasData ? "exploration-degraded" : "exploration-error"}
          role="alert"
          className={
            hasData
              ? "flex flex-wrap items-center justify-between gap-2 rounded-xl border border-amber-500/25 bg-amber-500/10 px-4 py-3 text-sm text-amber-700 dark:text-amber-300"
              : "flex flex-wrap items-center justify-between gap-2 rounded-xl border border-destructive/30 bg-destructive/10 px-4 py-3 text-sm text-destructive"
          }
        >
          <p>
            {hasData
              ? `部分图层加载失败（${chainErrorText}）。下图仅展示已成功加载的图层。`
              : `加载探索链路失败：${chainErrorText}`}
          </p>
          <button type="button" onClick={retry} className={BUTTON_CLASS}>
            重试
          </button>
        </div>
      ) : null}

      {loading && !hasData ? (
        <p
          data-testid="exploration-loading"
          role="status"
          className="flex items-center gap-2 rounded-xl border border-dashed border-border px-4 py-6 text-sm text-muted-foreground"
        >
          <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
          探索链路加载中…
        </p>
      ) : null}

      {!loading && !hasChainError && !hasData && !hasUnfilteredData ? (
        <div data-testid="exploration-empty">
          <EmptyState
            icon={GitBranch}
            title="暂无探索链路"
            description="版本的需求、用例与执行结果会在这里形成需求 → 用例 → 结果链路。"
          />
        </div>
      ) : null}

      {!loading && !hasChainError && !hasData && hasUnfilteredData ? (
        <p
          data-testid="exploration-filtered-empty"
          className="rounded-xl border border-dashed border-border px-4 py-10 text-center text-sm text-muted-foreground"
        >
          没有符合筛选条件的链路节点
        </p>
      ) : null}

      {hasData ? (
        <div className="grid gap-3 xl:grid-cols-[minmax(0,1fr)_320px]">
          <ChainGraph
            layout={layout}
            selectedId={selectedId}
            onSelect={handleSelect}
            onToggleRequirement={toggleRequirement}
          />

          <div className="space-y-3">
            {selectedNode === null ? (
              <aside
                data-testid="chain-drawer-hint"
                className="rounded-2xl border border-dashed border-border bg-card/60 px-4 py-6 text-sm text-muted-foreground"
              >
                点击节点查看摘要、证据与深链。
              </aside>
            ) : (
              <ChainDrawer
                node={selectedNode}
                onClose={() => setSelectedId(null)}
                onOpenEvidence={onOpenEvidence}
                onNavigate={onNavigate}
              />
            )}
          </div>
        </div>
      ) : null}

      <aside
        data-testid="chain-issue-panel"
        className="rounded-2xl border border-border bg-card px-4 py-3"
      >
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h3 className="text-sm font-semibold">问题节点</h3>
          <p
            data-testid="chain-issue-count"
            data-state={issuesUnavailable ? "degraded" : "ready"}
            className="text-xs text-muted-foreground"
          >
            {issuesUnavailable ? "问题数据不可用" : `${issueInputs.length} 个问题`}
          </p>
        </div>

        {issuesUnavailable ? (
          <p
            data-testid="chain-issues-degraded"
            role="alert"
            className="mt-2 rounded-lg border border-amber-500/25 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300"
          >
            {`问题数据加载失败：${issueQuery.error} 问题节点可能不完整（AI 分析降级时同样可能不完整）。`}
          </p>
        ) : issueQuery.loading && issueQuery.items.length === 0 ? (
          <p
            data-testid="chain-issues-loading"
            role="status"
            className="mt-2 flex items-center gap-2 text-xs text-muted-foreground"
          >
            <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
            问题加载中…
          </p>
        ) : issueQuery.items.length === 0 ? (
          <p data-testid="chain-issues-empty" className="mt-2 text-xs text-muted-foreground">
            本批次未发现问题。
          </p>
        ) : (
          <p className="mt-2 text-xs text-muted-foreground">
            {`红色菱形为问题节点（${
              effectiveIssueBatchFilter === "current" ? "当前批次" : "全部批次"
            }）；点击节点查看摘要、证据与深链。`}
          </p>
        )}

        {layout.unlinkedIssues.length === 0 ? null : (
          <div data-testid="chain-unlinked-issues" className="mt-3">
            <h4 className="text-xs font-semibold">未关联用例的问题</h4>
            <ul className="mt-1.5 flex flex-wrap gap-1.5">
              {layout.unlinkedIssues.map((issue) => (
                <li key={issue.id}>
                  <button
                    type="button"
                    data-testid={`chain-unlinked-issue-${issue.id}`}
                    onClick={() => onNavigate?.({ kind: "issue", id: issue.id })}
                    className={BUTTON_CLASS}
                  >
                    {`${issueSeverityLabel(issue.severity ?? "")} · ${issue.title}`}
                  </button>
                </li>
              ))}
            </ul>
          </div>
        )}
      </aside>
    </section>
  )
}
