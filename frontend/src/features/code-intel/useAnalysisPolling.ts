"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"

import { useVersionStream } from "@/features/versions/useVersionStream"
import { ApiError } from "@/lib/api-client"

import { getLatestAnalysis } from "./api"
import type { Analysis } from "./types"

/** Poll cadence for a queued/running analysis (spec §9.2/§9.3). */
export const ANALYSIS_POLL_INTERVAL_MS = 1500
/** Safety cap from spec §9.2 (`RunStatus`): stop after 60 polls, offer refresh. */
export const ANALYSIS_POLL_MAX_ATTEMPTS = 60
/** Debounce applied to `code.*` version SSE events (spec §9.3). */
export const CODE_EVENT_DEBOUNCE_MS = 300

const ACTIVE_STATUSES = new Set(["queued", "running"])

function isActiveAnalysis(status: string): boolean {
  return ACTIVE_STATUSES.has(status)
}

export type AnalysisPollingOptions = {
  /** False while the version has no code repository bound: no reads at all. */
  enabled?: boolean
  /** Called after a debounced `code.*` event so the container can refetch changes. */
  onCodeEvent?: () => void
}

export type AnalysisPollingState = {
  analysis: Analysis | null
  /** True while the first `analyses/latest` read is in flight. */
  loading: boolean
  /** True while the queued/running poll is scheduled. */
  polling: boolean
  /** True when the 60-poll cap was reached before a terminal status. */
  exhausted: boolean
  /** The last non-404 refresh failure, if any. */
  error: unknown
  /** Forces one latest-analysis read (used after starting/re-analyzing). */
  refresh: () => Promise<Analysis | null>
}

/**
 * Loads the latest analysis and follows it while it is queued/running
 * (spec §9.3): polls every 1.5s, stops on done/failed/unmount, pauses while
 * `document.hidden` and reads once immediately when the page becomes visible
 * again. `code.changed`/`code.impact_computed` version events also trigger a
 * debounced refresh (300ms) instead of waiting for the next interval.
 */
export function useAnalysisPolling(
  versionId: string,
  options: AnalysisPollingOptions = {},
): AnalysisPollingState {
  const enabled = options.enabled ?? true
  const [analysis, setAnalysis] = useState<Analysis | null>(null)
  const [loading, setLoading] = useState(enabled)
  const [exhausted, setExhausted] = useState(false)
  const [error, setError] = useState<unknown>(null)

  const requestRef = useRef(0)
  const attemptsRef = useRef(0)
  const exhaustedRef = useRef(false)
  const appliedCodeSeqRef = useRef(0)
  const onCodeEventRef = useRef(options.onCodeEvent)
  onCodeEventRef.current = options.onCodeEvent

  const loadLatest = useCallback(async (): Promise<Analysis | null> => {
    if (!enabled || versionId === "") return null
    const requestId = ++requestRef.current
    try {
      const latest = await getLatestAnalysis(versionId)
      if (requestRef.current !== requestId) return null
      setAnalysis(latest ?? null)
      setError(null)
      // A completed/failed (or missing) analysis means the cap hint is stale,
      // even when the reads that discover it come from refresh()/SSE.
      if (latest === null || !isActiveAnalysis(latest.status)) {
        exhaustedRef.current = false
        setExhausted(false)
      }
      return latest ?? null
    } catch (caught) {
      if (requestRef.current !== requestId) return null
      // A version without any analysis is a normal state, not an error.
      if (caught instanceof ApiError && caught.status === 404) {
        setAnalysis(null)
        setError(null)
        return null
      }
      setError(caught)
      return null
    }
  }, [enabled, versionId])

  const refresh = useCallback(async (): Promise<Analysis | null> => loadLatest(), [loadLatest])

  // Initial load (and reset when the version/binding changes).
  useEffect(() => {
    attemptsRef.current = 0
    exhaustedRef.current = false
    setAnalysis(null)
    setError(null)
    setExhausted(false)
    setLoading(enabled)
    // A new version/channel restarts code-event tracking from zero.
    appliedCodeSeqRef.current = 0

    if (!enabled || versionId === "") {
      setLoading(false)
      return
    }

    let cancelled = false
    void loadLatest().finally(() => {
      if (!cancelled) setLoading(false)
    })
    return () => {
      cancelled = true
      // Invalidate an in-flight latest read for this mount/version.
      requestRef.current += 1
    }
  }, [enabled, versionId, loadLatest])

  // Poll only while the analysis is queued/running.
  const shouldPoll = enabled && analysis !== null && isActiveAnalysis(analysis.status)

  useEffect(() => {
    if (!shouldPoll) return

    attemptsRef.current = 0
    exhaustedRef.current = false
    setExhausted(false)

    let stopped = false
    let timer: ReturnType<typeof setInterval> | null = null

    const stopTimer = (): void => {
      if (timer !== null) {
        clearInterval(timer)
        timer = null
      }
    }

    const tick = async (): Promise<void> => {
      if (stopped) return
      if (typeof document !== "undefined" && document.hidden) return
      if (exhaustedRef.current) return
      if (attemptsRef.current >= ANALYSIS_POLL_MAX_ATTEMPTS) {
        exhaustedRef.current = true
        setExhausted(true)
        stopTimer()
        return
      }
      attemptsRef.current += 1
      const latest = await loadLatest()
      if (stopped || latest === null) return
      if (!isActiveAnalysis(latest.status)) {
        stopped = true
        stopTimer()
      }
    }

    timer = setInterval(() => {
      void tick()
    }, ANALYSIS_POLL_INTERVAL_MS)

    const handleVisibility = (): void => {
      if (typeof document === "undefined" || document.hidden) return
      if (!exhaustedRef.current) void tick()
    }
    if (typeof document !== "undefined") {
      document.addEventListener("visibilitychange", handleVisibility)
    }

    return () => {
      stopped = true
      stopTimer()
      if (typeof document !== "undefined") {
        document.removeEventListener("visibilitychange", handleVisibility)
      }
    }
  }, [shouldPoll, loadLatest])

  // `code.*` version events debounce into one latest read (spec §9.3).
  const { events } = useVersionStream(enabled ? versionId : "")
  const lastCodeSeq = useMemo(() => {
    let seq = 0
    for (const event of events) {
      if (event.type.startsWith("code.")) seq = Math.max(seq, event.seq)
    }
    return seq
  }, [events])

  useEffect(() => {
    if (lastCodeSeq <= appliedCodeSeqRef.current) return
    appliedCodeSeqRef.current = lastCodeSeq
    const timer = setTimeout(() => {
      void loadLatest()
      onCodeEventRef.current?.()
    }, CODE_EVENT_DEBOUNCE_MS)
    return () => clearTimeout(timer)
  }, [lastCodeSeq, loadLatest])

  return {
    analysis,
    loading,
    polling: shouldPoll && !exhausted,
    exhausted,
    error,
    refresh,
  }
}
