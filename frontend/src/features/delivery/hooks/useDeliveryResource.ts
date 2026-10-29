"use client"

import { useCallback, useEffect, useRef, useState } from "react"

import type { VersionEvent } from "@/features/versions/types"
import { useVersionStream } from "@/features/versions/useVersionStream"
import type { ApiPage } from "@/lib/api-client"
import type { SSEStatus } from "@/lib/sse-client"

import { describeDeliveryError } from "../api"
import type { DeliveryStream } from "../types"

/** The stream events/status a delivery hook consumes. */
type StreamInput = { events: VersionEvent[]; status?: SSEStatus }

/**
 * One stream per hook tree. A caller that already owns the version stream (for
 * example the version detail container) passes it down; the hook still calls
 * `useVersionStream("")` to keep hook order stable, and an empty id never opens
 * a connection.
 */
function useDeliveryStreamInput(
  versionId: string,
  provided?: StreamInput,
): { events: VersionEvent[]; status: SSEStatus } {
  const own = useVersionStream(provided === undefined ? versionId : "")
  if (provided === undefined) return { events: own.events, status: own.status }
  return { events: provided.events, status: provided.status ?? own.status }
}

function isAbortError(error: unknown): boolean {
  return error instanceof Error && error.name === "AbortError"
}

function maxEventSeq(events: VersionEvent[]): number {
  let max = 0
  for (const event of events) {
    if (event.seq > max) max = event.seq
  }
  return max
}

/** Events may omit `version_id`; a null id still belongs to the open channel. */
function isEventForVersion(event: VersionEvent, versionId: string): boolean {
  return event.version_id === null || event.version_id === versionId
}

type ResourceOptions<T> = {
  versionId: string
  /** Reload trigger for parameter changes (never a fetch input). */
  key: string
  enabled: boolean
  fetcher: (signal: AbortSignal) => Promise<T>
  stream?: DeliveryStream
  isRelevantEvent: (event: VersionEvent) => boolean
}

export type DeliveryResource<T> = {
  data: T | null
  loading: boolean
  error: string
  streamStatus: SSEStatus
  refresh: () => Promise<void>
}

/**
 * Abort-safe read with live refresh: the latest request wins, and a new
 * issue or report event (or a reconnect) refetches the HTTP snapshot. The
 * event itself is never merged optimistically, so ordering cannot corrupt the
 * rendered state (spec §10.8).
 */
export function useDeliveryResource<T>(options: ResourceOptions<T>): DeliveryResource<T> {
  const { versionId, key, enabled, fetcher, stream, isRelevantEvent } = options
  const { events, status } = useDeliveryStreamInput(versionId, stream)
  const [data, setData] = useState<T | null>(null)
  const [loading, setLoading] = useState(enabled)
  const [error, setError] = useState("")

  const fetcherRef = useRef(fetcher)
  const abortRef = useRef<AbortController | null>(null)
  const requestRef = useRef(0)
  const maxSeqRef = useRef(0)
  const eventsRef = useRef(events)
  const previousStatusRef = useRef<SSEStatus>(status)

  fetcherRef.current = fetcher
  eventsRef.current = events

  const load = useCallback(
    async (initial: boolean): Promise<void> => {
      const request = requestRef.current + 1
      requestRef.current = request
      abortRef.current?.abort()
      abortRef.current = null

      if (!enabled) {
        setData(null)
        setLoading(false)
        setError("")
        return
      }

      const controller = new AbortController()
      abortRef.current = controller
      if (initial) setLoading(true)

      try {
        const next = await fetcherRef.current(controller.signal)
        if (controller.signal.aborted || requestRef.current !== request) return
        setData(next)
        setError("")
      } catch (caught) {
        if (controller.signal.aborted || requestRef.current !== request) return
        if (isAbortError(caught)) return
        setError(describeDeliveryError(caught))
      } finally {
        if (requestRef.current === request) {
          if (abortRef.current === controller) abortRef.current = null
          setLoading(false)
        }
      }
    },
    [enabled],
  )

  // Initial load and full reset when the version/key changes. Events already in
  // the snapshot become the baseline, so they never trigger an immediate reload.
  // biome-ignore lint/correctness/useExhaustiveDependencies: key is the reload trigger
  useEffect(() => {
    requestRef.current += 1
    abortRef.current?.abort()
    abortRef.current = null
    maxSeqRef.current = maxEventSeq(eventsRef.current)
    setError("")

    if (!enabled) {
      setData(null)
      setLoading(false)
      return
    }

    setData(null)
    setLoading(true)
    void load(true)

    return () => {
      abortRef.current?.abort()
      abortRef.current = null
    }
  }, [enabled, key, load])

  // A new live event for this channel means the REST projection is stale.
  useEffect(() => {
    if (!enabled || events.length === 0) return
    const scoped = events.filter((event) => isEventForVersion(event, versionId))
    if (scoped.length === 0) return

    const previousMax = maxSeqRef.current
    const nextMax = maxEventSeq(scoped)
    if (nextMax <= previousMax) return
    maxSeqRef.current = nextMax
    if (!scoped.some((event) => event.seq > previousMax && isRelevantEvent(event))) return
    void load(false)
  }, [events, enabled, versionId, isRelevantEvent, load])

  // A reconnect may have missed events; reconcile with one full read.
  useEffect(() => {
    const previous = previousStatusRef.current
    previousStatusRef.current = status
    if (!enabled || status !== "live") return
    if (previous !== "reconnecting" && previous !== "closed") return
    void load(false)
  }, [status, enabled, load])

  const refresh = useCallback(async (): Promise<void> => {
    await load(false)
  }, [load])

  return { data, loading, error, streamStatus: status, refresh }
}

type PageOptions<T> = {
  versionId: string
  key: string
  enabled: boolean
  fetcher: (cursor: string, signal: AbortSignal) => Promise<ApiPage<T>>
  stream?: DeliveryStream
  isRelevantEvent: (event: VersionEvent) => boolean
}

export type DeliveryPage<T> = {
  items: T[]
  hasMore: boolean
  loading: boolean
  loadingMore: boolean
  error: string
  streamStatus: SSEStatus
  refresh: () => Promise<void>
  loadMore: () => Promise<void>
}

/**
 * One cursor page with `loadMore`/`refresh` and the same live-refresh rules as
 * `useDeliveryResource`. Filter changes reset the page; `loadMore` appends.
 */
export function useDeliveryPage<T>(options: PageOptions<T>): DeliveryPage<T> {
  const { versionId, key, enabled, fetcher, stream, isRelevantEvent } = options
  const { events, status } = useDeliveryStreamInput(versionId, stream)
  const [items, setItems] = useState<T[]>([])
  const [nextCursor, setNextCursor] = useState("")
  const [hasMore, setHasMore] = useState(false)
  const [loading, setLoading] = useState(enabled)
  const [loadingMore, setLoadingMore] = useState(false)
  const [error, setError] = useState("")

  const fetcherRef = useRef(fetcher)
  const abortRef = useRef<AbortController | null>(null)
  const requestRef = useRef(0)
  const maxSeqRef = useRef(0)
  const eventsRef = useRef(events)
  const previousStatusRef = useRef<SSEStatus>(status)

  fetcherRef.current = fetcher
  eventsRef.current = events

  const load = useCallback(
    async (mode: "initial" | "refresh" | "more", cursor: string): Promise<void> => {
      const request = requestRef.current + 1
      requestRef.current = request
      abortRef.current?.abort()
      abortRef.current = null

      if (!enabled) {
        setItems([])
        setNextCursor("")
        setHasMore(false)
        setLoading(false)
        setLoadingMore(false)
        setError("")
        return
      }

      const controller = new AbortController()
      abortRef.current = controller
      if (mode === "initial") setLoading(true)
      if (mode === "more") setLoadingMore(true)

      try {
        const page = await fetcherRef.current(cursor, controller.signal)
        if (controller.signal.aborted || requestRef.current !== request) return
        setItems((previous) => (mode === "more" ? [...previous, ...page.items] : page.items))
        setNextCursor(page.nextCursor)
        setHasMore(page.hasMore)
        setError("")
      } catch (caught) {
        if (controller.signal.aborted || requestRef.current !== request) return
        if (isAbortError(caught)) return
        setError(describeDeliveryError(caught))
      } finally {
        if (requestRef.current === request) {
          if (abortRef.current === controller) abortRef.current = null
          setLoading(false)
          setLoadingMore(false)
        }
      }
    },
    [enabled],
  )

  // biome-ignore lint/correctness/useExhaustiveDependencies: key is the reload trigger
  useEffect(() => {
    requestRef.current += 1
    abortRef.current?.abort()
    abortRef.current = null
    maxSeqRef.current = maxEventSeq(eventsRef.current)
    setItems([])
    setNextCursor("")
    setHasMore(false)
    setError("")

    if (!enabled) {
      setLoading(false)
      setLoadingMore(false)
      return
    }

    setLoading(true)
    void load("initial", "")

    return () => {
      abortRef.current?.abort()
      abortRef.current = null
    }
  }, [enabled, key, load])

  useEffect(() => {
    if (!enabled || events.length === 0) return
    const scoped = events.filter((event) => isEventForVersion(event, versionId))
    if (scoped.length === 0) return

    const previousMax = maxSeqRef.current
    const nextMax = maxEventSeq(scoped)
    if (nextMax <= previousMax) return
    maxSeqRef.current = nextMax
    if (!scoped.some((event) => event.seq > previousMax && isRelevantEvent(event))) return
    void load("refresh", "")
  }, [events, enabled, versionId, isRelevantEvent, load])

  useEffect(() => {
    const previous = previousStatusRef.current
    previousStatusRef.current = status
    if (!enabled || status !== "live") return
    if (previous !== "reconnecting" && previous !== "closed") return
    void load("refresh", "")
  }, [status, enabled, load])

  const refresh = useCallback(async (): Promise<void> => {
    await load("refresh", "")
  }, [load])

  const loadMore = useCallback(async (): Promise<void> => {
    if (!hasMore || loading || loadingMore) return
    await load("more", nextCursor)
  }, [hasMore, loading, loadingMore, nextCursor, load])

  return {
    items,
    hasMore,
    loading,
    loadingMore,
    error,
    streamStatus: status,
    refresh,
    loadMore,
  }
}
