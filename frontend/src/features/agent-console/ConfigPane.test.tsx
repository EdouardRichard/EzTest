import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const mocks = vi.hoisted(() => ({
  listTools: vi.fn(),
  updateProfile: vi.fn(),
}))

vi.mock("./agent-api", () => ({
  listTools: mocks.listTools,
  updateProfile: mocks.updateProfile,
}))

vi.mock("@/lib/api-client", () => {
  class ApiError extends Error {
    readonly status: number
    readonly code: string
    readonly details: Record<string, unknown>
    constructor(init: {
      status: number
      code: string
      message: string
      details?: Record<string, unknown>
    }) {
      super(init.message)
      this.name = "ApiError"
      this.status = init.status
      this.code = init.code
      this.details = init.details ?? {}
    }
  }
  return { ApiError }
})

import { ApiError } from "@/lib/api-client"

import type { AgentProfile, AgentTool } from "./agent-api"
import { ConfigPane } from "./ConfigPane"

const PROFILE: AgentProfile = {
  id: 2,
  key: "runtime-diagnostic",
  name: "运行时验证 Agent",
  description: "",
  builtin: true,
  enabled: true,
  system_prompt: "you are a runtime diagnostic agent",
  model: "",
  allowed_tools: ["file.list", "file.read"],
  max_turns: 12,
  run_seconds: 600,
  hard_timeout_seconds: 60,
  thinking: "enabled",
  reasoning_effort: "max",
  max_output_tokens: 32768,
  created_at: "2026-10-27T00:00:00Z",
  updated_at: "2026-10-27T00:00:00Z",
}

const OTHER_PROFILE: AgentProfile = {
  ...PROFILE,
  id: 3,
  key: "secondary",
  name: "辅助 Agent",
  builtin: false,
  system_prompt: "you are a secondary agent",
}

const TOOLS: AgentTool[] = [
  { name: "file.list", description: "列出目录", schema: {} },
  { name: "file.read", description: "读取文件", schema: {} },
  { name: "http.get", description: "发起 HTTP GET", schema: {} },
  { name: "test.echo", description: "回显参数", schema: {} },
  { name: "test.fail", description: "固定失败", schema: {} },
]

describe("ConfigPane", () => {
  beforeEach(() => {
    mocks.listTools.mockReset()
    mocks.updateProfile.mockReset()
    mocks.listTools.mockResolvedValue(TOOLS)
    mocks.updateProfile.mockResolvedValue(PROFILE)
  })

  afterEach(() => {
    cleanup()
  })

  it("loads the tool catalog and renders the built-in profile in the editor", async () => {
    render(<ConfigPane profiles={[PROFILE]} onSaved={vi.fn()} />)

    expect(screen.getByRole("button", { name: "运行时验证 Agent" })).toBeTruthy()
    expect(screen.getByText("内置")).toBeTruthy()
    expect((screen.getByLabelText("系统提示词") as HTMLTextAreaElement).value).toBe(
      PROFILE.system_prompt,
    )

    await waitFor(() => expect(mocks.listTools).toHaveBeenCalledTimes(1))
    expect(await screen.findByLabelText("http.get")).toBeTruthy()
    expect((screen.getByLabelText("file.read") as HTMLInputElement).checked).toBe(true)
    expect((screen.getByLabelText("test.fail") as HTMLInputElement).checked).toBe(false)
  })

  it("switches the editor when another profile is selected", async () => {
    render(<ConfigPane profiles={[PROFILE, OTHER_PROFILE]} onSaved={vi.fn()} />)
    await screen.findByLabelText("http.get")

    fireEvent.click(screen.getByRole("button", { name: "辅助 Agent" }))

    expect((screen.getByLabelText("系统提示词") as HTMLTextAreaElement).value).toBe(
      OTHER_PROFILE.system_prompt,
    )
    expect(screen.getByRole("button", { name: "辅助 Agent" }).getAttribute("aria-current")).toBe(
      "true",
    )
    expect(
      screen.getByRole("button", { name: "运行时验证 Agent" }).getAttribute("aria-current"),
    ).toBeNull()
  })

  it("reports a tool catalog failure and still renders the editor", async () => {
    mocks.listTools.mockRejectedValue(
      new ApiError({ status: 403, code: "forbidden", message: "forbidden" }),
    )

    render(<ConfigPane profiles={[PROFILE]} onSaved={vi.fn()} />)

    expect(await screen.findByRole("alert")).toBeTruthy()
    expect(screen.getByRole("alert").textContent).toContain("forbidden")
    expect((screen.getByLabelText("系统提示词") as HTMLTextAreaElement).value).toBe(
      PROFILE.system_prompt,
    )
  })

  it("forwards a saved profile to onSaved", async () => {
    mocks.updateProfile.mockResolvedValue({ ...PROFILE, enabled: false })
    const onSaved = vi.fn()
    render(<ConfigPane profiles={[PROFILE]} onSaved={onSaved} />)
    await screen.findByLabelText("http.get")

    fireEvent.click(screen.getByLabelText("启用"))
    fireEvent.click(screen.getByRole("button", { name: "保存配置" }))

    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1))
    expect(mocks.updateProfile).toHaveBeenCalledWith(2, { enabled: false })
    const saved = onSaved.mock.calls[0]?.[0] as AgentProfile | undefined
    expect(saved?.enabled).toBe(false)
  })
})
