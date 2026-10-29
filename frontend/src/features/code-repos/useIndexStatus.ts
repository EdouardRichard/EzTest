"use client"

import { useCallback, useEffect, useRef, useState } from "react"

import { getCodeRepos, getLatestIndexRun, startIndex } from "@/features/code-intel/api"
import type { IndexRun } from "@/features/code-intel/types"
import { ApiError } from "@/lib/api-client"

/** Poll cadence for a queued/running index run (spec §7.4/§9.2). */
export const INDEX_POLL_INTERVAL_MS = 1500
/** Safety cap from spec §7.4: stop after 60 polls and ask for a manual refresh. */
export const INDEX_POLL_MAX_ATTEMPTS = 60

const ACTIVE_INDEX_STATUSES = new Set(["queued", "running"])

/** True while a trigger failed because another index run is already active. */
function isIndexInProgress(error: unknown): boolean {
  return error instanceof ApiError && error.details.reason === "index_in_progress"
}

/** Latest index projection of one repository (spec §7.2 `RepoSummary.index`). */
export type RepoIndexState = {
  /** `lastIndexedAt` from the P4 projection; the page falls back to P2 data. */
  lastIndexedAt: string | null
  /** Latest index run, or null when the repository has never been indexed. */
  run: IndexRun | null
}

/** Derived index state rendered by the repository table (spec §9.2). */
export type IndexView = "unindexed" | "queued" | "running" | "done" | "failed"

/** True while a run is queued/running and polling must continue. */
export function isActiveIndexRun(run: { status: string } | null | undefined): boolean {
  return run !== null && run !== undefined && ACTIVE_INDEX_STATUSES.has(run.status)
}

/**
 * Derives the visible index state: the latest run wins for queued/running/
 * failed, while a completed index (or a persisted `lastIndexedAt`) reads as
 * done and everything else is unindexed.
 */
export function indexView(state: RepoIndexState): IndexView {
  const status = state.run?.status ?? ""
  if (status === "queued" || status === "running" || status === "failed") return status
  if (status === "done") return "done"
  return state.lastIndexedAt !== null && state.lastIndexedAt !== "" ? "done" : "unindexed"
}

export type IndexStatusState = {
  /** Latest index projection keyed by the numeric repository id. */
  indexById: Record<number, RepoIndexState>
  /** Repository whose trigger/poll is in flight, or null. */
  refreshingId: number | null
  /** True when the 60-poll cap was reached before a terminal status. */
  exhausted: boolean
  /** The last index failure; the page renders it as a toast. */
  error: unknown
  /** Dismisses the current error toast. */
  clearError: () => void
  /** Reloads every repository's latest index projection (P4 list read). */
  loadIndexStatus: () => Promise<void>
  /** Triggers one index run, polls until terminal, then resolves. */
  refreshIndex: (repoId: number) => Promise<void>
}

/**
 * Loads and follows T0 index state for the repository page (spec §9.2/§7.4):
 * the list read merges P2 repositories with their latest run, and every
 * queued/running run — whether triggered here, discovered on load, or found
 * after a 409 — is polled every 1.5s (at most 60 times), stopping on
 * done/failed/unmount. Only admins reach the trigger; the backend 403 remains
 * the real guard.
 */
export function useIndexStatus(): IndexStatusState {
  const [indexById, setIndexById] = useState<Record<number, RepoIndexState>>({})
  const [refreshingId, setRefreshingId] = useState<number | null>(null)
  const [exhausted, setExhausted] = useState(false)
  const [error, setError] = useState<unknown>(null)

  const mountedRef = useRef(true)
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null)
  const finishRef = useRef<(() => void) | null>(null)
  const refreshingRef = useRef<number | null>(null)
  /** Latest projection, readable from the stable polling interval. */
  const indexByIdRef = useRef<Record<number, RepoIndexState>>({})
  /** Per-repository poll counts, so an effect restart cannot reset the cap. */
  const attemptsRef = useRef<Record<number, number>>({})
  /** Repositories whose automatic polling stopped (cap or read failure). */
  const stoppedIdsRef = useRef<Set<number>>(new Set())
  /** Repositories with a `latest` read in flight, to avoid overlapping polls. */
  const inFlightRef = useRef<Set<number>>(new Set())
  /** Which source owns the current error; a successful list read clears its own. */
  const errorSourceRef = useRef<"load" | "other" | null>(null)

  useEffect(() => {
    indexByIdRef.current = indexById
  }, [indexById])

  const clearError = useCallback(() => {
    errorSourceRef.current = null
    setError(null)
  }, [])

  const stopPolling = useCallback(() => {
    if (pollRef.current !== null) {
      clearInterval(pollRef.current)
      pollRef.current = null
    }
  }, [])

  const settle = useCallback(() => {
    refreshingRef.current = null
    if (mountedRef.current) setRefreshingId(null)
  }, [])

  /** Resolves the pending `refreshIndex` once its repository reaches a terminal state. */
  const resolveRefresh = useCallback((repoId: number) => {
    if (refreshingRef.current === repoId) finishRef.current?.()
  }, [])

  const applyRun = useCallback((repoId: number, run: IndexRun) => {
    setIndexById((previous) => ({
      ...previous,
      [repoId]: {
        lastIndexedAt: previous[repoId]?.lastIndexedAt ?? null,
        run,
      },
    }))
  }, [])

  const loadIndexStatus = useCallback(async () => {
    try {
      const summaries = await getCodeRepos()
      if (!mountedRef.current) return
      let anyActive = false
      let sawTerminal = false
      for (const summary of summaries) {
        const run = summary.index ?? null
        if (isActiveIndexRun(run)) {
          anyActive = true
          continue
        }
        if (run !== null) sawTerminal = true
        // A fresh list read supersedes the previous run's local poll budget.
        delete attemptsRef.current[summary.id]
        stoppedIdsRef.current.delete(summary.id)
      }
      if (errorSourceRef.current === "load") {
        errorSourceRef.current = null
        setError(null)
      }
      setIndexById((previous) => {
        const next: Record<number, RepoIndexState> = { ...previous }
        for (const summary of summaries) {
          next[summary.id] = {
            lastIndexedAt: summary.lastIndexedAt,
            run: summary.index ?? null,
          }
        }
        return next
      })
      if (sawTerminal && !anyActive) setExhausted(false)
    } catch (caught) {
      if (mountedRef.current) {
        errorSourceRef.current = "load"
        setError(caught)
      }
    }
  }, [])

  // One stable interval follows every queued/running run currently projected in
  // `indexById`, including runs discovered by a reload or a 409 recovery.
  const pollActiveRuns = useCallback(async () => {
    if (!mountedRef.current) return
    const activeIds = Object.keys(indexByIdRef.current)
      .map(Number)
      .filter((repoId) => isActiveIndexRun(indexByIdRef.current[repoId]?.run))
    await Promise.all(
      activeIds.map(async (repoId) => {
        if (!mountedRef.current || stoppedIdsRef.current.has(repoId)) return
        if (inFlightRef.current.has(repoId)) return
        const attempts = attemptsRef.current[repoId] ?? 0
        if (attempts >= INDEX_POLL_MAX_ATTEMPTS) {
          stoppedIdsRef.current.add(repoId)
          if (mountedRef.current) setExhausted(true)
          resolveRefresh(repoId)
          return
        }
        attemptsRef.current[repoId] = attempts + 1
        inFlightRef.current.add(repoId)
        try {
          const run = await getLatestIndexRun(repoId)
          if (!mountedRef.current) return
          applyRun(repoId, run)
          if (!isActiveIndexRun(run)) {
            delete attemptsRef.current[repoId]
            stoppedIdsRef.current.delete(repoId)
            setExhausted(false)
            resolveRefresh(repoId)
            return
          }
          // The 60th poll returned a still-active run: stop and offer the
          // manual refresh instead of polling forever.
          if ((attemptsRef.current[repoId] ?? 0) >= INDEX_POLL_MAX_ATTEMPTS) {
            stoppedIdsRef.current.add(repoId)
            setExhausted(true)
            resolveRefresh(repoId)
          }
        } catch (caught) {
          if (mountedRef.current) {
            errorSourceRef.current = "other"
            setError(caught)
          }
          stoppedIdsRef.current.add(repoId)
          resolveRefresh(repoId)
        } finally {
          inFlightRef.current.delete(repoId)
        }
      }),
    )
  }, [applyRun, resolveRefresh])

  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
      stopPolling()
      // Resolve a pending refreshIndex so its caller is not left hanging.
      finishRef.current?.()
      finishRef.current = null
    }
  }, [stopPolling])

  useEffect(() => {
    pollRef.current = setInterval(() => {
      void pollActiveRuns()
    }, INDEX_POLL_INTERVAL_MS)
    return () => {
      stopPolling()
    }
  }, [pollActiveRuns, stopPolling])

  const refreshIndex = useCallback(
    async (repoId: number): Promise<void> => {
      if (refreshingRef.current !== null) return

      errorSourceRef.current = null
      setError(null)
      setExhausted(false)
      attemptsRef.current[repoId] = 0
      stoppedIdsRef.current.delete(repoId)
      refreshingRef.current = repoId
      setRefreshingId(repoId)

      try {
        const accepted = await startIndex(repoId)
        if (!mountedRef.current) return
        applyRun(repoId, {
          runId: accepted.runId,
          commit: accepted.commit,
          status: accepted.status,
          startedAt: null,
          finishedAt: null,
        })
        if (!isActiveIndexRun(accepted)) return

        await new Promise<void>((resolve) => {
          finishRef.current = resolve
        })
      } catch (caught) {
        if (mountedRef.current) {
          errorSourceRef.current = "other"
          setError(caught)
        }
        // 409 means another tab/admin already started the run: follow it
        // instead of leaving the page stuck on the stale state.
        if (isIndexInProgress(caught)) void loadIndexStatus()
      } finally {
        finishRef.current = null
        settle()
      }
    },
    [applyRun, loadIndexStatus, settle],
  )

  return {
    indexById,
    refreshingId,
    exhausted,
    error,
    clearError,
    loadIndexStatus,
    refreshIndex,
  }
}
