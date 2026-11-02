import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import type { Setting } from "./api"
import { SystemSettings } from "./SystemSettings"

// Admin-only panels are covered by their own tests; keep this suite on the
// settings editor and avoid a second data-loading surface per test.
vi.mock("./GCPanel", () => ({ GCPanel: () => <div data-testid="gc-panel-stub">GC 面板</div> }))
vi.mock("./InstancePanel", () => ({
  InstancePanel: () => <div data-testid="instance-panel-stub">实例面板</div>,
}))

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

const fetchMock = vi.fn<typeof fetch>()

const settings: Setting[] = [
  {
    key: "execution.global_concurrency",
    category: "execution",
    type: "number",
    value: 3,
    is_secret: false,
    restart_required: false,
    description: "最大并发用例数",
    version: 1,
    updated_at: "2026-10-31T08:00:00Z",
  },
  {
    key: "execution.case_timeout_seconds",
    category: "execution",
    type: "number",
    value: 900,
    is_secret: false,
    restart_required: false,
    description: "单用例超时（秒）",
    version: 1,
    updated_at: "2026-10-31T08:00:00Z",
  },
  {
    key: "runtime.snowflake_node_id",
    category: "runtime",
    type: "number",
    value: 1,
    is_secret: false,
    restart_required: true,
    description: "Snowflake 节点 ID",
    version: 1,
    updated_at: "2026-10-31T08:00:00Z",
  },
  {
    key: "retention.gc_dry_run",
    category: "retention",
    type: "boolean",
    value: true,
    is_secret: false,
    restart_required: false,
    description: "GC 默认演练模式",
    version: 1,
    updated_at: "2026-10-31T08:00:00Z",
  },
  {
    key: "feature.experimental_flags",
    category: "feature",
    type: "json",
    value: { beta: false },
    is_secret: false,
    restart_required: false,
    description: "实验特性开关",
    version: 1,
    updated_at: "2026-10-31T08:00:00Z",
  },
]

function settingRow(key: string): HTMLElement {
  return screen.getByTestId(`setting-row-${key}`)
}

function displayValue(key: string): HTMLElement {
  return screen.getByTestId(`setting-value-${key}`)
}

describe("SystemSettings", () => {
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

  it("groups settings by category and marks restart-required keys", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: settings }))

    render(<SystemSettings />)

    expect(await screen.findByRole("heading", { name: /执行/ })).toBeInTheDocument()
    expect(screen.getByRole("heading", { name: /运行时/ })).toBeInTheDocument()
    expect(screen.getByRole("heading", { name: /保留与清理/ })).toBeInTheDocument()

    expect(settingRow("execution.global_concurrency")).toHaveTextContent("最大并发用例数")
    expect(within(settingRow("runtime.snowflake_node_id")).getByText("需重启生效")).toBeVisible()
    expect(within(settingRow("execution.global_concurrency")).queryByText("需重启生效")).toBeNull()
    expect(displayValue("retention.gc_dry_run")).toHaveTextContent("启用")
  })

  it("saves one numeric setting and updates the displayed value", async () => {
    const updated: Setting = { ...settings[0], value: 5, version: 2 }
    fetchMock.mockImplementation(async (_input, init) => {
      if (init?.method === "PATCH") return jsonResponse({ data: updated })
      return jsonResponse({ data: settings })
    })

    render(<SystemSettings />)
    await screen.findByTestId("setting-row-execution.global_concurrency")

    const row = settingRow("execution.global_concurrency")
    fireEvent.click(within(row).getByRole("button", { name: "编辑" }))

    const input = within(row).getByLabelText("execution.global_concurrency 新值")
    expect(input).toHaveValue(3)
    fireEvent.change(input, { target: { value: "5" } })
    fireEvent.click(within(row).getByRole("button", { name: "保存" }))

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
    const [url, init] = fetchMock.mock.calls[1]
    expect(url).toBe("http://localhost:13333/api/admin/settings/execution.global_concurrency")
    expect(init?.method).toBe("PATCH")
    expect(JSON.parse(String(init?.body))).toEqual({ value: 5 })

    await waitFor(() => expect(displayValue("execution.global_concurrency")).toHaveTextContent("5"))
    expect(within(row).getByRole("status")).toHaveTextContent("已保存")
  })

  it("rejects invalid numbers and JSON inline without calling the API", async () => {
    fetchMock.mockResolvedValue(jsonResponse({ data: settings }))

    render(<SystemSettings />)
    await screen.findByTestId("setting-row-execution.global_concurrency")

    const numberRow = settingRow("execution.global_concurrency")
    fireEvent.click(within(numberRow).getByRole("button", { name: "编辑" }))
    fireEvent.change(within(numberRow).getByLabelText("execution.global_concurrency 新值"), {
      target: { value: "" },
    })
    fireEvent.click(within(numberRow).getByRole("button", { name: "保存" }))
    expect(await within(numberRow).findByRole("alert")).toHaveTextContent("请输入数字")

    const jsonRow = settingRow("feature.experimental_flags")
    fireEvent.click(within(jsonRow).getByRole("button", { name: "编辑" }))
    fireEvent.change(within(jsonRow).getByLabelText("feature.experimental_flags 新值"), {
      target: { value: "{bad json" },
    })
    fireEvent.click(within(jsonRow).getByRole("button", { name: "保存" }))
    expect(await within(jsonRow).findByRole("alert")).toHaveTextContent("JSON 格式不正确")

    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it("masks secret values and treats a blank replacement as no change", async () => {
    const secret: Setting = {
      key: "llm.secret_probe",
      category: "llm",
      type: "string",
      value: "sk-should-never-render",
      is_secret: true,
      restart_required: false,
      description: "上游密钥",
      version: 1,
      updated_at: "2026-10-31T08:00:00Z",
    }
    fetchMock.mockResolvedValue(jsonResponse({ data: [secret] }))

    render(<SystemSettings />)
    await screen.findByTestId("setting-row-llm.secret_probe")

    expect(displayValue("llm.secret_probe")).toHaveTextContent("已设置（已隐藏）")
    expect(screen.queryByText("sk-should-never-render")).toBeNull()

    const row = settingRow("llm.secret_probe")
    fireEvent.click(within(row).getByRole("button", { name: "编辑" }))
    const input = within(row).getByLabelText("llm.secret_probe 新值")
    expect(input).toHaveAttribute("type", "password")
    fireEvent.click(within(row).getByRole("button", { name: "保存" }))

    await waitFor(() => expect(within(row).getByRole("status")).toHaveTextContent("未修改"))
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(screen.queryByText("sk-should-never-render")).toBeNull()
  })

  it("shows an inline API error and keeps the editor open", async () => {
    fetchMock.mockImplementation(async (_input, init) => {
      if (init?.method === "PATCH") {
        return jsonResponse(
          {
            error: {
              code: "invalid_argument",
              message: "value must be between 1 and 32",
              request_id: "req_set0000001",
              retryable: false,
              details: { field: "value" },
            },
          },
          400,
        )
      }
      return jsonResponse({ data: settings })
    })

    render(<SystemSettings />)
    await screen.findByTestId("setting-row-execution.global_concurrency")

    const row = settingRow("execution.global_concurrency")
    fireEvent.click(within(row).getByRole("button", { name: "编辑" }))
    fireEvent.change(within(row).getByLabelText("execution.global_concurrency 新值"), {
      target: { value: "64" },
    })
    fireEvent.click(within(row).getByRole("button", { name: "保存" }))

    expect(await within(row).findByRole("alert")).toHaveTextContent(
      "value must be between 1 and 32",
    )
    expect(within(row).getByLabelText("execution.global_concurrency 新值")).toBeInTheDocument()
  })
})
