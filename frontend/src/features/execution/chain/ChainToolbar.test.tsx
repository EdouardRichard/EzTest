import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import { CHAIN_TOOLBAR_DEFAULTS, ChainToolbar, type ChainToolbarValue } from "./ChainToolbar"

afterEach(() => {
  cleanup()
})

function renderToolbar(overrides: Partial<ChainToolbarValue> = {}) {
  const onChange = vi.fn()
  const onBatchScopeChange = vi.fn()
  const value: ChainToolbarValue = { ...CHAIN_TOOLBAR_DEFAULTS, ...overrides }
  const utils = render(
    <ChainToolbar
      value={value}
      onChange={onChange}
      onBatchScopeChange={onBatchScopeChange}
      batchScope="current"
      batchId="batch-1"
      requirementOptions={[
        { id: "r-1", title: "登录需求" },
        { id: "r-2", title: "下单需求" },
      ]}
    />,
  )
  return { ...utils, onChange, onBatchScopeChange, value }
}

describe("ChainToolbar", () => {
  it("renders every filter, search, follow, list and reset control", () => {
    renderToolbar()

    expect(screen.getByTestId("chain-toolbar")).not.toBeNull()
    expect(screen.getByTestId("chain-batch-filter")).not.toBeNull()
    expect(screen.getByTestId("exploration-filter-requirement")).not.toBeNull()
    expect(screen.getByTestId("exploration-filter-status")).not.toBeNull()
    expect(screen.getByTestId("exploration-filter-abnormal")).not.toBeNull()
    expect(screen.getByTestId("exploration-filter-issue-severity")).not.toBeNull()
    expect(screen.getByTestId("exploration-filter-issue-status")).not.toBeNull()
    expect(screen.getByTestId("exploration-filter-supplement")).not.toBeNull()
    expect(screen.getByTestId("exploration-filter-revalidation")).not.toBeNull()
    expect(screen.getByTestId("exploration-filter-search")).not.toBeNull()
    expect(screen.getByTestId("chain-toolbar-follow")).not.toBeNull()
    expect(screen.getByTestId("chain-toolbar-list")).not.toBeNull()
    expect(screen.getByTestId("chain-toolbar-reset")).not.toBeNull()
  })

  it("reports the requirement and result-status filters", () => {
    const { onChange, value } = renderToolbar()

    fireEvent.change(screen.getByTestId("exploration-filter-requirement"), {
      target: { value: "r-2" },
    })
    expect(onChange).toHaveBeenLastCalledWith({ ...value, requirementId: "r-2" })

    fireEvent.change(screen.getByTestId("exploration-filter-status"), {
      target: { value: "failed" },
    })
    expect(onChange).toHaveBeenLastCalledWith({ ...value, resultStatus: "failed" })
  })

  it("reports abnormal-only, AI supplement and revalidation toggles", () => {
    const { onChange, value } = renderToolbar()

    fireEvent.click(screen.getByTestId("exploration-filter-abnormal"))
    expect(onChange).toHaveBeenLastCalledWith({ ...value, abnormalOnly: true })

    fireEvent.click(screen.getByTestId("exploration-filter-supplement"))
    expect(onChange).toHaveBeenLastCalledWith({ ...value, includeSupplement: false })

    fireEvent.click(screen.getByTestId("exploration-filter-revalidation"))
    expect(onChange).toHaveBeenLastCalledWith({ ...value, revalidationOnly: true })
  })

  it("reports issue severity and issue status filters", () => {
    const { onChange, value } = renderToolbar()

    fireEvent.change(screen.getByTestId("exploration-filter-issue-severity"), {
      target: { value: "blocker" },
    })
    expect(onChange).toHaveBeenLastCalledWith({ ...value, issueSeverity: "blocker" })

    fireEvent.change(screen.getByTestId("exploration-filter-issue-status"), {
      target: { value: "confirmed" },
    })
    expect(onChange).toHaveBeenLastCalledWith({ ...value, issueStatus: "confirmed" })
  })

  it("reports the search term without debouncing the callback", () => {
    const { onChange, value } = renderToolbar()

    fireEvent.change(screen.getByTestId("exploration-filter-search"), {
      target: { value: "登录" },
    })
    expect(onChange).toHaveBeenLastCalledWith({ ...value, q: "登录" })
  })

  it("reports follow and list-view toggles through aria-pressed buttons", () => {
    const { onChange, value } = renderToolbar()

    const follow = screen.getByTestId("chain-toolbar-follow")
    expect(follow.getAttribute("aria-pressed")).toBe("false")
    fireEvent.click(follow)
    expect(onChange).toHaveBeenLastCalledWith({ ...value, follow: true })

    const list = screen.getByTestId("chain-toolbar-list")
    fireEvent.click(list)
    expect(onChange).toHaveBeenLastCalledWith({ ...value, listView: true })
  })

  it("resets every filter and view option to its default", () => {
    const { onChange } = renderToolbar({
      requirementId: "r-1",
      resultStatus: "failed",
      abnormalOnly: true,
      issueSeverity: "major",
      issueStatus: "open",
      includeSupplement: false,
      revalidationOnly: true,
      q: "登录",
      follow: true,
      listView: true,
    })

    fireEvent.click(screen.getByTestId("chain-toolbar-reset"))

    expect(onChange).toHaveBeenLastCalledWith(CHAIN_TOOLBAR_DEFAULTS)
  })

  it("maps the batch scope selector to its own callback", () => {
    const { onBatchScopeChange } = renderToolbar()

    fireEvent.change(screen.getByTestId("chain-batch-filter"), {
      target: { value: "all" },
    })

    expect(onBatchScopeChange).toHaveBeenCalledWith("all")
  })

  it("keeps every button at least 44px tall for touch targets", () => {
    renderToolbar()

    const buttons = screen.getAllByRole("button")
    expect(buttons.length).toBeGreaterThan(0)
    for (const button of buttons) {
      expect(button.className).toContain("min-h-11")
    }
  })
})
