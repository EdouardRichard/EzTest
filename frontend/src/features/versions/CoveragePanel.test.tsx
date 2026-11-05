import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

const versionApi = vi.hoisted(() => ({ getDiscoveryCoverage: vi.fn() }))
vi.mock(import("./api"), async (importOriginal) => {
  const actual = await importOriginal<typeof import("./api")>()
  return { ...actual, ...versionApi }
})

import { CoveragePanel } from "./CoveragePanel"
import type { DiscoveryCoverage } from "./types"

function coverage(overrides: Partial<DiscoveryCoverage> = {}): DiscoveryCoverage {
  return {
    pages: {
      observed: 3,
      proposed: 2,
      approved: 1,
      rejected: 0,
      out_of_scope: 0,
      uncovered: ["/login", "/orders/{id}"],
    },
    apis: {
      observed: 2,
      proposed: 1,
      approved: 1,
      rejected: 0,
      out_of_scope: 0,
      uncovered: ["POST /api/orders", "GET /api/orders"],
    },
    ...overrides,
  }
}

describe("CoveragePanel", () => {
  beforeEach(() => {
    versionApi.getDiscoveryCoverage.mockReset()
  })

  afterEach(cleanup)

  it("renders the pages/apis observed counts, decisions and uncovered identities", async () => {
    versionApi.getDiscoveryCoverage.mockResolvedValue(coverage())

    render(<CoveragePanel versionId="2001" />)

    expect(await screen.findByTestId("coverage-pages")).toHaveTextContent("3")
    expect(screen.getByTestId("coverage-apis")).toHaveTextContent("2")
    // Four decision buckets per kind.
    expect(screen.getAllByText("待处理")).toHaveLength(2)
    expect(screen.getAllByText("已批准")).toHaveLength(2)
    expect(screen.getAllByText("已拒绝")).toHaveLength(2)
    expect(screen.getAllByText("范围外")).toHaveLength(2)
    expect(screen.getByTestId("coverage-pages-uncovered")).toHaveTextContent("/orders/{id}")
    expect(screen.getByTestId("coverage-pages-uncovered")).toHaveTextContent("/login")
    // API identities keep the METHOD so GET and POST are distinct resources.
    const apiUncovered = screen.getByTestId("coverage-apis-uncovered")
    expect(apiUncovered).toHaveTextContent("POST /api/orders")
    expect(apiUncovered).toHaveTextContent("GET /api/orders")
    expect(versionApi.getDiscoveryCoverage).toHaveBeenCalledWith("2001")
  })

  it("describes the decision tally as four decisions plus observed", async () => {
    versionApi.getDiscoveryCoverage.mockResolvedValue(coverage())

    render(<CoveragePanel versionId="2001" />)

    expect(await screen.findByTestId("coverage-pages")).toBeInTheDocument()
    // The API returns four decision counts + observed; there is no fifth bucket.
    expect(screen.getByText(/四类决策 \+ observed/)).toBeInTheDocument()
    expect(screen.queryByText(/五类决策/)).not.toBeInTheDocument()
  })

  it("shows an explicit empty state when nothing was discovered", async () => {
    versionApi.getDiscoveryCoverage.mockResolvedValue(
      coverage({
        pages: {
          observed: 0,
          proposed: 0,
          approved: 0,
          rejected: 0,
          out_of_scope: 0,
          uncovered: [],
        },
        apis: {
          observed: 0,
          proposed: 0,
          approved: 0,
          rejected: 0,
          out_of_scope: 0,
          uncovered: [],
        },
      }),
    )

    render(<CoveragePanel versionId="2001" />)

    expect(await screen.findByText("该版本暂无探索覆盖")).toBeInTheDocument()
  })

  it("reloads on refreshKey", async () => {
    versionApi.getDiscoveryCoverage.mockResolvedValue(coverage())
    const { rerender } = render(<CoveragePanel versionId="2001" />)
    await screen.findByTestId("coverage-pages")

    rerender(<CoveragePanel versionId="2001" refreshKey={2} />)

    await waitFor(() => expect(versionApi.getDiscoveryCoverage).toHaveBeenCalledTimes(2))
  })

  it("shows the read error and retries", async () => {
    versionApi.getDiscoveryCoverage
      .mockRejectedValueOnce(
        new ApiError({
          status: 503,
          code: "unavailable",
          message: "覆盖矩阵读取失败",
          requestId: "req_coverage",
        }),
      )
      .mockResolvedValueOnce(coverage())

    render(<CoveragePanel versionId="2001" />)

    expect(await screen.findByText("覆盖矩阵读取失败")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(await screen.findByTestId("coverage-pages")).toBeInTheDocument()
  })
})
