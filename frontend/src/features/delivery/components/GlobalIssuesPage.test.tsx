import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { ApiError } from "@/lib/api-client"

import type { GlobalIssue } from "../types"

const mocks = vi.hoisted(() => ({
  listGlobalIssues: vi.fn(),
  listVersionOptions: vi.fn(),
}))

vi.mock("../api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../api")>()
  return {
    ...actual,
    listGlobalIssues: mocks.listGlobalIssues,
    listVersionOptions: mocks.listVersionOptions,
  }
})

import { GlobalIssuesPage } from "./GlobalIssuesPage"

const VERSION_1 = "1844674407370955161"
const VERSION_2 = "1844674407370955162"
const ISSUE_ID = "1844674407370955163"

function globalIssue(overrides: Partial<GlobalIssue> = {}): GlobalIssue {
  return {
    id: ISSUE_ID,
    versionId: VERSION_1,
    batchId: "1844674407370955164",
    caseRunId: null,
    caseNodeId: null,
    stepResultId: null,
    source: "ai",
    category: "defect",
    severity: "blocker",
    status: "open",
    confidence: "high",
    failureClass: "application",
    title: "跨版本问题",
    rerunSuggestion: "",
    recurrenceCount: 1,
    revision: 2,
    refs: [],
    codeRefs: [],
    dismissedReason: "",
    createdBy: "1",
    confirmedBy: null,
    confirmedAt: null,
    dismissedBy: null,
    dismissedAt: null,
    createdAt: "2026-10-28T00:00:00Z",
    updatedAt: "2026-10-28T00:00:00Z",
    versionName: "版本 A",
    deepLink: { versionId: VERSION_1, tab: "issues", issueId: ISSUE_ID },
    ...overrides,
  }
}

const VERSION_OPTIONS = {
  items: [
    { id: VERSION_1, name: "版本 A", label: "v1", status: "completed" },
    { id: VERSION_2, name: "版本 B", label: "v2", status: "executing" },
  ],
  nextCursor: "",
  hasMore: false,
}

function page<T>(items: T[]) {
  return { items, nextCursor: "", hasMore: false }
}

describe("GlobalIssuesPage", () => {
  beforeEach(() => {
    mocks.listGlobalIssues.mockReset()
    mocks.listVersionOptions.mockReset()
    mocks.listVersionOptions.mockResolvedValue(VERSION_OPTIONS)
    mocks.listGlobalIssues.mockResolvedValue(page([globalIssue()]))
  })

  afterEach(() => {
    cleanup()
  })

  it("lists cross-version issues and deep links to the version issue tab", async () => {
    render(<GlobalIssuesPage />)

    const row = await screen.findByTestId(`global-issue-row-${ISSUE_ID}`)
    expect(within(row).getByText("跨版本问题")).toBeTruthy()
    expect(within(row).getByText("版本 A")).toBeTruthy()
    expect(within(row).getByText("阻断")).toBeTruthy()

    const link = within(row).getByRole("link")
    expect(link.getAttribute("href")).toBe(`/versions/${VERSION_1}?tab=issues&issue=${ISSUE_ID}`)
    expect(screen.getByTestId("global-issues-page")).toBeTruthy()
  })

  it("filters by version through the version option selector", async () => {
    render(<GlobalIssuesPage />)
    await screen.findByTestId(`global-issue-row-${ISSUE_ID}`)

    fireEvent.change(screen.getByTestId("global-issues-version-filter"), {
      target: { value: VERSION_2 },
    })

    await waitFor(() =>
      expect(mocks.listGlobalIssues).toHaveBeenLastCalledWith(
        expect.objectContaining({ versionId: VERSION_2 }),
        expect.anything(),
      ),
    )
  })

  it("lets the caller intercept the deep link", async () => {
    const onOpenIssue = vi.fn()
    render(<GlobalIssuesPage onOpenIssue={onOpenIssue} />)
    const row = await screen.findByTestId(`global-issue-row-${ISSUE_ID}`)
    fireEvent.click(within(row).getByRole("link"))
    expect(onOpenIssue).toHaveBeenCalledWith(globalIssue().deepLink)
  })

  it("renders the empty and error states", async () => {
    mocks.listGlobalIssues.mockResolvedValueOnce(page([]))
    const { unmount } = render(<GlobalIssuesPage />)
    expect(await screen.findByText("暂无跨版本问题")).toBeTruthy()
    unmount()

    mocks.listGlobalIssues.mockRejectedValueOnce(
      new ApiError({ status: 403, code: "forbidden", message: "forbidden" }),
    )
    render(<GlobalIssuesPage />)
    const alert = await screen.findByTestId("global-issues-error")
    expect(alert.textContent).toContain("无权限")
  })
})
