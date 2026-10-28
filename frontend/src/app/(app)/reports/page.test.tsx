import { cleanup, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const mocks = vi.hoisted(() => ({
  listGlobalReports: vi.fn(),
  listVersionOptions: vi.fn(),
}))

vi.mock("@/features/delivery/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/features/delivery/api")>()
  return {
    ...actual,
    listGlobalReports: mocks.listGlobalReports,
    listVersionOptions: mocks.listVersionOptions,
  }
})

import ReportsPage from "./page"

const EMPTY_PAGE = { items: [], nextCursor: "", hasMore: false }

describe("/reports route", () => {
  beforeEach(() => {
    mocks.listVersionOptions.mockReset().mockResolvedValue(EMPTY_PAGE)
    mocks.listGlobalReports.mockReset().mockResolvedValue(EMPTY_PAGE)
  })

  afterEach(() => {
    cleanup()
  })

  it("renders the real delivery GlobalReportsPage, not a placeholder", async () => {
    render(<ReportsPage />)

    expect(await screen.findByRole("heading", { name: "报告中心" })).toBeTruthy()
    expect(screen.getByTestId("global-reports-page")).toBeTruthy()
    await waitFor(() => expect(mocks.listGlobalReports).toHaveBeenCalledTimes(1))
  })
})
