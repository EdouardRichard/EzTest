/** Shared DTOs for the P8 version knowledge API (spec §9.4/§11.1). */

export type KnowledgeKind = "pattern" | "pitfall" | "decision" | "case_idea"
export type KnowledgeStatus = "draft" | "approved" | "rejected"
export type KnowledgeConfidence = "high" | "medium" | "low"

export const KNOWLEDGE_KINDS: readonly { value: KnowledgeKind; label: string }[] = [
  { value: "pattern", label: "模式" },
  { value: "pitfall", label: "陷阱" },
  { value: "decision", label: "决策" },
  { value: "case_idea", label: "用例灵感" },
]

export const KNOWLEDGE_CONFIDENCES: readonly { value: KnowledgeConfidence; label: string }[] = [
  { value: "high", label: "高" },
  { value: "medium", label: "中" },
  { value: "low", label: "低" },
]

export const KNOWLEDGE_STATUSES: readonly { value: KnowledgeStatus; label: string }[] = [
  { value: "draft", label: "草稿" },
  { value: "approved", label: "已批准" },
  { value: "rejected", label: "已驳回" },
]

/**
 * One source reference. The referenced id is a P3 Snowflake serialized as a
 * decimal string and must never be converted with `Number()`.
 */
export type KnowledgeSourceRef = {
  kind: string
  id: string
}

/** `KnowledgeEntryDTO` of spec §11.1. */
export type KnowledgeEntry = {
  id: string
  versionId: string
  versionName?: string
  kind: KnowledgeKind
  title: string
  body: string
  tags: string[]
  sourceRefs: KnowledgeSourceRef[]
  status: KnowledgeStatus
  confidence: KnowledgeConfidence
  createdBy: number
  reviewedBy: number | null
  reviewNote: string
  revision: number
  createdAt: string
  updatedAt: string
}

export type KnowledgeQuery = {
  status?: KnowledgeStatus
  kind?: KnowledgeKind
  /** Decimal P3 version id (never a number). */
  versionId?: string
  limit?: number
  cursor?: string
}

export type CreateKnowledgeBody = {
  kind: KnowledgeKind
  title: string
  body: string
  confidence?: KnowledgeConfidence
  tags?: string[]
  sourceRefs?: KnowledgeSourceRef[]
}

export type UpdateKnowledgeBody = {
  revision: number
  kind?: KnowledgeKind
  title?: string
  body?: string
  confidence?: KnowledgeConfidence
  tags?: string[]
  sourceRefs?: KnowledgeSourceRef[]
}

export type ReviewKnowledgeBody = {
  revision: number
  status: "approved" | "rejected"
  /** Mandatory when status is rejected. */
  reason?: string
}

export type KnowledgePageResult = {
  items: KnowledgeEntry[]
  nextCursor: string
  hasMore: boolean
}

/** Labels the API returns for display; unknown values fall back to the raw code. */
export function knowledgeKindLabel(kind: string): string {
  return KNOWLEDGE_KINDS.find((item) => item.value === kind)?.label ?? kind
}

export function knowledgeConfidenceLabel(confidence: string): string {
  return KNOWLEDGE_CONFIDENCES.find((item) => item.value === confidence)?.label ?? confidence
}

export function knowledgeStatusLabel(status: string): string {
  return KNOWLEDGE_STATUSES.find((item) => item.value === status)?.label ?? status
}
