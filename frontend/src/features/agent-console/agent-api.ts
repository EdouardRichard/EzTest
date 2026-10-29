import { apiDelete, apiGet, apiPatch, apiPost } from "@/lib/api-client"

export type AgentRun = {
  id: number
  session_id: number
  status: "running" | "done" | "failed" | "aborted" | "interrupted"
  terminal_reason: string | null
  turns: number
  input_tokens: number
  output_tokens: number
  reasoning_tokens: number
  abort_code: string | null
  error_code: string | null
  last_tool_name: string | null
  last_tool_pending: boolean
  started_at: string
  ended_at: string | null
}

export type AgentSession = {
  id: number
  profile_id: number
  profile_key: string
  title: string
  created_by: number | null
  running: boolean
  active_run: AgentRun | null
  last_run: AgentRun | null
  created_at: string
  updated_at: string
  last_activity_at: string
}

export type AgentProfile = {
  id: number
  key: string
  name: string
  description: string
  builtin: boolean
  enabled: boolean
  system_prompt: string
  model: string
  allowed_tools: string[]
  max_turns: number
  run_seconds: number
  hard_timeout_seconds: number
  thinking: "auto" | "enabled" | "disabled"
  reasoning_effort: "" | "none" | "low" | "high" | "max"
  max_output_tokens: number
  created_at: string
  updated_at: string
}

export type AgentTool = { name: string; description: string; schema: unknown }

/**
 * P8 Agent centre (spec §8.1/D60). The catalogue is a read-only projection of
 * the 13 logical roles: it carries no system prompt and no credential, only the
 * profile configuration summary and run statistics.
 */
export type AgentCenterRoleStats = {
  run_count: number
  succeeded_count: number
  failed_count: number
  success_rate: number
}

export type AgentCenterRoleMode = "agent" | "deterministic" | "manual_review"

export type AgentCenterRole = {
  order: number
  role: string
  name: string
  phase: string
  mode: AgentCenterRoleMode
  note: string
  profile_key: string | null
  profile_id: number | null
  enabled: boolean
  model: string
  prompt_version: string | null
  allowed_tools: string[]
  stats: AgentCenterRoleStats
}

export type AgentCenterEval = {
  run_id: number
  passed: boolean
  metrics: Record<string, number>
  thresholds: Record<string, number>
  created_at: string
}

export type AgentCenterCatalog = {
  roles: AgentCenterRole[]
  latest_eval: AgentCenterEval | null
}

export type AgentCenterQuality = { latest: AgentCenterEval | null }

export type AgentCenterSessionStatus =
  | "idle"
  | "running"
  | "done"
  | "failed"
  | "aborted"
  | "interrupted"

export type AgentCenterSession = {
  id: number
  role: string
  title: string
  status: AgentCenterSessionStatus
  running: boolean
  created_at: string
  updated_at: string
  last_activity_at: string
}

export type AgentCenterSessionPage = { items: AgentCenterSession[] }

export type AgentUsage = {
  prompt_tokens: number
  completion_tokens: number
  reasoning_tokens: number
  total_tokens: number
}

export type AgentMessageDetail = {
  id: number
  session_id: number
  run_id: number | null
  role: "system" | "user" | "assistant" | "tool"
  content: { type: string; text?: string; image_url?: string }[]
  reasoning: string
  tool_calls: { id: string; name: string; arguments: string }[] | null
  tool_call_id: string | null
  tool_name: string | null
  usage: AgentUsage | null
  created_at: string
}

export type AgentEvent = {
  seq: number
  channel: string
  type: string
  ts: string
  summary: string
  agent_role?: string | null
  session_id?: string | null
  detail: Record<string, unknown>
}

export type ActivitiesPage = {
  items: AgentEvent[]
  snapshot_cursor: number
  has_more: boolean
}

export type ProfilePatch = Partial<
  Pick<
    AgentProfile,
    | "system_prompt"
    | "model"
    | "allowed_tools"
    | "max_turns"
    | "run_seconds"
    | "hard_timeout_seconds"
    | "thinking"
    | "reasoning_effort"
    | "max_output_tokens"
    | "enabled"
  >
>

export type SessionPage = {
  items: AgentSession[]
  has_more: boolean
  next_cursor: string | null
}

const AGENT_PROFILES_PATH = "/api/agents/profiles"
const AGENT_TOOLS_PATH = "/api/agents/tools"
const AGENT_SESSIONS_PATH = "/api/agents/sessions"
const AGENT_CENTER_PATH = "/api/agent-center"
const STREAM_EVENTS_PATH = "/api/stream/events"

function sessionPath(id: number): string {
  return `${AGENT_SESSIONS_PATH}/${id}`
}

function withQuery(path: string, query: URLSearchParams): string {
  const suffix = query.toString()
  return suffix === "" ? path : `${path}?${suffix}`
}

/** GET /api/agents/profiles — every configured agent profile (admin only). */
export function listProfiles(): Promise<AgentProfile[]> {
  return apiGet<AgentProfile[]>(AGENT_PROFILES_PATH)
}

/** PATCH /api/agents/profiles/{id} — only the editable profile fields. */
export function updateProfile(id: number, patch: ProfilePatch): Promise<AgentProfile> {
  return apiPatch<AgentProfile>(`${AGENT_PROFILES_PATH}/${id}`, patch)
}

/** GET /api/agents/tools — the registered tool catalog (admin only). */
export function listTools(): Promise<AgentTool[]> {
  return apiGet<AgentTool[]>(AGENT_TOOLS_PATH)
}

/**
 * GET /api/agents/sessions — one page of sessions. The server wraps the page in
 * `{data: {items, has_more, next_cursor}}`; apiGet already unwraps `data`, so
 * callers receive the page object directly.
 */
export function listSessions(
  params: { limit?: number; cursor?: string } = {},
): Promise<SessionPage> {
  const query = new URLSearchParams()
  if (params.limit !== undefined) query.set("limit", String(params.limit))
  if (params.cursor !== undefined) query.set("cursor", params.cursor)
  return apiGet<SessionPage>(withQuery(AGENT_SESSIONS_PATH, query))
}

/** POST /api/agents/sessions — creates a session on the requested/default profile. */
export function createSession(input: {
  profile_id?: number
  title?: string
}): Promise<AgentSession> {
  return apiPost<AgentSession>(AGENT_SESSIONS_PATH, input)
}

/** GET /api/agents/sessions/{id}. */
export function getSession(id: number): Promise<AgentSession> {
  return apiGet<AgentSession>(sessionPath(id))
}

/** PATCH /api/agents/sessions/{id} — sessions only allow title edits. */
export function updateSessionTitle(id: number, title: string): Promise<AgentSession> {
  return apiPatch<AgentSession>(sessionPath(id), { title })
}

/** DELETE /api/agents/sessions/{id}. The API aborts a running run first. */
export function deleteSession(id: number): Promise<void> {
  return apiDelete<void>(sessionPath(id))
}

/** POST /api/agents/sessions/{id}/messages — 202 with the accepted run id. */
export function sendMessage(
  id: number,
  message: string,
): Promise<{ run_id: number; status: string }> {
  return apiPost<{ run_id: number; status: string }>(`${sessionPath(id)}/messages`, { message })
}

/** POST /api/agents/sessions/{id}/stop — idempotent stop request. */
export function stopSession(id: number): Promise<{ status: "stopping" | "idle" }> {
  return apiPost<{ status: "stopping" | "idle" }>(`${sessionPath(id)}/stop`)
}

/** GET /api/agents/sessions/{id}/messages/{message_id} — full message detail. */
export function getMessage(sessionId: number, messageId: number): Promise<AgentMessageDetail> {
  return apiGet<AgentMessageDetail>(`${sessionPath(sessionId)}/messages/${messageId}`)
}

/**
 * GET /api/stream/events?channel=agent:{id} — the persisted activity snapshot.
 * The channel name contains a colon, so it is encoded as part of the query.
 */
export function fetchActivities(
  sessionId: number,
  params: { before_seq?: number; limit?: number } = {},
): Promise<ActivitiesPage> {
  const query = new URLSearchParams({ channel: `agent:${sessionId}` })
  if (params.before_seq !== undefined) query.set("before_seq", String(params.before_seq))
  if (params.limit !== undefined) query.set("limit", String(params.limit))
  return apiGet<ActivitiesPage>(withQuery(STREAM_EVENTS_PATH, query))
}

/**
 * GET /api/agent-center/catalog — the 13 logical roles with their profile
 * projection and run statistics. Readable by every logged-in user; never
 * contains a prompt or credential.
 */
export function getAgentCenterCatalog(): Promise<AgentCenterCatalog> {
  return apiGet<AgentCenterCatalog>(`${AGENT_CENTER_PATH}/catalog`)
}

/**
 * GET /api/agent-center/quality — the latest evaluation summary. Until the
 * evaluation context is wired (Task 23) the server returns `latest: null`.
 */
export function getAgentCenterQuality(): Promise<AgentCenterQuality> {
  return apiGet<AgentCenterQuality>(`${AGENT_CENTER_PATH}/quality`)
}

/**
 * GET /api/agent-center/sessions — read-only run observation filtered by the
 * logical role and derived status. Empty filters are omitted so the server
 * treats them as "all".
 */
export function listAgentCenterSessions(
  params: { role?: string; status?: string; limit?: number } = {},
): Promise<AgentCenterSessionPage> {
  const query = new URLSearchParams()
  if (params.role) query.set("role", params.role)
  if (params.status) query.set("status", params.status)
  if (params.limit !== undefined) query.set("limit", String(params.limit))
  return apiGet<AgentCenterSessionPage>(withQuery(`${AGENT_CENTER_PATH}/sessions`, query))
}
