"use client"

import { History, LayoutList, Network, RefreshCw } from "lucide-react"
import { useRouter, useSearchParams } from "next/navigation"
import { useCallback, useEffect, useMemo, useRef, useState } from "react"

import { Alert } from "@/components/ui/Alert"
import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState } from "@/components/ui/ErrorState"
import { Field } from "@/components/ui/Field"
import { Select } from "@/components/ui/Select"
import { LoadingCard } from "@/components/ui/Spinner"
import { SSE_STATUS_LABEL } from "@/features/clarification/labels"
import type { VersionEvent } from "@/features/clarification/types"
import { useVersionEvents } from "@/features/clarification/useVersionEvents"
import type { RunWave } from "@/features/execution/types"
import { getVersion } from "@/features/versions/api"
import type { Version } from "@/features/versions/types"
import { formatDateTime } from "@/lib/format"

import { getChain, listChainEvents, listChainWaves } from "./api"
import { ChainDetailSheet } from "./ChainDetailSheet"
import { type ChainFilterKey, ChainFilters, type ChainFilterValues } from "./ChainFilters"
import { ChainGraph } from "./ChainGraph"
import { ChainListView } from "./ChainListView"
import {
  CHAIN_DEGRADED_LAYER_LABELS,
  CHAIN_ISSUE_SEVERITY_LABELS,
  CHAIN_ISSUE_STATUS_LABELS,
  CHAIN_NODE_DOT_CLASSES,
  CHAIN_NODE_KIND_LABELS,
  CHAIN_TRUNCATED_LAYER_LABELS,
  CHAIN_TRUNCATION_REASON_LABELS,
  type ChainIssueSeverity,
  type ChainIssueStatus,
  type ChainNodeView,
  type ChainTruncatedLayers,
  type ChainTruncationReason,
  type ChainView,
} from "./types"

export type ChainTabProps = {
  versionId: string
  /**
   * Called when a `version.*` event arrives, in addition to the tab's own
   * debounced chain refresh. The version page uses it to refresh the header and
   * tab counts; the chain never rebuilds business state from events.
   */
  onVersionChanged?: () => void
}

const REFRESH_DEBOUNCE_MS = 350
const Q_DEBOUNCE_MS = 300
const DEFAULT_EVENT_LIMIT = 20
const MAX_EVENT_LIMIT = 200

type ChainViewMode = "graph" | "list"

const FILTER_KEYS: (keyof ChainFilterValues)[] = [
  "sutId",
  "pageId",
  "waveId",
  "status",
  "result",
  "q",
  "abnormalOnly",
  "issueStatus",
  "issueSeverity",
  "requirementLimit",
]

function deriveSelectedNode(params: URLSearchParams): string | null {
  const node = params.get("node")
  if (node !== null && node.length > 0) return node
  const issue = params.get("issue")
  if (issue !== null && issue.length > 0) return `issue:${issue}`
  const report = params.get("report")
  if (report !== null && report.length > 0) return `report:${report}`
  const wave = params.get("wave")
  if (wave !== null && wave.length > 0) return `wave:${wave}`
  return null
}

export function filtersFromParams(params: URLSearchParams): ChainFilterValues {
  return {
    sutId: params.get("sut_id") ?? "all",
    pageId: params.get("page_id") ?? "all",
    waveId: params.get("wave_id") ?? params.get("wave") ?? "all",
    status: params.get("status") ?? "all",
    result: params.get("result") ?? "all",
    q: params.get("q") ?? "",
    abnormalOnly: params.get("abnormal_only") === "true" ? "true" : "all",
    issueStatus: params.get("issue_status") ?? "all",
    issueSeverity: params.get("issue_severity") ?? "all",
    requirementLimit: params.get("requirement_limit") ?? "all",
  }
}

function sameFilters(left: ChainFilterValues, right: ChainFilterValues): boolean {
  return FILTER_KEYS.every((key) => left[key] === right[key])
}

export function chainFiltersToParams(
  filters: ChainFilterValues,
  selectedNodeId: string | null,
): URLSearchParams {
  const params = new URLSearchParams({ tab: "chain" })
  const set = (key: string, value: string) => {
    if (value.length > 0 && value !== "all") params.set(key, value)
  }
  set("sut_id", filters.sutId)
  set("page_id", filters.pageId)
  set("wave_id", filters.waveId)
  set("status", filters.status)
  set("result", filters.result)
  set("q", filters.q.trim())
  set("abnormal_only", filters.abnormalOnly === "true" ? "true" : "all")
  set("issue_status", filters.issueStatus)
  set("issue_severity", filters.issueSeverity)
  set("requirement_limit", filters.requirementLimit)
  if (selectedNodeId !== null) params.set("node", selectedNodeId)
  return params
}

function truncationReasonLabel(reason: ChainTruncationReason): string {
  return reason === "" ? "预算截断" : CHAIN_TRUNCATION_REASON_LABELS[reason]
}

function truncatedLayerKeys(
  layers: ChainTruncatedLayers | undefined,
): (keyof ChainTruncatedLayers)[] {
  if (layers === undefined) return []
  return (Object.keys(CHAIN_TRUNCATED_LAYER_LABELS) as (keyof ChainTruncatedLayers)[]).filter(
    (key) => layers[key],
  )
}

/**
 * P4/P6 exploration-chain tab (spec §8, §9.4, §10): React Flow graph or
 * accessible list over the server read model, server-side filters (P6 q /
 * abnormal / issue status+severity / requirement limit), typed counts+coverage,
 * follow auto-center, layered degradation/reconnect with the last snapshot
 * retained, per-layer truncation banners and URL deep links. No business state
 * is rebuilt from events; an event only schedules a debounced snapshot refetch.
 */
export function ChainTab({ versionId, onVersionChanged: notifyVersionChanged }: ChainTabProps) {
  const router = useRouter()
  const searchParams = useSearchParams()

  const [filters, setFilters] = useState<ChainFilterValues>(() => filtersFromParams(searchParams))
  const [qInput, setQInput] = useState<string>(() => filtersFromParams(searchParams).q)
  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(() =>
    deriveSelectedNode(searchParams),
  )
  const [chain, setChain] = useState<ChainView | null>(null)
  const [version, setVersion] = useState<Version | null>(null)
  const [waves, setWaves] = useState<RunWave[]>([])
  const [events, setEvents] = useState<VersionEvent[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<unknown>(null)
  const [reloadToken, setReloadToken] = useState(0)
  const [follow, setFollow] = useState(true)
  const [viewMode, setViewMode] = useState<ChainViewMode>("graph")

  const followRef = useRef(follow)
  followRef.current = follow
  const filtersRef = useRef(filters)
  filtersRef.current = filters
  const selectedRef = useRef(selectedNodeId)
  selectedRef.current = selectedNodeId
  const refreshTimer = useRef<ReturnType<typeof setTimeout> | null>(null)

  const syncUrl = useCallback(
    (nextFilters: ChainFilterValues, nodeId: string | null) => {
      const params = chainFiltersToParams(nextFilters, nodeId)
      router.replace(`/versions/${encodeURIComponent(versionId)}?${params.toString()}`)
    },
    [router, versionId],
  )

  // Follow back/forward navigation and deep links (?node / ?issue / ?report /
  // ?wave plus every P6 filter). The parsed values are compared first so the
  // tab never rewrites the URL just because it rendered.
  useEffect(() => {
    const parsed = filtersFromParams(searchParams)
    setFilters((current) => (sameFilters(current, parsed) ? current : parsed))
    setQInput(parsed.q)
    setSelectedNodeId(deriveSelectedNode(searchParams))
  }, [searchParams])

  // P6 q input is debounced 300ms; only the committed value reaches the API and
  // the URL, so typing never floods the chain endpoint.
  useEffect(() => {
    if (qInput === filters.q) return
    const timer = setTimeout(() => {
      const next = { ...filtersRef.current, q: qInput }
      setFilters(next)
      syncUrl(next, selectedRef.current)
    }, Q_DEBOUNCE_MS)
    return () => clearTimeout(timer)
  }, [filters.q, qInput, syncUrl])

  useEffect(() => {
    let cancelled = false
    void getVersion(versionId)
      .then((next) => {
        if (!cancelled) setVersion(next)
      })
      .catch(() => {
        // Options are optional; the graph still works without labels.
      })
    void listChainWaves(versionId)
      .then((next) => {
        if (!cancelled) setWaves(next)
      })
      .catch(() => {
        // Replay options degrade gracefully.
      })
    return () => {
      cancelled = true
    }
  }, [versionId])

  const { sutId, pageId, waveId, status, result, q, abnormalOnly, issueStatus, issueSeverity } =
    filters
  const requirementLimit = filters.requirementLimit

  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadToken forces a snapshot refetch
  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)
    void getChain(versionId, {
      sutId: sutId === "all" ? undefined : sutId,
      pageId: pageId === "all" ? undefined : pageId,
      waveId: waveId === "all" ? undefined : waveId,
      status: status === "all" ? undefined : status,
      result: result === "all" ? undefined : result,
      focusNode: selectedNodeId ?? undefined,
      limit: 300,
      q: q.trim().length > 0 ? q.trim() : undefined,
      abnormalOnly: abnormalOnly === "true" ? true : undefined,
      issueStatus: issueStatus === "all" ? undefined : (issueStatus as ChainIssueStatus),
      issueSeverity: issueSeverity === "all" ? undefined : (issueSeverity as ChainIssueSeverity),
      requirementLimit:
        requirementLimit === "all" ? undefined : Number.parseInt(requirementLimit, 10),
    })
      .then((next) => {
        if (!cancelled) {
          setChain(next)
          setError(null)
        }
      })
      .catch((cause: unknown) => {
        // P6 keeps the last successful graph; the host renders an inline
        // reconnect banner instead of replacing the canvas with an error state.
        if (!cancelled) setError(cause)
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [
    versionId,
    sutId,
    pageId,
    waveId,
    status,
    result,
    q,
    abnormalOnly,
    issueStatus,
    issueSeverity,
    requirementLimit,
    selectedNodeId,
    reloadToken,
  ])

  // P6 consumes replay.event_limit for the /api/events history read; the P0
  // endpoint caps a page at 200.
  const eventLimit = Math.min(
    Math.max(chain?.replay.event_limit ?? DEFAULT_EVENT_LIMIT, 1),
    MAX_EVENT_LIMIT,
  )
  useEffect(() => {
    let cancelled = false
    void listChainEvents(versionId, { limit: eventLimit })
      .then((next) => {
        if (!cancelled) setEvents(next)
      })
      .catch(() => {
        // The timeline is informative only.
      })
    return () => {
      cancelled = true
    }
  }, [eventLimit, versionId])

  // One debounced snapshot refetch shared by every P1-P4 chain-relevant event.
  const scheduleRefresh = useCallback(() => {
    if (!followRef.current) return
    if (refreshTimer.current !== null) clearTimeout(refreshTimer.current)
    refreshTimer.current = setTimeout(() => {
      refreshTimer.current = null
      setReloadToken((token) => token + 1)
    }, REFRESH_DEBOUNCE_MS)
  }, [])

  useEffect(
    () => () => {
      if (refreshTimer.current !== null) clearTimeout(refreshTimer.current)
    },
    [],
  )

  const sseStatus = useVersionEvents(versionId, {
    onWaveChanged: scheduleRefresh,
    onCaseRunChanged: scheduleRefresh,
    onEvidenceCreated: scheduleRefresh,
    onHumanRequestChanged: scheduleRefresh,
    onIssueChanged: scheduleRefresh,
    onReportChanged: scheduleRefresh,
    onRequirementChanged: scheduleRefresh,
    onVersionChanged: () => {
      notifyVersionChanged?.()
      scheduleRefresh()
    },
    onStreamReset: scheduleRefresh,
  })

  const updateFilter = useCallback(
    (key: ChainFilterKey, value: string) => {
      const next = { ...filtersRef.current, [key]: value }
      setFilters(next)
      syncUrl(next, selectedRef.current)
    },
    [syncUrl],
  )

  const selectNode = useCallback(
    (nodeId: string) => {
      setSelectedNodeId(nodeId)
      syncUrl(filtersRef.current, nodeId)
    },
    [syncUrl],
  )

  const closeDetail = useCallback(() => {
    setSelectedNodeId(null)
    syncUrl(filtersRef.current, null)
  }, [syncUrl])

  function selectHistoryWave(value: string): void {
    const next = { ...filtersRef.current, waveId: value }
    setFilters(next)
    const nodeId = value === "all" ? null : `wave:${value}`
    setSelectedNodeId(nodeId)
    syncUrl(next, nodeId)
  }

  function resumeFollow(): void {
    setFollow(true)
  }

  const sutOptions = useMemo(
    () =>
      version === null
        ? []
        : version.bindings.map((binding) => ({
            value: binding.id,
            label: `${binding.sut.name}（SUT ${binding.id}）`,
          })),
    [version],
  )

  const pageOptions = useMemo(
    () =>
      version === null
        ? []
        : version.bindings.flatMap((binding) =>
            binding.pages.map((page) => ({ value: page.id, label: page.name })),
          ),
    [version],
  )

  const selectedNode = useMemo<ChainNodeView | null>(
    () => chain?.nodes.find((node) => node.id === selectedNodeId) ?? null,
    [chain, selectedNodeId],
  )

  const replayApproximate = chain !== null && (chain.replay.approximate || filters.waveId !== "all")
  const degradedLayers = chain?.degraded_layers ?? []
  const truncatedLayers = truncatedLayerKeys(chain?.truncated_layers)

  const stats = useMemo(() => {
    const counts = chain?.counts ?? {
      requirements: 0,
      scenarios: 0,
      cases: 0,
      waves: 0,
      case_runs: 0,
      issues: 0,
      reports: 0,
      evidence: 0,
    }
    const coverage = counts.coverage
    const passedRuns = counts.by_case_run_status?.passed ?? 0
    const coverageValue =
      coverage !== undefined
        ? `${coverage.covered}/${coverage.requirements}`
        : counts.cases > 0
          ? `${passedRuns}/${counts.cases}`
          : "0/0"
    return [
      { key: "requirements", label: "需求", value: counts.requirements },
      { key: "scenarios", label: "场景", value: counts.scenarios },
      { key: "cases", label: "用例", value: counts.cases },
      { key: "waves", label: "波次", value: counts.waves },
      { key: "case_runs", label: "运行", value: counts.case_runs },
      { key: "issues", label: "问题", value: counts.issues },
      { key: "reports", label: "报告", value: counts.reports },
      { key: "evidence", label: "证据", value: counts.evidence },
      { key: "coverage", label: "覆盖", value: coverageValue },
    ]
  }, [chain])

  const runStatus = chain?.counts.by_case_run_status
  const issueStatusCounts = chain?.counts.by_issue_status
  const issueSeverityCounts = chain?.counts.by_issue_severity
  const coverageCounts = chain?.counts.coverage

  const initialLoading = loading && chain === null
  const empty = !loading && error === null && chain !== null && chain.nodes.length === 0
  const followNodeId = selectedNodeId ?? chain?.nodes[0]?.id ?? null

  return (
    <div data-testid="chain-tab" className="w-full min-w-0 space-y-3 px-1 pb-3 lg:px-0">
      <section
        aria-label="链路工具栏"
        data-testid="chain-toolbar"
        className="surface-card space-y-2 px-3 py-2.5"
      >
        <div className="flex flex-wrap items-center gap-2">
          <h2 className="text-sm font-semibold text-foreground">探索链路</h2>
          <span data-testid="chain-sse-status" className="inline-flex">
            <Badge
              tone={
                sseStatus === "live" ? "success" : sseStatus === "closed" ? "neutral" : "warning"
              }
            >
              {SSE_STATUS_LABEL[sseStatus]}
            </Badge>
          </span>
          {replayApproximate ? (
            <span data-testid="chain-replay-badge" className="inline-flex">
              <Badge tone="warning">历史回看 · 近似</Badge>
            </span>
          ) : null}
          {refreshingLabel(loading, chain) ? (
            <span data-testid="chain-refreshing" className="text-[11px] text-muted-foreground">
              刷新中…
            </span>
          ) : null}

          <span className="ml-auto flex flex-wrap items-center gap-1.5">
            <fieldset aria-label="链路视图切换" className="m-0 inline-flex min-w-0 border-0 p-0">
              <Button
                size="sm"
                variant={viewMode === "graph" ? "primary" : "secondary"}
                aria-pressed={viewMode === "graph"}
                aria-label="画布视图"
                onClick={() => setViewMode("graph")}
              >
                <Network className="size-3.5" aria-hidden="true" />
                画布
              </Button>
              <Button
                size="sm"
                variant={viewMode === "list" ? "primary" : "secondary"}
                aria-pressed={viewMode === "list"}
                aria-label="列表视图"
                onClick={() => setViewMode("list")}
              >
                <LayoutList className="size-3.5" aria-hidden="true" />
                列表
              </Button>
            </fieldset>
            {follow ? null : (
              <Button size="sm" variant="secondary" aria-label="恢复跟随" onClick={resumeFollow}>
                恢复跟随
              </Button>
            )}
            <Button
              size="sm"
              variant={follow ? "primary" : "secondary"}
              aria-pressed={follow}
              aria-label="实时跟随"
              data-testid="chain-follow"
              onClick={() => setFollow((current) => !current)}
            >
              {follow ? "实时跟随中" : "已暂停跟随"}
            </Button>
            <Button
              size="sm"
              variant="secondary"
              aria-label="刷新链路"
              onClick={() => setReloadToken((token) => token + 1)}
            >
              <RefreshCw className="size-3.5" aria-hidden="true" />
              刷新
            </Button>
          </span>
        </div>

        <div className="flex flex-wrap items-end gap-2">
          <ChainFilters
            values={filters}
            qInput={qInput}
            onQInputChange={setQInput}
            sutOptions={sutOptions}
            pageOptions={pageOptions}
            waves={waves}
            onChange={updateFilter}
          />
          <Field id="chain-history-wave" label="历史波次" className="w-40">
            <Select
              id="chain-history-wave"
              value={filters.waveId}
              onChange={(event) => selectHistoryWave(event.target.value)}
            >
              <option value="all">实时（最新）</option>
              {waves.map((wave) => (
                <option key={wave.id} value={wave.id}>
                  #{wave.id}
                </option>
              ))}
            </Select>
          </Field>
        </div>
        <p aria-live="polite" data-testid="chain-loading-status" className="sr-only">
          {loading ? "正在刷新链路" : ""}
        </p>
      </section>

      {replayApproximate ? (
        <p className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
          <History className="size-3.5" aria-hidden="true" />
          历史回看按实体 created_at 边界近似还原，不承诺任意时点快照。
        </p>
      ) : null}

      {degradedLayers.length > 0 ? (
        <div data-testid="chain-degraded-banner">
          <Alert tone="warning" title="部分链路图层不可用">
            <p>
              {degradedLayers.map((layer) => CHAIN_DEGRADED_LAYER_LABELS[layer]).join("、")}
              {" 图层读取失败；已保留健康图层与上一次快照，不会显示空态。"}
            </p>
            <Button
              size="sm"
              variant="secondary"
              className="mt-2"
              data-testid="chain-reconnect"
              onClick={() => setReloadToken((token) => token + 1)}
            >
              重新连接
            </Button>
          </Alert>
        </div>
      ) : null}

      {truncatedLayers.length > 0 ? (
        <div data-testid="chain-layer-truncation-banner">
          <Alert tone="warning" title="部分图层已按预算截断">
            <p>
              {truncatedLayers.map((layer) => CHAIN_TRUNCATED_LAYER_LABELS[layer]).join("、")}
              {" 图层已截断；请收窄筛选或调整链路预算。"}
            </p>
          </Alert>
        </div>
      ) : null}

      {chain?.truncated === true ? (
        <Alert tone="warning" title="链路已按预算截断">
          <p>
            <span role="status">{truncationReasonLabel(chain.truncation.reason)}</span>
            ：节点上限 {chain.truncation.max_nodes} · 边上限 {chain.truncation.max_edges} · 需求上限{" "}
            {chain.truncation.max_requirements}。节点按低优先级顺序丢弃，需求与深链目标优先保留。
          </p>
        </Alert>
      ) : null}

      {error !== null && chain !== null ? (
        <div data-testid="chain-refresh-error">
          <Alert tone="danger" title="链路刷新失败，已保留上一次快照">
            <p>网络或服务暂不可用；当前画布/列表仍是上一份成功数据。</p>
            <Button
              size="sm"
              variant="secondary"
              className="mt-2"
              data-testid="chain-refresh-reconnect"
              onClick={() => setReloadToken((token) => token + 1)}
            >
              重新连接
            </Button>
          </Alert>
        </div>
      ) : null}

      <div className="grid min-w-0 gap-3 lg:grid-cols-[minmax(0,1fr)_320px]">
        <div className="min-w-0 space-y-2">
          {initialLoading ? (
            <div className="surface-card">
              <LoadingCard label="正在加载探索链路…" />
            </div>
          ) : error !== null && chain === null ? (
            <ErrorState
              error={error}
              title="探索链路加载失败"
              onRetry={() => setReloadToken((token) => token + 1)}
            />
          ) : empty ? (
            <EmptyState
              title="暂无探索链路"
              description="需求确认并执行后，链路会展示需求、用例、波次、运行、问题与报告之间的关系。"
            />
          ) : chain !== null ? (
            viewMode === "graph" ? (
              <ChainGraph
                nodes={chain.nodes}
                edges={chain.edges}
                selectedNodeId={selectedNodeId}
                onSelectNode={selectNode}
                follow={follow}
                followNodeId={followNodeId}
                onUserMove={() => setFollow(false)}
              />
            ) : (
              <ChainListView
                nodes={chain.nodes}
                selectedNodeId={selectedNodeId}
                onSelectNode={selectNode}
              />
            )
          ) : null}

          <section
            data-testid="chain-legend"
            aria-label="链路图例"
            className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-lg border border-border bg-card px-3 py-2 text-[11px] text-muted-foreground"
          >
            {(Object.keys(CHAIN_NODE_KIND_LABELS) as (keyof typeof CHAIN_NODE_KIND_LABELS)[]).map(
              (kind) => (
                <span key={kind} className="inline-flex items-center gap-1">
                  <span
                    className={`size-1.5 rounded-full ${CHAIN_NODE_DOT_CLASSES[kind]}`}
                    aria-hidden="true"
                  />
                  {CHAIN_NODE_KIND_LABELS[kind]}
                </span>
              ),
            )}
          </section>
        </div>

        <aside className="min-w-0 space-y-3">
          <section
            data-testid="chain-stats"
            aria-label="链路统计"
            className="surface-card space-y-1.5 px-3 py-2.5"
          >
            <div className="flex flex-wrap items-center gap-1.5">
              {stats.map((item) => (
                <Badge key={item.key} tone={item.key === "coverage" ? "info" : "neutral"}>
                  {item.label} {item.value}
                </Badge>
              ))}
            </div>
            {runStatus !== undefined ? (
              <p
                data-testid="chain-run-status-counts"
                className="text-[10px] text-muted-foreground"
              >
                运行结果：通过 {runStatus.passed} · 失败 {runStatus.failed} · 阻塞{" "}
                {runStatus.blocked} · 未定 {runStatus.inconclusive} · 跳过 {runStatus.skipped} ·
                中断 {runStatus.interrupted}
              </p>
            ) : null}
            {coverageCounts !== undefined ? (
              <p data-testid="chain-coverage-counts" className="text-[10px] text-muted-foreground">
                覆盖：已覆盖 {coverageCounts.covered} · 未覆盖 {coverageCounts.uncovered} · 需求{" "}
                {coverageCounts.requirements}
              </p>
            ) : null}
            {issueStatusCounts !== undefined ? (
              <p
                data-testid="chain-issue-status-counts"
                className="text-[10px] text-muted-foreground"
              >
                问题状态：
                {(Object.keys(CHAIN_ISSUE_STATUS_LABELS) as ChainIssueStatus[])
                  .map((key) => `${CHAIN_ISSUE_STATUS_LABELS[key]} ${issueStatusCounts[key]}`)
                  .join(" · ")}
              </p>
            ) : null}
            {issueSeverityCounts !== undefined ? (
              <p
                data-testid="chain-issue-severity-counts"
                className="text-[10px] text-muted-foreground"
              >
                问题严重级：
                {(Object.keys(CHAIN_ISSUE_SEVERITY_LABELS) as ChainIssueSeverity[])
                  .map((key) => `${CHAIN_ISSUE_SEVERITY_LABELS[key]} ${issueSeverityCounts[key]}`)
                  .join(" · ")}
              </p>
            ) : null}
          </section>

          <section
            data-testid="chain-event-timeline"
            aria-label="事件时间线"
            className="surface-card overflow-hidden"
          >
            <header className="flex items-center justify-between border-b border-border px-3 py-2">
              <h2 className="text-sm font-semibold text-foreground">事件时间线</h2>
              <span className="text-[10px] text-muted-foreground">最多 {eventLimit} 条 · 近似</span>
            </header>
            {chain?.replay.truncated === true ? (
              <p
                data-testid="chain-replay-truncated"
                className="px-3 py-2 text-[10px] text-warning"
              >
                历史事件超过 replay.event_limit，仅展示最近 {eventLimit} 条。
              </p>
            ) : null}
            {events.length === 0 ? (
              <p className="px-3 py-4 text-[11px] text-muted-foreground">暂无版本事件。</p>
            ) : (
              <ol className="max-h-[520px] space-y-1.5 overflow-y-auto p-2">
                {events.map((event) => (
                  <li
                    key={event.seq}
                    className="rounded-lg border border-border bg-card px-2.5 py-1.5"
                  >
                    <p className="text-[11px] font-medium text-foreground">{event.type}</p>
                    <p className="text-[10px] text-muted-foreground">
                      #{event.seq} · {formatDateTime(event.created_at)}
                    </p>
                  </li>
                ))}
              </ol>
            )}
          </section>

          {chain !== null ? (
            <p className="text-[10px] text-muted-foreground">
              生成于 {formatDateTime(chain.generated_at)} · 快照游标 {chain.snapshot_cursor}
            </p>
          ) : null}
        </aside>
      </div>

      {selectedNode !== null ? (
        <ChainDetailSheet versionId={versionId} node={selectedNode} onClose={closeDetail} />
      ) : null}
    </div>
  )
}

function refreshingLabel(loading: boolean, chain: ChainView | null): boolean {
  return loading && chain !== null
}
