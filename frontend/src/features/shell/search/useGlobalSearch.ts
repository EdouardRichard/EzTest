"use client"

import { useCallback, useEffect, useRef, useState } from "react"

import { ApiError, apiGet } from "@/lib/api-client"

/** Debounce applied to every keystroke before a search request (spec §6.2). */
export const SEARCH_DEBOUNCE_MS = 300

/** localStorage key of the recent-search list (spec §6.2). */
export const RECENT_SEARCHES_KEY = "eztest.recentSearches"

/** The recent-search list keeps at most five terms (spec §6.2). */
export const MAX_RECENT_SEARCHES = 5

/** Per-type result cap sent to `GET /api/search` (spec §6.2). */
export const SEARCH_LIMIT = 5

/** Search result types rendered by the command palette (spec §6.2). */
export const SEARCH_TYPES = ["version", "requirement", "case", "issue"] as const

export type SearchType = (typeof SEARCH_TYPES)[number]

/** One global-search result returned by `GET /api/search` (spec §6.2). */
export type SearchItem = {
  id: string
  title: string
  subtitle: string
  versionId: string
  versionName: string
  status: string
  url: string
}

export type SearchGroup = {
  type: SearchType
  items: SearchItem[]
}

export type SearchSnapshot = {
  groups: SearchGroup[]
  total: number
  truncated: boolean
  degraded: boolean
}

/** Empty result used before the first request and for a cleared query. */
export const EMPTY_SEARCH_SNAPSHOT: SearchSnapshot = {
  groups: [],
  total: 0,
  truncated: false,
  degraded: false,
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

function text(value: unknown): string {
  return typeof value === "string" ? value : ""
}

function isSearchType(value: unknown): value is SearchType {
  return typeof value === "string" && (SEARCH_TYPES as readonly string[]).includes(value)
}

/**
 * Normalizes the `{"data":{"groups",...}}` payload so a missing or partial field
 * never renders `undefined`: unknown group types and malformed items are
 * dropped, and a missing total falls back to the item count.
 */
export function normalizeSearchSnapshot(payload: unknown): SearchSnapshot {
  if (!isRecord(payload)) return EMPTY_SEARCH_SNAPSHOT

  const rawGroups = Array.isArray(payload.groups) ? payload.groups : []
  const groups: SearchGroup[] = []
  let itemCount = 0
  for (const rawGroup of rawGroups) {
    if (!isRecord(rawGroup) || !isSearchType(rawGroup.type)) continue
    const rawItems = Array.isArray(rawGroup.items) ? rawGroup.items : []
    const items: SearchItem[] = []
    for (const rawItem of rawItems) {
      if (!isRecord(rawItem)) continue
      items.push({
        id: text(rawItem.id),
        title: text(rawItem.title),
        subtitle: text(rawItem.subtitle),
        versionId: text(rawItem.versionId),
        versionName: text(rawItem.versionName),
        status: text(rawItem.status),
        url: text(rawItem.url),
      })
    }
    itemCount += items.length
    groups.push({ type: rawGroup.type, items })
  }

  const rawTotal = payload.total
  const total =
    typeof rawTotal === "number" && Number.isFinite(rawTotal) && rawTotal >= 0
      ? Math.trunc(rawTotal)
      : itemCount

  return {
    groups,
    total,
    truncated: payload.truncated === true,
    degraded: payload.degraded === true,
  }
}

/**
 * Builds the search request path. The default filter requests every type and the
 * documented per-type limit.
 */
export function buildSearchPath(
  query: string,
  types: readonly SearchType[] = SEARCH_TYPES,
  limit: number = SEARCH_LIMIT,
): string {
  const params = new URLSearchParams()
  params.set("q", query)
  params.set("types", types.join(","))
  params.set("limit", String(limit))
  return `/api/search?${params.toString()}`
}

/** Fetches and normalizes one global-search snapshot. */
export async function searchGlobal(
  query: string,
  options: {
    types?: readonly SearchType[]
    limit?: number
    signal?: AbortSignal
  } = {},
): Promise<SearchSnapshot> {
  const path = buildSearchPath(query, options.types, options.limit)
  const payload = await apiGet<unknown>(path, { signal: options.signal })
  return normalizeSearchSnapshot(payload)
}

/** User-facing message of a failed search request. */
export function describeSearchError(error: unknown): string {
  if (!(error instanceof ApiError)) {
    return error instanceof Error ? error.message : "搜索失败"
  }
  if (error.status === 0) return "无法连接 API 服务，请确认后端已启动。"
  if (error.status === 401) return "登录状态已失效，请重新登录。"
  return error.message.length > 0 ? error.message : "搜索失败"
}

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

  // biome-ignore lint/correctness/useExhaustiveDependencies: typesParam/limit are the primitive request-shape keys; options object identity must not retrigger
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
