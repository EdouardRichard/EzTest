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
  created_at: "2026-10-31T08:00:00Z",
  updated_at: "2026-10-31T08:00:00Z",
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
      target: { value: "openai_compatible" },
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
      protocol: "openai_compatible",
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
