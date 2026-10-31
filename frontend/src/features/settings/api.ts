import { type ApiPage, apiDelete, apiGet, apiGetPage, apiPatch, apiPost } from "@/lib/api-client"

// --- System settings -------------------------------------------------------

export type SettingType = "string" | "number" | "boolean" | "json"

export type SettingValue = string | number | boolean | Record<string, unknown> | unknown[] | null

/** One row from `GET /api/admin/settings`; secret values are always null. */
export type Setting = {
  key: string
  category: string
  type: SettingType
  value: SettingValue
  is_secret: boolean
  restart_required: boolean
  description: string
  version: number
  updated_at: string
}

export type SettingGroup = {
  category: string
  label: string
  settings: Setting[]
}

/** Human labels for the frozen P0 setting categories (unknown keys fall back). */
export const SETTING_CATEGORY_LABELS: Record<string, string> = {
  runtime: "运行时",
  paths: "路径",
  execution: "执行",
  agent: "Agent",
  llm: "LLM",
  context: "上下文",
  team_room: "团队房间",
  retention: "保留与清理",
  report: "报告",
  knowledge: "知识",
  security: "安全",
  feature: "功能开关",
}

/**
 * Groups settings by category in the order the API returned them, which keeps
 * the panel stable across reloads without re-sorting server-owned data.
 */
export function groupSettings(settings: Setting[]): SettingGroup[] {
  const groups = new Map<string, Setting[]>()
  for (const setting of settings) {
    const bucket = groups.get(setting.category)
    if (bucket) {
      bucket.push(setting)
    } else {
      groups.set(setting.category, [setting])
    }
  }
  return Array.from(groups, ([category, items]) => ({
    category,
    label: SETTING_CATEGORY_LABELS[category] ?? category,
    settings: items,
  }))
}

export function fetchSettings(): Promise<Setting[]> {
  return apiGet<Setting[]>("/api/admin/settings")
}

export function updateSetting(key: string, value: unknown): Promise<Setting> {
  return apiPatch<Setting>(`/api/admin/settings/${encodeURIComponent(key)}`, { value })
}

// --- LLM configurations ----------------------------------------------------

export type LLMProtocol = "openai" | "openai_compatible"
export type LLMStatus = "active" | "disabled"
export type LLMModelSource = "catalog" | "manual"

export type LLMConfig = {
  id: string
  name: string
  provider: string
  protocol: LLMProtocol
  base_url: string
  api_key_set: boolean
  headers: Record<string, string>
  timeout_seconds: number
  max_retries: number
  is_default: boolean
  status: LLMStatus
  created_by?: string
  created_at: string
  updated_at: string
}

export type LLMModel = {
  id: string
  config_id: string
  model: string
  display_name: string
  context_window: number
  max_output: number
  supports_tools: boolean
  supports_vision: boolean
  source: LLMModelSource
  status: LLMStatus
  created_at: string
  updated_at: string
}

export type RemoteModel = {
  id: string
  context_window: number
  max_output: number
  supports_tools: boolean
  supports_vision: boolean
}

export type LLMTestResult = {
  ok: boolean
  latency_ms: number
  models: RemoteModel[]
  error?: string
}

export type LLMConfigInput = {
  name: string
  base_url: string
  provider?: string
  protocol?: LLMProtocol
  api_key?: string
  headers?: Record<string, string>
  timeout_seconds?: number
  max_retries?: number
  is_default?: boolean
  status?: LLMStatus
}

export type AddLLMModelInput = {
  model: string
  context_window?: number
  max_output?: number
  supports_tools?: boolean
  supports_vision?: boolean
}

export function fetchLLMConfigs(): Promise<LLMConfig[]> {
  return apiGet<LLMConfig[]>("/api/admin/llm/configs")
}

export function createLLMConfig(input: LLMConfigInput): Promise<LLMConfig> {
  return apiPost<LLMConfig>("/api/admin/llm/configs", input)
}

export function updateLLMConfig(id: string, input: LLMConfigInput): Promise<LLMConfig> {
  return apiPatch<LLMConfig>(`/api/admin/llm/configs/${encodeURIComponent(id)}`, input)
}

export function deleteLLMConfig(id: string): Promise<void> {
  return apiDelete<void>(`/api/admin/llm/configs/${encodeURIComponent(id)}`)
}

export function testLLMConfig(id: string): Promise<LLMTestResult> {
  return apiPost<LLMTestResult>(`/api/admin/llm/configs/${encodeURIComponent(id)}/test`)
}

export function refreshLLMModels(id: string): Promise<LLMModel[]> {
  return apiPost<LLMModel[]>(`/api/admin/llm/configs/${encodeURIComponent(id)}/refresh-models`)
}

export function fetchLLMModels(): Promise<LLMModel[]> {
  return apiGet<LLMModel[]>("/api/admin/llm/models")
}

export function addLLMModel(configId: string, input: AddLLMModelInput): Promise<LLMModel> {
  return apiPost<LLMModel>(`/api/admin/llm/configs/${encodeURIComponent(configId)}/models`, input)
}

export function deleteLLMModel(configId: string, modelId: string): Promise<void> {
  return apiDelete<void>(
    `/api/admin/llm/configs/${encodeURIComponent(configId)}/models/${encodeURIComponent(modelId)}`,
  )
}

// --- Admin users -----------------------------------------------------------

export type UserRole = "admin" | "user"
export type UserStatus = "active" | "disabled"

export type User = {
  id: string
  username: string
  display_name: string
  role: UserRole
  status: UserStatus
  created_at: string
  updated_at: string
}

export type CreateUserInput = {
  username: string
  display_name: string
  password: string
}

export type UpdateUserInput = {
  display_name?: string
  status?: UserStatus
}

export type PageQuery = {
  cursor?: string
  limit?: number
}

function withQuery(path: string, params: Record<string, string | number | undefined>): string {
  const search = new URLSearchParams()
  for (const [name, value] of Object.entries(params)) {
    if (value === undefined) continue
    const text = String(value).trim()
    if (text.length === 0) continue
    search.set(name, text)
  }
  const query = search.toString()
  return query.length > 0 ? `${path}?${query}` : path
}

export function fetchUsers(query: PageQuery = {}): Promise<ApiPage<User>> {
  return apiGetPage<User>(
    withQuery("/api/admin/users", { cursor: query.cursor, limit: query.limit }),
  )
}

export function createUser(input: CreateUserInput): Promise<User> {
  return apiPost<User>("/api/admin/users", input)
}

export function updateUser(id: string, input: UpdateUserInput): Promise<User> {
  return apiPatch<User>(`/api/admin/users/${encodeURIComponent(id)}`, input)
}

/**
 * Sends `new_password` because that is what the implemented identity handler
 * accepts today; the frozen OpenAPI ResetPasswordRequest still names the field
 * `password`. Flagged for contract/backend reconciliation.
 */
export function resetUserPassword(id: string, password: string): Promise<void> {
  return apiPost<void>(`/api/admin/users/${encodeURIComponent(id)}/reset-password`, {
    new_password: password,
  })
}

// --- Audit log -------------------------------------------------------------

export type AuditLogEntry = {
  id: string
  actor_id?: string
  action: string
  target_kind: string
  target_id: string
  request_id: string
  ip: string
  user_agent: string
  metadata: Record<string, unknown>
  created_at: string
}

export type AuditFilters = {
  action?: string
  actor?: string
  cursor?: string
  limit?: number
}

export function listAuditLogs(filters: AuditFilters = {}): Promise<ApiPage<AuditLogEntry>> {
  return apiGetPage<AuditLogEntry>(
    withQuery("/api/admin/audit-logs", {
      action: filters.action,
      actor: filters.actor,
      cursor: filters.cursor,
      limit: filters.limit,
    }),
  )
}
