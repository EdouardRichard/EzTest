import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import type { PlanItem } from "./api"
import { PlanPanel } from "./PlanPanel"

const fetchMock = vi.fn<typeof fetch>()

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function planList(items: PlanItem[]): Response {
  return jsonResponse({ data: items, next_cursor: null, has_more: false })
}

function planItem(overrides: Partial<PlanItem> = {}): PlanItem {
  return {
    id: "9101",
    version_id: "2001",
    phase: "准备",
    title: "准备环境",
    status: "pending",
    decision: "",
    order: 1,
    revision: 1,
    depends_on: [],
    work_item_id: null,
    ...overrides,
  }
}

describe("PlanPanel", () => {
  beforeEach(() => {
    window.sessionStorage.clear()
    fetchMock.mockReset()
    vi.stubGlobal("fetch", fetchMock)
  })

  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it("groups plan items by status, marks current/next and completes an item with its revision", async () => {
    const current = planItem({
      id: "9101",
      title: "准备环境",
      status: "in_progress",
      order: 1,
      revision: 3,
      decision: "使用测试环境",
    })
    const next = planItem({ id: "9102", title: "确认需求", status: "pending", order: 2 })
    const done = planItem({ id: "9103", title: "收尾", status: "done", order: 3 })
    fetchMock.mockResolvedValueOnce(planList([done, next, current]))

    render(<PlanPanel versionId="2001" />)

    expect(await screen.findByTestId("plan-item-9101")).toBeInTheDocument()
    expect(screen.getByTestId("plan-group-in_progress")).toHaveTextContent("进行中")
    expect(screen.getByTestId("plan-group-pending")).toHaveTextContent("待办")
    expect(screen.getByTestId("plan-group-done")).toHaveTextContent("已完成")

    expect(screen.getByTestId("plan-item-9101")).toHaveTextContent("当前")
    expect(screen.getByTestId("plan-item-9102")).toHaveTextContent("下一步")
    expect(screen.getByTestId("plan-item-9101")).toHaveTextContent("决策：使用测试环境")

    const checkbox = screen.getByRole("checkbox", { name: "完成 准备环境" })
    expect(checkbox).not.toBeChecked()
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ data: { ...current, status: "done", revision: 4 } }),
    )

    fireEvent.click(checkbox)

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
    const [url, init] = fetchMock.mock.calls[1]
    expect(String(url)).toBe("http://localhost:13333/api/versions/2001/plan-items/9101")
    expect(init?.method).toBe("PATCH")
    expect(JSON.parse(String(init?.body))).toEqual({ status: "done", revision: 3 })
    await waitFor(() =>
      expect(screen.getByRole("checkbox", { name: "完成 准备环境" })).toBeChecked(),
    )
    expect(screen.getByTestId("plan-item-9101")).toHaveTextContent("v4")
  })

  it("reverts an optimistic complete on a revision conflict and offers a refresh", async () => {
    const item = planItem({ id: "9201", title: "执行用例", revision: 1 })
    const refreshed = { ...item, status: "done", revision: 2 }
    let getCalls = 0
    let resolvePatch: (response: Response) => void = () => {
      throw new Error("plan PATCH was not issued")
    }

    fetchMock.mockImplementation((_input, init) => {
      const method = (init?.method ?? "GET").toUpperCase()
      if (method === "GET") {
        getCalls += 1
        return Promise.resolve(planList(getCalls === 1 ? [item] : [refreshed]))
      }
      return new Promise<Response>((resolve) => {
        resolvePatch = resolve
      })
    })

    render(<PlanPanel versionId="2001" />)

    const checkbox = await screen.findByRole("checkbox", { name: "完成 执行用例" })
    fireEvent.click(checkbox)

    // Optimistic: the item is shown done before the PATCH settles.
    await waitFor(() =>
      expect(screen.getByRole("checkbox", { name: "完成 执行用例" })).toBeChecked(),
    )
    await waitFor(() =>
      expect(fetchMock.mock.calls.some(([, init]) => init?.method === "PATCH")).toBe(true),
    )
    resolvePatch(
      jsonResponse(
        {
          error: {
            code: "conflict",
            message: "计划项已被更新",
            details: { item_id: "9201", expected_revision: 1, actual_revision: 2 },
          },
        },
        409,
      ),
    )

    const conflict = await screen.findByTestId("plan-conflict")
    expect(conflict).toHaveTextContent("计划项已在别处更新")
    expect(screen.getByRole("checkbox", { name: "完成 执行用例" })).not.toBeChecked()

    fireEvent.click(within(conflict).getByTestId("plan-refresh"))

    await waitFor(() => expect(getCalls).toBe(2))
    await waitFor(() =>
      expect(screen.getByRole("checkbox", { name: "完成 执行用例" })).toBeChecked(),
    )
    expect(screen.queryByTestId("plan-conflict")).not.toBeInTheDocument()
  })

  it("renders an empty state when the version has no plan items", async () => {
    fetchMock.mockResolvedValueOnce(planList([]))

    render(<PlanPanel versionId="2001" />)

    expect(await screen.findByText("暂无计划项")).toBeInTheDocument()
  })

  it("shows a load error and retries the plan read", async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse({ error: { code: "internal", message: "计划服务暂不可用" } }, 500),
    )

    render(<PlanPanel versionId="2001" />)

    expect(await screen.findByText("计划加载失败")).toBeInTheDocument()
    fetchMock.mockResolvedValueOnce(planList([planItem()]))
    fireEvent.click(screen.getByRole("button", { name: "重试" }))

    expect(await screen.findByTestId("plan-item-9101")).toBeInTheDocument()
  })
})
