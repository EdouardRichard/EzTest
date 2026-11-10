import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { DesignSplitPane } from "./DesignSplitPane"

const STORAGE_KEY = "eztest.test.split-pane"

/**
 * jsdom has no layout engine: the split measures itself through
 * `getBoundingClientRect`, so the tests pin a definite height before rendering.
 * The divider contract (`overflow` lock, floors) is otherwise unreachable.
 */
function mockContainerHeight(height: number): void {
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({
    x: 0,
    y: 0,
    width: 800,
    height,
    top: 0,
    right: 800,
    bottom: height,
    left: 0,
    toJSON: () => ({}),
  } as DOMRect)
}

function renderPane(
  overrides: Partial<Parameters<typeof DesignSplitPane>[0]> = {},
): ReturnType<typeof screen.getByTestId> {
  render(
    <DesignSplitPane
      testId="test-split"
      defaultPercent={40}
      minPercent={20}
      maxPercent={80}
      storageKey={STORAGE_KEY}
      ariaLabel="调整测试分栏高度"
      top={<div>顶部</div>}
      bottom={<div>底部</div>}
      topMinHeightPx={220}
      bottomMinHeightPx={180}
      scrollOnOverflow
      {...overrides}
    />,
  )
  return screen.getByTestId("test-split")
}

describe("DesignSplitPane divider", () => {
  beforeEach(() => {
    window.localStorage.clear()
  })

  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
  })

  it("locks a divider whose pixel floors cannot fit and ignores drag, keyboard and double-click", () => {
    // 220 (graph floor) + 180 (detail floor) + 8 (divider) > 100.
    mockContainerHeight(100)
    window.localStorage.setItem(STORAGE_KEY, "60")

    const split = renderPane()
    const divider = screen.getByTestId("test-split-divider")

    expect(split).toHaveAttribute("data-overflowing", "true")
    expect(divider).toHaveAttribute("aria-disabled", "true")
    expect(divider).toHaveAttribute("tabindex", "-1")
    expect(divider).not.toHaveAttribute("aria-valuenow")
    expect(divider).toHaveAttribute("title", "可用高度不足，已锁定分隔条")
    expect(split).toHaveAttribute("data-split-top", "60")

    // Pointer drag would otherwise move the split to the max clamp.
    fireEvent.pointerDown(divider, { clientY: 10 })
    fireEvent.pointerMove(divider, { clientY: 90 })
    fireEvent.pointerUp(divider, { clientY: 90 })
    expect(split).toHaveAttribute("data-split-top", "60")

    // Keyboard would otherwise step -/+ and Home/End would jump to the clamps.
    fireEvent.keyDown(divider, { key: "ArrowDown" })
    fireEvent.keyDown(divider, { key: "Home" })
    expect(split).toHaveAttribute("data-split-top", "60")

    // Double-click would otherwise reset to `defaultPercent` (40).
    fireEvent.doubleClick(divider)
    expect(split).toHaveAttribute("data-split-top", "60")
  })

  it("reports the persisted percentage through data and aria while a pixel floor clamps the pane", () => {
    // 20% of 600px = 120px, below the 220px graph floor: the pane is clamped
    // visually, the divider value stays the persisted ratio preference.
    mockContainerHeight(600)
    window.localStorage.setItem(STORAGE_KEY, "20")

    const split = renderPane()
    const topPane = screen.getByTestId("test-split-top")
    const divider = screen.getByTestId("test-split-divider")

    expect(split).toHaveAttribute("data-overflowing", "false")
    expect(topPane.style.minHeight).toBe("220px")
    expect(split).toHaveAttribute("data-split-top", "20")
    expect(divider).toHaveAttribute("aria-valuenow", "20")
    expect(divider).toHaveAttribute("aria-valuemin", "20")
  })

  it("excludes a collapsed top from the floor check guard", () => {
    // The collapsed top is display:none-ish (`flex: 0 0 auto`), so its 220px
    // floor must not mark a 100px container as overflowing. With no bottom
    // floor the DIVIDER_HEIGHT_PX guard (8px) is the only remaining minimum.
    mockContainerHeight(100)

    const split = renderPane({ topCollapsed: true, topMinHeightPx: 220, bottomMinHeightPx: 0 })
    const divider = screen.getByTestId("test-split-divider")

    expect(split).toHaveAttribute("data-overflowing", "false")
    expect(divider).toHaveAttribute("tabindex", "-1")
    expect(divider).not.toHaveAttribute("aria-valuenow")
    expect(divider.style.display).toBe("none")
    expect(screen.getByTestId("test-split-top").style.minHeight).toBe("0px")
  })
})
