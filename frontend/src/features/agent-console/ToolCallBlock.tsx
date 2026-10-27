"use client"

import { ChevronRight, Wrench } from "lucide-react"
import { useState } from "react"

import type { AgentMessageDetail } from "./agent-api"

const STATUS_LABELS: Record<string, string> = {
  ok: "成功",
  error: "失败",
  timeout: "超时",
  cancelled: "已取消",
  denied: "被拒绝",
  running: "执行中",
}

export type ToolCallBlockProps = {
  callId: string
  name: string
  args?: string
  result?: string
  status?: string
  durationMs?: number | null
  errorCode?: string
  /** The persisted tool message id used for the lazy detail request. */
  resultMessageId?: number | null
  getDetail?: (messageId: number) => Promise<AgentMessageDetail>
}

function statusLabel(status?: string): string {
  if (!status) return ""
  return STATUS_LABELS[status] ?? status
}

/**
 * One-line tool call row. The collapsed line is the E2E contract
 * (`工具 <name>`); expanding it lazily loads the full arguments/result from the
 * tool message detail and shows the status, duration and error code.
 */
export function ToolCallBlock({
  callId,
  name,
  args = "",
  result = "",
  status = "",
  durationMs = null,
  errorCode = "",
  resultMessageId = null,
  getDetail,
}: ToolCallBlockProps) {
  const [expanded, setExpanded] = useState(false)
  const [detailArgs, setDetailArgs] = useState("")
  const [detailResult, setDetailResult] = useState("")
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState("")
  const [loaded, setLoaded] = useState(false)

  const fullArgs = detailArgs || args
  const fullResult = detailResult || result
  const label = statusLabel(status)
  const duration = typeof durationMs === "number" ? `${durationMs}ms` : ""

  async function toggle() {
    const next = !expanded
    setExpanded(next)
    if (!next || loaded || resultMessageId === null || !getDetail) return
    setLoaded(true)
    setLoading(true)
    setError("")
    try {
      const detail = await getDetail(resultMessageId)
      const call =
        detail.tool_calls?.find((candidate) => candidate.id === callId) ??
        (detail.tool_calls?.length === 1 ? detail.tool_calls[0] : undefined)
      if (call?.arguments) setDetailArgs(call.arguments)
      const content = detail.content.map((part) => part.text ?? "").join("")
      if (content) setDetailResult(content)
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : "无法加载工具详情。")
      setLoaded(false)
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="overflow-hidden rounded-xl border border-border bg-card/70">
      <button
        type="button"
        aria-label={`工具 ${name}`}
        aria-expanded={expanded}
        onClick={() => void toggle()}
        className="flex w-full items-center gap-2 px-3 py-2 text-left transition-colors hover:bg-muted/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <Wrench className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
        <span className="min-w-0 truncate font-mono text-xs font-medium">工具 {name}</span>
        {label ? (
          <span className="shrink-0 rounded-full border border-border px-1.5 py-0.5 text-[11px] text-muted-foreground">
            {label}
          </span>
        ) : null}
        {duration ? (
          <span className="shrink-0 text-[11px] text-muted-foreground">{duration}</span>
        ) : null}
        {errorCode ? (
          <span className="shrink-0 font-mono text-[11px] text-destructive">{errorCode}</span>
        ) : null}
        <ChevronRight
          className={
            expanded
              ? "ml-auto h-3.5 w-3.5 shrink-0 rotate-90 transition-transform"
              : "ml-auto h-3.5 w-3.5 shrink-0 transition-transform"
          }
          aria-hidden="true"
        />
      </button>
      {expanded ? (
        <div className="space-y-2 border-t border-border px-3 py-2">
          {loading ? <p className="text-xs text-muted-foreground">正在加载工具详情…</p> : null}
          {error ? (
            <p role="alert" className="text-xs text-destructive">
              {error}
            </p>
          ) : null}
          <div>
            <p className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
              参数
            </p>
            <pre className="mt-1 overflow-x-auto whitespace-pre-wrap break-all rounded-lg bg-muted/50 p-2 font-mono text-xs">
              {fullArgs || "（无）"}
            </pre>
          </div>
          <div>
            <p className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
              结果
            </p>
            <pre className="mt-1 overflow-x-auto whitespace-pre-wrap break-all rounded-lg bg-muted/50 p-2 font-mono text-xs">
              {fullResult || "（无结果）"}
            </pre>
          </div>
        </div>
      ) : null}
    </div>
  )
}
