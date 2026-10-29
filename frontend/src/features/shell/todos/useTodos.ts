"use client"

import { usePathname } from "next/navigation"
import { useCallback, useEffect, useRef, useState } from "react"

import { ApiError, apiGet, apiGetPage } from "@/lib/api-client"

/** Todo groups of the P8 todo centre (spec §6.3). */
export type TodoKind = "review" | "takeover" | "gate"

/** One todo-centre item returned by `GET /api/todos` (spec §6.3). */
export type TodoItem = {
  kind: TodoKind
  title: string
  versionId: string
  versionName: string
  severity: string
  createdAt: string
  url: string
}

export type TodoCounts = {
  review: number
  takeover: number
  gate: number
}

export type TodoSnapshot = {
  items: TodoItem[]
  counts: TodoCounts
  /**
   * True when the admin knowledge-review queue page reported `has_more`: the
   * review count is then a lower bound of the capped page, not a total.
   */
  knowledgeTruncated?: boolean
}

/** 30 s refresh cadence of the topbar todo centre (spec §6.3). */
export const TODOS_POLL_INTERVAL_MS = 30_000

const TODOS_PATH = "/api/todos?limit=50"

/** Number of draft knowledge entries merged into one admin snapshot. */
export const KNOWLEDGE_QUEUE_LIMIT = 50

/**
 * Admin knowledge-review queue (spec §9.4): draft knowledge entries belong to
 * the review group. The route is admin-guarded, so it is only requested for an
 * administrator. The page envelope's `has_more` is kept so the merged review
 * count is documented as a lower bound when the cap is reached.
 */
const KNOWLEDGE_QUEUE_PATH = `/api/knowledge?status=draft&limit=${KNOWLEDGE_QUEUE_LIMIT}`

const EMPTY_SNAPSHOT: TodoSnapshot = {
  items: [],
  counts: { review: 0, takeover: 0, gate: 0 },
}

const TODO_KINDS: readonly TodoKind[] = ["review", "takeover", "gate"]

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

function text(value: unknown): string {
  return typeof value === "string" ? value : ""
}

function count(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) && value >= 0 ? value : 0
}

function isTodoKind(value: unknown): value is TodoKind {
  return typeof value === "string" && (TODO_KINDS as readonly string[]).includes(value)
}

/**
 * Normalizes the `{"data":{"items","counts"}}` payload so a missing or partial
 * field never renders `undefined`: unknown kinds are dropped and missing counts
 * fall back to the item totals.
 */
export function normalizeTodoSnapshot(payload: unknown): TodoSnapshot {
  if (!isRecord(payload)) return EMPTY_SNAPSHOT

  const rawItems = Array.isArray(payload.items) ? payload.items : []
  const items: TodoItem[] = []
  for (const raw of rawItems) {
    if (!isRecord(raw) || !isTodoKind(raw.kind)) continue
    items.push({
      kind: raw.kind,
      title: text(raw.title),
      versionId: text(raw.versionId),
      versionName: text(raw.versionName),
      severity: text(raw.severity),
      createdAt: text(raw.createdAt),
      url: text(raw.url),
    })
  }

  const rawCounts = isRecord(payload.counts) ? payload.counts : {}
  const derived = { review: 0, takeover: 0, gate: 0 }
  for (const item of items) derived[item.kind]++
  const counts: TodoCounts = {
    review: rawCounts.review === undefined ? derived.review : count(rawCounts.review),
    takeover: rawCounts.takeover === undefined ? derived.takeover : count(rawCounts.takeover),
    gate: rawCounts.gate === undefined ? derived.gate : count(rawCounts.gate),
  }
  return { items, counts }
}

function describeError(error: unknown): string {
  if (!(error instanceof ApiError)) {
    return error instanceof Error ? error.message : "待办加载失败"
  }
  if (error.status === 0) return "无法连接 API 服务，请确认后端已启动。"
  if (error.status === 401) return "登录状态已失效，请重新登录。"
  return error.message
}

/**
 * Normalizes the admin draft-knowledge read into review-group todo items. The
 * entry id is a decimal string and is never converted with `Number()`; the deep
 * link opens the review drawer focused on that entry.
 */
export function normalizeKnowledgeTodos(payload: unknown): TodoItem[] {
  if (!Array.isArray(payload)) return []
  const items: TodoItem[] = []
  for (const raw of payload) {
    if (!isRecord(raw)) continue
    const id = text(raw.id)
    if (id === "") continue
    const versionId = text(raw.versionId)
    items.push({
      kind: "review",
      title: `知识审核：${text(raw.title) || id}`,
      versionId,
      versionName: text(raw.versionName),
      severity: "info",
      createdAt: text(raw.createdAt),
      url: `/knowledge?review=${id}`,
    })
  }
  return items
}

/**
 * Merges the admin knowledge-review drafts into the review group: one item per
 * distinct draft (deduped by kind/url so a repeated read or a version todo that
 * already links the same entry is not counted twice), and the review count
 * includes them so the topbar badge and the group badge agree. `truncated`
 * records that the queue page reached its cap, making the count a lower bound.
 */
export function mergeKnowledgeTodos(
  snapshot: TodoSnapshot,
  knowledge: TodoItem[],
  truncated = false,
): TodoSnapshot {
  const seen = new Set(snapshot.items.map((item) => `${item.kind}\u0000${item.url}`))
  const fresh: TodoItem[] = []
  for (const item of knowledge) {
    const key = `${item.kind}\u0000${item.url}`
    if (seen.has(key)) continue
    seen.add(key)
    fresh.push(item)
  }
  const knowledgeTruncated = snapshot.knowledgeTruncated === true || truncated
  if (fresh.length === 0 && !knowledgeTruncated) return snapshot
  return {
    items: [...snapshot.items, ...fresh],
    counts: {
      ...snapshot.counts,
      review: snapshot.counts.review + fresh.length,
    },
    knowledgeTruncated,
  }
}

/**
 * Fetches and normalizes one todo-centre snapshot. An administrator additionally
 * reads the draft knowledge queue; a failure of that second read only omits the
 * knowledge items so the shell never loses the version todos. The queue is read
 * through the page envelope so `has_more` can be recorded instead of silently
 * undercounting drafts beyond the cap.
 */
export async function fetchTodos(admin = false): Promise<TodoSnapshot> {
  const snapshot = normalizeTodoSnapshot(await apiGet<unknown>(TODOS_PATH))
  if (!admin) return snapshot
  try {
    const page = await apiGetPage<unknown>(KNOWLEDGE_QUEUE_PATH)
    return mergeKnowledgeTodos(snapshot, normalizeKnowledgeTodos(page.items), page.hasMore)
  } catch {
    return snapshot
  }
}

export type UseTodosResult = {
  todos: TodoSnapshot
  loading: boolean
  error: string
  /** Refetches immediately (the panel's retry action). */
  refresh: () => Promise<void>
}

/**
 * Loads the todo centre, refreshes on every route change and polls every 30 s
 * (spec §6.3). An administrator also gets the draft knowledge queue in the
 * review group (spec §9.4). A failed read only sets `error`: the topbar shell
 * keeps rendering and the panel offers a retry.
 */
export function useTodos(admin = false): UseTodosResult {
  const pathname = usePathname() ?? ""
  const [todos, setTodos] = useState<TodoSnapshot>(EMPTY_SNAPSHOT)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState("")
  const requestRef = useRef(0)

  const load = useCallback(
    async (showLoading: boolean): Promise<void> => {
      const requestId = ++requestRef.current
      if (showLoading) setLoading(true)
      try {
        const snapshot = await fetchTodos(admin)
        if (requestRef.current !== requestId) return
        setTodos(snapshot)
        setError("")
      } catch (caught) {
        if (requestRef.current !== requestId) return
        setError(describeError(caught))
      } finally {
        if (requestRef.current === requestId) setLoading(false)
      }
    },
    [admin],
  )

  // A route change is an explicit refresh trigger (spec §6.3).
  // biome-ignore lint/correctness/useExhaustiveDependencies: pathname is the route-change trigger; load itself is pathname-independent
  useEffect(() => {
    void load(true)
  }, [pathname, load])

  useEffect(() => {
    const interval = window.setInterval(() => {
      void load(false)
    }, TODOS_POLL_INTERVAL_MS)
    return () => window.clearInterval(interval)
  }, [load])

  const refresh = useCallback(() => load(true), [load])
  return { todos, loading, error, refresh }
}
