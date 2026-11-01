import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

const agentCenterApi = vi.hoisted(() => ({
  saveAgentPrompt: vi.fn(),
  resetAgentPrompt: vi.fn(),
  previewAgentPrompt: vi.fn(),
  listPromptRevisions: vi.fn(),
  restoreAgentPrompt: vi.fn(),
}))
vi.mock("./api", () => agentCenterApi)

import { ApiError } from "@/lib/api-client"

import { PromptEditor } from "./PromptEditor"
import type { ProfileDetail } from "./types"

const detailFixture: ProfileDetail = {
  profile: {
    id: "101",
    key: "clarification_host",
    name: "澄清 Host",
    system_prompt: "[profile:clarification_host]\n原始提示词",
    allowed_tools: ["requirement.read"],
    model: null,
    budgets: { max_turns: 12, soft_seconds: 300, hard_seconds: 600 },
    enabled: true,
    prompt_version: 3,
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-02T00:00:00Z",
  },
  default_system_prompt: "[profile:clarification_host]\n默认提示词",
  variables: [
    {
      name: "VersionTitle",
      description: "当前版本标题",
      example: "订单系统 Q4",
      source: "runtime",
    },
    { name: "TaskGoal", description: "当前任务目标", example: "回答登录问题", source: "runtime" },
  ],
  prompt_version: 3,
}

const mutationFixture = {
  profile: { ...detailFixture.profile, system_prompt: "new", prompt_version: 4 },
  version: 4,
  changed: true,
}

describe("PromptEditor", () => {
  beforeEach(() => {
    agentCenterApi.saveAgentPrompt.mockReset()
    agentCenterApi.resetAgentPrompt.mockReset()
    agentCenterApi.previewAgentPrompt.mockReset()
    agentCenterApi.listPromptRevisions.mockReset()
    agentCenterApi.restoreAgentPrompt.mockReset()
    agentCenterApi.saveAgentPrompt.mockResolvedValue(mutationFixture)
    agentCenterApi.resetAgentPrompt.mockResolvedValue(mutationFixture)
    agentCenterApi.previewAgentPrompt.mockResolvedValue({
      rendered: "[profile:clarification_host]\n预览结果",
    })
    agentCenterApi.listPromptRevisions.mockResolvedValue({
      items: [],
      nextCursor: "",
      hasMore: false,
    })
  })

  afterEach(cleanup)

  it("renders the prompt, variable chips and the next-run hint", async () => {
    render(<PromptEditor detail={detailFixture} onMutated={vi.fn()} />)

    expect(await screen.findByTestId("agent-prompt-editor")).toBeInTheDocument()
    const textarea = screen.getByTestId("agent-prompt-textarea") as HTMLTextAreaElement
    expect(textarea.value).toBe(detailFixture.profile.system_prompt)
    expect(screen.getByTestId("agent-prompt-counter")).toHaveTextContent("/ 32768")
    expect(screen.getByTestId("agent-prompt-save-hint")).toHaveTextContent("下一次 run 生效")
    expect(screen.getByTestId("agent-prompt-variable-VersionTitle")).toBeInTheDocument()
    expect(screen.getByTestId("agent-prompt-variable-TaskGoal")).toBeInTheDocument()
  })

  it("inserts a variable at the cursor and updates the counter", async () => {
    render(<PromptEditor detail={detailFixture} onMutated={vi.fn()} />)
    const textarea = (await screen.findByTestId("agent-prompt-textarea")) as HTMLTextAreaElement

    fireEvent.change(textarea, { target: { value: "[profile:clarification_host]\nAB" } })
    textarea.setSelectionRange(1, 1)
    fireEvent.click(screen.getByTestId("agent-prompt-variable-VersionTitle"))

    await waitFor(() => expect(textarea.value).toContain("{{.VersionTitle}}"))
    expect(textarea.value.startsWith("[{{.VersionTitle}}profile:clarification_host]")).toBe(true)
    expect(screen.getByTestId("agent-prompt-counter")).toHaveTextContent(
      `${Array.from(textarea.value).length} / 32768`,
    )
  })

  it("saves a new version with note and expected version", async () => {
    const onMutated = vi.fn()
    render(<PromptEditor detail={detailFixture} onMutated={onMutated} />)
    const textarea = (await screen.findByTestId("agent-prompt-textarea")) as HTMLTextAreaElement
    const next = "[profile:clarification_host]\n新提示词 {{.VersionTitle}}"
    fireEvent.change(textarea, { target: { value: next } })
    fireEvent.change(screen.getByTestId("agent-prompt-note"), { target: { value: "编辑器保存" } })
    fireEvent.click(screen.getByTestId("agent-prompt-save"))

    await waitFor(() =>
      expect(agentCenterApi.saveAgentPrompt).toHaveBeenCalledWith("clarification_host", {
        system_prompt: next,
        note: "编辑器保存",
        expected_version: 3,
      }),
    )
    expect(await screen.findByTestId("agent-prompt-message")).toHaveTextContent("下一次 run 生效")
    await waitFor(() => expect(onMutated).toHaveBeenCalledTimes(1))
  })

  it("warns when the profile marker is missing", async () => {
    render(<PromptEditor detail={detailFixture} onMutated={vi.fn()} />)
    const textarea = (await screen.findByTestId("agent-prompt-textarea")) as HTMLTextAreaElement
    fireEvent.change(textarea, { target: { value: "no marker" } })
    expect(await screen.findByTestId("agent-prompt-marker-warning")).toHaveTextContent(
      "[profile:clarification_host]",
    )
  })

  it("previews the rendered prompt", async () => {
    render(<PromptEditor detail={detailFixture} onMutated={vi.fn()} />)
    await screen.findByTestId("agent-prompt-editor")
    fireEvent.click(screen.getByTestId("agent-prompt-preview-button"))

    await waitFor(() =>
      expect(agentCenterApi.previewAgentPrompt).toHaveBeenCalledWith(
        "clarification_host",
        expect.objectContaining({ system_prompt: detailFixture.profile.system_prompt }),
      ),
    )
    expect(await screen.findByTestId("agent-prompt-preview-output")).toHaveTextContent("预览结果")
  })

  it("shows a field-level 400 error inline", async () => {
    agentCenterApi.saveAgentPrompt.mockRejectedValueOnce(
      new ApiError({
        status: 400,
        code: "invalid_argument",
        message: "must keep the [profile:clarification_host] marker",
        details: { field: "system_prompt", reason: "missing_profile_marker" },
      }),
    )
    render(<PromptEditor detail={detailFixture} onMutated={vi.fn()} />)
    await screen.findByTestId("agent-prompt-editor")
    fireEvent.click(screen.getByTestId("agent-prompt-save"))

    const message = await screen.findByTestId("agent-prompt-message")
    expect(message).toHaveTextContent("system_prompt")
    expect(message).toHaveTextContent("profile:clarification_host")
  })

  it("resets to the default prompt", async () => {
    const onMutated = vi.fn()
    render(<PromptEditor detail={detailFixture} onMutated={onMutated} />)
    await screen.findByTestId("agent-prompt-editor")
    fireEvent.click(screen.getByTestId("agent-prompt-reset"))

    await waitFor(() =>
      expect(agentCenterApi.resetAgentPrompt).toHaveBeenCalledWith("clarification_host"),
    )
    await waitFor(() => expect(onMutated).toHaveBeenCalled())
  })
})
