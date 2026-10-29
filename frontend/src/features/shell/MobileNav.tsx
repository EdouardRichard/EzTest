"use client"

import { Menu, X } from "lucide-react"
import { usePathname } from "next/navigation"
import { useEffect, useRef } from "react"

import { SidebarFooter, SidebarHeader, SidebarNavigation } from "./Sidebar"

/** Drawer element id referenced by the hamburger's `aria-controls` (spec §8.3). */
export const MOBILE_NAV_ID = "mobile-nav-drawer"

/** Hamburger element id so closing the drawer can restore focus (spec §8.3). */
export const MOBILE_NAV_TRIGGER_ID = "mobile-nav-toggle"

const DESKTOP_MEDIA_QUERY = "(min-width: 1024px)"

const FOCUSABLE_SELECTOR = [
  "a[href]",
  "button:not([disabled])",
  "input:not([disabled])",
  "select:not([disabled])",
  "textarea:not([disabled])",
  '[tabindex]:not([tabindex="-1"])',
].join(",")

export type MobileMenuButtonProps = {
  open: boolean
  onToggle: () => void
}

/**
 * Topbar hamburger shown below the 1024px breakpoint (spec §8.3). It is the
 * labelled control for the drawer: `aria-expanded` mirrors the drawer state and
 * `aria-controls` points at the dialog element.
 */
export function MobileMenuButton({ open, onToggle }: MobileMenuButtonProps) {
  return (
    <button
      type="button"
      id={MOBILE_NAV_TRIGGER_ID}
      aria-label="导航菜单"
      aria-haspopup="dialog"
      aria-expanded={open}
      aria-controls={MOBILE_NAV_ID}
      onClick={onToggle}
      className="grid h-11 w-11 shrink-0 place-items-center rounded-lg border border-border bg-card text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring max-sm:order-1 lg:hidden"
    >
      <Menu className="h-5 w-5" aria-hidden="true" />
    </button>
  )
}

export type MobileNavProps = {
  open: boolean
  onClose: () => void
}

/**
 * Under-1024 navigation drawer (spec §8.3): an overlay plus a modal dialog with
 * a Tab focus trap, Escape/overlay/route-change close, body scroll lock and
 * focus return to the hamburger. Desktop keeps the fixed `Sidebar`; both
 * render the same navigation content, so the P8 copy and destinations never
 * diverge between layouts.
 */
export function MobileNav({ open, onClose }: MobileNavProps) {
  const pathname = usePathname() ?? ""
  const drawerRef = useRef<HTMLDivElement>(null)
  const previousPathname = useRef(pathname)
  const previouslyFocused = useRef<HTMLElement | null>(null)
  const onCloseRef = useRef(onClose)
  onCloseRef.current = onClose

  // A route change always dismisses the drawer, including ahead-of-render ones.
  useEffect(() => {
    if (previousPathname.current === pathname) return
    previousPathname.current = pathname
    if (open) onCloseRef.current()
  }, [open, pathname])

  // Resizing to the desktop breakpoint hands navigation back to the sidebar.
  useEffect(() => {
    if (!open) return
    if (typeof window === "undefined" || typeof window.matchMedia !== "function") return
    const desktop = window.matchMedia(DESKTOP_MEDIA_QUERY)
    if (desktop.matches) {
      onCloseRef.current()
      return
    }
    const onChange = (event: MediaQueryListEvent) => {
      if (event.matches) onCloseRef.current()
    }
    desktop.addEventListener("change", onChange)
    return () => desktop.removeEventListener("change", onChange)
  }, [open])

  // Focus trap + body scroll lock for the lifetime of one open drawer.
  useEffect(() => {
    if (!open) return
    const drawer = drawerRef.current
    previouslyFocused.current =
      document.activeElement instanceof HTMLElement ? document.activeElement : null
    const previousOverflow = document.body.style.overflow
    document.body.style.overflow = "hidden"

    const focusable = (): HTMLElement[] => {
      if (drawer === null) return []
      return Array.from(drawer.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR))
    }
    focusable()[0]?.focus()

    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault()
        onCloseRef.current()
        return
      }
      if (event.key !== "Tab") return
      const items = focusable()
      if (items.length === 0) {
        event.preventDefault()
        return
      }
      const first = items[0] as HTMLElement
      const last = items[items.length - 1] as HTMLElement
      const active = document.activeElement
      const outside = !(active instanceof Node) || drawer === null || !drawer.contains(active)
      if (event.shiftKey && (active === first || outside)) {
        event.preventDefault()
        last.focus()
      } else if (!event.shiftKey && (active === last || outside)) {
        event.preventDefault()
        first.focus()
      }
    }
    document.addEventListener("keydown", onKeyDown)

    return () => {
      document.removeEventListener("keydown", onKeyDown)
      document.body.style.overflow = previousOverflow
      // Return focus to the hamburger; fall back to whatever opened the drawer.
      const trigger = document.getElementById(MOBILE_NAV_TRIGGER_ID)
      if (trigger instanceof HTMLElement) trigger.focus()
      else previouslyFocused.current?.focus()
    }
  }, [open])

  return (
    // The drawer stays mounted (but `hidden`) so the hamburger's aria-controls
    // always references a real element and axe never reports a dangling IDREF.
    <div className="lg:hidden" hidden={!open}>
      <div
        data-testid="mobile-nav-overlay"
        aria-hidden="true"
        onClick={() => onCloseRef.current()}
        className="fixed inset-0 z-40 bg-slate-950/60 backdrop-blur-sm"
      />
      <div
        ref={drawerRef}
        id={MOBILE_NAV_ID}
        role="dialog"
        aria-modal="true"
        aria-label="主导航菜单"
        className="fixed inset-y-0 left-0 z-50 flex w-72 max-w-[85vw] flex-col border-r border-sidebar-border bg-sidebar text-sidebar-foreground shadow-2xl shadow-black/40"
      >
        <SidebarHeader
          action={
            <button
              type="button"
              aria-label="关闭导航菜单"
              onClick={() => onCloseRef.current()}
              className="grid h-11 w-11 shrink-0 place-items-center rounded-lg text-sidebar-muted transition-colors hover:bg-white/[0.06] hover:text-sidebar-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sky-400/60"
            >
              <X className="h-5 w-5" aria-hidden="true" />
            </button>
          }
        />
        <div className="flex min-h-0 flex-1 flex-col gap-1 overflow-y-auto px-3 py-4">
          <SidebarNavigation mobile onNavigate={() => onCloseRef.current()} />
        </div>
        <SidebarFooter />
      </div>
    </div>
  )
}
