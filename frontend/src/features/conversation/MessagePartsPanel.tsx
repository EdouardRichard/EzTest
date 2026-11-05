"use client"

import { Paperclip } from "lucide-react"
import { useCallback, useEffect, useState } from "react"

import { Badge } from "@/components/ui/Badge"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import { RequirementCard } from "@/features/clarification/RequirementCard"
import { RichText } from "@/features/clarification/RichText"
import type { RequirementCardPart } from "@/features/clarification/types"
import { formatDateTime } from "@/lib/format"

import { getMessageParts } from "./api"
import { CaseCard } from "./CaseCard"
import { ErrorPart } from "./ErrorPart"
import { JobCard } from "./JobCard"
import { ThinkingBlock } from "./ThinkingBlock"
import { ToolCallBlock } from "./ToolCallBlock"
import type {
  CaseCardPart,
  CaseCardStatus,
  ErrorPart as ErrorPartPayload,
  JobCardPart,
  JobKind,
  JobStatus,
  MessagePartView,
  ToolCallPart,
  ToolCallStatus,
} from "./types"

export type MessagePartsPanelProps = {
  versionId: string
  conversationId: string
  messageId: string
}

/** The payload is part-shaped JSON; `part_type` is the authoritative discriminator. */
type PartPayload = Record<string, unknown>

const TOOL_STATUSES: ToolCallStatus[] = ["running", "done", "failed"]
const JOB_STATUSES: JobStatus[] = [
  "open",
  "claimed",
  "running",
  "waiting_human",
  "done",
  "failed",
  "cancelled",
]
const CASE_STATUSES: CaseCardStatus[] = ["draft", "confirmed", "stale", "superseded"]

function stringField(payload: PartPayload, key: string): string {
  const value = payload[key]
  return typeof value === "string" ? value : ""
}

function numberField(payload: PartPayload, key: string): number {
  const value = payload[key]
  return typeof value === "number" && Number.isFinite(value) ? value : 0
}

function recordField(payload: PartPayload, key: string): Record<string, unknown> | undefined {
  const value = payload[key]
  return typeof value === "object" && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : undefined
}

function stringListField(payload: PartPayload, key: string): string[] {
  const value = payload[key]
  return Array.isArray(value)
    ? value.filter((item): item is string => typeof item === "string")
    : []
}

function toolStatus(payload: PartPayload): ToolCallStatus {
  const value = stringField(payload, "status") as ToolCallStatus
  return TOOL_STATUSES.includes(value) ? value : "done"
}

function jobStatus(payload: PartPayload): JobStatus {
  const value = stringField(payload, "status") as JobStatus
  return JOB_STATUSES.includes(value) ? value : "running"
}

function caseStatus(payload: PartPayload): CaseCardStatus {
  const value = stringField(payload, "status") as CaseCardStatus
  return CASE_STATUSES.includes(value) ? value : "draft"
}

/** Wraps one history row into the typed union member its part_type names. */
function renderPart(part: MessagePartView, versionId: string) {
  const payload = part.payload as PartPayload
  switch (part.part_type) {
    case "text":
      return <RichText>{stringField(payload, "text")}</RichText>
    case "thinking": {
      const thinking = {
        type: "thinking" as const,
        text: stringField(payload, "text"),
        duration_ms: numberField(payload, "duration_ms"),
      }
      return <ThinkingBlock part={thinking} />
    }
    case "tool_call": {
      const toolCall: ToolCallPart = {
        type: "tool_call",
        call_id: stringField(payload, "call_id"),
        name: stringField(payload, "name"),
        status: toolStatus(payload),
        arguments: recordField(payload, "arguments"),
        result: recordField(payload, "result") ?? null,
        duration_ms: numberField(payload, "duration_ms"),
        error: stringField(payload, "error") || undefined,
      }
      return <ToolCallBlock part={toolCall} />
    }
    case "job_card": {
      const job: JobCardPart = {
        type: "job_card",
        job_id: stringField(payload, "job_id"),
        kind: stringField(payload, "kind") as JobKind,
        status: jobStatus(payload),
        current: numberField(payload, "current"),
        total: numberField(payload, "total"),
      }
      return <JobCard versionId={versionId} part={job} />
    }
    case "error": {
      const error: ErrorPartPayload = {
        type: "error",
        code: stringField(payload, "code"),
        message: stringField(payload, "message"),
        retryable: payload.retryable === true,
        action: stringField(payload, "action") || undefined,
        field: stringField(payload, "field") || undefined,
        allowed: stringListField(payload, "allowed"),
        hint: stringField(payload, "hint") || undefined,
        details: recordField(payload, "details"),
      }
      return <ErrorPart part={error} />
    }
    case "case_card": {
      const card: CaseCardPart = {
        type: "case_card",
        case_id: stringField(payload, "case_id"),
        case_version_id: stringField(payload, "case_version_id") || null,
        title: stringField(payload, "title") || `用例 #${stringField(payload, "case_id")}`,
        status: caseStatus(payload),
      }
      return <CaseCard versionId={versionId} part={card} />
    }
    case "requirement_card": {
      const card: RequirementCardPart = {
        type: "requirement_card",
        requirement_id: stringField(payload, "requirement_id"),
        title: stringField(payload, "title"),
        status: stringField(payload, "status") as RequirementCardPart["status"],
        revision: numberField(payload, "revision"),
      }
      return <RequirementCard part={card} />
    }
    case "attachment":
      return (
        <span
          data-testid="message-part-attachment"
          className="inline-flex items-center gap-1 rounded-full border border-border bg-muted px-2 py-0.5 text-[11px] text-muted-foreground"
        >
          <Paperclip className="size-3" aria-hidden="true" />
          {stringField(payload, "name")}
        </span>
      )
    default:
      return (
        <pre className="max-h-40 overflow-auto rounded bg-muted/40 p-1.5 text-[10px] leading-relaxed text-foreground">
          {JSON.stringify(payload, null, 2)}
        </pre>
      )
  }
}

/**
 * Append-only message parts history (`GET .../messages/{mid}/parts`). It is the
 * audit view of what the assistant actually streamed: every row keeps its `seq`
 * and `part_type`, and the renderer reuses the same part primitives as the live
 * conversation thread instead of duplicating their markup.
 */
export function MessagePartsPanel({
  versionId,
  conversationId,
  messageId,
}: MessagePartsPanelProps) {
  const [parts, setParts] = useState<MessagePartView[] | null>(null)
  const [error, setError] = useState<unknown>(null)

  const load = useCallback(async () => {
    setError(null)
    setParts(null)
    try {
      setParts(await getMessageParts(versionId, conversationId, messageId))
    } catch (cause) {
      setError(cause)
    }
  }, [versionId, conversationId, messageId])

  useEffect(() => {
    void load()
  }, [load])

  return (
    <section
      data-testid="message-parts-panel"
      aria-label="消息片段历史"
      className="mt-1 w-full space-y-2 rounded-lg border border-border bg-card px-3 py-2.5"
    >
      <header className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h3 className="text-xs font-semibold text-foreground">消息片段历史</h3>
          <p className="text-[10px] text-muted-foreground">
            append-only 流式片段 · 按 seq 排序 · 共 {parts?.length ?? 0} 条
          </p>
        </div>
        <Badge tone="neutral">message #{messageId}</Badge>
      </header>

      {error !== null ? (
        <ErrorState error={error} title="消息片段加载失败" onRetry={() => void load()} />
      ) : parts === null ? (
        <LoadingCard label="正在加载消息片段…" />
      ) : parts.length === 0 ? (
        <EmptyState
          title="该消息暂无消息片段"
          description="片段在消息流式生成时逐条写入；纯快照消息可能没有历史片段。"
        />
      ) : (
        <ol className="space-y-1.5">
          {parts.map((part) => (
            <li
              key={part.seq}
              data-testid={`message-part-${part.seq}`}
              className="rounded-md border border-border px-2 py-1.5"
            >
              <div className="flex flex-wrap items-center gap-1.5 text-[10px] text-muted-foreground">
                <span data-testid={`message-part-type-${part.seq}`}>
                  <Badge tone="info">{part.part_type}</Badge>
                </span>
                <span>seq {part.seq}</span>
                <span>{formatDateTime(part.created_at)}</span>
              </div>
              <div className="mt-1">{renderPart(part, versionId)}</div>
            </li>
          ))}
        </ol>
      )}
    </section>
  )
}
