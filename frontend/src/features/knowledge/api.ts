import { type ApiPage, apiGet, apiGetPage, apiPatch, apiPost } from "@/lib/api-client"

import type {
  CreateKnowledgeInput,
  KnowledgeDetailView,
  KnowledgeItemView,
  KnowledgeListOptions,
  KnowledgeRevisionView,
  UpdateKnowledgeInput,
} from "./types"

const ITEMS_PATH = "/api/knowledge/items"

/** Builds `?a=1&b=2`, skipping empty values (P0/P1 pagination style). */
function query(params: Record<string, string | number | undefined>): string {
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === "") continue
    search.set(key, String(value))
  }
  const text = search.toString()
  return text.length > 0 ? `?${text}` : ""
}

function itemPath(id: string): string {
  return `${ITEMS_PATH}/${encodeURIComponent(id)}`
}

/**
 * GET /api/knowledge/items — login-only list/search ordered by id DESC.
 * `cursor` is the exclusive id cursor of the previous page.
 */
export function listKnowledge(
  options: KnowledgeListOptions = {},
  cursor = "",
): Promise<ApiPage<KnowledgeItemView>> {
  return apiGetPage<KnowledgeItemView>(
    `${ITEMS_PATH}${query({
      scope: options.scope,
      status: options.status,
      system_key: options.systemKey,
      tag: options.tag,
      q: options.q,
      cursor,
      limit: options.limit ?? 20,
    })}`,
  )
}

/** GET /api/knowledge/items/{id} — item fields plus the append-only revisions. */
export function getKnowledge(id: string): Promise<KnowledgeDetailView> {
  return apiGet<KnowledgeDetailView>(itemPath(id))
}

/** POST /api/knowledge/items — creates a draft owned by the current user. */
export function createKnowledge(body: CreateKnowledgeInput): Promise<KnowledgeItemView> {
  return apiPost<KnowledgeItemView>(ITEMS_PATH, body)
}

/** PATCH /api/knowledge/items/{id} — draft/rejected only (author or admin). */
export function updateKnowledge(
  id: string,
  body: UpdateKnowledgeInput,
): Promise<KnowledgeItemView> {
  return apiPatch<KnowledgeItemView>(itemPath(id), body)
}

/** POST .../{id}/approve — admin: draft|revoked → approved. */
export function approveKnowledge(id: string): Promise<KnowledgeItemView> {
  return apiPost<KnowledgeItemView>(`${itemPath(id)}/approve`, {})
}

/** POST .../{id}/reject — admin: draft → rejected; the reason is mandatory. */
export function rejectKnowledge(id: string, reason: string): Promise<KnowledgeItemView> {
  return apiPost<KnowledgeItemView>(`${itemPath(id)}/reject`, { reason })
}

/** POST .../{id}/revoke — admin: approved → revoked; the reason is mandatory. */
export function revokeKnowledge(id: string, reason: string): Promise<KnowledgeItemView> {
  return apiPost<KnowledgeItemView>(`${itemPath(id)}/revoke`, { reason })
}

/** GET .../{id}/revisions — append-only content history, newest first. */
export async function listKnowledgeRevisions(id: string): Promise<KnowledgeRevisionView[]> {
  const page = await apiGetPage<KnowledgeRevisionView>(`${itemPath(id)}/revisions`)
  return page.items
}
