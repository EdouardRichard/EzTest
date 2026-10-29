"use client"

import { Gauge, Loader2, RefreshCw, ShieldAlert } from "lucide-react"
import { useCallback, useEffect, useState } from "react"

import { EmptyState } from "@/components/ui/EmptyState"
import { useAuth } from "@/features/auth/AuthProvider"
import { ApiError } from "@/lib/api-client"
import { listEvalRuns } from "./api"
import { formatRunTime, modeLabel, QualitySummary, runStatusBadge } from "./QualitySummary"
import type { EvalRun } from "./types"

/** Number of historical runs loaded by the dashboard (server max is 50). */
const HISTORY_LIMIT = 20

/** Renders a load failure with its request id when the API supplied one. */
function describeError(error: unknown): string {
  if (error instanceof ApiError) {
    const requestId = error.requestId === "" ? "" : `（request id: ${error.requestId}）`
    if (error.status === 0) return `无法连接 API 服务，请确认后端已启动。${requestId}`
    return `${error.message}${requestId}`
  }
  return error instanceof Error ? error.message : "未知错误"
}

/**
 * `/quality` admin dashboard (design spec §9.1/§12 #28): the latest run with its
 * five KPI groups, thresholds and verdicts, plus the recent history. The page is
 * administrator-only; a normal user gets an explicit read-only denial and the
 * API itself answers 403 (spec §11.5). The data is never cached across requests.
 */
export function QualityPage() {
  const { user } = useAuth()
  const isAdmin = user?.role === "admin"

  const [runs, setRuns] = useState<EvalRun[]>([])
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [error, setError] = useState("")

  const load = useCallback(async (signal: AbortSignal) => {
    try {
      const next = await listEvalRuns(HISTORY_LIMIT, { signal })
      if (signal.aborted) return
      setRuns(next)
      setError("")
    } catch (cause) {
      if (signal.aborted) return
      setError(describeError(cause))
    } finally {
      if (!signal.aborted) {
        setLoading(false)
        setRefreshing(false)
      }
    }
  }, [])

  useEffect(() => {
    if (!isAdmin) {
      setLoading(false)
      return
    }
    const controller = new AbortController()
    setLoading(true)
    void load(controller.signal)
    return () => controller.abort()
  }, [isAdmin, load])

  function handleRefresh(): void {
    if (refreshing) return
    setRefreshing(true)
    const controller = new AbortController()
    void load(controller.signal)
  }

  if (!isAdmin) {
    return (
      <section data-testid="quality-page-denied" className="space-y-4">
        <header>
          <h1 className="text-2xl font-semibold tracking-tight">质量中心</h1>
          <p className="mt-1 text-sm text-muted-foreground">评测指标与基线（管理员）。</p>
        </header>
        <EmptyState
          icon={ShieldAlert}
          title="仅管理员可查看评测"
          description="评测结果包含质量门禁与基线信息，普通用户没有访问权限。"
        />
      </section>
    )
  }

  const latest = runs[0] ?? null

  return (
    <section data-testid="quality-page" className="space-y-4">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">质量中心</h1>
          <p className="mt-1 text-sm text-muted-foreground">
            确定性评测门禁与真实 LLM 报告（最近 {HISTORY_LIMIT} 次）。
          </p>
        </div>
        <button
          type="button"
          data-testid="quality-refresh"
          onClick={handleRefresh}
          disabled={loading || refreshing}
          className="inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium text-foreground hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50"
        >
          <RefreshCw
            className={refreshing ? "h-4 w-4 animate-spin" : "h-4 w-4"}
            aria-hidden="true"
          />
          刷新
        </button>
      </header>

      {loading && runs.length === 0 ? (
        <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
          正在加载评测结果…
        </p>
      ) : error !== "" ? (
        <div
          role="alert"
          data-testid="quality-error"
          className="flex flex-wrap items-center gap-2 rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
        >
          <span>加载评测结果失败：{error}</span>
          <button
            type="button"
            onClick={() => handleRefresh()}
            className="rounded-lg border border-border bg-card px-2.5 py-1 text-xs font-medium text-foreground hover:bg-muted"
          >
            重试
          </button>
        </div>
      ) : latest === null ? (
        <EmptyState
          icon={Gauge}
          title="尚未运行评测"
          description="运行 scripts/eval.ps1 生成确定性评测结果；结果会写入质量中心的最近运行。"
        />
      ) : (
        <>
          <QualitySummary run={latest} />

          <section
            data-testid="quality-history"
            aria-labelledby="quality-history-title"
            className="rounded-2xl border border-border bg-card p-4"
          >
            <h2 id="quality-history-title" className="text-sm font-semibold tracking-tight">
              历史趋势
            </h2>
            <div className="mt-3 overflow-x-auto">
              <table className="w-full min-w-[40rem] border-collapse text-sm">
                <caption className="sr-only">最近评测运行历史</caption>
                <thead>
                  <tr className="border-b border-border text-left text-xs uppercase tracking-wide text-muted-foreground">
                    <th scope="col" className="px-2 py-2 font-medium">
                      运行
                    </th>
                    <th scope="col" className="px-2 py-2 font-medium">
                      时间
                    </th>
                    <th scope="col" className="px-2 py-2 font-medium">
                      轨道
                    </th>
                    <th scope="col" className="px-2 py-2 font-medium">
                      语料
                    </th>
                    <th scope="col" className="px-2 py-2 font-medium">
                      状态
                    </th>
                    <th scope="col" className="px-2 py-2 font-medium">
                      未达标
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {runs.map((run) => {
                    const status = runStatusBadge(run.status)
                    const failed = run.metrics.filter((metric) => !metric.passed).length
                    return (
                      <tr
                        key={run.id}
                        data-testid={`quality-history-row-${run.id}`}
                        className="border-b border-border/60 last:border-0"
                      >
                        <th scope="row" className="px-2 py-2 text-left font-medium tabular-nums">
                          #{run.id}
                        </th>
                        <td className="px-2 py-2 text-muted-foreground">
                          {formatRunTime(run.finishedAt ?? run.startedAt)}
                        </td>
                        <td className="px-2 py-2">{modeLabel(run.mode)}</td>
                        <td className="px-2 py-2 text-muted-foreground">{run.corpusVersion}</td>
                        <td className="px-2 py-2">{status.label}</td>
                        <td className="px-2 py-2 tabular-nums text-muted-foreground">
                          {run.metrics.length === 0 ? "—" : `${failed} / ${run.metrics.length}`}
                        </td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>
          </section>
        </>
      )}
    </section>
  )
}
