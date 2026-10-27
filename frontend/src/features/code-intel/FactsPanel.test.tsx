import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { ApiError } from "@/lib/api-client"

import type { Analysis, CodeEntity } from "./types"

const mocks = vi.hoisted(() => ({
  getFacts: vi.fn(),
  getFile: vi.fn(),
}))

vi.mock("./api", () => ({
  getFacts: mocks.getFacts,
  getFile: mocks.getFile,
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
})
