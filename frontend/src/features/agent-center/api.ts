import { apiGet, apiGetPage, apiPatch, apiPost, apiPut } from "@/lib/api-client"

import type {
  AgentCatalog,
  AgentCenterQuality,
  AgentRunView,
  AgentTool,
  LLMModel,
  ProfileDetail,
  ProfileView,
  PromptMutation,
  PromptRevision,
} from "./types"

const RUN_PAGE_LIMIT = 20

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

function profilePath(key: string): string {
  return `/api/agents/profiles/${encodeURIComponent(key)}`
}

/** GET /api/agent-center/catalog — logged-in read-only catalog/stats. */
export function getAgentCatalog(): Promise<AgentCatalog> {
  return apiGet<AgentCatalog>("/api/agent-center/catalog")
}

export type AgentRunFilters = {
  profileKey?: string
  status?: string
  versionId?: string
  before?: string
  limit?: number
}

/** GET /api/agent-center/runs — before+limit pages, newest run first. */
export async function listAgentRuns(filters: AgentRunFilters = {}) {
  return apiGetPage<AgentRunView>(
    `/api/agent-center/runs${query({
      profile_key: filters.profileKey,
      status: filters.status,
      version_id: filters.versionId,
      before: filters.before,
      limit: filters.limit ?? RUN_PAGE_LIMIT,
    })}`,
  )
}

/** GET /api/agent-center/quality — latest eval summary + global run stats. */
export function getAgentCenterQuality(): Promise<AgentCenterQuality> {
  return apiGet<AgentCenterQuality>("/api/agent-center/quality")
}

/* ---------------------------------- admin --------------------------------- */

/** GET /api/agents/profiles — admin profile list including the prompt body. */
export async function listAgentProfiles(): Promise<ProfileView[]> {
  const payload = await apiGet<unknown>("/api/agents/profiles")
  return Array.isArray(payload) ? (payload as ProfileView[]) : []
}

/** GET /api/agents/profiles/{key} — admin detail + default prompt + variables. */
export function getAgentProfile(key: string): Promise<ProfileDetail> {
  return apiGet<ProfileDetail>(profilePath(key))
}

export type ProfilePatchBody = {
  model?: string | null
  budgets?: { max_turns: number; soft_seconds: number; hard_seconds: number }
  allowed_tools?: string[]
}

/** PATCH /api/agents/profiles/{key} — config only; prompt uses the version API. */
export function patchAgentProfile(key: string, body: ProfilePatchBody): Promise<ProfileView> {
  return apiPatch<ProfileView>(profilePath(key), body)
}

export type SavePromptBody = {
  system_prompt: string
  note?: string
  expected_version?: number
}

/** PUT /api/agents/profiles/{key}/prompt — append a prompt version. */
export function saveAgentPrompt(key: string, body: SavePromptBody): Promise<PromptMutation> {
  return apiPut<PromptMutation>(`${profilePath(key)}/prompt`, body)
}

/** POST /api/agents/profiles/{key}/prompt/reset — append the default body. */
export function resetAgentPrompt(key: string): Promise<PromptMutation> {
  return apiPost<PromptMutation>(`${profilePath(key)}/prompt/reset`)
}

/** POST /api/agents/profiles/{key}/prompt/restore — append an older version. */
export function restoreAgentPrompt(key: string, version: number): Promise<PromptMutation> {
  return apiPost<PromptMutation>(`${profilePath(key)}/prompt/restore`, { version })
}

/** GET /api/agents/profiles/{key}/prompt/revisions — version DESC pages. */
export async function listPromptRevisions(
  key: string,
  options: { limit?: number; beforeVersion?: number } = {},
) {
  return apiGetPage<PromptRevision>(
    `${profilePath(key)}/prompt/revisions${query({
      limit: options.limit ?? 20,
      before_version: options.beforeVersion,
    })}`,
  )
}

export type PreviewPromptBody = {
  system_prompt?: string
  sample?: Record<string, string>
}

/** POST /api/agents/profiles/{key}/prompt/preview — render with sample values. */
export function previewAgentPrompt(
  key: string,
  body: PreviewPromptBody,
): Promise<{ rendered: string }> {
  return apiPost<{ rendered: string }>(`${profilePath(key)}/prompt/preview`, body)
}

/** GET /api/agents/tools — read-only P5 registry catalog (no execution). */
export async function listAgentTools(): Promise<AgentTool[]> {
  const payload = await apiGet<unknown>("/api/agents/tools")
  return Array.isArray(payload) ? (payload as AgentTool[]) : []
}

/**
 * GET /api/admin/llm/models — default LLM model catalog for the profile model
 * dropdown. It is an existing P0 admin endpoint; failures degrade to the
 * profile's current model only.
 */
export async function listLLMModels(): Promise<LLMModel[]> {
  const page = await apiGetPage<LLMModel>("/api/admin/llm/models?limit=100")
  return page.items
}
