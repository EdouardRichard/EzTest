"use client"

import { KeyRound, Plus, RefreshCw, Save, Sparkles } from "lucide-react"
import { type FormEvent, useCallback, useEffect, useRef, useState } from "react"

import { EmptyState } from "@/components/ui/EmptyState"
import { StatusBadge } from "@/components/ui/StatusBadge"
import { ApiError, apiGet, apiPatch, apiPost, apiStreamFetch } from "@/lib/api-client"
import { consumeSSEStream } from "@/lib/sse-client"

const PROTOCOLS = [
  { value: "chat_completions", label: "Chat Completions" },
  { value: "responses", label: "Responses" },
] as const

type LLMProtocol = (typeof PROTOCOLS)[number]["value"]

type ModelInfo = {
  id: string
  context_window?: number
  max_output_tokens?: number
  input_modalities?: string[]
}

type LLMConfig = {
  id: number
  name: string
  protocol: LLMProtocol
  base_url: string
  model: string
  has_api_key: boolean
  key_version: number
  header_names: string[]
  retry_count: number
  retry_interval_ms: number
  timeout_ms: number
  stream_enabled: boolean
  multimodal_enabled: boolean
  is_default: boolean
  model_catalog: ModelInfo[]
  catalog_fetched_at: string | null
}

type Usage = {
  prompt_tokens?: number
  completion_tokens?: number
  total_tokens?: number
}

type TestResult = {
  mode: "sync" | "stream"
  text: string
  usage?: Usage
  latencyMs?: number
}

type ConfigForm = {
  name: string
  protocol: LLMProtocol
  baseUrl: string
  model: string
  apiKey: string
  headers: string
  retryCount: string
  retryIntervalMs: string
  timeoutMs: string
  streamEnabled: boolean
  multimodalEnabled: boolean
  isDefault: boolean
}

const EMPTY_FORM: ConfigForm = {
  name: "",
  protocol: "chat_completions",
  baseUrl: "",
  model: "",
  apiKey: "",
  headers: "",
  retryCount: "2",
  retryIntervalMs: "500",
  timeoutMs: "60000",
  streamEnabled: true,
  multimodalEnabled: true,
  isDefault: false,
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

function formFromConfig(config: LLMConfig): ConfigForm {
  return {
    name: config.name,
    protocol: config.protocol,
    baseUrl: config.base_url,
    model: config.model,
    // Secrets are never loaded into the form. Leaving these blank keeps the
    // stored key/headers on PATCH; typing a new value replaces them.
    apiKey: "",
    headers: "",
    retryCount: String(config.retry_count),
    retryIntervalMs: String(config.retry_interval_ms),
    timeoutMs: String(config.timeout_ms),
    streamEnabled: config.stream_enabled,
    multimodalEnabled: config.multimodal_enabled,
    isDefault: config.is_default,
  }
}

function contextWindowOf(config: LLMConfig): string {
  const model = config.model_catalog?.find((item) => item.id === config.model)
  if (!model || typeof model.context_window !== "number") return "未知"
  return `${new Intl.NumberFormat("zh-CN").format(model.context_window)} tokens`
}

function describeError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 0) return "无法连接 API 服务，请确认后端已启动。"
    if (error.status === 403) return "当前账户没有管理员权限。"
    if (error.code === "llm_not_configured") return "该配置尚未设置 API Key。"
    if (error.code === "llm_auth_failed") return "上游模型鉴权失败，请检查 API Key。"
    if (error.code === "llm_rate_limited") return "上游模型限流，请稍后重试。"
    if (error.code === "llm_timeout") return "上游模型请求超时。"
    if (error.code === "llm_invalid_response") return "上游返回了无法解析的响应。"
    return error.message
  }
  return error instanceof Error ? error.message : "未知错误"
}

/**
 * Admin LLM gateway settings: list, create and edit configurations, refresh the
 * model catalog (showing context_window), and run sync or streaming connectivity
 * tests with optional image input. API keys and custom header values are never
 * returned by the API nor rendered here; editing omits them to keep the stored
 * secret and sends a replacement only when the admin types a new one.
 */
export function LLMPanel() {
  const [configs, setConfigs] = useState<LLMConfig[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState("")
  const [notice, setNotice] = useState("")

  const [editingId, setEditingId] = useState<number | null>(null)
  const [form, setForm] = useState<ConfigForm>(EMPTY_FORM)
  const [saving, setSaving] = useState(false)
  const [refreshingId, setRefreshingId] = useState<number | null>(null)

  const [testPrompt, setTestPrompt] = useState("")
  const [testImage, setTestImage] = useState(false)
  const [testStream, setTestStream] = useState(false)
  const [testing, setTesting] = useState(false)
  const [testError, setTestError] = useState("")
  const [testResult, setTestResult] = useState<TestResult | null>(null)
  const [streamText, setStreamText] = useState("")
  const abortRef = useRef<AbortController | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError("")
    try {
      const list = await apiGet<LLMConfig[]>("/api/admin/llm/configs")
      setConfigs(Array.isArray(list) ? list : [])
    } catch (caught) {
      setError(describeError(caught))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
    return () => abortRef.current?.abort()
  }, [load])

  const editingConfig =
    editingId === null ? null : (configs.find((item) => item.id === editingId) ?? null)

  function resetTest() {
    abortRef.current?.abort()
    abortRef.current = null
    setTesting(false)
    setTestError("")
    setTestResult(null)
    setStreamText("")
    setTestImage(false)
    setTestStream(false)
  }

  function startCreate() {
    resetTest()
    setEditingId(null)
    setForm(EMPTY_FORM)
    setError("")
    setNotice("")
  }

  function startEdit(config: LLMConfig) {
    resetTest()
    setEditingId(config.id)
    setForm(formFromConfig(config))
    setError("")
    setNotice("")
  }

  function cancelEdit() {
    resetTest()
    setEditingId(null)
    setForm(EMPTY_FORM)
    setError("")
    setNotice("")
  }

  function updateForm(patch: Partial<ConfigForm>) {
    setForm((previous) => ({ ...previous, ...patch }))
  }

  function parseHeaders(text: string): { value?: Record<string, string>; error?: string } {
    if (text.trim() === "") return {}
    let parsed: unknown
    try {
      parsed = JSON.parse(text)
    } catch {
      return { error: "Headers 必须是合法的 JSON 对象。" }
    }
    if (!isRecord(parsed)) return { error: "Headers 必须是 JSON 对象。" }
    const headers: Record<string, string> = {}
    for (const [key, value] of Object.entries(parsed)) {
      if (key.trim() === "") return { error: "Header 名称不能为空。" }
      if (typeof value !== "string") return { error: "Header 值必须是字符串。" }
      headers[key] = value
    }
    return { value: headers }
  }

  async function handleSave(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setError("")
    setNotice("")

    const name = form.name.trim()
    const baseUrl = form.baseUrl.trim()
    const model = form.model.trim()
    if (name === "") {
      setError("请输入配置名称。")
      return
    }
    if (!/^https?:\/\//i.test(baseUrl)) {
      setError("Base URL 必须以 http:// 或 https:// 开头。")
      return
    }
    if (model === "") {
      setError("请输入模型名称。")
      return
    }

    const retryCount = Number(form.retryCount)
    if (!Number.isInteger(retryCount) || retryCount < 0 || retryCount > 5) {
      setError("重试次数必须是 0 到 5 之间的整数。")
      return
    }
    const retryIntervalMs = Number(form.retryIntervalMs)
    if (!Number.isInteger(retryIntervalMs) || retryIntervalMs < 100 || retryIntervalMs > 30000) {
      setError("重试间隔必须是 100 到 30000 之间的整数（毫秒）。")
      return
    }
    const timeoutMs = Number(form.timeoutMs)
    if (!Number.isInteger(timeoutMs) || timeoutMs < 1000 || timeoutMs > 300000) {
      setError("超时必须是 1000 到 300000 之间的整数（毫秒）。")
      return
    }

    const parsedHeaders = parseHeaders(form.headers)
    if (parsedHeaders.error) {
      setError(parsedHeaders.error)
      return
    }

    const payload: Record<string, unknown> = {
      name,
      protocol: form.protocol,
      base_url: baseUrl,
      model,
      retry_count: retryCount,
      retry_interval_ms: retryIntervalMs,
      timeout_ms: timeoutMs,
      stream_enabled: form.streamEnabled,
      multimodal_enabled: form.multimodalEnabled,
      is_default: form.isDefault,
    }
    const apiKey = form.apiKey.trim()
    if (apiKey !== "") payload.api_key = apiKey
    if (parsedHeaders.value !== undefined) payload.headers = parsedHeaders.value

    setSaving(true)
    try {
      const wasCreating = editingId === null
      const saved = wasCreating
        ? await apiPost<LLMConfig>("/api/admin/llm/configs", payload)
        : await apiPatch<LLMConfig>(`/api/admin/llm/configs/${editingId}`, payload)
      setConfigs((previous) =>
        [...previous.filter((item) => item.id !== saved.id), saved].sort(
          (left, right) => left.id - right.id,
        ),
      )
      setEditingId(saved.id)
      setForm(formFromConfig(saved))
      setNotice(wasCreating ? `已创建 LLM 配置 ${saved.name}。` : `已保存 LLM 配置 ${saved.name}。`)
    } catch (caught) {
      setError(describeError(caught))
    } finally {
      setSaving(false)
    }
  }

  async function handleRefreshCatalog(config: LLMConfig) {
    setError("")
    setNotice("")
    setRefreshingId(config.id)
    try {
      const updated = await apiPost<LLMConfig>(
        `/api/admin/llm/configs/${config.id}/catalog/refresh`,
      )
      setConfigs((previous) => previous.map((item) => (item.id === updated.id ? updated : item)))
      setNotice(`已刷新 ${updated.name} 的模型目录。`)
    } catch (caught) {
      setError(describeError(caught))
    } finally {
      setRefreshingId(null)
    }
  }

  async function handleTest() {
    if (!editingConfig) {
      setTestError("请先选择一个已保存的 LLM 配置。")
      return
    }
    setTestError("")
    setTestResult(null)
    setStreamText("")
    setError("")

    if (testStream && !editingConfig.stream_enabled) {
      setTestError("该配置未启用流式输出。")
      return
    }
    if (testImage && !editingConfig.multimodal_enabled) {
      setTestError("该配置未启用多模态输入。")
      return
    }

    const body = {
      stream: testStream,
      prompt: testPrompt.trim() === "" ? undefined : testPrompt.trim(),
      image: testImage,
    }

    setTesting(true)
    try {
      if (testStream) {
        const abortController = new AbortController()
        abortRef.current = abortController
        const response = await apiStreamFetch(`/api/admin/llm/configs/${editingConfig.id}/test`, {
          method: "POST",
          body: JSON.stringify(body),
          headers: { Accept: "text/event-stream" },
          signal: abortController.signal,
        })
        let text = ""
        let doneUsage: Usage | undefined
        let doneLatencyMs: number | undefined
        let doneSeen = false
        let streamError = ""
        await consumeSSEStream(response, {
          signal: abortController.signal,
          onEvent: (event) => {
            try {
              const data: unknown = JSON.parse(event.data)
              if (event.event === "llm.delta" && isRecord(data) && typeof data.delta === "string") {
                text += data.delta
                setStreamText(text)
              } else if (event.event === "llm.done" && isRecord(data)) {
                doneSeen = true
                doneUsage = isRecord(data.usage) ? (data.usage as Usage) : undefined
                doneLatencyMs = typeof data.latency_ms === "number" ? data.latency_ms : undefined
              } else if (event.event === "llm.error" && isRecord(data)) {
                streamError = typeof data.message === "string" ? data.message : "流式测试失败。"
              }
            } catch {
              // Ignore malformed frames instead of aborting a live stream.
            }
          },
        })
        if (streamError !== "") {
          setTestError(streamError)
          return
        }
        if (!doneSeen) {
          // A body that closes without llm.done means the stream was truncated
          // (or the backend died mid-test); deltas alone do not prove success.
          setTestError("流式响应意外结束：未收到 llm.done。")
          return
        }
        setTestResult({
          mode: "stream",
          text,
          usage: doneUsage,
          latencyMs: doneLatencyMs,
        })
      } else {
        const result = await apiPost<{
          text: string
          usage?: Usage
          latency_ms?: number
        }>(`/api/admin/llm/configs/${editingConfig.id}/test`, body)
        setTestResult({
          mode: "sync",
          text: result.text,
          usage: result.usage,
          latencyMs: result.latency_ms,
        })
      }
    } catch (caught) {
      if (!(caught instanceof DOMException && caught.name === "AbortError")) {
        setTestError(describeError(caught))
      }
    } finally {
      abortRef.current = null
      setTesting(false)
    }
  }

  return (
    <section className="space-y-5" aria-labelledby="llm-panel-title">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 id="llm-panel-title" className="flex items-center gap-2 text-base font-semibold">
            <KeyRound className="h-4 w-4 text-primary" aria-hidden="true" />
            LLM 配置
          </h2>
          <p className="mt-1 max-w-2xl text-sm leading-relaxed text-muted-foreground">
            管理 LLM 网关配置、刷新模型目录并测试连通性。接口只返回密钥是否存在、密钥版本与 Header
            名称，不返回任何密钥或 Header 值。
          </p>
        </div>
        <div className="flex flex-wrap gap-1.5">
          <button
            type="button"
            onClick={startCreate}
            className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <Plus className="h-4 w-4" aria-hidden="true" />
            新建配置
          </button>
          <button
            type="button"
            onClick={() => void load()}
            disabled={loading}
            className="inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <RefreshCw className={`h-4 w-4 ${loading ? "animate-spin" : ""}`} aria-hidden="true" />
            刷新
          </button>
        </div>
      </div>

      {error ? (
        <p
          role="alert"
          className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
        >
          {error}
        </p>
      ) : null}
      {notice ? (
        <p
          role="status"
          className="rounded-xl border border-emerald-500/25 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-700 dark:text-emerald-300"
        >
          {notice}
        </p>
      ) : null}

      {loading && configs.length === 0 ? (
        <p className="rounded-xl border border-dashed border-border px-4 py-8 text-center text-sm text-muted-foreground">
          正在加载 LLM 配置…
        </p>
      ) : configs.length === 0 ? (
        <EmptyState
          icon={KeyRound}
          title="未配置 LLM"
          description="使用下方表单创建第一个网关配置；没有默认配置时 P1 的 Agent 调用会被拒绝。"
        />
      ) : (
        <div className="space-y-3">
          {configs.map((config) => (
            <article key={config.id} className="rounded-xl border border-border bg-muted/20 p-4">
              <div className="flex flex-wrap items-start justify-between gap-3">
                <div className="min-w-0 space-y-1.5">
                  <div className="flex flex-wrap items-center gap-1.5">
                    <h3 className="text-sm font-semibold">{config.name}</h3>
                    {config.is_default ? <StatusBadge variant="info">默认</StatusBadge> : null}
                    <StatusBadge variant={config.has_api_key ? "success" : "warning"}>
                      {config.has_api_key
                        ? `密钥已配置 · 版本 ${config.key_version}`
                        : "未配置密钥"}
                    </StatusBadge>
                  </div>
                  <p className="break-all font-mono text-xs text-muted-foreground">
                    {config.protocol} · {config.model} · {config.base_url}
                  </p>
                  <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                    <span>
                      上下文：<strong className="text-foreground">{contextWindowOf(config)}</strong>
                    </span>
                    <span aria-hidden="true">·</span>
                    <span>{config.stream_enabled ? "流式已启用" : "流式未启用"}</span>
                    <span aria-hidden="true">·</span>
                    <span>{config.multimodal_enabled ? "多模态已启用" : "多模态未启用"}</span>
                  </div>
                  <div className="flex flex-wrap items-center gap-1.5 text-[11px] text-muted-foreground">
                    <span>Headers：</span>
                    {(config.header_names ?? []).length === 0 ? (
                      <span>无</span>
                    ) : (
                      (config.header_names ?? []).map((name) => (
                        <span
                          key={name}
                          className="rounded-full border border-border bg-card px-2 py-0.5 font-mono"
                        >
                          {name}
                        </span>
                      ))
                    )}
                  </div>
                </div>
                <div className="flex flex-wrap gap-1.5">
                  <button
                    type="button"
                    onClick={() => startEdit(config)}
                    className="rounded-lg border border-border bg-card px-3 py-1.5 text-xs font-medium transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  >
                    编辑
                  </button>
                  <button
                    type="button"
                    onClick={() => void handleRefreshCatalog(config)}
                    disabled={refreshingId === config.id}
                    className="inline-flex items-center gap-1 rounded-lg border border-border bg-card px-3 py-1.5 text-xs font-medium transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  >
                    <RefreshCw
                      className={`h-3.5 w-3.5 ${refreshingId === config.id ? "animate-spin" : ""}`}
                      aria-hidden="true"
                    />
                    刷新目录
                  </button>
                </div>
              </div>
            </article>
          ))}
        </div>
      )}

      <form
        onSubmit={(event) => void handleSave(event)}
        className="space-y-4 rounded-xl border border-border bg-card p-4"
      >
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h3 className="text-sm font-semibold">
            {editingId === null ? "新建 LLM 配置" : `编辑配置：${editingConfig?.name ?? ""}`}
          </h3>
          {editingId !== null ? (
            <button
              type="button"
              onClick={cancelEdit}
              className="text-xs font-medium text-muted-foreground underline-offset-2 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              取消编辑
            </button>
          ) : null}
        </div>

        <div className="grid gap-3 sm:grid-cols-2">
          <div>
            <label htmlFor="llm-name" className="text-xs font-medium text-muted-foreground">
              名称
            </label>
            <input
              id="llm-name"
              value={form.name}
              onChange={(event) => updateForm({ name: event.target.value })}
              placeholder="例如 default"
              autoComplete="off"
              className="mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
            />
          </div>
          <div>
            <label htmlFor="llm-protocol" className="text-xs font-medium text-muted-foreground">
              协议
            </label>
            <select
              id="llm-protocol"
              value={form.protocol}
              onChange={(event) => updateForm({ protocol: event.target.value as LLMProtocol })}
              className="mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              {PROTOCOLS.map((protocol) => (
                <option key={protocol.value} value={protocol.value}>
                  {protocol.label}
                </option>
              ))}
            </select>
          </div>
          <div>
            <label htmlFor="llm-base-url" className="text-xs font-medium text-muted-foreground">
              Base URL
            </label>
            <input
              id="llm-base-url"
              value={form.baseUrl}
              onChange={(event) => updateForm({ baseUrl: event.target.value })}
              placeholder="https://api.example.com/v1"
              autoComplete="off"
              className="mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
            />
          </div>
          <div>
            <label htmlFor="llm-model" className="text-xs font-medium text-muted-foreground">
              模型
            </label>
            <input
              id="llm-model"
              value={form.model}
              onChange={(event) => updateForm({ model: event.target.value })}
              placeholder="例如 deepseek-chat"
              autoComplete="off"
              className="mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
            />
          </div>
          <div>
            <label htmlFor="llm-api-key" className="text-xs font-medium text-muted-foreground">
              API Key
            </label>
            <input
              id="llm-api-key"
              type="password"
              value={form.apiKey}
              onChange={(event) => updateForm({ apiKey: event.target.value })}
              placeholder={editingConfig?.has_api_key ? "留空保持现有 Key" : "输入 API Key"}
              autoComplete="new-password"
              className="mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
            />
            <p className="mt-1 text-[11px] text-muted-foreground">
              留空表示保留已存储的 Key；输入新值会替换（不会回显）。
            </p>
          </div>
          <div>
            <label htmlFor="llm-headers" className="text-xs font-medium text-muted-foreground">
              自定义 Headers（JSON）
            </label>
            <textarea
              id="llm-headers"
              value={form.headers}
              onChange={(event) => updateForm({ headers: event.target.value })}
              placeholder={editingId === null ? '{"X-Tenant":"..."}' : "留空保持现有 Headers"}
              rows={2}
              className="mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 font-mono text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring"
            />
            <p className="mt-1 text-[11px] text-muted-foreground">
              留空表示保留已存储的 Headers；输入 <code>{"{}"}</code> 可清空。
            </p>
          </div>
          <div>
            <label htmlFor="llm-retry-count" className="text-xs font-medium text-muted-foreground">
              重试次数
            </label>
            <input
              id="llm-retry-count"
              type="number"
              min={0}
              max={5}
              value={form.retryCount}
              onChange={(event) => updateForm({ retryCount: event.target.value })}
              className="mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
            />
          </div>
          <div>
            <label
              htmlFor="llm-retry-interval"
              className="text-xs font-medium text-muted-foreground"
            >
              重试间隔（ms）
            </label>
            <input
              id="llm-retry-interval"
              type="number"
              min={100}
              max={30000}
              value={form.retryIntervalMs}
              onChange={(event) => updateForm({ retryIntervalMs: event.target.value })}
              className="mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
            />
          </div>
          <div>
            <label htmlFor="llm-timeout" className="text-xs font-medium text-muted-foreground">
              超时（ms）
            </label>
            <input
              id="llm-timeout"
              type="number"
              min={1000}
              max={300000}
              value={form.timeoutMs}
              onChange={(event) => updateForm({ timeoutMs: event.target.value })}
              className="mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
            />
          </div>
        </div>

        <div className="flex flex-wrap gap-4">
          <label className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={form.streamEnabled}
              onChange={(event) => updateForm({ streamEnabled: event.target.checked })}
              className="h-4 w-4 rounded border-border"
            />
            启用流式输出
          </label>
          <label className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={form.multimodalEnabled}
              onChange={(event) => updateForm({ multimodalEnabled: event.target.checked })}
              className="h-4 w-4 rounded border-border"
            />
            启用多模态输入
          </label>
          <label className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={form.isDefault}
              onChange={(event) => updateForm({ isDefault: event.target.checked })}
              className="h-4 w-4 rounded border-border"
            />
            设为默认配置
          </label>
        </div>

        <div>
          <button
            type="submit"
            disabled={saving}
            className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3.5 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <Save className="h-4 w-4" aria-hidden="true" />
            {saving ? "保存中…" : "保存配置"}
          </button>
        </div>
      </form>

      {editingConfig ? (
        <div className="space-y-3 rounded-xl border border-border bg-card p-4">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <h3 className="text-sm font-semibold">模型目录</h3>
            <span className="text-[11px] text-muted-foreground">
              {editingConfig.catalog_fetched_at
                ? `刷新于 ${new Date(editingConfig.catalog_fetched_at).toLocaleString("zh-CN")}`
                : "尚未刷新"}
            </span>
          </div>
          {(editingConfig.model_catalog ?? []).length === 0 ? (
            <p className="text-sm text-muted-foreground">
              暂无模型目录，点击配置上的“刷新目录”从上游获取。
            </p>
          ) : (
            <ul className="divide-y divide-border rounded-lg border border-border">
              {(editingConfig.model_catalog ?? []).map((model) => (
                <li
                  key={model.id}
                  className="flex flex-wrap items-center justify-between gap-2 px-3 py-2 text-sm"
                >
                  <span className="font-mono text-xs">{model.id}</span>
                  <span className="text-xs text-muted-foreground">
                    上下文：
                    {typeof model.context_window === "number"
                      ? `${new Intl.NumberFormat("zh-CN").format(model.context_window)} tokens`
                      : "未知"}
                    {typeof model.max_output_tokens === "number"
                      ? ` · 最大输出 ${new Intl.NumberFormat("zh-CN").format(model.max_output_tokens)}`
                      : ""}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </div>
      ) : null}

      {editingConfig ? (
        <div className="space-y-4 rounded-xl border border-border bg-card p-4">
          <div>
            <h3 className="text-sm font-semibold">连通性测试</h3>
            <p className="mt-1 text-sm text-muted-foreground">
              非流式返回完整文本；流式使用 fetch 读取归一化 SSE（llm.delta / llm.done /
              llm.error）。图片测试使用后端内置的 1×1 PNG。
            </p>
          </div>

          <div>
            <label htmlFor="llm-test-prompt" className="text-xs font-medium text-muted-foreground">
              测试提示词
            </label>
            <input
              id="llm-test-prompt"
              value={testPrompt}
              onChange={(event) => setTestPrompt(event.target.value)}
              placeholder="留空使用默认提示词"
              className="mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
            />
          </div>

          <div className="flex flex-wrap gap-4">
            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={testImage}
                onChange={(event) => setTestImage(event.target.checked)}
                disabled={!editingConfig.multimodal_enabled}
                className="h-4 w-4 rounded border-border"
              />
              附带图片
              {!editingConfig.multimodal_enabled ? (
                <span className="text-[11px] text-muted-foreground">（配置未启用多模态）</span>
              ) : null}
            </label>
            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={testStream}
                onChange={(event) => setTestStream(event.target.checked)}
                disabled={!editingConfig.stream_enabled}
                className="h-4 w-4 rounded border-border"
              />
              流式输出
              {!editingConfig.stream_enabled ? (
                <span className="text-[11px] text-muted-foreground">（配置未启用流式）</span>
              ) : null}
            </label>
          </div>

          <button
            type="button"
            onClick={() => void handleTest()}
            disabled={testing}
            className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3.5 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <Sparkles className="h-4 w-4" aria-hidden="true" />
            {testing ? "测试中…" : "测试连通性"}
          </button>

          {testError ? (
            <p
              role="alert"
              className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
            >
              {testError}
            </p>
          ) : null}

          {testStream && streamText !== "" ? (
            <div>
              <p className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
                流式输出
              </p>
              <pre className="mt-1 whitespace-pre-wrap break-words rounded-lg border border-border bg-muted/40 p-3 text-sm">
                {streamText}
              </pre>
            </div>
          ) : null}

          {testResult ? (
            <div className="space-y-2">
              <div className="flex flex-wrap items-center gap-2 text-[11px] text-muted-foreground">
                <StatusBadge variant="success">测试通过</StatusBadge>
                <span>{testResult.mode === "stream" ? "流式" : "非流式"}</span>
                {typeof testResult.latencyMs === "number" ? (
                  <span>延迟 {testResult.latencyMs} ms</span>
                ) : null}
                {testResult.usage ? (
                  <span>
                    tokens：输入 {testResult.usage.prompt_tokens ?? 0} / 输出{" "}
                    {testResult.usage.completion_tokens ?? 0} / 总计{" "}
                    {testResult.usage.total_tokens ?? 0}
                  </span>
                ) : null}
              </div>
              <pre className="whitespace-pre-wrap break-words rounded-lg border border-border bg-muted/40 p-3 text-sm">
                {testResult.text || "（空响应）"}
              </pre>
            </div>
          ) : null}
        </div>
      ) : null}
    </section>
  )
}
