import { cleanup, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const mocks = vi.hoisted(() => ({
  listGlobalIssues: vi.fn(),
  listVersionOptions: vi.fn(),
}))

vi.mock("@/features/delivery/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/features/delivery/api")>()
  return {
    ...actual,
    listGlobalIssues: mocks.listGlobalIssues,
    listVersionOptions: mocks.listVersionOptions,
  }
})

import IssuesPage from "./page"

const EMPTY_PAGE = { items: [], nextCursor: "", hasMore: false }

describe("/issues route", () => {
  beforeEach(() => {
    mocks.listVersionOptions.mockReset().mockResolvedValue(EMPTY_PAGE)
    mocks.listGlobalIssues.mockReset().mockResolvedValue(EMPTY_PAGE)
  })

  afterEach(() => {
    cleanup()
  })

  it("renders the real delivery GlobalIssuesPage, not a placeholder", async () => {
    render(<IssuesPage />)

    expect(await screen.findByRole("heading", { name: "问题中心" })).toBeTruthy()
    expect(screen.getByTestId("global-issues-page")).toBeTruthy()
    await waitFor(() => expect(mocks.listGlobalIssues).toHaveBeenCalledTimes(1))
  })
})
