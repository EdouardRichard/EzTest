"use client"

import { Save } from "lucide-react"
import { type FormEvent, useId, useState } from "react"

import { ApiError } from "@/lib/api-client"

import type { AgentProfile, AgentTool, ProfilePatch } from "./agent-api"
import { updateProfile } from "./agent-api"

export type ProfileEditorProps = {
  profile: AgentProfile
  tools: AgentTool[]
  onSaved: (profile: AgentProfile) => void
}

type Thinking = AgentProfile["thinking"]
type ReasoningEffort = AgentProfile["reasoning_effort"]

type FormState = {
  systemPrompt: string
  model: string
  maxTurns: string
  runSeconds: string
  hardTimeoutSeconds: string
  thinking: Thinking
  reasoningEffort: ReasoningEffort
  maxOutputTokens: string
  allowedTools: string[]
  enabled: boolean
}

type FieldKey =
  | "systemPrompt"
  | "maxTurns"
  | "runSeconds"
  | "hardTimeoutSeconds"
  | "maxOutputTokens"
  | "reasoningEffort"

type FieldErrors = Partial<Record<FieldKey, string>>

type ValidationResult = {
  errors: FieldErrors
  patch: ProfilePatch
}

const THINKING_OPTIONS: { value: Thinking; label: string }[] = [
  { value: "auto", label: "自动（auto）" },
  { value: "enabled", label: "开启（enabled）" },
  { value: "disabled", label: "关闭（disabled）" },
]

const EFFORT_OPTIONS: { value: ReasoningEffort; label: string }[] = [
  { value: "", label: "未指定" },
  { value: "none", label: "none" },
  { value: "low", label: "low" },
  { value: "high", label: "high" },
  { value: "max", label: "max" },
]

const LABEL_CLASS = "text-xs font-medium text-muted-foreground"
const FIELD_CLASS =
  "mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-60"
const NUMBER_FIELD_CLASS = `${FIELD_CLASS} font-mono`
const HINT_CLASS = "mt-1 text-[11px] text-muted-foreground"
const ERROR_CLASS = "mt-1 text-xs text-destructive"

function formFromProfile(profile: AgentProfile): FormState {
  const effort =
    profile.thinking === "disabled" &&
    profile.reasoning_effort !== "" &&
    profile.reasoning_effort !== "none"
      ? "none"
      : profile.reasoning_effort
  return {
    systemPrompt: profile.system_prompt,
    model: profile.model,
    maxTurns: String(profile.max_turns),
    runSeconds: String(profile.run_seconds),
    hardTimeoutSeconds: String(profile.hard_timeout_seconds),
    thinking: profile.thinking,
    reasoningEffort: effort,
    maxOutputTokens: String(profile.max_output_tokens),
    allowedTools: [...profile.allowed_tools],
    enabled: profile.enabled,
  }
}

/** Parses a base-10 integer field, returning null for empty/invalid input. */
function parseInteger(value: string): number | null {
  const trimmed = value.trim()
  if (!/^\d+$/.test(trimmed)) return null
  const parsed = Number(trimmed)
  return Number.isSafeInteger(parsed) ? parsed : null
}

/** Compares two tool name lists ignoring order. */
function sameToolSet(left: string[], right: string[]): boolean {
  if (left.length !== right.length) return false
  const known = new Set(right)
  return left.every((name) => known.has(name))
}

/**
 * Validates against the server's profile ranges (spec §10.3) and computes the
 * minimal PATCH body. The server remains authoritative; these checks only avoid
 * a round trip for values the API would reject.
 */
function validateForm(form: FormState, baseline: AgentProfile): ValidationResult {
  const errors: FieldErrors = {}
  const patch: ProfilePatch = {}

  const prompt = form.systemPrompt
  if (prompt.trim().length === 0) {
    errors.systemPrompt = "系统提示词不能为空"
  } else if (prompt.length > 32768) {
    errors.systemPrompt = "系统提示词不能超过 32768 个字符"
  } else if (prompt !== baseline.system_prompt) {
    patch.system_prompt = prompt
  }

  if (form.model !== baseline.model) patch.model = form.model

  const maxTurns = parseInteger(form.maxTurns)
  if (maxTurns === null || maxTurns < 0 || maxTurns > 100) {
    errors.maxTurns = "最大回合必须是 0 到 100 之间的整数"
  } else if (maxTurns !== baseline.max_turns) {
    patch.max_turns = maxTurns
  }

  const runSeconds = parseInteger(form.runSeconds)
  if (runSeconds === null || runSeconds < 30 || runSeconds > 3600) {
    errors.runSeconds = "运行时长（秒）必须是 30 到 3600 之间的整数"
  } else if (runSeconds !== baseline.run_seconds) {
    patch.run_seconds = runSeconds
  }

  const hardTimeoutSeconds = parseInteger(form.hardTimeoutSeconds)
  if (hardTimeoutSeconds === null || hardTimeoutSeconds < 5 || hardTimeoutSeconds > 300) {
    errors.hardTimeoutSeconds = "硬超时（秒）必须是 5 到 300 之间的整数"
  } else if (hardTimeoutSeconds !== baseline.hard_timeout_seconds) {
    patch.hard_timeout_seconds = hardTimeoutSeconds
  }

  const maxOutputTokens = parseInteger(form.maxOutputTokens)
  if (maxOutputTokens === null || maxOutputTokens < 1024 || maxOutputTokens > 131072) {
    errors.maxOutputTokens = "输出上限必须是 1024 到 131072 之间的整数"
  } else if (maxOutputTokens !== baseline.max_output_tokens) {
    patch.max_output_tokens = maxOutputTokens
  }

  if (form.thinking !== baseline.thinking) patch.thinking = form.thinking

  if (
    form.thinking === "disabled" &&
    form.reasoningEffort !== "" &&
    form.reasoningEffort !== "none"
  ) {
    errors.reasoningEffort = "思考模式为 disabled 时，思考强度只能为 none 或留空"
  } else if (form.reasoningEffort !== baseline.reasoning_effort) {
    patch.reasoning_effort = form.reasoningEffort
  }

  if (!sameToolSet(form.allowedTools, baseline.allowed_tools)) {
    patch.allowed_tools = [...form.allowedTools]
  }

  if (form.enabled !== baseline.enabled) patch.enabled = form.enabled

  return { errors, patch }
}

function describeSaveError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 0) return "无法连接 API 服务，请确认后端已启动。"
    return error.message
  }
  return error instanceof Error ? error.message : "未知错误"
}

/**
 * Builds the editable tool list from the registered catalog plus any tool the
 * profile already allows. Keeping unknown names visible prevents a failed or
 * stale catalog request from silently dropping tool permissions on save.
 */
function toolOptions(tools: AgentTool[], names: string[]): AgentTool[] {
  const known = new Set(tools.map((tool) => tool.name))
  const extras = [...new Set(names)]
    .filter((name) => !known.has(name))
    .map((name) => ({ name, description: "未在工具注册表中", schema: null }))
  return [...tools, ...extras]
}

/**
 * Editable agent profile form. Only the fields the user actually changed are
 * sent to PATCH /api/agents/profiles/{id}; a successful save means the change
 * applies to the next run, never to a run already in flight.
 */
export function ProfileEditor({ profile, tools, onSaved }: ProfileEditorProps) {
  const formId = useId()
  const [baseline, setBaseline] = useState(profile)
  const [form, setForm] = useState<FormState>(() => formFromProfile(profile))
  const [errors, setErrors] = useState<FieldErrors>({})
  const [error, setError] = useState("")
  const [notice, setNotice] = useState("")
  const [saving, setSaving] = useState(false)

  function edit(changes: Partial<FormState>, keys: FieldKey[] = []) {
    setForm((previous) => ({ ...previous, ...changes }))
    if (keys.length > 0) {
      setErrors((previous) => {
        const next = { ...previous }
        for (const key of keys) delete next[key]
        return next
      })
    }
    setError("")
    setNotice("")
  }

  function changeThinking(thinking: Thinking) {
    const keepEffort = form.reasoningEffort === "" ? "" : "none"
    edit(thinking === "disabled" ? { thinking, reasoningEffort: keepEffort } : { thinking }, [
      "reasoningEffort",
    ])
  }

  function toggleTool(name: string, checked: boolean) {
    setForm((previous) => ({
      ...previous,
      allowedTools: checked
        ? [...previous.allowedTools, name]
        : previous.allowedTools.filter((tool) => tool !== name),
    }))
    setError("")
    setNotice("")
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (saving) return
    setError("")
    setNotice("")

    const result = validateForm(form, baseline)
    setErrors(result.errors)
    if (Object.keys(result.errors).length > 0) return

    if (Object.keys(result.patch).length === 0) {
      setNotice("没有需要保存的修改")
      return
    }

    setSaving(true)
    try {
      const saved = await updateProfile(baseline.id, result.patch)
      setBaseline(saved)
      setForm(formFromProfile(saved))
      setErrors({})
      setNotice("下次运行生效")
      onSaved(saved)
    } catch (caught) {
      setError(describeSaveError(caught))
    } finally {
      setSaving(false)
    }
  }

  const options = toolOptions(tools, [...baseline.allowed_tools, ...form.allowedTools])
  const hasFieldErrors = Object.keys(errors).length > 0

  return (
    <form
      onSubmit={(event) => void handleSubmit(event)}
      noValidate
      className="space-y-4 rounded-2xl border border-border bg-card p-4 shadow-sm"
      aria-label={`配置 ${profile.name}`}
    >
      <div>
        <h2 className="text-sm font-semibold">配置：{profile.name}</h2>
        <p className="mt-1 text-xs text-muted-foreground">
          保存只影响下一次运行；正在运行的会话继续使用启动时的配置。
        </p>
      </div>

      {hasFieldErrors ? (
        <p
          role="alert"
          className="rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
        >
          请修正表单中的错误后再保存。
        </p>
      ) : null}
      {error ? (
        <p
          role="alert"
          className="rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
        >
          {error}
        </p>
      ) : null}
      {notice ? (
        <p
          role="status"
          className="rounded-lg border border-emerald-500/25 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-700 dark:text-emerald-300"
        >
          {notice}
        </p>
      ) : null}

      <div>
        <label htmlFor={`${formId}-system-prompt`} className={LABEL_CLASS}>
          系统提示词
        </label>
        <textarea
          id={`${formId}-system-prompt`}
          value={form.systemPrompt}
          onChange={(event) => edit({ systemPrompt: event.target.value }, ["systemPrompt"])}
          rows={8}
          aria-invalid={errors.systemPrompt ? true : undefined}
          className={`${FIELD_CLASS} resize-y`}
        />
        {errors.systemPrompt ? <p className={ERROR_CLASS}>{errors.systemPrompt}</p> : null}
      </div>

      <div className="grid gap-3 sm:grid-cols-2">
        <div>
          <label htmlFor={`${formId}-model`} className={LABEL_CLASS}>
            模型
          </label>
          <input
            id={`${formId}-model`}
            value={form.model}
            onChange={(event) => edit({ model: event.target.value })}
            placeholder="留空使用默认模型"
            autoComplete="off"
            className={FIELD_CLASS}
          />
        </div>
        <div>
          <label htmlFor={`${formId}-max-turns`} className={LABEL_CLASS}>
            最大回合
          </label>
          <input
            id={`${formId}-max-turns`}
            type="number"
            inputMode="numeric"
            min={0}
            max={100}
            value={form.maxTurns}
            onChange={(event) => edit({ maxTurns: event.target.value }, ["maxTurns"])}
            aria-invalid={errors.maxTurns ? true : undefined}
            className={NUMBER_FIELD_CLASS}
          />
          {errors.maxTurns ? <p className={ERROR_CLASS}>{errors.maxTurns}</p> : null}
        </div>
        <div>
          <label htmlFor={`${formId}-run-seconds`} className={LABEL_CLASS}>
            运行时长（秒）
          </label>
          <input
            id={`${formId}-run-seconds`}
            type="number"
            inputMode="numeric"
            min={30}
            max={3600}
            value={form.runSeconds}
            onChange={(event) => edit({ runSeconds: event.target.value }, ["runSeconds"])}
            aria-invalid={errors.runSeconds ? true : undefined}
            className={NUMBER_FIELD_CLASS}
          />
          {errors.runSeconds ? <p className={ERROR_CLASS}>{errors.runSeconds}</p> : null}
        </div>
        <div>
          <label htmlFor={`${formId}-hard-timeout`} className={LABEL_CLASS}>
            硬超时（秒）
          </label>
          <input
            id={`${formId}-hard-timeout`}
            type="number"
            inputMode="numeric"
            min={5}
            max={300}
            value={form.hardTimeoutSeconds}
            onChange={(event) =>
              edit({ hardTimeoutSeconds: event.target.value }, ["hardTimeoutSeconds"])
            }
            aria-invalid={errors.hardTimeoutSeconds ? true : undefined}
            className={NUMBER_FIELD_CLASS}
          />
          {errors.hardTimeoutSeconds ? (
            <p className={ERROR_CLASS}>{errors.hardTimeoutSeconds}</p>
          ) : null}
        </div>
        <div>
          <label htmlFor={`${formId}-thinking`} className={LABEL_CLASS}>
            思考模式
          </label>
          <select
            id={`${formId}-thinking`}
            value={form.thinking}
            onChange={(event) => changeThinking(event.target.value as Thinking)}
            className={FIELD_CLASS}
          >
            {THINKING_OPTIONS.map((option) => (
              <option key={option.value} value={option.value}>
                {option.label}
              </option>
            ))}
          </select>
        </div>
        <div>
          <label htmlFor={`${formId}-effort`} className={LABEL_CLASS}>
            思考强度
          </label>
          <select
            id={`${formId}-effort`}
            value={form.reasoningEffort}
            onChange={(event) =>
              edit({ reasoningEffort: event.target.value as ReasoningEffort }, ["reasoningEffort"])
            }
            disabled={form.thinking === "disabled"}
            aria-invalid={errors.reasoningEffort ? true : undefined}
            className={FIELD_CLASS}
          >
            {EFFORT_OPTIONS.map((option) => (
              <option key={option.value} value={option.value}>
                {option.label}
              </option>
            ))}
          </select>
          {form.thinking === "disabled" ? (
            <p className={HINT_CLASS}>
              思考模式为 disabled 时，思考强度不可用（只能为 none 或留空）。
            </p>
          ) : null}
          {errors.reasoningEffort ? <p className={ERROR_CLASS}>{errors.reasoningEffort}</p> : null}
        </div>
        <div>
          <label htmlFor={`${formId}-max-output-tokens`} className={LABEL_CLASS}>
            输出上限
          </label>
          <input
            id={`${formId}-max-output-tokens`}
            type="number"
            inputMode="numeric"
            min={1024}
            max={131072}
            value={form.maxOutputTokens}
            onChange={(event) => edit({ maxOutputTokens: event.target.value }, ["maxOutputTokens"])}
            aria-invalid={errors.maxOutputTokens ? true : undefined}
            className={NUMBER_FIELD_CLASS}
          />
          {errors.maxOutputTokens ? <p className={ERROR_CLASS}>{errors.maxOutputTokens}</p> : null}
        </div>
      </div>

      <fieldset>
        <legend className={LABEL_CLASS}>允许工具</legend>
        <p className={HINT_CLASS}>仅勾选的工具会暴露给该 profile 的下一次运行。</p>
        <ul className="mt-2 grid gap-2 sm:grid-cols-2">
          {options.map((tool) => (
            <li key={tool.name} className="min-w-0">
              <label className="flex items-center gap-2 text-sm">
                <input
                  type="checkbox"
                  checked={form.allowedTools.includes(tool.name)}
                  onChange={(event) => toggleTool(tool.name, event.target.checked)}
                  className="h-4 w-4 rounded border-border"
                />
                <span className="font-mono text-xs">{tool.name}</span>
              </label>
              <p className="ml-6 text-[11px] text-muted-foreground">{tool.description}</p>
            </li>
          ))}
        </ul>
      </fieldset>

      <div className="flex flex-wrap items-center justify-between gap-3 border-t border-border pt-3">
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={form.enabled}
            onChange={(event) => edit({ enabled: event.target.checked })}
            className="h-4 w-4 rounded border-border"
          />
          启用
        </label>
        <button
          type="submit"
          disabled={saving}
          aria-busy={saving || undefined}
          className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3.5 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <Save className="h-4 w-4" aria-hidden="true" />
          保存配置
        </button>
      </div>
    </form>
  )
}
