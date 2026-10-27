"use client"

import { useCallback, useEffect, useRef, useState } from "react"

import { ApiError, resolveApiUrl } from "@/lib/api-client"
import { getToken } from "@/lib/auth-storage"
import { connectSSE, type SSEEvent, type SSEStatus } from "@/lib/sse-client"

import {
  type AgentEvent,
  type AgentMessageDetail,
  type AgentSession,
  fetchActivities,
  getMessage,
} from "./agent-api"

/** Snapshot page size from the design (§11.3): the last 50 persisted activities. */
const SNAPSHOT_LIMIT = 50

export type AgentSessionState = {
  activities: AgentEvent[]
  status: SSEStatus
  loading: boolean
  error: string
  running: boolean
  hasMore: boolean
  loadEarlier: () => Promise<void>
  refresh: () => Promise<void>
  getDetail: (messageId: number) => Promise<AgentMessageDetail>
}

type SnapshotResult = {
  items: AgentEvent[]
  cursor: number
  hasMore: boolean
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
 * stale. The stream is stopped on session change and unmount.
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
  const detailsRef = useRef(new Map<number, AgentMessageDetail>())
  const sessionRunning = session?.running ?? false

  useEffect(() => {
    activitiesRef.current = activities
  }, [activities])

  const loadSnapshot = useCallback(
    async (id: number, mode: "merge" | "replace"): Promise<SnapshotResult> => {
      const page = await fetchActivities(id, { limit: SNAPSHOT_LIMIT })
      const items = normalizeActivities(page.items)
      const result: SnapshotResult = {
        items,
        cursor: typeof page.snapshot_cursor === "number" ? page.snapshot_cursor : 0,
        hasMore: Boolean(page.has_more),
      }
      // A session change while the request was in flight must not write into
      // the new session's state.
      if (sessionIdRef.current !== id) return result
      setHasMore(result.hasMore)
      setActivities((previous) => (mode === "replace" ? items : mergeActivities(previous, items)))
      return result
    },
    [],
  )

  const reload = useCallback(
    async (mode: "merge" | "replace"): Promise<void> => {
      const id = sessionIdRef.current
      if (id === null) return
      try {
        const snapshot = await loadSnapshot(id, mode)
        if (sessionIdRef.current !== id) return
        const nextRunning = runningFromActivities(snapshot.items)
        if (nextRunning !== undefined) setRunning(nextRunning)
      } catch (caught) {
        if (sessionIdRef.current === id) setError(describeError(caught))
      }
    },
    [loadSnapshot],
  )

  useEffect(() => {
    sessionIdRef.current = sessionId
    detailsRef.current.clear()
    setError("")
    setActivities([])
    setHasMore(false)
    setRunning(sessionRunning)

    if (sessionId === null) {
      setLoading(false)
      setStatus("closed")
      stopRef.current?.()
      stopRef.current = null
      return
    }

    const id = sessionId
    const token = getToken()
    setLoading(true)
    setStatus("connecting")

    if (!token) {
      setLoading(false)
      setStatus("closed")
      setError("当前会话没有访问令牌，请重新登录。")
      return
    }
    const bearer = token

    let disposed = false

    async function reloadAfterEvent(mode: "merge" | "replace"): Promise<void> {
      try {
        const snapshot = await loadSnapshot(id, mode)
        if (disposed) return
        const nextRunning = runningFromActivities(snapshot.items)
        if (nextRunning !== undefined) setRunning(nextRunning)
      } catch (caught) {
        if (!disposed) setError(describeError(caught))
      }
    }

    function handleEvent(event: SSEEvent): void {
      if (disposed) return
      const activity = parseActivity(event)
      if (!activity) return
      setActivities((previous) => mergeActivities(previous, [activity]))
      const kind = activityKind(activity)
      if (kind === "run_started") {
        setRunning(true)
      } else if (kind === "run_finished") {
        setRunning(false)
        // Reconcile with the persisted snapshot in case an event was missed.
        void reloadAfterEvent("merge")
      }
    }

    async function connect(): Promise<void> {
      try {
        const snapshot = await loadSnapshot(id, "replace")
        if (disposed) return
        const nextRunning = runningFromActivities(snapshot.items)
        setRunning(nextRunning === undefined ? sessionRunning : nextRunning)
        setLoading(false)
        stopRef.current = connectSSE({
          url: resolveApiUrl(`/api/stream?channel=${encodeURIComponent(`agent:${id}`)}`),
          token: bearer,
          lastEventId: snapshot.cursor,
          onEvent: handleEvent,
          onStatus: (next) => {
            if (!disposed) setStatus(next)
          },
          onReset: () => {
            if (disposed) return
            setActivities([])
            setHasMore(false)
            void reloadAfterEvent("replace")
          },
        })
      } catch (caught) {
        if (disposed) return
        setLoading(false)
        setStatus("closed")
        setError(describeError(caught))
      }
    }

    void connect()

    return () => {
      disposed = true
      stopRef.current?.()
      stopRef.current = null
    }
  }, [sessionId, sessionRunning, loadSnapshot])

  const loadEarlier = useCallback(async (): Promise<void> => {
    const id = sessionIdRef.current
    const first = activitiesRef.current[0]
    if (id === null || first === undefined) return
    try {
      const page = await fetchActivities(id, { before_seq: first.seq, limit: SNAPSHOT_LIMIT })
      if (sessionIdRef.current !== id) return
      const items = normalizeActivities(page.items)
      setActivities((previous) => mergeActivities(previous, items))
      setHasMore(Boolean(page.has_more))
    } catch (caught) {
      if (sessionIdRef.current === id) setError(describeError(caught))
    }
  }, [])

  const refresh = useCallback(async (): Promise<void> => {
    await reload("merge")
  }, [reload])

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
    getDetail,
  }
}
