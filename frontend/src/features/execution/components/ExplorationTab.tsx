"use client"

import { Loader2, RefreshCw } from "lucide-react"
import dynamic from "next/dynamic"
import { useCallback, useEffect, useMemo, useRef, useState } from "react"

import type { VersionStreamState } from "@/features/versions/useVersionStream"
import { ChainLegend } from "../chain/ChainLegend"
import { ChainListView } from "../chain/ChainListView"
import { ChainSheet } from "../chain/ChainSheet"
import {
  ChainDegraded,
  ChainEmpty,
  ChainError,
  ChainIssuesDegraded,
  ChainSkeleton,
  ChainStreamStatus,
  ChainTruncated,
} from "../chain/ChainStates"
import { ChainToolbar, type ChainToolbarValue } from "../chain/ChainToolbar"
import { autoCollapsedChainRequirements, layoutChain } from "../chain/chain-layout"
import { getIssueSeverityToken } from "../chain/chain-tokens"
import type { ChainSnapshotNode } from "../chain/chain-types"
import { CHAIN_RESULT_STATUS_FILTERS, chainSnapshotTotal } from "../chain/chain-types"
import { useExplorationChain } from "../chain/use-exploration-chain"

/**
 * React Flow must never render during SSR: the canvas is a Client Component
 * that imports its stylesheet and measures the DOM. The dynamic boundary also
 * keeps the graph bundle out of the first load (spec §5.3/§5.8).
 */
const ChainCanvas = dynamic(() => import("../chain/ChainCanvas"), {
  ssr: false,
  loading: () => <ChainSkeleton className="h-full min-h-[420px]" />,
})

const BUTTON_CLASS =
  "inline-flex min-h-9 items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-1.5 text-xs font-medium text-foreground transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"

/** Debounce of the `q` server filter (spec §5.5). */
const SEARCH_DEBOUNCE_MS = 300

/** URL-owned filter subset; the page persists these in the query string. */
export type ExplorationFilters = {
  requirementId: string
  resultStatus: string
  abnormalOnly: boolean
  q: string
}

/** Deep-link target of the node Sheet (spec §5.7). */
export type ExplorationDeepLink =
  | { kind: "requirement"; id: string }
  | { kind: "case"; id: string }
  | { kind: "result"; id: string }
  | { kind: "issue"; id: string }

export type ExplorationTabProps = {
  /** P3 version id; never re-parsed (spec §7.1). */
  versionId: string
  /** Selected batch; `undefined`/empty lets the read model use its latest batch. */
  batchId?: string
  /** Version stream owned by the tab container; the tab never opens its own. */
  stream?: VersionStreamState
  /** Opens one evidence item in the results tab's viewer. */
  onOpenEvidence?: (evidenceId: string) => void
  /** Results-tab deep link callback for requirement/case/result nodes. */
  onNavigate?: (target: ExplorationDeepLink) => void
  /** Filter values read from the URL deep link; used to seed the local filters. */
  initialFilters?: Partial<ExplorationFilters>
  /** Called whenever the URL-owned filters change so the page can sync the URL. */
  onFiltersChange?: (filters: ExplorationFilters) => void
  /** Deep-linked node (`<kind>:<id>`); when set this Sheet is open. */
  selectedNodeId?: string
  /** Called when the user opens/closes a node Sheet so the page can sync the URL. */
  onSelectedNodeChange?: (nodeId: string) => void
}

type ResultStatusFilter = "" | (typeof CHAIN_RESULT_STATUS_FILTERS)[number]

const CHAIN_NODE_KINDS = new Set<ChainSnapshotNode["kind"]>([
  "requirement",
  "case",
  "result",
  "issue",
])

function normalizeStatusFilter(value: string | undefined): ResultStatusFilter {
  if (value === undefined) return ""
  return CHAIN_RESULT_STATUS_FILTERS.includes(value as (typeof CHAIN_RESULT_STATUS_FILTERS)[number])
    ? (value as ResultStatusFilter)
    : ""
}

/** Parses the backend node/deep-link id `<kind>:<decimal id>`. */
function parseChainNodeId(
  raw: string | null | undefined,
): { kind: ChainSnapshotNode["kind"]; id: string } | null {
  if (raw === null || raw === undefined) return null
  const separator = raw.indexOf(":")
  if (separator <= 0 || separator === raw.length - 1) return null
  const kind = raw.slice(0, separator)
  const id = raw.slice(separator + 1)
  if (!CHAIN_NODE_KINDS.has(kind as ChainSnapshotNode["kind"]) || id === "") return null
  return { kind: kind as ChainSnapshotNode["kind"], id }
}

function domainIdOf(nodeId: string): string {
  const separator = nodeId.indexOf(":")
  return separator === -1 ? nodeId : nodeId.slice(separator + 1)
}

function mergeRequirementOptions(
  previous: readonly { id: string; title: string }[],
  incoming: readonly { id: string; title: string }[],
): { id: string; title: string }[] {
  const byId = new Map<string, { id: string; title: string }>()
  for (const row of previous) byId.set(row.id, row)
  for (const row of incoming) byId.set(row.id, row)
  return [...byId.values()]
}

/**
 * Exploration chain tab (spec §5.1/§5.5/§5.7): one read-model snapshot drives
 * the requirement → case → result → issue canvas. The tab owns the data layer,
 * filters, deep links and Sheet; the canvas only renders. A full-width React
 * Flow canvas replaces the former fixed 320px side column, the legend/toolbar
 * float above it, and a keyboard tree plus a list-view fallback keep the graph
 * usable without a pointer.
 */
export function ExplorationTab({
  versionId,
  batchId,
  stream,
  onOpenEvidence,
  onNavigate,
  initialFilters,
  onFiltersChange,
  selectedNodeId,
  onSelectedNodeChange,
}: ExplorationTabProps) {
  const initialRequirementId = initialFilters?.requirementId ?? ""
  const initialStatus = normalizeStatusFilter(initialFilters?.resultStatus)
  const initialAbnormal = initialFilters?.abnormalOnly === true
  const initialSearch = initialFilters?.q ?? ""

  const [requirementFilter, setRequirementFilter] = useState(initialRequirementId)
  const [statusFilter, setStatusFilter] = useState<ResultStatusFilter>(initialStatus)
  const [abnormalOnly, setAbnormalOnly] = useState(initialAbnormal)
  const [searchInput, setSearchInput] = useState(initialSearch)
  const [searchQuery, setSearchQuery] = useState(initialSearch)
  const [issueStatusFilter, setIssueStatusFilter] = useState("")
  const [issueSeverityFilter, setIssueSeverityFilter] = useState("")
  const [includeSupplement, setIncludeSupplement] = useState(true)
  const [revalidationOnly, setRevalidationOnly] = useState(false)
  const [batchScope, setBatchScope] = useState<"current" | "all">("current")
  const [follow, setFollow] = useState(false)
  const [listView, setListView] = useState(false)
  const [requirementOptions, setRequirementOptions] = useState<{ id: string; title: string }[]>([])
  const [selectedNodeLocal, setSelectedNodeLocal] = useState(selectedNodeId ?? "")
  const [collapsed, setCollapsed] = useState<ReadonlySet<string> | null>(null)

  const onFiltersChangeRef = useRef(onFiltersChange)
  useEffect(() => {
    onFiltersChangeRef.current = onFiltersChange
  }, [onFiltersChange])

  const effectiveBatchId = batchScope === "all" ? "all" : (batchId ?? "")
  const chain = useExplorationChain(
    versionId,
    {
      batchId: effectiveBatchId,
      requirementId: requirementFilter,
      resultStatus: statusFilter,
      abnormalOnly,
      issueStatus: issueStatusFilter,
      issueSeverity: issueSeverityFilter,
      q: searchQuery,
      includeSupplement,
      revalidationOnly,
    },
    stream,
  )

  // Persist the URL-owned filters after every change (and on first mount, which
  // normalizes a shared link into the canonical form).
  useEffect(() => {
    onFiltersChangeRef.current?.({
      requirementId: requirementFilter,
      resultStatus: statusFilter,
      abnormalOnly,
      q: searchQuery,
    })
  }, [requirementFilter, statusFilter, abnormalOnly, searchQuery])

  // The title search hits the server; debounce it for 300ms (spec §5.5).
  useEffect(() => {
    if (searchInput === searchQuery) return
    const timer = setTimeout(() => {
      setSearchQuery(searchInput.trim())
    }, SEARCH_DEBOUNCE_MS)
    return () => clearTimeout(timer)
  }, [searchInput, searchQuery])

  // Keep the requirement dropdown populated across filtered responses: a
  // single-requirement response only carries that requirement node.
  const optionsVersionRef = useRef(versionId)
  useEffect(() => {
    if (optionsVersionRef.current === versionId) return
    optionsVersionRef.current = versionId
    setRequirementOptions([])
  }, [versionId])
  useEffect(() => {
    if (chain.data === null || chain.data.versionId !== versionId) return
    const incoming = chain.data.nodes
      .filter((node) => node.kind === "requirement")
      .map((node) => ({ id: domainIdOf(node.id), title: node.title }))
    if (incoming.length === 0) return
    setRequirementOptions((previous) => mergeRequirementOptions(previous, incoming))
  }, [chain.data, versionId])

  useEffect(() => {
    setSelectedNodeLocal(selectedNodeId ?? "")
  }, [selectedNodeId])

  const autoCollapsed = useMemo(
    () => (chain.data === null ? [] : autoCollapsedChainRequirements(chain.data.nodes)),
    [chain.data],
  )
  const collapsedSet = useMemo(
    () => collapsed ?? new Set(autoCollapsed),
    [collapsed, autoCollapsed],
  )
  const layout = useMemo(
    () =>
      chain.data === null
        ? null
        : layoutChain(chain.data.nodes, { edges: chain.data.edges, collapsed: collapsedSet }),
    [chain.data, collapsedSet],
  )

  // Batch of every case run, so the Sheet can load evidence of an issue whose
  // caseRunId points at a result of another batch.
  const resultBatchByRun = useMemo(() => {
    const map = new Map<string, string>()
    for (const node of chain.data?.nodes ?? []) {
      if (node.kind !== "result") continue
      const runId = node.caseRunId ?? domainIdOf(node.id)
      if (node.batchId !== undefined && node.batchId !== "") map.set(runId, node.batchId)
    }
    return map
  }, [chain.data])

  const selectedNodeParam = selectedNodeId !== undefined ? selectedNodeId : selectedNodeLocal
  const parsedSelection = useMemo(() => parseChainNodeId(selectedNodeParam), [selectedNodeParam])
  const selectedSnapshotNode = useMemo(() => {
    if (parsedSelection === null || chain.data === null) return undefined
    return chain.data.nodes.find(
      (node) => node.id === `${parsedSelection.kind}:${parsedSelection.id}`,
    )
  }, [parsedSelection, chain.data])
  const selectedLayoutNode = useMemo(() => {
    if (selectedSnapshotNode === undefined || layout === null) return undefined
    return layout.nodes.find((node) => node.id === selectedSnapshotNode.id)
  }, [selectedSnapshotNode, layout])
  const nodeMissing =
    selectedNodeParam !== "" &&
    chain.data !== null &&
    !chain.loading &&
    chain.error === "" &&
    (parsedSelection === null || selectedSnapshotNode === undefined)
  const selectedCanvasId =
    parsedSelection === null ? null : `${parsedSelection.kind}:${parsedSelection.id}`

  const handleSelectNode = useCallback(
    (nextNodeId: string): void => {
      setSelectedNodeLocal(nextNodeId)
      onSelectedNodeChange?.(nextNodeId)
    },
    [onSelectedNodeChange],
  )
  const handleCanvasSelect = useCallback(
    (nodeId: string): void => {
      handleSelectNode(nodeId)
    },
    [handleSelectNode],
  )
  const closeSheet = useCallback((): void => {
    handleSelectNode("")
  }, [handleSelectNode])

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

  const requirementSelectOptions = useMemo(() => {
    if (
      requirementFilter === "" ||
      requirementOptions.some((option) => option.id === requirementFilter)
    ) {
      return requirementOptions
    }
    return [...requirementOptions, { id: requirementFilter, title: `需求 ${requirementFilter}` }]
  }, [requirementOptions, requirementFilter])

  const toolbarValue = useMemo<ChainToolbarValue>(
    () => ({
      requirementId: requirementFilter,
      resultStatus: statusFilter,
      abnormalOnly,
      issueSeverity: issueSeverityFilter,
      issueStatus: issueStatusFilter,
      includeSupplement,
      revalidationOnly,
      q: searchInput,
      follow,
      listView,
    }),
    [
      requirementFilter,
      statusFilter,
      abnormalOnly,
      issueSeverityFilter,
      issueStatusFilter,
      includeSupplement,
      revalidationOnly,
      searchInput,
      follow,
      listView,
    ],
  )

  const handleToolbarChange = useCallback((next: ChainToolbarValue): void => {
    setRequirementFilter(next.requirementId)
    setStatusFilter(normalizeStatusFilter(next.resultStatus))
    setAbnormalOnly(next.abnormalOnly)
    setIssueSeverityFilter(next.issueSeverity)
    setIssueStatusFilter(next.issueStatus)
    setIncludeSupplement(next.includeSupplement)
    setRevalidationOnly(next.revalidationOnly)
    setSearchInput(next.q)
    // Clearing the search applies immediately instead of waiting for the
    // debounce window; typing still debounces the server read.
    if (next.q.trim() === "") setSearchQuery("")
    setFollow(next.follow)
    setListView(next.listView)
  }, [])

  const counts = chain.data?.counts
  const totalAvailable = counts === undefined ? 0 : chainSnapshotTotal(counts)
  const truncatedFlags = chain.data?.truncated
  const truncatedLayers =
    truncatedFlags === undefined
      ? []
      : (
          [
            ["requirements", "需求"],
            ["cases", "用例"],
            ["results", "结果"],
            ["issues", "问题"],
          ] as const
        )
          .filter(([key]) => truncatedFlags[key])
          .map(([, label]) => label)
  const isTruncated = truncatedLayers.length > 0

  const filtersActive =
    requirementFilter !== "" ||
    statusFilter !== "" ||
    abnormalOnly ||
    searchQuery !== "" ||
    issueStatusFilter !== "" ||
    issueSeverityFilter !== "" ||
    !includeSupplement ||
    revalidationOnly

  const hasSnapshot = chain.data !== null
  const hasData = chain.data !== null && chain.data.nodes.length > 0
  const issuesUnavailable = chain.error !== ""
  const issueNodeCount = chain.data?.nodes.filter((node) => node.kind === "issue").length ?? 0
  const issueCountLabel = issuesUnavailable
    ? "问题数据不可用"
    : counts === undefined
      ? "问题加载中…"
      : chain.data?.truncated.issues === true
        ? `${issueNodeCount} 个问题（共 ${counts.issues} 个）`
        : `${counts.issues} 个问题`
  const unlinkedIssues = layout?.unlinkedIssues ?? []
  const visibleCount = layout?.visibleIds.length ?? 0
  const hiddenCount = layout?.hiddenIds.length ?? 0

  function retry(): void {
    void chain.refresh()
  }

  const toolbar = (
    <ChainToolbar
      value={toolbarValue}
      onChange={handleToolbarChange}
      requirementOptions={requirementSelectOptions}
      batchScope={batchScope}
      batchId={batchId ?? ""}
      onBatchScopeChange={setBatchScope}
    />
  )

  return (
    <section data-testid="exploration-tab" className="space-y-3">
      <header className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <h2 className="text-base font-semibold tracking-tight">探索链路</h2>
          <p className="mt-0.5 text-xs text-muted-foreground">
            需求 → 用例 → 结果 →
            问题的血缘图；图例与筛选浮在画布上，可切换列表视图。筛选与搜索由服务端读模型完成，拖拽平移、滚轮缩放、双击需求或按
            ←/→ 折叠。
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <ChainStreamStatus status={chain.streamStatus} />
          <p data-testid="exploration-visible-count" className="text-xs text-muted-foreground">
            {`可见 ${visibleCount} 个节点${hiddenCount > 0 ? `，折叠 ${hiddenCount} 个` : ""}`}
          </p>
          <button
            type="button"
            data-testid="exploration-refresh"
            onClick={retry}
            disabled={chain.loading}
            className={BUTTON_CLASS}
          >
            <RefreshCw className="h-3.5 w-3.5" aria-hidden="true" />
            刷新
          </button>
        </div>
      </header>

      {isTruncated && chain.data !== null && counts !== undefined ? (
        <ChainTruncated
          shown={chain.data.nodes.length}
          total={totalAvailable}
          layers={truncatedLayers}
        />
      ) : null}

      {chain.error !== "" && hasSnapshot ? (
        <ChainDegraded message={chain.error} onRetry={retry} />
      ) : null}

      {nodeMissing ? (
        <p
          data-testid="chain-node-missing"
          role="alert"
          className="flex flex-wrap items-center justify-between gap-2 rounded-xl border border-dashed border-border px-4 py-3 text-sm text-muted-foreground"
        >
          <span>节点不存在或已被刷新，请重新选择链路节点。</span>
          <button type="button" onClick={closeSheet} className={BUTTON_CLASS}>
            关闭
          </button>
        </p>
      ) : null}

      {hasData && listView ? (
        <div data-testid="chain-list-workspace" className="space-y-2">
          {toolbar}
          <div className="h-[60vh] min-h-[420px] overflow-auto lg:h-[72vh]">
            {chain.data !== null ? (
              <ChainListView
                snapshot={chain.data}
                collapsed={collapsedSet}
                selectedId={selectedCanvasId}
                onSelect={handleCanvasSelect}
              />
            ) : null}
          </div>
        </div>
      ) : (
        <div
          data-testid="chain-workspace"
          className="relative h-[60vh] min-h-[420px] w-full lg:h-[72vh]"
        >
          {hasData && chain.data !== null ? (
            <div className="absolute inset-0">
              <ChainCanvas
                snapshot={chain.data}
                selectedId={selectedCanvasId}
                onSelect={handleCanvasSelect}
                collapsed={collapsedSet}
                onToggleCollapse={toggleRequirement}
                follow={follow}
                onEscape={closeSheet}
              />
            </div>
          ) : null}

          <ChainLegend empty={!hasData || (layout?.nodes.length ?? 0) === 0} />

          <div className="pointer-events-none absolute inset-x-2 top-2 z-30 flex justify-end">
            <div className="pointer-events-auto max-w-full">{toolbar}</div>
          </div>

          {!hasData ? (
            <div className="absolute inset-0 flex flex-col items-center justify-center gap-2 overflow-auto p-4">
              {chain.loading && !hasSnapshot ? (
                <ChainSkeleton className="w-full" />
              ) : chain.error !== "" && !hasSnapshot ? (
                <div className="w-full">
                  <ChainError message={chain.error} onRetry={retry} />
                </div>
              ) : hasSnapshot ? (
                <ChainEmpty filtered={filtersActive} />
              ) : null}
            </div>
          ) : null}
        </div>
      )}

      {selectedSnapshotNode !== undefined ? (
        <ChainSheet
          key={selectedSnapshotNode.id}
          node={selectedSnapshotNode}
          versionId={versionId}
          batchId={batchId ?? ""}
          stream={stream}
          resultBatchId={
            (selectedSnapshotNode.caseRunId === undefined
              ? undefined
              : resultBatchByRun.get(selectedSnapshotNode.caseRunId)) ??
            selectedSnapshotNode.batchId
          }
          collapsed={selectedLayoutNode?.collapsed ?? false}
          onToggleCollapse={
            selectedLayoutNode?.kind === "requirement" && selectedLayoutNode.hasChildren
              ? () => toggleRequirement(selectedSnapshotNode.id)
              : undefined
          }
          onClose={closeSheet}
          onOpenEvidence={onOpenEvidence}
          onNavigate={onNavigate}
        />
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
            {issueCountLabel}
          </p>
        </div>

        {issuesUnavailable ? (
          <ChainIssuesDegraded message={chain.error} />
        ) : chain.loading && !hasSnapshot ? (
          <p
            data-testid="chain-issues-loading"
            role="status"
            className="mt-2 flex items-center gap-2 text-xs text-muted-foreground"
          >
            <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
            问题加载中…
          </p>
        ) : counts !== undefined && counts.issues === 0 && chain.data?.truncated.issues !== true ? (
          <p data-testid="chain-issues-empty" className="mt-2 text-xs text-muted-foreground">
            本批次未发现问题。
          </p>
        ) : (
          <p className="mt-2 text-xs text-muted-foreground">
            {`红色问题卡为问题节点（${batchScope === "all" ? "全部批次" : "当前批次"}）；点击节点查看摘要、证据与深链。`}
          </p>
        )}

        {unlinkedIssues.length === 0 ? null : (
          <div data-testid="chain-unlinked-issues" className="mt-3">
            <h4 className="text-xs font-semibold">未关联用例的问题</h4>
            <ul className="mt-1.5 flex flex-wrap gap-1.5">
              {unlinkedIssues.map((issue) => (
                <li key={issue.id}>
                  <button
                    type="button"
                    data-testid={`chain-unlinked-issue-${domainIdOf(issue.id)}`}
                    onClick={() => onNavigate?.({ kind: "issue", id: domainIdOf(issue.id) })}
                    className={BUTTON_CLASS}
                  >
                    {`${getIssueSeverityToken(issue.severity).label} · ${issue.title}`}
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
