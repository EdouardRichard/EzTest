"use client"

import { GitBranch, Loader2, RefreshCw } from "lucide-react"
import { useCallback, useEffect, useMemo, useRef, useState } from "react"

import { EmptyState } from "@/components/ui/EmptyState"
import { StatusBadge, type StatusBadgeVariant } from "@/components/ui/StatusBadge"
import { getGraph } from "@/features/mindmap/api"
import type { CaseNodeView, MindmapGraph } from "@/features/mindmap/types"
import { listRequirements } from "@/features/versions/api"
import type { Requirement } from "@/features/versions/types"
import type { VersionStreamState } from "@/features/versions/useVersionStream"

import {
  autoCollapsedRequirements,
  type ChainCaseInput,
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

/** Requirements fetched for the chain; comfortably below the auto-collapse scale. */
const REQUIREMENT_LIMIT = 200

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

/** Deep-link target of the node drawer (spec §9.3: 节点抽屉 + 深链). */
export type ExplorationDeepLink =
  | { kind: "requirement"; id: string }
  | { kind: "case"; id: string }
  | { kind: "result"; id: string }

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

/** Node drawer: summary, evidence and deep links (spec §9.3). */
function ChainDrawer({ node, onClose, onOpenEvidence, onNavigate }: ChainDrawerProps) {
  const kindLabel =
    node.kind === "requirement" ? "需求节点" : node.kind === "case" ? "用例节点" : "执行结果"
  const statusLabel =
    node.kind === "result"
      ? (RESULT_STATUS_LABELS[node.status as CaseRunStatus] ?? node.status)
      : node.kind === "case"
        ? caseStatusLabel(node.status)
        : requirementStatusLabel(node.status)
  const badgeVariant =
    node.kind === "result"
      ? (RESULT_BADGE_VARIANTS[node.status as CaseRunStatus] ?? "neutral")
      : "neutral"
  const evidenceIds = node.kind === "result" ? (node.result?.evidenceIds ?? []) : []
  const resultCaseId = node.kind === "result" ? node.caseNodeId : null
  const summary =
    node.kind === "result" && (node.result?.summary ?? "") !== ""
      ? (node.result?.summary ?? "")
      : node.kind === "requirement"
        ? "该需求派生出的用例与执行结果如下。"
        : node.kind === "case"
          ? "该用例的执行结果与证据如下。"
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
          问题节点由 P7 提供，P6 不抓取问题数据（空态）。
        </p>
      </section>
    </aside>
  )
}

/**
 * Exploration chain tab (spec §9.3): requirement → case → result layers drawn
 * as hand-written SVG, status colour plus glyph/text, requirement/status/
 * abnormal filters, a node drawer with evidence and deep-link callbacks, and
 * per-requirement collapse (auto-collapsed above 200 nodes). Issue data is
 * never fetched — the P7 empty state is rendered instead.
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
  const [metaError, setMetaError] = useState("")
  const [requirementFilter, setRequirementFilter] = useState("")
  const [statusFilter, setStatusFilter] = useState<ChainResultFilter>("")
  const [abnormalOnly, setAbnormalOnly] = useState(false)
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [collapsed, setCollapsed] = useState<ReadonlySet<string> | null>(null)
  const requestRef = useRef(0)

  const loadMeta = useCallback(async (): Promise<void> => {
    const requestId = ++requestRef.current
    setMetaLoading(true)
    setMetaError("")
    try {
      const [page, view] = await Promise.all([
        listRequirements(versionId, { limit: REQUIREMENT_LIMIT }),
        getGraph(versionId),
      ])
      if (requestRef.current !== requestId) return
      setRequirements(page.items)
      setGraph(view)
    } catch (caught) {
      if (requestRef.current !== requestId) return
      setMetaError(describeLoadError(caught))
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

  // A version or batch switch starts a fresh view.
  // biome-ignore lint/correctness/useExhaustiveDependencies: switching scope must reset the view state even though no value is read.
  useEffect(() => {
    setRequirementFilter("")
    setStatusFilter("")
    setAbnormalOnly(false)
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

  const baseInput = useMemo<ChainLayoutInput>(
    () => ({
      requirements: requirementInputs,
      cases: caseInputs,
      results: resultInputs,
    }),
    [requirementInputs, caseInputs, resultInputs],
  )

  const filteredInput = useMemo<ChainLayoutInput>(() => {
    let nextRequirements = baseInput.requirements
    let nextCases = baseInput.cases
    let nextResults = baseInput.results ?? []

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

    return { requirements: nextRequirements, cases: nextCases, results: nextResults }
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
  const error = metaError !== "" ? metaError : runsError
  const hasData = layout.nodes.length > 0
  const hasUnfilteredData =
    baseInput.requirements.length + baseInput.cases.length + (baseInput.results?.length ?? 0) > 0

  function retry(): void {
    void loadMeta()
    void refresh()
  }

  return (
    <section data-testid="exploration-tab" className="space-y-3">
      <header className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <h2 className="text-base font-semibold tracking-tight">探索链路</h2>
          <p className="mt-0.5 text-xs text-muted-foreground">
            需求 → 用例 → 结果的血缘图；问题节点由 P7 提供。拖拽平移、滚轮缩放、按需求折叠。
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

      <div className="grid gap-2 rounded-2xl border border-border bg-card p-3 sm:grid-cols-2 lg:grid-cols-4">
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

      {error === "" ? null : (
        <div
          data-testid="exploration-error"
          role="alert"
          className="flex flex-wrap items-center justify-between gap-2 rounded-xl border border-destructive/30 bg-destructive/10 px-4 py-3 text-sm text-destructive"
        >
          <p>{`加载探索链路失败：${error}`}</p>
          <button type="button" onClick={retry} className={BUTTON_CLASS}>
            重试
          </button>
        </div>
      )}

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

      {!loading && error === "" && !hasData && !hasUnfilteredData ? (
        <div data-testid="exploration-empty">
          <EmptyState
            icon={GitBranch}
            title="暂无探索链路"
            description="版本的需求、用例与执行结果会在这里形成需求 → 用例 → 结果链路。"
          />
        </div>
      ) : null}

      {!loading && error === "" && !hasData && hasUnfilteredData ? (
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

            <aside
              data-testid="chain-issue-placeholder"
              className="rounded-2xl border border-dashed border-border bg-card/60 px-4 py-3"
            >
              <h3 className="text-sm font-semibold">问题节点（P7）</h3>
              <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
                P7 提供问题数据；P6 不抓取问题节点，当前显示为空态。
              </p>
            </aside>
          </div>
        </div>
      ) : null}
    </section>
  )
}
