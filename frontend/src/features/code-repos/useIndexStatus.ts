"use client"

import { useCallback, useEffect, useRef, useState } from "react"

import { getCodeRepos, getLatestIndexRun, startIndex } from "@/features/code-intel/api"
import type { IndexRun } from "@/features/code-intel/types"

/** Poll cadence for a queued/running index run (spec §7.4/§9.2). */
export const INDEX_POLL_INTERVAL_MS = 1500
/** Safety cap from spec §7.4: stop after 60 polls and ask for a manual refresh. */
export const INDEX_POLL_MAX_ATTEMPTS = 60

const ACTIVE_INDEX_STATUSES = new Set(["queued", "running"])

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
 * the list read merges P2 repositories with their latest run, and an explicit
 * refresh starts a run then polls `index-runs/latest` every 1.5s (at most 60
 * times), stopping on done/failed/unmount. Only admins reach the trigger; the
 * backend 403 remains the real guard.
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
  const attemptsRef = useRef(0)

  const clearError = useCallback(() => setError(null), [])

  const stopPolling = useCallback(() => {
    if (pollRef.current !== null) {
      clearInterval(pollRef.current)
      pollRef.current = null
    }
  }, [])

  const settle = useCallback(() => {
    stopPolling()
    refreshingRef.current = null
    if (mountedRef.current) setRefreshingId(null)
  }, [stopPolling])

  const loadIndexStatus = useCallback(async () => {
    try {
      const summaries = await getCodeRepos()
      if (!mountedRef.current) return
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
    } catch (caught) {
      if (mountedRef.current) setError(caught)
    }
  }, [])

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

  const applyRun = useCallback((repoId: number, run: IndexRun) => {
    setIndexById((previous) => ({
      ...previous,
      [repoId]: {
        lastIndexedAt: previous[repoId]?.lastIndexedAt ?? null,
        run,
      },
    }))
  }, [])

  const refreshIndex = useCallback(
    async (repoId: number): Promise<void> => {
      if (refreshingRef.current !== null) return

      setError(null)
      setExhausted(false)
      attemptsRef.current = 0
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
          pollRef.current = setInterval(() => {
            void (async () => {
              if (attemptsRef.current >= INDEX_POLL_MAX_ATTEMPTS) {
                if (mountedRef.current) setExhausted(true)
                resolve()
                return
              }
              attemptsRef.current += 1
              try {
                const run = await getLatestIndexRun(repoId)
                if (!mountedRef.current) {
                  resolve()
                  return
                }
                applyRun(repoId, run)
                if (!isActiveIndexRun(run)) {
                  resolve()
                  return
                }
                // The 60th poll returned a still-active run: stop and offer
                // the manual refresh instead of polling forever.
                if (attemptsRef.current >= INDEX_POLL_MAX_ATTEMPTS) {
                  setExhausted(true)
                  resolve()
                }
              } catch (caught) {
                if (mountedRef.current) setError(caught)
                resolve()
              }
            })()
          }, INDEX_POLL_INTERVAL_MS)
        })
      } catch (caught) {
        if (mountedRef.current) setError(caught)
      } finally {
        finishRef.current = null
        settle()
      }
    },
    [applyRun, settle],
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
