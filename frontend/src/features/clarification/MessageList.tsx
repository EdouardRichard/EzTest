"use client"

import { Paperclip } from "lucide-react"
import { useEffect, useMemo, useState } from "react"

import { Button } from "@/components/ui/Button"
import { cn } from "@/components/ui/cn"
import { EmptyState } from "@/components/ui/EmptyState"
import { Spinner } from "@/components/ui/Spinner"
import { ErrorPart as ErrorPartView } from "@/features/conversation/ErrorPart"
import { JobCard } from "@/features/conversation/JobCard"
import { ThinkingBlock } from "@/features/conversation/ThinkingBlock"
import { ToolCallBlock } from "@/features/conversation/ToolCallBlock"
import type {
  MessagePart as ConversationMessagePart,
  ErrorPart as ErrorPayload,
  JobCardPart,
} from "@/features/conversation/types"
import { formatDateTime } from "@/lib/format"

import { downloadContent } from "./api"
import { RequirementCard } from "./RequirementCard"
import { RichText } from "./RichText"
import { RollbackCard } from "./RollbackCard"
import type { ConversationMessage, ImagePart, MessagePart, RequirementDetail } from "./types"
import { mergePartsBySeq, useJobCardUpdate } from "./useConversation"

export type MessageListProps = {
  versionId: string
  messages: ConversationMessage[]
  /** Current `agent.activity` text; null when the AI is idle. */
  activity: string | null
  onRequirementSelect: (requirementId: string) => void
  onRequirementChanged?: (requirement: RequirementDetail) => void
  /** History pagination: true while the messages endpoint has a newer page. */
  hasMore?: boolean
  loadingMore?: boolean
  /** Non-fatal `loadMore` failure: rendered inline so the thread stays mounted. */
  loadMoreError?: unknown
  onLoadMore?: () => void
  onRetryLoadMore?: () => void
  /** Runs the action button of a `error` part. */
  onErrorAction?: (part: ErrorPayload) => void
}

/** The clarification thread can carry both the P1 and the 0.2.1 part unions. */
type RenderablePart = MessagePart | ConversationMessagePart

/**
 * Renders one image part through the authenticated content endpoint: the
 * content bytes are fetched with the bearer token (like every other API call)
 * and exposed as an object URL, which keeps `GET /api/content/{sha256}` auth
 * intact instead of a bare `<img src>`.
 */
function MessageImage({ part }: { part: ImagePart }) {
  const [url, setUrl] = useState<string | null>(null)
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    let cancelled = false
    let objectUrl: string | null = null
    setUrl(null)
    setFailed(false)
    void downloadContent(part.data_ref).then(
      (download) => {
        if (cancelled) return
        objectUrl = URL.createObjectURL(download.blob)
        setUrl(objectUrl)
      },
      () => {
        if (!cancelled) setFailed(true)
      },
    )
    return () => {
      cancelled = true
      if (objectUrl !== null) URL.revokeObjectURL(objectUrl)
    }
  }, [part.data_ref])

  if (failed) {
    return (
      <span data-testid="message-image-error" className="text-[11px] text-destructive">
        图片加载失败
      </span>
    )
  }
  if (url === null) {
    return (
      <span data-testid="message-image-loading" className="text-[11px] text-muted-foreground">
        图片加载中…
      </span>
    )
  }
  // The source is an authenticated content blob object URL, not a static asset
  // that next/image can optimize.
  return (
    // biome-ignore lint/performance/noImgElement: blob URL from the authenticated content API
    <img
      data-testid="message-image"
      src={url}
      alt="消息图片"
      width={part.width}
      height={part.height}
      className="max-h-64 w-auto rounded-lg border border-border object-contain"
    />
  )
}

function JobCardHost({ versionId, part }: { versionId: string; part: JobCardPart }) {
  const update = useJobCardUpdate(versionId, part.job_id)
  // Keep the merged part identity stable across unrelated parent renders: a new
  // object every render used to make JobCard think the job snapshot had moved
  // and discard the local cancel/retry result.
  const mergedPart = useMemo(
    () => (update === null ? part : { ...part, ...update }),
    [part, update],
  )
  return <JobCard versionId={versionId} part={mergedPart} />
}

function MessagePartView({
  versionId,
  messageId,
  part,
  onRequirementSelect,
  onRequirementChanged,
  onErrorAction,
}: {
  versionId: string
  messageId: string
  part: RenderablePart
  onRequirementSelect: (requirementId: string) => void
  onRequirementChanged?: (requirement: RequirementDetail) => void
  onErrorAction?: (part: ErrorPayload) => void
}) {
  switch (part.type) {
    case "text":
      return <RichText>{part.text}</RichText>
    case "image":
      return <MessageImage part={part} />
    case "attachment":
      return (
        <span
          data-testid="message-attachment"
          className="inline-flex items-center gap-1 rounded-full border border-border bg-muted px-2 py-0.5 text-[11px] text-muted-foreground"
        >
          <Paperclip className="size-3" aria-hidden="true" />
          {part.name}
        </span>
      )
    case "requirement_card":
      return <RequirementCard part={part} onSelect={onRequirementSelect} />
    case "rollback_proposal":
      return (
        <RollbackCard
          versionId={versionId}
          part={part}
          proposalMessageId={messageId}
          onRolledBack={onRequirementChanged}
        />
      )
    case "thinking":
      return <ThinkingBlock part={part} />
    case "tool_call":
      return <ToolCallBlock part={part} />
    case "job_card":
      return <JobCardHost versionId={versionId} part={part} />
    case "error":
      return <ErrorPartView part={part} onAction={onErrorAction} />
    case "case_card":
      // Case cards are wired by the design conversation task; the thread
      // intentionally ignores them until then instead of rendering a bare ID.
      return null
  }
}

function partKey(part: RenderablePart): string {
  switch (part.type) {
    case "text":
      return `text:${part.text.slice(0, 32)}`
    case "attachment":
      return `attachment:${part.sha256}`
    case "image":
      return `image:${part.data_ref}`
    case "requirement_card":
      return `requirement:${part.requirement_id}`
    case "rollback_proposal":
      return `rollback:${part.requirement_id}`
    case "thinking":
      return `thinking:${part.duration_ms}:${part.text.slice(0, 32)}`
    case "tool_call":
      return `tool:${part.call_id}`
    case "job_card":
      return `job:${part.job_id}`
    case "case_card":
      return `case:${part.case_id}`
    case "error":
      return `error:${part.code}:${part.message.slice(0, 32)}`
  }
}

function MessageBubble({
  versionId,
  message,
  onRequirementSelect,
  onRequirementChanged,
  onErrorAction,
}: {
  versionId: string
  message: ConversationMessage
  onRequirementSelect: (requirementId: string) => void
  onRequirementChanged?: (requirement: RequirementDetail) => void
  onErrorAction?: (part: ErrorPayload) => void
}) {
  const isUser = message.role === "user"
  const seenKeys = new Map<string, number>()
  // Streamed rows are de-duplicated by seq before they reach the renderer; a
  // repeated SSE frame therefore cannot render the same part twice.
  const parts = mergePartsBySeq<RenderablePart>([], message.parts).map((part) => {
    const base = `${message.id}:${partKey(part)}`
    const occurrence = seenKeys.get(base) ?? 0
    seenKeys.set(base, occurrence + 1)
    return { key: `${base}:${occurrence}`, part }
  })
  return (
    <article
      data-testid={`message-${message.role}`}
      className={cn("flex flex-col gap-1", isUser ? "items-end" : "items-start")}
    >
      <span className="text-[10px] font-medium text-muted-foreground">{isUser ? "你" : "AI"}</span>
      <div
        className={cn(
          "max-w-[85%] space-y-2 rounded-xl border px-3 py-2",
          isUser ? "border-primary/20 bg-primary/5" : "border-border bg-card",
        )}
      >
        {parts.map(({ key, part }) => (
          <MessagePartView
            key={key}
            versionId={versionId}
            messageId={message.id}
            part={part}
            onRequirementSelect={onRequirementSelect}
            onRequirementChanged={onRequirementChanged}
            onErrorAction={onErrorAction}
          />
        ))}
      </div>
      <span className="flex items-center gap-1.5 text-[10px] text-muted-foreground">
        {formatDateTime(message.created_at)}
        {/* 0.2.1: the assistant message shows the model that actually generated
            it (from the linked agent_run), never the currently selected one. */}
        {!isUser && (message.actual_model ?? "").trim() !== "" ? (
          <span
            data-testid="actual-model-badge"
            title="实际生成该消息的模型"
            className="rounded-full border border-border bg-muted px-1.5 py-0.5 font-medium"
          >
            {message.actual_model}
          </span>
        ) : null}
      </span>
    </article>
  )
}

/** Scrollable thread plus the AI activity bubble. */
export function MessageList({
  versionId,
  messages,
  activity,
  onRequirementSelect,
  onRequirementChanged,
  hasMore = false,
  loadingMore = false,
  loadMoreError,
  onLoadMore,
  onRetryLoadMore,
  onErrorAction,
}: MessageListProps) {
  return (
    <div
      data-testid="message-list"
      className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4 py-4"
    >
      {messages.length === 0 && activity === null ? (
        <EmptyState
          title="暂无消息"
          description="描述需求，AI 会先读取版本上下文，再产出需求草案。"
          className="border-none bg-transparent py-8"
        />
      ) : null}

      {messages.map((message) => (
        <MessageBubble
          key={message.id}
          versionId={versionId}
          message={message}
          onRequirementSelect={onRequirementSelect}
          onRequirementChanged={onRequirementChanged}
          onErrorAction={onErrorAction}
        />
      ))}

      {loadMoreError !== undefined && loadMoreError !== null ? (
        <div
          data-testid="load-more-error"
          role="alert"
          className="flex flex-wrap items-center justify-center gap-2"
        >
          <p className="text-[11px] text-destructive">加载更多消息失败。</p>
          {onRetryLoadMore !== undefined ? (
            <Button variant="secondary" size="sm" disabled={loadingMore} onClick={onRetryLoadMore}>
              重试加载
            </Button>
          ) : null}
        </div>
      ) : hasMore && onLoadMore !== undefined ? (
        <div className="flex justify-center">
          <Button variant="secondary" size="sm" disabled={loadingMore} onClick={onLoadMore}>
            {loadingMore ? "加载中…" : "加载更多消息"}
          </Button>
        </div>
      ) : null}

      {activity !== null ? (
        <div
          data-testid="agent-activity"
          className="flex items-center gap-2 rounded-lg border border-border bg-card px-3 py-2 text-[11px] text-muted-foreground"
        >
          <Spinner size="sm" label="AI 正在思考" />
          <span>{activity}</span>
        </div>
      ) : null}
    </div>
  )
}
