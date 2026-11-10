"use client"

import {
  type KeyboardEvent,
  type PointerEvent,
  type ReactNode,
  useEffect,
  useRef,
  useState,
} from "react"

import { cn } from "@/components/ui/cn"

import {
  clampSplitPercent,
  KEYBOARD_SPLIT_STEP,
  readStoredSplit,
  roundSplitPercent,
  writeStoredSplit,
} from "./layout"

export type DesignSplitPaneProps = {
  /** Stable test id for the split container; panes default to `${testId}-top`. */
  testId: string
  /** Override for the top pane's `data-testid` (e.g. `design-graph`). */
  topTestId?: string
  /** Override for the bottom pane's `data-testid` (e.g. `design-detail`). */
  bottomTestId?: string
  /** Persisted top-pane share in percent (R-D2-2). */
  defaultPercent: number
  minPercent: number
  maxPercent: number
  storageKey: string
  ariaLabel: string
  top: ReactNode
  bottom: ReactNode
  /**
   * Collapsed top (short viewport / user collapse): the top shrinks to its
   * content height and the bottom fills the remaining space.
   */
  topCollapsed?: boolean
  className?: string
  topClassName?: string
  bottomClassName?: string
  topOverflow?: "auto" | "hidden"
  bottomOverflow?: "auto" | "hidden"
  /**
   * Pixel floor for the top pane (D-01). Percentage-only panes collapsed the
   * graph to ~110px on a 1440x900 viewport; a floor keeps the canvas usable.
   */
  topMinHeightPx?: number
  /** Pixel floor for the bottom pane (D-01/D-02). */
  bottomMinHeightPx?: number
  /**
   * Scroll the split itself when both pixel floors cannot fit the caller's
   * height (short viewports). The page stays scroll-free; the divider is locked
   * because the ratio cannot be honoured.
   */
  scrollOnOverflow?: boolean
}

type DragState = {
  startY: number
  startPercent: number
}

/** Height of the divider (`h-2`) reserved when checking the floors. */
const DIVIDER_HEIGHT_PX = 8

/**
 * Vertical two-pane splitter with the accessible divider contract from the
 * approved v0.22 UI: pointer drag, double-click reset, ArrowUp/ArrowDown (5%)
 * and Home/End (min/max). The top share is persisted in `localStorage`, so the
 * approved 40/60 and 65/35 ratios survive a refresh per user.
 *
 * It is deliberately presentation-only: the panes keep their own scroll and
 * the divider uses the existing border/primary tokens (no new visual styles).
 */
export function DesignSplitPane({
  testId,
  topTestId,
  bottomTestId,
  defaultPercent,
  minPercent,
  maxPercent,
  storageKey,
  ariaLabel,
  top,
  bottom,
  topCollapsed = false,
  className,
  topClassName,
  bottomClassName,
  topOverflow = "auto",
  bottomOverflow = "auto",
  topMinHeightPx,
  bottomMinHeightPx,
  scrollOnOverflow = false,
}: DesignSplitPaneProps) {
  const [percent, setPercent] = useState(() =>
    readStoredSplit(storageKey, defaultPercent, minPercent, maxPercent),
  )
  const containerRef = useRef<HTMLDivElement>(null)
  const dragRef = useRef<DragState | null>(null)
  const [dragging, setDragging] = useState(false)
  /**
   * True when the caller's height cannot hold both pixel floors: the panes
   * keep their minimums, the split scrolls and the divider is locked.
   */
  const [overflowing, setOverflowing] = useState(false)

  // Measure once and on every resize: the floors and the available height both
  // change with the viewport, stage notices and the focus dialog.
  useEffect(() => {
    const element = containerRef.current
    if (element === null) return undefined
    const measure = () => {
      const height = element.getBoundingClientRect().height
      const floors =
        (topCollapsed ? 0 : (topMinHeightPx ?? 0)) + (bottomMinHeightPx ?? 0) + DIVIDER_HEIGHT_PX
      setOverflowing(floors > DIVIDER_HEIGHT_PX && height > 0 && height < floors)
    }
    measure()
    if (typeof ResizeObserver !== "function") return undefined
    const observer = new ResizeObserver(measure)
    observer.observe(element)
    return () => observer.disconnect()
  }, [bottomMinHeightPx, topCollapsed, topMinHeightPx])

  const interactionDisabled = topCollapsed || overflowing

  // Persist on every committed change (drag, keyboard, double-click reset).
  useEffect(() => {
    writeStoredSplit(storageKey, percent)
  }, [percent, storageKey])

  const bottomPercent = roundSplitPercent(100 - percent)

  function handlePointerDown(event: PointerEvent<HTMLDivElement>): void {
    if (interactionDisabled) return
    dragRef.current = { startY: event.clientY, startPercent: percent }
    setDragging(true)
    if (typeof event.currentTarget.setPointerCapture === "function") {
      event.currentTarget.setPointerCapture(event.pointerId)
    }
    event.preventDefault()
  }

  function handlePointerMove(event: PointerEvent<HTMLDivElement>): void {
    const drag = dragRef.current
    if (drag === null) return
    const height = containerRef.current?.getBoundingClientRect().height ?? 0
    if (height <= 0) return
    const delta = ((event.clientY - drag.startY) / height) * 100
    setPercent(clampSplitPercent(drag.startPercent + delta, minPercent, maxPercent))
  }

  function endDrag(event: PointerEvent<HTMLDivElement>): void {
    if (dragRef.current === null) return
    dragRef.current = null
    setDragging(false)
    if (
      typeof event.currentTarget.releasePointerCapture === "function" &&
      event.currentTarget.hasPointerCapture?.(event.pointerId)
    ) {
      event.currentTarget.releasePointerCapture(event.pointerId)
    }
  }

  function reset(): void {
    if (interactionDisabled) return
    setPercent(defaultPercent)
  }

  function handleKeyDown(event: KeyboardEvent<HTMLDivElement>): void {
    if (interactionDisabled) return
    if (event.key === "Home" || event.key === "End") {
      event.preventDefault()
      setPercent(event.key === "Home" ? minPercent : maxPercent)
      return
    }
    if (event.key !== "ArrowUp" && event.key !== "ArrowDown") return
    event.preventDefault()
    const delta = event.key === "ArrowDown" ? KEYBOARD_SPLIT_STEP : -KEYBOARD_SPLIT_STEP
    setPercent((current) => clampSplitPercent(current + delta, minPercent, maxPercent))
  }

  const topMinHeight = topCollapsed ? 0 : (topMinHeightPx ?? 0)
  const bottomMinHeight = bottomMinHeightPx ?? 0
  const topStyle = topCollapsed
    ? { flex: "0 0 auto", overflow: topOverflow, minHeight: 0, minWidth: 0 }
    : {
        flexBasis: `${percent}%`,
        flexGrow: 0,
        flexShrink: 1,
        overflow: topOverflow,
        minHeight: topMinHeight,
        minWidth: 0,
      }
  const bottomStyle = topCollapsed
    ? { flex: "1 1 0%", overflow: bottomOverflow, minHeight: bottomMinHeight, minWidth: 0 }
    : {
        flexBasis: `${bottomPercent}%`,
        flexGrow: 0,
        flexShrink: 1,
        overflow: bottomOverflow,
        minHeight: bottomMinHeight,
        minWidth: 0,
      }

  return (
    <div
      ref={containerRef}
      data-testid={testId}
      data-split-top={roundSplitPercent(percent)}
      data-collapsed={topCollapsed ? "true" : "false"}
      data-overflowing={overflowing ? "true" : "false"}
      className={cn(
        "flex min-h-0 min-w-0 flex-col",
        scrollOnOverflow && "overflow-y-auto",
        className,
      )}
    >
      <div
        data-testid={topTestId ?? `${testId}-top`}
        className={cn("flex min-h-0 min-w-0 flex-col", topClassName)}
        style={topStyle}
      >
        {top}
      </div>

      <hr
        data-testid={`${testId}-divider`}
        // biome-ignore lint/a11y/noRedundantRoles: explicit role documents the splitter contract
        role="separator"
        aria-orientation="horizontal"
        aria-label={ariaLabel}
        aria-valuemin={minPercent}
        aria-valuemax={maxPercent}
        aria-valuenow={interactionDisabled ? undefined : Math.round(percent)}
        aria-disabled={interactionDisabled ? true : undefined}
        title={overflowing ? "可用高度不足，已锁定分隔条" : undefined}
        tabIndex={interactionDisabled ? -1 : 0}
        style={topCollapsed ? { display: "none" } : undefined}
        onPointerDown={handlePointerDown}
        onPointerMove={handlePointerMove}
        onPointerUp={endDrag}
        onPointerCancel={endDrag}
        onDoubleClick={reset}
        onKeyDown={handleKeyDown}
        className={cn(
          "group relative m-0 h-2 w-full shrink-0 touch-none select-none border-0 bg-transparent",
          "after:absolute after:left-1/2 after:top-1/2 after:h-1 after:w-12 after:-translate-x-1/2 after:-translate-y-1/2",
          "after:rounded-full after:bg-border after:transition-colors after:content-['']",
          "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
          interactionDisabled
            ? "cursor-not-allowed opacity-60"
            : "cursor-row-resize hover:after:bg-primary/60",
          dragging && "after:bg-primary",
        )}
      />

      <div
        data-testid={bottomTestId ?? `${testId}-bottom`}
        className={cn("flex min-h-0 min-w-0 flex-col", bottomClassName)}
        style={bottomStyle}
      >
        {bottom}
      </div>
    </div>
  )
}
