"use client"

import { Loader2 } from "lucide-react"
import { useEffect, useId, useMemo, useRef, useState } from "react"

import { StatusBadge, type StatusBadgeVariant } from "@/components/ui/StatusBadge"

import { mindmapApi } from "./api"
import { describeMindmapError } from "./error-messages"
import type {
  CaseRunView,
  LatestRunView,
  MindmapGraph,
  RunKind,
  RunStatus,
  StartRunBody,
} from "./types"

/** Fallback poll cadence while a run is queued/running (spec §9.2). */
export const RUN_POLL_INTERVAL_MS = 2000

const RUN_KINDS: readonly {
  kind: RunKind
  label: string
  description: string
  buttonTestId: string
}[] = [
  {
    kind: "design",
    label: "测试设计",
    description: "根据已确认需求与应用模型生成场景和用例。",
    buttonTestId: "btn-generate",
  },
  {
    kind: "review",
    label: "设计评审",
    description: "对当前用例图产出评审发现项（不修改用例）。",
    buttonTestId: "btn-generate-review",
  },
  {
    kind: "data_plan",
    label: "数据规划",
    description: "为已有用例补齐前置条件与测试数据。",
    buttonTestId: "btn-generate-data-plan",
  },
]

const RUN_KIND_LABEL: Record<RunKind, string> = {
  design: "测试设计",
  review: "设计评审",
  data_plan: "数据规划",
  explore: "应用探索",
}

const RUN_STATUS_META: Record<RunStatus, { label: string; variant: StatusBadgeVariant }> = {
  queued: { label: "排队中", variant: "neutral" },
  running: { label: "运行中", variant: "info" },
  done: { label: "已完成", variant: "success" },
  failed: { label: "失败", variant: "danger" },
  degraded: { label: "降级", variant: "warning" },
}

const RUN_REASON_LABEL: Record<string, string> = {
  ai_unavailable: "AI 服务暂不可用",
  invalid_case_proposal: "AI 返回的用例内容不合法",
  environment_unreachable: "环境不可达",
  environment_not_allowed: "环境不在访问白名单内",
  agent_timeout: "Agent 运行超时",
  agent_start_timeout: "Agent 启动超时",
  agent_failed: "Agent 运行失败",
  worker_restart: "服务重启导致运行中断",
}

const BUTTON_CLASS =
  "inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-2.5 py-1.5 text-xs font-medium text-foreground transition-colors hover:bg-muted disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"

function isActive(status: RunStatus): boolean {
  return status === "queued" || status === "running"
}

function describeRunReason(reason: string): string {
  if (reason === "") return "未知原因"
  return RUN_REASON_LABEL[reason] ?? reason
}

/** Projects a `latestRuns` summary into the detail shape for first paint. */
function summaryToRun(summary: LatestRunView, kind: RunKind, versionId: string): CaseRunView {
  return {
    id: summary.id,
    versionId,
    kind,
    status: summary.status,
    reason: "",
    stats: null,
    errors: [],
    agentSessionId: null,
    agentRunId: null,
    model: "",
    promptVersion: "",
    createdBy: 0,
    createdAt: "",
    startedAt: null,
    finishedAt: summary.finishedAt ?? null,
  }
}

function statNumber(stats: Record<string, unknown> | null, key: string): number | null {
  if (stats === null) return null
  const value = stats[key]
  return typeof value === "number" && Number.isFinite(value) ? value : null
}

function statArrayLength(stats: Record<string, unknown> | null, key: string): number | null {
  if (stats === null) return null
  const value = stats[key]
  if (Array.isArray(value)) return value.length
  return typeof value === "number" && Number.isFinite(value) ? value : null
}

/** Renders the spec §9.2 run counters, omitting zero values. */
export function formatRunStats(stats: Record<string, unknown> | null): string {
  if (stats === null) return ""
  const parts: string[] = []
  if (stats.contextTruncated === true) parts.push("上下文已截断")
  const anchorsDropped = statNumber(stats, "anchorsDropped")
  if (anchorsDropped !== null && anchorsDropped > 0) parts.push(`锚点丢弃 ${anchorsDropped}`)
  const skipped = statNumber(stats, "skipped")
  if (skipped !== null && skipped > 0) parts.push(`跳过 ${skipped}`)
  const validationErrors = statArrayLength(stats, "validationErrors")
  if (validationErrors !== null && validationErrors > 0) {
    parts.push(`校验错误 ${validationErrors}`)
  }
  return parts.join(" · ")
}

export type RunPanelProps = {
  /** P3-owned version id; never re-parsed (spec §7.1). */
  versionId: string
  /** Server-authoritative graph snapshot carrying `latestRuns`. */
  graph: MindmapGraph
  /** Notifies the page after a run starts or reaches a terminal state. */
  onChanged?: () => void
}

/**
 * Run panel (spec §9.2): starts design/review/data_plan runs, renders the latest
 * run of each kind with its reason and counters, and keeps queued/running runs
 * fresh by polling `GET /runs/{runId}` every two seconds as the SSE fallback.
 * A failed/degraded run shows a banner with the mapped reason and the manual
 * path hint; AI unavailability never blocks manual editing. Replacing the draft
 * graph always asks for confirmation first (spec §6.7/§9.2).
 */
export function RunPanel({ versionId, graph, onChanged }: RunPanelProps) {
  const uid = useId()
  const [details, setDetails] = useState<Partial<Record<RunKind, CaseRunView>>>({})
  const [starting, setStarting] = useState<RunKind | null>(null)
  const [error, setError] = useState("")
  const [notice, setNotice] = useState("")
  const [mode, setMode] = useState<"append" | "replace">("append")
  const inFlightRef = useRef(new Set<string>())
  const onChangedRef = useRef(onChanged)

  useEffect(() => {
    onChangedRef.current = onChanged
  }, [onChanged])

  // Load the full detail behind each `latestRuns` summary (the summary omits
  // reason/stats by contract). One request per run id, retried only when the
  // graph points at a different run.
  useEffect(() => {
    let cancelled = false
    const pending = RUN_KINDS.filter(({ kind }) => {
      const summary = graph.latestRuns[kind]
      if (summary === undefined) return false
      const detail = details[kind]
      if (detail !== undefined && detail.id === summary.id) return false
      return !inFlightRef.current.has(summary.id)
    })

    for (const { kind } of pending) {
      const summary = graph.latestRuns[kind]
      if (summary === undefined) continue
      inFlightRef.current.add(summary.id)
      void mindmapApi
        .getRun(versionId, summary.id)
        .then((run) => {
          if (!cancelled) setDetails((previous) => ({ ...previous, [run.kind]: run }))
        })
        .catch(() => {
          // The summary status still renders; detail is supplementary.
        })
        .finally(() => {
          inFlightRef.current.delete(summary.id)
        })
    }

    return () => {
      cancelled = true
    }
  }, [versionId, graph.latestRuns, details])

  /** Run ids that still need polling, across every kind. */
  const activeRunIds = useMemo(() => {
    const ids = new Set<string>()
    for (const { kind } of RUN_KINDS) {
      const summary = graph.latestRuns[kind]
      const detail = details[kind]
      const id = detail?.id ?? summary?.id
      const status = detail?.status ?? summary?.status
      if (id !== undefined && status !== undefined && isActive(status)) ids.add(id)
    }
    return [...ids].sort().join(",")
  }, [details, graph.latestRuns])

  // Polling fallback (spec §9.2): 2s cadence, chained timeouts so a slow read
  // never overlaps the next one, stopped on a terminal status or unmount.
  useEffect(() => {
    if (activeRunIds === "") return
    const ids = activeRunIds.split(",")
    let cancelled = false
    let timer: ReturnType<typeof setTimeout> | null = null

    const poll = async (): Promise<void> => {
      let reachedTerminal = false
      for (const id of ids) {
        try {
          const run = await mindmapApi.getRun(versionId, id)
          if (cancelled) return
          setDetails((previous) => ({ ...previous, [run.kind]: run }))
          if (!isActive(run.status)) reachedTerminal = true
        } catch (caught) {
          if (!cancelled) setError(describeMindmapError(caught))
          return
        }
      }
      if (cancelled || reachedTerminal) {
        if (!cancelled) onChangedRef.current?.()
        return
      }
      timer = setTimeout(() => {
        void poll()
      }, RUN_POLL_INTERVAL_MS)
    }

    timer = setTimeout(() => {
      void poll()
    }, RUN_POLL_INTERVAL_MS)

    return () => {
      cancelled = true
      if (timer !== null) clearTimeout(timer)
    }
  }, [versionId, activeRunIds])

  async function start(kind: RunKind): Promise<void> {
    if (starting !== null) return
    if (kind === "design" && mode === "replace") {
      const confirmed = window.confirm("替换模式会删除本版本全部草稿用例，确认继续？")
      if (!confirmed) return
    }

    setStarting(kind)
    setError("")
    setNotice("")
    try {
      const body: StartRunBody = { kind }
      if (kind === "design") {
        body.mode = mode
        if (mode === "replace") body.confirmReplace = true
      }
      const result = await mindmapApi.startRun(versionId, body)
      const run = await mindmapApi.getRun(versionId, result.runId)
      setDetails((previous) => ({ ...previous, [run.kind]: run }))
      setNotice(
        isActive(run.status)
          ? `已提交${RUN_KIND_LABEL[kind]}运行，状态每 2 秒自动刷新。`
          : `${RUN_KIND_LABEL[kind]}运行已结束：${RUN_STATUS_META[run.status].label}`,
      )
      onChanged?.()
    } catch (caught) {
      setError(describeMindmapError(caught))
    } finally {
      setStarting(null)
    }
  }

  function latestRunFor(kind: RunKind): CaseRunView | null {
    const detail = details[kind]
    const summary = graph.latestRuns[kind]
    if (summary !== undefined && (detail === undefined || detail.id !== summary.id)) {
      return summaryToRun(summary, kind, versionId)
    }
    return detail ?? null
  }

  return (
    <section
      aria-label="AI 运行"
      data-testid="run-panel"
      className="min-w-0 rounded-2xl border border-border bg-card p-4"
    >
      <div>
        <h2 className="text-base font-semibold">AI 运行</h2>
        <p className="mt-0.5 text-xs text-muted-foreground">
          生成、评审与数据规划均异步执行；运行期间每 2 秒自动刷新状态。
        </p>
      </div>

      {error === "" ? null : (
        <p
          role="alert"
          className="mt-2 rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
        >
          {error}
        </p>
      )}
      {notice === "" ? null : (
        <p
          role="status"
          className="mt-2 rounded-xl border border-emerald-500/30 bg-emerald-500/10 px-3 py-2 text-xs text-emerald-700 dark:text-emerald-300"
        >
          {notice}
        </p>
      )}

      <div className="mt-3 space-y-2">
        {RUN_KINDS.map(({ kind, label, description, buttonTestId }) => {
          const run = latestRunFor(kind)
          const detail = details[kind]
          const detailForRun =
            run !== null && detail !== undefined && detail.id === run.id ? detail : undefined
          const statusMeta = run === null ? null : RUN_STATUS_META[run.status]
          const statsText = detailForRun === undefined ? "" : formatRunStats(detailForRun.stats)
          const failed = detailForRun !== undefined && !isActive(detailForRun.status)
          const banner = failed && detailForRun?.status !== "done" ? detailForRun : undefined
          const busy = starting === kind

          return (
            <article
              key={kind}
              data-testid={`run-row-${kind}`}
              className="rounded-xl border border-border px-3 py-2"
            >
              <div className="flex flex-wrap items-start justify-between gap-2">
                <div className="min-w-0">
                  <h3 className="text-sm font-semibold">{label}</h3>
                  <p className="mt-0.5 text-xs text-muted-foreground">{description}</p>
                </div>
                <button
                  type="button"
                  data-testid={buttonTestId}
                  onClick={() => void start(kind)}
                  disabled={starting !== null}
                  className={BUTTON_CLASS}
                >
                  {busy ? (
                    <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
                  ) : null}
                  {busy ? "正在提交…" : `开始${label}`}
                </button>
              </div>

              {kind === "design" ? (
                <fieldset className="mt-2 flex flex-wrap gap-x-4 gap-y-1 border-0 p-0">
                  <legend className="sr-only">生成模式</legend>
                  <label className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
                    <input
                      type="radio"
                      name={`${uid}-design-mode`}
                      checked={mode === "append"}
                      onChange={() => setMode("append")}
                      className="h-3.5 w-3.5 accent-primary"
                    />
                    追加生成
                  </label>
                  <label className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
                    <input
                      type="radio"
                      name={`${uid}-design-mode`}
                      checked={mode === "replace"}
                      onChange={() => setMode("replace")}
                      className="h-3.5 w-3.5 accent-primary"
                    />
                    替换现有草稿
                  </label>
                </fieldset>
              ) : null}

              <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
                <span data-testid={`run-status-${kind}`}>
                  {statusMeta === null || run === null ? (
                    "尚未运行"
                  ) : (
                    <StatusBadge variant={statusMeta.variant}>{statusMeta.label}</StatusBadge>
                  )}
                </span>
                {detailForRun !== undefined && detailForRun.reason !== "" ? (
                  <span>{`原因：${describeRunReason(detailForRun.reason)}`}</span>
                ) : null}
                {statsText === "" ? null : (
                  <span data-testid={`run-stats-${kind}`}>{`统计：${statsText}`}</span>
                )}
                {run?.finishedAt ? <span>{`完成于 ${run.finishedAt}`}</span> : null}
              </div>

              {banner === undefined ? null : (
                <div
                  role="alert"
                  data-testid={`run-banner-${kind}`}
                  className="mt-2 rounded-lg border border-amber-500/30 bg-amber-500/10 px-2.5 py-2 text-xs text-amber-800 dark:text-amber-200"
                >
                  <p>
                    {`运行${banner.status === "failed" ? "失败" : "降级"}：${describeRunReason(
                      banner.reason,
                    )}`}
                  </p>
                  <p className="mt-0.5">
                    可继续手工编辑用例并完成评审与导出；AI 不可用不影响人工路径。
                  </p>
                </div>
              )}
            </article>
          )
        })}
      </div>
    </section>
  )
}
