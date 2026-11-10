import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import type { QuestionBatch } from "@/features/clarification/api"
import { ApiError } from "@/lib/api-client"

const versionApi = vi.hoisted(() => ({
  getVersion: vi.fn(),
  getVersionStage: vi.fn(),
  listPlanItems: vi.fn(),
  getVersionBinding: vi.fn(),
  getVersionFacts: vi.fn(),
  listWorkItems: vi.fn(),
  getDiscoveryObservations: vi.fn(),
  getDiscoveryCoverage: vi.fn(),
  getLatestContextPacket: vi.fn(),
  advanceStage: vi.fn(),
  rejectStage: vi.fn(),
  arbitrateFact: vi.fn(),
}))
vi.mock("./api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./api")>()
  return { ...actual, ...versionApi }
})

// The context packet card is admin-only; the page gates its mount on the cached
// session role, so the tests drive that role explicitly.
const auth = vi.hoisted(() => ({ isAdmin: false }))
vi.mock("@/features/auth/useIsAdmin", () => ({ useIsAdmin: () => auth.isAdmin }))

const conversationApi = vi.hoisted(() => ({ getLatestContextPacket: vi.fn() }))
vi.mock("@/features/conversation/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/features/conversation/api")>()
  return { ...actual, ...conversationApi }
})

const clarificationApi = vi.hoisted(() => ({
  listQuestions: vi.fn(),
  listVersionAttachments: vi.fn(),
}))
vi.mock("@/features/clarification/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/features/clarification/api")>()
  return { ...actual, ...clarificationApi }
})

const deliveryApi = vi.hoisted(() => ({
  listIssues: vi.fn(),
  listReports: vi.fn(),
}))
vi.mock("@/features/delivery/api", () => deliveryApi)

const executionApi = vi.hoisted(() => ({
  listHumanRequests: vi.fn(),
  resolveHumanRequest: vi.fn(),
}))
vi.mock("@/features/execution/api", () => executionApi)

const sse = vi.hoisted(() => ({
  handlers: null as Record<string, (event: unknown) => void> | null,
}))
vi.mock("@/features/clarification/useVersionEvents", () => ({
  useVersionEvents: (
    _versionId: string,
    handlers: Record<string, (event: unknown) => void>,
  ): string => {
    sse.handlers = handlers
    return "live"
  },
}))

vi.mock("@/features/clarification/RequirementsTab", async () => {
  const React = await import("react")
  return {
    RequirementsTab: (props: { versionId: string }) =>
      React.createElement("div", {
        "data-testid": "clarification-workspace",
        "data-version-id": props.versionId,
      }),
  }
})

const executionTab = vi.hoisted(() => ({ evidenceId: undefined as string | undefined }))
vi.mock("@/features/execution/ExecutionTab", async () => {
  const React = await import("react")
  return {
    ExecutionTab: (props: { versionId: string; initialEvidenceId?: string }) => {
      executionTab.evidenceId = props.initialEvidenceId
      return React.createElement("div", {
        "data-testid": "execution-workspace",
        "data-version-id": props.versionId,
      })
    },
  }
})

vi.mock("@/features/design/DesignTab", async () => {
  const React = await import("react")
  return {
    DesignTab: (props: { versionId: string }) =>
      React.createElement("div", {
        "data-testid": "design-workspace",
        "data-version-id": props.versionId,
      }),
  }
})
vi.mock("@/features/delivery/IssuesTab", async () => {
  const React = await import("react")
  return {
    IssuesTab: (props: { versionId: string }) =>
      React.createElement("div", {
        "data-testid": "issues-workspace",
        "data-version-id": props.versionId,
      }),
  }
})
vi.mock("@/features/delivery/ReportsTab", async () => {
  const React = await import("react")
  return {
    ReportsTab: (props: { versionId: string }) =>
      React.createElement("div", {
        "data-testid": "reports-workspace",
        "data-version-id": props.versionId,
      }),
  }
})
vi.mock("@/features/chain/ChainTab", async () => {
  const React = await import("react")
  return {
    ChainTab: (props: { versionId: string }) =>
      React.createElement("div", {
        "data-testid": "chain-workspace",
        "data-version-id": props.versionId,
      }),
  }
})
vi.mock("@/features/code-intel/CodeTab", async () => {
  const React = await import("react")
  return {
    CodeTab: (props: { versionId: string }) =>
      React.createElement("div", {
        "data-testid": "code-workspace",
        "data-version-id": props.versionId,
      }),
  }
})

const navigation = vi.hoisted(() => ({
  push: vi.fn(),
  replace: vi.fn(),
  refresh: vi.fn(),
  back: vi.fn(),
  forward: vi.fn(),
  prefetch: vi.fn(),
}))
const search = vi.hoisted(() => ({ params: new URLSearchParams() }))
vi.mock("next/navigation", () => ({
  useRouter: () => navigation,
  usePathname: () => "/versions/2001",
  useSearchParams: () => search.params,
}))

import { approvalHumanRequestFixture } from "@/features/execution/test-fixtures"

import { bindingOverviewFixture, versionFixture } from "./test-fixtures"
import { VersionDetailPage } from "./VersionDetailPage"

const stageClarifying = {
  version_id: "2001",
  stage: "clarifying" as const,
  stage_entered_at: "2026-11-05T01:00:00Z",
  blocker: null,
}

const stageRequirementReview = {
  ...stageClarifying,
  stage: "requirement_review" as const,
  stage_entered_at: "2026-11-05T02:00:00Z",
}

const questionBatch: QuestionBatch = {
  id: "101",
  version_id: "2001",
  conversation_id: "4001",
  message_id: "6100",
  kind: "clarification_question",
  status: "pending",
  batch_id: "qb-1",
  questions: [
    {
      id: "q1",
      text: "退款按 T+1 还是 T+3？",
      options: [
        { id: "a", label: "T+1" },
        { id: "b", label: "T+3" },
      ],
      multiple: false,
      allow_free_text: true,
      required: true,
      source_refs: [],
    },
  ],
  expires_at: "2099-01-01T00:00:00Z",
  answers: [],
  created_at: "2026-11-05T01:00:00Z",
}

/** W2-2 extends GET /questions with decision rows (empty questions array). */
const decisionBatch: QuestionBatch = {
  ...questionBatch,
  id: "401",
  kind: "approval",
  batch_id: "",
  questions: [],
}

describe("VersionDetailPage v0.22 W0 wiring", () => {
  beforeEach(() => {
    versionApi.getVersion.mockReset().mockResolvedValue(versionFixture)
    versionApi.getVersionStage.mockReset().mockResolvedValue(stageClarifying)
    versionApi.listPlanItems.mockReset().mockResolvedValue([])
    versionApi.getVersionBinding.mockReset().mockResolvedValue(bindingOverviewFixture)
    versionApi.getVersionFacts.mockReset().mockResolvedValue([])
    versionApi.listWorkItems.mockReset().mockResolvedValue([])
    versionApi.getDiscoveryObservations.mockReset().mockResolvedValue([])
    versionApi.getDiscoveryCoverage.mockReset().mockResolvedValue({
      pages: {
        observed: 0,
        proposed: 0,
        approved: 0,
        rejected: 0,
        out_of_scope: 0,
        uncovered: [],
      },
      apis: {
        observed: 0,
        proposed: 0,
        approved: 0,
        rejected: 0,
        out_of_scope: 0,
        uncovered: [],
      },
    })
    versionApi.getLatestContextPacket
      .mockReset()
      .mockRejectedValue(
        new ApiError({ status: 403, code: "forbidden", message: "上下文包仅管理员可读" }),
      )
    clarificationApi.listQuestions.mockReset().mockResolvedValue([questionBatch])
    clarificationApi.listVersionAttachments.mockReset().mockResolvedValue([])
    deliveryApi.listIssues.mockReset().mockResolvedValue([])
    deliveryApi.listReports.mockReset().mockResolvedValue([])
    executionApi.listHumanRequests.mockReset().mockResolvedValue([])
    executionApi.resolveHumanRequest.mockReset().mockResolvedValue({
      ...approvalHumanRequestFixture,
      status: "answered",
    })
    navigation.replace.mockReset()
    search.params = new URLSearchParams()
    sse.handlers = null
    executionTab.evidenceId = undefined
    auth.isAdmin = false
    conversationApi.getLatestContextPacket.mockReset()
    conversationApi.getLatestContextPacket.mockRejectedValue(
      new ApiError({ status: 403, code: "forbidden", message: "上下文包仅管理员可读" }),
    )
  })

  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it("wires stage, plan and question cards into the version page", async () => {
    render(<VersionDetailPage versionId="2001" />)

    expect(await screen.findByTestId("stage-bar")).toBeInTheDocument()
    expect(await screen.findByTestId("plan-overview")).toBeInTheDocument()
    expect(await screen.findByTestId("question-card")).toBeInTheDocument()
    // The v0.22 binding overview replaces the legacy summary section once the
    // stage projection is available.
    expect(await screen.findByRole("region", { name: "SUT 绑定概览" })).toBeInTheDocument()
    expect(versionApi.getVersionBinding).toHaveBeenCalledWith("2001")
    expect(clarificationApi.listQuestions).toHaveBeenCalledWith("2001", { status: "pending" })
  })

  it("renders decision rows as approval cards and keeps them out of QuestionCard", async () => {
    clarificationApi.listQuestions.mockResolvedValue([questionBatch, decisionBatch])
    executionApi.listHumanRequests.mockResolvedValue([approvalHumanRequestFixture])

    render(<VersionDetailPage versionId="2001" />)

    const card = await screen.findByTestId("human-decision-card-9601")
    expect(within(card).getByText("发布前请确认版本可以上线")).toBeInTheDocument()
    // Only the real clarification batch becomes a QuestionCard; the approval
    // row must never render as "澄清提问 共 0 题" or reach /answer.
    expect(screen.getAllByTestId("question-card")).toHaveLength(1)
    expect(screen.queryByText(/共 0 题/)).not.toBeInTheDocument()

    fireEvent.change(within(card).getByLabelText("原因"), { target: { value: "no" } })
    fireEvent.click(within(card).getByRole("button", { name: "拒绝" }))

    await waitFor(() =>
      expect(executionApi.resolveHumanRequest).toHaveBeenCalledWith("9601", {
        decision: "rejected",
        reason: "no",
      }),
    )
    await waitFor(() => expect(executionApi.listHumanRequests.mock.calls.length).toBeGreaterThan(1))
  })

  it("does not mount or request the admin-only context packet for a non-admin", async () => {
    render(<VersionDetailPage versionId="2001" />)

    await screen.findByTestId("version-data")
    expect(screen.queryByTestId("context-packet")).not.toBeInTheDocument()
    expect(conversationApi.getLatestContextPacket).not.toHaveBeenCalled()
  })

  it("mounts and requests the context packet for an admin", async () => {
    auth.isAdmin = true

    render(<VersionDetailPage versionId="2001" />)

    expect(await screen.findByTestId("context-packet")).toBeInTheDocument()
    await waitFor(() => expect(conversationApi.getLatestContextPacket).toHaveBeenCalledWith("2001"))
    expect(await screen.findByTestId("context-packet-forbidden")).toBeInTheDocument()
  })

  it("refreshes stage, plan, questions and binding from version events", async () => {
    render(<VersionDetailPage versionId="2001" />)
    await screen.findByTestId("stage-bar")
    await waitFor(() => expect(versionApi.listPlanItems).toHaveBeenCalledTimes(1))
    const planCalls = versionApi.listPlanItems.mock.calls.length
    const questionCalls = clarificationApi.listQuestions.mock.calls.length
    const humanCalls = executionApi.listHumanRequests.mock.calls.length
    const bindingCalls = versionApi.getVersionBinding.mock.calls.length
    const stageCalls = versionApi.getVersionStage.mock.calls.length

    versionApi.getVersionStage.mockResolvedValue(stageRequirementReview)
    act(() => {
      sse.handlers?.onStageChanged?.({
        type: "version.stage.changed",
        payload: { from: "clarifying", to: "requirement_review", reason: "requirements_ready" },
      })
    })
    await waitFor(() => expect(versionApi.getVersionStage.mock.calls.length).toBe(stageCalls + 1), {
      timeout: 2000,
    })
    await waitFor(
      () =>
        expect(screen.getByTestId("stage-item-requirement_review")).toHaveAttribute(
          "aria-current",
          "step",
        ),
      { timeout: 2000 },
    )

    act(() => {
      sse.handlers?.onPlanChanged?.({
        type: "plan.item_changed",
        payload: { plan_item_id: "9001", status: "done" },
      })
      sse.handlers?.onQuestionChanged?.({
        type: "question.answered",
        payload: { question_id: "101", status: "answered" },
      })
      sse.handlers?.onHumanRequestChanged?.({
        type: "approval.requested",
        payload: { human_request_id: "9601", kind: "approval" },
      })
      sse.handlers?.onBindingChanged?.({
        type: "version.binding.changed",
        payload: { version_id: "2001" },
      })
    })

    await waitFor(() => expect(versionApi.listPlanItems.mock.calls.length).toBe(planCalls + 1), {
      timeout: 2000,
    })
    await waitFor(
      () => expect(clarificationApi.listQuestions.mock.calls.length).toBeGreaterThan(questionCalls),
      { timeout: 2000 },
    )
    // approval.requested/decided refresh the version-level decision cards.
    await waitFor(
      () => expect(executionApi.listHumanRequests.mock.calls.length).toBeGreaterThan(humanCalls),
      { timeout: 2000 },
    )
    await waitFor(
      () => expect(versionApi.getVersionBinding.mock.calls.length).toBe(bindingCalls + 1),
      { timeout: 2000 },
    )
  })

  it("mounts the discovery panels and refetches them on resource.delta", async () => {
    render(<VersionDetailPage versionId="2001" />)

    expect(await screen.findByTestId("discovery-panel")).toBeInTheDocument()
    expect(await screen.findByTestId("coverage-panel")).toBeInTheDocument()
    expect(await screen.findByText("该版本暂无探索发现")).toBeInTheDocument()
    expect(await screen.findByText("该版本暂无探索覆盖")).toBeInTheDocument()
    await waitFor(() => expect(versionApi.getDiscoveryObservations).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(versionApi.getDiscoveryCoverage).toHaveBeenCalledTimes(1))

    act(() => {
      sse.handlers?.onDiscoveryChanged?.({
        type: "resource.delta",
        payload: { version_id: "2001", wave_id: "9201" },
      })
    })

    await waitFor(() => expect(versionApi.getDiscoveryObservations.mock.calls.length).toBe(2), {
      timeout: 2000,
    })
    await waitFor(() => expect(versionApi.getDiscoveryCoverage.mock.calls.length).toBe(2), {
      timeout: 2000,
    })
  })

  it("refreshes the discovery panels and version on a frozen scope snapshot", async () => {
    render(<VersionDetailPage versionId="2001" />)
    await screen.findByTestId("discovery-panel")
    await waitFor(() => expect(versionApi.getDiscoveryObservations).toHaveBeenCalledTimes(1))

    act(() => {
      sse.handlers?.onScopeSnapshotChanged?.({
        type: "version.scope.snapshot_created",
        payload: { version_id: "2001", wave_id: "9201" },
      })
    })

    await waitFor(() => expect(versionApi.getDiscoveryObservations.mock.calls.length).toBe(2), {
      timeout: 2000,
    })
    await waitFor(() => expect(versionApi.getVersion).toHaveBeenCalledTimes(2), { timeout: 2000 })
  })

  it("refreshes the work-item and context-packet projections from version events", async () => {
    auth.isAdmin = true
    render(<VersionDetailPage versionId="2001" />)
    await screen.findByTestId("work-items-panel")
    await waitFor(() => expect(versionApi.listWorkItems).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(conversationApi.getLatestContextPacket).toHaveBeenCalledTimes(1))

    act(() => {
      sse.handlers?.onWorkItemChanged?.({
        type: "work_item.created",
        payload: { work_item_id: "5001", state: "open" },
      })
      sse.handlers?.onContextChanged?.({
        type: "context.compacted",
        payload: { cached_tokens: 10 },
      })
    })

    await waitFor(() => expect(versionApi.listWorkItems.mock.calls.length).toBe(2), {
      timeout: 2000,
    })
    await waitFor(() => expect(conversationApi.getLatestContextPacket.mock.calls.length).toBe(2), {
      timeout: 2000,
    })
  })

  it("refreshes the work-item activity projection on tool.retry", async () => {
    render(<VersionDetailPage versionId="2001" />)
    await screen.findByTestId("work-items-panel")
    await waitFor(() => expect(versionApi.listWorkItems).toHaveBeenCalledTimes(1))

    act(() => {
      sse.handlers?.onToolRetry?.({ type: "tool.retry", payload: { attempt: 2 } })
    })

    await waitFor(() => expect(versionApi.listWorkItems.mock.calls.length).toBe(2), {
      timeout: 2000,
    })
  })

  it("routes the legacy stage event name through the same refresh", async () => {
    render(<VersionDetailPage versionId="2001" />)
    await screen.findByTestId("stage-bar")
    const stageCalls = versionApi.getVersionStage.mock.calls.length

    act(() => {
      sse.handlers?.onStageChanged?.({
        type: "version.stage_changed",
        payload: { from: "created", to: "clarifying" },
      })
    })
    await waitFor(() => expect(versionApi.getVersionStage.mock.calls.length).toBe(stageCalls + 1), {
      timeout: 2000,
    })
  })

  it("shows the discovery revision notice and refetches discovery on case.revision.auto_confirmed", async () => {
    render(<VersionDetailPage versionId="2001" />)
    await screen.findByTestId("discovery-panel")
    await waitFor(() => expect(versionApi.getDiscoveryObservations).toHaveBeenCalledTimes(1))

    act(() => {
      sse.handlers?.onDesignCaseChanged?.({
        type: "case.revision.auto_confirmed",
        payload: {
          version_id: "2001",
          case_id: "7001",
          case_version_id: "7010",
          trigger: "discovery_delta",
          source_observation_id: "501",
          resource_id: "2301",
          rollback_of: "0",
          confidence: "high",
          actor: "system",
        },
      })
    })

    expect(await screen.findByTestId("discovery-revision-notice")).toBeInTheDocument()
    expect(screen.getByText(/自动确认/)).toBeInTheDocument()
    await waitFor(() => expect(versionApi.getDiscoveryObservations.mock.calls.length).toBe(2), {
      timeout: 2000,
    })

    act(() => {
      sse.handlers?.onDesignCaseChanged?.({
        type: "case.revision.auto_confirmed",
        payload: {
          version_id: "2001",
          case_id: "7001",
          case_version_id: "7011",
          trigger: "rollback",
          source_observation_id: "501",
          resource_id: "2301",
          rollback_of: "7010",
          confidence: "high",
          actor: "system",
        },
      })
    })

    expect(await screen.findByText(/已回滚到替代 revision 7011/)).toBeInTheDocument()
    expect(screen.getByText(/原 revision 7010/)).toBeInTheDocument()
  })

  it("forwards the execution evidence deep link to the execution tab", async () => {
    search.params = new URLSearchParams("tab=execution&evidence=960001")

    render(<VersionDetailPage versionId="2001" initialTab="execution" />)
    await screen.findByTestId("execution-workspace")
    expect(executionTab.evidenceId).toBe("960001")
  })

  it("loads the authenticated attachment image as a blob link and revokes the object URL", async () => {
    search.params = new URLSearchParams("tab=overview&attachment=55")
    const image = {
      id: "77",
      attachment_id: "55",
      seq: 1,
      sha256: "a".repeat(64),
      url: `/api/content/${"a".repeat(64)}`,
      mime: "image/png",
      width: 320,
      height: 200,
      status: "done",
      caption: "登录页面",
      error: "",
    }
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url.includes("/attachments/55/images")) {
        return new Response(JSON.stringify({ data: [image] }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        })
      }
      if (url.includes("/api/content/")) {
        // A byte body, not a jsdom `Blob`: on Node 22 undici's
        // `Response.blob()` calls `body.stream()`, which jsdom's Blob does not
        // implement, so a Blob-bodied Response rejects with "object.stream is
        // not a function" and the panel would render "原图加载失败".
        return new Response(new TextEncoder().encode("png"), {
          status: 200,
          headers: { "Content-Type": "image/png" },
        })
      }
      throw new Error(`unexpected fetch: ${url}`)
    })
    vi.stubGlobal("fetch", fetchMock)
    const createObjectURL = vi.fn(() => "blob:attachment-original")
    const revokeObjectURL = vi.fn()
    Object.assign(URL, { createObjectURL, revokeObjectURL })

    const { unmount } = render(<VersionDetailPage versionId="2001" initialTab="overview" />)
    const link = await screen.findByTestId("attachment-image-link")
    expect(link).toHaveAttribute("href", "blob:attachment-original")
    expect(link).toHaveAttribute("target", "_blank")
    expect(createObjectURL).toHaveBeenCalledTimes(1)

    unmount()
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:attachment-original")
  })

  it("lists the version attachments and opens the image panel from a row link", async () => {
    clarificationApi.listVersionAttachments.mockResolvedValue([
      {
        id: "55",
        name: "需求文档.docx",
        mime: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
        size: 2048,
        extraction_status: "done",
        error_code: "",
        error_text: "",
        image_count: 3,
      },
      {
        id: "56",
        name: "损坏文档.pdf",
        mime: "application/pdf",
        size: 1024,
        extraction_status: "failed",
        error_code: "extraction_failed",
        error_text: "解析器崩溃",
        image_count: 0,
      },
    ])
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url.includes("/attachments/55/images")) {
        return new Response(JSON.stringify({ data: [] }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        })
      }
      throw new Error(`unexpected fetch: ${url}`)
    })
    vi.stubGlobal("fetch", fetchMock)

    render(<VersionDetailPage versionId="2001" initialTab="overview" />)

    const section = await screen.findByTestId("version-attachments")
    const doneRow = await screen.findByTestId("version-attachment-55")
    expect(section).toHaveTextContent("附件")
    expect(doneRow).toHaveTextContent("需求文档.docx")
    expect(doneRow).toHaveTextContent("已解析")
    expect(doneRow).toHaveTextContent("3 张图片")
    expect(screen.getByTestId("version-attachment-56")).toHaveTextContent("解析失败")
    expect(screen.getByTestId("version-attachment-56")).toHaveTextContent("解析器崩溃")

    const failedRow = screen.getByTestId("version-attachment-56")
    // A zero-image attachment still reports the count but has no image panel to
    // open, so the "查看图片 →" affordance is hidden.
    expect(failedRow).toHaveTextContent("0 张图片")
    expect(within(failedRow).queryByText("查看图片 →")).not.toBeInTheDocument()
    expect(within(doneRow).getByText("查看图片 →")).toBeInTheDocument()

    const link = within(doneRow).getByRole("link")
    expect(link).toHaveAttribute("href", "/versions/2001?tab=overview&attachment=55")
    // Opening the image panel preserves unrelated workspace URL params.
    search.params.set("conversation", "4001")
    search.params.set("requirement", "6001")
    fireEvent.click(link)

    expect(await screen.findByTestId("attachment-images")).toBeInTheDocument()
    expect(fetchMock).toHaveBeenCalled()
    expect(navigation.replace).toHaveBeenCalledWith(
      "/versions/2001?tab=overview&conversation=4001&requirement=6001&attachment=55",
      { scroll: false },
    )
  })

  it("shows an empty state when the version has no attachments", async () => {
    render(<VersionDetailPage versionId="2001" initialTab="overview" />)

    expect(await screen.findByTestId("version-attachments-empty")).toHaveTextContent(
      "该版本暂无附件",
    )
  })

  it("reports reanalyze counts, per-row update times and distinct failure/skip reasons", async () => {
    search.params = new URLSearchParams("tab=overview&attachment=55")
    const before = [
      {
        id: "77",
        attachment_id: "55",
        seq: 1,
        sha256: "",
        mime: "image/png",
        status: "failed",
        caption: "",
        error: "模型超时",
        updated_at: "2026-11-10T01:00:00Z",
      },
      {
        id: "78",
        attachment_id: "55",
        seq: 2,
        sha256: "",
        mime: "image/png",
        status: "skipped",
        caption: "",
        error: "",
        updated_at: "2026-11-10T01:00:00Z",
      },
    ]
    const after = [
      {
        id: "77",
        attachment_id: "55",
        seq: 1,
        sha256: "",
        mime: "image/png",
        status: "done",
        caption: "登录页面",
        error: "",
        updated_at: "2026-11-10T02:00:00",
      },
      {
        id: "78",
        attachment_id: "55",
        seq: 2,
        sha256: "",
        mime: "image/png",
        status: "failed",
        caption: "",
        error: "模型超时",
        updated_at: "2026-11-10T02:01:00",
      },
      {
        id: "79",
        attachment_id: "55",
        seq: 3,
        sha256: "",
        mime: "image/png",
        status: "skipped",
        caption: "",
        error: "无文字图片",
        updated_at: "2026-11-10T02:02:00",
      },
    ]
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.includes("/reanalyze")) {
        return new Response(JSON.stringify({ data: after }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        })
      }
      if (url.includes("/attachments/55/images")) {
        return new Response(JSON.stringify({ data: before }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        })
      }
      throw new Error(`unexpected fetch: ${url} ${init?.method ?? "GET"}`)
    })
    vi.stubGlobal("fetch", fetchMock)

    render(<VersionDetailPage versionId="2001" initialTab="overview" />)
    await screen.findByTestId("attachment-images")
    await waitFor(() => expect(screen.getByTestId("attachment-image-77")).toHaveTextContent("失败"))

    fireEvent.click(screen.getByRole("button", { name: "重新分析图片" }))

    const notice = await screen.findByTestId("attachment-reanalyze-notice")
    expect(notice).toHaveTextContent("已重新分析：1 成功 / 1 失败 / 1 跳过")
    // Literal expectation, not the same formatter the component uses.
    expect(notice).toHaveTextContent("最近更新 2026-11-10 02:02")
    expect(await screen.findByTestId("attachment-image-77")).toHaveTextContent("更新于")
    expect(screen.getByTestId("attachment-image-78")).toHaveTextContent("失败")
    expect(screen.getByTestId("attachment-image-79")).toHaveTextContent("跳过")
    expect(screen.getByTestId("attachment-image-78")).toHaveTextContent("模型超时")
    expect(screen.getByTestId("attachment-image-79")).toHaveTextContent("无文字图片")
  })

  it("shows an inline error and no success notice when reanalyze fails", async () => {
    search.params = new URLSearchParams("tab=overview&attachment=55")
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.includes("/reanalyze")) {
        return new Response(
          JSON.stringify({
            error: { code: "internal", message: "重新分析失败，请稍后重试" },
          }),
          { status: 500, headers: { "Content-Type": "application/json" } },
        )
      }
      if (url.includes("/attachments/55/images")) {
        return new Response(JSON.stringify({ data: [] }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        })
      }
      throw new Error(`unexpected fetch: ${url} ${init?.method ?? "GET"}`)
    })
    vi.stubGlobal("fetch", fetchMock)

    render(<VersionDetailPage versionId="2001" initialTab="overview" />)
    await screen.findByTestId("attachment-images")
    fireEvent.click(screen.getByRole("button", { name: "重新分析图片" }))

    expect(await screen.findByText("重新分析失败，请稍后重试")).toBeInTheDocument()
    expect(screen.queryByTestId("attachment-reanalyze-notice")).not.toBeInTheDocument()
  })
})
