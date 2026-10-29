import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { ApiError } from "@/lib/api-client"

import type { GlobalReport } from "../types"

const mocks = vi.hoisted(() => ({
  listGlobalReports: vi.fn(),
  listVersionOptions: vi.fn(),
}))

vi.mock("../api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../api")>()
  return {
    ...actual,
    listGlobalReports: mocks.listGlobalReports,
    listVersionOptions: mocks.listVersionOptions,
  }
})

import { GlobalReportsPage } from "./GlobalReportsPage"

const VERSION_1 = "1844674407370955161"
const VERSION_2 = "1844674407370955162"
const REPORT_ID = "1844674407370955163"

function globalReport(overrides: Partial<GlobalReport> = {}): GlobalReport {
  return {
    id: REPORT_ID,
    versionId: VERSION_1,
    batchId: "1844674407370955164",
    revision: 2,
    status: "stale",
    title: "批次报告",
    inputFingerprint: "a".repeat(64),
    markdown: { sha256: "b".repeat(64), sizeBytes: 2048, downloadPath: "/api/x" },
    staleReason: "批次结果已更新",
    publishedAt: "2026-10-28T01:00:00Z",
    createdAt: "2026-10-28T00:00:00Z",
    updatedAt: "2026-10-28T01:00:00Z",
    versionName: "版本 A",
    batchNo: 2,
    deepLink: { versionId: VERSION_1, tab: "report", reportId: REPORT_ID },
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

describe("GlobalReportsPage", () => {
  beforeEach(() => {
    mocks.listGlobalReports.mockReset()
    mocks.listVersionOptions.mockReset()
    mocks.listVersionOptions.mockResolvedValue(VERSION_OPTIONS)
    mocks.listGlobalReports.mockResolvedValue(page([globalReport()]))
  })

  afterEach(() => {
    cleanup()
  })

  it("lists cross-version reports and deep links to the version report tab", async () => {
    render(<GlobalReportsPage />)

    const row = await screen.findByTestId(`global-report-row-${REPORT_ID}`)
    expect(within(row).getByText("版本 A")).toBeTruthy()
    expect(within(row).getByText("Batch #2")).toBeTruthy()
    expect(within(row).getByText("revision 2")).toBeTruthy()
    expect(within(row).getByText("已过期")).toBeTruthy()

    const link = within(row).getByRole("link")
    expect(link.getAttribute("href")).toBe(`/versions/${VERSION_1}?tab=report&report=${REPORT_ID}`)
    expect(screen.getByTestId("global-reports-page")).toBeTruthy()
  })

  it("filters by version through the version option selector", async () => {
    render(<GlobalReportsPage />)
    await screen.findByTestId(`global-report-row-${REPORT_ID}`)

    fireEvent.change(screen.getByTestId("global-reports-version-filter"), {
      target: { value: VERSION_2 },
    })

    await waitFor(() =>
      expect(mocks.listGlobalReports).toHaveBeenLastCalledWith(
        expect.objectContaining({ versionId: VERSION_2 }),
        expect.anything(),
      ),
    )
  })

  it("renders the empty and error states", async () => {
    mocks.listGlobalReports.mockResolvedValueOnce(page([]))
    const { unmount } = render(<GlobalReportsPage />)
    expect(await screen.findByText("暂无跨版本报告")).toBeTruthy()
    unmount()

    mocks.listGlobalReports.mockRejectedValueOnce(
      new ApiError({ status: 403, code: "forbidden", message: "forbidden" }),
    )
    render(<GlobalReportsPage />)
    const alert = await screen.findByTestId("global-reports-error")
    expect(alert.textContent).toContain("无权限")
  })
})
