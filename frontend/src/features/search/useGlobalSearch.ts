"use client"

import { useCallback, useEffect, useRef, useState } from "react"

import { describeSearchError, SEARCH_LIMIT, searchGlobal } from "./api"
import { EMPTY_SEARCH_SNAPSHOT, SEARCH_TYPES, type SearchSnapshot, type SearchType } from "./types"

/** Debounce applied to every keystroke before a search request (spec §6). */
export const SEARCH_DEBOUNCE_MS = 300

/** localStorage key of the recent-search list. */
export const RECENT_SEARCHES_KEY = "eztest.recentSearches"

/** The recent-search list keeps at most five terms (spec §6). */
export const MAX_RECENT_SEARCHES = 5

/** Reads the recent-search list; a corrupt payload falls back to an empty list. */
export function readRecentSearches(): string[] {
  if (typeof window === "undefined") return []
  try {
    const parsed: unknown = JSON.parse(window.localStorage.getItem(RECENT_SEARCHES_KEY) ?? "[]")
    if (!Array.isArray(parsed)) return []
    const recent: string[] = []
    for (const raw of parsed) {
      if (typeof raw !== "string") continue
      const term = raw.trim()
      if (term === "" || recent.includes(term)) continue
      recent.push(term)
      if (recent.length === MAX_RECENT_SEARCHES) break
    }
    return recent
  } catch {
    // A corrupt payload must not break the shell: start with an empty list.
    return []
  }
}

function writeRecentSearches(recent: string[]): void {
  if (typeof window === "undefined") return
  try {
    window.localStorage.setItem(RECENT_SEARCHES_KEY, JSON.stringify(recent))
  } catch {
    // Storage can be unavailable (private mode); the command still works.
  }
}

/**
 * Records a submitted/selected search term: it moves to the front, duplicates
 * collapse and the list is capped at five entries.
 */
export function rememberSearch(term: string, current: readonly string[]): string[] {
  const trimmed = term.trim()
  if (trimmed === "") return [...current]
  const next = [trimmed, ...current.filter((candidate) => candidate !== trimmed)].slice(
    0,
    MAX_RECENT_SEARCHES,
  )
  writeRecentSearches(next)
  return next
}

export type UseGlobalSearchResult = {
  snapshot: SearchSnapshot
  loading: boolean
  error: string
  /** Re-runs the current query (the command palette's retry action). */
  retry: () => void
}

/**
 * Runs the debounced, cancellable global search. A blank query performs no
 * request; a newer query aborts the in-flight one and stale responses are
 * ignored. Request failures only set `error`, so the shell stays usable.
 */
export function useGlobalSearch(
  query: string,
  options: { types?: readonly SearchType[]; limit?: number } = {},
): UseGlobalSearchResult {
  const text = query.trim()
  const typesParam = (options.types ?? SEARCH_TYPES).join(",")
  const limit = options.limit ?? SEARCH_LIMIT

  const [snapshot, setSnapshot] = useState<SearchSnapshot>(EMPTY_SEARCH_SNAPSHOT)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState("")
  const [attempt, setAttempt] = useState(0)
  const requestRef = useRef(0)
  const abortRef = useRef<AbortController | null>(null)

  // biome-ignore lint/correctness/useExhaustiveDependencies: `attempt` forces the retry to re-run the request even though the effect body does not read it
  useEffect(() => {
    if (text === "") {
      requestRef.current++
      abortRef.current?.abort()
      abortRef.current = null
      setSnapshot(EMPTY_SEARCH_SNAPSHOT)
      setLoading(false)
      setError("")
      return
    }

    const requestId = requestRef.current + 1
    requestRef.current = requestId
    // A new query cancels the previous request before the debounce window, so a
    // slow response can never overwrite the newer result.
    abortRef.current?.abort()
    abortRef.current = null
    setLoading(true)
    setError("")
    // Drop the previous query's hits while the new one loads: keeping them on
    // screen lets a fast Enter open a stale deep link.
    setSnapshot(EMPTY_SEARCH_SNAPSHOT)

    const timer = window.setTimeout(() => {
      const controller = new AbortController()
      abortRef.current = controller
      const requestedTypes = typesParam.split(",") as SearchType[]
      searchGlobal(text, { types: requestedTypes, limit, signal: controller.signal })
        .then((next) => {
          if (requestRef.current !== requestId) return
          setSnapshot(next)
          setError("")
        })
        .catch((caught: unknown) => {
          if (requestRef.current !== requestId || controller.signal.aborted) return
          setSnapshot(EMPTY_SEARCH_SNAPSHOT)
          setError(describeSearchError(caught))
        })
        .finally(() => {
          if (requestRef.current !== requestId) return
          abortRef.current = null
          setLoading(false)
        })
    }, SEARCH_DEBOUNCE_MS)

    return () => {
      window.clearTimeout(timer)
    }
  }, [text, typesParam, limit, attempt])

  // Abort whatever is still in flight when the palette unmounts.
  useEffect(() => {
    return () => {
      abortRef.current?.abort()
      abortRef.current = null
    }
  }, [])

  const retry = useCallback(() => setAttempt((previous) => previous + 1), [])

  return { snapshot, loading, error, retry }
}
