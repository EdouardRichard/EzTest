import { type ApiPage, apiGetPage, apiPatch, apiPost } from "@/lib/api-client"

import type {
  CreateKnowledgeBody,
  KnowledgeEntry,
  KnowledgeKind,
  KnowledgePageResult,
  KnowledgeQuery,
  KnowledgeSourceRef,
  KnowledgeStatus,
  ReviewKnowledgeBody,
  UpdateKnowledgeBody,
} from "./types"

const VERSIONS_PATH = "/api/versions"
const GLOBAL_PATH = "/api/knowledge"
const PAGE_LIMIT = 50

const KINDS: readonly KnowledgeKind[] = ["pattern", "pitfall", "decision", "case_idea"]
const STATUSES: readonly KnowledgeStatus[] = ["draft", "approved", "rejected"]
const CONFIDENCES = ["high", "medium", "low"] as const

/** Path for one version's knowledge collection; the id stays a decimal string. */
function versionKnowledgePath(versionId: string): string {
  return `${VERSIONS_PATH}/${versionId}/knowledge`
}

/** Path for one knowledge entry; both ids stay decimal strings. */
function knowledgeEntryPath(versionId: string, knowledgeId: string): string {
  return `${versionKnowledgePath(versionId)}/${knowledgeId}`
}

function withQuery(path: string, query: URLSearchParams): string {
  const suffix = query.toString()
  return suffix === "" ? path : `${path}?${suffix}`
}

function text(value: unknown): string {
  return typeof value === "string" ? value : ""
}

function numberValue(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) ? value : 0
}

function optionalNumber(value: unknown): number | null {
  return typeof value === "number" && Number.isFinite(value) ? value : null
}

function stringArray(value: unknown): string[] {
  return Array.isArray(value)
    ? value.filter((item): item is string => typeof item === "string")
    : []
}

function sourceRefs(value: unknown): KnowledgeSourceRef[] {
  if (!Array.isArray(value)) return []
  const refs: KnowledgeSourceRef[] = []
  for (const raw of value) {
    if (typeof raw !== "object" || raw === null || Array.isArray(raw)) continue
    const record = raw as Record<string, unknown>
    const id = record.id
    if (typeof id !== "string" && typeof id !== "number") continue
    refs.push({ kind: text(record.kind), id: String(id) })
  }
  return refs
}

/**
 * Normalizes one entry so the UI never renders `undefined` and never re-parses a
 * P3 id through `Number()`. Unknown enums fall back to the stable draft/pattern
 * defaults instead of breaking the list.
 */
export function normalizeKnowledgeEntry(raw: unknown): KnowledgeEntry {
  const record = (
    typeof raw === "object" && raw !== null && !Array.isArray(raw) ? raw : {}
  ) as Record<string, unknown>
  const kind =
    typeof record.kind === "string" && KINDS.includes(record.kind as KnowledgeKind)
      ? (record.kind as KnowledgeKind)
      : "pattern"
  const status =
    typeof record.status === "string" && STATUSES.includes(record.status as KnowledgeStatus)
      ? (record.status as KnowledgeStatus)
      : "draft"
  const confidence =
    typeof record.confidence === "string" &&
    (CONFIDENCES as readonly string[]).includes(record.confidence)
      ? (record.confidence as (typeof CONFIDENCES)[number])
      : "medium"
  return {
    id: text(record.id),
    versionId: text(record.versionId),
    versionName: text(record.versionName),
    kind,
    title: text(record.title),
    body: text(record.body),
    tags: stringArray(record.tags),
    sourceRefs: sourceRefs(record.sourceRefs),
    status,
    confidence,
    createdBy: numberValue(record.createdBy),
    reviewedBy: optionalNumber(record.reviewedBy),
    reviewNote: text(record.reviewNote),
    revision: numberValue(record.revision),
    createdAt: text(record.createdAt),
    updatedAt: text(record.updatedAt),
  }
}

/** Normalizes one entry page payload (defensive against partial responses). */
export function normalizeKnowledgeEntries(payload: unknown): KnowledgeEntry[] {
  if (!Array.isArray(payload)) return []
  return payload
    .filter(
      (raw): raw is Record<string, unknown> =>
        typeof raw === "object" && raw !== null && !Array.isArray(raw),
    )
    .map(normalizeKnowledgeEntry)
}

function readPage(page: ApiPage<unknown>): KnowledgePageResult {
  return {
    items: normalizeKnowledgeEntries(page.items),
    nextCursor: page.nextCursor,
    hasMore: page.hasMore,
  }
}

/** GET /api/versions/{id}/knowledge — one cursor page, optional status/kind. */
export async function listVersionKnowledge(
  versionId: string,
  query: KnowledgeQuery = {},
): Promise<KnowledgePageResult> {
  const params = new URLSearchParams()
  if (query.status !== undefined) params.set("status", query.status)
  if (query.kind !== undefined) params.set("kind", query.kind)
  if (query.limit !== undefined) params.set("limit", String(query.limit))
  if (query.cursor !== undefined && query.cursor !== "") params.set("cursor", query.cursor)
  return readPage(await apiGetPage<unknown>(withQuery(versionKnowledgePath(versionId), params)))
}

/**
 * GET /api/knowledge — the global read. The backend defaults to approved; a
 * draft/rejected queue requires an administrator.
 */
export async function listGlobalKnowledge(
  query: KnowledgeQuery = {},
): Promise<KnowledgePageResult> {
  const params = new URLSearchParams()
  params.set("status", query.status ?? "approved")
  if (query.versionId !== undefined && query.versionId !== "") {
    params.set("versionId", query.versionId)
  }
  if (query.kind !== undefined) params.set("kind", query.kind)
  params.set("limit", String(query.limit ?? PAGE_LIMIT))
  if (query.cursor !== undefined && query.cursor !== "") params.set("cursor", query.cursor)
  return readPage(await apiGetPage<unknown>(withQuery(GLOBAL_PATH, params)))
}

/** POST /api/versions/{id}/knowledge — 201 with the created draft. */
export function createKnowledge(
  versionId: string,
  body: CreateKnowledgeBody,
): Promise<KnowledgeEntry> {
  return apiPost<unknown>(versionKnowledgePath(versionId), body).then(normalizeKnowledgeEntry)
}

/** PATCH /api/versions/{id}/knowledge/{kid} — creator/admin, revision CAS. */
export function updateKnowledge(
  versionId: string,
  knowledgeId: string,
  body: UpdateKnowledgeBody,
): Promise<KnowledgeEntry> {
  return apiPatch<unknown>(knowledgeEntryPath(versionId, knowledgeId), body).then(
    normalizeKnowledgeEntry,
  )
}

/** POST .../knowledge/{kid}/review — admin approve/reject. */
export function reviewKnowledge(
  versionId: string,
  knowledgeId: string,
  body: ReviewKnowledgeBody,
): Promise<KnowledgeEntry> {
  return apiPost<unknown>(`${knowledgeEntryPath(versionId, knowledgeId)}/review`, body).then(
    normalizeKnowledgeEntry,
  )
}
