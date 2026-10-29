import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { KnowledgeEntry } from "./types"

const mocks = vi.hoisted(() => ({
  listVersionKnowledge: vi.fn(),
  createKnowledge: vi.fn(),
}))

vi.mock("./api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./api")>()
  return {
    ...actual,
    listVersionKnowledge: mocks.listVersionKnowledge,
    createKnowledge: mocks.createKnowledge,
  }
})

import { KnowledgePanel } from "./KnowledgePanel"

function entry(overrides: Partial<KnowledgeEntry> = {}): KnowledgeEntry {
  return {
    id: "6001",
    versionId: "100",
    versionName: "订单系统 2.0",
    kind: "pattern",
    title: "下单幂等键",
    body: "重复提交使用同一幂等键。",
    tags: ["order"],
    sourceRefs: [],
    status: "approved",
    confidence: "medium",
    createdBy: 42,
    reviewedBy: null,
    reviewNote: "",
    revision: 1,
    createdAt: "2026-10-29T10:00:00Z",
    updatedAt: "2026-10-29T10:00:00Z",
    ...overrides,
  }
}

function page(items: KnowledgeEntry[]) {
  return { items, nextCursor: "", hasMore: false }
}

beforeEach(() => {
  mocks.listVersionKnowledge.mockReset()
  mocks.createKnowledge.mockReset()
})

afterEach(cleanup)

describe("KnowledgePanel", () => {
  it("lists the approved knowledge of the version with the global deep link", async () => {
    mocks.listVersionKnowledge.mockResolvedValue(page([entry()]))

    render(<KnowledgePanel versionId="100" />)

    const panel = await screen.findByTestId("knowledge-panel")
    expect(within(panel).getByText("下单幂等键")).toBeTruthy()
    expect(mocks.listVersionKnowledge).toHaveBeenCalledWith("100", {
      status: "approved",
      limit: 50,
    })
    const link = within(panel).getByRole("link", { name: "查看知识库" })
    expect(link.getAttribute("href")).toBe("/knowledge?version=100")
  })

  it("shows the empty state and a retryable load error", async () => {
    mocks.listVersionKnowledge.mockResolvedValueOnce(page([]))
    render(<KnowledgePanel versionId="100" />)
    expect(await screen.findByText("本版本暂无已批准知识。")).toBeTruthy()
    cleanup()

    mocks.listVersionKnowledge
      .mockRejectedValueOnce(new Error("网络不可用"))
      .mockResolvedValueOnce(page([entry()]))
    render(<KnowledgePanel versionId="100" />)
    const error = await screen.findByTestId("knowledge-panel-error")
    expect(error.textContent).toContain("网络不可用")
    fireEvent.click(within(error).getByRole("button", { name: "重试" }))
    expect(await screen.findByText("下单幂等键")).toBeTruthy()
  })

  it("creates a draft from the version panel", async () => {
    mocks.listVersionKnowledge.mockResolvedValue(page([]))
    mocks.createKnowledge.mockResolvedValue(
      entry({ id: "6002", status: "draft", title: "新知识", revision: 1 }),
    )

    render(<KnowledgePanel versionId="100" />)
    await screen.findByText("本版本暂无已批准知识。")

    fireEvent.click(screen.getByTestId("knowledge-create-toggle"))
    fireEvent.change(screen.getByTestId("knowledge-title"), { target: { value: "新知识" } })
    fireEvent.change(screen.getByTestId("knowledge-body"), { target: { value: "记录一条经验" } })
    fireEvent.change(screen.getByTestId("knowledge-kind"), { target: { value: "pitfall" } })
    fireEvent.change(screen.getByTestId("knowledge-confidence"), { target: { value: "high" } })
    fireEvent.click(screen.getByTestId("knowledge-submit"))

    await waitFor(() =>
      expect(mocks.createKnowledge).toHaveBeenCalledWith("100", {
        kind: "pitfall",
        title: "新知识",
        body: "记录一条经验",
        confidence: "high",
      }),
    )
    expect(await screen.findByTestId("knowledge-submit-success")).toBeTruthy()
    // The draft is listed with its pending-review badge, not as approved.
    expect(await screen.findByText("新知识")).toBeTruthy()
    expect(screen.getByText("待审核")).toBeTruthy()
  })

  it("validates the create form and shows a submit error", async () => {
    mocks.listVersionKnowledge.mockResolvedValue(page([]))
    render(<KnowledgePanel versionId="100" />)
    await screen.findByText("本版本暂无已批准知识。")

    fireEvent.click(screen.getByTestId("knowledge-create-toggle"))
    fireEvent.click(screen.getByTestId("knowledge-submit"))
    expect(screen.getByTestId("knowledge-form-error").textContent).toContain("标题和正文必填")
    expect(mocks.createKnowledge).not.toHaveBeenCalled()

    mocks.createKnowledge.mockRejectedValue(new Error("服务器错误"))
    fireEvent.change(screen.getByTestId("knowledge-title"), { target: { value: "标题" } })
    fireEvent.change(screen.getByTestId("knowledge-body"), { target: { value: "正文" } })
    fireEvent.click(screen.getByTestId("knowledge-submit"))
    expect(await screen.findByTestId("knowledge-submit-error")).toBeTruthy()
  })
})
