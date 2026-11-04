import type { PublicModel } from "./api"

/**
 * Resolved model a conversation is pinned to. `unavailable` keeps a stored
 * override the catalog no longer serves visible instead of silently reverting
 * to the configuration default (D24).
 */
export type ConversationModelSelection = {
  value: string
  model: PublicModel | null
  unavailable: string | null
}

/**
 * Resolves the conversation's model: the stored override when the catalog still
 * offers it, otherwise the LLM configuration's explicit `default` model. It
 * never falls back to `models[0]` (D24) and never switches the model on its own.
 *
 * Shared by the model picker and the draft vision gate so both agree on which
 * model the conversation currently uses.
 */
export function selectConversationModel(
  models: PublicModel[],
  override: string | null | undefined,
): ConversationModelSelection {
  const value = override?.trim() ?? ""
  if (value !== "") {
    const match = models.find((model) => model.model === value && !model.deprecated)
    if (match !== undefined) return { value: match.model, model: match, unavailable: null }
    return { value, model: null, unavailable: value }
  }
  const fallback = models.find((model) => model.default) ?? null
  return { value: fallback?.model ?? "", model: fallback, unavailable: null }
}
