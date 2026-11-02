import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import type { LLMConfig, LLMModel } from "./api"
import { LLMSettings } from "./LLMSettings"

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

const fetchMock = vi.fn<typeof fetch>()

const config: LLMConfig = {
  id: "7001",
  name: "local-fake",
  provider: "openai",
  protocol: "openai_compatible",
  base_url: "http://127.0.0.1:19000/v1",
  api_key_set: true,
  headers: { "X-Tenant": "qa" },
  timeout_seconds: 60,
  max_retries: 2,
  is_default: true,
  status: "active",
  thinking_type: "auto",
  reasoning_effort: "none",
  created_at: "2026-10-31T08:00:00Z",
  updated_at: "2026-10-31T08:00:00Z",
}

const canonicalConfig: LLMConfig = {
  ...config,
  id: "7003",
  name: "responses-proxy",
  protocol: "openai_responses",
  thinking_type: "enabled",
  reasoning_effort: "high",
  default_model_id: "8002",
}

const model: LLMModel = {
  id: "8001",
  config_id: "7001",
  model: "manual-a",
  display_name: "manual-a",
  context_window: 128000,
  max_output: 4096,
  supports_tools: true,
  supports_vision: false,
  source: "manual",
  status: "active",
  created_at: "2026-10-31T08:00:00Z",
  updated_at: "2026-10-31T08:00:00Z",
}

const reasoningModel: LLMModel = {
  ...model,
  id: "8002",
  config_id: "7003",
  model: "gpt-reasoning",
  display_name: "gpt-reasoning",
  context_window: 128000,
  max_output: 16384,
  supports_tools: true,
  supports_vision: false,
  capabilities: {
    supports_tools: true,
    supports_vision: false,
    supports_reasoning: true,
    supports_prompt_cache: true,
    context_window: 128000,
  },
  deprecated: false,
}

const plainModel: LLMModel = {
  ...model,
  id: "8004",
  config_id: "7003",
  model: "gpt-plain",
  display_name: "gpt-plain",
  context_window: 32000,
  supports_tools: true,
  supports_vision: true,
  capabilities: {
    supports_tools: true,
    supports_vision: true,
    supports_reasoning: false,
    supports_prompt_cache: false,
    context_window: 32000,
  },
  deprecated: false,
}

const deprecatedModel: LLMModel = {
  ...model,
  id: "8003",
  config_id: "7003",
  model: "gpt-legacy",
  display_name: "gpt-legacy",
  deprecated: true,
}

type RouteHandler = (url: URL, init: RequestInit) => Response | Promise<Response>

function installFetch(handler: RouteHandler): void {
  fetchMock.mockImplementation(async (input, init) => {
    const url = new URL(String(input))
    return handler(url, init ?? {})
  })
}

function method(init: RequestInit): string {
  return init.method ?? "GET"
}

function readBody(init: RequestInit): unknown {
  return typeof init.body === "string" ? (JSON.parse(init.body) as unknown) : undefined
}

function configCard(id = "7001"): HTMLElement {
  return screen.getByTestId(`llm-config-${id}`)
}

async function openCreateDialog(): Promise<HTMLElement> {
  const buttons = await screen.findAllByRole("button", { name: "新建配置" })
  fireEvent.click(buttons[0])
  return screen.getByRole("dialog")
}

describe("LLMSettings", () => {
  beforeEach(() => {
    window.sessionStorage.clear()
    fetchMock.mockReset()
    vi.stubGlobal("fetch", fetchMock)
    vi.stubEnv("NEXT_PUBLIC_API_BASE_URL", "")
  })

  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
    vi.unstubAllEnvs()
    window.sessionStorage.clear()
  })

  it("lists configs with api_key_set state and never prefills a key", async () => {
    const unset: LLMConfig = {
      ...config,
      id: "7002",
      name: "backup",
      api_key_set: false,
      is_default: false,
    }
    installFetch((url, _init) => {
      if (url.pathname === "/api/admin/llm/configs") {
        return jsonResponse({ data: [config, unset] })
      }
      return jsonResponse({ data: [] })
    })

    render(<LLMSettings />)

    expect(await screen.findByTestId("llm-config-7001")).toHaveTextContent("local-fake")
    expect(within(configCard("7001")).getByText("密钥已设置")).toBeVisible()
    expect(within(configCard("7002")).getByText("密钥未设置")).toBeVisible()
    expect(within(configCard("7001")).getByText("默认配置")).toBeVisible()

    fireEvent.click(within(configCard("7001")).getByRole("button", { name: "编辑" }))
    const dialog = screen.getByRole("dialog")
    const apiKey = within(dialog).getByLabelText("API Key")
    expect(apiKey).toHaveAttribute("type", "password")
    expect(apiKey).toHaveValue("")
    expect(within(dialog).getByText("留空保持不变；密钥不会回显")).toBeVisible()
    expect(screen.queryByDisplayValue(/sk-/)).toBeNull()
    expect(method(fetchMock.mock.calls[0][1] ?? {})).toBe("GET")
  })

  it("validates the create form before calling the API", async () => {
    installFetch((url) => {
      if (url.pathname === "/api/admin/llm/configs") return jsonResponse({ data: [] })
      return jsonResponse({ data: [] })
    })

    render(<LLMSettings />)
    const dialog = await openCreateDialog()

    fireEvent.click(within(dialog).getByRole("button", { name: "创建配置" }))
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("请输入配置名称")

    fireEvent.change(within(dialog).getByLabelText("配置名称"), { target: { value: "local-fake" } })
    fireEvent.click(within(dialog).getByRole("button", { name: "创建配置" }))
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("请输入 Base URL")

    fireEvent.change(within(dialog).getByLabelText("Base URL"), {
      target: { value: "not-a-url" },
    })
    fireEvent.click(within(dialog).getByRole("button", { name: "创建配置" }))
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("请输入合法的 http(s) URL")

    fireEvent.change(within(dialog).getByLabelText("Base URL"), {
      target: { value: "http://127.0.0.1:19000/v1" },
    })
    fireEvent.change(within(dialog).getByLabelText("额外 Headers"), {
      target: { value: "{not-json" },
    })
    fireEvent.click(within(dialog).getByRole("button", { name: "创建配置" }))
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("Headers 必须是 JSON 对象")

    expect(fetchMock.mock.calls.every((call) => method(call[1] ?? {}) === "GET")).toBe(true)
  })

  it("creates a config with parsed headers and numeric bounds", async () => {
    const created: LLMConfig = { ...config, id: "7003", name: "new-config" }
    installFetch((url, init) => {
      if (method(init) === "POST" && url.pathname === "/api/admin/llm/configs") {
        return jsonResponse({ data: created }, 201)
      }
      if (url.pathname === "/api/admin/llm/configs") return jsonResponse({ data: [config] })
      return jsonResponse({ data: [] })
    })

    render(<LLMSettings />)
    const dialog = await openCreateDialog()

    fireEvent.change(within(dialog).getByLabelText("配置名称"), { target: { value: "new-config" } })
    fireEvent.change(within(dialog).getByLabelText("协议"), {
      target: { value: "openai_chat_completions" },
    })
    fireEvent.change(within(dialog).getByLabelText("Base URL"), {
      target: { value: "http://127.0.0.1:19000/v1" },
    })
    fireEvent.change(within(dialog).getByLabelText("API Key"), { target: { value: "sk-test" } })
    fireEvent.change(within(dialog).getByLabelText("超时（秒）"), { target: { value: "45" } })
    fireEvent.change(within(dialog).getByLabelText("最大重试"), { target: { value: "3" } })
    fireEvent.change(within(dialog).getByLabelText("额外 Headers"), {
      target: { value: '{"X-Tenant":"qa","X-Trace":"1"}' },
    })
    fireEvent.click(within(dialog).getByLabelText("设为默认配置"))
    fireEvent.click(within(dialog).getByRole("button", { name: "创建配置" }))

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())

    const post = fetchMock.mock.calls.find((call) => method(call[1] ?? {}) === "POST")
    expect(post).toBeDefined()
    expect(readBody(post?.[1] ?? {})).toEqual({
      name: "new-config",
      base_url: "http://127.0.0.1:19000/v1",
      protocol: "openai_chat_completions",
      thinking_type: "auto",
      reasoning_effort: "none",
      api_key: "sk-test",
      headers: { "X-Tenant": "qa", "X-Trace": "1" },
      timeout_seconds: 45,
      max_retries: 3,
      is_default: true,
      status: "active",
    })
    const createdCard = await screen.findByTestId("llm-config-7003")
    expect(createdCard).toHaveTextContent("new-config")
    expect(within(createdCard).getByText("默认配置")).toBeVisible()
    expect(within(configCard("7001")).queryByText("默认配置")).toBeNull()
  })

  it("edits a config without sending a blank api_key", async () => {
    const updated: LLMConfig = { ...config, name: "renamed-config" }
    installFetch((url, init) => {
      if (method(init) === "PATCH" && url.pathname === "/api/admin/llm/configs/7001") {
        return jsonResponse({ data: updated })
      }
      if (url.pathname === "/api/admin/llm/configs") return jsonResponse({ data: [config] })
      return jsonResponse({ data: [] })
    })

    render(<LLMSettings />)
    await screen.findByTestId("llm-config-7001")

    fireEvent.click(within(configCard()).getByRole("button", { name: "编辑" }))
    const dialog = screen.getByRole("dialog")
    expect(within(dialog).getByLabelText("API Key")).toHaveValue("")
    fireEvent.change(within(dialog).getByLabelText("配置名称"), {
      target: { value: "renamed-config" },
    })
    fireEvent.click(within(dialog).getByRole("button", { name: "保存配置" }))

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())

    const patch = fetchMock.mock.calls.find((call) => method(call[1] ?? {}) === "PATCH")
    const body = readBody(patch?.[1] ?? {}) as Record<string, unknown>
    expect(body).toMatchObject({
      name: "renamed-config",
      base_url: config.base_url,
      protocol: config.protocol,
      timeout_seconds: config.timeout_seconds,
      max_retries: config.max_retries,
      is_default: config.is_default,
      status: config.status,
    })
    expect(body).not.toHaveProperty("api_key")
    expect(await screen.findByTestId("llm-config-7001")).toHaveTextContent("renamed-config")
  })

  it("labels canonical protocols and keeps legacy configurations visible", async () => {
    installFetch((url) => {
      if (url.pathname === "/api/admin/llm/configs") {
        return jsonResponse({ data: [canonicalConfig, config] })
      }
      return jsonResponse({ data: [reasoningModel, plainModel, deprecatedModel] })
    })

    render(<LLMSettings />)
    const canonicalCard = await screen.findByTestId("llm-config-7003")
    expect(canonicalCard).toHaveTextContent("OpenAI Responses")
    expect(canonicalCard).toHaveTextContent("enabled")
    expect(canonicalCard).toHaveTextContent("high")
    expect(canonicalCard).toHaveTextContent("gpt-reasoning")
    expect(configCard("7001")).toHaveTextContent("OpenAI 兼容")

    fireEvent.click(within(canonicalCard).getByRole("button", { name: "编辑" }))
    const dialog = screen.getByRole("dialog")
    expect(within(dialog).getByLabelText("协议")).toHaveValue("openai_responses")
    expect(
      within(dialog).getByRole("option", { name: "OpenAI Chat Completions" }),
    ).toBeInTheDocument()
    expect(within(dialog).getByRole("option", { name: "OpenAI Responses" })).toBeInTheDocument()
    expect(within(dialog).queryByRole("option", { name: "OpenAI 兼容" })).toBeNull()
  })

  it("edits thinking settings and shows the default model capabilities", async () => {
    installFetch((url, init) => {
      if (url.pathname === "/api/admin/llm/configs" && method(init) === "GET") {
        return jsonResponse({ data: [canonicalConfig] })
      }
      if (url.pathname === "/api/admin/llm/models") {
        return jsonResponse({ data: [reasoningModel, plainModel, deprecatedModel] })
      }
      if (url.pathname === "/api/admin/llm/configs/7003" && method(init) === "PATCH") {
        return jsonResponse({
          data: {
            ...canonicalConfig,
            thinking_type: "disabled",
            reasoning_effort: "xhigh",
            default_model_id: "8004",
          },
        })
      }
      return jsonResponse({ data: [] })
    })

    render(<LLMSettings />)
    await screen.findByTestId("llm-config-7003")
    fireEvent.click(within(configCard("7003")).getByRole("button", { name: "编辑" }))
    const dialog = screen.getByRole("dialog")

    const thinking = within(dialog).getByLabelText("思考模式")
    expect(thinking).toHaveValue("enabled")
    const effort = within(dialog).getByLabelText("思考强度")
    expect(effort).toHaveValue("high")
    const defaultModel = within(dialog).getByLabelText("默认模型")
    expect(defaultModel).toHaveValue("8002")
    expect(within(dialog).getByRole("option", { name: /gpt-legacy/ })).toBeDisabled()
    expect(within(dialog).getByLabelText("支持工具")).toBeChecked()
    expect(within(dialog).getByLabelText("支持视觉")).not.toBeChecked()
    expect(within(dialog).getByLabelText("支持推理")).toBeChecked()
    expect(within(dialog).getByLabelText("提示缓存")).toBeChecked()
    expect(within(dialog).getByText(/上下文窗口 128000/)).toBeVisible()

    fireEvent.change(thinking, { target: { value: "disabled" } })
    // disabled thinking cannot be combined with a non-none reasoning effort
    // (the runtime rejects that contradiction): the effort is pinned to none.
    expect(effort).toHaveValue("none")
    expect(effort).toBeDisabled()
    fireEvent.change(thinking, { target: { value: "enabled" } })
    expect(effort).not.toBeDisabled()
    fireEvent.change(effort, { target: { value: "xhigh" } })
    fireEvent.change(defaultModel, { target: { value: "8004" } })
    expect(within(dialog).getByLabelText("支持推理")).not.toBeChecked()
    expect(within(dialog).getByLabelText("支持视觉")).toBeChecked()
    expect(within(dialog).getByText(/上下文窗口 32000/)).toBeVisible()

    fireEvent.click(within(dialog).getByRole("button", { name: "保存配置" }))
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())

    const patch = fetchMock.mock.calls.find(
      (call) =>
        method(call[1] ?? {}) === "PATCH" &&
        new URL(String(call[0])).pathname === "/api/admin/llm/configs/7003",
    )
    expect(readBody(patch?.[1] ?? {})).toMatchObject({
      protocol: "openai_responses",
      thinking_type: "enabled",
      reasoning_effort: "xhigh",
      default_model_id: "8004",
    })
  })

  it("edits a model capability and saves it through the model endpoint", async () => {
    installFetch((url, init) => {
      if (url.pathname === "/api/admin/llm/configs" && method(init) === "GET") {
        return jsonResponse({ data: [canonicalConfig] })
      }
      if (url.pathname === "/api/admin/llm/models") {
        return jsonResponse({ data: [reasoningModel, plainModel, deprecatedModel] })
      }
      if (url.pathname === "/api/admin/llm/configs/7003" && method(init) === "PATCH") {
        return jsonResponse({ data: canonicalConfig })
      }
      if (url.pathname === "/api/admin/llm/configs/7003/models/8002" && method(init) === "PATCH") {
        return jsonResponse({
          data: {
            ...reasoningModel,
            capabilities: {
              ...reasoningModel.capabilities,
              supports_reasoning: false,
              supports_vision: true,
            },
          },
        })
      }
      return jsonResponse({ data: [] })
    })

    render(<LLMSettings />)
    await screen.findByTestId("llm-config-7003")
    fireEvent.click(within(configCard("7003")).getByRole("button", { name: "编辑" }))
    const dialog = screen.getByRole("dialog")

    // The capability checkboxes edit the selected default model instead of
    // being read-only: gateways that do not report capabilities can be fixed.
    const reasoning = within(dialog).getByLabelText("支持推理")
    expect(reasoning).toBeChecked()
    fireEvent.click(reasoning)
    expect(reasoning).not.toBeChecked()
    fireEvent.click(within(dialog).getByLabelText("支持视觉"))

    fireEvent.click(within(dialog).getByRole("button", { name: "保存配置" }))
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())

    const modelPatch = fetchMock.mock.calls.find(
      (call) =>
        method(call[1] ?? {}) === "PATCH" &&
        new URL(String(call[0])).pathname === "/api/admin/llm/configs/7003/models/8002",
    )
    expect(readBody(modelPatch?.[1] ?? {})).toEqual({
      supports_tools: true,
      supports_vision: true,
      supports_reasoning: false,
      supports_prompt_cache: true,
      context_window: 128000,
    })
    const configPatch = fetchMock.mock.calls.find(
      (call) =>
        method(call[1] ?? {}) === "PATCH" &&
        new URL(String(call[0])).pathname === "/api/admin/llm/configs/7003",
    )
    expect(readBody(configPatch?.[1] ?? {})).toMatchObject({ default_model_id: "8002" })
  })

  it("allows clearing the default model when no selectable model exists", async () => {
    const deprecatedOnly: LLMConfig = { ...canonicalConfig, default_model_id: "8003" }
    installFetch((url, init) => {
      if (url.pathname === "/api/admin/llm/configs" && method(init) === "GET") {
        return jsonResponse({ data: [deprecatedOnly] })
      }
      if (url.pathname === "/api/admin/llm/models") {
        return jsonResponse({ data: [deprecatedModel] })
      }
      if (url.pathname === "/api/admin/llm/configs/7003" && method(init) === "PATCH") {
        return jsonResponse({ data: { ...deprecatedOnly, default_model_id: undefined } })
      }
      return jsonResponse({ data: [] })
    })

    render(<LLMSettings />)
    await screen.findByTestId("llm-config-7003")
    fireEvent.click(within(configCard("7003")).getByRole("button", { name: "编辑" }))
    const dialog = screen.getByRole("dialog")
    const defaultModel = within(dialog).getByLabelText("默认模型")

    // The stored default is deprecated, so it is visible but not selectable;
    // clearing it must actually persist the clear instead of being omitted.
    fireEvent.change(defaultModel, { target: { value: "" } })
    fireEvent.click(within(dialog).getByRole("button", { name: "保存配置" }))
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())

    const patch = fetchMock.mock.calls.find(
      (call) =>
        method(call[1] ?? {}) === "PATCH" &&
        new URL(String(call[0])).pathname === "/api/admin/llm/configs/7003",
    )
    expect(readBody(patch?.[1] ?? {})).toMatchObject({ default_model_id: "" })
  })

  it("requires a default model when selectable models exist", async () => {
    const withoutDefault: LLMConfig = {
      ...canonicalConfig,
      default_model_id: undefined,
      thinking_type: "auto",
      reasoning_effort: "none",
    }
    installFetch((url, init) => {
      if (url.pathname === "/api/admin/llm/configs" && method(init) === "GET") {
        return jsonResponse({ data: [withoutDefault] })
      }
      if (url.pathname === "/api/admin/llm/models") {
        return jsonResponse({ data: [reasoningModel] })
      }
      return jsonResponse({ data: [] })
    })

    render(<LLMSettings />)
    await screen.findByTestId("llm-config-7003")
    fireEvent.click(within(configCard("7003")).getByRole("button", { name: "编辑" }))
    const dialog = screen.getByRole("dialog")

    fireEvent.click(within(dialog).getByRole("button", { name: "保存配置" }))
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("请选择默认模型")
    expect(fetchMock.mock.calls.some((call) => method(call[1] ?? {}) === "PATCH")).toBe(false)
  })

  it("deletes a config after confirmation", async () => {
    installFetch((url, init) => {
      if (url.pathname === "/api/admin/llm/configs") return jsonResponse({ data: [config] })
      if (url.pathname === "/api/admin/llm/configs/7001" && method(init) === "DELETE") {
        return new Response(null, { status: 204 })
      }
      return jsonResponse({ data: [] })
    })

    render(<LLMSettings />)
    await screen.findByTestId("llm-config-7001")

    fireEvent.click(within(configCard()).getByRole("button", { name: "删除" }))
    const dialog = screen.getByRole("dialog")
    expect(dialog).toHaveTextContent("local-fake")
    fireEvent.click(within(dialog).getByRole("button", { name: "确认删除" }))

    await waitFor(() => expect(screen.queryByTestId("llm-config-7001")).not.toBeInTheDocument())
    const remove = fetchMock.mock.calls.find((call) => method(call[1] ?? {}) === "DELETE")
    expect(String(remove?.[0])).toBe("http://localhost:13333/api/admin/llm/configs/7001")
  })

  it("tests connectivity and refreshes the model catalog", async () => {
    installFetch((url, init) => {
      if (url.pathname === "/api/admin/llm/configs") return jsonResponse({ data: [config] })
      if (url.pathname === "/api/admin/llm/models") return jsonResponse({ data: [] })
      if (url.pathname.endsWith("/test") && method(init) === "POST") {
        return jsonResponse({
          data: {
            ok: true,
            latency_ms: 120,
            models: [
              {
                id: "fake-model",
                context_window: 128000,
                max_output: 4096,
                supports_tools: true,
                supports_vision: false,
              },
            ],
          },
        })
      }
      if (url.pathname.endsWith("/refresh-models") && method(init) === "POST") {
        return jsonResponse({ data: [model] })
      }
      return jsonResponse({ data: [] })
    })

    render(<LLMSettings />)
    await screen.findByTestId("llm-config-7001")

    fireEvent.click(within(configCard()).getByRole("button", { name: "测试连接" }))
    expect(await within(configCard()).findByText(/连通正常/)).toHaveTextContent(
      "连通正常 · 120 ms · 1 个模型",
    )

    fireEvent.click(within(configCard()).getByRole("button", { name: "刷新模型" }))
    expect(await within(configCard()).findByText("已刷新 1 个模型")).toBeVisible()

    fireEvent.click(within(configCard()).getByRole("button", { name: "管理模型" }))
    expect(within(configCard()).getByText("manual-a")).toBeVisible()
    expect(within(configCard()).getByText("手工")).toBeVisible()
  })

  it("adds and deletes a manual model", async () => {
    installFetch((url, init) => {
      if (url.pathname === "/api/admin/llm/configs") return jsonResponse({ data: [config] })
      if (url.pathname === "/api/admin/llm/models") return jsonResponse({ data: [] })
      if (url.pathname === "/api/admin/llm/configs/7001/models" && method(init) === "POST") {
        return jsonResponse({ data: model }, 201)
      }
      if (url.pathname === "/api/admin/llm/configs/7001/models/8001" && method(init) === "DELETE") {
        return new Response(null, { status: 204 })
      }
      return jsonResponse({ data: [] })
    })

    render(<LLMSettings />)
    await screen.findByTestId("llm-config-7001")

    fireEvent.click(within(configCard()).getByRole("button", { name: "管理模型" }))
    fireEvent.click(within(configCard()).getByRole("button", { name: "新增模型" }))

    const dialog = screen.getByRole("dialog")
    fireEvent.change(within(dialog).getByLabelText("模型名称"), {
      target: { value: "manual-a" },
    })
    fireEvent.change(within(dialog).getByLabelText("上下文窗口"), {
      target: { value: "128000" },
    })
    fireEvent.change(within(dialog).getByLabelText("最大输出"), { target: { value: "4096" } })
    fireEvent.click(within(dialog).getByLabelText("支持工具"))
    fireEvent.click(within(dialog).getByRole("button", { name: "添加模型" }))

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())

    const add = fetchMock.mock.calls.find(
      (call) =>
        new URL(String(call[0])).pathname === "/api/admin/llm/configs/7001/models" &&
        method(call[1] ?? {}) === "POST",
    )
    expect(readBody(add?.[1] ?? {})).toEqual({
      model: "manual-a",
      context_window: 128000,
      max_output: 4096,
      supports_tools: true,
      supports_vision: false,
    })

    const modelRow = await screen.findByTestId("llm-model-8001")
    expect(modelRow).toHaveTextContent("manual-a")

    fireEvent.click(within(modelRow).getByRole("button", { name: "删除模型" }))

    await waitFor(() => expect(screen.queryByTestId("llm-model-8001")).not.toBeInTheDocument())
    const remove = fetchMock.mock.calls.find((call) => method(call[1] ?? {}) === "DELETE")
    expect(String(remove?.[0])).toContain("/api/admin/llm/configs/7001/models/8001")
  })
})
