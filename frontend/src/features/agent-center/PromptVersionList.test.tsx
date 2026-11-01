import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

const agentCenterApi = vi.hoisted(() => ({
  listPromptRevisions: vi.fn(),
  restoreAgentPrompt: vi.fn(),
}))
vi.mock("./api", () => agentCenterApi)

import { PromptVersionList } from "./PromptVersionList"
import type { PromptRevision } from "./types"

const revisionFixture: PromptRevision = {
  version: 2,
  system_prompt: "[profile:clarification_host]\n旧版本内容",
  note: "第一次编辑",
  source: "admin",
  actor_id: "7",
  created_at: "2026-11-01T10:00:00Z",
}

const currentPrompt = "[profile:clarification_host]\n当前内容"

describe("PromptVersionList", () => {
  beforeEach(() => {
    agentCenterApi.listPromptRevisions.mockReset()
    agentCenterApi.restoreAgentPrompt.mockReset()
    agentCenterApi.listPromptRevisions.mockResolvedValue({
      items: [revisionFixture],
      nextCursor: "",
      hasMore: false,
    })
    agentCenterApi.restoreAgentPrompt.mockResolvedValue({
      profile: { key: "clarification_host" },
      version: 3,
      changed: true,
    })
  })

  afterEach(cleanup)

  it("lists versions with source, actor and note", async () => {
    render(
      <PromptVersionList
        profileKey="clarification_host"
        currentPrompt={currentPrompt}
        onLoad={vi.fn()}
        onRestored={vi.fn()}
      />,
    )

    expect(await screen.findByTestId("agent-prompt-versions")).toBeInTheDocument()
    const row = screen.getByTestId("agent-prompt-version-2")
    expect(row).toHaveTextContent("v2")
    expect(row).toHaveTextContent("admin")
    expect(row).toHaveTextContent("第一次编辑")
    expect(agentCenterApi.listPromptRevisions).toHaveBeenCalledWith("clarification_host", {
      limit: 20,
    })
  })

  it("shows an empty state when there is no history", async () => {
    agentCenterApi.listPromptRevisions.mockResolvedValue({
      items: [],
      nextCursor: "",
      hasMore: false,
    })
    render(
      <PromptVersionList
        profileKey="clarification_host"
        currentPrompt={currentPrompt}
        onLoad={vi.fn()}
        onRestored={vi.fn()}
      />,
    )
    expect(await screen.findByTestId("agent-prompt-versions-empty")).toHaveTextContent(
      "尚无历史版本",
    )
  })

  it("shows the full revision text and the line diff against the current prompt", async () => {
    render(
      <PromptVersionList
        profileKey="clarification_host"
        currentPrompt={currentPrompt}
        onLoad={vi.fn()}
        onRestored={vi.fn()}
      />,
    )

    await screen.findByTestId("agent-prompt-version-2")
    fireEvent.click(screen.getByTestId("agent-prompt-version-view-2"))
    const view = await screen.findByTestId("agent-prompt-version-view-text")
    expect(view).toHaveTextContent("旧版本内容")

    fireEvent.click(screen.getByTestId("agent-prompt-version-diff-2"))
    const diff = await screen.findByTestId("agent-prompt-version-diff-panel-2")
    expect(diff).toHaveTextContent("-")
    expect(diff).toHaveTextContent("旧版本内容")
    expect(diff).toHaveTextContent("+")
    expect(diff).toHaveTextContent("当前内容")
  })

  it("loads a revision into the editor", async () => {
    const onLoad = vi.fn()
    render(
      <PromptVersionList
        profileKey="clarification_host"
        currentPrompt={currentPrompt}
        onLoad={onLoad}
        onRestored={vi.fn()}
      />,
    )
    await screen.findByTestId("agent-prompt-version-2")
    fireEvent.click(screen.getByTestId("agent-prompt-version-load-2"))
    expect(onLoad).toHaveBeenCalledWith(revisionFixture)
  })

  it("restores a revision and refreshes the history", async () => {
    const onRestored = vi.fn()
    render(
      <PromptVersionList
        profileKey="clarification_host"
        currentPrompt={currentPrompt}
        onLoad={vi.fn()}
        onRestored={onRestored}
      />,
    )
    await screen.findByTestId("agent-prompt-version-2")
    fireEvent.click(screen.getByTestId("agent-prompt-version-restore-2"))

    await waitFor(() =>
      expect(agentCenterApi.restoreAgentPrompt).toHaveBeenCalledWith("clarification_host", 2),
    )
    await waitFor(() => expect(onRestored).toHaveBeenCalled())
    await waitFor(() => expect(agentCenterApi.listPromptRevisions).toHaveBeenCalledTimes(2))
  })

  it("renders a retryable error", async () => {
    agentCenterApi.listPromptRevisions.mockRejectedValueOnce(new Error("历史不可用"))
    render(
      <PromptVersionList
        profileKey="clarification_host"
        currentPrompt={currentPrompt}
        onLoad={vi.fn()}
        onRestored={vi.fn()}
      />,
    )
    expect(await screen.findByText("历史不可用")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(await screen.findByTestId("agent-prompt-version-2")).toBeInTheDocument()
  })
})
