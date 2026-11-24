import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
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
  listJudgments: vi.fn(),
  overrideJudgment: vi.fn(),
}))
vi.mock("./api", () => api)

const navigation = vi.hoisted(() => ({ replace: vi.fn(), push: vi.fn() }))
vi.mock("next/navigation", () => ({
  useRouter: () => navigation,
  usePathname: () => "/versions/2001",
}))

vi.mock("@/features/clarification/useVersionEvents", () => ({
  useVersionEvents: (): string => "live",
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

describe("DesignTab judgment report (case_review)", () => {
  beforeEach(() => {
    for (const fn of Object.values(api)) fn.mockReset()
    for (const fn of Object.values(clarificationApi)) fn.mockReset()
    for (const fn of Object.values(conversationApi)) fn.mockReset()
    versionsApi.updateVersion.mockReset()
    navigation.replace.mockReset()
    mockWorkspace()
    api.listJudgments.mockResolvedValue({
      cases: [
        {
          case_id: "7003",
          current_version_id: "7004",
          review_state: "escalated_fail",
          judgment: {
            id: "9002",
            case_id: "7003",
            case_version_id: "7004",
            content_hash: "b".repeat(64),
            round: 2,
            verdict: "fail",
            findings: [
              {
                dimension: "coverage",
                severity: "blocker",
                message: "AC-2 未覆盖",
                case_ref: "7003",
              },
            ],
            ac_coverage_matrix: {},
            created_at: TIMESTAMP,
          },
        },
      ],
    })
  })

  afterEach(cleanup)

  it("opens the judgment report and lists the escalated case with an override action", async () => {
    render(<DesignTab versionId="2001" stage="case_review" />)
    await screen.findByTestId("design-mindmap")

    fireEvent.click(screen.getByTestId("design-judgment-report"))
    expect(await screen.findByTestId("judgment-report")).toBeInTheDocument()
    expect(api.listJudgments).toHaveBeenCalledWith("2001", undefined)
    expect(screen.getByTestId("judgment-case-7003")).toBeInTheDocument()
    // admin + escalated_fail → override offered
    expect(screen.getByTestId("override-btn")).toBeInTheDocument()
  })

  it("override from the report calls the API and refreshes the graph", async () => {
    api.overrideJudgment.mockResolvedValue({
      id: "9100",
      case_id: "7003",
      case_version_id: "7004",
      content_hash: "b".repeat(64),
      condition: "overridden",
      actor_id: "1",
      reason: "人工复核通过",
      created_at: TIMESTAMP,
    })
    render(<DesignTab versionId="2001" stage="case_review" />)
    await screen.findByTestId("design-mindmap")
    fireEvent.click(screen.getByTestId("design-judgment-report"))
    await screen.findByTestId("judgment-report")

    const graphCalls = api.getDesignGraph.mock.calls.length
    fireEvent.click(screen.getByTestId("override-btn"))
    fireEvent.change(screen.getByLabelText(/决断原因/), { target: { value: "人工复核通过" } })
    fireEvent.click(screen.getByTestId("override-submit"))

    await waitFor(() =>
      expect(api.overrideJudgment).toHaveBeenCalledWith("2001", "7003", "人工复核通过"),
    )
    await waitFor(() => expect(api.getDesignGraph.mock.calls.length).toBeGreaterThan(graphCalls))
  })

  it("does not offer the judgment report button outside the case gate", async () => {
    render(<DesignTab versionId="2001" stage="designing" />)
    await screen.findByTestId("design-mindmap")
    expect(screen.queryByTestId("design-judgment-report")).toBeNull()
  })
})
