import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { KnowledgeEntry } from "./types"

const mocks = vi.hoisted(() => ({
  listGlobalKnowledge: vi.fn(),
  reviewKnowledge: vi.fn(),
  useAuth: vi.fn(),
}))

vi.mock("@/features/auth/AuthProvider", () => ({
  useAuth: mocks.useAuth,
}))

vi.mock("./api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./api")>()
  return {
    ...actual,
    listGlobalKnowledge: mocks.listGlobalKnowledge,
    reviewKnowledge: mocks.reviewKnowledge,
  }
})

import { KnowledgePage } from "./KnowledgePage"

function adminAuth() {
  return {
    user: { id: 1, username: "admin", role: "admin", status: "active" },
    loading: false,
    login: vi.fn(),
    logout: vi.fn(),
  }
}

function userAuth() {
  return {
    user: { id: 2, username: "qa01", role: "user", status: "active" },
    loading: false,
    login: vi.fn(),
    logout: vi.fn(),
  }
}

function entry(overrides: Partial<KnowledgeEntry> = {}): KnowledgeEntry {
  return {
    id: "6001",
    versionId: "100",
    versionName: "订单系统 2.0",
    kind: "decision",
    title: "支付重试三次后失败",
    body: "重试三次仍失败时标记用例失败。",
    tags: ["payment"],
    sourceRefs: [{ kind: "requirement", id: "200" }],
    status: "approved",
    confidence: "high",
    createdBy: 42,
    reviewedBy: 43,
    reviewNote: "确认符合沙箱行为",
    revision: 2,
    createdAt: "2026-10-29T10:00:00Z",
    updatedAt: "2026-10-29T11:00:00Z",
    ...overrides,
  }
}

function page(items: KnowledgeEntry[]) {
  return { items, nextCursor: "", hasMore: false }
}

beforeEach(() => {
  mocks.listGlobalKnowledge.mockReset()
  mocks.reviewKnowledge.mockReset()
  mocks.useAuth.mockReset()
  mocks.useAuth.mockReturnValue(userAuth())
  window.history.pushState({}, "", "/knowledge")
})

afterEach(cleanup)

describe("KnowledgePage", () => {
  it("lists the approved knowledge and renders the selected detail", async () => {
    const second = entry({
      id: "6002",
      title: "幂等键复用",
      tags: [],
      sourceRefs: [],
      reviewNote: "",
    })
    mocks.listGlobalKnowledge.mockResolvedValue(page([entry(), second]))

    render(<KnowledgePage />)

    const pageEl = await screen.findByTestId("knowledge-page")
    expect(within(pageEl).getAllByText("支付重试三次后失败").length).toBeGreaterThan(0)
    expect(mocks.listGlobalKnowledge).toHaveBeenCalledWith({
      status: "approved",
      kind: undefined,
      versionId: undefined,
      limit: 50,
    })

    fireEvent.click(screen.getByTestId("knowledge-list-item-6001"))
    const detail = await screen.findByTestId("knowledge-detail")
    expect(within(detail).getByText("重试三次仍失败时标记用例失败。")).toBeTruthy()
    expect(within(detail).getByText("已批准")).toBeTruthy()
    expect(within(detail).getByText(/确认符合沙箱行为/)).toBeTruthy()
    const versionLink = within(detail).getByRole("link", { name: "在版本总览中查看" })
    expect(versionLink.getAttribute("href")).toBe("/versions/100?tab=overview")

    fireEvent.click(screen.getByTestId("knowledge-list-item-6002"))
    const secondDetail = await screen.findByTestId("knowledge-detail")
    expect(within(secondDetail).getByText("幂等键复用")).toBeTruthy()
  })

  it("shows loading, then the empty state", async () => {
    let resolve: (value: ReturnType<typeof page>) => void = () => {}
    mocks.listGlobalKnowledge.mockReturnValue(
      new Promise((done) => {
        resolve = done
      }),
    )
    render(<KnowledgePage />)
    expect(screen.getByTestId("knowledge-loading")).toBeTruthy()
    resolve(page([]))
    expect(await screen.findByText("暂无已批准知识。")).toBeTruthy()
  })

  it("shows the error with a retry and recovers", async () => {
    mocks.listGlobalKnowledge
      .mockRejectedValueOnce(new Error("服务器错误"))
      .mockResolvedValueOnce(page([entry()]))
    render(<KnowledgePage />)

    const error = await screen.findByTestId("knowledge-error")
    expect(error.textContent).toContain("服务器错误")
    fireEvent.click(within(error).getByRole("button", { name: "重试" }))
    expect(await screen.findByTestId("knowledge-list-item-6001")).toBeTruthy()
  })

  it("filters by kind and version for every user, and by status for admins", async () => {
    mocks.listGlobalKnowledge.mockResolvedValue(page([entry()]))
    render(<KnowledgePage />)
    await screen.findByTestId("knowledge-page")

    fireEvent.change(screen.getByTestId("knowledge-kind-filter"), { target: { value: "pitfall" } })
    await waitFor(() =>
      expect(mocks.listGlobalKnowledge).toHaveBeenLastCalledWith({
        status: "approved",
        kind: "pitfall",
        versionId: undefined,
        limit: 50,
      }),
    )
    // The status filter is administrator-only.
    expect(screen.queryByTestId("knowledge-status-filter")).toBeNull()

    mocks.useAuth.mockReturnValue(adminAuth())
    cleanup()
    mocks.listGlobalKnowledge.mockClear()
    mocks.listGlobalKnowledge.mockResolvedValue(page([entry()]))
    render(<KnowledgePage />)
    await screen.findByTestId("knowledge-page")

    fireEvent.change(screen.getByTestId("knowledge-status-filter"), { target: { value: "draft" } })
    await waitFor(() =>
      expect(mocks.listGlobalKnowledge).toHaveBeenLastCalledWith({
        status: "draft",
        kind: undefined,
        versionId: undefined,
        limit: 50,
      }),
    )
  })

  it("applies the version deep link as a filter", async () => {
    window.history.pushState({}, "", "/knowledge?version=100")
    mocks.listGlobalKnowledge.mockResolvedValue(page([entry()]))
    render(<KnowledgePage />)
    await screen.findByTestId("knowledge-page")
    expect(mocks.listGlobalKnowledge).toHaveBeenCalledWith({
      status: "approved",
      kind: undefined,
      versionId: "100",
      limit: 50,
    })
    const versionInput = screen.getByTestId("knowledge-version-filter") as HTMLInputElement
    expect(versionInput.value).toBe("100")
  })

  it("opens the admin review drawer from the queue button and refreshes after approval", async () => {
    mocks.useAuth.mockReturnValue(adminAuth())
    const draft = entry({ id: "6001", status: "draft", reviewedBy: null, reviewNote: "" })
    const approved = entry({ id: "6001", status: "approved" })
    mocks.listGlobalKnowledge.mockImplementation((query: { status?: string }) =>
      Promise.resolve(query.status === "draft" ? page([draft]) : page([approved])),
    )
    mocks.reviewKnowledge.mockResolvedValue(approved)

    render(<KnowledgePage />)
    await screen.findByTestId("knowledge-page")
    fireEvent.click(screen.getByTestId("knowledge-open-review"))

    const drawer = await screen.findByTestId("knowledge-review-drawer")
    fireEvent.click(within(drawer).getByTestId("knowledge-review-approve"))
    await waitFor(() => expect(mocks.reviewKnowledge).toHaveBeenCalled())
    await waitFor(() =>
      expect(mocks.listGlobalKnowledge).toHaveBeenLastCalledWith({
        status: "approved",
        kind: undefined,
        versionId: undefined,
        limit: 50,
      }),
    )
  })

  it("opens the review drawer from the ?review= deep link for admins", async () => {
    window.history.pushState({}, "", "/knowledge?review=6001")
    mocks.useAuth.mockReturnValue(adminAuth())
    mocks.listGlobalKnowledge.mockImplementation((query: { status?: string }) =>
      Promise.resolve(query.status === "draft" ? page([entry({ status: "draft" })]) : page([])),
    )

    render(<KnowledgePage />)
    expect(await screen.findByTestId("knowledge-review-drawer")).toBeTruthy()
    expect(screen.getByTestId("knowledge-review-item-6001")).toBeTruthy()
  })

  it("opens the review drawer once the async auth resolves", async () => {
    window.history.pushState({}, "", "/knowledge?review=6001")
    mocks.useAuth.mockReturnValue({
      user: null,
      loading: true,
      login: vi.fn(),
      logout: vi.fn(),
    })
    mocks.listGlobalKnowledge.mockImplementation((query: { status?: string }) =>
      Promise.resolve(query.status === "draft" ? page([entry({ status: "draft" })]) : page([])),
    )

    const view = render(<KnowledgePage />)
    await screen.findByTestId("knowledge-page")
    expect(screen.queryByTestId("knowledge-review-drawer")).toBeNull()

    mocks.useAuth.mockReturnValue(adminAuth())
    view.rerender(<KnowledgePage />)
    expect(await screen.findByTestId("knowledge-review-drawer")).toBeTruthy()
    expect(screen.getByTestId("knowledge-review-item-6001")).toBeTruthy()
  })

  it("keeps the review drawer closed when auth resolves to a regular user", async () => {
    window.history.pushState({}, "", "/knowledge?review=6001")
    mocks.useAuth.mockReturnValue({
      user: null,
      loading: true,
      login: vi.fn(),
      logout: vi.fn(),
    })
    mocks.listGlobalKnowledge.mockResolvedValue(page([]))

    const view = render(<KnowledgePage />)
    await screen.findByTestId("knowledge-page")

    mocks.useAuth.mockReturnValue(userAuth())
    view.rerender(<KnowledgePage />)
    await waitFor(() => expect(screen.queryByTestId("knowledge-open-review")).toBeNull())
    expect(screen.queryByTestId("knowledge-review-drawer")).toBeNull()
  })

  it("hides the review entry from a regular user even with the deep link", async () => {
    window.history.pushState({}, "", "/knowledge?review=6001")
    mocks.listGlobalKnowledge.mockResolvedValue(page([]))
    render(<KnowledgePage />)
    await screen.findByTestId("knowledge-page")
    expect(screen.queryByTestId("knowledge-review-drawer")).toBeNull()
    expect(screen.queryByTestId("knowledge-open-review")).toBeNull()
  })
})
