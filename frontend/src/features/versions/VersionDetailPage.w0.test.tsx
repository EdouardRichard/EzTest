import { act, cleanup, render, screen, waitFor } from "@testing-library/react"
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

const clarificationApi = vi.hoisted(() => ({ listQuestions: vi.fn() }))
vi.mock("@/features/clarification/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/features/clarification/api")>()
  return { ...actual, ...clarificationApi }
})

const deliveryApi = vi.hoisted(() => ({
  listIssues: vi.fn(),
  listReports: vi.fn(),
}))
vi.mock("@/features/delivery/api", () => deliveryApi)

const executionApi = vi.hoisted(() => ({ listHumanRequests: vi.fn() }))
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

describe("VersionDetailPage v0.22 W0 wiring", () => {
  beforeEach(() => {
    versionApi.getVersion.mockReset().mockResolvedValue(versionFixture)
    versionApi.getVersionStage.mockReset().mockResolvedValue(stageClarifying)
    versionApi.listPlanItems.mockReset().mockResolvedValue([])
    versionApi.getVersionBinding.mockReset().mockResolvedValue(bindingOverviewFixture)
    versionApi.getVersionFacts.mockReset().mockResolvedValue([])
    versionApi.listWorkItems.mockReset().mockResolvedValue([])
    versionApi.getLatestContextPacket
      .mockReset()
      .mockRejectedValue(
        new ApiError({ status: 403, code: "forbidden", message: "上下文包仅管理员可读" }),
      )
    clarificationApi.listQuestions.mockReset().mockResolvedValue([questionBatch])
    deliveryApi.listIssues.mockReset().mockResolvedValue([])
    deliveryApi.listReports.mockReset().mockResolvedValue([])
    executionApi.listHumanRequests.mockReset().mockResolvedValue([])
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
    await waitFor(
      () => expect(versionApi.getVersionBinding.mock.calls.length).toBe(bindingCalls + 1),
      { timeout: 2000 },
    )
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

    await waitFor(() => expect(versionApi.listWorkItems.mock.calls.length).toBe(2), { timeout: 2000 })
    await waitFor(
      () => expect(conversationApi.getLatestContextPacket.mock.calls.length).toBe(2),
      { timeout: 2000 },
    )
  })

  it("refreshes the work-item activity projection on tool.retry", async () => {
    render(<VersionDetailPage versionId="2001" />)
    await screen.findByTestId("work-items-panel")
    await waitFor(() => expect(versionApi.listWorkItems).toHaveBeenCalledTimes(1))

    act(() => {
      sse.handlers?.onToolRetry?.({ type: "tool.retry", payload: { attempt: 2 } })
    })

    await waitFor(() => expect(versionApi.listWorkItems.mock.calls.length).toBe(2), { timeout: 2000 })
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
        return new Response(new Blob(["png"], { type: "image/png" }), {
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
})
