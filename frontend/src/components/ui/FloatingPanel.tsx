"use client"

import { X } from "lucide-react"
import { type ReactNode, useEffect, useId, useLayoutEffect, useRef, useState } from "react"
import { createPortal } from "react-dom"

import { cn } from "./cn"

// The panel measures and moves DOM nodes; on the server there is nothing to
// measure, and rendering with a layout effect would only add an SSR warning.
const useIsomorphicLayoutEffect = typeof window === "undefined" ? useEffect : useLayoutEffect

const FOCUSABLE_SELECTOR = [
  "a[href]",
  "button:not([disabled])",
  "textarea:not([disabled])",
  "input:not([disabled])",
  "select:not([disabled])",
  "[tabindex]:not([tabindex='-1'])",
].join(",")

type ScrollOffset = {
  element: HTMLElement
  top: number
  left: number
}

/**
 * Scroll offsets inside a subtree that is about to be re-parented. Reparenting
 * a scrolled element can reset its offset in some engines, so the primitive
 * saves and restores it around the DOM move instead of assuming the browser
 * keeps it.
 */
function scrollOffsetsWithin(root: HTMLElement): ScrollOffset[] {
  const offsets: ScrollOffset[] = []
  for (const element of Array.from(root.querySelectorAll<HTMLElement>("*"))) {
    if (element.scrollTop !== 0 || element.scrollLeft !== 0) {
      offsets.push({ element, top: element.scrollTop, left: element.scrollLeft })
    }
  }
  return offsets
}

function restoreScrollOffsets(offsets: readonly ScrollOffset[]): void {
  for (const offset of offsets) {
    offset.element.scrollTop = offset.top
    offset.element.scrollLeft = offset.left
  }
}

function firstFocusableIn(dialog: HTMLElement): HTMLElement | null {
  return dialog.querySelector<HTMLElement>(FOCUSABLE_SELECTOR)
}

function lastFocusableIn(dialog: HTMLElement): HTMLElement | null {
  const focusables = dialog.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR)
  return focusables.length === 0 ? null : focusables[focusables.length - 1]
}

export type FloatingPanelProps = {
  open: boolean
  onClose: () => void
  title: string
  children: ReactNode
  /**
   * Prefix for the floating layer's own test ids (`<prefix>-dialog`, `-close`,
   * `-backdrop`, `-overlay`, `-title`, `-content`). The caller's inline content
   * keeps its own ids: there is never a second copy of it.
   */
  testIdPrefix?: string
  /** Extra classes for the inline slot that holds the content while closed. */
  className?: string
}

/**
 * Reusable enlarged floating layer (spec §9.1): a 90vw × 90vh body-level
 * portal with page margins, a focus trap, body scroll lock and ESC/backdrop
 * close; below the `sm` breakpoint it degrades to a full-screen sheet.
 *
 * The children are mounted exactly once: they render through a portal whose
 * container is a stable host `<div>`, and opening the panel only moves that
 * host node into the floating dialog (closing moves it back). Reparenting the
 * container instead of re-rendering the children keeps component state
 * (composer draft), scroll offsets and hook subscriptions (useConversation /
 * SSE snapshot) alive, and because the portal container moves with the content
 * React's delegated events keep working on the moved subtree.
 */
export function FloatingPanel({
  open,
  onClose,
  title,
  children,
  testIdPrefix = "floating-panel",
  className,
}: FloatingPanelProps) {
  const titleId = useId()
  const inlineRef = useRef<HTMLDivElement>(null)
  const hostRef = useRef<HTMLDivElement>(null)
  const slotRef = useRef<HTMLDivElement>(null)
  const dialogRef = useRef<HTMLDivElement>(null)
  const [mounted, setMounted] = useState(false)
  const [contentHost, setContentHost] = useState<HTMLDivElement | null>(null)

  // `onClose` usually changes identity on every parent render; keep it in a ref
  // so the effects below only depend on the panel being open.
  const onCloseRef = useRef(onClose)
  onCloseRef.current = onClose

  useEffect(() => {
    setMounted(true)
  }, [])

  // Portal target for the content. It is a plain host node rendered inline, so
  // the content starts (and ends) in the caller's layout, but React renders the
  // children into it through a portal: their fiber identity is independent of
  // where the node currently lives in the DOM.
  useIsomorphicLayoutEffect(() => {
    setContentHost(hostRef.current)
  }, [])

  useIsomorphicLayoutEffect(() => {
    if (!open || !mounted) return undefined
    const host = hostRef.current
    const slot = slotRef.current
    const inline = inlineRef.current
    if (host === null || slot === null || inline === null) return undefined

    const openingOffsets = scrollOffsetsWithin(host)
    slot.appendChild(host)
    restoreScrollOffsets(openingOffsets)

    return () => {
      const closingOffsets = scrollOffsetsWithin(host)
      inline.appendChild(host)
      restoreScrollOffsets(closingOffsets)
    }
  }, [open, mounted])

  useEffect(() => {
    if (!open || !mounted) return undefined
    const dialog = dialogRef.current
    if (dialog === null) return undefined

    const restoreFocus =
      document.activeElement instanceof HTMLElement ? document.activeElement : null
    const previousOverflow = document.body.style.overflow
    const previousPaddingRight = document.body.style.paddingRight
    // Compensate for the scrollbar the lock removes, so the page does not jump.
    const scrollbarWidth = window.innerWidth - document.documentElement.clientWidth
    document.body.style.overflow = "hidden"
    if (scrollbarWidth > 0) document.body.style.paddingRight = `${scrollbarWidth}px`
    dialog.focus()

    // Native listener: the chat subtree is moved into the dialog, so its React
    // fibers are not descendants of the dialog's fiber (the DOM is). A React
    // onKeyDown on the dialog would not see those events; a DOM listener does.
    const onDialogKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Tab") return
      const first = firstFocusableIn(dialog)
      const last = lastFocusableIn(dialog)
      if (first === null || last === null) return
      const active = document.activeElement
      const activeInside = active instanceof Node && dialog.contains(active)
      if (event.shiftKey) {
        if (!activeInside || active === first) {
          event.preventDefault()
          last.focus()
        }
        return
      }
      if (!activeInside || active === last || active === dialog) {
        event.preventDefault()
        first.focus()
      }
    }

    const onWindowKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return
      event.preventDefault()
      onCloseRef.current()
    }

    dialog.addEventListener("keydown", onDialogKeyDown)
    window.addEventListener("keydown", onWindowKeyDown)

    return () => {
      dialog.removeEventListener("keydown", onDialogKeyDown)
      window.removeEventListener("keydown", onWindowKeyDown)
      document.body.style.overflow = previousOverflow
      document.body.style.paddingRight = previousPaddingRight
      if (restoreFocus?.isConnected) restoreFocus.focus()
    }
  }, [open, mounted])

  return (
    <>
      <div ref={inlineRef} data-floating-panel-inline="" className={className}>
        <div
          ref={hostRef}
          data-floating-panel-host=""
          data-floating-panel-state={open ? "open" : "closed"}
          className="flex h-full min-h-0 flex-col"
        />
      </div>
      {contentHost === null ? null : createPortal(children, contentHost)}
      {open && mounted
        ? createPortal(
            <div
              data-testid={`${testIdPrefix}-overlay`}
              className="fixed inset-0 z-50 flex items-center justify-center p-4 max-sm:p-0"
            >
              <button
                type="button"
                tabIndex={-1}
                aria-label="关闭浮层"
                data-testid={`${testIdPrefix}-backdrop`}
                onClick={() => onCloseRef.current()}
                className="absolute inset-0 h-full w-full cursor-default bg-slate-950/55 backdrop-blur-[1px]"
              />
              <div
                ref={dialogRef}
                role="dialog"
                aria-modal="true"
                aria-labelledby={titleId}
                tabIndex={-1}
                data-testid={`${testIdPrefix}-dialog`}
                className={cn(
                  "relative z-10 flex h-[90vh] w-[90vw] flex-col overflow-hidden",
                  "rounded-xl border border-border bg-card text-card-foreground shadow-2xl",
                  // Mobile: the floating card becomes a full-screen sheet.
                  "max-sm:h-dvh max-sm:max-h-none max-sm:w-full max-sm:max-w-none",
                  "max-sm:rounded-none max-sm:border-0",
                )}
              >
                <header className="flex shrink-0 items-center justify-between gap-3 border-b border-border px-4 py-2.5">
                  <h2
                    id={titleId}
                    data-testid={`${testIdPrefix}-title`}
                    className="truncate text-sm font-semibold text-foreground"
                  >
                    {title}
                  </h2>
                  <button
                    type="button"
                    data-testid={`${testIdPrefix}-close`}
                    aria-label={`关闭${title}`}
                    onClick={() => onCloseRef.current()}
                    className="rounded-md p-1 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  >
                    <X className="size-4" aria-hidden="true" />
                  </button>
                </header>
                <div
                  ref={slotRef}
                  data-testid={`${testIdPrefix}-content`}
                  className="flex min-h-0 flex-1 flex-col overflow-hidden"
                />
              </div>
            </div>,
            document.body,
          )
        : null}
    </>
  )
}
