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
}

type DragState = {
  startY: number
  startPercent: number
}

/**
 * Vertical two-pane splitter with the accessible divider contract from the
 * approved v0.22 UI: pointer drag, double-click reset, ArrowUp/ArrowDown (5%)
 * and Home reset. The top share is persisted in `localStorage`, so the
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
}: DesignSplitPaneProps) {
  const [percent, setPercent] = useState(() =>
    readStoredSplit(storageKey, defaultPercent, minPercent, maxPercent),
  )
  const containerRef = useRef<HTMLDivElement>(null)
  const dragRef = useRef<DragState | null>(null)
  const [dragging, setDragging] = useState(false)

  // Persist on every committed change (drag, keyboard, double-click reset).
  useEffect(() => {
    writeStoredSplit(storageKey, percent)
  }, [percent, storageKey])

  const bottomPercent = roundSplitPercent(100 - percent)

  function handlePointerDown(event: PointerEvent<HTMLDivElement>): void {
    if (topCollapsed) return
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
    if (topCollapsed) return
    setPercent(defaultPercent)
  }

  function handleKeyDown(event: KeyboardEvent<HTMLDivElement>): void {
    if (topCollapsed) return
    if (event.key === "Home") {
      event.preventDefault()
      reset()
      return
    }
    if (event.key !== "ArrowUp" && event.key !== "ArrowDown") return
    event.preventDefault()
    const delta = event.key === "ArrowDown" ? KEYBOARD_SPLIT_STEP : -KEYBOARD_SPLIT_STEP
    setPercent((current) => clampSplitPercent(current + delta, minPercent, maxPercent))
  }

  const topStyle = topCollapsed
    ? { flex: "0 0 auto", overflow: topOverflow, minHeight: 0, minWidth: 0 }
    : {
        flexBasis: `${percent}%`,
        flexGrow: 0,
        flexShrink: 1,
        overflow: topOverflow,
        minHeight: 0,
        minWidth: 0,
      }
  const bottomStyle = topCollapsed
    ? { flex: "1 1 0%", overflow: bottomOverflow, minHeight: 0, minWidth: 0 }
    : {
        flexBasis: `${bottomPercent}%`,
        flexGrow: 0,
        flexShrink: 1,
        overflow: bottomOverflow,
        minHeight: 0,
        minWidth: 0,
      }

  return (
    <div
      ref={containerRef}
      data-testid={testId}
      data-split-top={roundSplitPercent(percent)}
      data-collapsed={topCollapsed ? "true" : "false"}
      className={cn("flex min-h-0 min-w-0 flex-col", className)}
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
        aria-valuenow={topCollapsed ? undefined : Math.round(percent)}
        aria-disabled={topCollapsed ? true : undefined}
        tabIndex={topCollapsed ? -1 : 0}
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
          topCollapsed
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
