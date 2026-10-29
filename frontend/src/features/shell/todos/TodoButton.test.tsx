import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { TodoSnapshot } from "./useTodos"

const mocks = vi.hoisted(() => ({
  useTodos: vi.fn(),
}))

vi.mock("./useTodos", () => ({
  useTodos: mocks.useTodos,
}))

import { TodoButton } from "./TodoButton"

const SNAPSHOT: TodoSnapshot = {
  items: [
    {
      kind: "review",
      title: "版本甲",
      versionId: "11",
      versionName: "版本甲",
      severity: "info",
      createdAt: "2026-10-29T00:00:00Z",
      url: "/versions/11?tab=mindmap",
    },
  ],
  counts: { review: 2, takeover: 1, gate: 1 },
}

function todoState(overrides: Partial<ReturnType<typeof baseState>> = {}) {
  return { ...baseState(), ...overrides }
}

function baseState() {
  return {
    todos: SNAPSHOT,
    loading: false,
    error: "",
    refresh: vi.fn(),
  }
}

beforeEach(() => {
  mocks.useTodos.mockReset()
  mocks.useTodos.mockReturnValue(todoState())
})

afterEach(cleanup)

describe("TodoButton", () => {
  it("shows the open bell, the total count badge and opens the panel", () => {
    render(<TodoButton />)

    const trigger = screen.getByRole("button", { name: "待办中心" })
    expect(trigger.getAttribute("aria-expanded")).toBe("false")
    expect(screen.getByTestId("todo-count").textContent).toBe("4")

    fireEvent.click(trigger)
    expect(trigger.getAttribute("aria-expanded")).toBe("true")
    expect(screen.getByRole("dialog", { name: "待办中心" })).not.toBeNull()
  })

  it("hides the badge when there is nothing to do", () => {
    mocks.useTodos.mockReturnValue(
      todoState({ todos: { items: [], counts: { review: 0, takeover: 0, gate: 0 } } }),
    )
    render(<TodoButton />)

    expect(screen.queryByTestId("todo-count")).toBeNull()
  })

  it("shows the panel error and retries without hiding the bell", () => {
    const refresh = vi.fn()
    mocks.useTodos.mockReturnValue(todoState({ error: "网络不可用", refresh }))
    render(<TodoButton />)

    const trigger = screen.getByRole("button", { name: "待办中心" })
    expect(trigger).not.toBeNull()
    fireEvent.click(trigger)
    expect(screen.getByText("网络不可用")).not.toBeNull()

    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(refresh).toHaveBeenCalledTimes(1)
  })

  it("closes the panel from its close button", () => {
    render(<TodoButton />)
    fireEvent.click(screen.getByRole("button", { name: "待办中心" }))
    expect(screen.getByRole("dialog", { name: "待办中心" })).not.toBeNull()

    fireEvent.click(screen.getByRole("button", { name: "关闭待办中心" }))
    expect(screen.queryByRole("dialog", { name: "待办中心" })).toBeNull()
  })
})
