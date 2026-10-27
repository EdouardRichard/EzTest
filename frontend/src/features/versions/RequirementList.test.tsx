import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import { RequirementList } from "./RequirementList"
import type { Requirement } from "./types"

const REQ_A = "1844674407370956001"
const REQ_B = "1844674407370956002"

function requirement(
  overrides: Partial<Requirement> & Pick<Requirement, "id" | "title">,
): Requirement {
  return {
    versionId: "1844674407370955161",
    description: "",
    acceptanceCriteria: "",
    priority: "should",
    status: "draft",
    source: "user",
    lastUpdateSource: "user",
    revision: 1,
    confirmedAt: null,
    confirmedBy: null,
    openQuestionCount: 0,
    relationCount: 0,
    attachmentCount: 0,
    createdBy: 1,
    updatedBy: 1,
    createdAt: "2026-10-27T10:00:00Z",
    updatedAt: "2026-10-27T10:00:00Z",
    ...overrides,
  }
}

const DRAFT = requirement({
  id: REQ_A,
  title: "订单创建",
  openQuestionCount: 2,
  relationCount: 1,
  attachmentCount: 3,
})

const CLARIFIED = requirement({
  id: REQ_B,
  title: "库存扣减",
  status: "clarified",
  revision: 4,
  confirmedAt: "2026-10-27T11:00:00Z",
  confirmedBy: 1,
})

describe("RequirementList", () => {
  afterEach(cleanup)

  it("renders requirements with status and open-question, relation and attachment badges", () => {
    render(
      <RequirementList
        requirements={[DRAFT, CLARIFIED]}
        selectedId={null}
        onSelect={vi.fn()}
        onCreate={vi.fn()}
      />,
    )

    expect(screen.getByRole("heading", { name: "需求列表" })).toBeTruthy()
    expect(screen.getByRole("button", { name: /订单创建/ })).toBeTruthy()
    expect(screen.getByText("待澄清 2")).toBeTruthy()
    expect(screen.getByText("关系 1")).toBeTruthy()
    expect(screen.getByText("附件 3")).toBeTruthy()
    expect(screen.getAllByText("草稿").length).toBeGreaterThan(0)
    expect(screen.getAllByText("已澄清").length).toBeGreaterThan(0)
  })

  it("filters draft and clarified requirements", () => {
    render(
      <RequirementList
        requirements={[DRAFT, CLARIFIED]}
        selectedId={null}
        onSelect={vi.fn()}
        onCreate={vi.fn()}
      />,
    )

    fireEvent.click(screen.getByRole("button", { name: "草稿" }))
    expect(screen.getByText("订单创建")).toBeTruthy()
    expect(screen.queryByText("库存扣减")).toBeNull()

    fireEvent.click(screen.getByRole("button", { name: "已澄清" }))
    expect(screen.getByText("库存扣减")).toBeTruthy()
    expect(screen.queryByText("订单创建")).toBeNull()

    fireEvent.click(screen.getByRole("button", { name: "全部" }))
    expect(screen.getByText("订单创建")).toBeTruthy()
    expect(screen.getByText("库存扣减")).toBeTruthy()
  })

  it("selects a requirement and reports creation intent", () => {
    const onSelect = vi.fn()
    const onCreate = vi.fn()
    render(
      <RequirementList
        requirements={[DRAFT, CLARIFIED]}
        selectedId={REQ_A}
        onSelect={onSelect}
        onCreate={onCreate}
      />,
    )

    const selected = screen.getByRole("button", { name: /订单创建/ })
    expect(selected.getAttribute("aria-pressed")).toBe("true")

    fireEvent.click(screen.getByRole("button", { name: /库存扣减/ }))
    expect(onSelect).toHaveBeenCalledWith(REQ_B)

    fireEvent.click(screen.getByRole("button", { name: "新增需求" }))
    expect(onCreate).toHaveBeenCalledTimes(1)
  })

  it("shows the empty state when there are no requirements", () => {
    const onCreate = vi.fn()
    render(
      <RequirementList
        requirements={[]}
        selectedId={null}
        onSelect={vi.fn()}
        onCreate={onCreate}
      />,
    )

    expect(screen.getByText("暂无需求")).toBeTruthy()
    expect(screen.getByText("先新增需求并填写标题，再补充描述、验收标准与优先级。")).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "新增第一条需求" }))
    expect(onCreate).toHaveBeenCalledTimes(1)
  })

  it("keeps the last requirements and offers a retry after a load failure", () => {
    const onRetry = vi.fn()
    render(
      <RequirementList
        requirements={[DRAFT]}
        selectedId={REQ_A}
        onSelect={vi.fn()}
        onCreate={vi.fn()}
        error="网络异常"
        onRetry={onRetry}
      />,
    )

    const alert = screen.getByRole("alert")
    expect(alert.textContent).toContain("加载需求失败：网络异常")
    expect(screen.getByText("订单创建")).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(onRetry).toHaveBeenCalledTimes(1)
  })

  it("shows loading, filter-empty and load-more states", () => {
    const onLoadMore = vi.fn()
    const { rerender } = render(
      <RequirementList
        requirements={[]}
        selectedId={null}
        onSelect={vi.fn()}
        onCreate={vi.fn()}
        loading
      />,
    )
    expect(screen.getByText("正在加载需求…")).toBeTruthy()

    rerender(
      <RequirementList
        requirements={[CLARIFIED]}
        selectedId={null}
        onSelect={vi.fn()}
        onCreate={vi.fn()}
        hasMore
        onLoadMore={onLoadMore}
      />,
    )
    fireEvent.click(screen.getByRole("button", { name: "草稿" }))
    expect(screen.getByText("当前筛选下暂无需求。")).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: "加载更多需求" }))
    expect(onLoadMore).toHaveBeenCalledTimes(1)
  })
})
