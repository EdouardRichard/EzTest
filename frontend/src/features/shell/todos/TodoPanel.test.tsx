import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import { TodoPanel } from "./TodoPanel"
import type { TodoSnapshot } from "./useTodos"

function item(overrides: Partial<TodoSnapshot["items"][number]>) {
  return {
    kind: "review" as const,
    title: "版本甲",
    versionId: "11",
    versionName: "版本甲",
    severity: "info",
    createdAt: "2026-10-29T00:00:00Z",
    url: "/versions/11?tab=mindmap",
    ...overrides,
  }
}

const FULL: TodoSnapshot = {
  items: [
    item({}),
    item({
      kind: "takeover",
      title: "登录流程",
      versionId: "12",
      versionName: "版本乙",
      severity: "warning",
      url: "/versions/12?tab=sessions",
    }),
    item({
      kind: "gate",
      title: "下单流程",
      versionId: "12",
      versionName: "版本乙",
      severity: "major",
      url: "/versions/12?tab=sessions",
    }),
  ],
  counts: { review: 1, takeover: 1, gate: 1 },
}

const EMPTY: TodoSnapshot = {
  items: [],
  counts: { review: 0, takeover: 0, gate: 0 },
}

afterEach(cleanup)

describe("TodoPanel", () => {
  it("groups items and shows the per-group counts", () => {
    render(<TodoPanel todos={FULL} />)

    expect(screen.getByText("待评审")).not.toBeNull()
    expect(screen.getByText("待接管")).not.toBeNull()
    expect(screen.getByText("待升级")).not.toBeNull()
    expect(screen.getByTestId("todo-count-review").textContent).toBe("1")
    expect(screen.getByTestId("todo-count-takeover").textContent).toBe("1")
    expect(screen.getByTestId("todo-count-gate").textContent).toBe("1")
    expect(screen.getByRole("dialog", { name: "待办中心" })).not.toBeNull()
  })

  it("deep-links every item to its version tab", () => {
    render(<TodoPanel todos={FULL} />)

    expect(screen.getByRole("link", { name: /版本甲/ }).getAttribute("href")).toBe(
      "/versions/11?tab=mindmap",
    )
    expect(screen.getByRole("link", { name: /登录流程/ }).getAttribute("href")).toBe(
      "/versions/12?tab=sessions",
    )
    expect(screen.getByRole("link", { name: /下单流程/ }).getAttribute("href")).toBe(
      "/versions/12?tab=sessions",
    )
  })

  it("renders the loading state", () => {
    render(<TodoPanel todos={EMPTY} loading />)

    expect(screen.getByText("正在加载待办…")).not.toBeNull()
    expect(screen.queryByText("暂无待办")).toBeNull()
  })

  it("renders the error state with a retry callback", () => {
    const onRetry = vi.fn()
    render(<TodoPanel todos={EMPTY} error="网络不可用" onRetry={onRetry} />)

    expect(screen.getByText("网络不可用")).not.toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(onRetry).toHaveBeenCalledTimes(1)
  })

  it("renders the empty state", () => {
    render(<TodoPanel todos={EMPTY} />)

    expect(screen.getByText("暂无待办")).not.toBeNull()
  })

  it("marks the review count as a lower bound when the knowledge queue is capped", () => {
    render(
      <TodoPanel
        todos={{
          items: [item({})],
          counts: { review: 1, takeover: 0, gate: 0 },
          knowledgeTruncated: true,
        }}
      />,
    )

    expect(screen.getByTestId("todo-knowledge-truncated")).not.toBeNull()
  })

  it("hides the takeover and gate groups for a regular user", () => {
    render(<TodoPanel todos={{ items: [item({})], counts: { review: 1, takeover: 0, gate: 0 } }} />)

    expect(screen.getByText("待评审")).not.toBeNull()
    expect(screen.queryByText("待接管")).toBeNull()
    expect(screen.queryByText("待升级")).toBeNull()
  })
})
