import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { ApiError } from "@/lib/api-client"

import type { Analysis, CodeEntity, FactReview } from "./types"

const mocks = vi.hoisted(() => ({
  getFacts: vi.fn(),
  getFile: vi.fn(),
  createFactReview: vi.fn(),
}))

vi.mock("./api", () => ({
  getFacts: mocks.getFacts,
  getFile: mocks.getFile,
  createFactReview: mocks.createFactReview,
}))

import { FactsPanel } from "./FactsPanel"

const REPO_ID = 3
const VERSION_ID = "1844674407370955161"
const ANALYSIS_ID = "1844674407370955162"
const HEAD = "89abcdef0123456789abcdef0123456789abcdef"

function analysisFixture(overrides: Partial<Analysis> = {}): Analysis {
  return {
    id: ANALYSIS_ID,
    versionId: VERSION_ID,
    repoId: REPO_ID,
    baseCommit: "0123456789abcdef0123456789abcdef01234567",
    headCommit: HEAD,
    status: "done",
    degraded: false,
    analysisVersion: 1,
    model: "deepseek-chat",
    promptVersion: "p4.1",
    changeSetId: "1844674407370955163",
    changeSummary: "退款接口新增 reason 参数",
    stats: null,
    factVersions: [],
    candidates: [],
    unmapped: [],
    errors: [],
    createdAt: "2026-10-28T01:00:00Z",
    startedAt: "2026-10-28T01:00:00Z",
    finishedAt: "2026-10-28T01:02:03Z",
    ...overrides,
  }
}

function entityFixture(overrides: Partial<CodeEntity> = {}): CodeEntity {
  return {
    id: "1844674407370955201",
    repoId: REPO_ID,
    commit: HEAD,
    kind: "route",
    path: "src/routes/orders.ts",
    name: "GET /orders",
    startLine: 1,
    endLine: 10,
    signature: "",
    blobHash: "git-sha1:1111111111111111111111111111111111111111",
    source: "ai",
    confidence: "high",
    evidence: [{ file: "src/routes/orders.ts", startLine: 2, endLine: 3 }],
    ...overrides,
  }
}

function factsPage(items: CodeEntity[], hasMore = false) {
  return { items, nextCursor: hasMore ? "next" : "", hasMore }
}

/** One persisted human verdict nested in a fact (the additive P8 field). */
function reviewFixture(overrides: Partial<FactReview> = {}): FactReview {
  return {
    id: "9301",
    factId: "1",
    commit: HEAD,
    verdict: "correct",
    reason: "证据行号已核对",
    createdBy: 2,
    createdAt: "2026-10-29T10:00:00Z",
    ...overrides,
  }
}

/** Serves the per-kind fact queries the panel issues (spec §9.2). */
function mockFactsByKind(items: CodeEntity[], hasMore = false): void {
  mocks.getFacts.mockImplementation((_repoId: number, params: { kind?: string }) =>
    Promise.resolve(
      factsPage(
        items.filter((item) => item.kind === params.kind),
        hasMore,
      ),
    ),
  )
}

describe("FactsPanel", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset()
    mocks.getFacts.mockResolvedValue(factsPage([]))
  })

  afterEach(() => {
    cleanup()
  })

  it("groups facts by kind and marks low-confidence clues", async () => {
    mockFactsByKind([
      entityFixture({ id: "1", kind: "route", name: "GET /orders" }),
      entityFixture({
        id: "2",
        kind: "api",
        name: "POST /api/refund",
        path: "src/api/refund.ts",
        confidence: "medium",
      }),
      entityFixture({
        id: "3",
        kind: "component",
        name: "OrderList",
        path: "src/components/OrderList.tsx",
        confidence: "low",
        evidence: [],
      }),
      entityFixture({
        id: "4",
        kind: "file",
        name: "src/api/refund.ts",
        source: "git",
        evidence: [],
      }),
    ])

    render(<FactsPanel analysis={analysisFixture()} />)

    expect(await screen.findByText("GET /orders")).toBeTruthy()
    // One bounded query per fact kind keeps T0 `file` facts from crowding out
    // the AI facts, which would be hidden behind the page limit otherwise.
    expect(mocks.getFacts).toHaveBeenCalledTimes(7)
    expect(mocks.getFacts).toHaveBeenCalledWith(REPO_ID, {
      commit: HEAD,
      kind: "route",
      limit: 50,
    })
    expect(mocks.getFacts).toHaveBeenCalledWith(REPO_ID, {
      commit: HEAD,
      kind: "symbol",
      limit: 50,
    })
    expect(mocks.getFacts).not.toHaveBeenCalledWith(REPO_ID, { commit: HEAD, limit: 200 })
    expect(screen.getByRole("heading", { name: "代码事实" })).toBeTruthy()
    expect(screen.getByRole("heading", { name: "路由" })).toBeTruthy()
    expect(screen.getByRole("heading", { name: "接口" })).toBeTruthy()
    expect(screen.getByRole("heading", { name: "组件" })).toBeTruthy()
    // T0 file facts are not one of the seven AI fact kinds and stay hidden.
    expect(document.querySelectorAll('[data-fact-kind="file"]')).toHaveLength(0)
    expect(screen.queryByRole("heading", { name: "文件" })).toBeNull()

    expect(screen.getByText("高置信")).toBeTruthy()
    expect(screen.getByText("中置信")).toBeTruthy()
    expect(screen.getByText("低置信线索")).toBeTruthy()
    const lowRow = screen.getByText("OrderList").closest("li")
    expect(lowRow?.getAttribute("data-confidence")).toBe("low")
    expect(lowRow?.className).toContain("opacity-60")
  })

  it("opens a file window drawer and highlights the evidence lines", async () => {
    mockFactsByKind([
      entityFixture({
        id: "1",
        kind: "route",
        name: "GET /orders",
        evidence: [{ file: "src/routes/orders.ts", startLine: 2, endLine: 3 }],
      }),
    ])
    mocks.getFile.mockResolvedValue({
      commit: HEAD,
      path: "src/routes/orders.ts",
      startLine: 1,
      endLine: 5,
      totalLines: 12,
      truncated: false,
      content: "alpha\nbravo\ncharlie\ndelta\necho",
    })

    render(<FactsPanel analysis={analysisFixture()} />)

    const link = await screen.findByRole("button", {
      name: "查看证据 src/routes/orders.ts:2-3",
    })
    expect(mocks.getFile).not.toHaveBeenCalled()
    fireEvent.click(link)

    await waitFor(() =>
      expect(mocks.getFile).toHaveBeenCalledWith(REPO_ID, {
        commit: HEAD,
        path: "src/routes/orders.ts",
        startLine: 1,
        endLine: 23,
      }),
    )

    const dialog = await screen.findByRole("dialog", { name: /src\/routes\/orders\.ts/ })
    expect(dialog.textContent).toContain("bravo")
    expect(dialog.textContent).toContain("charlie")
    const highlighted = dialog.querySelectorAll('[data-evidence-line="true"]')
    expect(highlighted).toHaveLength(2)
    expect(highlighted[0]?.textContent).toContain("bravo")
    expect(highlighted[1]?.textContent).toContain("charlie")

    fireEvent.keyDown(dialog, { key: "Escape" })
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
  })

  it("renders empty, error and running states without fabricating facts", async () => {
    const empty = render(<FactsPanel analysis={analysisFixture()} />)
    expect(await screen.findByText("暂无代码事实")).toBeTruthy()
    empty.unmount()

    mocks.getFacts.mockRejectedValue(
      new ApiError({ status: 503, code: "unavailable", message: "git unavailable" }),
    )
    const failed = render(<FactsPanel analysis={analysisFixture()} />)
    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("加载代码事实失败")
    expect(screen.getByRole("button", { name: "重试加载事实" })).toBeTruthy()
    failed.unmount()

    mocks.getFacts.mockReset()
    const running = render(<FactsPanel analysis={analysisFixture({ status: "running" })} />)
    expect(screen.getByText("分析运行中，代码事实完成后展示。")).toBeTruthy()
    expect(mocks.getFacts).not.toHaveBeenCalled()
    running.unmount()
  })

  it("shows the persisted human verdict and reason on reviewed facts", async () => {
    mockFactsByKind([
      entityFixture({
        id: "1",
        kind: "route",
        name: "GET /orders",
        review: reviewFixture({ factId: "1", verdict: "correct", reason: "证据行号已核对" }),
      }),
      entityFixture({
        id: "2",
        kind: "api",
        name: "POST /api/refund",
        path: "src/api/refund.ts",
        review: reviewFixture({
          id: "9302",
          factId: "2",
          verdict: "incorrect",
          reason: "接口已删除",
          createdBy: 7,
        }),
      }),
      entityFixture({ id: "3", kind: "component", name: "OrderList", evidence: [] }),
    ])

    render(<FactsPanel analysis={analysisFixture()} />)

    const badges = await screen.findAllByTestId("fact-review-badge")
    expect(badges).toHaveLength(2)
    expect(badges[0]?.textContent).toContain("已人工修正")
    expect(badges[0]?.getAttribute("data-review-verdict")).toBe("correct")
    expect(badges[1]?.textContent).toContain("错误")
    expect(badges[1]?.getAttribute("data-review-verdict")).toBe("incorrect")

    const reasons = screen.getAllByTestId("fact-review-reason")
    expect(reasons).toHaveLength(2)
    expect(reasons[0]?.textContent).toContain("证据行号已核对")
    expect(reasons[1]?.textContent).toContain("接口已删除")

    // An unreviewed fact keeps no marker.
    const componentRow = screen.getByText("OrderList").closest("li")
    expect(componentRow?.querySelector('[data-testid="fact-review-badge"]')).toBeNull()
  })

  it("submits an incorrect verdict with a reason and marks the fact", async () => {
    mockFactsByKind([entityFixture({ id: "1", kind: "route", name: "GET /orders" })])
    mocks.createFactReview.mockResolvedValue(
      reviewFixture({ factId: "1", verdict: "incorrect", reason: "接口已删除", createdBy: 2 }),
    )

    render(<FactsPanel analysis={analysisFixture()} />)

    fireEvent.click(await screen.findByRole("button", { name: "人工修正 GET /orders" }))
    fireEvent.change(screen.getByLabelText("修正原因"), { target: { value: "接口已删除" } })
    fireEvent.click(screen.getByRole("button", { name: "标记为错误" }))

    await waitFor(() =>
      expect(mocks.createFactReview).toHaveBeenCalledWith(REPO_ID, "1", {
        verdict: "incorrect",
        reason: "接口已删除",
      }),
    )
    const badge = await screen.findByTestId("fact-review-badge")
    expect(badge.textContent).toContain("错误")
    expect(screen.getByTestId("fact-review-reason").textContent).toContain("接口已删除")
    // The inline form closes after a successful write.
    expect(screen.queryByLabelText("修正原因")).toBeNull()
  })

  it("keeps a failed review write visible with a retry-able error", async () => {
    mockFactsByKind([entityFixture({ id: "1", kind: "route", name: "GET /orders" })])
    mocks.createFactReview.mockRejectedValue(
      new ApiError({ status: 422, code: "invalid_argument", message: "verdict invalid" }),
    )

    render(<FactsPanel analysis={analysisFixture()} />)

    fireEvent.click(await screen.findByRole("button", { name: "人工修正 GET /orders" }))
    fireEvent.click(screen.getByRole("button", { name: "标记为正确" }))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("保存人工修正失败")
    expect(screen.queryByTestId("fact-review-badge")).toBeNull()
    // The form stays open so the reviewer can fix and retry.
    expect(screen.getByLabelText("修正原因")).toBeTruthy()
  })
})
