"use client"

import { useCallback, useEffect, useRef, useState } from "react"

import { resolveApiUrl } from "@/lib/api-client"
import { getToken } from "@/lib/auth-storage"
import { connectSSE, type SSEEvent, type SSEStatus } from "@/lib/sse-client"

import { fetchVersionEvents } from "./api"
import type { VersionEvent, VersionEventPage } from "./types"

/** Snapshot page size from the design (§9.4): the last 50 persisted events. */
const SNAPSHOT_LIMIT = 50

export type VersionStreamState = {
  events: VersionEvent[]
  status: SSEStatus
  reconnect: () => void
}

/** A new version replaces the rendered events; a resnapshot merges into them. */
type SnapshotMode = "replace" | "merge"

type StreamOptions = {
  /** Keep the exposed status while a replacement stream opens after a reset. */
  keepStatus?: boolean
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

function readStrings(value: unknown): string[] {
  if (!Array.isArray(value)) return []
  return value.filter((item): item is string => typeof item === "string")
}

/**
 * Validates one snapshot/live event and keeps only the envelope fields the UI
 * consumes. Anything without a numeric seq and a type is dropped: a malformed
 * frame must never enter the rendered event list.
 */
function normalizeEvent(raw: unknown): VersionEvent | null {
  if (!isRecord(raw)) return null
  if (typeof raw.type !== "string" || raw.type === "") return null
  if (typeof raw.seq !== "number" || !Number.isFinite(raw.seq)) return null
  const refs = isRecord(raw.refs) ? raw.refs : {}

  return {
    seq: raw.seq,
    channel: typeof raw.channel === "string" ? raw.channel : "",
    type: raw.type,
    version_id: typeof raw.version_id === "string" ? raw.version_id : null,
    batch_id: typeof raw.batch_id === "string" ? raw.batch_id : null,
    case_id: typeof raw.case_id === "string" ? raw.case_id : null,
    step_id: typeof raw.step_id === "string" ? raw.step_id : null,
    agent_role: typeof raw.agent_role === "string" ? raw.agent_role : null,
    session_id: typeof raw.session_id === "string" ? raw.session_id : null,
    ts: typeof raw.ts === "string" ? raw.ts : "",
    summary: typeof raw.summary === "string" ? raw.summary : "",
    detail: isRecord(raw.detail) ? raw.detail : {},
    refs: {
      evidence: readStrings(refs.evidence),
      case: readStrings(refs.case),
      requirement: readStrings(refs.requirement),
      code: readStrings(refs.code),
    },
  }
}

function normalizeEvents(items: unknown): VersionEvent[] {
  if (!Array.isArray(items)) return []
  const events: VersionEvent[] = []
  for (const item of items) {
    const event = normalizeEvent(item)
    if (event) events.push(event)
  }
  return mergeEvents([], events)
}

/** Parses one SSE frame into an event, ignoring malformed payloads. */
function parseEvent(event: SSEEvent): VersionEvent | null {
  try {
    return normalizeEvent(JSON.parse(event.data))
  } catch {
    return null
  }
}

/** Dedupes by seq (latest wins) and returns a seq-ascending list. */
function mergeEvents(previous: VersionEvent[], incoming: VersionEvent[]): VersionEvent[] {
  const bySeq = new Map<number, VersionEvent>()
  for (const event of previous) bySeq.set(event.seq, event)
  for (const event of incoming) bySeq.set(event.seq, event)
  return [...bySeq.values()].sort((left, right) => left.seq - right.seq)
}

/**
 * Loads a version's persisted event snapshot, then follows its live SSE stream
 * (spec §9.4). The snapshot cursor becomes the stream's `lastEventId`, so no
 * event is lost between the REST read and the connect. Events are deduped by
 * seq. A `stream.reset` merges a fresh snapshot with the live events received
 * while it was in flight and replaces the transport from the merged high-water
 * seq, so live events survive the reset and a later reconnect cannot fall back
 * to the stale cursor that caused it. The stream is stopped on version change
 * and unmount. Exposes `live`/`reconnecting`/`closed` (plus the transient
 * `connecting`) so the UI can show "重连中" instead of "无消息".
 */
export function useVersionStream(versionId: string): VersionStreamState {
  const [events, setEvents] = useState<VersionEvent[]>([])
  const [status, setStatus] = useState<SSEStatus>("closed")

  const versionIdRef = useRef(versionId)
  const stopRef = useRef<(() => void) | null>(null)
  const generationRef = useRef(0)
  /** Highest channel seq already covered by a snapshot or a live event. */
  const lastSeqRef = useRef(0)
  /** One reset refetch at a time, even if the server repeats the control frame. */
  const resyncingRef = useRef(false)
  /** Synced after render: stream callbacks are created before this handler. */
  const reloadAfterResetRef = useRef<(id: string, generation: number, token: string) => void>(
    () => {},
  )

  const loadSnapshot = useCallback(
    async (id: string, generation: number, mode: SnapshotMode): Promise<number> => {
      const snapshot: VersionEventPage = await fetchVersionEvents(id, { limit: SNAPSHOT_LIMIT })
      const items = normalizeEvents(snapshot.events)
      // A version change / reconnect while the request was in flight must not
      // write into the new connection's state.
      if (versionIdRef.current === id && generationRef.current === generation) {
        setEvents((previous) => (mode === "replace" ? items : mergeEvents(previous, items)))
        // The cursor is the channel head and may be past the last returned
        // event; the high-water seq covers both.
        const snapshotSeq = Math.max(snapshot.cursor, items.at(-1)?.seq ?? 0)
        lastSeqRef.current =
          mode === "replace" ? snapshotSeq : Math.max(lastSeqRef.current, snapshotSeq)
      }
      return snapshot.cursor
    },
    [],
  )

  const startStream = useCallback(
    (
      id: string,
      generation: number,
      token: string,
      cursor: number,
      options: StreamOptions = {},
    ): (() => void) | null => {
      const stop = connectSSE({
        url: resolveApiUrl(`/api/versions/${id}/stream`),
        token,
        lastEventId: cursor,
        onEvent: (event) => {
          if (generationRef.current !== generation) return
          const parsed = parseEvent(event)
          if (!parsed) return
          lastSeqRef.current = Math.max(lastSeqRef.current, parsed.seq)
          setEvents((previous) => mergeEvents(previous, [parsed]))
        },
        onStatus: (next) => {
          if (generationRef.current !== generation) return
          // A replacement stream must not regress a UI that was already live:
          // the previous stream stayed live while the snapshot was refetched.
          if (options.keepStatus && next === "connecting") return
          setStatus(next)
        },
        onReset: () => {
          if (generationRef.current !== generation) return
          reloadAfterResetRef.current(id, generation, token)
        },
      })
      // The version may have changed while connectSSE was being set up.
      if (versionIdRef.current !== id || generationRef.current !== generation) {
        stop()
        return null
      }
      return stop
    },
    [],
  )

  const reloadAfterReset = useCallback(
    async (id: string, generation: number, token: string): Promise<void> => {
      if (resyncingRef.current) return
      resyncingRef.current = true
      try {
        try {
          // Merge, never replace: live frames received while the snapshot is in
          // flight are newer than the snapshot and must survive it.
          await loadSnapshot(id, generation, "merge")
        } catch {
          // The stream stays connected; the next event or a manual reconnect
          // repairs the snapshot.
          return
        }
        if (versionIdRef.current !== id || generationRef.current !== generation) return

        // A `stream.reset` frame never advances the transport's internal
        // lastSeq, so a later auto-reconnect would present the stale cursor and
        // the server would reset the stream again. Replace the transport with
        // one that resumes from the merged high-water seq. The token the stream
        // connected with is reused: a stale token gets the usual 401 close.
        generationRef.current += 1
        const nextGeneration = generationRef.current
        stopRef.current?.()
        stopRef.current = null
        stopRef.current = startStream(id, nextGeneration, token, lastSeqRef.current, {
          keepStatus: true,
        })
      } finally {
        resyncingRef.current = false
      }
    },
    [loadSnapshot, startStream],
  )

  // A reset frame only arrives after the stream opened, so syncing the latest
  // handler in an effect runs in time.
  useEffect(() => {
    reloadAfterResetRef.current = reloadAfterReset
  }, [reloadAfterReset])

  const connectVersion = useCallback(
    async (id: string, mode: SnapshotMode): Promise<void> => {
      const generation = generationRef.current
      const token = getToken()
      if (!token) {
        if (versionIdRef.current === id && generationRef.current === generation) {
          setStatus("closed")
        }
        return
      }

      let cursor = 0
      try {
        cursor = await loadSnapshot(id, generation, mode)
      } catch {
        // A channel with no persisted events yet 404s, and a transient read
        // failure must not block live updates: open the stream from the start
        // instead of from a snapshot cursor.
        cursor = 0
      }
      if (versionIdRef.current !== id || generationRef.current !== generation) return

      // Never resume below a seq already rendered: live events may have run
      // ahead of the snapshot cursor just fetched.
      const lastEventId = Math.max(cursor, lastSeqRef.current)
      stopRef.current = startStream(id, generation, token, lastEventId)
    },
    [loadSnapshot, startStream],
  )

  useEffect(() => {
    versionIdRef.current = versionId
    generationRef.current += 1
    stopRef.current?.()
    stopRef.current = null
    lastSeqRef.current = 0
    setEvents([])

    if (versionId === "") {
      setStatus("closed")
      return
    }

    setStatus("connecting")
    void connectVersion(versionId, "replace")

    return () => {
      generationRef.current += 1
      stopRef.current?.()
      stopRef.current = null
    }
  }, [versionId, connectVersion])

  /**
   * Manual recovery for the disconnected UI: reopens the stream from a fresh
   * snapshot while keeping the events already rendered.
   */
  const reconnect = useCallback((): void => {
    const id = versionIdRef.current
    if (id === "") return
    generationRef.current += 1
    stopRef.current?.()
    stopRef.current = null
    setStatus("connecting")
    void connectVersion(id, "merge")
  }, [connectVersion])

  return { events, status, reconnect }
}
