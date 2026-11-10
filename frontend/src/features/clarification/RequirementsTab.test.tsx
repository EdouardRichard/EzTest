import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
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
  getRequirementExtraction: vi.fn(),
  startRequirementExtraction: vi.fn(),
  pauseRequirementExtraction: vi.fn(),
  resumeRequirementExtraction: vi.fn(),
  retryRequirementExtraction: vi.fn(),
  getTeamRoom: vi.fn(),
  uploadContent: vi.fn(),
  getAttachmentPendingDetails: vi.fn(),
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
import { requirementFixture, requirementSecondFixture } from "./test-fixtures"

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
    navigation.push.mockReset()
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
    api.getRequirementExtraction.mockResolvedValue(null)
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

  it("shows Chinese gate guidance and switches to drafts when start-design is rejected by unconfirmed requirements", async () => {
    api.listRequirements.mockResolvedValue([requirementFixture, requirementSecondFixture])
    api.confirmStageRequirements.mockRejectedValue(
      new ApiError({
        status: 409,
        code: "stage_gate_failed",
        message:
          "stage gate requirement_confirmation is closed: requirements_unconfirmed (confirm every current requirement before starting design)",
        requestId: "req_09e1192ec1db",
        details: {
          gate: "requirement_confirmation",
          reason: "requirements_unconfirmed",
          action: "confirm every current requirement before starting design",
          next: "designing",
        },
      }),
    )
    const onStageChanged = vi.fn()

    render(
      <RequirementsTab
        versionId="2001"
        stage="requirement_review"
        onStageChanged={onStageChanged}
      />,
    )

    expect(await screen.findByTestId("design-cta")).toHaveTextContent("1 条需求待设计")
    fireEvent.click(screen.getByRole("button", { name: "开始设计" }))

    const alert = await screen.findByRole("alert")
    expect(alert).toHaveTextContent("需先确认全部需求，才能开始设计")
    expect(alert).toHaveTextContent("批量确认")
    expect(alert).not.toHaveTextContent("requirements_unconfirmed")
    expect(alert).not.toHaveTextContent("confirm every current requirement")
    // The rejected gate must not advance the stage nor navigate anywhere.
    expect(onStageChanged).not.toHaveBeenCalled()
    expect(navigation.push).not.toHaveBeenCalled()
    expect(navigation.replace).not.toHaveBeenCalled()

    fireEvent.click(within(alert).getByRole("button", { name: "查看未确认需求" }))

    // The alert must not stay over the draft list it just switched to.
    expect(screen.queryByRole("alert")).not.toBeInTheDocument()
    expect(screen.getByTestId("req-filter-draft")).toHaveAttribute("aria-pressed", "true")
    expect(screen.getByTestId("requirement-item-6001")).toBeInTheDocument()
    expect(screen.queryByTestId("requirement-item-6002")).not.toBeInTheDocument()
  })

  it("shows the open-gap gate reason when start-design is rejected by unanswered questions", async () => {
    api.listRequirements.mockResolvedValue([requirementFixture, requirementSecondFixture])
    api.confirmStageRequirements.mockRejectedValue(
      new ApiError({
        status: 409,
        code: "stage_gate_failed",
        message:
          "stage gate requirement_gap is closed: open_gap (resolve or explicitly accept every open requirement gap)",
        requestId: "req_5a2c0e5b2898",
        details: {
          action: "resolve or explicitly accept every open requirement gap",
          gate: "requirement_gap",
          next: "designing",
          reason: "open_gap",
        },
      }),
    )

    render(<RequirementsTab versionId="2001" stage="requirement_review" />)

    expect(await screen.findByTestId("design-cta")).toHaveTextContent("1 条需求待设计")
    fireEvent.click(screen.getByRole("button", { name: "开始设计" }))

    const alert = await screen.findByRole("alert")
    expect(alert).toHaveTextContent("还有待回答的问题或未接受的开放缺口，无法开始设计")
    expect(alert).toHaveTextContent("需求与澄清")
    expect(alert).not.toHaveTextContent("open_gap")
    expect(alert).not.toHaveTextContent("请按页面提示完成前置条件后重试")
    expect(alert).not.toHaveTextContent("resolve or explicitly accept")
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
