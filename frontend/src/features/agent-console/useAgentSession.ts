"use client"

import { useCallback, useEffect, useRef, useState } from "react"

import { ApiError, resolveApiUrl } from "@/lib/api-client"
import { getToken } from "@/lib/auth-storage"
import { connectSSE, type SSEEvent, type SSEStatus } from "@/lib/sse-client"

import {
  type ActivitiesPage,
  type AgentEvent,
  type AgentMessageDetail,
  type AgentSession,
  fetchActivities,
  getMessage,
} from "./agent-api"

/** Snapshot page size from the design (§11.3): the last 50 persisted activities. */
const SNAPSHOT_LIMIT = 50

type SnapshotResult = {
  items: AgentEvent[]
  cursor: number
  hasMore: boolean
}

/** The snapshot of a channel that has no persisted events yet (the API 404s). */
function emptySnapshot(): SnapshotResult {
  return { items: [], cursor: 0, hasMore: false }
}

export type AgentSessionState = {
  activities: AgentEvent[]
  status: SSEStatus
  loading: boolean
  error: string
  running: boolean
  hasMore: boolean
  loadEarlier: () => Promise<void>
  refresh: () => Promise<void>
  reconnect: () => void
  getDetail: (messageId: number) => Promise<AgentMessageDetail>
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

/** Reads `detail.kind`, the frozen V1 discriminator of an activity event. */
export function activityKind(event: AgentEvent): string {
  const kind = event.detail.kind
  return typeof kind === "string" ? kind : ""
}

/**
 * Validates one persisted/live event and keeps only the envelope fields the UI
 * consumes. Anything without an `agent.activity` type or a numeric seq is
 * dropped: the hook must never merge unrelated stream events into a transcript.
 */
function normalizeActivity(raw: unknown): AgentEvent | null {
  if (!isRecord(raw)) return null
  if (raw.type !== "agent.activity") return null
  if (typeof raw.seq !== "number" || !Number.isFinite(raw.seq)) return null

  const sessionId =
    typeof raw.session_id === "string"
      ? raw.session_id
      : typeof raw.session_id === "number"
        ? String(raw.session_id)
        : null

  return {
    seq: raw.seq,
    channel: typeof raw.channel === "string" ? raw.channel : "",
    type: "agent.activity",
    ts: typeof raw.ts === "string" ? raw.ts : "",
    summary: typeof raw.summary === "string" ? raw.summary : "",
    agent_role: typeof raw.agent_role === "string" ? raw.agent_role : null,
    session_id: sessionId,
    detail: isRecord(raw.detail) ? raw.detail : {},
  }
}

function normalizeActivities(items: unknown): AgentEvent[] {
  if (!Array.isArray(items)) return []
  const activities: AgentEvent[] = []
  for (const item of items) {
    const activity = normalizeActivity(item)
    if (activity) activities.push(activity)
  }
  return mergeActivities([], activities)
}

/** Parses one SSE frame into an activity, ignoring malformed payloads. */
function parseActivity(event: SSEEvent): AgentEvent | null {
  let payload: unknown
  try {
    payload = JSON.parse(event.data)
  } catch {
    return null
  }
  return normalizeActivity(payload)
}

/** Dedupes by seq (latest wins) and returns a seq-ascending list. */
function mergeActivities(previous: AgentEvent[], incoming: AgentEvent[]): AgentEvent[] {
  const bySeq = new Map<number, AgentEvent>()
  for (const event of previous) bySeq.set(event.seq, event)
  for (const event of incoming) bySeq.set(event.seq, event)
  return [...bySeq.values()].sort((left, right) => left.seq - right.seq)
}

/** Whether the newest run lifecycle event says a run is still active. */
function runningFromActivities(items: AgentEvent[]): boolean | undefined {
  let running: boolean | undefined
  for (const event of items) {
    const kind = activityKind(event)
    if (kind === "run_started") running = true
    else if (kind === "run_finished") running = false
  }
  return running
}

function describeError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 401) return "登录已失效，请重新登录。"
    if (error.status === 403) return "仅管理员可用"
    if (error.status === 0) return "无法连接 API 服务，请确认后端已启动。"
    return error.message
  }
  return error instanceof Error ? error.message : "未知错误"
}

/**
 * Loads one session's persisted activity snapshot, then attaches to its live
 * SSE channel with `lastEventId=snapshot_cursor`. Activities are deduped and
 * sorted by seq; `run_started`/`run_finished` drive `running`, and a finished
 * run triggers a snapshot refresh so a missed event cannot leave the view
 * stale. A snapshot 404 means the channel has no persisted events yet, which is
 * the normal state of a new session. The stream is stopped on session change
 * and unmount; `reconnect` re-opens it after a `closed` status. `generationRef`
 * fences every callback so a stopped session can never write into a new one.
 */
export function useAgentSession(
  sessionId: number | null,
  session: AgentSession | null,
): AgentSessionState {
  const [activities, setActivities] = useState<AgentEvent[]>([])
  const [status, setStatus] = useState<SSEStatus>("closed")
  const [loading, setLoading] = useState(sessionId !== null)
  const [error, setError] = useState("")
  const [running, setRunning] = useState(session?.running ?? false)
  const [hasMore, setHasMore] = useState(false)

  const sessionIdRef = useRef<number | null>(sessionId)
  const activitiesRef = useRef<AgentEvent[]>(activities)
  const stopRef = useRef<(() => void) | null>(null)
  const generationRef = useRef(0)
  const detailsRef = useRef(new Map<number, AgentMessageDetail>())

  // Mirror the parent's `running` flag on every render. The stream effect only
  // reads it when the session identity changes: a later parent refresh that
  // flips `session.running` must not tear down the live stream.
  const latestSessionRunningRef = useRef(session?.running ?? false)
  useEffect(() => {
    latestSessionRunningRef.current = session?.running ?? false
  })

  useEffect(() => {
    activitiesRef.current = activities
  }, [activities])

  const loadSnapshot = useCallback(
    async (id: number, generation: number, mode: "merge" | "replace"): Promise<SnapshotResult> => {
      let page: ActivitiesPage
      try {
        page = await fetchActivities(id, { limit: SNAPSHOT_LIMIT })
      } catch (caught) {
        // The snapshot endpoint 404s while a channel still has no events. That
        // is the empty state of a brand-new session, not a fatal error.
        if (caught instanceof ApiError && caught.status === 404) return emptySnapshot()
        throw caught
      }
      const items = normalizeActivities(page.items)
      const result: SnapshotResult = {
        items,
        cursor: typeof page.snapshot_cursor === "number" ? page.snapshot_cursor : 0,
        hasMore: Boolean(page.has_more),
      }
      // A session change / reconnect while the request was in flight must not
      // write into the new connection's state.
      if (sessionIdRef.current !== id || generationRef.current !== generation) return result
      setHasMore(result.hasMore)
      setActivities((previous) => (mode === "replace" ? items : mergeActivities(previous, items)))
      return result
    },
    [],
  )

  /**
   * Reconciles the snapshot: merges/replaces activities, derives `running`, and
   * clears a previous error because this fetch succeeded.
   */
  const syncSnapshot = useCallback(
    async (id: number, generation: number, mode: "merge" | "replace"): Promise<void> => {
      try {
        const snapshot = await loadSnapshot(id, generation, mode)
        if (sessionIdRef.current !== id || generationRef.current !== generation) return
        setError("")
        const nextRunning = runningFromActivities(snapshot.items)
        if (nextRunning !== undefined) setRunning(nextRunning)
      } catch (caught) {
        if (sessionIdRef.current !== id || generationRef.current !== generation) return
        setError(describeError(caught))
      }
    },
    [loadSnapshot],
  )

  const connectSession = useCallback(
    async (id: number, token: string, mode: "merge" | "replace"): Promise<void> => {
      const generation = generationRef.current
      let snapshot = emptySnapshot()
      let snapshotFailed = false
      try {
        snapshot = await loadSnapshot(id, generation, mode)
      } catch (caught) {
        if (sessionIdRef.current !== id || generationRef.current !== generation) return
        // A failed snapshot must not block live updates: surface the error but
        // open the stream anyway, so the running run can still resolve. The
        // retry action re-fetches the snapshot.
        snapshotFailed = true
        setError(describeError(caught))
      }
      if (sessionIdRef.current !== id || generationRef.current !== generation) return
      setLoading(false)
      if (!snapshotFailed) {
        setError("")
        const nextRunning = runningFromActivities(snapshot.items)
        if (nextRunning !== undefined) setRunning(nextRunning)
      }

      stopRef.current = connectSSE({
        url: resolveApiUrl(`/api/stream?channel=${encodeURIComponent(`agent:${id}`)}`),
        token,
        lastEventId: snapshot.cursor,
        onEvent: (event) => {
          if (generationRef.current !== generation) return
          const activity = parseActivity(event)
          if (!activity) return
          setActivities((previous) => mergeActivities(previous, [activity]))
          const kind = activityKind(activity)
          if (kind === "run_started") {
            setRunning(true)
          } else if (kind === "run_finished") {
            setRunning(false)
            // Reconcile with the persisted snapshot in case an event was missed.
            void syncSnapshot(id, generation, "merge")
          }
        },
        onStatus: (next) => {
          if (generationRef.current === generation) setStatus(next)
        },
        onReset: () => {
          if (generationRef.current !== generation) return
          setActivities([])
          setHasMore(false)
          void syncSnapshot(id, generation, "replace")
        },
      })
      // The session may have changed while connectSSE was being set up.
      if (sessionIdRef.current !== id || generationRef.current !== generation) {
        stopRef.current()
        stopRef.current = null
      }
    },
    [loadSnapshot, syncSnapshot],
  )

  useEffect(() => {
    sessionIdRef.current = sessionId
    generationRef.current += 1
    stopRef.current?.()
    stopRef.current = null
    detailsRef.current.clear()
    setError("")
    setActivities([])
    setHasMore(false)
    setRunning(latestSessionRunningRef.current)

    if (sessionId === null) {
      setLoading(false)
      setStatus("closed")
      return
    }

    const token = getToken()
    setLoading(true)
    setStatus("connecting")

    if (!token) {
      setLoading(false)
      setStatus("closed")
      setError("当前会话没有访问令牌，请重新登录。")
      return
    }

    void connectSession(sessionId, token, "replace")

    return () => {
      generationRef.current += 1
      stopRef.current?.()
      stopRef.current = null
    }
  }, [sessionId, connectSession])

  /**
   * Manual recovery action: tears down the current stream, refreshes the
   * snapshot (keeping the rendered transcript), and reconnects. Used by the
   * `已断开` reconnect button and the initial-load retry.
   */
  const reconnect = useCallback((): void => {
    const id = sessionIdRef.current
    if (id === null) return
    const token = getToken()
    if (!token) {
      setLoading(false)
      setStatus("closed")
      setError("当前会话没有访问令牌，请重新登录。")
      return
    }
    // Invalidate the old stream before stopping it so its late callbacks cannot
    // overwrite the new connection's state.
    generationRef.current += 1
    stopRef.current?.()
    stopRef.current = null
    setError("")
    setStatus("connecting")
    setLoading(activitiesRef.current.length === 0)
    void connectSession(id, token, "merge")
  }, [connectSession])

  const loadEarlier = useCallback(async (): Promise<void> => {
    const id = sessionIdRef.current
    const first = activitiesRef.current[0]
    if (id === null || first === undefined) return
    const generation = generationRef.current
    try {
      const page = await fetchActivities(id, { before_seq: first.seq, limit: SNAPSHOT_LIMIT })
      if (sessionIdRef.current !== id || generationRef.current !== generation) return
      const items = normalizeActivities(page.items)
      setActivities((previous) => mergeActivities(previous, items))
      setHasMore(Boolean(page.has_more))
      setError("")
    } catch (caught) {
      if (sessionIdRef.current === id && generationRef.current === generation) {
        setError(describeError(caught))
      }
    }
  }, [])

  const refresh = useCallback(async (): Promise<void> => {
    const id = sessionIdRef.current
    if (id === null) return
    await syncSnapshot(id, generationRef.current, "merge")
  }, [syncSnapshot])

  const getDetail = useCallback(async (messageId: number): Promise<AgentMessageDetail> => {
    const cached = detailsRef.current.get(messageId)
    if (cached) return cached
    const id = sessionIdRef.current
    if (id === null) throw new Error("没有选中的会话")
    const detail = await getMessage(id, messageId)
    detailsRef.current.set(messageId, detail)
    return detail
  }, [])

  return {
    activities,
    status,
    loading,
    error,
    running,
    hasMore,
    loadEarlier,
    refresh,
    reconnect,
    getDetail,
  }
}
