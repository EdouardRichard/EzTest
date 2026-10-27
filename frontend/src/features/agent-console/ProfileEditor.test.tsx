import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const mocks = vi.hoisted(() => ({
  updateProfile: vi.fn(),
}))

vi.mock("./agent-api", () => ({
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
import { ProfileEditor } from "./ProfileEditor"

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

const TOOLS: AgentTool[] = [
  { name: "file.list", description: "列出目录", schema: {} },
  { name: "file.read", description: "读取文件", schema: {} },
  { name: "http.get", description: "发起 HTTP GET", schema: {} },
  { name: "test.echo", description: "回显参数", schema: {} },
  { name: "test.fail", description: "固定失败", schema: {} },
]

function makeProfile(overrides: Partial<AgentProfile> = {}): AgentProfile {
  return { ...PROFILE, ...overrides }
}

function renderEditor(profile: AgentProfile = PROFILE) {
  const onSaved = vi.fn()
  render(<ProfileEditor profile={profile} tools={TOOLS} onSaved={onSaved} />)
  return { onSaved }
}

describe("ProfileEditor", () => {
  beforeEach(() => {
    mocks.updateProfile.mockReset()
    mocks.updateProfile.mockResolvedValue(PROFILE)
  })

  afterEach(() => {
    cleanup()
  })

  it("renders the built-in profile values", () => {
    renderEditor()

    expect((screen.getByLabelText("系统提示词") as HTMLTextAreaElement).value).toBe(
      PROFILE.system_prompt,
    )
    expect((screen.getByLabelText("模型") as HTMLInputElement).value).toBe("")
    expect((screen.getByLabelText("最大回合") as HTMLInputElement).value).toBe("12")
    expect((screen.getByLabelText("运行时长（秒）") as HTMLInputElement).value).toBe("600")
    expect((screen.getByLabelText("硬超时（秒）") as HTMLInputElement).value).toBe("60")
    expect((screen.getByLabelText("思考模式") as HTMLSelectElement).value).toBe("enabled")
    expect((screen.getByLabelText("思考强度") as HTMLSelectElement).value).toBe("max")
    expect((screen.getByLabelText("输出上限") as HTMLInputElement).value).toBe("32768")
    expect((screen.getByLabelText("启用") as HTMLInputElement).checked).toBe(true)

    expect(screen.getByRole("group", { name: "允许工具" })).toBeTruthy()
    expect((screen.getByLabelText("file.list") as HTMLInputElement).checked).toBe(true)
    expect((screen.getByLabelText("file.read") as HTMLInputElement).checked).toBe(true)
    expect((screen.getByLabelText("http.get") as HTMLInputElement).checked).toBe(false)
    expect((screen.getByLabelText("test.echo") as HTMLInputElement).checked).toBe(false)
    expect((screen.getByLabelText("test.fail") as HTMLInputElement).checked).toBe(false)
  })

  it("patch sends only changed fields and shows the next-run notice", async () => {
    mocks.updateProfile.mockResolvedValue(makeProfile({ system_prompt: "新的系统提示词" }))
    const { onSaved } = renderEditor()

    fireEvent.change(screen.getByLabelText("系统提示词"), {
      target: { value: "新的系统提示词" },
    })
    fireEvent.click(screen.getByRole("button", { name: "保存配置" }))

    await waitFor(() => expect(mocks.updateProfile).toHaveBeenCalledTimes(1))
    expect(mocks.updateProfile).toHaveBeenCalledWith(2, { system_prompt: "新的系统提示词" })
    expect((await screen.findByText("下次运行生效")).textContent).toBe("下次运行生效")
    expect(onSaved).toHaveBeenCalledWith(makeProfile({ system_prompt: "新的系统提示词" }))
  })

  it("rejects out-of-range numbers before calling the api", () => {
    renderEditor()

    fireEvent.change(screen.getByLabelText("最大回合"), { target: { value: "101" } })
    fireEvent.click(screen.getByRole("button", { name: "保存配置" }))

    expect(screen.getByText("最大回合必须是 0 到 100 之间的整数")).toBeTruthy()
    expect(mocks.updateProfile).not.toHaveBeenCalled()
  })

  it.each([
    ["运行时长（秒）", "29", "运行时长（秒）必须是 30 到 3600 之间的整数"],
    ["硬超时（秒）", "4", "硬超时（秒）必须是 5 到 300 之间的整数"],
    ["输出上限", "1023", "输出上限必须是 1024 到 131072 之间的整数"],
  ])("rejects %s = %s before calling the api", (label, value, message) => {
    renderEditor()

    fireEvent.change(screen.getByLabelText(label), { target: { value } })
    fireEvent.click(screen.getByRole("button", { name: "保存配置" }))

    expect(screen.getByText(message)).toBeTruthy()
    expect(mocks.updateProfile).not.toHaveBeenCalled()
  })

  it("rejects an empty or oversized system prompt before calling the api", () => {
    renderEditor()

    fireEvent.change(screen.getByLabelText("系统提示词"), { target: { value: "   " } })
    fireEvent.click(screen.getByRole("button", { name: "保存配置" }))
    expect(screen.getByText("系统提示词不能为空")).toBeTruthy()
    expect(mocks.updateProfile).not.toHaveBeenCalled()

    fireEvent.change(screen.getByLabelText("系统提示词"), {
      target: { value: "x".repeat(32769) },
    })
    fireEvent.click(screen.getByRole("button", { name: "保存配置" }))
    expect(screen.getByText("系统提示词不能超过 32768 个字符")).toBeTruthy()
    expect(mocks.updateProfile).not.toHaveBeenCalled()
  })

  it("disables reasoning effort when thinking is disabled and normalizes it to none", async () => {
    mocks.updateProfile.mockResolvedValue(
      makeProfile({ thinking: "disabled", reasoning_effort: "none" }),
    )
    renderEditor()

    fireEvent.change(screen.getByLabelText("思考模式"), { target: { value: "disabled" } })

    const effort = screen.getByLabelText("思考强度") as HTMLSelectElement
    expect(effort.value).toBe("none")
    expect(effort.disabled).toBe(true)
    expect(screen.getByText(/思考模式为 disabled/)).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: "保存配置" }))

    await waitFor(() => expect(mocks.updateProfile).toHaveBeenCalledTimes(1))
    expect(mocks.updateProfile).toHaveBeenCalledWith(2, {
      thinking: "disabled",
      reasoning_effort: "none",
    })
  })

  it("sends allowed_tools and enabled only when they change", async () => {
    renderEditor()

    fireEvent.click(screen.getByLabelText("http.get"))
    fireEvent.click(screen.getByLabelText("启用"))
    fireEvent.click(screen.getByRole("button", { name: "保存配置" }))

    await waitFor(() => expect(mocks.updateProfile).toHaveBeenCalledTimes(1))
    expect(mocks.updateProfile).toHaveBeenCalledWith(2, {
      allowed_tools: ["file.list", "file.read", "http.get"],
      enabled: false,
    })
  })

  it("shows an ApiError when saving fails", async () => {
    mocks.updateProfile.mockRejectedValue(
      new ApiError({
        status: 422,
        code: "invalid_argument",
        message: "max_turns 超出范围",
        details: { field: "max_turns" },
      }),
    )
    renderEditor()

    fireEvent.change(screen.getByLabelText("系统提示词"), { target: { value: "改一下" } })
    fireEvent.click(screen.getByRole("button", { name: "保存配置" }))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("max_turns 超出范围")
    expect(screen.queryByText("下次运行生效")).toBeNull()
  })
})
