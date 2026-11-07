"use client"

import type { ReactNode } from "react"

import { FloatingPanel } from "@/components/ui/FloatingPanel"

/** Dialog title and accessible name of the enlarged case workbench. */
export const DESIGN_FOCUS_TITLE = "用例工作区"

export type DesignFocusDialogProps = {
  open: boolean
  onClose: () => void
  /**
   * The whole workbench subtree (graph over detail, with its toolbars and the
   * persisted 65/35 split). The floating panel re-parents this exact subtree
   * instead of rendering a second mindmap: the canvas, its selection/filter
   * state, the detail fetch and the scroll offsets all survive the toggle, and
   * no test id is duplicated.
   */
  children: ReactNode
}

/**
 * Whole-workbench modal (R-D2-3). Opening moves only the graph + detail
 * workbench into a 90vw × 90vh dialog while the design chat stays inline in the
 * page; closing moves the same subtree back, preserving canvas state, selection,
 * detail fetch and scroll offsets. The workbench keeps its draggable and
 * persisted 65/35 split inside the dialog — there is no separate dialog layout
 * and no focus-specific split constant.
 */
export function DesignFocusDialog({ open, onClose, children }: DesignFocusDialogProps) {
  return (
    <FloatingPanel
      open={open}
      onClose={onClose}
      title={DESIGN_FOCUS_TITLE}
      testIdPrefix="design-focus"
      className="flex min-h-0 flex-1 flex-col"
    >
      {children}
    </FloatingPanel>
  )
}
