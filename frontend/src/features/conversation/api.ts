import { apiGet, apiPatch } from "@/lib/api-client"

/** `PublicModelView.capabilities` (OpenAPI, spec §9.1). */
export type PublicModelCapabilities = {
  supports_tools: boolean
  supports_vision: boolean
  supports_reasoning: boolean
  context_window: number
  supports_prompt_cache: boolean
}

/** `GET /api/llm/models` item: one enabled, selectable model of an active LLM config. */
export type PublicModel = {
  id: string
  config_id: string
  config_name: string
  model: string
  display_name: string
  context_window: number
  capabilities: PublicModelCapabilities
  /** True only for the LLM config's explicit `default_model_id`, never models[0]. */
  default: boolean
  deprecated: boolean
}

/** The conversation fields returned by the model-override PATCH. */
export type ConversationModel = {
  id: string
  model_override: string | null
  effective_model: string | null
}

/** One ordered segment of the cache-first context packet (admin-only read). */
export type ContextPacketSegment = {
  name: string
  role: string
  content: string
  tool_call_id?: string
  /** True for the stable, cacheable system prefix. */
  prefix: boolean
  tokens: number
}

/** `GET /api/versions/{id}/context-packets/latest` (admin-only, spec §6.3). */
export type ContextPacketView = {
  prefix_hash: string
  segments: ContextPacketSegment[]
  cached_tokens: number
  cache_hit: boolean
  invalidation_reason: string
  briefing_hash: string
}

/**
 * Reads the latest persisted context packet of the version. The payload contains
 * prompt content, so the backend only serves it to an admin (403 for a normal
 * user, 404 when no packet was recorded, 503 when the reader is not wired).
 */
export function getLatestContextPacket(versionId: string): Promise<ContextPacketView> {
  return apiGet<ContextPacketView>(
    `/api/versions/${encodeURIComponent(versionId)}/context-packets/latest`,
  )
}

/**
 * Reads the login-user model catalog. The backend only returns models that are
 * enabled, not deprecated and owned by an active LLM configuration, and it
 * never includes gateway credentials.
 */
export function listPublicModels(): Promise<PublicModel[]> {
  return apiGet<PublicModel[]>("/api/llm/models")
}

/**
 * Sets or clears one conversation's model override. The switch is always
 * user-initiated (D24): the runtime never changes a conversation's model on its
 * own. `null` clears the override and falls back to the config default.
 */
export function updateConversationModel(
  versionId: string,
  conversationId: string,
  modelOverride: string | null,
): Promise<ConversationModel> {
  return apiPatch<ConversationModel>(
    `/api/versions/${encodeURIComponent(versionId)}/conversations/${encodeURIComponent(conversationId)}`,
    { model_override: modelOverride },
  )
}
