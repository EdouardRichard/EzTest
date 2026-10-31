import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

const designApi = vi.hoisted(() => ({
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
vi.mock("./api", () => designApi)

const versionsApi = vi.hoisted(() => ({ updateVersion: vi.fn() }))
vi.mock("@/features/versions/api", () => versionsApi)

vi.mock("@/features/clarification/useVersionEvents", () => ({
  useVersionEvents: () => "live",
}))

const navigation = vi.hoisted(() => ({ replace: vi.fn(), push: vi.fn() }))
vi.mock("next/navigation", () => ({
  useRouter: () => navigation,
  usePathname: () => "/versions/2001",
}))

type MindmapProps = {
  onSelectCase: (caseId: string) => void
  onConfirmCase: (caseId: string) => void
  onConfirmScenario: (scenarioId: string) => void
  onConfirmRequirement: (requirementId: string) => void
}

type ChatProps = {
  onSelectConversation: (conversationId: string) => void
  onCreateConversation: () => void
}

const captured = vi.hoisted(() => ({
  mindmap: [] as MindmapProps[],
  chat: [] as ChatProps[],
}))

vi.mock("./DesignMindmap", async () => {
  const React = await import("react")
  return {
    DesignMindmap: (props: MindmapProps) => {
      captured.mindmap.push(props)
      return React.createElement("div", { "data-testid": "design-mindmap" })
    },
  }
})

vi.mock("./DesignChat", async () => {
  const React = await import("react")
  return {
    DesignChat: (props: ChatProps) => {
      captured.chat.push(props)
      return React.createElement("div", { "data-testid": "design-chat" })
    },
  }
})

import { versionFixture } from "@/features/versions/test-fixtures"

import { DesignTab } from "./DesignTab"
import { designGraphFixture, designOverviewFixture } from "./test-fixtures"

const TIMESTAMP = "2026-10-31T12:00:00Z"

function latest<T>(items: T[]): T {
  const item = items.at(-1)
  if (item === undefined) throw new Error("nothing captured")
  return item
}

describe("DesignTab mindmap stability", () => {
  beforeEach(() => {
    for (const fn of Object.values(designApi)) fn.mockReset()
    versionsApi.updateVersion.mockReset()
    navigation.replace.mockReset()
    captured.mindmap.length = 0
    captured.chat.length = 0

    designApi.getDesignGraph.mockResolvedValue(designGraphFixture)
    designApi.getDesignOverview.mockResolvedValue(designOverviewFixture)
    designApi.listDesignConversations.mockResolvedValue([
      {
        id: "4009",
        title: "用例设计 2026-10-31 12:00",
        kind: "design",
        status: "active",
        message_count: 2,
        last_message_at: TIMESTAMP,
        created_at: TIMESTAMP,
      },
    ])
    versionsApi.updateVersion.mockResolvedValue(versionFixture)
  })

  afterEach(cleanup)

  it("keeps the React Flow callbacks stable across a state-only re-render", async () => {
    render(<DesignTab versionId="2001" />)
    await screen.findByTestId("design-mindmap")

    const firstMindmap = latest(captured.mindmap)
    const firstChat = latest(captured.chat)

    // Opening the export dialog only changes local UI state; it must not churn
    // the callback identities that feed DesignMindmap's memoized nodes.
    fireEvent.click(screen.getByTestId("export-cases"))
    await screen.findByRole("dialog", { name: "导出用例" })

    const nextMindmap = latest(captured.mindmap)
    const nextChat = latest(captured.chat)
    expect(nextMindmap).not.toBe(firstMindmap)

    expect(nextMindmap.onSelectCase).toBe(firstMindmap.onSelectCase)
    expect(nextMindmap.onConfirmCase).toBe(firstMindmap.onConfirmCase)
    expect(nextMindmap.onConfirmScenario).toBe(firstMindmap.onConfirmScenario)
    expect(nextMindmap.onConfirmRequirement).toBe(firstMindmap.onConfirmRequirement)
    expect(nextChat.onSelectConversation).toBe(firstChat.onSelectConversation)
    expect(nextChat.onCreateConversation).toBe(firstChat.onCreateConversation)
  })
})
