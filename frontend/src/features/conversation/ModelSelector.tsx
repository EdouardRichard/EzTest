"use client"

import { useEffect, useState } from "react"
import { cn } from "@/components/ui/cn"
import { Select } from "@/components/ui/Select"

import { listPublicModels, type PublicModel, updateConversationModel } from "./api"

export type ModelSelectorProps = {
  versionId: string
  /** Null when no conversation is active; the selector then renders nothing. */
  conversationId: string | null
  /** Persisted conversation override; null follows the config's default model. */
  modelOverride?: string | null
  /** The composer draft contains images, so non-vision models are gated. */
  draftHasImages?: boolean
  className?: string
}

function errorMessage(cause: unknown): string {
  if (cause instanceof Error && cause.message.length > 0) return cause.message
  return "模型切换失败，请重试"
}

/**
 * Resolves the initial selection: the conversation override when it still names
 * a selectable model, otherwise the LLM configuration's explicit
 * `default_model_id`. It must never fall back to `models[0]` (D24).
 */
function initialModel(models: PublicModel[], override: string | null | undefined): string {
  const value = override?.trim() ?? ""
  if (value !== "") {
    const match = models.find((model) => model.model === value && !model.deprecated)
    if (match !== undefined) return match.model
  }
  return models.find((model) => model.default)?.model ?? ""
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
  className,
}: ModelSelectorProps) {
  const [models, setModels] = useState<PublicModel[] | null>(null)
  const [selected, setSelected] = useState("")
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
      return () => {
        cancelled = true
      }
    }
    setModels(null)
    setError(null)
    void listPublicModels().then(
      (items) => {
        if (cancelled) return
        setModels(items)
        setSelected(initialModel(items, modelOverride))
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
      await updateConversationModel(versionId, conversationId, next)
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
      {error !== null ? (
        <p role="alert" className="text-[10px] text-destructive">
          {error}
        </p>
      ) : null}
    </div>
  )
}
