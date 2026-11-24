import { act, cleanup, render, screen, waitFor } from "@testing-library/react"
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

// R-E2r (release notes E §3): the event's only producer is now the Rollback
// path, announcing a DRAFT replacement (trigger=rollback, rollback_of nonzero).
// The discovery auto-confirm path no longer emits this event.
function rollbackDraftEvent(caseId: string, caseVersionId: string, rollbackOf: string) {
  return {
    type: "case.revision.auto_confirmed",
    payload: {
      version_id: "2001",
      case_id: caseId,
      case_version_id: caseVersionId,
      trigger: "rollback",
      source_observation_id: "501",
      resource_id: "2301",
      rollback_of: rollbackOf,
      confidence: "high",
      actor: "1",
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

  it("shows a rollback draft revision for a case that is not selected", async () => {
    render(<DesignTab versionId="2001" />)
    await screen.findByTestId("design-mindmap")

    act(() => {
      sse.handlers?.onDesignCaseChanged?.(rollbackDraftEvent("7003", "7004", "7002"))
    })

    expect(await screen.findByText(/已回滚到替代 revision 7004/)).toBeInTheDocument()
    expect(screen.getByText(/用例 7003/)).toBeInTheDocument()
    // R-E2r: the event never renders an auto-confirm anymore.
    expect(screen.queryByText("自动确认")).not.toBeInTheDocument()
  })

  it("refreshes the selected case and graph after a rollback event", async () => {
    render(<DesignTab versionId="2001" initialCaseId="7001" />)
    await screen.findByText("退款 T+1 到账")

    const graphCalls = api.getDesignGraph.mock.calls.length
    const detailCalls = api.getDesignCase.mock.calls.length

    act(() => {
      sse.handlers?.onDesignCaseChanged?.(rollbackDraftEvent("7001", "7011", "7002"))
    })

    expect(await screen.findByText(/已回滚到替代 revision 7011/)).toBeInTheDocument()
    expect(screen.getByText(/原 revision 7002/)).toBeInTheDocument()
    expect(screen.queryByText("自动确认")).not.toBeInTheDocument()
    await waitFor(() => expect(api.getDesignGraph.mock.calls.length).toBeGreaterThan(graphCalls))
    await waitFor(() => expect(api.getDesignCase.mock.calls.length).toBeGreaterThan(detailCalls))
  })
})
