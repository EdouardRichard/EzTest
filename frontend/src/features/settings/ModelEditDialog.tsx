"use client"

import type { FormEvent } from "react"
import { useState } from "react"

import { Alert } from "@/components/ui/Alert"
import { Button } from "@/components/ui/Button"
import { Dialog } from "@/components/ui/Dialog"
import { Field } from "@/components/ui/Field"
import { Input } from "@/components/ui/Input"
import { ApiError } from "@/lib/api-client"

import {
  type LLMModel,
  type LLMModelCapabilities,
  resetLLMModelParams,
  type UpdateLLMModelInput,
  updateLLMModel,
} from "./api"

/** v0.22 K unit: 1K = 1000 tokens (A §4.1). Storage and API stay raw tokens. */
const K = 1000

const DEFAULT_CONTEXT_WINDOW_TOKENS = 128_000
const DEFAULT_MAX_OUTPUT_TOKENS = 4096
const MAX_CONTEXT_WINDOW_TOKENS = 10_000_000
const MAX_K_DECIMALS = 3

export type CapabilityFlag =
  | "supports_tools"
  | "supports_vision"
  | "supports_reasoning"
  | "supports_prompt_cache"

export const CAPABILITY_FIELDS: ReadonlyArray<{ key: CapabilityFlag; label: string; id: string }> =
  [
    { key: "supports_tools", label: "支持工具", id: "llm-model-tools" },
    { key: "supports_vision", label: "支持视觉", id: "llm-model-vision" },
    { key: "supports_reasoning", label: "支持推理", id: "llm-model-reasoning" },
    { key: "supports_prompt_cache", label: "提示缓存", id: "llm-model-prompt-cache" },
  ]

/**
 * Merges the structured capability object with the legacy flat columns so a
 * partial payload (or an older API build) still renders sensible state.
 */
export function modelCapabilities(model: LLMModel | undefined): LLMModelCapabilities {
  const capabilities = model?.capabilities
  return {
    supports_tools: capabilities?.supports_tools ?? model?.supports_tools ?? false,
    supports_vision: capabilities?.supports_vision ?? model?.supports_vision ?? false,
    supports_reasoning: capabilities?.supports_reasoning ?? false,
    supports_prompt_cache: capabilities?.supports_prompt_cache ?? false,
    context_window: capabilities?.context_window ?? model?.context_window ?? 0,
  }
}

/** Raw stored context window, falling back to the capability mirror. */
export function modelContextWindow(model: LLMModel): number {
  if (model.context_window > 0) return model.context_window
  const mirrored = model.capabilities?.context_window ?? 0
  return mirrored > 0 ? mirrored : DEFAULT_CONTEXT_WINDOW_TOKENS
}

function deriveMaxInput(windowTokens: number, outputTokens: number): number {
  const output = outputTokens > 0 ? outputTokens : DEFAULT_MAX_OUTPUT_TOKENS
  return Math.max(windowTokens - output, 0)
}

/**
 * Read-only input budget: the server-derived `max_input` wins when present;
 * older payloads fall back to effective window − raw output (output 0 → 4096),
 * mirroring the backend rule (A §4.1).
 */
export function modelMaxInput(model: LLMModel): number {
  if (typeof model.max_input === "number") return model.max_input
  return deriveMaxInput(modelContextWindow(model), model.max_output)
}

/** Renders raw tokens as K with at most 3 decimals (128000 → "128"). */
export function formatTokensK(tokens: number): string {
  return Number.parseFloat((tokens / K).toFixed(MAX_K_DECIMALS)).toString()
}

/** Row label: 0 means auto/unknown and is called out explicitly. */
export function formatTokensKWithAuto(tokens: number): string {
  return tokens === 0 ? "0K（自动）" : `${formatTokensK(tokens)}K`
}

type ParsedK = { ok: true; tokens: number } | { ok: false; error: string }

/**
 * Parses a K input into raw tokens with an exact integer conversion. The
 * backend remains authoritative; this only keeps obvious mistakes from being
 * sent and matches the server's 0 ≤ W ≤ 10,000,000 / O ≤ W rules.
 */
function parseKInput(text: string, label: string): ParsedK {
  const trimmed = text.trim()
  if (trimmed.length === 0 || !/^\d+(\.\d{1,3})?$/.test(trimmed)) {
    return { ok: false, error: `${label}必须是 0 或更大的数字（最多 3 位小数）` }
  }
  const tokens = Math.round(Number(trimmed) * K)
  if (!Number.isFinite(tokens) || tokens < 0) {
    return { ok: false, error: `${label}必须是 0 或更大的数字（最多 3 位小数）` }
  }
  if (tokens > MAX_CONTEXT_WINDOW_TOKENS) {
    return { ok: false, error: `${label}不能超过 ${MAX_CONTEXT_WINDOW_TOKENS / K}K` }
  }
  return { ok: true, tokens }
}

type ModelEditDialogProps = {
  model: LLMModel
  onClose: () => void
  /** Called with the fresh row after a save or reset; the dialog stays open on reset. */
  onModelChange: (model: LLMModel) => void
}

/**
 * Per-model parameter editor (R-A1-1/R-A1-10). Window/output are entered in K,
 * converted to raw tokens for the API, and the derived input budget is shown
 * read-only. Capability flags and deprecation live here instead of the provider
 * configuration dialog (R-A1-7); "restore upstream" is only available while at
 * least one numeric field carries a manual override.
 */
export function ModelEditDialog({ model, onClose, onModelChange }: ModelEditDialogProps) {
  const capabilities = modelCapabilities(model)
  const [current, setCurrent] = useState(model)
  const [windowK, setWindowK] = useState(formatTokensK(model.context_window))
  const [outputK, setOutputK] = useState(formatTokensK(model.max_output))
  const [supportsTools, setSupportsTools] = useState(capabilities.supports_tools)
  const [supportsVision, setSupportsVision] = useState(capabilities.supports_vision)
  const [supportsReasoning, setSupportsReasoning] = useState(capabilities.supports_reasoning)
  const [supportsPromptCache, setSupportsPromptCache] = useState(capabilities.supports_prompt_cache)
  const [deprecated, setDeprecated] = useState(Boolean(model.deprecated))
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [pending, setPending] = useState(false)

  const capabilityState: Record<CapabilityFlag, boolean> = {
    supports_tools: supportsTools,
    supports_vision: supportsVision,
    supports_reasoning: supportsReasoning,
    supports_prompt_cache: supportsPromptCache,
  }

  const manualSource = current.window_source === "manual" || current.output_source === "manual"

  // Live read-only input budget. While the form is untouched the server value
  // is authoritative (it knows the effective window when the stored value is 0).
  const typedWindow = Math.round(Number(windowK) * K)
  const typedOutput = Math.round(Number(outputK) * K)
  const pristine =
    Number.isFinite(typedWindow) &&
    Number.isFinite(typedOutput) &&
    typedWindow === current.context_window &&
    typedOutput === current.max_output
  const maxInput =
    pristine && typeof current.max_input === "number"
      ? current.max_input
      : deriveMaxInput(
          Number.isFinite(typedWindow) && typedWindow > 0
            ? typedWindow
            : DEFAULT_CONTEXT_WINDOW_TOKENS,
          Number.isFinite(typedOutput) && typedOutput > 0 ? typedOutput : 0,
        )

  function setCapability(key: CapabilityFlag, value: boolean): void {
    switch (key) {
      case "supports_tools":
        setSupportsTools(value)
        break
      case "supports_vision":
        setSupportsVision(value)
        break
      case "supports_reasoning":
        setSupportsReasoning(value)
        break
      case "supports_prompt_cache":
        setSupportsPromptCache(value)
        break
    }
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault()
    setError(null)
    setNotice(null)

    const window = parseKInput(windowK, "上下文窗口")
    if (!window.ok) {
      setError(window.error)
      return
    }
    const output = parseKInput(outputK, "最大输出")
    if (!output.ok) {
      setError(output.error)
      return
    }
    if (window.tokens > 0 && output.tokens > window.tokens) {
      setError("最大输出不能大于上下文窗口")
      return
    }

    const patch: UpdateLLMModelInput = {
      supports_tools: supportsTools,
      supports_vision: supportsVision,
      supports_reasoning: supportsReasoning,
      supports_prompt_cache: supportsPromptCache,
      deprecated,
    }
    // Only send numeric fields that actually changed, so editing capabilities
    // never silently flips a catalog-owned value to manual.
    if (window.tokens !== current.context_window) patch.context_window = window.tokens
    if (output.tokens !== current.max_output) patch.max_output = output.tokens

    setPending(true)
    try {
      const saved = await updateLLMModel(current.config_id, current.id, patch)
      onModelChange(saved)
      onClose()
    } catch (caught) {
      setError(caught instanceof ApiError ? caught.message : "保存模型失败，请稍后重试")
    } finally {
      setPending(false)
    }
  }

  async function handleReset(): Promise<void> {
    setError(null)
    setNotice(null)
    setPending(true)
    try {
      const reset = await resetLLMModelParams(current.config_id, current.id)
      const resetCapabilities = modelCapabilities(reset)
      setCurrent(reset)
      setWindowK(formatTokensK(reset.context_window))
      setOutputK(formatTokensK(reset.max_output))
      setSupportsTools(resetCapabilities.supports_tools)
      setSupportsVision(resetCapabilities.supports_vision)
      setSupportsReasoning(resetCapabilities.supports_reasoning)
      setSupportsPromptCache(resetCapabilities.supports_prompt_cache)
      setDeprecated(Boolean(reset.deprecated))
      onModelChange(reset)
      setNotice("已恢复上游值")
    } catch (caught) {
      setError(caught instanceof ApiError ? caught.message : "恢复上游值失败，请稍后重试")
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog
      open
      onClose={onClose}
      title="编辑模型参数"
      description={`调整「${model.model}」的上下文窗口与最大输出；1K = 1000 tokens。`}
      className="max-w-lg"
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={pending}>
            取消
          </Button>
          <Button
            variant="secondary"
            onClick={() => void handleReset()}
            disabled={pending || !manualSource}
          >
            恢复上游值
          </Button>
          <Button type="submit" form="llm-model-edit-form" disabled={pending}>
            {pending ? "保存中…" : "保存"}
          </Button>
        </>
      }
    >
      <form
        id="llm-model-edit-form"
        onSubmit={handleSubmit}
        className="flex flex-col gap-4"
        noValidate
      >
        <div className="grid gap-4 sm:grid-cols-2">
          <Field
            id="llm-model-window-k"
            label="上下文窗口 (K)"
            hint="模型总容量，输入输出共享；0 = 自动。"
          >
            <Input
              id="llm-model-window-k"
              type="number"
              min={0}
              step="0.001"
              value={windowK}
              onChange={(event) => setWindowK(event.target.value)}
            />
          </Field>
          <Field
            id="llm-model-output-k"
            label="最大输出 (K)"
            hint="单次回复上限；0 = 自动（回退 4K）。"
          >
            <Input
              id="llm-model-output-k"
              type="number"
              min={0}
              step="0.001"
              value={outputK}
              onChange={(event) => setOutputK(event.target.value)}
            />
          </Field>
        </div>

        <Field
          id="llm-model-max-input"
          label="最大输入"
          hint="只读派生：上下文窗口 − 最大输出（输出为 0 时按 4096 计）。"
        >
          <Input
            id="llm-model-max-input"
            readOnly
            value={`${formatTokensK(maxInput)}K`}
            aria-readonly="true"
            className="bg-muted"
          />
        </Field>

        <fieldset className="rounded-md border border-border px-3 py-2">
          <legend className="px-1 text-xs font-medium text-foreground">模型能力</legend>
          <div className="flex flex-wrap items-center gap-4 text-xs text-foreground">
            {CAPABILITY_FIELDS.map((field) => (
              <label key={field.key} htmlFor={field.id} className="flex items-center gap-2">
                <input
                  id={field.id}
                  type="checkbox"
                  checked={capabilityState[field.key]}
                  disabled={pending}
                  onChange={(event) => setCapability(field.key, event.target.checked)}
                />
                {field.label}
              </label>
            ))}
          </div>
          <label
            htmlFor="llm-model-deprecated"
            className="mt-2 flex items-center gap-2 text-xs text-foreground"
          >
            <input
              id="llm-model-deprecated"
              type="checkbox"
              checked={deprecated}
              disabled={pending}
              onChange={(event) => setDeprecated(event.target.checked)}
            />
            已下线（deprecated）
          </label>
        </fieldset>

        {error ? <Alert tone="danger">{error}</Alert> : null}
        {notice ? (
          <p role="status" className="text-xs text-success">
            {notice}
          </p>
        ) : null}
      </form>
    </Dialog>
  )
}
