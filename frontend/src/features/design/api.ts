import type { Conversation, ConversationSummary } from "@/features/clarification/types"
import { apiDownload, apiGet, apiGetPage, apiPost } from "@/lib/api-client"

import type {
  CaseConfirmationInput,
  CaseConfirmationResult,
  DesignCaseDetail,
  DesignGraph,
  DesignOverview,
  ExportFormat,
  ExportRequest,
  ExportView,
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

/** Downloads the export bytes through the shared bearer/401 client. */
export function downloadExport(exportId: string): ReturnType<typeof apiDownload> {
  return apiDownload(`/api/exports/${encodeURIComponent(exportId)}/download`)
}
