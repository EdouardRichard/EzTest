import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

const api = vi.hoisted(() => ({
  getDesignGraph: vi.fn(),
  getDesignOverview: vi.fn(),
  listDesignConversations: vi.fn(),
  createDesignConversation: vi.fn(),
  getDesignCase: vi.fn(),
  confirmCases: vi.fn(),
  createExport: vi.fn(),
  downloadExport: vi.fn(),
  listExports: vi.fn(),
}))

vi.mock("./api", () => api)

vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: vi.fn(), push: vi.fn() }),
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

const conversationApi = vi.hoisted(() => ({
  listPublicModels: vi.fn(),
  updateConversationModel: vi.fn(),
}))
vi.mock("@/features/conversation/api", () => conversationApi)

const versionsApi = vi.hoisted(() => ({ updateVersion: vi.fn() }))
vi.mock("@/features/versions/api", () => versionsApi)

import { DesignTab } from "./DesignTab"
import { designCaseDetailFixture, designGraphFixture, designOverviewFixture } from "./test-fixtures"
import type { DesignGraph } from "./types"

const TIMESTAMP = "2026-10-31T12:00:00Z"

const designConversation = {
  id: "4009",
  title: "用例设计 2026-10-31 12:00",
  kind: "design" as const,
  status: "active" as const,
  message_count: 2,
  last_message_at: TIMESTAMP,
  created_at: TIMESTAMP,
}

const publicModels = [
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
    default: true,
    deprecated: false,
  },
]

/**
 * The backend graph now carries each case's latest execution verdict; this is
 * the exact shape `GET /design/graph` returns (S8 production wiring).
 */
function graphWithExecutionVerdicts(): DesignGraph {
  const verdicts: Record<string, "passed" | "failed" | "blocked"> = {
    "7001": "passed",
    "7003": "failed",
    "7005": "blocked",
  }
  return {
    ...designGraphFixture,
    requirements: designGraphFixture.requirements.map((requirement) => ({
      ...requirement,
      scenarios: requirement.scenarios.map((scenario) => ({
        ...scenario,
        cases: scenario.cases.map((designCase) => ({
          ...designCase,
          execution_verdict: verdicts[designCase.case_id] ?? "inconclusive",
        })),
      })),
    })),
  }
}

function mockWorkspace(graph: DesignGraph = designGraphFixture) {
  api.getDesignGraph.mockResolvedValue(graph)
  api.getDesignOverview.mockResolvedValue(designOverviewFixture)
  api.listDesignConversations.mockResolvedValue([designConversation])
  api.getDesignCase.mockResolvedValue(designCaseDetailFixture)
}

describe("DesignTab execution result wiring (S8)", () => {
  beforeEach(() => {
    for (const fn of Object.values(api)) fn.mockReset()
    for (const fn of Object.values(clarificationApi)) fn.mockReset()
    conversationApi.listPublicModels.mockReset()
    conversationApi.updateConversationModel.mockReset()
    versionsApi.updateVersion.mockReset()
    conversationApi.listPublicModels.mockResolvedValue(publicModels)
    clarificationApi.listMessages.mockResolvedValue([])
    clarificationApi.getTeamRoom.mockResolvedValue({
      team: "design",
      digest: null,
      posts: [],
    })
    mockWorkspace(graphWithExecutionVerdicts())
  })

  afterEach(cleanup)

  it("passes the graph verdicts to the mindmap so results colour the real page", async () => {
    render(<DesignTab versionId="2001" />)

    const passed = await screen.findByTestId("rf__node-case-7001")
    expect(passed).toHaveClass("case-result-passed")
    expect(screen.getByTestId("rf__node-case-7003")).toHaveClass("case-result-failed")
    expect(screen.getByTestId("rf__node-case-7005")).toHaveClass("case-result-blocked")

    const resultFilter = screen.getByTestId("design-filter-result")
    expect(resultFilter).toBeEnabled()
    expect(resultFilter).not.toHaveAttribute("title")
  })

  it("filters the mindmap by the verdicts carried by the graph", async () => {
    render(<DesignTab versionId="2001" />)
    await screen.findByTestId("case-card-7001")

    fireEvent.change(screen.getByTestId("design-filter-result"), {
      target: { value: "failed" },
    })

    await waitFor(() => expect(screen.queryByTestId("case-card-7001")).not.toBeInTheDocument())
    expect(screen.getByTestId("case-card-7003")).toBeInTheDocument()
    expect(screen.queryByTestId("case-card-7005")).not.toBeInTheDocument()
  })

  it("keeps the result filter disabled when the graph carries no verdicts", async () => {
    mockWorkspace()
    render(<DesignTab versionId="2001" />)
    await screen.findByTestId("case-card-7001")

    expect(screen.getByTestId("design-filter-result")).toBeDisabled()
    expect(screen.getByTestId("rf__node-case-7001")).not.toHaveClass("case-result-passed")
  })
})
