import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { KnowledgeEntry } from "./types"

const mocks = vi.hoisted(() => ({
  listGlobalKnowledge: vi.fn(),
  reviewKnowledge: vi.fn(),
}))

vi.mock("./api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./api")>()
  return {
    ...actual,
    listGlobalKnowledge: mocks.listGlobalKnowledge,
    reviewKnowledge: mocks.reviewKnowledge,
  }
})

import { ReviewDrawer } from "./ReviewDrawer"

function entry(overrides: Partial<KnowledgeEntry> = {}): KnowledgeEntry {
  return {
    id: "6001",
    versionId: "100",
    versionName: "订单系统 2.0",
    kind: "pitfall",
    title: "支付超时必须用假时钟",
    body: "沙箱网关 30s 超时。",
    tags: ["payment"],
    sourceRefs: [{ kind: "requirement", id: "200" }],
    status: "draft",
    confidence: "high",
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
  mocks.listGlobalKnowledge.mockReset()
  mocks.reviewKnowledge.mockReset()
})

afterEach(cleanup)

describe("ReviewDrawer", () => {
  it("loads the draft queue, shows the detail and approves with the revision", async () => {
    mocks.listGlobalKnowledge.mockResolvedValue(
      page([entry(), entry({ id: "6002", title: "另一条草稿" })]),
    )
    mocks.reviewKnowledge.mockResolvedValue(entry({ status: "approved", revision: 2 }))
    const onReviewed = vi.fn()

    render(<ReviewDrawer open focusId="" onClose={vi.fn()} onReviewed={onReviewed} />)

    const drawer = await screen.findByTestId("knowledge-review-drawer")
    expect(
      within(within(drawer).getByTestId("knowledge-review-item-6001")).getByText(
        "支付超时必须用假时钟",
      ),
    ).toBeTruthy()
    expect(mocks.listGlobalKnowledge).toHaveBeenCalledWith({ status: "draft", limit: 50 })

    fireEvent.click(screen.getByTestId("knowledge-review-approve"))

    await waitFor(() =>
      expect(mocks.reviewKnowledge).toHaveBeenCalledWith("100", "6001", {
        revision: 1,
        status: "approved",
      }),
    )
    expect(await screen.findByTestId("knowledge-review-success")).toBeTruthy()
    expect(onReviewed).toHaveBeenCalledTimes(1)
    expect(screen.queryByTestId("knowledge-review-item-6001")).toBeNull()
  })

  it("requires a non-empty reason before rejecting", async () => {
    mocks.listGlobalKnowledge.mockResolvedValue(page([entry()]))
    mocks.reviewKnowledge.mockResolvedValue(entry({ status: "rejected", revision: 2 }))

    render(<ReviewDrawer open focusId="" onClose={vi.fn()} />)
    await screen.findByTestId("knowledge-review-drawer")

    fireEvent.click(screen.getByTestId("knowledge-review-reject"))
    expect(screen.getByTestId("knowledge-review-reason-error").textContent).toContain(
      "驳回必须填写原因",
    )
    expect(mocks.reviewKnowledge).not.toHaveBeenCalled()

    fireEvent.change(screen.getByTestId("knowledge-review-reason"), {
      target: { value: "与决策 D12 重复" },
    })
    fireEvent.click(screen.getByTestId("knowledge-review-reject"))

    await waitFor(() =>
      expect(mocks.reviewKnowledge).toHaveBeenCalledWith("100", "6001", {
        revision: 1,
        status: "rejected",
        reason: "与决策 D12 重复",
      }),
    )
  })

  it("shows the empty state, the load error with retry, and the review error", async () => {
    mocks.listGlobalKnowledge.mockResolvedValueOnce(page([]))
    render(<ReviewDrawer open focusId="" onClose={vi.fn()} />)
    expect(await screen.findByText("暂无待审核知识")).toBeTruthy()
    cleanup()

    mocks.listGlobalKnowledge
      .mockRejectedValueOnce(new Error("数据库不可用"))
      .mockResolvedValueOnce(page([entry()]))
    render(<ReviewDrawer open focusId="" onClose={vi.fn()} />)
    const error = await screen.findByTestId("knowledge-review-load-error")
    expect(error.textContent).toContain("数据库不可用")
    fireEvent.click(within(error).getByRole("button", { name: "重试" }))
    expect(await screen.findByTestId("knowledge-review-item-6001")).toBeTruthy()
    cleanup()

    mocks.listGlobalKnowledge.mockResolvedValue(page([entry()]))
    mocks.reviewKnowledge.mockRejectedValue(
      Object.assign(new Error("版本已变化"), { name: "ApiError", status: 409 }),
    )
    render(<ReviewDrawer open focusId="" onClose={vi.fn()} />)
    await screen.findByTestId("knowledge-review-drawer")
    fireEvent.click(screen.getByTestId("knowledge-review-approve"))
    expect(await screen.findByTestId("knowledge-review-submit-error")).toBeTruthy()
  })

  it("closes through the close button", () => {
    mocks.listGlobalKnowledge.mockResolvedValue(page([]))
    const onClose = vi.fn()
    render(<ReviewDrawer open focusId="" onClose={onClose} />)
    fireEvent.click(screen.getByRole("button", { name: "关闭审核抽屉" }))
    expect(onClose).toHaveBeenCalledTimes(1)
  })
})
