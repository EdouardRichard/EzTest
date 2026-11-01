import type { BadgeTone } from "@/components/ui/Badge"

/**
 * Wire types for the P7 cross-version knowledge page (spec §6.1/§10).
 *
 * Field names and enum values are pinned by the frozen P7 contract
 * (`docs/version-V0.2/contracts/openapi.yaml`): IDs are decimal strings,
 * timestamps are RFC3339 UTC, list endpoints use `{data, next_cursor,
 * has_more}` and detail endpoints use `{data: ...}`. `source_stale` is computed
 * at read time and is never a status.
 */

/* ---------------------------------- enums ---------------------------------- */

/** `scope=global` is visible to every version; `system` matches a SUT family. */
export type KnowledgeScope = "global" | "system"

/** Review state machine: draft → approved|rejected; approved → revoked; revoked → approved. */
export type KnowledgeStatus = "draft" | "approved" | "rejected" | "revoked"

/* ---------------------------------- views ---------------------------------- */

/** `KnowledgeItemView` (list row and every mutation response). */
export type KnowledgeItemView = {
  id: string
  scope: KnowledgeScope
  system_key: string | null
  title: string
  content: string
  tags: string[]
  status: KnowledgeStatus
  confidence: number | null
  source_version_id: string | null
  promoted_from_version_id: string | null
  revision: number
  review_reason: string | null
  reviewed_by: string | null
  reviewed_at: string | null
  revoked_at: string | null
  /** Read-time marker: the source version no longer exists; excluded from injection. */
  source_stale: boolean
  created_by: string | null
  created_at: string
  updated_at: string
}

/** `KnowledgeRevisionView`: one append-only content history entry. */
export type KnowledgeRevisionView = {
  id: string
  knowledge_id: string
  revision: number
  title: string
  content: string
  tags: string[]
  changed_by: string | null
  created_at: string
}

/** `GET /api/knowledge/items/{id}`: the item plus its revisions. */
export type KnowledgeDetailView = KnowledgeItemView & {
  revisions: KnowledgeRevisionView[]
}

/* --------------------------------- requests -------------------------------- */

export type KnowledgeListOptions = {
  scope?: KnowledgeScope
  status?: KnowledgeStatus
  /** `system_key` query parameter (only meaningful with `scope=system`). */
  systemKey?: string
  tag?: string
  /** Full-text query matched against title/content/tags (server `ts_rank`). */
  q?: string
  limit?: number
}

/** `POST /api/knowledge/items`: the server always creates the item as a draft. */
export type CreateKnowledgeInput = {
  title: string
  content: string
  scope: KnowledgeScope
  system_key?: string | null
  tags: string[]
  source_version_id?: string
  promoted_from_version_id?: string
}

/** `PATCH /api/knowledge/items/{id}`: omitted fields keep their stored value. */
export type UpdateKnowledgeInput = {
  title?: string
  content?: string
  tags?: string[]
  scope?: KnowledgeScope
  system_key?: string | null
}

/* ------------------------------- UI metadata ------------------------------- */

export const KNOWLEDGE_SCOPE_LABELS: Record<KnowledgeScope, string> = {
  global: "全局",
  system: "系统",
}

export const KNOWLEDGE_STATUS_LABELS: Record<KnowledgeStatus, string> = {
  draft: "草稿",
  approved: "已通过",
  rejected: "已驳回",
  revoked: "已撤回",
}

export const KNOWLEDGE_STATUS_TONES: Record<KnowledgeStatus, BadgeTone> = {
  draft: "warning",
  approved: "success",
  rejected: "danger",
  revoked: "info",
}

/** Human scope label including the system key (`系统 · order-center`). */
export function knowledgeScopeLabel(item: Pick<KnowledgeItemView, "scope" | "system_key">): string {
  if (item.scope === "system" && item.system_key !== null && item.system_key.length > 0) {
    return `${KNOWLEDGE_SCOPE_LABELS.system} · ${item.system_key}`
  }
  return KNOWLEDGE_SCOPE_LABELS[item.scope]
}

/**
 * Spec §6.1: only a draft/rejected item can be edited and only by its author
 * or an administrator. The server enforces the same rule; this only decides
 * whether the UI offers the editor.
 */
export function canEditKnowledge(
  item: Pick<KnowledgeItemView, "status" | "created_by">,
  currentUserId: string,
  isAdmin: boolean,
): boolean {
  if (item.status !== "draft" && item.status !== "rejected") return false
  if (isAdmin) return true
  return currentUserId.length > 0 && item.created_by === currentUserId
}
