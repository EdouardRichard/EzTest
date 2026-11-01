import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

const agentCenterApi = vi.hoisted(() => ({
  getAgentProfile: vi.fn(),
  patchAgentProfile: vi.fn(),
}))
vi.mock("./api", () => agentCenterApi)
vi.mock("./PromptEditor", () => ({
  PromptEditor: () => null,
}))

import { ApiError } from "@/lib/api-client"

import { ProfileEditor } from "./ProfileEditor"
import type { AgentTool, LLMModel, ProfileDetail } from "./types"

const detailFixture: ProfileDetail = {
  profile: {
    id: "101",
    key: "clarification_host",
    name: "澄清 Host",
    system_prompt: "[profile:clarification_host] prompt",
    allowed_tools: ["requirement.read"],
    model: "deepseek-flash",
    budgets: { max_turns: 12, soft_seconds: 300, hard_seconds: 600 },
    enabled: true,
    prompt_version: 3,
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-02T00:00:00Z",
  },
  default_system_prompt: "[profile:clarification_host] default",
  variables: [],
  prompt_version: 3,
}

const toolsFixture: AgentTool[] = [
  { name: "requirement.read", description: "读取需求", parameters: { type: "object" } },
  { name: "context.task_pack", description: "任务上下文", parameters: { type: "object" } },
]

const modelsFixture: LLMModel[] = [
  { id: "1", model: "deepseek-flash", display_name: "DeepSeek Flash", status: "active" },
  { id: "2", model: "deepseek-pro", display_name: "DeepSeek Pro", status: "active" },
]

describe("ProfileEditor", () => {
  beforeEach(() => {
    agentCenterApi.getAgentProfile.mockReset()
    agentCenterApi.patchAgentProfile.mockReset()
    agentCenterApi.getAgentProfile.mockResolvedValue(detailFixture)
    agentCenterApi.patchAgentProfile.mockResolvedValue(detailFixture.profile)
  })

  afterEach(cleanup)

  it("loads the profile and renders only the writable config fields", async () => {
    render(
      <ProfileEditor
        profileKey="clarification_host"
        tools={toolsFixture}
        models={modelsFixture}
        onClose={vi.fn()}
        onSaved={vi.fn()}
      />,
    )

    expect(await screen.findByTestId("agent-center-profile-config")).toBeInTheDocument()
    expect(agentCenterApi.getAgentProfile).toHaveBeenCalledWith("clarification_host")
    expect(screen.getByTestId("agent-center-model")).toHaveValue("deepseek-flash")
    expect(screen.getByTestId("agent-center-budget-max-turns")).toHaveValue(12)
    expect(screen.getByTestId("agent-center-budget-soft-seconds")).toHaveValue(300)
    expect(screen.getByTestId("agent-center-budget-hard-seconds")).toHaveValue(600)
    expect(screen.getByTestId("agent-center-tool-requirement.read")).toBeChecked()
    expect(screen.getByTestId("agent-center-tool-context.task_pack")).not.toBeChecked()

    // enabled/name/wrapup/triggers/MCP/Skill are not editable here.
    expect(screen.queryByTestId("agent-center-enabled")).not.toBeInTheDocument()
    expect(screen.queryByText("MCP")).not.toBeInTheDocument()
    expect(screen.queryByText("Skill")).not.toBeInTheDocument()
  })

  it("saves model, budgets and allowed tools", async () => {
    const onSaved = vi.fn()
    render(
      <ProfileEditor
        profileKey="clarification_host"
        tools={toolsFixture}
        models={modelsFixture}
        onClose={vi.fn()}
        onSaved={onSaved}
      />,
    )
    await screen.findByTestId("agent-center-profile-config")

    fireEvent.change(screen.getByTestId("agent-center-budget-max-turns"), {
      target: { value: "20" },
    })
    fireEvent.click(screen.getByTestId("agent-center-tool-context.task_pack"))
    fireEvent.click(screen.getByTestId("agent-center-profile-save"))

    await waitFor(() =>
      expect(agentCenterApi.patchAgentProfile).toHaveBeenCalledWith("clarification_host", {
        model: "deepseek-flash",
        budgets: { max_turns: 20, soft_seconds: 300, hard_seconds: 600 },
        allowed_tools: ["requirement.read", "context.task_pack"],
      }),
    )
    expect(await screen.findByTestId("agent-center-profile-saved")).toHaveTextContent(
      "下一次 run 生效",
    )
    expect(onSaved).toHaveBeenCalledTimes(1)
  })

  it("sends a null model when the default is selected", async () => {
    render(
      <ProfileEditor
        profileKey="clarification_host"
        tools={toolsFixture}
        models={modelsFixture}
        onClose={vi.fn()}
        onSaved={vi.fn()}
      />,
    )
    await screen.findByTestId("agent-center-profile-config")
    fireEvent.change(screen.getByTestId("agent-center-model"), { target: { value: "" } })
    fireEvent.click(screen.getByTestId("agent-center-profile-save"))

    await waitFor(() =>
      expect(agentCenterApi.patchAgentProfile).toHaveBeenCalledWith(
        "clarification_host",
        expect.objectContaining({ model: null }),
      ),
    )
  })

  it("shows a field-level validation error inline", async () => {
    agentCenterApi.patchAgentProfile.mockRejectedValueOnce(
      new ApiError({
        status: 400,
        code: "invalid_argument",
        message: "unknown tool: bogus.tool",
        details: { field: "allowed_tools" },
      }),
    )
    render(
      <ProfileEditor
        profileKey="clarification_host"
        tools={toolsFixture}
        models={modelsFixture}
        onClose={vi.fn()}
        onSaved={vi.fn()}
      />,
    )
    await screen.findByTestId("agent-center-profile-config")
    fireEvent.click(screen.getByTestId("agent-center-profile-save"))

    const error = await screen.findByTestId("agent-center-config-error")
    expect(error).toHaveTextContent("allowed_tools")
    expect(error).toHaveTextContent("unknown tool")
  })

  it("retries a failed detail load and closes the drawer", async () => {
    agentCenterApi.getAgentProfile.mockRejectedValueOnce(new Error("详情不可用"))
    const onClose = vi.fn()
    render(
      <ProfileEditor
        profileKey="clarification_host"
        tools={toolsFixture}
        models={modelsFixture}
        onClose={onClose}
        onSaved={vi.fn()}
      />,
    )
    expect(await screen.findByText("详情不可用")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(await screen.findByTestId("agent-center-profile-config")).toBeInTheDocument()

    fireEvent.click(screen.getByLabelText("关闭对话框"))
    expect(onClose).toHaveBeenCalled()
  })
})
