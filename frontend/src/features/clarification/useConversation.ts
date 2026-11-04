"use client"

import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react"

import type { JobStatus } from "@/features/conversation/types"
import type { ApiPage } from "@/lib/api-client"

import { listMessages, postMessage } from "./api"
import type { ConversationMessage, NewMessagePart } from "./types"

/** Matches the messages endpoint default: a full page implies another page. */
const MESSAGES_PAGE_SIZE = 100

/**
 * The messages endpoint returns one cursor page. A bare item list (older caller
 * or test double) is still accepted and promoted to a page; the page-size
 * heuristic only applies when the server envelope is absent.
 */
function toMessagePage(
  value: ApiPage<ConversationMessage> | ConversationMessage[],
): ApiPage<ConversationMessage> {
  if (!Array.isArray(value)) return value
  const last = value.length > 0 ? value[value.length - 1].id : ""
  const hasMore = value.length >= MESSAGES_PAGE_SIZE
  return { items: value, nextCursor: hasMore ? last : "", hasMore }
}

function newestMessageId(messages: readonly ConversationMessage[]): string {
  return messages.length > 0 ? messages[messages.length - 1].id : ""
}

/* --------------------------- streamed part merge ---------------------------- */

function seqOf(part: unknown): number | null {
  if (typeof part !== "object" || part === null) return null
  const seq = (part as { seq?: unknown }).seq
  return typeof seq === "number" && Number.isInteger(seq) && seq > 0 ? seq : null
}

/**
 * Merges streamed message parts by their 1-based `seq` (OpenAPI
 * `MessagePartView.seq`). A later payload for the same seq wins and the result
 * is ordered by seq, so a repeated SSE frame never renders the same part twice.
 *
 * Without any seq (the `conversation.message` full snapshot, P1 shape) the
 * newer snapshot replaces the older one; this also makes a duplicate frame
 * idempotent instead of appending a second copy.
 */
export function mergePartsBySeq<T>(existing: readonly T[], incoming: readonly T[]): T[] {
  const incomingSequenced = incoming.some((part) => seqOf(part) !== null)
  if (!incomingSequenced) {
    return incoming.length >= existing.length ? [...incoming] : [...existing]
  }

  const bySeq = new Map<number, T>()
  for (const part of existing) {
    const seq = seqOf(part)
    if (seq !== null) bySeq.set(seq, part)
  }
  for (const part of incoming) {
    const seq = seqOf(part)
    if (seq !== null) bySeq.set(seq, part)
  }
  const sequenced = [...bySeq.entries()]
    .sort(([leftSeq], [rightSeq]) => leftSeq - rightSeq)
    .map(([, part]) => part)
  const unsequenced = incoming.filter((part) => seqOf(part) === null)
  return [...sequenced, ...unsequenced]
}

/* ------------------------------ stream resets ------------------------------ */

type StreamResetListener = (versionId: string, data: unknown) => void

const streamResetListeners = new Set<StreamResetListener>()

/** Notifies snapshot owners that the cursor fell out of the event window. */
export function subscribeStreamReset(listener: StreamResetListener): () => void {
  streamResetListeners.add(listener)
  return () => {
    streamResetListeners.delete(listener)
  }
}

/** Called by the single SSE owner (`useVersionEvents`) on `stream.reset`. */
export function publishStreamReset(versionId: string, data: unknown): void {
  for (const listener of streamResetListeners) listener(versionId, data)
}

/* ------------------------------ job progress ------------------------------- */

/** The live slice of a job card that version-channel job events mutate. */
export type JobCardUpdate = {
  status?: JobStatus
  current?: number
  total?: number
}

/** Version-channel event types that mutate the rendered job card. */
export type JobEventType = "job.progress" | "job.finished" | "job.canceled" | "job.requeued"

const JOB_STATUSES: ReadonlySet<string> = new Set<JobStatus>([
  "open",
  "claimed",
  "running",
  "waiting_human",
  "done",
  "failed",
  "cancelled",
])

/** `done/failed/cancelled` are terminal: a card never regresses out of them. */
const TERMINAL_JOB_STATUSES: ReadonlySet<JobStatus> = new Set<JobStatus>([
  "done",
  "failed",
  "cancelled",
])

function isJobStatus(value: unknown): value is JobStatus {
  return typeof value === "string" && JOB_STATUSES.has(value)
}

function jobKey(versionId: string, jobId: string): string {
  return `${versionId}:${jobId}`
}

// Job events arrive on the version stream while the card lives inside a
// conversation message. This projection lets the stream owner publish without
// threading a prop through every workspace layer.
const jobUpdates = new Map<string, JobCardUpdate>()
const jobUpdateListeners = new Set<() => void>()

/**
 * Sticky-terminal merge for one job event.
 *
 * A terminal card (`done/failed/cancelled`) accepts only an idempotent terminal
 * repeat or an explicit revival: `job.requeued`, or an `open` status (which the
 * worker also emits after an automatic retry). Everything else is a stale
 * in-flight update from the fenced old owner — a coalesced `running` snapshot,
 * or a `failed` progress event that lost the race to a cancel/done — and is
 * dropped with its counters so the card cannot roll back. After a revival the
 * next owner's `running`/`finished` events apply normally.
 */
function mergeJobUpdate(
  current: JobCardUpdate | undefined,
  update: JobCardUpdate,
  eventType: JobEventType,
): JobCardUpdate | null {
  const currentStatus = current?.status
  if (currentStatus !== undefined && TERMINAL_JOB_STATUSES.has(currentStatus)) {
    const revivedByControlEvent =
      eventType === "job.requeued" &&
      update.status !== undefined &&
      !TERMINAL_JOB_STATUSES.has(update.status)
    const revivedByOpenStatus = update.status === "open"
    const idempotentTerminal = update.status !== undefined && update.status === currentStatus
    if (!revivedByControlEvent && !revivedByOpenStatus && !idempotentTerminal) return null
  }
  return { ...(current ?? {}), ...update }
}

/**
 * Parses one validated job event payload and merges it into the live job card
 * projection. `job.canceled`/`job.requeued` share the same merge path as
 * `job.progress`/`job.finished`; malformed payloads are ignored.
 */
export function publishJobEvent(
  versionId: string,
  payload: Record<string, unknown>,
  eventType: JobEventType,
): void {
  const rawId = payload.job_id
  const jobId =
    typeof rawId === "string"
      ? rawId
      : typeof rawId === "number" && Number.isInteger(rawId) && rawId > 0
        ? String(rawId)
        : null
  if (jobId === null || !/^[0-9]+$/.test(jobId)) return

  const update: JobCardUpdate = {}
  if (isJobStatus(payload.status)) update.status = payload.status
  if (
    typeof payload.current === "number" &&
    Number.isInteger(payload.current) &&
    payload.current >= 0
  ) {
    update.current = payload.current
  }
  if (typeof payload.total === "number" && Number.isInteger(payload.total) && payload.total >= 0) {
    update.total = payload.total
  }
  if (Object.keys(update).length === 0) return

  const key = jobKey(versionId, jobId)
  const next = mergeJobUpdate(jobUpdates.get(key), update, eventType)
  if (next === null) return
  jobUpdates.set(key, next)
  for (const listener of jobUpdateListeners) listener()
}

/**
 * Subscribes one rendered job card to the streamed job updates of its version.
 * Returns `null` until a job event (progress/finished/canceled/requeued) for
 * that job lands.
 */
export function useJobCardUpdate(versionId: string, jobId: string): JobCardUpdate | null {
  const subscribe = useCallback((listener: () => void): (() => void) => {
    jobUpdateListeners.add(listener)
    return () => {
      jobUpdateListeners.delete(listener)
    }
  }, [])

  return useSyncExternalStore(
    subscribe,
    () => jobUpdates.get(jobKey(versionId, jobId)) ?? null,
    () => null,
  )
}

function compareMessageIds(left: ConversationMessage, right: ConversationMessage): number {
  return compareIds(left.id, right.id)
}

function compareIds(left: string, right: string): number {
  if (left === right) return 0
  try {
    const leftId = BigInt(left)
    const rightId = BigInt(right)
    return leftId < rightId ? -1 : 1
  } catch {
    return left.localeCompare(right)
  }
}

function oldestMessageId(messages: readonly ConversationMessage[]): string {
  let oldest = ""
  for (const message of messages) {
    if (oldest === "" || compareIds(message.id, oldest) < 0) oldest = message.id
  }
  return oldest
}

function sortMessages(messages: ConversationMessage[]): ConversationMessage[] {
  return [...messages].sort(compareMessageIds)
}

/**
 * Which way the thread's history pagination runs.
 *
 * API-8 returns the newest page by default and older pages through `before`.
 * The P1 endpoint returned the oldest page first and newer pages through
 * `after`; the envelope of that legacy first page points its cursor at the
 * newest loaded id. Detecting that shape keeps the older P1 callers (and their
 * tests) working without a second endpoint, while the default remains the
 * spec's older-history direction.
 */
type HistoryDirection = "older" | "newer"

function resolveHistoryDirection(page: {
  items: readonly ConversationMessage[]
  nextCursor: string
  hasMore: boolean
}): HistoryDirection {
  const newest = newestMessageId(page.items)
  return page.nextCursor !== "" && page.nextCursor === newest ? "newer" : "older"
}

/**
 * Merges one SSE batch into the thread. A message that is already loaded keeps
 * its identity but receives the newest parts, merged by `seq` (a full snapshot
 * without seq replaces the older one). This makes duplicate or incremental
 * `conversation.message` frames idempotent instead of rendering twice.
 */
function mergeConversationMessages(
  current: ConversationMessage[],
  incoming: ConversationMessage[],
): ConversationMessage[] {
  if (incoming.length === 0) return current
  const byId = new Map<string, ConversationMessage>()
  for (const message of current) byId.set(message.id, message)
  for (const message of incoming) {
    const existing = byId.get(message.id)
    if (existing === undefined) {
      byId.set(message.id, message)
      continue
    }
    byId.set(message.id, {
      ...existing,
      ...message,
      parts: mergePartsBySeq(existing.parts, message.parts),
    })
  }
  return sortMessages([...byId.values()])
}

function createClientMessageId(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID()
  }
  return `client_${Date.now()}_${Math.random().toString(36).slice(2, 10)}`
}

export type UseConversationOptions = {
  versionId: string
  conversationId: string | null
  /** Messages pushed by the version SSE channel; merged by id. */
  liveMessages?: ConversationMessage[]
}

export type UseConversationResult = {
  messages: ConversationMessage[]
  /** True while the first snapshot of the current conversation is loading. */
  loading: boolean
  /** True while an already loaded snapshot is being refetched (thread stays). */
  refreshing: boolean
  error: unknown
  sending: boolean
  sendError: unknown
  /** True when older history can still be loaded through the `before` cursor. */
  hasMore: boolean
  loadingMore: boolean
  /** Error from the last `loadMore`; inline only, the thread stays mounted. */
  loadMoreError: unknown
  reload: () => void
  /** Loads the next older history page from the stored server cursor. */
  loadMore: () => Promise<void>
  /** Returns true when the API accepted the message (draft can be cleared). */
  send: (parts: NewMessagePart[]) => Promise<boolean>
}

/**
 * Owns one conversation thread: initial fetch, live SSE merge, send with a
 * client idempotency key, and reload for retries. The SSE connection itself is
 * owned by the workspace (`useVersionEvents`), so this hook never reconnects.
 */
export function useConversation({
  versionId,
  conversationId,
  liveMessages = [],
}: UseConversationOptions): UseConversationResult {
  const [messages, setMessages] = useState<ConversationMessage[]>([])
  const [loading, setLoading] = useState(conversationId !== null)
  const [refreshing, setRefreshing] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [hasMore, setHasMore] = useState(false)
  const [loadingMore, setLoadingMore] = useState(false)
  const [loadMoreError, setLoadMoreError] = useState<unknown>(null)
  const [reloadToken, setReloadToken] = useState(0)
  const [sending, setSending] = useState(false)
  const [sendError, setSendError] = useState<unknown>(null)

  // The history cursor belongs to the loaded snapshot only: live SSE messages
  // and optimistic sends never move it, so a higher live id cannot skip
  // unloaded history.
  const historyCursorRef = useRef("")
  // Direction of the loaded snapshot's pagination (`before` for API-8's latest
  // page, `after` only for the legacy P1 oldest-first envelope).
  const historyDirectionRef = useRef<HistoryDirection>("older")
  // Oldest rendered message id: guards the cursor against a backend/response
  // that returns the same page again, which would otherwise loop forever.
  const oldestLoadedIdRef = useRef("")
  // Bumped by every snapshot fetch and by unmount/switch, so a late `loadMore`
  // response can be recognized as stale and ignored.
  const generationRef = useRef(0)
  // Identity (`versionId:conversationId`) whose snapshot is currently rendered.
  // A fetch for this identity is a refresh of a loaded thread, not a new load.
  const loadedIdentityRef = useRef("")
  const identity = conversationId === null ? "" : `${versionId}:${conversationId}`
  const identityRef = useRef(identity)
  identityRef.current = identity

  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadToken is a manual refresh trigger
  useEffect(() => {
    generationRef.current += 1
    const generation = generationRef.current
    let cancelled = false
    const isCurrent = () => !cancelled && generationRef.current === generation
    const invalidate = () => {
      generationRef.current += 1
    }
    historyCursorRef.current = ""

    if (conversationId === null) {
      loadedIdentityRef.current = ""
      historyDirectionRef.current = "older"
      oldestLoadedIdRef.current = ""
      setMessages([])
      setLoading(false)
      setRefreshing(false)
      setError(null)
      setHasMore(false)
      setLoadingMore(false)
      setLoadMoreError(null)
      return () => {
        cancelled = true
        invalidate()
      }
    }

    const fetchIdentity = `${versionId}:${conversationId}`
    // Refetching the already rendered conversation keeps the thread and the
    // Composer (with its unsent draft) mounted: only the initial load of a
    // conversation replaces the panel with the loading card.
    const isRefresh = loadedIdentityRef.current === fetchIdentity
    if (isRefresh) {
      setRefreshing(true)
    } else {
      setLoading(true)
      setRefreshing(false)
    }
    setError(null)
    setHasMore(false)
    setLoadingMore(false)
    setLoadMoreError(null)
    void listMessages(versionId, conversationId).then(
      (next) => {
        if (!isCurrent()) return
        const page = toMessagePage(next)
        const sorted = sortMessages(page.items)
        const direction = resolveHistoryDirection(page)
        const newest = newestMessageId(sorted)
        const oldest = oldestMessageId(sorted)
        historyDirectionRef.current = direction
        oldestLoadedIdRef.current = oldest
        setMessages(sorted)
        if (direction === "older") {
          // API-8 default page: the cursor (when the server sends one) points
          // before the oldest loaded message. A full page also implies more
          // history even when the server omits the envelope.
          historyCursorRef.current = page.nextCursor || oldest
          setHasMore(page.hasMore || page.items.length >= MESSAGES_PAGE_SIZE)
        } else {
          historyCursorRef.current = page.nextCursor || newest
          setHasMore(page.hasMore)
        }
        loadedIdentityRef.current = fetchIdentity
        setRefreshing(false)
        setLoading(false)
      },
      (cause: unknown) => {
        if (!isCurrent()) return
        setError(cause)
        setRefreshing(false)
        setLoading(false)
      },
    )

    return () => {
      cancelled = true
      invalidate()
    }
  }, [versionId, conversationId, reloadToken])

  useEffect(() => {
    if (conversationId === null || liveMessages.length === 0) return
    const relevant = liveMessages.filter((message) => message.conversation_id === conversationId)
    if (relevant.length === 0) return
    setMessages((current) => mergeConversationMessages(current, relevant))
  }, [liveMessages, conversationId])

  // `stream.reset` means the cursor fell out of the event window: refetch the
  // conversation snapshot through the existing HTTP path (no second SSE).
  useEffect(
    () =>
      subscribeStreamReset((resetVersionId) => {
        if (resetVersionId === versionId) setReloadToken((token) => token + 1)
      }),
    [versionId],
  )

  const loadMore = useCallback(async (): Promise<void> => {
    if (conversationId === null || loadingMore || !hasMore) return
    const cursor = historyCursorRef.current
    if (cursor === "") return
    const identityAtStart = `${versionId}:${conversationId}`
    const generationAtStart = generationRef.current
    const isCurrent = () =>
      identityRef.current === identityAtStart && generationRef.current === generationAtStart

    setLoadingMore(true)
    setLoadMoreError(null)
    try {
      const direction = historyDirectionRef.current
      const page = toMessagePage(
        await listMessages(
          versionId,
          conversationId,
          direction === "older"
            ? { before: cursor, limit: MESSAGES_PAGE_SIZE }
            : { after: cursor, limit: MESSAGES_PAGE_SIZE },
        ),
      )
      if (!isCurrent()) return

      if (direction === "older") {
        // Only truly older rows may be prepended; a backend that ignores the
        // cursor would otherwise re-merge the same page and keep `hasMore` on.
        const olderItems = sortMessages(page.items).filter(
          (message) => compareIds(message.id, cursor) < 0,
        )
        if (olderItems.length === 0) {
          setHasMore(false)
          return
        }
        setMessages((current) => mergeConversationMessages(current, olderItems))
        const oldest = oldestMessageId(olderItems)
        if (compareIds(oldest, oldestLoadedIdRef.current) < 0) {
          oldestLoadedIdRef.current = oldest
        }
        historyCursorRef.current = page.nextCursor || oldest
        setHasMore(page.hasMore || olderItems.length >= MESSAGES_PAGE_SIZE)
        return
      }

      setMessages((current) => mergeConversationMessages(current, page.items))
      const pageCursor = page.nextCursor || newestMessageId(sortMessages(page.items))
      historyCursorRef.current = pageCursor || cursor
      setHasMore(page.hasMore && pageCursor !== "")
    } catch (cause) {
      if (!isCurrent()) return
      setLoadMoreError(cause)
    } finally {
      if (isCurrent()) setLoadingMore(false)
    }
  }, [versionId, conversationId, loadingMore, hasMore])

  const reload = useCallback(() => {
    setReloadToken((token) => token + 1)
  }, [])

  const send = useCallback(
    async (parts: NewMessagePart[]): Promise<boolean> => {
      if (conversationId === null) return false
      setSending(true)
      setSendError(null)
      try {
        const message = await postMessage(versionId, conversationId, {
          client_message_id: createClientMessageId(),
          parts,
        })
        setMessages((current) =>
          current.some((existing) => existing.id === message.id)
            ? current
            : sortMessages([...current, message]),
        )
        return true
      } catch (cause) {
        setSendError(cause)
        return false
      } finally {
        setSending(false)
      }
    },
    [versionId, conversationId],
  )

  return {
    messages,
    loading,
    refreshing,
    error,
    sending,
    sendError,
    hasMore,
    loadingMore,
    loadMoreError,
    reload,
    loadMore,
    send,
  }
}
