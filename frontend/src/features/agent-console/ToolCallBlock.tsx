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

/** The collapsed row stays one line: cap the args summary at 80 characters. */
const ARGS_SUMMARY_LIMIT = 80

export type ToolCallBlockProps = {
  callId: string
  name: string
  args?: string
  result?: string
  status?: string
  durationMs?: number | null
  errorCode?: string
  /**
   * `tool_use.message_id` — the assistant message that owns the tool_calls, so
   * the full arguments can be lazily loaded from the authoritative detail.
   */
  assistantMessageId?: number | null
  /** The persisted tool message id used for the lazy result detail request. */
  resultMessageId?: number | null
  getDetail?: (messageId: number) => Promise<AgentMessageDetail>
}

function statusLabel(status?: string): string {
  if (!status) return ""
  return STATUS_LABELS[status] ?? status
}

/** Pretty-prints a JSON args string; non-JSON previews are shown verbatim. */
function formatArguments(raw: string): string {
  const text = raw.trim()
  if (!text) return ""
  try {
    return JSON.stringify(JSON.parse(text), null, 2)
  } catch {
    return raw
  }
}

/** One-line, capped summary of the args preview (or of the loaded full args). */
function summarizeArguments(raw: string, limit = ARGS_SUMMARY_LIMIT): string {
  const text = raw.replace(/\s+/g, " ").trim()
  if (text.length <= limit) return text
  return `${text.slice(0, limit - 1)}…`
}

function errorMessage(caught: unknown): string {
  return caught instanceof Error ? caught.message : "无法加载工具详情。"
}

/**
 * Finds the call matching the streamed `call_id` in the assistant detail. A
 * single-call message is unambiguous even if the id shape changed, and a unique
 * name match is the last equivalent before falling back to the streamed args.
 */
function findToolCall(
  detail: AgentMessageDetail | null | undefined,
  callId: string,
  name: string,
): { id: string; name: string; arguments: string } | undefined {
  const calls = Array.isArray(detail?.tool_calls) ? detail.tool_calls : []
  const named = calls.filter((candidate) => candidate.name === name)
  return (
    calls.find((candidate) => candidate.id === callId) ??
    (calls.length === 1 ? calls[0] : undefined) ??
    (named.length === 1 ? named[0] : undefined)
  )
}

/**
 * One-line tool call row. The collapsed line is the E2E contract
 * (`工具 <name>`) plus a capped args summary; expanding it lazily loads the
 * full arguments from the owning assistant message detail and the full result
 * from the tool message detail, keeping the streamed previews as fallbacks.
 */
export function ToolCallBlock({
  callId,
  name,
  args = "",
  result = "",
  status = "",
  durationMs = null,
  errorCode = "",
  assistantMessageId = null,
  resultMessageId = null,
  getDetail,
}: ToolCallBlockProps) {
  const [expanded, setExpanded] = useState(false)
  const [detailArgs, setDetailArgs] = useState("")
  const [detailName, setDetailName] = useState("")
  const [detailResult, setDetailResult] = useState("")
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState("")
  const [loaded, setLoaded] = useState(false)

  const rawArgs = detailArgs || args
  const fullArgs = formatArguments(rawArgs)
  const argsSummary = summarizeArguments(rawArgs)
  const fullResult = detailResult || result
  const displayName = detailName || name
  const label = statusLabel(status)
  const duration = typeof durationMs === "number" ? `${durationMs}ms` : ""

  async function loadAssistantDetail(messageId: number): Promise<void> {
    if (!getDetail) return
    const detail = await getDetail(messageId)
    const call = findToolCall(detail, callId, name)
    if (!call) return
    if (call.arguments) setDetailArgs(call.arguments)
    if (call.name) setDetailName(call.name)
  }

  async function loadToolResult(messageId: number): Promise<void> {
    if (!getDetail) return
    const detail = await getDetail(messageId)
    const content = (Array.isArray(detail?.content) ? detail.content : [])
      .map((part) => part.text ?? "")
      .join("")
    if (content) setDetailResult(content)
  }

  async function toggle() {
    const next = !expanded
    setExpanded(next)
    if (!next || loaded || !getDetail) return
    const argsMessageId = assistantMessageId ?? null
    const toolMessageId = resultMessageId ?? null
    if (argsMessageId === null && toolMessageId === null) return
    setLoaded(true)
    setLoading(true)
    setError("")
    const requests: Promise<void>[] = []
    if (argsMessageId !== null) requests.push(loadAssistantDetail(argsMessageId))
    if (toolMessageId !== null) requests.push(loadToolResult(toolMessageId))
    const outcomes = await Promise.allSettled(requests)
    const failure = outcomes.find((outcome) => outcome.status === "rejected")
    if (failure && failure.status === "rejected") {
      setError(errorMessage(failure.reason))
      setLoaded(false)
    }
    setLoading(false)
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
        <span className="max-w-[45%] shrink-0 truncate font-mono text-xs font-medium">
          工具 {name}
        </span>
        {argsSummary ? (
          <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-muted-foreground">
            · {argsSummary}
          </span>
        ) : (
          <span className="flex-1" />
        )}
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
          <p className="flex flex-wrap items-center gap-x-2 text-[11px] text-muted-foreground">
            <span className="font-mono text-foreground">{displayName}</span>
            {displayName === callId ? null : <span className="font-mono">{callId}</span>}
          </p>
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
