import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import type { ConversationSummary } from "@/features/clarification/types"

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
  getExport: vi.fn(),
  listExports: vi.fn(),
  rollbackCaseVersion: vi.fn(),
}))
vi.mock("./api", () => api)

const navigation = vi.hoisted(() => ({ replace: vi.fn(), push: vi.fn() }))
vi.mock("next/navigation", () => ({
  useRouter: () => navigation,
  usePathname: () => "/versions/2001",
}))

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

const versionsApi = vi.hoisted(() => ({ updateVersion: vi.fn() }))
vi.mock("@/features/versions/api", () => versionsApi)

vi.mock("@/features/auth/useIsAdmin", () => ({ useIsAdmin: () => true }))

import { DesignTab } from "./DesignTab"
import { designCaseDetailFixture, designGraphFixture, designOverviewFixture } from "./test-fixtures"

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

function mockWorkspace() {
  api.getDesignGraph.mockResolvedValue(designGraphFixture)
  api.getDesignOverview.mockResolvedValue(designOverviewFixture)
  api.listDesignConversations.mockResolvedValue([designConversation])
  api.getDesignCase.mockResolvedValue(designCaseDetailFixture)
  conversationApi.listPublicModels.mockResolvedValue([])
  clarificationApi.listMessages.mockResolvedValue([])
  clarificationApi.getTeamRoom.mockResolvedValue({ team: "design", digest: null, posts: [] })
}

function autoConfirmedEvent(caseId: string, caseVersionId: string) {
  return {
    type: "case.revision.auto_confirmed",
    payload: {
      version_id: "2001",
      case_id: caseId,
      case_version_id: caseVersionId,
      trigger: "discovery_delta",
      source_observation_id: "501",
      resource_id: "2301",
      rollback_of: "0",
      confidence: "high",
      actor: "system",
    },
  }
}

describe("DesignTab discovery revisions", () => {
  beforeEach(() => {
    for (const fn of Object.values(api)) fn.mockReset()
    for (const fn of Object.values(clarificationApi)) fn.mockReset()
    for (const fn of Object.values(conversationApi)) fn.mockReset()
    versionsApi.updateVersion.mockReset()
    navigation.replace.mockReset()
    sse.handlers = null
    mockWorkspace()
  })

  afterEach(cleanup)

  it("shows an auto-confirmed revision for a case that is not selected", async () => {
    render(<DesignTab versionId="2001" />)
    await screen.findByTestId("design-mindmap")

    act(() => {
      sse.handlers?.onDesignCaseChanged?.(autoConfirmedEvent("7003", "7004"))
    })

    expect(await screen.findByText(/自动确认/)).toBeInTheDocument()
    expect(screen.getByText(/用例 7003/)).toBeInTheDocument()
  })

  it("refreshes the selected case and graph after a rollback", async () => {
    api.rollbackCaseVersion.mockResolvedValue({
      case_id: "7001",
      case_version_id: "7011",
      rolled_back_from: "7002",
      revision: 3,
      status: "confirmed",
      trigger: "rollback",
    })
    render(<DesignTab versionId="2001" initialCaseId="7001" />)
    await screen.findByText("退款 T+1 到账")

    act(() => {
      sse.handlers?.onDesignCaseChanged?.(autoConfirmedEvent("7001", "7002"))
    })

    expect(await screen.findByText(/自动确认/)).toBeInTheDocument()
    const graphCalls = api.getDesignGraph.mock.calls.length
    const detailCalls = api.getDesignCase.mock.calls.length

    fireEvent.click(screen.getByRole("button", { name: "回滚" }))

    await waitFor(() => expect(api.rollbackCaseVersion).toHaveBeenCalledWith("2001", "7002"))
    expect(await screen.findByText(/已回滚到替代 revision 7011/)).toBeInTheDocument()
    await waitFor(() => expect(api.getDesignGraph.mock.calls.length).toBeGreaterThan(graphCalls))
    await waitFor(() => expect(api.getDesignCase.mock.calls.length).toBeGreaterThan(detailCalls))
  })
})
