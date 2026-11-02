"use client"

import { useCallback, useEffect, useState } from "react"

import { Button } from "@/components/ui/Button"
import { Dialog } from "@/components/ui/Dialog"
import { ErrorState } from "@/components/ui/ErrorState"
import { Input } from "@/components/ui/Input"
import { Select } from "@/components/ui/Select"
import { LoadingCard } from "@/components/ui/Spinner"
import { ApiError } from "@/lib/api-client"
import { formatDateTime } from "@/lib/format"

import { getAgentProfile, patchAgentProfile } from "./api"
import { PromptEditor } from "./PromptEditor"
import type { AgentTool, LLMModel, ProfileDetail } from "./types"

export type ProfileEditorProps = {
  profileKey: string
  tools: AgentTool[]
  models: LLMModel[]
  onClose: () => void
  /** Called after any successful profile/prompt write. */
  onSaved: () => void
}

/**
 * Admin profile drawer: model/budgets/allowed_tools configuration plus the
 * prompt editor. `enabled`, `name`, wrapup, triggers, MCP and Skill are
 * deliberately not editable in 0.2.
 */
export function ProfileEditor({ profileKey, tools, models, onClose, onSaved }: ProfileEditorProps) {
  const [detail, setDetail] = useState<ProfileDetail | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<unknown>(null)
  const [reloadToken, setReloadToken] = useState(0)

  const [model, setModel] = useState("")
  const [maxTurns, setMaxTurns] = useState("0")
  const [softSeconds, setSoftSeconds] = useState("0")
  const [hardSeconds, setHardSeconds] = useState("0")
  const [allowedTools, setAllowedTools] = useState<string[]>([])
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<unknown>(null)
  const [savedMessage, setSavedMessage] = useState("")

  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadToken re-runs the explicit refetch
  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)
    void getAgentProfile(profileKey)
      .then((next) => {
        if (cancelled) return
        setDetail(next)
        setModel(next.profile.model ?? "")
        setMaxTurns(String(next.profile.budgets.max_turns))
        setSoftSeconds(String(next.profile.budgets.soft_seconds))
        setHardSeconds(String(next.profile.budgets.hard_seconds))
        setAllowedTools([...next.profile.allowed_tools])
      })
      .catch((cause: unknown) => {
        if (!cancelled) {
          setDetail(null)
          setError(cause)
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [profileKey, reloadToken])

  const reload = useCallback(() => setReloadToken((token) => token + 1), [])

  async function saveConfig(): Promise<void> {
    if (detail === null) return
    setSaving(true)
    setSaveError(null)
    setSavedMessage("")
    try {
      const updated = await patchAgentProfile(profileKey, {
        model: model.trim().length > 0 ? model.trim() : null,
        budgets: {
          max_turns: parseNonNegative(maxTurns),
          soft_seconds: parseNonNegative(softSeconds),
          hard_seconds: parseNonNegative(hardSeconds),
        },
        allowed_tools: allowedTools,
      })
      setDetail((current) => (current === null ? current : { ...current, profile: updated }))
      setSavedMessage("配置已保存；下一次 run 生效，正在运行的 run 不受影响。")
      onSaved()
    } catch (cause) {
      setSaveError(cause)
    } finally {
      setSaving(false)
    }
  }

  function toggleTool(name: string): void {
    setAllowedTools((current) =>
      current.includes(name) ? current.filter((item) => item !== name) : [...current, name],
    )
  }

  const toolOptions = mergeToolOptions(tools, detail?.profile.allowed_tools ?? [])
  const modelOptions = mergeModelOptions(models, detail?.profile.model ?? null)

  return (
    <Dialog
      open
      onClose={onClose}
      title={detail === null ? `编辑 ${profileKey}` : `编辑 ${detail.profile.name}`}
      description="只编辑 model / budgets / allowed_tools 与提示词；保存后下一次 run 生效。"
      className="max-w-4xl"
    >
      {loading && detail === null ? (
        <LoadingCard label="正在加载 profile 配置…" />
      ) : error !== null ? (
        <ErrorState error={error} title="profile 加载失败" onRetry={reload} />
      ) : detail !== null ? (
        <div className="max-h-[70vh] space-y-5 overflow-y-auto pr-1">
          <section
            data-testid="agent-center-profile-config"
            aria-label="profile 配置"
            className="space-y-3"
          >
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
              <label
                htmlFor="agent-center-model"
                className="flex flex-col gap-1 text-[11px] text-muted-foreground"
              >
                模型
                <Select
                  id="agent-center-model"
                  data-testid="agent-center-model"
                  value={model}
                  onChange={(event) => setModel(event.target.value)}
                  className="h-8 text-xs"
                >
                  <option value="">默认模型（跟随全局 LLM 配置）</option>
                  {modelOptions.map((option) => (
                    <option key={option.value} value={option.value}>
                      {option.label}
                    </option>
                  ))}
                </Select>
              </label>
              <div className="flex flex-col gap-1 text-[11px] text-muted-foreground">
                <span>预算（0 = 跟随全局）</span>
                <div className="grid grid-cols-3 gap-1.5">
                  <Input
                    data-testid="agent-center-budget-max-turns"
                    type="number"
                    min={0}
                    aria-label="max_turns"
                    value={maxTurns}
                    onChange={(event) => setMaxTurns(event.target.value)}
                    className="h-8 text-xs"
                  />
                  <Input
                    data-testid="agent-center-budget-soft-seconds"
                    type="number"
                    min={0}
                    aria-label="soft_seconds"
                    value={softSeconds}
                    onChange={(event) => setSoftSeconds(event.target.value)}
                    className="h-8 text-xs"
                  />
                  <Input
                    data-testid="agent-center-budget-hard-seconds"
                    type="number"
                    min={0}
                    aria-label="hard_seconds"
                    value={hardSeconds}
                    onChange={(event) => setHardSeconds(event.target.value)}
                    className="h-8 text-xs"
                  />
                </div>
              </div>
            </div>

            <fieldset className="space-y-1">
              <legend className="text-[11px] text-muted-foreground">
                允许的工具（来自 P5 tool registry，保存时为完整覆盖）
              </legend>
              {toolOptions.length === 0 ? (
                <p className="text-[11px] text-muted-foreground">工具目录为空。</p>
              ) : (
                <div className="grid max-h-40 grid-cols-1 gap-1 overflow-y-auto rounded-md border border-border bg-card p-2 sm:grid-cols-2">
                  {toolOptions.map((tool) => (
                    <label
                      key={tool.name}
                      title={tool.description}
                      className="flex items-start gap-2 text-[11px] text-foreground"
                    >
                      <input
                        type="checkbox"
                        data-testid={`agent-center-tool-${tool.name}`}
                        checked={allowedTools.includes(tool.name)}
                        onChange={() => toggleTool(tool.name)}
                        className="mt-0.5 size-3.5"
                      />
                      <span className="font-mono">{tool.name}</span>
                    </label>
                  ))}
                </div>
              )}
            </fieldset>

            <div className="flex flex-wrap items-center gap-2">
              <Button
                data-testid="agent-center-profile-save"
                size="sm"
                disabled={saving}
                onClick={() => void saveConfig()}
              >
                {saving ? "保存中…" : "保存配置"}
              </Button>
              <span className="text-[11px] text-muted-foreground">
                当前版本 v{detail.prompt_version} · 更新于{" "}
                {formatDateTime(detail.profile.updated_at)}
              </span>
            </div>

            {saveError !== null ? (
              <p
                data-testid="agent-center-config-error"
                role="alert"
                className="text-xs text-destructive"
              >
                {describeConfigError(saveError)}
              </p>
            ) : savedMessage.length > 0 ? (
              <p data-testid="agent-center-profile-saved" className="text-xs text-success">
                {savedMessage}
              </p>
            ) : null}
          </section>

          <PromptEditor
            detail={detail}
            onMutated={() => {
              reload()
              onSaved()
            }}
          />
        </div>
      ) : null}
    </Dialog>
  )
}

function parseNonNegative(value: string): number {
  const parsed = Number(value)
  if (!Number.isFinite(parsed) || parsed < 0) return 0
  return Math.trunc(parsed)
}

function mergeToolOptions(tools: AgentTool[], allowed: string[]) {
  const byName = new Map(tools.map((tool) => [tool.name, tool]))
  const options = [...tools]
  for (const name of allowed) {
    if (!byName.has(name)) {
      options.push({
        name,
        description: "未在工具注册表中（保存前请移除）",
        parameters: {},
      })
    }
  }
  return options
}

function mergeModelOptions(models: LLMModel[], current: string | null) {
  const options = models.map((model) => ({
    value: model.model,
    label:
      model.display_name.trim().length > 0
        ? `${model.display_name}（${model.model}）`
        : model.model,
  }))
  if (
    current !== null &&
    current.length > 0 &&
    !options.some((option) => option.value === current)
  ) {
    options.unshift({ value: current, label: `${current}（当前值）` })
  }
  return options
}

function describeConfigError(error: unknown): string {
  if (error instanceof ApiError) {
    const field = typeof error.details.field === "string" ? error.details.field : ""
    return field.length > 0 ? `${field}: ${error.message}` : error.message
  }
  if (error instanceof Error) return error.message
  return "保存失败，请稍后重试"
}
