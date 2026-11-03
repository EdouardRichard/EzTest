import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

const api = vi.hoisted(() => ({
  getDesignGraph: vi.fn(),
  getDesignOverview: vi.fn(),
  listDesignConversations: vi.fn(),
  createDesignConversation: vi.fn(),
  getDesignCase: vi.fn(),
  confirmCases: vi.fn(),
  confirmStageCases: vi.fn(),
  createExport: vi.fn(),
  downloadExport: vi.fn(),
  listExports: vi.fn(),
}))

vi.mock("./api", () => api)

const navigation = vi.hoisted(() => ({ replace: vi.fn(), push: vi.fn() }))
vi.mock("next/navigation", () => ({
  useRouter: () => navigation,
  usePathname: () => "/versions/2001",
}))

vi.mock("@/features/clarification/useVersionEvents", () => ({
  useVersionEvents: () => "live",
}))

const clarificationApi = vi.hoisted(() => ({
  listMessages: vi.fn(),
  postMessage: vi.fn(),
  getTeamRoom: vi.fn(),
  uploadContent: vi.fn(),
  rollbackRequirement: vi.fn(),
}))
vi.mock("@/features/clarification/api", () => clarificationApi)

// The design chat header mounts the login-user model selector; mock its API so
// the switch and the collapse/expand remount can be asserted without a request.
const conversationApi = vi.hoisted(() => ({
  listPublicModels: vi.fn(),
  updateConversationModel: vi.fn(),
}))
vi.mock("@/features/conversation/api", () => conversationApi)

const versionsApi = vi.hoisted(() => ({ updateVersion: vi.fn() }))
vi.mock("@/features/versions/api", () => versionsApi)

import type { ConversationSummary } from "@/features/clarification/types"
import { versionFixture } from "@/features/versions/test-fixtures"
import { DesignTab } from "./DesignTab"
import { designCaseDetailFixture, designGraphFixture, designOverviewFixture } from "./test-fixtures"
import type { DesignGraph } from "./types"

const TIMESTAMP = "2026-10-31T12:00:00Z"

const designConversation: ConversationSummary = {
  id: "4009",
  title: "用例设计 2026-10-31 12:00",
  kind: "design",
  status: "active",
  message_count: 2,
  last_message_at: TIMESTAMP,
  created_at: TIMESTAMP,
}

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

function mockWorkspace(graph: DesignGraph = designGraphFixture) {
  api.getDesignGraph.mockResolvedValue(graph)
  api.getDesignOverview.mockResolvedValue(designOverviewFixture)
  api.listDesignConversations.mockResolvedValue([designConversation])
  api.getDesignCase.mockResolvedValue(designCaseDetailFixture)
  api.createDesignConversation.mockResolvedValue({
    ...designConversation,
    id: "4010",
    title: "用例设计 2026-10-31 13:00",
  })
  api.confirmCases.mockResolvedValue({
    scope: "requirement",
    target_id: "6001",
    batch_id: "batch_1",
    confirmed: [{ case_id: "7001", case_version_id: "7002" }],
    skipped: [],
    confirmed_at: TIMESTAMP,
  })
  api.confirmStageCases.mockResolvedValue({
    version_id: "2001",
    stage: "executing",
    stage_entered_at: TIMESTAMP,
    blocker: null,
  })
}

describe("DesignTab", () => {
  beforeEach(() => {
    for (const fn of Object.values(api)) fn.mockReset()
    for (const fn of Object.values(clarificationApi)) fn.mockReset()
    versionsApi.updateVersion.mockReset()
    navigation.replace.mockReset()
    conversationApi.listPublicModels.mockReset()
    conversationApi.updateConversationModel.mockReset()
    conversationApi.listPublicModels.mockResolvedValue(publicModels)
    conversationApi.updateConversationModel.mockResolvedValue({
      id: "4009",
      model_override: "vision-plus",
      effective_model: "vision-plus",
    })
    clarificationApi.listMessages.mockResolvedValue([])
    clarificationApi.getTeamRoom.mockResolvedValue({
      team: "design",
      digest: null,
      posts: [],
    })
    versionsApi.updateVersion.mockResolvedValue({
      ...versionFixture,
      auto_design_new_requirements: true,
    })
    mockWorkspace()
  })

  afterEach(cleanup)

  it("shows a loading state while the workspace loads", () => {
    const pending = new Promise<never>(() => {})
    api.getDesignGraph.mockReturnValue(pending)
    api.getDesignOverview.mockReturnValue(pending)
    api.listDesignConversations.mockReturnValue(pending)

    render(<DesignTab versionId="2001" />)

    expect(screen.getByText("正在加载设计图…")).toBeInTheDocument()
  })

  it("renders a retryable error state when the graph fails", async () => {
    api.getDesignGraph
      .mockRejectedValueOnce(
        new ApiError({
          status: 404,
          code: "not_found",
          message: "设计图不存在",
          requestId: "req_graph",
        }),
      )
      .mockResolvedValueOnce(designGraphFixture)

    render(<DesignTab versionId="2001" />)

    expect(await screen.findByText("设计图不存在")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(await screen.findByTestId("design-mindmap")).toBeInTheDocument()
  })

  it("renders the empty state when there is no clarified requirement", async () => {
    mockWorkspace({
      ...designGraphFixture,
      pending_design_count: 0,
      requirements: [],
      stats: { requirements: 0, scenarios: 0, cases: 0, draft: 0, confirmed: 0, stale: 0 },
    })

    render(<DesignTab versionId="2001" />)

    expect(
      await screen.findByText("暂无已澄清需求，请先在需求与澄清中确认需求"),
    ).toBeInTheDocument()
    expect(screen.getByTestId("design-new-conversation")).toBeInTheDocument()
  })

  it("renders the toolbar, stats and mindmap", async () => {
    render(<DesignTab versionId="2001" />)

    expect(
      await screen.findByRole("heading", { name: "用例脑图 · 设计轮次 #1" }),
    ).toBeInTheDocument()
    expect(screen.getByTestId("design-stats")).toHaveTextContent("需求 2 · 场景 2 · 用例 3")
    expect(screen.getByTestId("export-cases")).toBeInTheDocument()
    expect(screen.getByTestId("auto-design-switch")).toBeInTheDocument()
    expect(screen.getByTestId("design-mindmap")).toBeInTheDocument()
    expect(await screen.findByTestId("design-chat")).toBeInTheDocument()
    expect(screen.getByTestId("case-detail")).toBeInTheDocument()
  })

  it("selects a case, updates the URL and loads the detail panel", async () => {
    render(<DesignTab versionId="2001" />)

    await screen.findByTestId("case-select-7001")
    fireEvent.click(screen.getByTestId("case-select-7001"))

    expect(navigation.replace).toHaveBeenCalledWith(
      "/versions/2001?tab=design&conversation=4009&case=7001",
    )
    expect(await screen.findByText("退款 T+1 到账")).toBeInTheDocument()
    expect(api.getDesignCase).toHaveBeenCalledWith("2001", "7001", undefined)
  })

  it("honours the case deep link passed by the page", async () => {
    render(<DesignTab versionId="2001" initialCaseId="7001" />)

    expect(await screen.findByText("退款 T+1 到账")).toBeInTheDocument()
    expect(api.getDesignCase).toHaveBeenCalledWith("2001", "7001", undefined)
  })

  it("creates a new design conversation from the toolbar", async () => {
    render(<DesignTab versionId="2001" />)
    await screen.findByTestId("design-mindmap")

    fireEvent.click(screen.getByTestId("design-new-conversation"))

    expect(api.createDesignConversation).toHaveBeenCalledWith("2001")
    expect(await screen.findByText("用例设计 2026-10-31 13:00")).toBeInTheDocument()
    await waitFor(() =>
      expect(navigation.replace).toHaveBeenCalledWith(
        "/versions/2001?tab=design&conversation=4010",
      ),
    )
  })

  it("confirms a whole requirement and refreshes the graph", async () => {
    render(<DesignTab versionId="2001" />)

    fireEvent.click(await screen.findByTestId("btn-confirm-requirement-6001"))

    expect(api.confirmCases).toHaveBeenCalledWith("2001", {
      scope: "requirement",
      target_id: "6001",
    })
    expect(await screen.findByTestId("design-confirm-notice")).toHaveTextContent("已确认 1 个用例")
    expect(api.getDesignGraph).toHaveBeenCalledTimes(2)
  })

  it("shows a 409 confirmation error inline", async () => {
    api.confirmCases.mockRejectedValue(
      new ApiError({
        status: 409,
        code: "state_conflict",
        message: "该需求下没有可确认的 draft 用例",
        requestId: "req_confirm",
      }),
    )
    render(<DesignTab versionId="2001" />)

    fireEvent.click(await screen.findByTestId("btn-confirm-requirement-6001"))

    expect(await screen.findByText("该需求下没有可确认的 draft 用例")).toBeInTheDocument()
  })

  it("opens the export format dialog from the toolbar", async () => {
    render(<DesignTab versionId="2001" />)
    await screen.findByTestId("export-cases")

    fireEvent.click(screen.getByTestId("export-cases"))

    expect(await screen.findByRole("dialog", { name: "导出用例" })).toBeInTheDocument()
    expect(screen.getByTestId("export-format-markdown")).toBeInTheDocument()
  })

  it("toggles the auto-design switch through the version API", async () => {
    render(<DesignTab versionId="2001" />)
    const toggle = await screen.findByTestId("auto-design-switch")

    fireEvent.click(toggle)

    await waitFor(() =>
      expect(versionsApi.updateVersion).toHaveBeenCalledWith("2001", {
        auto_design_new_requirements: true,
      }),
    )
    await waitFor(() => expect(toggle).toHaveAttribute("aria-checked", "true"))
  })

  it("creates a conversation from the design chat header", async () => {
    render(<DesignTab versionId="2001" />)
    await screen.findByTestId("design-chat")

    fireEvent.click(screen.getByTestId("design-chat-new-conversation"))

    expect(api.createDesignConversation).toHaveBeenCalledWith("2001")
  })

  it("keeps a switched model after collapsing and expanding the chat", async () => {
    render(<DesignTab versionId="2001" />)

    const select = await screen.findByTestId("model-selector")
    expect(select).toHaveValue("gpt-4o")
    fireEvent.change(select, { target: { value: "vision-plus" } })
    await waitFor(() =>
      expect(conversationApi.updateConversationModel).toHaveBeenCalledWith(
        "2001",
        "4009",
        "vision-plus",
      ),
    )
    await waitFor(() => expect(select).toHaveValue("vision-plus"))

    // Collapsing the chat unmounts the selector; expanding remounts it from the
    // workspace conversation state, which must already carry the new override.
    fireEvent.click(screen.getByTestId("design-chat-collapse-toggle"))
    expect(screen.queryByTestId("model-selector")).not.toBeInTheDocument()

    fireEvent.click(screen.getByTestId("design-chat-collapse-toggle"))
    await waitFor(() => expect(screen.getByTestId("model-selector")).toHaveValue("vision-plus"))
  })

  it("fills the design composer from the case '请求修改' action without sending", async () => {
    render(<DesignTab versionId="2001" initialCaseId="7001" />)
    await screen.findByTestId("btn-confirm-detail-7001")

    fireEvent.click(screen.getByRole("button", { name: "请求修改" }))

    expect(screen.getByTestId("composer-textarea")).toHaveValue("请修改：退款 T+1 到账")
    expect(clarificationApi.postMessage).not.toHaveBeenCalled()
  })

  it("batch confirms selected cases without creating an execution wave", async () => {
    render(<DesignTab versionId="2001" />)

    await screen.findByTestId("case-checkbox-7001")
    fireEvent.click(screen.getByTestId("case-checkbox-7001"))
    fireEvent.click(screen.getByTestId("case-checkbox-7003"))
    fireEvent.click(screen.getByTestId("case-batch-confirm"))

    await waitFor(() =>
      expect(api.confirmCases).toHaveBeenCalledWith("2001", {
        scope: "case",
        target_id: "7001",
      }),
    )
    expect(api.confirmCases).toHaveBeenCalledWith("2001", {
      scope: "case",
      target_id: "7003",
    })
    expect(api.confirmStageCases).not.toHaveBeenCalled()
    expect(await screen.findByTestId("design-confirm-notice")).toHaveTextContent(
      "已确认 2 条用例（未执行）",
    )
  })

  it("hides case confirm/execute actions outside the case gate", async () => {
    render(<DesignTab versionId="2001" stage="designing" />)

    await screen.findByTestId("design-mindmap")
    expect(screen.getByTestId("case-gate-notice")).toHaveTextContent(
      "用例确认与批量执行只在「用例待确认」阶段可用",
    )
    expect(screen.queryByTestId("case-batch-confirm")).not.toBeInTheDocument()
    expect(screen.queryByTestId("case-batch-execute")).not.toBeInTheDocument()
    expect(screen.queryByTestId("btn-confirm-case-7001")).not.toBeInTheDocument()
  })

  it("batch executes only the confirmed cases in the selection", async () => {
    render(<DesignTab versionId="2001" />)

    await screen.findByTestId("case-checkbox-7003")
    fireEvent.click(screen.getByTestId("case-checkbox-7003"))
    expect(screen.getByTestId("case-batch-execute")).toBeEnabled()
    fireEvent.click(screen.getByTestId("case-batch-execute"))

    await waitFor(() => expect(api.confirmStageCases).toHaveBeenCalledWith("2001", ["7003"]))
    expect(await screen.findByTestId("design-confirm-notice")).toHaveTextContent(
      "已提交 1 条用例执行",
    )
  })

  it("hands checked cases to AI modification as case reference cards", async () => {
    clarificationApi.postMessage.mockResolvedValue({
      id: "5001",
      conversation_id: "4009",
      role: "user",
      parts: [
        { type: "text", text: "请修改以下 1 条用例：" },
        {
          type: "case_card",
          case_id: "7001",
          case_version_id: "7002",
          title: "退款 T+1 到账",
          status: "draft",
        },
      ],
      refs: [],
      created_at: TIMESTAMP,
    })
    render(<DesignTab versionId="2001" />)

    await screen.findByTestId("case-checkbox-7001")
    fireEvent.click(screen.getByTestId("case-checkbox-7001"))
    fireEvent.click(screen.getByTestId("case-batch-ai-modify"))

    expect(screen.getByTestId("composer-case-ref-7001")).toHaveTextContent("退款 T+1 到账")
    expect(screen.getByTestId("composer-textarea")).toHaveValue(
      "请修改以下 1 条用例：\n- 退款 T+1 到账（#7001）",
    )

    fireEvent.click(screen.getByRole("button", { name: "发送" }))

    await waitFor(() => expect(clarificationApi.postMessage).toHaveBeenCalledTimes(1))
    const payload = clarificationApi.postMessage.mock.calls[0]?.[2] as {
      parts: Array<Record<string, unknown>>
    }
    expect(payload.parts).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          type: "case_card",
          case_id: "7001",
          case_version_id: "7002",
          title: "退款 T+1 到账",
          status: "draft",
        }),
      ]),
    )
    expect(await screen.findByTestId("message-case-card-7001")).toBeInTheDocument()
  })

  it("hands the detail case to AI modification as a reference card", async () => {
    render(<DesignTab versionId="2001" initialCaseId="7001" />)

    fireEvent.click(await screen.findByTestId("btn-ai-modify-detail-7001"))

    expect(screen.getByTestId("composer-case-ref-7001")).toBeInTheDocument()
    expect(screen.getByTestId("composer-textarea")).toHaveValue(
      "请修改以下 1 条用例：\n- 退款 T+1 到账（#7001）",
    )
  })
})
