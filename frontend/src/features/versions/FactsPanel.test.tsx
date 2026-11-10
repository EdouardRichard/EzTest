import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

const api = vi.hoisted(() => ({
  getVersionFacts: vi.fn(),
  arbitrateFact: vi.fn(),
}))
vi.mock("./api", () => api)

import type { VersionFact } from "./api"
import { FactsPanel } from "./FactsPanel"

function fact(overrides: Partial<VersionFact>): VersionFact {
  return {
    id: "11",
    version_id: "2001",
    kind: "page",
    key: "page:env:1:/orders",
    payload: { title: "订单列表" },
    source: "explore",
    source_ref: "run:7",
    evidence_refs: ["5"],
    content_hash: "a".repeat(64),
    confidence: 0.9,
    superseded_by: null,
    created_at: "2026-11-01T08:00:00Z",
    ...overrides,
  }
}

const pageFact = fact({ id: "11", kind: "page", key: "page:env:1:/orders" })
const apiFact = fact({
  id: "12",
  kind: "api",
  key: "api:env:1:GET /orders",
  source: "code_scan",
  confidence: null,
})

describe("FactsPanel", () => {
  beforeEach(() => {
    api.getVersionFacts.mockReset()
    api.arbitrateFact.mockReset()
  })

  afterEach(cleanup)

  it("renders the version facts read from the API", async () => {
    api.getVersionFacts.mockResolvedValue([pageFact, apiFact])

    render(<FactsPanel versionId="2001" />)

    expect(await screen.findByText("page:env:1:/orders")).toBeInTheDocument()
    expect(screen.getByText("api:env:1:GET /orders")).toBeInTheDocument()
    expect(screen.getByText("page")).toBeInTheDocument()
    expect(screen.getByText("api")).toBeInTheDocument()
    expect(screen.getByText("code_scan", { exact: false })).toBeInTheDocument()
    expect(api.getVersionFacts).toHaveBeenCalledWith("2001", { includeSuperseded: true })
  })

  it("keeps the fact list internally scrollable instead of stretching the page", async () => {
    api.getVersionFacts.mockResolvedValue([pageFact, apiFact])

    render(<FactsPanel versionId="2001" />)

    const list = await screen.findByTestId("version-facts-list")
    expect(list).toHaveClass("max-h-[36rem]")
    expect(list).toHaveClass("overflow-y-auto")
  })

  it("shows an explicit empty state when the version has no facts", async () => {
    api.getVersionFacts.mockResolvedValue([])

    render(<FactsPanel versionId="2001" />)

    expect(await screen.findByText("该版本暂无事实")).toBeInTheDocument()
  })

  it("shows a loading state while the fact read is pending", () => {
    api.getVersionFacts.mockImplementation(() => new Promise<VersionFact[]>(() => undefined))

    render(<FactsPanel versionId="2001" />)

    expect(screen.getByText("正在加载版本事实…")).toBeInTheDocument()
  })

  it("shows the load error and retries the fact read", async () => {
    api.getVersionFacts
      .mockRejectedValueOnce(
        new ApiError({
          status: 503,
          code: "unavailable",
          message: "事实读取失败",
          requestId: "req_facts",
        }),
      )
      .mockResolvedValueOnce([pageFact])

    render(<FactsPanel versionId="2001" />)

    expect(await screen.findByText("事实读取失败")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))

    expect(await screen.findByText("page:env:1:/orders")).toBeInTheDocument()
    expect(api.getVersionFacts).toHaveBeenCalledTimes(2)
  })

  it("opens an arbitration card for a conflicting fact group", async () => {
    api.getVersionFacts.mockResolvedValue([
      fact({ id: "21", kind: "api", key: "api:env:1:GET /orders", confidence: 0.4 }),
      fact({ id: "22", kind: "api", key: "api:env:1:GET /orders", confidence: 0.9 }),
    ])

    render(<FactsPanel versionId="2001" initialFactId="21" />)

    expect(await screen.findByText("冲突事实仲裁")).toBeInTheDocument()
    expect(screen.getByText(/2 条并存事实/)).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "保留此条" })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "丢弃此条" })).toBeInTheDocument()
  })

  it("requests superseded rows and renders their superseded badge", async () => {
    api.getVersionFacts.mockResolvedValue([
      fact({ id: "31", kind: "page", key: "page:env:1:/orders" }),
      fact({ id: "32", kind: "page", key: "page:env:1:/orders", superseded_by: "31" }),
    ])

    render(<FactsPanel versionId="2001" />)

    expect(await screen.findByText("已被 #31 替代")).toBeInTheDocument()
    expect(api.getVersionFacts).toHaveBeenCalledWith("2001", { includeSuperseded: true })
  })

  it("shows the read-only reason when the deep-linked fact is superseded", async () => {
    api.getVersionFacts.mockResolvedValue([
      fact({ id: "31", kind: "page", key: "page:env:1:/orders" }),
      fact({ id: "32", kind: "page", key: "page:env:1:/orders", superseded_by: "31" }),
    ])

    render(<FactsPanel versionId="2001" initialFactId="32" />)

    expect(await screen.findByText("该事实已被 #31 替代，无需仲裁。")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "保留此条" })).not.toBeInTheDocument()
  })
})
