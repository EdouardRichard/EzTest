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

// --- Admin GC --------------------------------------------------------------

export type GCRunMode = "dry_run" | "apply"
export type GCRunStatus = "running" | "completed" | "failed"
export type GCActorKind = "user" | "system"

/** Retention values a GC pass used (`gc_run.report.settings`). */
export type GCReportSettings = {
  temp_hours: number
  replaced_export_days: number
  unreferenced_content_days: number
  grace_hours: number
  batch_size: number
}

export type GCReportScanned = {
  objects: number
  refs: number
  files: number
  export_artifacts: number
}

export type GCReportActions = {
  objects: number
  refs: number
  files: number
  bytes: number
}

export type GCReportFiles = {
  deleted: number
  missing: number
  failed: number
}

/** One candidate in the capped report sample (`report.candidate_samples`). */
export type GCCandidateSample = {
  class: string
  object_sha: string
  size: number
  owner_kind?: string
  owner_id?: number
  ref_id?: number
  export_artifact_id?: number
  age_seconds: number
  delete_object: boolean
  delete_ref: boolean
}

export type GCProtectedSample = {
  object_sha?: string
  owner_kind?: string
  owner_id?: number
  ref_id?: number
  reason: string
  age_seconds: number
}

/**
 * `gc_run.report` JSONB. The API passes the stored object through, so optional
 * fields keep old/partial reports renderable without a schema migration.
 */
export type GCReport = {
  mode?: GCRunMode
  dry_run?: boolean
  status?: string
  started_at?: string
  finished_at?: string | null
  error?: string
  settings?: Partial<GCReportSettings>
  scanned?: Partial<GCReportScanned>
  candidates?: Record<string, number>
  candidate_samples?: GCCandidateSample[]
  protected?: Record<string, number>
  protected_samples?: GCProtectedSample[]
  samples_truncated?: boolean
  unknown_owner_kinds?: string[]
  planned?: Partial<GCReportActions>
  deleted?: Partial<GCReportActions>
  files?: Partial<GCReportFiles>
  tombstones?: number
  errors?: string[]
}

/** `GCRunView` from `GET/POST /api/admin/gc/runs` (contract p7-api §3). */
export type GCRun = {
  id: string
  mode: GCRunMode
  status: GCRunStatus
  actor_id?: string | null
  actor_kind: GCActorKind
  started_at: string
  finished_at?: string | null
  scanned_objects: number
  scanned_refs: number
  deleted_objects: number
  deleted_refs: number
  deleted_bytes: number
  report: GCReport
  error?: string | null
}

/** `gcTombstoneDTO` from `GET /api/admin/gc/runs/{gcRunId}`. */
export type GCTombstone = {
  id: string
  run_id: string
  object_sha: string
  size: number
  mime: string
  relative_path: string
  reason: string
  deleted_at: string
}

export type GCRunDetail = GCRun & { tombstones: GCTombstone[] }

/** `GET /api/admin/gc/runs?limit=` (newest first). */
export function listGCRuns(limit = 20): Promise<ApiPage<GCRun>> {
  return apiGetPage<GCRun>(withQuery("/api/admin/gc/runs", { limit }))
}

/**
 * `POST /api/admin/gc/runs`: dry runs need no confirmation; apply passes
 * `confirm=true` (the server answers 400 `confirmation_required` otherwise).
 */
export function createGCRun(mode: GCRunMode, confirm?: boolean): Promise<GCRun> {
  const body: { mode: GCRunMode; confirm?: boolean } = { mode }
  if (confirm === true) body.confirm = true
  return apiPost<GCRun>("/api/admin/gc/runs", body)
}

/** `GET /api/admin/gc/runs/{gcRunId}`: run + tombstones. */
export function getGCRun(id: string): Promise<GCRunDetail> {
  return apiGet<GCRunDetail>(`/api/admin/gc/runs/${encodeURIComponent(id)}`)
}

// --- Admin instances -------------------------------------------------------

export type InstanceStatus = "active" | "draining" | "offline" | "stopped"

/** `instanceView` from `GET /api/admin/instances` (contract p7-api §2). */
export type Instance = {
  id: string
  instance_key: string
  hostname: string
  pid: number
  api_port: number
  node_id: number
  status: InstanceStatus
  version: string
  /** P8 in-flight counters reported by the last heartbeat (drain observability). */
  active_requests?: number
  active_work?: number
  last_error?: string | null
  started_at: string
  heartbeat_at: string
  draining_at?: string | null
  stopped_at?: string | null
  created_at: string
  updated_at: string
}

/** `GET /api/admin/instances` (admin-only roster). */
export function listInstances(): Promise<ApiPage<Instance>> {
  return apiGetPage<Instance>("/api/admin/instances")
}

/** `POST /api/admin/instances/{instanceId}/drain`: readiness 503, stop claiming. */
export function drainInstance(id: string): Promise<Instance> {
  return apiPost<Instance>(`/api/admin/instances/${encodeURIComponent(id)}/drain`)
}

/** `POST /api/admin/instances/{instanceId}/activate`: cancel draining. */
export function activateInstance(id: string): Promise<Instance> {
  return apiPost<Instance>(`/api/admin/instances/${encodeURIComponent(id)}/activate`)
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
