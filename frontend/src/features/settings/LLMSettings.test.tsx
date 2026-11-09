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

/** Catalog-owned window/output, no manual override, 64K/4.096K. */
const editableModel: LLMModel = {
  ...model,
  id: "8010",
  model: "editable-model",
  display_name: "editable-model",
  context_window: 64000,
  max_output: 4096,
  max_input: 59904,
  source: "catalog",
  window_source: "catalog",
  output_source: "catalog",
}

/** Manual window/output override with a real server-derived input budget. */
const manualParamsModel: LLMModel = {
  ...model,
  id: "8011",
  model: "manual-params",
  display_name: "manual-params",
  context_window: 128000,
  max_output: 8192,
  max_input: 119808,
  window_source: "manual",
  output_source: "manual",
}

/**
 * Deliberately inconsistent with the raw pair (131072-8192=122880) so the test
 * pins that the server-derived max_input is preferred over client derivation.
 */
const apiMaxInputModel: LLMModel = {
  ...model,
  id: "8012",
  model: "api-max-input",
  display_name: "api-max-input",
  context_window: 131072,
  max_output: 8192,
  max_input: 119808,
  window_source: "catalog",
  output_source: "manual",
}

/** Manually asserted vision capability that a catalog refresh must preserve. */
const manualVisionModel: LLMModel = {
  ...model,
  id: "8013",
  model: "manual-vision",
  display_name: "manual-vision",
  context_window: 1_048_576,
  max_output: 8192,
  max_input: 1_040_384,
  supports_vision: true,
  supports_vision_source: "manual",
  source: "catalog",
  window_source: "catalog",
  output_source: "catalog",
  capabilities: {
    supports_tools: true,
    supports_vision: true,
    supports_reasoning: false,
    supports_prompt_cache: false,
    context_window: 1_048_576,
    supports_vision_source: "manual",
  },
}

const unhealthyConfig: LLMConfig = {
  ...config,
  health: {
    default_model_valid: false,
    vision_ready: false,
    reason: "default_model_missing",
  },
}

const notVisionConfig: LLMConfig = {
  ...config,
  id: "7005",
  name: "vision-gap",
  health: {
    default_model_valid: true,
    vision_ready: false,
    reason: "default_model_not_vision",
  },
}

/** The same default config once its default model satisfies the runtime gate. */
const healthyConfig: LLMConfig = {
  ...config,
  health: { default_model_valid: true, vision_ready: true },
}

/** A secondary config never drives runtime, so its health must stay silent. */
const nonDefaultUnhealthyConfig: LLMConfig = {
  ...unhealthyConfig,
  id: "7006",
  name: "secondary",
  is_default: false,
}

/** A refresh that reports catalog-owned vision: nothing was preserved. */
const catalogVisionModel: LLMModel = {
  ...manualVisionModel,
  supports_vision_source: "catalog",
  capabilities: {
    supports_tools: true,
    supports_vision: true,
    supports_reasoning: false,
    supports_prompt_cache: false,
    context_window: 1_048_576,
    supports_vision_source: "catalog",
  },
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

async function openModelDirectory(id = "7001"): Promise<void> {
  fireEvent.click(within(configCard(id)).getByRole("button", { name: "管理模型" }))
  await screen.findByRole("region", { name: "模型目录" })
}

async function openModelEditor(modelID = "8010"): Promise<HTMLElement> {
  const row = await screen.findByTestId(`llm-model-${modelID}`)
  fireEvent.click(within(row).getByRole("button", { name: "编辑" }))
  return screen.getByRole("dialog")
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

  it("edits thinking settings and the default model", async () => {
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

    fireEvent.change(thinking, { target: { value: "disabled" } })
    // disabled thinking cannot be combined with a non-none reasoning effort
    // (the runtime rejects that contradiction): the effort is pinned to none.
    expect(effort).toHaveValue("none")
    expect(effort).toBeDisabled()
    fireEvent.change(thinking, { target: { value: "enabled" } })
    expect(effort).not.toBeDisabled()
    fireEvent.change(effort, { target: { value: "xhigh" } })
    fireEvent.change(defaultModel, { target: { value: "8004" } })

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

  it("removes the model capability block from the provider config dialog", async () => {
    installFetch((url) => {
      if (url.pathname === "/api/admin/llm/configs") return jsonResponse({ data: [config] })
      if (url.pathname === "/api/admin/llm/models") return jsonResponse({ data: [editableModel] })
      return jsonResponse({ data: [] })
    })

    render(<LLMSettings />)
    await screen.findByTestId("llm-config-7001")
    fireEvent.click(within(configCard()).getByRole("button", { name: "编辑" }))
    const dialog = screen.getByRole("dialog")

    expect(within(dialog).queryByText(/模型能力/)).toBeNull()
    expect(within(dialog).queryByLabelText("支持推理")).toBeNull()
    expect(within(dialog).queryByText(/上下文窗口 \d+/)).toBeNull()
  })

  it("edits capabilities in the model edit dialog without patching window sources", async () => {
    installFetch((url, init) => {
      if (url.pathname === "/api/admin/llm/configs") return jsonResponse({ data: [config] })
      if (url.pathname === "/api/admin/llm/models") return jsonResponse({ data: [editableModel] })
      if (url.pathname === "/api/admin/llm/configs/7001/models/8010" && method(init) === "PATCH") {
        return jsonResponse({
          data: { ...editableModel, supports_vision: true, supports_reasoning: true },
        })
      }
      return jsonResponse({ data: [] })
    })

    render(<LLMSettings />)
    await screen.findByTestId("llm-config-7001")
    await openModelDirectory()
    const dialog = await openModelEditor()

    fireEvent.click(within(dialog).getByLabelText("支持视觉"))
    fireEvent.click(within(dialog).getByLabelText("支持推理"))
    fireEvent.click(within(dialog).getByRole("button", { name: "保存" }))
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())

    const patch = fetchMock.mock.calls.find(
      (call) =>
        method(call[1] ?? {}) === "PATCH" &&
        new URL(String(call[0])).pathname === "/api/admin/llm/configs/7001/models/8010",
    )
    expect(readBody(patch?.[1] ?? {})).toEqual({
      supports_tools: true,
      supports_vision: true,
      supports_reasoning: true,
      supports_prompt_cache: false,
      deprecated: false,
    })
  })

  it("renders model parameters in K units", async () => {
    installFetch((url) => {
      if (url.pathname === "/api/admin/llm/configs") return jsonResponse({ data: [config] })
      if (url.pathname === "/api/admin/llm/models") {
        return jsonResponse({ data: [manualParamsModel, editableModel] })
      }
      return jsonResponse({ data: [] })
    })

    render(<LLMSettings />)
    await screen.findByTestId("llm-config-7001")
    await openModelDirectory()

    const manualRow = screen.getByTestId("llm-model-8011")
    expect(manualRow).toHaveTextContent("上下文 128K")
    expect(manualRow).toHaveTextContent("输出 8.192K")
    expect(manualRow).toHaveTextContent("输入 119.808K")

    const catalogRow = screen.getByTestId("llm-model-8010")
    expect(catalogRow).toHaveTextContent("上下文 64K")
    expect(catalogRow).toHaveTextContent("输出 4.096K")
    expect(catalogRow).toHaveTextContent("输入 59.904K")
  })

  it("marks manual parameter sources and only enables reset for them", async () => {
    installFetch((url) => {
      if (url.pathname === "/api/admin/llm/configs") return jsonResponse({ data: [config] })
      if (url.pathname === "/api/admin/llm/models") {
        return jsonResponse({ data: [manualParamsModel, editableModel] })
      }
      return jsonResponse({ data: [] })
    })

    render(<LLMSettings />)
    await screen.findByTestId("llm-config-7001")
    await openModelDirectory()

    expect(within(screen.getByTestId("llm-model-8011")).getByText("参数：手工")).toBeVisible()
    expect(within(screen.getByTestId("llm-model-8010")).queryByText("参数：手工")).toBeNull()

    let dialog = await openModelEditor("8011")
    expect(within(dialog).getByRole("button", { name: "恢复上游值" })).toBeEnabled()
    fireEvent.click(within(dialog).getByRole("button", { name: "取消" }))
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())

    dialog = await openModelEditor("8010")
    expect(within(dialog).getByRole("button", { name: "恢复上游值" })).toBeDisabled()
  })

  it("displays the server-derived max input", async () => {
    installFetch((url) => {
      if (url.pathname === "/api/admin/llm/configs") return jsonResponse({ data: [config] })
      if (url.pathname === "/api/admin/llm/models")
        return jsonResponse({ data: [apiMaxInputModel] })
      return jsonResponse({ data: [] })
    })

    render(<LLMSettings />)
    await screen.findByTestId("llm-config-7001")
    await openModelDirectory()

    expect(screen.getByTestId("llm-model-8012")).toHaveTextContent("输入 119.808K")

    const dialog = await openModelEditor("8012")
    expect(within(dialog).getByLabelText("最大输入")).toHaveValue("119.808K")
  })

  it("patches the context window in raw tokens from a K input", async () => {
    installFetch((url, init) => {
      if (url.pathname === "/api/admin/llm/configs") return jsonResponse({ data: [config] })
      if (url.pathname === "/api/admin/llm/models") return jsonResponse({ data: [editableModel] })
      if (url.pathname === "/api/admin/llm/configs/7001/models/8010" && method(init) === "PATCH") {
        return jsonResponse({
          data: {
            ...editableModel,
            context_window: 128000,
            max_input: 123904,
            window_source: "manual",
          },
        })
      }
      return jsonResponse({ data: [] })
    })

    render(<LLMSettings />)
    await screen.findByTestId("llm-config-7001")
    await openModelDirectory()
    const dialog = await openModelEditor()

    const input = within(dialog).getByLabelText("上下文窗口 (K)")
    expect(input).toHaveValue(64)
    fireEvent.change(input, { target: { value: "128" } })
    fireEvent.click(within(dialog).getByRole("button", { name: "保存" }))
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())

    const patch = fetchMock.mock.calls.find(
      (call) =>
        method(call[1] ?? {}) === "PATCH" &&
        new URL(String(call[0])).pathname === "/api/admin/llm/configs/7001/models/8010",
    )
    expect(String(patch?.[1]?.body)).toContain('"context_window":128000')
    expect(await screen.findByTestId("llm-model-8010")).toHaveTextContent("上下文 128K")
  })

  it("converts fractional K values to exact raw tokens", async () => {
    installFetch((url, init) => {
      if (url.pathname === "/api/admin/llm/configs") return jsonResponse({ data: [config] })
      if (url.pathname === "/api/admin/llm/models") return jsonResponse({ data: [editableModel] })
      if (url.pathname === "/api/admin/llm/configs/7001/models/8010" && method(init) === "PATCH") {
        return jsonResponse({
          data: { ...editableModel, max_output: 500, max_input: 63500, output_source: "manual" },
        })
      }
      return jsonResponse({ data: [] })
    })

    render(<LLMSettings />)
    await screen.findByTestId("llm-config-7001")
    await openModelDirectory()
    const dialog = await openModelEditor()

    const output = within(dialog).getByLabelText("最大输出 (K)")
    expect(output).toHaveValue(4.096)
    fireEvent.change(output, { target: { value: "0.5" } })
    fireEvent.click(within(dialog).getByRole("button", { name: "保存" }))
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())

    const patch = fetchMock.mock.calls.find(
      (call) =>
        method(call[1] ?? {}) === "PATCH" &&
        new URL(String(call[0])).pathname === "/api/admin/llm/configs/7001/models/8010",
    )
    expect(String(patch?.[1]?.body)).toContain('"max_output":500')
  })

  it("resets manual parameters through the reset-params endpoint", async () => {
    const resetModel: LLMModel = {
      ...manualParamsModel,
      context_window: 64000,
      max_output: 4096,
      max_input: 59904,
      window_source: "catalog",
      output_source: "catalog",
    }
    installFetch((url, init) => {
      if (url.pathname === "/api/admin/llm/configs") return jsonResponse({ data: [config] })
      if (url.pathname === "/api/admin/llm/models") {
        return jsonResponse({ data: [manualParamsModel, editableModel] })
      }
      if (
        url.pathname === "/api/admin/llm/configs/7001/models/8011/reset-params" &&
        method(init) === "POST"
      ) {
        return jsonResponse({ data: resetModel })
      }
      return jsonResponse({ data: [] })
    })

    render(<LLMSettings />)
    await screen.findByTestId("llm-config-7001")
    await openModelDirectory()
    const dialog = await openModelEditor("8011")

    fireEvent.click(within(dialog).getByRole("button", { name: "恢复上游值" }))
    await waitFor(() => expect(within(dialog).getByLabelText("上下文窗口 (K)")).toHaveValue(64))

    const reset = fetchMock.mock.calls.find(
      (call) =>
        method(call[1] ?? {}) === "POST" &&
        new URL(String(call[0])).pathname ===
          "/api/admin/llm/configs/7001/models/8011/reset-params",
    )
    expect(reset).toBeDefined()
    expect(screen.getByTestId("llm-model-8011")).toHaveTextContent("上下文 64K")
    expect(within(dialog).getByRole("button", { name: "恢复上游值" })).toBeDisabled()
  })

  it("rejects max_output greater than context_window without patching", async () => {
    installFetch((url) => {
      if (url.pathname === "/api/admin/llm/configs") return jsonResponse({ data: [config] })
      if (url.pathname === "/api/admin/llm/models") return jsonResponse({ data: [editableModel] })
      return jsonResponse({ data: [] })
    })

    render(<LLMSettings />)
    await screen.findByTestId("llm-config-7001")
    await openModelDirectory()
    const dialog = await openModelEditor()

    fireEvent.change(within(dialog).getByLabelText("最大输出 (K)"), {
      target: { value: "128" },
    })
    fireEvent.click(within(dialog).getByRole("button", { name: "保存" }))

    expect(await within(dialog).findByRole("alert")).toHaveTextContent("最大输出不能大于上下文窗口")
    expect(fetchMock.mock.calls.some((call) => method(call[1] ?? {}) === "PATCH")).toBe(false)
  })

  it("rejects negative or non-numeric K values without patching", async () => {
    installFetch((url) => {
      if (url.pathname === "/api/admin/llm/configs") return jsonResponse({ data: [config] })
      if (url.pathname === "/api/admin/llm/models") return jsonResponse({ data: [editableModel] })
      return jsonResponse({ data: [] })
    })

    render(<LLMSettings />)
    await screen.findByTestId("llm-config-7001")
    await openModelDirectory()
    const dialog = await openModelEditor()
    const input = within(dialog).getByLabelText("上下文窗口 (K)")

    fireEvent.change(input, { target: { value: "-1" } })
    fireEvent.click(within(dialog).getByRole("button", { name: "保存" }))
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("上下文窗口必须是")

    fireEvent.change(input, { target: { value: "abc" } })
    fireEvent.click(within(dialog).getByRole("button", { name: "保存" }))
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("上下文窗口必须是")
    expect(fetchMock.mock.calls.some((call) => method(call[1] ?? {}) === "PATCH")).toBe(false)
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

  it("localizes the default-model vision gate under the select and focuses it", async () => {
    installFetch((url, init) => {
      if (url.pathname === "/api/admin/llm/configs" && method(init) === "GET") {
        return jsonResponse({ data: [canonicalConfig] })
      }
      if (url.pathname === "/api/admin/llm/models") {
        return jsonResponse({ data: [reasoningModel, plainModel] })
      }
      if (url.pathname === "/api/admin/llm/configs/7003" && method(init) === "PATCH") {
        return jsonResponse(
          {
            error: {
              code: "invalid_argument",
              message: "default model must support vision",
              details: {
                field: "default_model_id",
                model: "gpt-plain",
                missing_capabilities: ["vision"],
              },
            },
          },
          400,
        )
      }
      return jsonResponse({ data: [] })
    })

    render(<LLMSettings />)
    await screen.findByTestId("llm-config-7003")
    fireEvent.click(within(configCard("7003")).getByRole("button", { name: "编辑" }))
    const dialog = screen.getByRole("dialog")
    const defaultModel = within(dialog).getByLabelText("默认模型")

    fireEvent.click(within(dialog).getByRole("button", { name: "保存配置" }))

    expect(
      await within(dialog).findByText("默认模型必须支持视觉能力，才能分析文档中的图片"),
    ).toBeVisible()
    expect(defaultModel).toHaveFocus()
    expect(screen.queryByText(/default model must support vision/)).toBeNull()
  })

  it("warns in Chinese when a config's default model is missing or not vision-ready", async () => {
    installFetch((url) => {
      if (url.pathname === "/api/admin/llm/configs") {
        return jsonResponse({ data: [unhealthyConfig, notVisionConfig] })
      }
      // A selectable model exists, so the actionable copy is "re-pick" rather
      // than "refresh the catalog".
      if (url.pathname === "/api/admin/llm/models")
        return jsonResponse({ data: [manualVisionModel] })
      return jsonResponse({ data: [] })
    })

    render(<LLMSettings />)

    const missingCard = await screen.findByTestId("llm-config-7001")
    expect(within(missingCard).getByText(/默认模型缺失/)).toBeVisible()
    expect(within(missingCard).getByText(/重新选择默认模型/)).toBeVisible()

    const visionCard = screen.getByTestId("llm-config-7005")
    expect(within(visionCard).getByText(/默认模型不支持视觉/)).toBeVisible()
    expect(within(visionCard).getByText(/改选支持视觉的模型/)).toBeVisible()
    expect(screen.queryByText(/default_model_/)).toBeNull()
  })

  it("only warns about the default config and asks for a catalog refresh when none is selectable", async () => {
    installFetch((url) => {
      if (url.pathname === "/api/admin/llm/configs") {
        return jsonResponse({ data: [nonDefaultUnhealthyConfig, unhealthyConfig] })
      }
      return jsonResponse({ data: [] })
    })

    render(<LLMSettings />)

    const defaultCard = await screen.findByTestId("llm-config-7001")
    // No selectable model exists, so "重新选择默认模型" would be a dead end.
    expect(within(defaultCard).getByText(/请先刷新模型目录/)).toBeVisible()
    expect(within(defaultCard).queryByText(/重新选择默认模型/)).toBeNull()

    const secondaryCard = screen.getByTestId("llm-config-7006")
    expect(within(secondaryCard).queryByText(/默认模型缺失/)).toBeNull()
    expect(within(secondaryCard).queryByText(/请先刷新模型目录/)).toBeNull()
  })

  it("clears the health warning after a refresh makes the default model valid", async () => {
    let healed = false
    installFetch((url, init) => {
      if (url.pathname === "/api/admin/llm/configs") {
        return jsonResponse({ data: [healed ? healthyConfig : unhealthyConfig] })
      }
      if (url.pathname === "/api/admin/llm/models") {
        return jsonResponse({ data: [manualVisionModel] })
      }
      if (url.pathname.endsWith("/refresh-models") && method(init) === "POST") {
        healed = true
        return jsonResponse({ data: [manualVisionModel] })
      }
      return jsonResponse({ data: [] })
    })

    render(<LLMSettings />)
    const card = await screen.findByTestId("llm-config-7001")
    expect(within(card).getByText(/默认模型缺失/)).toBeVisible()

    fireEvent.click(within(card).getByRole("button", { name: "刷新模型" }))

    await waitFor(() => expect(within(card).queryByText(/默认模型缺失/)).toBeNull())
    expect(within(card).getByRole("status")).toHaveTextContent("已刷新 1 个模型")
  })

  it("shows capability provenance, the K conversion and the runtime budget note", async () => {
    installFetch((url) => {
      if (url.pathname === "/api/admin/llm/configs") return jsonResponse({ data: [config] })
      if (url.pathname === "/api/admin/llm/models") {
        return jsonResponse({ data: [manualVisionModel] })
      }
      return jsonResponse({ data: [] })
    })

    render(<LLMSettings />)
    await screen.findByTestId("llm-config-7001")
    await openModelDirectory()

    const row = screen.getByTestId("llm-model-8013")
    expect(row).toHaveTextContent("来源：上游目录")
    expect(row).toHaveTextContent("参数：上游目录")
    expect(row).toHaveTextContent("K 口径（1K = 1000）")
    expect(row).toHaveTextContent("上下文 1048.576K（1048576 tokens）")
    expect(row).toHaveTextContent(/模型容量/)
    expect(row).toHaveTextContent(/context\.window_tokens/)
    expect(row).toHaveTextContent(/128000 tokens/)
  })

  it("reports manual capabilities preserved across a catalog refresh", async () => {
    installFetch((url, init) => {
      if (url.pathname === "/api/admin/llm/configs") return jsonResponse({ data: [config] })
      if (url.pathname === "/api/admin/llm/models") {
        return jsonResponse({ data: [manualVisionModel] })
      }
      if (url.pathname.endsWith("/refresh-models") && method(init) === "POST") {
        return jsonResponse({ data: [manualVisionModel] })
      }
      return jsonResponse({ data: [] })
    })

    render(<LLMSettings />)
    await screen.findByTestId("llm-config-7001")

    fireEvent.click(within(configCard()).getByRole("button", { name: "刷新模型" }))

    const notice = await within(configCard()).findByRole("status")
    expect(notice).toHaveTextContent("已刷新 1 个模型")
    expect(notice).toHaveTextContent("已保留 1 项手工能力：支持视觉")

    fireEvent.click(within(configCard()).getByRole("button", { name: "管理模型" }))
    expect(within(configCard()).getByText("视觉")).toBeVisible()
    expect(within(configCard()).getByText("来源：上游目录")).toBeVisible()
  })

  it("does not claim preserved capabilities when the refresh returns catalog sources", async () => {
    installFetch((url, init) => {
      if (url.pathname === "/api/admin/llm/configs") return jsonResponse({ data: [config] })
      if (url.pathname === "/api/admin/llm/models") {
        return jsonResponse({ data: [manualVisionModel] })
      }
      if (url.pathname.endsWith("/refresh-models") && method(init) === "POST") {
        return jsonResponse({ data: [catalogVisionModel] })
      }
      return jsonResponse({ data: [] })
    })

    render(<LLMSettings />)
    await screen.findByTestId("llm-config-7001")

    fireEvent.click(within(configCard()).getByRole("button", { name: "刷新模型" }))

    const notice = await within(configCard()).findByRole("status")
    expect(notice).toHaveTextContent("已刷新 1 个模型")
    expect(notice).not.toHaveTextContent("已保留")
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
    expect(await within(configCard()).findByText(/已刷新 1 个模型/)).toBeVisible()

    fireEvent.click(within(configCard()).getByRole("button", { name: "管理模型" }))
    expect(within(configCard()).getByText("manual-a")).toBeVisible()
    expect(within(configCard()).getByText("来源：手工")).toBeVisible()
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
    expect(dialog).toHaveTextContent("1K = 1000")
    fireEvent.change(within(dialog).getByLabelText("模型名称"), {
      target: { value: "manual-a" },
    })
    fireEvent.change(within(dialog).getByLabelText("上下文窗口 (K)"), {
      target: { value: "128" },
    })
    fireEvent.change(within(dialog).getByLabelText("最大输出 (K)"), { target: { value: "4.096" } })
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

  it("sends explicit 0 for automatic window and output when adding a model", async () => {
    installFetch((url, init) => {
      if (url.pathname === "/api/admin/llm/configs") return jsonResponse({ data: [config] })
      if (url.pathname === "/api/admin/llm/models") return jsonResponse({ data: [] })
      if (url.pathname === "/api/admin/llm/configs/7001/models" && method(init) === "POST") {
        return jsonResponse({ data: model }, 201)
      }
      return jsonResponse({ data: [] })
    })

    render(<LLMSettings />)
    await screen.findByTestId("llm-config-7001")

    fireEvent.click(within(configCard()).getByRole("button", { name: "管理模型" }))
    fireEvent.click(within(configCard()).getByRole("button", { name: "新增模型" }))
    const dialog = screen.getByRole("dialog")
    fireEvent.change(within(dialog).getByLabelText("模型名称"), {
      target: { value: "auto-model" },
    })
    fireEvent.click(within(dialog).getByRole("button", { name: "添加模型" }))

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())

    const add = fetchMock.mock.calls.find(
      (call) =>
        new URL(String(call[0])).pathname === "/api/admin/llm/configs/7001/models" &&
        method(call[1] ?? {}) === "POST",
    )
    expect(readBody(add?.[1] ?? {})).toEqual({
      model: "auto-model",
      context_window: 0,
      max_output: 0,
      supports_tools: false,
      supports_vision: false,
    })
  })
})
