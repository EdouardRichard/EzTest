"use client"

import { Radio } from "lucide-react"
import { useCallback, useEffect, useMemo, useRef, useState } from "react"

import { EmptyState } from "@/components/ui/EmptyState"
import { StatusBadge, type StatusBadgeVariant } from "@/components/ui/StatusBadge"
import { fetchVersionEvents } from "@/features/versions/api"
import type { VersionEvent } from "@/features/versions/types"
import { useVersionStream } from "@/features/versions/useVersionStream"
import type { SSEStatus } from "@/lib/sse-client"

/** Snapshot page size of the history cursor (spec §9.4). */
const PAGE_SIZE = 50

/** Severity is derived from the event payload, not a frozen envelope field. */
type Severity = "error" | "warning" | "info"

const SEVERITY_META: Record<Severity, { label: string; variant: StatusBadgeVariant }> = {
  error: { label: "错误", variant: "danger" },
  warning: { label: "警告", variant: "warning" },
  info: { label: "信息", variant: "neutral" },
}

const STREAM_STATUS_META: Record<SSEStatus, { label: string; variant: StatusBadgeVariant }> = {
  connecting: { label: "连接中", variant: "info" },
  live: { label: "实时", variant: "success" },
  reconnecting: { label: "重连中", variant: "warning" },
  closed: { label: "断开", variant: "danger" },
}

/** Chinese labels of the execution event types published on the version channel. */
const TYPE_LABELS: Record<string, string> = {
  "batch.created": "批次创建",
  "batch.started": "批次开始",
  "batch.paused": "批次暂停",
  "batch.resumed": "批次恢复",
  "batch.finished": "批次结束",
  "run.started": "用例开始",
  "run.paused": "用例暂停",
  "run.resumed": "用例恢复",
  "run.finished": "用例结束",
  "case.status_changed": "用例状态变更",
  "step.finished": "步骤完成",
}

const STATUS_SEVERITY: Record<string, Severity> = {
  failed: "error",
  error: "error",
  interrupted: "error",
  cancelled: "error",
  canceled: "error",
  timeout: "error",
  blocked: "warning",
  inconclusive: "warning",
  paused: "warning",
  skipped: "warning",
  aborted: "warning",
  warning: "warning",
  passed: "info",
  done: "info",
  running: "info",
  pending: "info",
  planned: "info",
  live: "info",
  success: "info",
}

const EXPLICIT_SEVERITY: Record<string, Severity> = {
  error: "error",
  critical: "error",
  fatal: "error",
  danger: "error",
  warning: "warning",
  warn: "warning",
  info: "info",
  notice: "info",
  debug: "info",
}

const FIELD_CLASS =
  "mt-1 w-full rounded-lg border border-border bg-input px-2.5 py-1.5 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
const LINK_CLASS =
  "text-xs font-medium text-sky-700 underline-offset-2 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring dark:text-sky-300"
const PRIMARY_BUTTON_CLASS =
  "inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-1.5 text-sm font-medium transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"

/** Reads a scalar detail value as a string (ids may arrive as JSON numbers). */
function valueString(value: unknown): string | null {
  if (typeof value === "string") return value === "" ? null : value
  if (typeof value === "number" && Number.isFinite(value)) return String(value)
  return null
}

function detailString(event: VersionEvent, key: string): string | null {
  return valueString(event.detail[key])
}

/**
 * Derives the broadcast severity (spec §9.3: 严重级过滤/异常置顶). An explicit
 * `detail.severity` wins; otherwise the status/verdict and the event type decide.
 */
function eventSeverity(event: VersionEvent): Severity {
  const explicit = detailString(event, "severity")
  if (explicit !== null) {
    const mapped = EXPLICIT_SEVERITY[explicit.toLowerCase()]
    if (mapped !== undefined) return mapped
  }

  for (const key of ["status", "verdict", "outcome"]) {
    const value = detailString(event, key)
    if (value === null) continue
    const mapped = STATUS_SEVERITY[value.toLowerCase()]
    if (mapped !== undefined) return mapped
  }

  const failureClass = detailString(event, "failure_class")
  if (failureClass !== null && failureClass !== "none") return "error"

  const type = event.type.toLowerCase()
  if (type.includes("failed") || type.includes("error") || type.includes("interrupted")) {
    return "error"
  }
  if (type.includes("paused") || type.includes("blocked") || type.includes("inconclusive")) {
    return "warning"
  }
  return "info"
}

/** Dedupes by seq (latest wins) and returns a seq-ascending list. */
function mergeEvents(previous: VersionEvent[], incoming: VersionEvent[]): VersionEvent[] {
  const bySeq = new Map<number, VersionEvent>()
  for (const event of previous) bySeq.set(event.seq, event)
  for (const event of incoming) bySeq.set(event.seq, event)
  return [...bySeq.values()].sort((left, right) => left.seq - right.seq)
}

/**
 * Sorts descending by seq, with anomalies pinned above everything else (spec
 * §9.3: 全量事件倒序、异常置顶).
 */
function compareEvents(left: VersionEvent, right: VersionEvent): number {
  const leftAnomaly = eventSeverity(left) === "info" ? 0 : 1
  const rightAnomaly = eventSeverity(right) === "info" ? 0 : 1
  if (leftAnomaly !== rightAnomaly) return rightAnomaly - leftAnomaly
  return right.seq - left.seq
}

function matchesQuery(event: VersionEvent, query: string): boolean {
  const needle = query.trim().toLowerCase()
  if (needle === "") return true
  const haystack = [
    event.type,
    event.summary,
    event.agent_role ?? "",
    event.batch_id ?? "",
    event.case_id ?? "",
    event.step_id ?? "",
    detailString(event, "case_run_id") ?? "",
    ...event.refs.case,
    ...event.refs.evidence,
  ]
    .join(" ")
    .toLowerCase()
  return haystack.includes(needle)
}

/** Results-tab deep link of one case run (execution events carry `case_run_id`). */
function caseHref(versionId: string, event: VersionEvent): string | null {
  const target = detailString(event, "case_run_id")
  if (target === null) return null
  const params = new URLSearchParams({ tab: "results", caseRun: target })
  return `/versions/${encodeURIComponent(versionId)}?${params.toString()}`
}

/** Results-tab deep link that opens one evidence item. */
function evidenceHref(versionId: string, evidenceId: string): string {
  return `/versions/${encodeURIComponent(versionId)}?tab=results&evidence=${encodeURIComponent(
    evidenceId,
  )}`
}

function describeLoadError(error: unknown): string {
  if (error instanceof Error && error.message !== "") return error.message
  return "未知错误"
}

function formatTimestamp(value: string): string {
  if (value === "") return ""
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString("zh-CN", { hour12: false })
}

type BroadcastRowProps = {
  event: VersionEvent
  versionId: string
}

function BroadcastRow({ event, versionId }: BroadcastRowProps) {
  const severity = eventSeverity(event)
  const meta = SEVERITY_META[severity]
  const caseLink = caseHref(versionId, event)
  const anomaly = severity !== "info"

  return (
    <li
      data-testid={`broadcast-row-${event.seq}`}
      data-seq={event.seq}
      data-anomaly={anomaly ? "true" : "false"}
      data-severity={severity}
      className={
        anomaly
          ? "rounded-xl border border-amber-500/30 bg-amber-500/5 px-3 py-2.5"
          : "rounded-xl border border-border bg-card px-3 py-2.5"
      }
    >
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-mono text-[11px] text-muted-foreground">{`#${event.seq}`}</span>
        <span className="text-xs font-medium">{TYPE_LABELS[event.type] ?? event.type}</span>
        <StatusBadge variant={meta.variant}>{meta.label}</StatusBadge>
        {event.agent_role === null ? null : (
          <span className="rounded-full bg-muted px-2 py-0.5 text-[11px] text-muted-foreground">
            {event.agent_role}
          </span>
        )}
        {event.ts === "" ? null : (
          <time dateTime={event.ts} className="ml-auto text-[11px] text-muted-foreground">
            {formatTimestamp(event.ts)}
          </time>
        )}
      </div>

      <p className="mt-1.5 text-sm leading-relaxed">{event.summary === "" ? "—" : event.summary}</p>

      {caseLink === null && event.refs.evidence.length === 0 ? null : (
        <div className="mt-1.5 flex flex-wrap items-center gap-3">
          {caseLink === null ? null : (
            <a href={caseLink} className={LINK_CLASS}>
              查看用例
            </a>
          )}
          {event.refs.evidence.map((evidenceId) => (
            <a key={evidenceId} href={evidenceHref(versionId, evidenceId)} className={LINK_CLASS}>
              {`证据 ${evidenceId}`}
            </a>
          ))}
        </div>
      )}
    </li>
  )
}

export type BroadcastTabProps = {
  /** P3 version id; a decimal string from the route segment. */
  versionId: string
}

/**
 * Real-time execution broadcast (spec §9.3): the full version-channel timeline in
 * descending order with anomalies pinned first, type/agent/severity filters,
 * search, snapshot-cursor pagination (`before_seq` + `has_more`), case/evidence
 * deep links and the live/reconnecting status of `useVersionStream`.
 */
export function BroadcastTab({ versionId }: BroadcastTabProps) {
  const stream = useVersionStream(versionId)
  const [historyEvents, setHistoryEvents] = useState<VersionEvent[]>([])
  const [hasMore, setHasMore] = useState(false)
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState("")
  const [loadingOlder, setLoadingOlder] = useState(false)
  const [olderError, setOlderError] = useState("")
  const [typeFilter, setTypeFilter] = useState("")
  const [agentFilter, setAgentFilter] = useState("")
  const [severityFilter, setSeverityFilter] = useState("")
  const [query, setQuery] = useState("")
  const requestRef = useRef(0)

  const loadInitial = useCallback(async (): Promise<void> => {
    const requestId = ++requestRef.current
    setLoading(true)
    setLoadError("")
    setHistoryEvents([])
    setHasMore(false)
    setOlderError("")
    try {
      const snapshot = await fetchVersionEvents(versionId, { limit: PAGE_SIZE })
      if (requestRef.current !== requestId) return
      setHistoryEvents(snapshot.events)
      setHasMore(snapshot.hasMore)
    } catch (caught) {
      if (requestRef.current !== requestId) return
      setLoadError(describeLoadError(caught))
    } finally {
      if (requestRef.current === requestId) setLoading(false)
    }
  }, [versionId])

  useEffect(() => {
    void loadInitial()
    return () => {
      // Invalidate a history request still in flight for this mount/version.
      requestRef.current += 1
    }
  }, [loadInitial])

  const events = useMemo(
    () => mergeEvents(historyEvents, stream.events),
    [historyEvents, stream.events],
  )

  const oldestSeq = events[0]?.seq ?? null
  const typeOptions = useMemo(
    () => [...new Set(events.map((event) => event.type))].sort(),
    [events],
  )
  const agentOptions = useMemo(
    () =>
      [...new Set(events.flatMap((event) => (event.agent_role ? [event.agent_role] : [])))].sort(),
    [events],
  )

  const visibleEvents = useMemo(() => {
    return events
      .filter((event) => typeFilter === "" || event.type === typeFilter)
      .filter((event) => agentFilter === "" || event.agent_role === agentFilter)
      .filter((event) => severityFilter === "" || eventSeverity(event) === severityFilter)
      .filter((event) => matchesQuery(event, query))
      .sort(compareEvents)
  }, [events, typeFilter, agentFilter, severityFilter, query])

  const loadOlder = useCallback(async (): Promise<void> => {
    if (oldestSeq === null || loadingOlder) return
    const requestId = requestRef.current
    setLoadingOlder(true)
    setOlderError("")
    try {
      const snapshot = await fetchVersionEvents(versionId, {
        beforeSeq: oldestSeq,
        limit: PAGE_SIZE,
      })
      if (requestRef.current !== requestId) return
      setHistoryEvents((previous) => mergeEvents(previous, snapshot.events))
      setHasMore(snapshot.hasMore)
    } catch (caught) {
      if (requestRef.current !== requestId) return
      setOlderError(describeLoadError(caught))
    } finally {
      if (requestRef.current === requestId) setLoadingOlder(false)
    }
  }, [versionId, oldestSeq, loadingOlder])

  const streamMeta = STREAM_STATUS_META[stream.status]
  const reconnectable = stream.status === "closed" || stream.status === "reconnecting"
  const showLoading = loading && events.length === 0 && loadError === ""

  return (
    <section data-testid="broadcast-tab" className="space-y-3">
      <header className="flex flex-wrap items-start justify-between gap-3 rounded-2xl border border-border bg-card p-4">
        <div className="min-w-0">
          <h2 className="text-base font-semibold tracking-tight">实时播报</h2>
          <p className="mt-1 text-sm text-muted-foreground">
            版本频道的全量执行事件，异常事件置顶显示。
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <span data-testid="stream-status" data-status={stream.status}>
            <StatusBadge variant={streamMeta.variant}>{streamMeta.label}</StatusBadge>
          </span>
          {reconnectable ? (
            <button
              type="button"
              data-testid="broadcast-reconnect"
              onClick={stream.reconnect}
              className={PRIMARY_BUTTON_CLASS}
            >
              重新连接
            </button>
          ) : null}
        </div>
      </header>

      <div className="grid gap-2 rounded-2xl border border-border bg-card p-3 sm:grid-cols-2 lg:grid-cols-4">
        <label className="text-xs font-medium text-muted-foreground">
          类型
          <select
            data-testid="broadcast-filter-type"
            value={typeFilter}
            onChange={(event) => setTypeFilter(event.target.value)}
            className={FIELD_CLASS}
          >
            <option value="">全部类型</option>
            {typeOptions.map((type) => (
              <option key={type} value={type}>
                {TYPE_LABELS[type] ?? type}
              </option>
            ))}
          </select>
        </label>

        <label className="text-xs font-medium text-muted-foreground">
          Agent
          <select
            data-testid="broadcast-filter-agent"
            value={agentFilter}
            onChange={(event) => setAgentFilter(event.target.value)}
            className={FIELD_CLASS}
          >
            <option value="">全部 Agent</option>
            {agentOptions.map((agent) => (
              <option key={agent} value={agent}>
                {agent}
              </option>
            ))}
          </select>
        </label>

        <label className="text-xs font-medium text-muted-foreground">
          严重级
          <select
            data-testid="broadcast-filter-severity"
            value={severityFilter}
            onChange={(event) => setSeverityFilter(event.target.value)}
            className={FIELD_CLASS}
          >
            <option value="">全部严重级</option>
            <option value="error">错误</option>
            <option value="warning">警告</option>
            <option value="info">信息</option>
          </select>
        </label>

        <label className="text-xs font-medium text-muted-foreground">
          搜索事件
          <input
            type="search"
            data-testid="broadcast-search"
            value={query}
            placeholder="摘要 / 类型 / 用例 / 证据"
            onChange={(event) => setQuery(event.target.value)}
            className={FIELD_CLASS}
          />
        </label>
      </div>

      {loadError === "" ? null : (
        <div
          data-testid="broadcast-error"
          role="alert"
          className="space-y-2 rounded-xl border border-destructive/30 bg-destructive/10 px-4 py-3 text-sm text-destructive"
        >
          <p>{`加载执行事件失败：${loadError}`}</p>
          <button type="button" onClick={() => void loadInitial()} className={PRIMARY_BUTTON_CLASS}>
            重试
          </button>
        </div>
      )}

      {olderError === "" ? null : (
        <p
          data-testid="broadcast-older-error"
          role="alert"
          className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
        >
          {`加载更早事件失败：${olderError}`}
        </p>
      )}

      {showLoading ? (
        <p
          data-testid="broadcast-loading"
          role="status"
          className="rounded-xl border border-dashed border-border px-4 py-10 text-sm text-muted-foreground"
        >
          正在加载执行事件…
        </p>
      ) : null}

      {!loading && loadError === "" && events.length === 0 ? (
        <div data-testid="broadcast-empty">
          <EmptyState
            icon={Radio}
            title="暂无执行事件"
            description="批次开始后，执行事件会实时出现在这里。"
          />
        </div>
      ) : null}

      {events.length > 0 && visibleEvents.length === 0 ? (
        <div
          data-testid="broadcast-filtered-empty"
          className="rounded-xl border border-dashed border-border px-4 py-10 text-center text-sm text-muted-foreground"
        >
          没有符合筛选条件的事件
        </div>
      ) : null}

      {visibleEvents.length === 0 ? null : (
        <ol data-testid="broadcast-timeline" className="space-y-1.5">
          {visibleEvents.map((event) => (
            <BroadcastRow key={event.seq} event={event} versionId={versionId} />
          ))}
        </ol>
      )}

      {hasMore && events.length > 0 ? (
        <button
          type="button"
          data-testid="broadcast-load-older"
          onClick={() => void loadOlder()}
          disabled={loadingOlder}
          className={PRIMARY_BUTTON_CLASS}
        >
          {loadingOlder ? "加载中…" : "加载更早事件"}
        </button>
      ) : null}
    </section>
  )
}
