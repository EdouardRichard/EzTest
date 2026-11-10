import { act, cleanup, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import type { VersionEvent } from "@/features/clarification/types"
import type { VersionEventHandlers } from "@/features/clarification/useVersionEvents"

const designApi = vi.hoisted(() => ({
  getDesignGraph: vi.fn(),
  getDesignOverview: vi.fn(),
  listDesignConversations: vi.fn(),
  createDesignConversation: vi.fn(),
  getDesignCase: vi.fn(),
  confirmCases: vi.fn(),
  confirmStageCases: vi.fn(),
  rollbackCaseVersion: vi.fn(),
}))
vi.mock("./api", () => designApi)

// DesignChat mounts the clarification thread through the clarification api.
const clarificationApi = vi.hoisted(() => ({
  listMessages: vi.fn(),
  postMessage: vi.fn(),
  getTeamRoom: vi.fn(),
  uploadContent: vi.fn(),
  rollbackRequirement: vi.fn(),
  getAttachmentPendingDetails: vi.fn(),
}))
vi.mock("@/features/clarification/api", () => clarificationApi)

const conversationApi = vi.hoisted(() => ({
  listPublicModels: vi.fn(),
  updateConversationModel: vi.fn(),
}))
vi.mock("@/features/conversation/api", () => conversationApi)

vi.mock("@/features/versions/api", () => ({ updateVersion: vi.fn() }))

// Capture the handlers the tab registers on the version channel so the test can
// deliver `agent.activity` / `agent.run.*` frames without a real SSE stream.
const versionEvents = vi.hoisted(() => ({
  handlers: null as VersionEventHandlers | null,
}))
vi.mock("@/features/clarification/useVersionEvents", () => ({
  useVersionEvents: (_versionId: string, handlers: VersionEventHandlers) => {
    versionEvents.handlers = handlers
    return "live"
  },
}))

const navigation = vi.hoisted(() => ({ replace: vi.fn(), push: vi.fn() }))
vi.mock("next/navigation", () => ({
  useRouter: () => navigation,
  usePathname: () => "/versions/2001",
}))

// The activity contract is about the workspace shell, not React Flow.
vi.mock("./DesignMindmap", async () => {
  const React = await import("react")
  return {
    DesignMindmap: () => React.createElement("div", { "data-testid": "design-mindmap" }),
  }
})

import { DesignTab } from "./DesignTab"
import { designCaseDetailFixture, designGraphFixture, designOverviewFixture } from "./test-fixtures"

const TIMESTAMP = "2026-10-31T12:00:00Z"
const CONVERSATION_ID = "4009"
const RUN_ID = "9101"

function versionEvent(type: string, payload: Record<string, unknown>): VersionEvent {
  return {
    seq: 1,
    channel: "version:2001",
    type,
    scope: { version_id: "2001", conversation_id: CONVERSATION_ID, agent_run_id: RUN_ID },
    payload,
    actor_id: null,
    created_at: TIMESTAMP,
  }
}

function deliver(handlers: (current: VersionEventHandlers | null) => void): void {
  act(() => handlers(versionEvents.handlers))
}

async function renderWorkspace(): Promise<void> {
  render(<DesignTab versionId="2001" />)
  await screen.findByTestId("design-mindmap")
  // The thread is the renderer of the activity bubble.
  await screen.findByTestId("message-list")
}

function activityBubble(): HTMLElement | null {
  return screen.queryByTestId("agent-activity")
}

describe("DesignTab agent activity (D-06)", () => {
  beforeEach(() => {
    for (const fn of Object.values(designApi)) fn.mockReset()
    for (const fn of Object.values(clarificationApi)) fn.mockReset()
    for (const fn of Object.values(conversationApi)) fn.mockReset()
    versionEvents.handlers = null
    designApi.getDesignGraph.mockResolvedValue(designGraphFixture)
    designApi.getDesignOverview.mockResolvedValue(designOverviewFixture)
    designApi.listDesignConversations.mockResolvedValue([
      {
        id: CONVERSATION_ID,
        title: "用例设计 2026-10-31 12:00",
        kind: "design",
        status: "active",
        message_count: 0,
        last_message_at: TIMESTAMP,
        created_at: TIMESTAMP,
      },
    ])
    designApi.getDesignCase.mockResolvedValue(designCaseDetailFixture)
    conversationApi.listPublicModels.mockResolvedValue([])
    clarificationApi.listMessages.mockResolvedValue([])
    clarificationApi.getTeamRoom.mockResolvedValue({ team: "design", digest: null, posts: [] })
  })

  afterEach(cleanup)

  it("shows the activity of the running design run", async () => {
    await renderWorkspace()
    expect(activityBubble()).not.toBeInTheDocument()

    deliver((handlers) =>
      handlers?.onAgentActivity?.({
        run_id: RUN_ID,
        conversation_id: CONVERSATION_ID,
        status: "thinking",
      }),
    )

    expect(await screen.findByTestId("agent-activity")).toHaveTextContent("AI 正在处理…")
  })

  it("clears the activity when the run finishes", async () => {
    await renderWorkspace()

    deliver((handlers) =>
      handlers?.onAgentActivity?.({
        run_id: RUN_ID,
        conversation_id: CONVERSATION_ID,
        status: "tool",
        activity: "正在生成用例",
      }),
    )
    expect(activityBubble()).toHaveTextContent("正在生成用例")

    deliver((handlers) =>
      handlers?.onAgentRunChanged?.(versionEvent("agent.run.finished", { run_id: RUN_ID })),
    )

    await waitFor(() => expect(activityBubble()).not.toBeInTheDocument())
  })

  it("clears the activity when the run fails", async () => {
    await renderWorkspace()

    deliver((handlers) =>
      handlers?.onAgentActivity?.({
        run_id: RUN_ID,
        conversation_id: CONVERSATION_ID,
        status: "thinking",
      }),
    )
    expect(activityBubble()).toBeInTheDocument()

    deliver((handlers) =>
      handlers?.onAgentRunChanged?.(versionEvent("agent.run.failed", { run_id: RUN_ID })),
    )

    await waitFor(() => expect(activityBubble()).not.toBeInTheDocument())
  })

  it("keeps an overlapping run active when another run finishes", async () => {
    await renderWorkspace()

    deliver((handlers) =>
      handlers?.onAgentActivity?.({
        run_id: "9101",
        conversation_id: CONVERSATION_ID,
        status: "thinking",
        activity: "正在评审用例",
      }),
    )
    deliver((handlers) =>
      handlers?.onAgentActivity?.({
        run_id: "9102",
        conversation_id: CONVERSATION_ID,
        status: "tool",
        activity: "正在生成用例",
      }),
    )
    expect(activityBubble()).toHaveTextContent("正在生成用例")

    deliver((handlers) =>
      handlers?.onAgentRunChanged?.(versionEvent("agent.run.finished", { run_id: "9102" })),
    )

    await waitFor(() => expect(activityBubble()).toHaveTextContent("正在评审用例"))
  })

  it("does not fall back to the placeholder for an unknown status", async () => {
    await renderWorkspace()

    deliver((handlers) =>
      handlers?.onAgentActivity?.({
        run_id: RUN_ID,
        conversation_id: CONVERSATION_ID,
        status: "telemetry-v2",
      }),
    )

    expect(activityBubble()).not.toBeInTheDocument()
  })

  it("ignores an activity of another conversation", async () => {
    await renderWorkspace()

    deliver((handlers) =>
      handlers?.onAgentActivity?.({
        run_id: RUN_ID,
        conversation_id: "9999",
        status: "thinking",
      }),
    )

    expect(activityBubble()).not.toBeInTheDocument()
  })
})
