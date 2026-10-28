"use client"

import { useCallback, useEffect, useRef, useState } from "react"

import type { VersionEvent } from "@/features/versions/types"
import { useVersionStream } from "@/features/versions/useVersionStream"
import type { SSEStatus } from "@/lib/sse-client"

import { mindmapApi } from "./api"
import { describeMindmapError } from "./error-messages"
import type { MindmapGraph } from "./types"

/** At most one graph refresh per window, whichever events trigger it (spec §9.2). */
export const MINDMAP_REFRESH_THROTTLE_MS = 500

/** SSE event types that can change the graph or its editability (spec §7.5). */
function isRefreshEvent(type: string): boolean {
  return type.startsWith("case.") || type.startsWith("gate.") || type === "version.status_changed"
}

function maxEventSeq(events: VersionEvent[]): number {
  let max = 0
  for (const event of events) {
    if (event.seq > max) max = event.seq
  }
  return max
}

/**
 * A `stream.reset` makes `useVersionStream` refetch its snapshot and merge it
 * into the rendered events. When that snapshot is pruned (cursor too old), the
 * merged list jumps over channel seqs; the missed events may have changed the
 * graph, so only a full refetch is safe (spec §11 R9).
 */
function hasMissedEvents(events: VersionEvent[], previousMax: number): boolean {
  if (previousMax <= 0) return false
  const max = maxEventSeq(events)
  if (max <= previousMax + 1) return false
  const present = new Set(events.map((event) => event.seq))
  for (let seq = previousMax + 1; seq < max; seq += 1) {
    if (!present.has(seq)) return true
  }
  return false
}

export type UseMindmapResult = {
  /** The server-authoritative graph snapshot; null before the first load. */
  graph: MindmapGraph | null
  /** True while the first read for the current version is in flight. */
  loading: boolean
  /** User-facing Chinese failure copy; empty when the last read succeeded. */
  error: string
  /** Live/reconnecting/closed state of the version channel (spec §9.2). */
  streamStatus: SSEStatus
  /** Forces a full refetch (retry button, post-write reconciliation). */
  refresh: () => Promise<void>
  /** Replaces the graph with a server response after a write (no optimistic UI). */
  applyGraph: (graph: MindmapGraph) => void
}

/**
 * Loads one version's case graph and keeps it fresh: the initial `GET /mindmap`
 * is followed by a full refetch on a reconnected stream or a detected event
 * gap, and by a throttled refetch (≤1/500ms) for `case.*`, `gate.*` and
 * `version.status_changed` events. Writes stay server-authoritative: callers
 * feed their response back through `applyGraph`, and `refresh()` reconciles the
 * whole snapshot after a conflict or a run.
 */
export function useMindmap(versionId: string): UseMindmapResult {
  const [graph, setGraph] = useState<MindmapGraph | null>(null)
  const [loading, setLoading] = useState(versionId !== "")
  const [error, setError] = useState("")

  const { events, status } = useVersionStream(versionId)

  const versionIdRef = useRef(versionId)
  const generationRef = useRef(0)
  const requestRef = useRef(0)
  const maxSeqRef = useRef(0)
  const previousStatusRef = useRef<SSEStatus>(status)
  const lastRefreshAtRef = useRef(0)
  const trailingTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  const cancelTrailing = useCallback((): void => {
    if (trailingTimerRef.current !== null) {
      clearTimeout(trailingTimerRef.current)
      trailingTimerRef.current = null
    }
  }, [])

  const loadGraph = useCallback(
    async (id: string, generation: number, initial: boolean): Promise<void> => {
      if (id === "") return
      const request = requestRef.current + 1
      requestRef.current = request
      if (initial) setLoading(true)
      try {
        const next = await mindmapApi.getGraph(id)
        if (versionIdRef.current !== id || generationRef.current !== generation) return
        if (requestRef.current !== request) return
        setGraph(next)
        setError("")
      } catch (caught) {
        if (versionIdRef.current !== id || generationRef.current !== generation) return
        if (requestRef.current !== request) return
        setError(describeMindmapError(caught))
      } finally {
        if (versionIdRef.current === id && generationRef.current === generation) {
          setLoading(false)
        }
      }
    },
    [],
  )

  const runRefresh = useCallback(
    (id: string, generation: number): Promise<void> => {
      cancelTrailing()
      lastRefreshAtRef.current = Date.now()
      return loadGraph(id, generation, false)
    },
    [cancelTrailing, loadGraph],
  )

  const scheduleThrottledRefresh = useCallback((): void => {
    const id = versionIdRef.current
    if (id === "") return
    const generation = generationRef.current
    if (trailingTimerRef.current !== null) return
    const wait = MINDMAP_REFRESH_THROTTLE_MS - (Date.now() - lastRefreshAtRef.current)
    if (wait <= 0) {
      void runRefresh(id, generation)
      return
    }
    trailingTimerRef.current = setTimeout(() => {
      trailingTimerRef.current = null
      if (versionIdRef.current !== id || generationRef.current !== generation) return
      void runRefresh(id, generation)
    }, wait)
  }, [runRefresh])

  // Initial load and full reset when the version changes.
  useEffect(() => {
    versionIdRef.current = versionId
    generationRef.current += 1
    requestRef.current += 1
    cancelTrailing()
    maxSeqRef.current = 0
    lastRefreshAtRef.current = Date.now()
    setGraph(null)
    setError("")
    setLoading(versionId !== "")

    const generation = generationRef.current
    if (versionId !== "") void loadGraph(versionId, generation, true)

    return () => {
      generationRef.current += 1
      cancelTrailing()
    }
  }, [versionId, loadGraph, cancelTrailing])

  // `case.*`/`gate.*`/`version.status_changed` events refresh the graph, at most
  // once per throttle window. A seq gap means a reset dropped events: refetch
  // the whole snapshot immediately instead of waiting for the window.
  useEffect(() => {
    if (versionId === "" || events.length === 0) return
    const id = versionIdRef.current
    const generation = generationRef.current
    const previousMax = maxSeqRef.current
    const nextMax = maxEventSeq(events)
    if (nextMax > maxSeqRef.current) maxSeqRef.current = nextMax
    if (nextMax <= previousMax) return

    if (hasMissedEvents(events, previousMax)) {
      void runRefresh(id, generation)
      return
    }
    if (events.some((event) => event.seq > previousMax && isRefreshEvent(event.type))) {
      scheduleThrottledRefresh()
    }
  }, [events, versionId, runRefresh, scheduleThrottledRefresh])

  // A reconnect after `reconnecting`/`closed` may have skipped events while the
  // transport was down: reconcile with one full read.
  useEffect(() => {
    const previous = previousStatusRef.current
    previousStatusRef.current = status
    if (status !== "live") return
    if (previous !== "reconnecting" && previous !== "closed") return
    const id = versionIdRef.current
    if (id === "") return
    void runRefresh(id, generationRef.current)
  }, [status, runRefresh])

  const refresh = useCallback(async (): Promise<void> => {
    const id = versionIdRef.current
    if (id === "") return
    await runRefresh(id, generationRef.current)
  }, [runRefresh])

  const applyGraph = useCallback((next: MindmapGraph): void => {
    // Invalidate an in-flight read: the write response is newer than it.
    requestRef.current += 1
    setGraph(next)
    setError("")
  }, [])

  return {
    graph,
    loading,
    error,
    streamStatus: status,
    refresh,
    applyGraph,
  }
}
