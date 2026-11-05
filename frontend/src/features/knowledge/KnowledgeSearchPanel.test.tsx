import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

const api = vi.hoisted(() => ({
  searchKnowledge: vi.fn(),
}))
vi.mock("./api", () => api)

import type { ApiPage } from "@/lib/api-client"

import type { KnowledgeSearchResult } from "./api"
import { KnowledgeSearchPanel } from "./KnowledgeSearchPanel"

function result(overrides: Partial<KnowledgeSearchResult>): KnowledgeSearchResult {
  return {
    id: "7002",
    scope: "global",
    system_key: null,
    title: "所有接口必须带 request id",
    content: "请求头必须携带 X-Request-ID。",
    tags: ["接口"],
    status: "approved",
    confidence: 0.92,
    source_version_id: null,
    promoted_from_version_id: null,
    revision: 2,
    review_reason: null,
    reviewed_by: "9001",
    reviewed_at: "2026-11-01T06:00:00Z",
    revoked_at: null,
    source_stale: false,
    created_by: "9001",
    created_at: "2026-11-01T02:00:00Z",
    updated_at: "2026-11-01T06:00:00Z",
    last_verified_at: "2026-11-01T06:00:00Z",
    bindings: {
      system_id: "10",
      environment_id: null,
      page_key: "/orders",
      api_key: "GET /orders",
      account_role: null,
    },
    ...overrides,
  }
}

function page(
  items: KnowledgeSearchResult[],
  nextCursor = "",
  hasMore = false,
): ApiPage<KnowledgeSearchResult> {
  return { items, nextCursor, hasMore }
}

describe("KnowledgeSearchPanel", () => {
  beforeEach(() => {
    api.searchKnowledge.mockReset()
  })

  afterEach(cleanup)

  it("submits the keyword and renders the matched knowledge", async () => {
    api.searchKnowledge.mockResolvedValue(page([result({})]))

    render(<KnowledgeSearchPanel />)

    fireEvent.change(screen.getByLabelText("检索关键词"), { target: { value: " 退款 " } })
    fireEvent.click(screen.getByRole("button", { name: "检索" }))

    expect(await screen.findByText("所有接口必须带 request id")).toBeInTheDocument()
    expect(screen.getByText("接口")).toBeInTheDocument()
    expect(screen.getByTestId("knowledge-search-scope-7002")).toHaveTextContent("全局")
    expect(screen.getByTestId("knowledge-search-binding-7002")).toHaveTextContent("GET /orders")
    expect(api.searchKnowledge).toHaveBeenCalledWith(
      expect.objectContaining({ q: "退款", status: "approved" }),
    )
  })

  it("shows an explicit empty state when nothing matches", async () => {
    api.searchKnowledge.mockResolvedValue(page([]))

    render(<KnowledgeSearchPanel />)
    fireEvent.change(screen.getByLabelText("检索关键词"), { target: { value: "不存在" } })
    fireEvent.click(screen.getByRole("button", { name: "检索" }))

    expect(await screen.findByText("没有匹配的知识")).toBeInTheDocument()
  })

  it("shows the search error and retries the same keyword", async () => {
    api.searchKnowledge
      .mockRejectedValueOnce(new Error("检索服务不可用"))
      .mockResolvedValueOnce(page([result({})]))

    render(<KnowledgeSearchPanel />)
    fireEvent.change(screen.getByLabelText("检索关键词"), { target: { value: "退款" } })
    fireEvent.click(screen.getByRole("button", { name: "检索" }))

    expect(await screen.findByText("检索服务不可用")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))

    expect(await screen.findByText("所有接口必须带 request id")).toBeInTheDocument()
    expect(api.searchKnowledge).toHaveBeenLastCalledWith(expect.objectContaining({ q: "退款" }))
  })
})
