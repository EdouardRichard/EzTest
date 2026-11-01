"use client"

import { useEffect } from "react"

const FOCUSABLE_SELECTOR = [
  "a[href]",
  "button:not([disabled])",
  "textarea:not([disabled])",
  "input:not([disabled])",
  "select:not([disabled])",
  "[tabindex]:not([tabindex='-1'])",
].join(",")

/**
 * Minimal dialog focus trap used by the code tab drawers (spec §9.2/§9.6):
 * focus moves into the open dialog, Tab cycles inside it, and the previously
 * focused element is restored when the dialog closes. Escape-to-close stays in
 * the shared `Dialog` component.
 */
export function useFocusTrap(open: boolean): void {
  useEffect(() => {
    if (!open) return undefined

    const dialog = document.querySelector<HTMLElement>('[role="dialog"][aria-modal="true"]')
    if (dialog === null) return undefined

    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null
    const focusables = (): HTMLElement[] =>
      Array.from(dialog.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR))

    const first = focusables()[0]
    first?.focus()

    function onKeyDown(event: KeyboardEvent): void {
      if (event.key !== "Tab") return
      const items = focusables()
      if (items.length === 0) return
      const firstItem = items[0]
      const lastItem = items[items.length - 1]
      if (event.shiftKey && document.activeElement === firstItem) {
        event.preventDefault()
        lastItem.focus()
      } else if (!event.shiftKey && document.activeElement === lastItem) {
        event.preventDefault()
        firstItem.focus()
      }
    }

    dialog.addEventListener("keydown", onKeyDown)
    return () => {
      dialog.removeEventListener("keydown", onKeyDown)
      previous?.focus()
    }
  }, [open])
}
