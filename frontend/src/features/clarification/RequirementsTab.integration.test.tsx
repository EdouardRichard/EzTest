import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { versionFixture } from "@/features/versions/test-fixtures"
import { ApiError } from "@/lib/api-client"

import {
  confirmedRequirementDetailFixture,
  conversationFixture,
  conversationSecondFixture,
  requirementCardMessageFixture,
  requirementClosedFixture,
  requirementDetailFixture,
  requirementFixture,
  requirementSecondFixture,
  teamRoomFixture,
} from "./test-fixtures"

const api = vi.hoisted(() => ({
  listConversations: vi.fn(),
  createConversation: vi.fn(),
  getConversation: vi.fn(),
  listMessages: vi.fn(),
  postMessage: vi.fn(),
  listRequirements: vi.fn(),
  createRequirement: vi.fn(),
  getRequirement: vi.fn(),
  updateRequirement: vi.fn(),
  confirmRequirement: vi.fn(),
  confirmRequirements: vi.fn(),
  rollbackRequirement: vi.fn(),
  listRequirementRelations: vi.fn(),
  getTeamRoom: vi.fn(),
  uploadContent: vi.fn(),
}))
vi.mock("./api", () => api)

const versionsApi = vi.hoisted(() => ({ getVersion: vi.fn() }))
vi.mock("@/features/versions/api", () => versionsApi)

const navigation = vi.hoisted(() => ({
  push: vi.fn(),
  replace: vi.fn(),
  refresh: vi.fn(),
  back: vi.fn(),
  forward: vi.fn(),
  prefetch: vi.fn(),
}))
vi.mock("next/navigation", () => ({
  useRouter: () => navigation,
}))

type CapturedHandlers = {
  onConversationMessage?: (message: typeof requirementCardMessageFixture) => void
  onAgentActivity?: (payload: Record<string, unknown>) => void
  onRequirementChanged?: (event: unknown) => void
  onTeamRoomChanged?: (event: unknown) => void
  onVersionChanged?: (event: unknown) => void
  onStatus?: (status: string) => void
}

const sse = vi.hoisted(() => ({
  versionId: "",
  handlers: {} as CapturedHandlers,
}))
vi.mock("./useVersionEvents", () => ({
  useVersionEvents: (versionId: string, handlers: CapturedHandlers) => {
    sse.versionId = versionId
    sse.handlers = handlers
    return "live"
  },
}))

import { RequirementsTab } from "./RequirementsTab"

const secondDetail = {
  ...confirmedRequirementDetailFixture,
  id: "6002",
  current_version_id: "6102",
  revision: 1,
  title: "大额订单人工审核",
  priority: "P0" as const,
  status: "clarified" as const,
  targets: { version_suts: ["1001"], pages: ["1301"], apis: [] },
}

describe("RequirementsTab integration", () => {
  beforeEach(() => {
    for (const fn of Object.values(api)) fn.mockReset()
    versionsApi.getVersion.mockReset()
    navigation.replace.mockReset()
    sse.versionId = ""
    sse.handlers = {}

    versionsApi.getVersion.mockResolvedValue(versionFixture)
    api.listConversations.mockResolvedValue([conversationFixture, conversationSecondFixture])
    api.listRequirements.mockResolvedValue([
      requirementFixture,
      requirementSecondFixture,
      requirementClosedFixture,
    ])
    api.listMessages.mockResolvedValue([])
    api.getRequirement.mockImplementation((_versionId: string, requirementId: string) =>
      Promise.resolve(requirementId === "6002" ? secondDetail : requirementDetailFixture),
    )
    api.getTeamRoom.mockResolvedValue(teamRoomFixture)
    api.confirmRequirements.mockResolvedValue({
      batch_id: "batch_1",
      confirmed: ["6001"],
      skipped: ["6002"],
    })
  })

  afterEach(cleanup)

  it("assembles chat, sidebar, design CTA, team panel and loads workspace data", async () => {
    render(<RequirementsTab versionId="2001" />)

    expect(await screen.findByTestId("requirements-tab")).toBeInTheDocument()
    expect(api.listConversations).toHaveBeenCalledWith("2001")
    expect(api.listRequirements).toHaveBeenCalledWith("2001")
    expect(versionsApi.getVersion).toHaveBeenCalledWith("2001")

    expect(screen.getByTestId("chat-panel")).toBeInTheDocument()
    expect(screen.getByTestId("requirement-sidebar")).toBeInTheDocument()
    expect(screen.getByTestId("design-cta")).toHaveTextContent("1 条需求待设计")
    expect(screen.getByTestId("team-room-panel")).toBeInTheDocument()
    expect(sse.versionId).toBe("2001")

    // The first conversation is selected by default; requirements keep their bound target names.
    expect(screen.getByText(conversationFixture.title)).toBeInTheDocument()
    expect(api.listMessages).toHaveBeenCalledWith("2001", "4001")
    expect(screen.getByTestId("requirement-item-6001")).toHaveTextContent("支付网关")
  })

  it("restores the conversation and requirement deep links from the URL", async () => {
    render(
      <RequirementsTab versionId="2001" initialConversationId="4002" initialRequirementId="6002" />,
    )

    expect(await screen.findByTestId("requirements-tab")).toBeInTheDocument()
    expect(api.listMessages).toHaveBeenCalledWith("2001", "4002")
    expect(api.getRequirement).toHaveBeenCalledWith("2001", "6002")

    const item = screen.getByTestId("requirement-item-6002")
    expect(item).toHaveAttribute("data-active", "true")
    const editor = await screen.findByTestId("requirement-editor")
    expect(within(editor).getByText("大额订单人工审核")).toBeInTheDocument()
    expect(navigation.replace).not.toHaveBeenCalled()
  })

  it("writes the requirement selection into the URL", async () => {
    render(<RequirementsTab versionId="2001" />)
    await screen.findByTestId("requirements-tab")

    fireEvent.click(screen.getByRole("button", { name: "退款到账时效" }))

    expect(navigation.replace).toHaveBeenCalledWith(
      "/versions/2001?tab=requirements&conversation=4001&requirement=6001",
    )
    expect(await screen.findByTestId("requirement-editor")).toHaveTextContent("退款到账时效")
  })

  it("switches conversations from the history dropdown and writes the URL", async () => {
    render(<RequirementsTab versionId="2001" />)
    await screen.findByTestId("requirements-tab")

    fireEvent.click(screen.getByRole("button", { name: "历史对话" }))
    fireEvent.click(screen.getByRole("button", { name: /需求澄清 2026-10-30 16:00/ }))

    expect(navigation.replace).toHaveBeenCalledWith(
      "/versions/2001?tab=requirements&conversation=4002",
    )
    await waitFor(() => expect(api.listMessages).toHaveBeenCalledWith("2001", "4002"))
    expect(screen.getByText(conversationSecondFixture.title)).toBeInTheDocument()
  })

  it("creates a new conversation and opens it", async () => {
    const created = { ...conversationFixture, id: "4003", title: "需求澄清 2026-10-31 11:00" }
    api.createConversation.mockResolvedValue(created)
    render(<RequirementsTab versionId="2001" />)
    await screen.findByTestId("requirements-tab")

    fireEvent.click(screen.getByRole("button", { name: /新增对话/ }))

    await waitFor(() =>
      expect(api.createConversation).toHaveBeenCalledWith("2001", { kind: "clarification" }),
    )
    expect(navigation.replace).toHaveBeenCalledWith(
      "/versions/2001?tab=requirements&conversation=4003",
    )
    await waitFor(() => expect(api.listMessages).toHaveBeenCalledWith("2001", "4003"))
  })

  it("coordinates SSE messages, activity, requirement refreshes and team-room refreshes", async () => {
    render(<RequirementsTab versionId="2001" />)
    await screen.findByTestId("requirements-tab")
    expect(api.listRequirements).toHaveBeenCalledTimes(1)

    act(() => {
      sse.handlers.onConversationMessage?.(requirementCardMessageFixture)
    })
    expect(await screen.findByTestId("requirement-card-6001")).toBeInTheDocument()

    act(() => {
      sse.handlers.onAgentActivity?.({ activity: "正在分析需求关系…", status: "running" })
    })
    expect(await screen.findByTestId("agent-activity")).toHaveTextContent("正在分析需求关系…")

    act(() => {
      sse.handlers.onRequirementChanged?.({
        seq: 8,
        type: "requirement.created",
        payload: { requirement_id: "6004" },
      })
    })
    await waitFor(() => expect(api.listRequirements).toHaveBeenCalledTimes(2))

    fireEvent.click(screen.getByTestId("team-room-toggle"))
    await waitFor(() => expect(api.getTeamRoom).toHaveBeenCalledTimes(1))
    act(() => {
      sse.handlers.onTeamRoomChanged?.({ seq: 9, type: "team_room.post", payload: {} })
    })
    await waitFor(() => expect(api.getTeamRoom).toHaveBeenCalledTimes(2))
  })

  it("batch confirms selected requirements, skips confirmed ones and refreshes", async () => {
    render(<RequirementsTab versionId="2001" />)
    await screen.findByTestId("requirements-tab")

    fireEvent.click(screen.getByRole("checkbox", { name: "选择退款到账时效" }))
    fireEvent.click(screen.getByRole("checkbox", { name: "选择大额订单人工审核" }))

    expect(screen.getByTestId("bulk-confirm-bar")).toHaveTextContent("已选 2 条")
    fireEvent.click(screen.getByRole("button", { name: "批量确认已澄清" }))

    await waitFor(() =>
      expect(api.confirmRequirements).toHaveBeenCalledWith("2001", ["6001", "6002"]),
    )
    expect(await screen.findByText("已确认 1 条，跳过 1 条已确认需求")).toBeInTheDocument()
    expect(screen.queryByTestId("bulk-confirm-bar")).not.toBeInTheDocument()
    expect(api.listRequirements).toHaveBeenCalledTimes(2)
  })

  it("keeps a two-column desktop layout with a single column on mobile", async () => {
    render(<RequirementsTab versionId="2001" />)
    await screen.findByTestId("requirements-tab")

    const grid = screen.getByTestId("workspace-grid")
    expect(grid.className).toContain("grid")
    expect(grid.className).toContain("lg:grid-cols-[minmax(0,1fr)_400px]")
    expect(grid.className).not.toContain("md:grid-cols-2")
  })

  it("renders the empty workspace without data or design CTA", async () => {
    api.listConversations.mockResolvedValue([])
    api.listRequirements.mockResolvedValue([])

    render(<RequirementsTab versionId="2001" />)

    expect(await screen.findByTestId("requirements-tab")).toBeInTheDocument()
    expect(screen.getByText("暂无澄清对话")).toBeInTheDocument()
    expect(screen.getByText("暂无需求")).toBeInTheDocument()
    expect(screen.queryByTestId("design-cta")).not.toBeInTheDocument()
    expect(api.listMessages).not.toHaveBeenCalled()
  })

  it("shows a retryable workspace error and recovers", async () => {
    api.listRequirements
      .mockRejectedValueOnce(
        new ApiError({ status: 500, code: "internal", message: "澄清工作区暂不可用" }),
      )
      .mockResolvedValueOnce([requirementFixture])
      .mockResolvedValueOnce([requirementFixture])
    render(<RequirementsTab versionId="2001" />)

    expect(await screen.findByText("澄清工作区暂不可用")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))

    expect(await screen.findByTestId("requirements-tab")).toBeInTheDocument()
  })
})
