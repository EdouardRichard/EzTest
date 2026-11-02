import { ApiError, apiGet } from "@/lib/api-client"

import {
  EMPTY_SEARCH_SNAPSHOT,
  SEARCH_TYPES,
  type SearchItem,
  type SearchSnapshot,
  type SearchType,
} from "./types"

/** Per-type result cap sent to `GET /api/search` (spec §4.3.1). */
export const SEARCH_LIMIT = 5

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
 * Normalizes the `{"groups": [...], ...}` payload so a missing or partial field
 * never renders `undefined`: unknown group types and malformed items are
 * dropped, and a missing total falls back to the item count.
 */
export function normalizeSearchSnapshot(payload: unknown): SearchSnapshot {
  if (!isRecord(payload)) return EMPTY_SEARCH_SNAPSHOT

  const rawGroups = Array.isArray(payload.groups) ? payload.groups : []
  const groups: SearchSnapshot["groups"] = []
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
        version_id: text(rawItem.version_id),
        version_title: text(rawItem.version_title),
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
