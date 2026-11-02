import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

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

vi.mock("./useVersionEvents", () => ({
  useVersionEvents: () => "live",
}))

import { RequirementsTab } from "./RequirementsTab"
import { requirementSecondFixture } from "./test-fixtures"

describe("RequirementsTab shell", () => {
  beforeEach(() => {
    for (const fn of Object.values(api)) fn.mockReset()
    versionsApi.getVersion.mockReset()
    navigation.replace.mockReset()
    api.listConversations.mockResolvedValue([])
    api.listRequirements.mockResolvedValue([])
    api.listMessages.mockResolvedValue([])
    versionsApi.getVersion.mockResolvedValue({ id: "2001", bindings: [] })
  })

  afterEach(cleanup)

  it("shows a loading state before the clarification workspace resolves", async () => {
    let resolveConversations: (value: unknown) => void = () => {}
    api.listConversations.mockReturnValue(
      new Promise((resolve) => {
        resolveConversations = resolve
      }),
    )

    render(<RequirementsTab versionId="2001" />)

    expect(screen.getByText("正在加载澄清工作区…")).toBeInTheDocument()
    expect(api.listConversations).toHaveBeenCalledWith("2001")
    expect(api.listRequirements).toHaveBeenCalledWith("2001")

    resolveConversations([])
    expect(await screen.findByTestId("requirements-tab")).toBeInTheDocument()
  })

  it("renders the empty clarification states when the version has no data", async () => {
    render(<RequirementsTab versionId="2001" />)

    expect(await screen.findByTestId("requirements-tab")).toBeInTheDocument()
    expect(screen.getByText("暂无澄清对话")).toBeInTheDocument()
    expect(screen.getByText("暂无需求")).toBeInTheDocument()
  })

  it("shows a retryable error state when the workspace fails to load", async () => {
    api.listConversations
      .mockRejectedValueOnce(
        new ApiError({
          status: 404,
          code: "not_found",
          message: "版本不存在",
          requestId: "req_clarification",
        }),
      )
      .mockResolvedValueOnce([])

    render(<RequirementsTab versionId="2001" />)

    expect(await screen.findByText("版本不存在")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))

    expect(await screen.findByTestId("requirements-tab")).toBeInTheDocument()
    expect(api.listConversations).toHaveBeenCalledTimes(2)
  })

  it("renders the workspace when the version has conversations and requirements", async () => {
    api.listConversations.mockResolvedValue([
      {
        id: "4001",
        version_id: "2001",
        kind: "clarification",
        title: "需求澄清 2026-10-31 10:00",
        status: "active",
        message_count: 2,
        last_message_at: "2026-10-31T10:00:00Z",
        created_by: "9001",
        created_at: "2026-10-31T10:00:00Z",
        updated_at: "2026-10-31T10:00:00Z",
      },
    ])
    api.listRequirements.mockResolvedValue([])

    render(<RequirementsTab versionId="2001" />)

    expect(await screen.findByText("需求澄清 2026-10-31 10:00")).toBeInTheDocument()
    expect(screen.queryByText("暂无澄清对话")).not.toBeInTheDocument()
  })

  it("links the pending design CTA to the version design tab", async () => {
    api.listRequirements.mockResolvedValue([requirementSecondFixture])

    render(<RequirementsTab versionId="2001" />)

    expect(await screen.findByTestId("design-cta")).toHaveTextContent("1 条需求待设计")
    expect(screen.getByRole("link", { name: "开始设计" })).toHaveAttribute(
      "href",
      "/versions/2001?tab=design",
    )
  })

  it("prefers the server pending_design_count projection over the local clarified count", async () => {
    versionsApi.getVersion.mockResolvedValue({ id: "2001", bindings: [], pending_design_count: 4 })
    api.listRequirements.mockResolvedValue([])

    render(<RequirementsTab versionId="2001" />)

    expect(await screen.findByTestId("design-cta")).toHaveTextContent("4 条需求待设计")
  })
})
