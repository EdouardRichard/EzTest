import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
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
  confirmStageRequirements: vi.fn(),
  rollbackRequirement: vi.fn(),
  listRequirementRelations: vi.fn(),
  getTeamRoom: vi.fn(),
  uploadContent: vi.fn(),
}))
vi.mock("./api", () => api)

// The header model selector talks to the login-user model catalog; mock it so
// the workspace never issues a real request and the switch can be asserted.
const conversationApi = vi.hoisted(() => ({
  listPublicModels: vi.fn(),
  updateConversationModel: vi.fn(),
}))
vi.mock("@/features/conversation/api", () => conversationApi)

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

const publicModels = [
  {
    id: "8102",
    config_id: "7101",
    config_name: "Primary",
    model: "vision-plus",
    display_name: "Vision Plus",
    context_window: 128000,
    capabilities: {
      supports_tools: true,
      supports_vision: true,
      supports_reasoning: true,
      supports_prompt_cache: true,
      context_window: 128000,
    },
    default: false,
    deprecated: false,
  },
  {
    id: "8101",
    config_id: "7101",
    config_name: "Primary",
    model: "gpt-4o",
    display_name: "GPT-4o",
    context_window: 128000,
    capabilities: {
      supports_tools: true,
      supports_vision: true,
      supports_reasoning: true,
      supports_prompt_cache: true,
      context_window: 128000,
    },
    // Deliberately not models[0]: the selector must use the explicit default.
    default: true,
    deprecated: false,
  },
]

const conversationFixtures = [
  {
    id: "4001",
    kind: "clarification",
    title: "退款规则澄清",
    status: "active",
    message_count: 2,
    last_message_at: "2026-10-31T10:00:00Z",
    created_at: "2026-10-31T10:00:00Z",
  },
  {
    id: "4002",
    kind: "clarification",
    title: "退款时效澄清",
    status: "active",
    message_count: 1,
    last_message_at: "2026-10-30T16:00:00Z",
    created_at: "2026-10-30T16:00:00Z",
  },
]

describe("RequirementsTab shell", () => {
  beforeEach(() => {
    for (const fn of Object.values(api)) fn.mockReset()
    versionsApi.getVersion.mockReset()
    navigation.replace.mockReset()
    conversationApi.listPublicModels.mockReset()
    conversationApi.updateConversationModel.mockReset()
    conversationApi.listPublicModels.mockResolvedValue(publicModels)
    conversationApi.updateConversationModel.mockResolvedValue({
      id: "4001",
      model_override: "vision-plus",
      effective_model: "vision-plus",
    })
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

  it("applies the requirement gate from the pending design CTA", async () => {
    api.listRequirements.mockResolvedValue([requirementSecondFixture])
    api.confirmStageRequirements.mockResolvedValue({
      version_id: "2001",
      stage: "designing",
      stage_entered_at: "2026-10-31T10:00:00Z",
      blocker: null,
    })

    render(<RequirementsTab versionId="2001" />)

    expect(await screen.findByTestId("design-cta")).toHaveTextContent("1 条需求待设计")
    fireEvent.click(screen.getByRole("button", { name: "开始设计" }))

    await waitFor(() => expect(api.confirmStageRequirements).toHaveBeenCalledWith("2001", ["6002"]))
    await waitFor(() =>
      expect(navigation.replace).toHaveBeenCalledWith("/versions/2001?tab=design"),
    )
  })

  it("recovers a stuck clarifying version before applying the requirement gate", async () => {
    api.listRequirements.mockResolvedValue([requirementSecondFixture])
    api.confirmRequirements.mockResolvedValue({
      batch_id: "batch_1",
      confirmed: [],
      skipped: ["6002"],
    })
    api.confirmStageRequirements.mockResolvedValue({
      version_id: "2001",
      stage: "designing",
      stage_entered_at: "2026-10-31T10:00:00Z",
      blocker: null,
    })

    render(<RequirementsTab versionId="2001" stage="clarifying" />)

    expect(await screen.findByTestId("design-cta")).toHaveTextContent("1 条需求待设计")
    fireEvent.click(screen.getByRole("button", { name: "开始设计" }))

    await waitFor(() => expect(api.confirmRequirements).toHaveBeenCalledWith("2001", ["6002"]))
    await waitFor(() => expect(api.confirmStageRequirements).toHaveBeenCalledWith("2001", ["6002"]))
  })

  it("prefers the server pending_design_count projection over the local clarified count", async () => {
    versionsApi.getVersion.mockResolvedValue({ id: "2001", bindings: [], pending_design_count: 4 })
    api.listRequirements.mockResolvedValue([])

    render(<RequirementsTab versionId="2001" />)

    expect(await screen.findByTestId("design-cta")).toHaveTextContent("4 条需求待设计")
  })

  it("keeps a switched model when leaving and returning to the conversation", async () => {
    api.listConversations.mockResolvedValue(conversationFixtures)

    render(<RequirementsTab versionId="2001" />)

    const select = await screen.findByTestId("model-selector")
    expect(select).toHaveValue("gpt-4o")
    fireEvent.change(select, { target: { value: "vision-plus" } })
    await waitFor(() =>
      expect(conversationApi.updateConversationModel).toHaveBeenCalledWith(
        "2001",
        "4001",
        "vision-plus",
      ),
    )
    await waitFor(() => expect(select).toHaveValue("vision-plus"))

    // Leave the conversation and return: the keyed selector remounts, so it can
    // only keep the switch if the workspace state now carries the new override.
    fireEvent.click(screen.getByRole("button", { name: "历史对话" }))
    fireEvent.click(screen.getByRole("button", { name: /退款时效澄清/ }))
    await waitFor(() => expect(screen.getByTestId("model-selector")).toHaveValue("gpt-4o"))

    fireEvent.click(screen.getByRole("button", { name: "历史对话" }))
    fireEvent.click(screen.getByRole("button", { name: /退款规则澄清/ }))
    await waitFor(() => expect(screen.getByTestId("model-selector")).toHaveValue("vision-plus"))
  })
})
