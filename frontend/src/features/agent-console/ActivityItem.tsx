"use client"

import { AlertTriangle } from "lucide-react"

import type { AgentEvent, AgentMessageDetail } from "./agent-api"
import { ThinkingBlock } from "./ThinkingBlock"
import { ToolCallBlock } from "./ToolCallBlock"

/** One rendered transcript row; the tool/thinking/text rows are pre-merged. */
export type TranscriptEntry =
  | { kind: "text"; key: string; messageId: number; part: number; text: string }
  | { kind: "thinking"; key: string; messageId: number | null; part: number; text: string }
  | {
      kind: "tool"
      key: string
      callId: string
      name: string
      use: AgentEvent | null
      result: AgentEvent | null
    }
  | { kind: "decision"; key: string; event: AgentEvent }
  | { kind: "notice"; key: string; event: AgentEvent }

/** Reads `detail.kind`, the frozen V1 activity discriminator. */
export function activityKind(event: AgentEvent): string {
  const kind = event.detail.kind
  return typeof kind === "string" ? kind : ""
}

export function readDetailText(event: AgentEvent, key: string): string {
  const value = event.detail[key]
  return typeof value === "string" ? value : ""
}

export function readDetailNumber(event: AgentEvent, key: string): number | null {
  const value = event.detail[key]
  return typeof value === "number" && Number.isFinite(value) ? value : null
}

/** Reads an id that may arrive as a JSON number or a decimal string. */
export function readDetailId(event: AgentEvent, key: string): string | null {
  const value = event.detail[key]
  if (typeof value === "string" && value !== "") return value
  if (typeof value === "number" && Number.isFinite(value)) return String(value)
  return null
}

export type ActivityItemProps = {
  entry: TranscriptEntry
  getDetail?: (messageId: number) => Promise<AgentMessageDetail>
}

/** Dispatches one merged transcript row to its kind-specific renderer. */
export function ActivityItem({ entry, getDetail }: ActivityItemProps) {
  switch (entry.kind) {
    case "text": {
      return <p className="whitespace-pre-wrap break-words text-sm leading-relaxed">{entry.text}</p>
    }
    case "thinking": {
      return (
        <ThinkingBlock messageId={entry.messageId} preview={entry.text} getDetail={getDetail} />
      )
    }
    case "tool": {
      const use = entry.use
      const result = entry.result
      return (
        <ToolCallBlock
          callId={entry.callId}
          name={entry.name}
          args={use ? readDetailText(use, "args") : ""}
          result={result ? readDetailText(result, "result") : ""}
          status={result ? readDetailText(result, "status") : use ? "running" : ""}
          durationMs={result ? readDetailNumber(result, "duration_ms") : null}
          errorCode={result ? readDetailText(result, "error_code") : ""}
          assistantMessageId={use ? readDetailNumber(use, "message_id") : null}
          resultMessageId={result ? readDetailNumber(result, "message_id") : null}
          getDetail={getDetail}
        />
      )
    }
    case "decision": {
      const decision = readDetailText(entry.event, "decision") || "decision"
      const reason = readDetailText(entry.event, "reason")
      return (
        <div className="flex items-start gap-2 rounded-xl border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-sm text-amber-800 dark:text-amber-200">
          <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden="true" />
          <p className="min-w-0 break-words">
            <span className="font-medium">决策：{decision}</span>
            {reason ? (
              <span className="ml-2 text-amber-700 dark:text-amber-300">原因：{reason}</span>
            ) : null}
          </p>
        </div>
      )
    }
    case "notice": {
      const kind = activityKind(entry.event)
      return (
        <p className="text-xs text-muted-foreground">{entry.event.summary || kind || "未知活动"}</p>
      )
    }
  }
}
