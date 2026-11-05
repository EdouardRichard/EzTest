import { type ApiPage, apiGet, apiGetPage, apiPatch, apiPost } from "@/lib/api-client"

import type {
  CreateKnowledgeInput,
  KnowledgeDetailView,
  KnowledgeItemView,
  KnowledgeListOptions,
  KnowledgeRevisionView,
  KnowledgeStatus,
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

/* ---------------------------- structured search --------------------------- */

/** Structured SUT binding projection returned by the search read model. */
export type KnowledgeSearchBindings = {
  system_id: string | null
  environment_id: string | null
  page_key: string | null
  api_key: string | null
  account_role: string | null
}

/**
 * `GET /api/knowledge/search` item: the frozen `KnowledgeItemView` plus the
 * structured bindings and the last verification timestamp. The search is
 * approved-only by default and never exposes a credential field.
 */
export type KnowledgeSearchResult = KnowledgeItemView & {
  last_verified_at: string | null
  bindings: KnowledgeSearchBindings
}

/** Optional filters accepted by `GET /api/knowledge/search`. */
export type KnowledgeSearchOptions = {
  /** Free-text query under the retrieval text bound (max 512 runes). */
  q?: string
  status?: KnowledgeStatus
  systemId?: string
  environmentId?: string
  pageKey?: string
  apiKey?: string
  accountRole?: string
  limit?: number
}

/**
 * GET /api/knowledge/search — the 0.2.1 structured search. `status` defaults to
 * `approved` on the server, so unpublished knowledge never leaks into the
 * result unless a reviewer explicitly asks for another state.
 */
export function searchKnowledge(
  options: KnowledgeSearchOptions = {},
  cursor = "",
): Promise<ApiPage<KnowledgeSearchResult>> {
  return apiGetPage<KnowledgeSearchResult>(
    `/api/knowledge/search${query({
      q: options.q,
      status: options.status,
      system_id: options.systemId,
      environment_id: options.environmentId,
      page_key: options.pageKey,
      api_key: options.apiKey,
      account_role: options.accountRole,
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
