import type { Conversation, ConversationSummary } from "@/features/clarification/types"
import { apiDownload, apiGet, apiGetPage, apiPost } from "@/lib/api-client"

import type {
  CaseConfirmationInput,
  CaseConfirmationResult,
  CaseRevisionRollbackView,
  DesignCaseDetail,
  DesignGraph,
  DesignOverview,
  ExportFormat,
  ExportRequest,
  ExportView,
  JudgmentOverrideView,
  JudgmentReportView,
  StageView,
} from "./types"

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

function versionPath(versionId: string): string {
  return `/api/versions/${encodeURIComponent(versionId)}`
}

/* --------------------------------- conversations ---------------------------- */

/** Lists design conversations newest first; the latest one is the active round. */
export async function listDesignConversations(versionId: string): Promise<ConversationSummary[]> {
  const page = await apiGetPage<ConversationSummary>(
    `${versionPath(versionId)}/conversations${query({ kind: "design", limit: 100 })}`,
  )
  return page.items
}

/** Creates a design conversation; the backend derives the default title. */
export function createDesignConversation(versionId: string): Promise<Conversation> {
  return apiPost<Conversation>(`${versionPath(versionId)}/conversations`, { kind: "design" })
}

/* ------------------------------- design graph ------------------------------- */

/** Whole-version design graph snapshot; truth lives in the DB (spec §6.3). */
export function getDesignGraph(versionId: string): Promise<DesignGraph> {
  return apiGet<DesignGraph>(`${versionPath(versionId)}/design/graph`)
}

export function getDesignOverview(versionId: string): Promise<DesignOverview> {
  return apiGet<DesignOverview>(`${versionPath(versionId)}/design/overview`)
}

/** One case with its version history; `caseVersionId` selects a historical version. */
export function getDesignCase(
  versionId: string,
  caseId: string,
  caseVersionId?: string,
): Promise<DesignCaseDetail> {
  return apiGet<DesignCaseDetail>(
    `${versionPath(versionId)}/design/cases/${encodeURIComponent(caseId)}${query({
      version_id: caseVersionId,
    })}`,
  )
}

/* -------------------------------- confirmation ------------------------------ */

/** Confirms one case, a whole scenario or a whole requirement (user-only action). */
export function confirmCases(
  versionId: string,
  input: CaseConfirmationInput,
): Promise<CaseConfirmationResult> {
  return apiPost<CaseConfirmationResult>(`${versionPath(versionId)}/design/confirmations`, input)
}

/**
 * Applies the 0.2.1 case hard gate for an explicit selection
 * (`POST /stage/confirm-cases`, spec §5.5/D23): the checked `case_ids` are the
 * only cases confirmed, and the resulting `execute.batch` job carries exactly
 * that list. An empty list means "all current draft cases".
 */
export function confirmStageCases(versionId: string, caseIds: string[]): Promise<StageView> {
  return apiPost<StageView>(`${versionPath(versionId)}/stage/confirm-cases`, {
    case_ids: caseIds,
  })
}

/* ----------------------------- judgment (T16) ------------------------------- */

/**
 * Read-only judgment report (spec SC-E5): one entry per current case with its
 * review_state and latest current-version judge fact. Visible to admin and user
 * (the case_review report is user-facing); the server enforces visibility.
 */
export function listJudgments(
  versionId: string,
  requirementId?: string,
): Promise<JudgmentReportView> {
  return apiGet<JudgmentReportView>(
    `${versionPath(versionId)}/design/judgments${query({ requirement_id: requirementId })}`,
  )
}

/**
 * ADMIN-only human override of an escalated judge verdict (spec SC-E5). The
 * server re-checks the admin role AND the overridable precondition; the client
 * only offers the action for escalated_fail/judge_unavailable. `reason` is
 * mandatory audit metadata.
 */
export function overrideJudgment(
  versionId: string,
  caseId: string,
  reason: string,
): Promise<JudgmentOverrideView> {
  return apiPost<JudgmentOverrideView>(
    `${versionPath(versionId)}/design/judgments/${encodeURIComponent(caseId)}/override`,
    { reason },
  )
}

/* --------------------------- discovery revisions ---------------------------- */

/**
 * API-41: rolls back one auto-confirmed discovery revision (`trigger=discovery_delta`).
 * The backend supersedes the original, creates a confirmed replacement revision
 * (`trigger=rollback`, `rollback_of`) and keeps the case, so a repeat returns
 * the same replacement instead of deleting anything (W2-C11). The optional
 * `reason` is audit metadata.
 */
export function rollbackCaseVersion(
  versionId: string,
  caseVersionId: string,
  reason?: string,
): Promise<CaseRevisionRollbackView> {
  const trimmed = reason?.trim() ?? ""
  return apiPost<CaseRevisionRollbackView>(
    `${versionPath(versionId)}/case-versions/${encodeURIComponent(caseVersionId)}/rollback`,
    trimmed.length > 0 ? { reason: trimmed } : {},
  )
}

/* ---------------------------------- exports --------------------------------- */
/** Creates a whole-version case export; every call creates a new revision. */
export function createExport(versionId: string, input: ExportRequest): Promise<ExportView> {
  return apiPost<ExportView>(`${versionPath(versionId)}/exports`, input)
}

/** Lists previous exports, newest first (id desc). */
export async function listExports(
  versionId: string,
  options: { format?: ExportFormat; limit?: number; before?: string } = {},
): Promise<ExportView[]> {
  const page = await apiGetPage<ExportView>(
    `${versionPath(versionId)}/exports${query({
      format: options.format,
      before: options.before,
      limit: options.limit ?? 20,
    })}`,
  )
  return page.items
}

/** GET /api/exports/{exportId} — one export artifact's metadata (spec §7.4). */
export function getExport(exportId: string): Promise<ExportView> {
  return apiGet<ExportView>(`/api/exports/${encodeURIComponent(exportId)}`)
}

/** Downloads the export bytes through the shared bearer/401 client. */
export function downloadExport(exportId: string): ReturnType<typeof apiDownload> {
  return apiDownload(`/api/exports/${encodeURIComponent(exportId)}/download`)
}

/* ------------------------ clarification → design handoff -------------------- */

export type HandoffRequirement = {
  id: string
  current_version_id: string
  revision: number
  title: string
  user_story: { as_a: string; i_want: string; so_that: string }
  rules: string
  acceptance_criteria: Array<{ id: string; given: string; when: string; then: string }>
  priority: string
  sut_description: string
  scope_out: string
  account_roles_hint: string[]
  status: string
  source_quote: string
  confidence?: number | null
  confirmed_at?: string | null
}

export type HandoffQAOption = { id: string; label: string }

export type HandoffQA = {
  question_id: string
  message_id?: string
  conversation_id?: string
  question_key?: string
  question: string
  options: HandoffQAOption[]
  source_refs: string[]
  requirement_id?: string
  requirement_refs: string[]
  status: string
  answer_selected: string[]
  answer_text: string
  asked_at?: string
  answered_at?: string | null
}

export type HandoffDecision = {
  fact_id: string
  kind: string
  key: string
  summary: string
  confidence?: number | null
  source: string
  source_ref?: string
  created_at?: string
}

export type HandoffOpen = {
  kind: string
  summary: string
  status: string
  source: string
  reference_id?: string
  question_key?: string
  created_at?: string | null
}

export type HandoffPlanItem = {
  id: string
  phase: string
  title: string
  status: string
  decision: string
}

export type HandoffPlan = {
  stage: string
  current?: HandoffPlanItem | null
  next?: HandoffPlanItem | null
}

export type HandoffResources = {
  environment: { id: string; name: string; status: string }
  suts: Array<{
    sut_id: string
    name: string
    base_url: string
    pages: Array<Record<string, unknown>>
    apis: Array<Record<string, unknown>>
    databases: Array<Record<string, unknown>>
  }>
  accounts: Array<Record<string, unknown>>
  repositories: Array<Record<string, unknown>>
  truncated: boolean
}

/** `HandoffView`: the six-block clarification → design handoff document. */
export type HandoffView = {
  version_id: string
  revision: number
  generated_at: string
  requirements: HandoffRequirement[]
  qa: HandoffQA[]
  decisions: HandoffDecision[]
  open: HandoffOpen[]
  plan: HandoffPlan
  resources: HandoffResources
}

/**
 * `GET /api/versions/{id}/handoff[?revision=]`: the newest handoff revision, or
 * the explicitly requested one. All six blocks are always arrays/objects.
 */
export function getVersionHandoff(
  versionId: string,
  revision?: string | number,
): Promise<HandoffView> {
  return apiGet<HandoffView>(
    `${versionPath(versionId)}/handoff${query({ revision: revision === undefined ? undefined : String(revision) })}`,
  )
}
