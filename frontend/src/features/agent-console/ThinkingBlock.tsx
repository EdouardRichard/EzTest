"use client"

import { BrainCircuit, ChevronRight } from "lucide-react"
import { useState } from "react"

import type { AgentMessageDetail } from "./agent-api"

export type ThinkingBlockProps = {
  messageId: number | null
  /** Preview text already streamed through thinking events. */
  preview?: string
  getDetail?: (messageId: number) => Promise<AgentMessageDetail>
}

/**
 * Collapsed thinking row. Expanding it lazily loads the full reasoning through
 * `getDetail(message_id)`; the event chunks are only a preview and may be
 * truncated, so the persisted message stays authoritative.
 */
export function ThinkingBlock({ messageId, preview = "", getDetail }: ThinkingBlockProps) {
  const [expanded, setExpanded] = useState(false)
  const [reasoning, setReasoning] = useState("")
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState("")
  const [loaded, setLoaded] = useState(false)

  async function toggle() {
    const next = !expanded
    setExpanded(next)
    if (!next || loaded || messageId === null || !getDetail) return
    setLoaded(true)
    setLoading(true)
    setError("")
    try {
      const detail = await getDetail(messageId)
      setReasoning(detail.reasoning)
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : "无法加载推理详情。")
      setLoaded(false)
    } finally {
      setLoading(false)
    }
  }

  const text = reasoning || preview

  return (
    <div className="rounded-xl border border-dashed border-border bg-muted/30">
      <button
        type="button"
        aria-label="思考"
        aria-expanded={expanded}
        onClick={() => void toggle()}
        className="flex w-full items-center gap-2 px-3 py-2 text-left text-xs font-medium text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <BrainCircuit className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
        <span>思考</span>
        <ChevronRight
          className={
            expanded
              ? "ml-auto h-3.5 w-3.5 rotate-90 transition-transform"
              : "ml-auto h-3.5 w-3.5 transition-transform"
          }
          aria-hidden="true"
        />
      </button>
      {expanded ? (
        <div className="border-t border-border px-3 py-2">
          {loading ? <p className="text-xs text-muted-foreground">正在加载推理…</p> : null}
          {error ? (
            <p role="alert" className="text-xs text-destructive">
              {error}
            </p>
          ) : null}
          {!loading && text ? (
            <p className="whitespace-pre-wrap break-words text-sm leading-relaxed text-muted-foreground">
              {text}
            </p>
          ) : null}
          {!loading && !error && !text ? (
            <p className="text-xs text-muted-foreground">暂无推理内容。</p>
          ) : null}
        </div>
      ) : null}
    </div>
  )
}
