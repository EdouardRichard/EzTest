"use client"

import type { ReactNode } from "react"

import { FloatingPanel } from "@/components/ui/FloatingPanel"

/** Dialog title and accessible name of the focused mindmap workspace. */
export const DESIGN_FOCUS_TITLE = "脑图与用例详情"

/**
 * Fixed split of the focus layout (spec §9.3): mindmap 65%, detail 35%. The
 * inline three-layer layout keeps its own draggable chat/mindmap/detail split;
 * this pair only applies while the floating dialog is open.
 */
export const DESIGN_FOCUS_SPLITS = { mindmap: 65, detail: 35 } as const

export type DesignFocusDialogProps = {
  open: boolean
  onClose: () => void
  /**
   * The inline three-layer region (chat / dividers / mindmap / detail). The
   * floating panel re-parents this exact subtree instead of rendering a second
   * mindmap: the canvas, its selection/filter state, the detail fetch and the
   * scroll offsets all survive the toggle, and no test id is duplicated.
   */
  children: ReactNode
}

/**
 * Enlarged mindmap + case detail workspace (spec §9.3). It is the same inline
 * three-layer region rendered through `FloatingPanel`: opening moves the host
 * into the 90vw × 90vh dialog while `DesignTab` switches to the fixed 65/35
 * split and hides the chat layer; closing moves the host back and restores the
 * draggable layout, the prior selection and the scroll offsets.
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
