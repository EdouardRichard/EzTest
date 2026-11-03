"use client"

import { useEffect, useState } from "react"

import { listPublicModels, type PublicModel } from "./api"
import { selectConversationModel } from "./modelSelection"

/**
 * Send-blocking explanation for an image draft on a model without vision. The
 * backend repeats this decision with a typed `llm_capability_mismatch` 400; the
 * page disables the action first so the user never gets a 201 ghost send.
 */
export const VISION_SEND_BLOCKED_MESSAGE =
  "当前草稿含图片，所选模型不支持图片，发送已禁用；请更换支持视觉的模型或移除图片。"

export type VisionGate = {
  /** True when the draft has an image and the conversation model lacks vision. */
  blocked: boolean
  /** Resolved model name, or null while unknown. */
  model: string | null
}

export type VisionGateInput = {
  conversationId: string | null
  modelOverride: string | null | undefined
  draftHasImages: boolean
}

/**
 * Computes the 0.2.1 vision send gate from the persisted conversation model.
 *
 * It deliberately does not depend on `ModelSelector` being rendered: the
 * collapsed design chat hides the picker but still accepts image drafts, and a
 * hidden selector must not silently leave the send action enabled. The backend
 * gate is the authority; this hook keeps the normal UI path from ever reaching
 * it.
 */
export function useVisionGate({
  conversationId,
  modelOverride,
  draftHasImages,
}: VisionGateInput): VisionGate {
  const [models, setModels] = useState<PublicModel[] | null>(null)

  useEffect(() => {
    let cancelled = false
    setModels(null)
    if (conversationId === null) {
      return () => {
        cancelled = true
      }
    }
    void listPublicModels().then(
      (items) => {
        if (!cancelled) setModels(items)
      },
      () => {
        // A failed catalog read stays unknown; the server-side gate still
        // rejects the send with the typed capability error.
        if (!cancelled) setModels(null)
      },
    )
    return () => {
      cancelled = true
    }
  }, [conversationId])

  if (!draftHasImages || models === null) return { blocked: false, model: null }
  const selection = selectConversationModel(models, modelOverride)
  return {
    blocked: selection.model !== null && !selection.model.capabilities.supports_vision,
    model: selection.model?.model ?? null,
  }
}
