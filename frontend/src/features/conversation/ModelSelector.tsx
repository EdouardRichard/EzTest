"use client"

import { useEffect, useState } from "react"
import { cn } from "@/components/ui/cn"
import { Select } from "@/components/ui/Select"

import {
  type ConversationModel,
  listPublicModels,
  type PublicModel,
  updateConversationModel,
} from "./api"

export type ModelSelectorProps = {
  versionId: string
  /** Null when no conversation is active; the selector then renders nothing. */
  conversationId: string | null
  /** Persisted conversation override; null follows the config's default model. */
  modelOverride?: string | null
  /** The composer draft contains images, so non-vision models are gated. */
  draftHasImages?: boolean
  /**
   * Fired after a successful PATCH with the server's canonical view, so the
   * parent conversation summary stays in sync and a remounted selector reseeds
   * from the new override.
   */
  onModelChanged?: (conversation: ConversationModel) => void
  className?: string
}

function errorMessage(cause: unknown): string {
  if (cause instanceof Error && cause.message.length > 0) return cause.message
  return "模型切换失败，请重试"
}

type InitialSelection = {
  value: string
  /** Stored override the catalog no longer offers as a selectable model. */
  unavailable: string | null
}

/**
 * Resolves the initial selection: the conversation override when it still names
 * a selectable model, otherwise the LLM configuration's explicit
 * `default_model_id`. It must never fall back to `models[0]` (D24). An override
 * that is no longer selectable stays visible and disabled instead of silently
 * reverting to the default: the conversation is still pinned to it, so the user
 * has to pick a new model deliberately.
 */
function initialSelection(
  models: PublicModel[],
  override: string | null | undefined,
): InitialSelection {
  const value = override?.trim() ?? ""
  if (value !== "") {
    const match = models.find((model) => model.model === value && !model.deprecated)
    if (match !== undefined) return { value: match.model, unavailable: null }
    return { value, unavailable: value }
  }
  return { value: models.find((model) => model.default)?.model ?? "", unavailable: null }
}

function optionLabel(model: PublicModel): string {
  if (model.display_name.trim() === "" || model.display_name === model.model) return model.model
  return `${model.display_name} · ${model.model}`
}

/**
 * Conversation-level model picker (spec §9.1): shows the configured default,
 * sends the user-initiated override through `PATCH /conversations/{cid}`,
 * disables non-vision models while the draft carries images, and never switches
 * the model automatically (D24). A failed switch reverts the control and shows
 * the API error instead of pretending the model changed.
 */
export function ModelSelector({
  versionId,
  conversationId,
  modelOverride = null,
  draftHasImages = false,
  onModelChanged,
  className,
}: ModelSelectorProps) {
  const [models, setModels] = useState<PublicModel[] | null>(null)
  const [selected, setSelected] = useState("")
  const [unavailable, setUnavailable] = useState<string | null>(null)
  const [switching, setSwitching] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Fetch once per conversation/override pair. A parent rerender with the same
  // summary does not reset the local selection, so the picker keeps the model
  // the user just chose even though the summary prop is momentarily stale.
  // biome-ignore lint/correctness/useExhaustiveDependencies: modelOverride only seeds the initial selection
  useEffect(() => {
    let cancelled = false
    if (conversationId === null) {
      setModels(null)
      setError(null)
      setUnavailable(null)
      return () => {
        cancelled = true
      }
    }
    setModels(null)
    setError(null)
    setUnavailable(null)
    void listPublicModels().then(
      (items) => {
        if (cancelled) return
        const selection = initialSelection(items, modelOverride)
        setModels(items)
        setSelected(selection.value)
        setUnavailable(selection.unavailable)
      },
      (cause: unknown) => {
        if (!cancelled) setError(errorMessage(cause))
      },
    )
    return () => {
      cancelled = true
    }
  }, [versionId, conversationId])

  async function handleChange(next: string): Promise<void> {
    const previous = selected
    if (next === previous) return
    const model = models?.find((candidate) => candidate.model === next)
    if (conversationId === null || model === undefined) return
    setSelected(next)
    setError(null)
    setSwitching(true)
    try {
      const updated = await updateConversationModel(versionId, conversationId, next)
      // Publish the server's canonical value to the parent summary: the keyed
      // selector is remounted on a conversation switch or a collapse/expand and
      // must reseed from the persisted override rather than the stale prop.
      setSelected(updated.model_override ?? next)
      setUnavailable(null)
      onModelChanged?.(updated)
    } catch (cause) {
      // No silent switch: put the control back and surface the reason.
      setSelected(previous)
      setError(errorMessage(cause))
    } finally {
      setSwitching(false)
    }
  }

  if (conversationId === null) return null
  if (models === null) {
    return error === null ? null : (
      <p role="alert" className={cn("text-[11px] text-destructive", className)}>
        {error}
      </p>
    )
  }

  const selectedModel = models.find((model) => model.model === selected)
  const visionBlocked =
    draftHasImages && selectedModel !== undefined && !selectedModel.capabilities.supports_vision

  return (
    <div className={cn("flex min-w-0 flex-col gap-1", className)}>
      <div className="flex min-w-0 items-center gap-1.5">
        <span className="shrink-0 text-[10px] text-muted-foreground">模型</span>
        <Select
          data-testid="model-selector"
          aria-label="会话模型"
          value={selected}
          disabled={switching || selected === ""}
          onChange={(event) => {
            void handleChange(event.target.value)
          }}
          className="h-7 w-auto max-w-[14rem] min-w-[8rem] py-0 text-xs"
        >
          {selected === "" ? (
            <option value="" disabled>
              未配置默认模型
            </option>
          ) : null}
          {unavailable !== null && unavailable === selected ? (
            <option value={unavailable} disabled>
              {unavailable}（不可用，请重新选择）
            </option>
          ) : null}
          {models.map((model) => {
            const visionGated = draftHasImages && !model.capabilities.supports_vision
            return (
              <option key={model.id} value={model.model} disabled={model.deprecated || visionGated}>
                {optionLabel(model)}
                {model.deprecated ? "（已弃用）" : ""}
                {visionGated ? "（不支持图片）" : ""}
              </option>
            )
          })}
        </Select>
      </div>
      {visionBlocked ? (
        <p className="text-[10px] text-warning">当前草稿含图片，请选择支持视觉的模型</p>
      ) : null}
      {unavailable !== null ? (
        <p role="alert" className="text-[10px] text-destructive">
          会话保存的模型“{unavailable}”当前不可用，请重新选择模型
        </p>
      ) : null}
      {error !== null ? (
        <p role="alert" className="text-[10px] text-destructive">
          {error}
        </p>
      ) : null}
    </div>
  )
}
