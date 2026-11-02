"use client"

import type { FormEvent } from "react"
import { useEffect, useState } from "react"

import { Alert } from "@/components/ui/Alert"
import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { Dialog } from "@/components/ui/Dialog"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState, InlineError } from "@/components/ui/ErrorState"
import { Field } from "@/components/ui/Field"
import { Input } from "@/components/ui/Input"
import { Select } from "@/components/ui/Select"
import { LoadingCard } from "@/components/ui/Spinner"
import { Textarea } from "@/components/ui/Textarea"
import { ApiError } from "@/lib/api-client"
import { formatDateTime } from "@/lib/format"

import {
  type AddLLMModelInput,
  addLLMModel,
  createLLMConfig,
  deleteLLMConfig,
  deleteLLMModel,
  fetchLLMConfigs,
  fetchLLMModels,
  type LLMConfig,
  type LLMConfigInput,
  type LLMModel,
  type LLMProtocol,
  type LLMStatus,
  type LLMTestResult,
  refreshLLMModels,
  testLLMConfig,
  updateLLMConfig,
} from "./api"

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

function errorText(caught: unknown, fallback: string): string {
  return caught instanceof ApiError ? caught.message : fallback
}

type ConfigParseResult = { ok: true; input: LLMConfigInput } | { ok: false; error: string }

function parseHeaders(
  text: string,
): { ok: true; value: Record<string, string> } | { ok: false; error: string } {
  const trimmed = text.trim()
  if (trimmed.length === 0) return { ok: true, value: {} }
  let parsed: unknown
  try {
    parsed = JSON.parse(trimmed) as unknown
  } catch {
    return { ok: false, error: "Headers 必须是 JSON 对象" }
  }
  if (!isRecord(parsed)) return { ok: false, error: "Headers 必须是 JSON 对象" }

  const headers: Record<string, string> = {}
  for (const [name, value] of Object.entries(parsed)) {
    const headerName = name.trim()
    if (headerName.length === 0) return { ok: false, error: "Header 名称不能为空" }
    if (typeof value !== "string") return { ok: false, error: "Headers 的值必须是字符串" }
    if (headerName.toLowerCase() === "authorization") {
      return { ok: false, error: "Authorization 由 API Key 管理，请勿在 Headers 中设置" }
    }
    headers[headerName] = value.trim()
  }
  return { ok: true, value: headers }
}

type ConfigFormDialogProps = {
  config: LLMConfig | null
  onClose: () => void
  onSaved: (saved: LLMConfig, created: boolean) => void
}

function ConfigFormDialog({ config, onClose, onSaved }: ConfigFormDialogProps) {
  const [name, setName] = useState(config?.name ?? "")
  const [protocol, setProtocol] = useState<LLMProtocol>(config?.protocol ?? "openai_compatible")
  const [baseUrl, setBaseUrl] = useState(config?.base_url ?? "")
  const [apiKey, setApiKey] = useState("")
  const [headers, setHeaders] = useState(JSON.stringify(config?.headers ?? {}, null, 2))
  const [timeoutSeconds, setTimeoutSeconds] = useState(String(config?.timeout_seconds ?? 60))
  const [maxRetries, setMaxRetries] = useState(String(config?.max_retries ?? 2))
  const [isDefault, setIsDefault] = useState(config?.is_default ?? false)
  const [status, setStatus] = useState<LLMStatus>(config?.status ?? "active")
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState(false)

  function buildInput(): ConfigParseResult {
    const trimmedName = name.trim()
    if (trimmedName.length === 0) return { ok: false, error: "请输入配置名称" }
    if (trimmedName.length > 100) {
      return { ok: false, error: "配置名称不能超过 100 个字符" }
    }

    const trimmedUrl = baseUrl.trim()
    if (trimmedUrl.length === 0) return { ok: false, error: "请输入 Base URL" }
    let parsedUrl: URL
    try {
      parsedUrl = new URL(trimmedUrl)
    } catch {
      return { ok: false, error: "请输入合法的 http(s) URL" }
    }
    if (parsedUrl.protocol !== "http:" && parsedUrl.protocol !== "https:") {
      return { ok: false, error: "请输入合法的 http(s) URL" }
    }

    const timeout = Number(timeoutSeconds.trim())
    if (!Number.isInteger(timeout) || timeout < 1 || timeout > 600) {
      return { ok: false, error: "超时必须是 1–600 的整数" }
    }
    const retries = Number(maxRetries.trim())
    if (!Number.isInteger(retries) || retries < 0 || retries > 10) {
      return { ok: false, error: "最大重试必须是 0–10 的整数" }
    }

    const parsedHeaders = parseHeaders(headers)
    if (!parsedHeaders.ok) return parsedHeaders

    const input: LLMConfigInput = {
      name: trimmedName,
      base_url: trimmedUrl,
      protocol,
      headers: parsedHeaders.value,
      timeout_seconds: timeout,
      max_retries: retries,
      is_default: isDefault,
      status,
    }
    if (apiKey.length > 0) input.api_key = apiKey
    return { ok: true, input }
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault()
    setError(null)
    const parsed = buildInput()
    if (!parsed.ok) {
      setError(parsed.error)
      return
    }

    setPending(true)
    try {
      const created = config === null
      const saved = created
        ? await createLLMConfig(parsed.input)
        : await updateLLMConfig(config.id, parsed.input)
      onSaved(saved, created)
    } catch (caught) {
      setError(errorText(caught, "保存失败，请稍后重试"))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog
      open
      onClose={onClose}
      title={config ? "编辑 LLM 配置" : "新建 LLM 配置"}
      description="API Key 只写入、不回显；留空表示保持当前值。"
      className="max-w-2xl"
    >
      <form onSubmit={handleSubmit} className="flex flex-col gap-4" noValidate>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field id="llm-name" label="配置名称" required>
            <Input
              id="llm-name"
              value={name}
              maxLength={100}
              onChange={(event) => setName(event.target.value)}
            />
          </Field>
          <Field id="llm-protocol" label="协议">
            <Select
              id="llm-protocol"
              value={protocol}
              onChange={(event) => setProtocol(event.target.value as LLMProtocol)}
            >
              <option value="openai_compatible">OpenAI 兼容</option>
              <option value="openai">OpenAI</option>
            </Select>
          </Field>
        </div>

        <Field id="llm-base-url" label="Base URL" required hint="例如 http://127.0.0.1:19000/v1">
          <Input
            id="llm-base-url"
            value={baseUrl}
            placeholder="http://127.0.0.1:19000/v1"
            onChange={(event) => setBaseUrl(event.target.value)}
          />
        </Field>

        <Field
          id="llm-api-key"
          label="API Key"
          hint={config ? "留空保持不变；密钥不会回显" : "可选；保存后仅显示是否已设置，永不回显"}
        >
          <Input
            id="llm-api-key"
            type="password"
            autoComplete="new-password"
            value={apiKey}
            placeholder={config?.api_key_set ? "已设置，留空保持不变" : "sk-…"}
            onChange={(event) => setApiKey(event.target.value)}
          />
        </Field>

        <div className="grid gap-4 sm:grid-cols-3">
          <Field id="llm-timeout" label="超时（秒）">
            <Input
              id="llm-timeout"
              type="number"
              min={1}
              max={600}
              value={timeoutSeconds}
              onChange={(event) => setTimeoutSeconds(event.target.value)}
            />
          </Field>
          <Field id="llm-retries" label="最大重试">
            <Input
              id="llm-retries"
              type="number"
              min={0}
              max={10}
              value={maxRetries}
              onChange={(event) => setMaxRetries(event.target.value)}
            />
          </Field>
          <Field id="llm-status" label="状态">
            <Select
              id="llm-status"
              value={status}
              onChange={(event) => setStatus(event.target.value as LLMStatus)}
            >
              <option value="active">启用</option>
              <option value="disabled">禁用</option>
            </Select>
          </Field>
        </div>

        <Field
          id="llm-headers"
          label="额外 Headers"
          hint="JSON 对象，键和值均为字符串；Authorization 请使用 API Key。"
        >
          <Textarea
            id="llm-headers"
            className="min-h-20 font-mono text-xs"
            value={headers}
            onChange={(event) => setHeaders(event.target.value)}
          />
        </Field>

        <label htmlFor="llm-default" className="flex items-center gap-2 text-xs text-foreground">
          <input
            id="llm-default"
            type="checkbox"
            checked={isDefault}
            onChange={(event) => setIsDefault(event.target.checked)}
          />
          设为默认配置
        </label>

        {error ? <Alert tone="danger">{error}</Alert> : null}

        <div className="flex items-center justify-end gap-2 pt-1">
          <Button variant="secondary" onClick={onClose} disabled={pending}>
            取消
          </Button>
          <Button type="submit" disabled={pending}>
            {pending ? "保存中…" : config ? "保存配置" : "创建配置"}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}

type ModelDialogProps = {
  config: LLMConfig
  onClose: () => void
  onSaved: (model: LLMModel) => void
}

function ModelDialog({ config, onClose, onSaved }: ModelDialogProps) {
  const [modelName, setModelName] = useState("")
  const [contextWindow, setContextWindow] = useState("0")
  const [maxOutput, setMaxOutput] = useState("0")
  const [supportsTools, setSupportsTools] = useState(false)
  const [supportsVision, setSupportsVision] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState(false)

  async function handleSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault()
    setError(null)

    const trimmed = modelName.trim()
    if (trimmed.length === 0) {
      setError("请输入模型名称")
      return
    }
    const context = Number(contextWindow.trim())
    if (!Number.isInteger(context) || context < 0) {
      setError("上下文窗口必须是 0 或更大的整数")
      return
    }
    const output = Number(maxOutput.trim())
    if (!Number.isInteger(output) || output < 0) {
      setError("最大输出必须是 0 或更大的整数")
      return
    }

    const input: AddLLMModelInput = {
      model: trimmed,
      context_window: context,
      max_output: output,
      supports_tools: supportsTools,
      supports_vision: supportsVision,
    }

    setPending(true)
    try {
      onSaved(await addLLMModel(config.id, input))
    } catch (caught) {
      setError(errorText(caught, "新增模型失败，请稍后重试"))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog
      open
      onClose={onClose}
      title="新增模型"
      description={`为「${config.name}」手工添加模型，来源标记为 manual。`}
    >
      <form onSubmit={handleSubmit} className="flex flex-col gap-4" noValidate>
        <Field id="llm-model-name" label="模型名称" required>
          <Input
            id="llm-model-name"
            value={modelName}
            placeholder="fake-model"
            onChange={(event) => setModelName(event.target.value)}
          />
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field id="llm-model-context" label="上下文窗口">
            <Input
              id="llm-model-context"
              type="number"
              min={0}
              value={contextWindow}
              onChange={(event) => setContextWindow(event.target.value)}
            />
          </Field>
          <Field id="llm-model-output" label="最大输出">
            <Input
              id="llm-model-output"
              type="number"
              min={0}
              value={maxOutput}
              onChange={(event) => setMaxOutput(event.target.value)}
            />
          </Field>
        </div>
        <div className="flex flex-wrap gap-4 text-xs text-foreground">
          <label htmlFor="llm-model-tools" className="flex items-center gap-2">
            <input
              id="llm-model-tools"
              type="checkbox"
              checked={supportsTools}
              onChange={(event) => setSupportsTools(event.target.checked)}
            />
            支持工具
          </label>
          <label htmlFor="llm-model-vision" className="flex items-center gap-2">
            <input
              id="llm-model-vision"
              type="checkbox"
              checked={supportsVision}
              onChange={(event) => setSupportsVision(event.target.checked)}
            />
            支持视觉
          </label>
        </div>

        {error ? <Alert tone="danger">{error}</Alert> : null}

        <div className="flex items-center justify-end gap-2 pt-1">
          <Button variant="secondary" onClick={onClose} disabled={pending}>
            取消
          </Button>
          <Button type="submit" disabled={pending}>
            {pending ? "添加中…" : "添加模型"}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}

/** LLM configuration catalogue: CRUD, connectivity, model refresh and manual models. */
export function LLMSettings() {
  const [configs, setConfigs] = useState<LLMConfig[] | null>(null)
  const [models, setModels] = useState<LLMModel[]>([])
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState<unknown>(null)
  const [reloadKey, setReloadKey] = useState(0)

  const [formState, setFormState] = useState<
    { mode: "create" } | { mode: "edit"; config: LLMConfig } | null
  >(null)
  const [deleteTarget, setDeleteTarget] = useState<LLMConfig | null>(null)
  const [modelTarget, setModelTarget] = useState<LLMConfig | null>(null)
  const [deleting, setDeleting] = useState(false)
  const [deleteError, setDeleteError] = useState<unknown>(null)

  const [expandedId, setExpandedId] = useState<string | null>(null)
  const [busyKey, setBusyKey] = useState<string | null>(null)
  const [actionErrors, setActionErrors] = useState<Record<string, unknown>>({})
  const [notices, setNotices] = useState<Record<string, string>>({})
  const [testResults, setTestResults] = useState<Record<string, LLMTestResult>>({})

  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadKey retriggers reload
  useEffect(() => {
    let active = true
    setLoading(true)
    setLoadError(null)
    Promise.all([fetchLLMConfigs(), fetchLLMModels()])
      .then(([configRows, modelRows]) => {
        if (!active) return
        setConfigs(configRows)
        setModels(modelRows)
      })
      .catch((caught: unknown) => {
        if (active) setLoadError(caught)
      })
      .finally(() => {
        if (active) setLoading(false)
      })
    return () => {
      active = false
    }
  }, [reloadKey])

  function clearActions(id: string): void {
    setActionErrors((current) => {
      const next = { ...current }
      delete next[id]
      return next
    })
    setNotices((current) => {
      const next = { ...current }
      delete next[id]
      return next
    })
  }

  function setError(id: string, caught: unknown): void {
    setActionErrors((current) => ({ ...current, [id]: caught }))
    setNotices((current) => {
      const next = { ...current }
      delete next[id]
      return next
    })
  }

  function setNotice(id: string, text: string): void {
    setNotices((current) => ({ ...current, [id]: text }))
    setActionErrors((current) => {
      const next = { ...current }
      delete next[id]
      return next
    })
  }

  async function handleTest(config: LLMConfig): Promise<void> {
    setBusyKey(`test:${config.id}`)
    clearActions(config.id)
    try {
      const result = await testLLMConfig(config.id)
      setTestResults((current) => ({ ...current, [config.id]: result }))
    } catch (caught) {
      setError(config.id, caught)
    } finally {
      setBusyKey(null)
    }
  }

  async function handleRefresh(config: LLMConfig): Promise<void> {
    setBusyKey(`refresh:${config.id}`)
    clearActions(config.id)
    try {
      const refreshed = await refreshLLMModels(config.id)
      setModels((current) => [
        ...current.filter((item) => item.config_id !== config.id),
        ...refreshed,
      ])
      setNotice(config.id, `已刷新 ${refreshed.length} 个模型`)
    } catch (caught) {
      setError(config.id, caught)
    } finally {
      setBusyKey(null)
    }
  }

  async function handleDeleteModel(config: LLMConfig, model: LLMModel): Promise<void> {
    setBusyKey(`model:${model.id}`)
    clearActions(config.id)
    try {
      await deleteLLMModel(config.id, model.id)
      setModels((current) => current.filter((item) => item.id !== model.id))
      setNotice(config.id, `已删除模型 ${model.model}`)
    } catch (caught) {
      setError(config.id, caught)
    } finally {
      setBusyKey(null)
    }
  }

  async function confirmDelete(): Promise<void> {
    if (deleteTarget === null) return
    setDeleting(true)
    setDeleteError(null)
    try {
      await deleteLLMConfig(deleteTarget.id)
      setConfigs((current) =>
        current ? current.filter((item) => item.id !== deleteTarget.id) : current,
      )
      setModels((current) => current.filter((item) => item.config_id !== deleteTarget.id))
      if (expandedId === deleteTarget.id) setExpandedId(null)
      setDeleteTarget(null)
    } catch (caught) {
      setDeleteError(caught)
    } finally {
      setDeleting(false)
    }
  }

  function handleSaved(saved: LLMConfig, created: boolean): void {
    setConfigs((current) => {
      const next = current
        ? created
          ? [...current, saved]
          : current.map((item) => (item.id === saved.id ? saved : item))
        : [saved]
      if (!saved.is_default) return next
      return next.map((item) => (item.id === saved.id ? item : { ...item, is_default: false }))
    })
    setFormState(null)
  }

  function handleModelSaved(saved: LLMModel): void {
    setModels((current) => [...current, saved])
    setNotice(saved.config_id, `已添加模型 ${saved.model}`)
    setModelTarget(null)
  }

  if (loading && configs === null) return <LoadingCard label="正在加载 LLM 配置…" />
  if (loadError) {
    return (
      <ErrorState
        error={loadError}
        title="LLM 配置加载失败"
        onRetry={() => setReloadKey((n) => n + 1)}
      />
    )
  }

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 className="text-sm font-semibold text-foreground">LLM 配置</h2>
          <p className="mt-1 text-xs text-muted-foreground">
            管理 OpenAI 兼容网关、连通性测试与模型目录；API Key 只写入、永不回显。
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="secondary" size="sm" onClick={() => setReloadKey((n) => n + 1)}>
            刷新
          </Button>
          <Button size="sm" onClick={() => setFormState({ mode: "create" })}>
            新建配置
          </Button>
        </div>
      </div>

      {configs !== null && configs.length === 0 ? (
        <EmptyState
          title="还没有 LLM 配置"
          description="创建第一个配置并拉取模型目录后，Agent 才能调用上游模型。"
          action={
            <Button size="sm" onClick={() => setFormState({ mode: "create" })}>
              新建配置
            </Button>
          }
        />
      ) : null}

      <div className="space-y-4">
        {(configs ?? []).map((config) => {
          const configModels = models.filter((item) => item.config_id === config.id)
          const testResult = testResults[config.id]
          const actionError = actionErrors[config.id]
          const notice = notices[config.id]
          const expanded = expandedId === config.id
          const busy = busyKey !== null

          return (
            <article
              key={config.id}
              data-testid={`llm-config-${config.id}`}
              className="surface-card p-4"
            >
              <header className="flex flex-wrap items-start justify-between gap-3">
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-2">
                    <h3 className="text-sm font-semibold text-foreground">{config.name}</h3>
                    {config.is_default ? <Badge tone="info">默认配置</Badge> : null}
                    <Badge tone={config.status === "active" ? "success" : "danger"}>
                      {config.status === "active" ? "启用" : "禁用"}
                    </Badge>
                    <Badge tone={config.api_key_set ? "success" : "neutral"}>
                      {config.api_key_set ? "密钥已设置" : "密钥未设置"}
                    </Badge>
                  </div>
                  <p className="mt-1 break-all font-mono text-[11px] text-muted-foreground">
                    {config.base_url}
                  </p>
                </div>
                <div className="text-right text-[10px] text-muted-foreground">
                  <p>更新于 {formatDateTime(config.updated_at)}</p>
                  <p>协议 {config.protocol}</p>
                </div>
              </header>

              <dl className="mt-3 grid grid-cols-2 gap-2 text-[11px] sm:grid-cols-4">
                <div>
                  <dt className="text-muted-foreground">超时</dt>
                  <dd className="font-mono text-foreground">{config.timeout_seconds}s</dd>
                </div>
                <div>
                  <dt className="text-muted-foreground">最大重试</dt>
                  <dd className="font-mono text-foreground">{config.max_retries}</dd>
                </div>
                <div>
                  <dt className="text-muted-foreground">模型数</dt>
                  <dd className="font-mono text-foreground">{configModels.length}</dd>
                </div>
                <div>
                  <dt className="text-muted-foreground">创建时间</dt>
                  <dd className="text-foreground">{formatDateTime(config.created_at)}</dd>
                </div>
              </dl>

              <div className="mt-3 flex flex-wrap items-center gap-2">
                <Button
                  variant="secondary"
                  size="sm"
                  disabled={busy}
                  onClick={() => void handleTest(config)}
                >
                  {busyKey === `test:${config.id}` ? "测试中…" : "测试连接"}
                </Button>
                <Button
                  variant="secondary"
                  size="sm"
                  disabled={busy}
                  onClick={() => void handleRefresh(config)}
                >
                  {busyKey === `refresh:${config.id}` ? "刷新中…" : "刷新模型"}
                </Button>
                <Button
                  variant="ghost"
                  size="sm"
                  disabled={busy}
                  onClick={() => setExpandedId(expanded ? null : config.id)}
                >
                  {expanded ? "收起模型" : "管理模型"}
                </Button>
                <Button
                  variant="ghost"
                  size="sm"
                  disabled={busy}
                  onClick={() => setFormState({ mode: "edit", config })}
                >
                  编辑
                </Button>
                <Button
                  variant="danger"
                  size="sm"
                  disabled={busy}
                  onClick={() => {
                    setDeleteError(null)
                    setDeleteTarget(config)
                  }}
                >
                  删除
                </Button>
              </div>

              {testResult ? (
                <div className="mt-3">
                  <Alert tone={testResult.ok ? "success" : "danger"}>
                    {testResult.ok
                      ? `连通正常 · ${testResult.latency_ms} ms · ${testResult.models.length} 个模型`
                      : (testResult.error ?? "连通性测试失败")}
                  </Alert>
                </div>
              ) : null}

              {actionError ? (
                <div className="mt-3">
                  <InlineError error={actionError} />
                </div>
              ) : null}
              {notice ? (
                <p role="status" className="mt-3 text-xs text-success">
                  {notice}
                </p>
              ) : null}

              {expanded ? (
                <section className="mt-4 border-t border-border pt-3" aria-label="模型目录">
                  <div className="flex items-center justify-between gap-2">
                    <h4 className="text-xs font-semibold text-foreground">模型目录</h4>
                    <Button variant="secondary" size="sm" onClick={() => setModelTarget(config)}>
                      新增模型
                    </Button>
                  </div>

                  {configModels.length === 0 ? (
                    <p className="mt-2 text-xs text-muted-foreground">
                      该配置还没有模型；刷新上游目录或手工新增。
                    </p>
                  ) : (
                    <ul className="mt-2 divide-y divide-border rounded-lg border border-border">
                      {configModels.map((model) => (
                        <li
                          key={model.id}
                          data-testid={`llm-model-${model.id}`}
                          className="flex flex-wrap items-center justify-between gap-2 px-3 py-2"
                        >
                          <div className="min-w-0">
                            <div className="flex flex-wrap items-center gap-2">
                              <code className="font-mono text-[11px] font-semibold text-foreground">
                                {model.model}
                              </code>
                              <Badge tone={model.source === "manual" ? "info" : "neutral"}>
                                {model.source === "manual" ? "手工" : "目录"}
                              </Badge>
                              {model.supports_tools ? <Badge tone="success">工具</Badge> : null}
                              {model.supports_vision ? <Badge tone="success">视觉</Badge> : null}
                            </div>
                            <p className="mt-0.5 text-[10px] text-muted-foreground">
                              上下文 {model.context_window} · 输出 {model.max_output}
                            </p>
                          </div>
                          <Button
                            variant="danger"
                            size="sm"
                            disabled={busy}
                            onClick={() => void handleDeleteModel(config, model)}
                          >
                            删除模型
                          </Button>
                        </li>
                      ))}
                    </ul>
                  )}
                </section>
              ) : null}
            </article>
          )
        })}
      </div>

      {formState ? (
        <ConfigFormDialog
          key={formState.mode === "edit" ? formState.config.id : "new"}
          config={formState.mode === "edit" ? formState.config : null}
          onClose={() => setFormState(null)}
          onSaved={handleSaved}
        />
      ) : null}

      {modelTarget ? (
        <ModelDialog
          config={modelTarget}
          onClose={() => setModelTarget(null)}
          onSaved={handleModelSaved}
        />
      ) : null}

      <Dialog
        open={deleteTarget !== null}
        onClose={() => {
          if (!deleting) setDeleteTarget(null)
        }}
        title="删除 LLM 配置"
        description={
          deleteTarget
            ? `确定删除「${deleteTarget.name}」？其模型目录会级联删除，且不可恢复。`
            : undefined
        }
        footer={
          <>
            <Button variant="secondary" onClick={() => setDeleteTarget(null)} disabled={deleting}>
              取消
            </Button>
            <Button variant="danger" onClick={() => void confirmDelete()} disabled={deleting}>
              {deleting ? "删除中…" : "确认删除"}
            </Button>
          </>
        }
      >
        {deleteError ? (
          <Alert tone="danger">{errorText(deleteError, "删除失败，请稍后重试")}</Alert>
        ) : null}
        <p className="text-xs text-muted-foreground">
          删除前请确认没有进行中的 Agent 运行依赖此配置。
        </p>
      </Dialog>
    </div>
  )
}
