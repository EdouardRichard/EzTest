"use client"

import { History, RefreshCw } from "lucide-react"
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
import {
  CHAIN_NODE_DOT_CLASSES,
  CHAIN_NODE_KIND_LABELS,
  CHAIN_TRUNCATION_REASON_LABELS,
  type ChainNodeView,
  type ChainTruncationReason,
  type ChainView,
} from "./types"

export type ChainTabProps = {
  versionId: string
}

const REFRESH_DEBOUNCE_MS = 350
const EVENT_LIMIT = 20

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

function truncationReasonLabel(reason: ChainTruncationReason): string {
  return reason === "" ? "预算截断" : CHAIN_TRUNCATION_REASON_LABELS[reason]
}

/**
 * P4 exploration-chain tab (spec §8, §9.4): React Flow graph over the server
 * read model, server-side filters, live-follow SSE, approximate wave replay,
 * truncation warning, event timeline and URL deep links. No business state is
 * rebuilt from events; an event only schedules a debounced snapshot refetch.
 */
export function ChainTab({ versionId }: ChainTabProps) {
  const router = useRouter()
  const searchParams = useSearchParams()

  const urlWave = searchParams.get("wave")
  const [filters, setFilters] = useState<ChainFilterValues>(() => ({
    sutId: "all",
    pageId: "all",
    waveId: urlWave !== null && urlWave.length > 0 ? urlWave : "all",
    status: "all",
    result: "all",
  }))
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

  const followRef = useRef(follow)
  followRef.current = follow
  const refreshTimer = useRef<ReturnType<typeof setTimeout> | null>(null)

  // Follow back/forward navigation: ?node / ?issue / ?report / ?wave.
  useEffect(() => {
    const nextNode = deriveSelectedNode(searchParams)
    if (nextNode !== null) setSelectedNodeId(nextNode)
    const wave = searchParams.get("wave")
    if (wave !== null && wave.length > 0) {
      setFilters((current) => (current.waveId === wave ? current : { ...current, waveId: wave }))
    }
  }, [searchParams])

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
    void listChainEvents(versionId, { limit: EVENT_LIMIT })
      .then((next) => {
        if (!cancelled) setEvents(next)
      })
      .catch(() => {
        // The timeline is informative only.
      })
    return () => {
      cancelled = true
    }
  }, [versionId])

  const { sutId, pageId, waveId, status, result } = filters

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
    })
      .then((next) => {
        if (!cancelled) setChain(next)
      })
      .catch((cause: unknown) => {
        if (!cancelled) setError(cause)
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [versionId, sutId, pageId, waveId, status, result, selectedNodeId, reloadToken])

  // One debounced snapshot refetch shared by every P1-P4 chain-relevant event.
  const scheduleRefresh = useCallback(() => {
    if (!followRef.current) return
    if (refreshTimer.current !== null) clearTimeout(refreshTimer.current)
    refreshTimer.current = setTimeout(() => {
      refreshTimer.current = null
      setReloadToken((token) => token + 1)
      void listChainEvents(versionId, { limit: EVENT_LIMIT })
        .then(setEvents)
        .catch(() => {
          // Keep the last timeline snapshot.
        })
    }, REFRESH_DEBOUNCE_MS)
  }, [versionId])

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
    onStreamReset: scheduleRefresh,
  })

  const selectNode = useCallback(
    (nodeId: string) => {
      setSelectedNodeId(nodeId)
      const params = new URLSearchParams({ tab: "chain" })
      if (waveId !== "all") params.set("wave", waveId)
      params.set("node", nodeId)
      router.replace(`/versions/${encodeURIComponent(versionId)}?${params.toString()}`)
    },
    [router, versionId, waveId],
  )

  const closeDetail = useCallback(() => {
    setSelectedNodeId(null)
    const params = new URLSearchParams({ tab: "chain" })
    if (waveId !== "all") params.set("wave", waveId)
    router.replace(`/versions/${encodeURIComponent(versionId)}?${params.toString()}`)
  }, [router, versionId, waveId])

  const updateFilter = useCallback((key: ChainFilterKey, value: string) => {
    setFilters((current) => ({ ...current, [key]: value }))
  }, [])

  function selectHistoryWave(value: string): void {
    setFilters((current) => ({ ...current, waveId: value }))
    const nodeId = value === "all" ? null : `wave:${value}`
    setSelectedNodeId(nodeId)
    const params = new URLSearchParams({ tab: "chain" })
    if (value !== "all") params.set("wave", value)
    if (nodeId !== null) params.set("node", nodeId)
    router.replace(`/versions/${encodeURIComponent(versionId)}?${params.toString()}`)
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
    const passedRuns =
      chain?.nodes.filter(
        (node) => node.kind === "case_run" && String(node.meta?.result ?? "") === "passed",
      ).length ?? 0
    const coverage = counts.cases > 0 ? Math.round((passedRuns / counts.cases) * 100) : 0
    return [
      { key: "requirements", label: "需求", value: counts.requirements },
      { key: "scenarios", label: "场景", value: counts.scenarios },
      { key: "cases", label: "用例", value: counts.cases },
      { key: "waves", label: "波次", value: counts.waves },
      { key: "case_runs", label: "运行", value: counts.case_runs },
      { key: "issues", label: "问题", value: counts.issues },
      { key: "reports", label: "报告", value: counts.reports },
      { key: "evidence", label: "证据", value: counts.evidence },
      { key: "coverage", label: "覆盖", value: `${coverage}%` },
    ]
  }, [chain])

  const empty = !loading && error === null && chain !== null && chain.nodes.length === 0

  return (
    <div
      data-testid="chain-tab"
      className="relative left-1/2 w-screen -translate-x-1/2 space-y-3 px-5 pb-3 md:w-[calc(100vw-15rem)] lg:px-8"
    >
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

          <span className="ml-auto flex flex-wrap items-center gap-1.5">
            <Button
              size="sm"
              variant={follow ? "primary" : "secondary"}
              aria-pressed={follow}
              aria-label="实时跟随"
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
      </section>

      {replayApproximate ? (
        <p className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
          <History className="size-3.5" aria-hidden="true" />
          历史回看按实体 created_at 边界近似还原，不承诺任意时点快照。
        </p>
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

      <div className="grid gap-3 xl:grid-cols-[minmax(0,1fr)_320px]">
        <div className="min-w-0 space-y-2">
          {loading ? (
            <div className="surface-card">
              <LoadingCard label="正在加载探索链路…" />
            </div>
          ) : error !== null ? (
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
            <ChainGraph
              nodes={chain.nodes}
              edges={chain.edges}
              selectedNodeId={selectedNodeId}
              onSelectNode={selectNode}
            />
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
            className="surface-card flex flex-wrap items-center gap-1.5 px-3 py-2.5"
          >
            {stats.map((item) => (
              <Badge key={item.key} tone={item.key === "coverage" ? "info" : "neutral"}>
                {item.label} {item.value}
              </Badge>
            ))}
          </section>

          <section
            data-testid="chain-event-timeline"
            aria-label="事件时间线"
            className="surface-card overflow-hidden"
          >
            <header className="flex items-center justify-between border-b border-border px-3 py-2">
              <h2 className="text-sm font-semibold text-foreground">事件时间线</h2>
              <span className="text-[10px] text-muted-foreground">
                最多 {EVENT_LIMIT} 条 · 近似
              </span>
            </header>
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
