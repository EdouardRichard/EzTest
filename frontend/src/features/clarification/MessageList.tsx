"use client"

import { Paperclip } from "lucide-react"
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react"

import { Button } from "@/components/ui/Button"
import { cn } from "@/components/ui/cn"
import { EmptyState } from "@/components/ui/EmptyState"
import { Spinner } from "@/components/ui/Spinner"
import { CaseCard } from "@/features/conversation/CaseCard"
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
  /** Opens a `case_card` reference; the design workspace selects the case. */
  onCaseSelect?: (caseId: string) => void
  /** History pagination: true while older history can still be loaded. */
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

/** Distance from the bottom that still counts as "following the latest". */
const BOTTOM_THRESHOLD_PX = 80
/** Distance from the top that triggers loading the older history page. */
const LOAD_OLDER_THRESHOLD_PX = 80

// The thread is pre-rendered on the server for the first paint (Next client
// component), where layout effects cannot run.
const useIsomorphicLayoutEffect = typeof window === "undefined" ? useEffect : useLayoutEffect

type SavedScrollPosition = {
  scrollTop: number
  autoFollow: boolean
}

/**
 * Manual scroll positions must survive a conversation switch: the workspace
 * unmounts the thread while the next snapshot loads, so React state alone is
 * not enough. Only paused positions are recorded; a reader at the bottom should
 * follow the next message again.
 */
const savedScrollPositions = new Map<string, SavedScrollPosition>()

function compareMessageIds(left: string, right: string): number {
  if (left === right) return 0
  try {
    const leftId = BigInt(left)
    const rightId = BigInt(right)
    return leftId < rightId ? -1 : 1
  } catch {
    return left.localeCompare(right)
  }
}

function newestMessageId(messages: readonly ConversationMessage[]): string {
  let newest = ""
  for (const message of messages) {
    if (newest === "" || compareMessageIds(message.id, newest) > 0) newest = message.id
  }
  return newest
}

function oldestMessageId(messages: readonly ConversationMessage[]): string {
  let oldest = ""
  for (const message of messages) {
    if (oldest === "" || compareMessageIds(message.id, oldest) < 0) oldest = message.id
  }
  return oldest
}

function isNewerMessageId(candidate: string, baseline: string): boolean {
  return baseline === "" ? candidate !== "" : compareMessageIds(candidate, baseline) > 0
}

function distanceFromBottom(element: HTMLElement): number {
  return Math.max(0, element.scrollHeight - element.scrollTop - element.clientHeight)
}

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
  onCaseSelect,
  onErrorAction,
}: {
  versionId: string
  messageId: string
  part: RenderablePart
  onRequirementSelect: (requirementId: string) => void
  onRequirementChanged?: (requirement: RequirementDetail) => void
  onCaseSelect?: (caseId: string) => void
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
      return <CaseCard part={part} onOpen={onCaseSelect} versionId={versionId} />
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
  onCaseSelect,
  onErrorAction,
}: {
  versionId: string
  message: ConversationMessage
  onRequirementSelect: (requirementId: string) => void
  onRequirementChanged?: (requirement: RequirementDetail) => void
  onCaseSelect?: (caseId: string) => void
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
            onCaseSelect={onCaseSelect}
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

/**
 * Scrollable thread plus the AI activity bubble.
 *
 * The thread is stick-to-bottom by default. A reader who scrolls more than 80px
 * away from the bottom is never pulled back; the "回到最新 (N)" affordance is
 * the only way to resume following, and it also reports how many messages
 * arrived while reading history. Older pages are requested from the top with
 * scrollTop compensation so prepending them does not move what is on screen.
 */
export function MessageList({
  versionId,
  messages,
  activity,
  onRequirementSelect,
  onRequirementChanged,
  onCaseSelect,
  hasMore = false,
  loadingMore = false,
  loadMoreError,
  onLoadMore,
  onRetryLoadMore,
  onErrorAction,
}: MessageListProps) {
  const containerRef = useRef<HTMLDivElement | null>(null)
  const autoFollowRef = useRef(true)
  const [autoFollow, setAutoFollowState] = useState(true)
  const [unreadCount, setUnreadCount] = useState(0)
  const frameRef = useRef<{ kind: "raf" | "timeout"; id: number } | null>(null)
  const conversationKeyRef = useRef<string | null>(null)
  const previousIdsRef = useRef<Set<string>>(new Set())
  const previousNewestIdRef = useRef("")
  const previousOldestIdRef = useRef("")
  const previousScrollHeightRef = useRef(0)
  const loadOlderRequestedRef = useRef(false)
  const oldestMessageIdRef = useRef("")

  const conversationKey = messages[0]?.conversation_id ?? ""
  const oldestId = oldestMessageId(messages)

  const scrollToBottom = useCallback(() => {
    const element = containerRef.current
    if (element === null) return
    if (typeof element.scrollTo === "function") {
      element.scrollTo({ top: element.scrollHeight, behavior: "auto" })
    } else {
      element.scrollTop = element.scrollHeight
    }
  }, [])

  /**
   * Follow-up scrolls are merged into one animation frame: a token-level part
   * update can re-render dozens of times per second without laying out the
   * thread each time.
   */
  const scheduleScrollToBottom = useCallback(() => {
    if (frameRef.current !== null) return
    const run = () => {
      frameRef.current = null
      if (autoFollowRef.current) scrollToBottom()
    }
    if (typeof requestAnimationFrame === "function") {
      frameRef.current = { kind: "raf", id: requestAnimationFrame(run) }
    } else {
      frameRef.current = { kind: "timeout", id: window.setTimeout(run, 0) }
    }
  }, [scrollToBottom])

  useEffect(
    () => () => {
      const frame = frameRef.current
      if (frame === null) return
      if (frame.kind === "raf" && typeof cancelAnimationFrame === "function") {
        cancelAnimationFrame(frame.id)
      } else {
        window.clearTimeout(frame.id)
      }
    },
    [],
  )

  const saveScrollPosition = useCallback((element: HTMLElement, following: boolean) => {
    const key = conversationKeyRef.current
    if (key === null || key === "") return
    if (following) {
      savedScrollPositions.delete(key)
      return
    }
    savedScrollPositions.set(key, { scrollTop: element.scrollTop, autoFollow: false })
  }, [])

  useIsomorphicLayoutEffect(() => {
    const element = containerRef.current
    if (element === null) return

    const isConversationChange = conversationKeyRef.current !== conversationKey
    const nextIds = new Set(messages.map((message) => message.id))
    const nextNewestId = newestMessageId(messages)
    const nextOldestId = oldestMessageId(messages)

    if (isConversationChange) {
      conversationKeyRef.current = conversationKey
      previousIdsRef.current = nextIds
      previousNewestIdRef.current = nextNewestId
      previousOldestIdRef.current = nextOldestId

      const saved = conversationKey === "" ? undefined : savedScrollPositions.get(conversationKey)
      if (saved !== undefined && !saved.autoFollow) {
        autoFollowRef.current = false
        setAutoFollowState(false)
        setUnreadCount(0)
        element.scrollTop = saved.scrollTop
      } else {
        autoFollowRef.current = true
        setAutoFollowState(true)
        setUnreadCount(0)
        scrollToBottom()
      }
      previousScrollHeightRef.current = element.scrollHeight
      return
    }

    const prepended =
      previousIdsRef.current.size > 0 &&
      previousOldestIdRef.current !== "" &&
      nextOldestId !== previousOldestIdRef.current &&
      nextIds.has(previousOldestIdRef.current)
    const appended = messages.filter(
      (message) =>
        !previousIdsRef.current.has(message.id) &&
        isNewerMessageId(message.id, previousNewestIdRef.current),
    )
    const nextHeight = element.scrollHeight
    const previousHeight = previousScrollHeightRef.current

    previousIdsRef.current = nextIds
    previousNewestIdRef.current = nextNewestId === "" ? previousNewestIdRef.current : nextNewestId
    previousOldestIdRef.current = nextOldestId === "" ? previousOldestIdRef.current : nextOldestId

    if (prepended) {
      // Keep the current message under the reader's eyes: the content above it
      // grew, so the scroll offset must grow by the same amount.
      const heightDelta = nextHeight - previousHeight
      if (heightDelta > 0) element.scrollTop += heightDelta
      previousScrollHeightRef.current = nextHeight
      saveScrollPosition(element, autoFollowRef.current)
      return
    }

    previousScrollHeightRef.current = nextHeight
    if (autoFollowRef.current) {
      if (messages.length > 0) scheduleScrollToBottom()
      return
    }
    if (appended.length > 0) setUnreadCount((count) => count + appended.length)
  }, [conversationKey, messages, saveScrollPosition, scheduleScrollToBottom, scrollToBottom])

  // Activity text can change the thread height (the bubble appears/grows)
  // without changing the message list, so it gets its own merged follow-up.
  useIsomorphicLayoutEffect(() => {
    if (activity !== null && autoFollowRef.current) scheduleScrollToBottom()
  }, [activity, scheduleScrollToBottom])

  // A new (or prepended) first message means a completed older-page request; a
  // failed request flips loadingMore back to false, allowing a retry on scroll.
  useEffect(() => {
    if (oldestMessageIdRef.current === oldestId) return
    oldestMessageIdRef.current = oldestId
    loadOlderRequestedRef.current = false
  }, [oldestId])
  useEffect(() => {
    if (!loadingMore) loadOlderRequestedRef.current = false
  }, [loadingMore])

  const handleReturnToLatest = useCallback(() => {
    autoFollowRef.current = true
    setAutoFollowState(true)
    setUnreadCount(0)
    const key = conversationKeyRef.current
    if (key !== null && key !== "") savedScrollPositions.delete(key)
    scrollToBottom()
  }, [scrollToBottom])

  const handleScroll = useCallback(() => {
    const element = containerRef.current
    if (element === null) return

    const following = distanceFromBottom(element) <= BOTTOM_THRESHOLD_PX
    if (following) {
      if (!autoFollowRef.current) {
        autoFollowRef.current = true
        setAutoFollowState(true)
        setUnreadCount(0)
      }
      saveScrollPosition(element, true)
    } else {
      if (autoFollowRef.current) {
        autoFollowRef.current = false
        setAutoFollowState(false)
      }
      saveScrollPosition(element, false)
    }

    if (
      element.scrollTop <= LOAD_OLDER_THRESHOLD_PX &&
      hasMore &&
      !loadingMore &&
      onLoadMore !== undefined &&
      !loadOlderRequestedRef.current
    ) {
      loadOlderRequestedRef.current = true
      onLoadMore()
    }
  }, [hasMore, loadingMore, onLoadMore, saveScrollPosition])

  const returnToLatestLabel = unreadCount > 0 ? `回到最新 (${unreadCount})` : "回到最新"

  return (
    <div className="relative flex min-h-0 flex-1 flex-col">
      <div
        ref={containerRef}
        data-testid="message-list"
        role="log"
        aria-live="polite"
        aria-label="对话消息"
        onScroll={handleScroll}
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
            onCaseSelect={onCaseSelect}
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
              <Button
                variant="secondary"
                size="sm"
                disabled={loadingMore}
                onClick={onRetryLoadMore}
              >
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

      {!autoFollow ? (
        <div className="pointer-events-none absolute inset-x-0 bottom-3 z-10 flex justify-center">
          <Button
            variant="secondary"
            size="sm"
            aria-label={returnToLatestLabel}
            className="pointer-events-auto"
            onClick={handleReturnToLatest}
          >
            {returnToLatestLabel}
          </Button>
        </div>
      ) : null}
    </div>
  )
}
