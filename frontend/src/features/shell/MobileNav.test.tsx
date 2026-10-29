import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { useState } from "react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const nav = vi.hoisted(() => ({ pathname: "/" }))

vi.mock("next/navigation", () => ({
  usePathname: () => nav.pathname,
}))

// Both the fixed sidebar and the drawer render the same auth-filtered nav list.
vi.mock("@/features/auth/AuthProvider", () => ({
  useAuth: () => ({
    user: { id: 1, username: "admin", role: "admin", status: "active" },
    loading: false,
    logout: vi.fn(),
  }),
}))

import { MOBILE_NAV_ID, MOBILE_NAV_TRIGGER_ID, MobileMenuButton, MobileNav } from "./MobileNav"
import { Sidebar } from "./Sidebar"

/** Mirrors the AppShell wiring: one open state drives the trigger and drawer. */
function Harness() {
  const [open, setOpen] = useState(false)
  return (
    <>
      <MobileMenuButton open={open} onToggle={() => setOpen((previous) => !previous)} />
      <MobileNav open={open} onClose={() => setOpen(false)} />
    </>
  )
}

function trigger(): HTMLElement {
  return screen.getByRole("button", { name: "导航菜单" })
}

function openDrawer(): HTMLElement {
  fireEvent.click(trigger())
  return screen.getByRole("dialog", { name: "主导航菜单" })
}

beforeEach(() => {
  nav.pathname = "/"
})

afterEach(() => {
  cleanup()
  document.body.style.overflow = ""
})

describe("MobileNav", () => {
  it("shows the hamburger only below lg and keeps the desktop sidebar fixed from lg", () => {
    render(<Harness />)

    const menuButton = trigger()
    expect(menuButton.className).toContain("lg:hidden")
    expect(menuButton.id).toBe(MOBILE_NAV_TRIGGER_ID)
    expect(menuButton.getAttribute("aria-controls")).toBe(MOBILE_NAV_ID)
    // Kept mounted but hidden so the aria-controls IDREF always resolves.
    expect(document.getElementById(MOBILE_NAV_ID)?.closest("[hidden]")).not.toBeNull()
    expect(screen.queryByRole("dialog", { name: "主导航菜单" })).toBeNull()

    cleanup()
    render(<Sidebar />)

    const aside = screen.getByRole("complementary")
    expect(aside.className).toContain("fixed")
    expect(aside.className).toContain("hidden")
    expect(aside.className).toContain("lg:flex")
  })

  it("opens the drawer, moves focus into it, locks body scroll and flips aria-expanded", () => {
    render(<Harness />)
    expect(trigger().getAttribute("aria-expanded")).toBe("false")

    const dialog = openDrawer()

    expect(trigger().getAttribute("aria-expanded")).toBe("true")
    expect(document.getElementById(MOBILE_NAV_ID)?.closest("[hidden]")).toBeNull()
    expect(dialog.getAttribute("aria-modal")).toBe("true")
    expect(within(dialog).getByRole("link", { name: /概览/ })).not.toBeNull()
    expect(dialog.contains(document.activeElement)).toBe(true)
    expect(document.body.style.overflow).toBe("hidden")
  })

  it("keeps focus trapped inside the drawer for Tab and Shift+Tab", () => {
    render(<Harness />)
    const dialog = openDrawer()
    const focusables = Array.from(
      dialog.querySelectorAll<HTMLElement>("a[href], button:not([disabled])"),
    )
    expect(focusables.length).toBeGreaterThan(1)
    const first = focusables[0] as HTMLElement
    const last = focusables[focusables.length - 1] as HTMLElement

    last.focus()
    fireEvent.keyDown(document, { key: "Tab" })
    expect(document.activeElement).toBe(first)

    first.focus()
    fireEvent.keyDown(document, { key: "Tab", shiftKey: true })
    expect(document.activeElement).toBe(last)
  })

  it("closes on Escape and returns focus to the hamburger", () => {
    render(<Harness />)
    openDrawer()
    expect(document.body.style.overflow).toBe("hidden")

    fireEvent.keyDown(document, { key: "Escape" })

    expect(screen.queryByRole("dialog", { name: "主导航菜单" })).toBeNull()
    expect(trigger().getAttribute("aria-expanded")).toBe("false")
    expect(document.activeElement).toBe(trigger())
    expect(document.body.style.overflow).toBe("")
  })

  it("closes when the overlay is activated and returns focus to the hamburger", () => {
    render(<Harness />)
    openDrawer()

    fireEvent.click(screen.getByTestId("mobile-nav-overlay"))

    expect(screen.queryByRole("dialog", { name: "主导航菜单" })).toBeNull()
    expect(document.activeElement).toBe(trigger())
    expect(document.body.style.overflow).toBe("")
  })

  it("closes automatically when the route changes", () => {
    const view = render(<Harness />)
    openDrawer()

    nav.pathname = "/issues"
    view.rerender(<Harness />)

    expect(screen.queryByRole("dialog", { name: "主导航菜单" })).toBeNull()
    expect(trigger().getAttribute("aria-expanded")).toBe("false")
    expect(document.body.style.overflow).toBe("")
  })

  it("keeps the P8 copy and 44px touch targets inside the drawer", () => {
    render(<Harness />)
    const dialog = openDrawer()

    expect(within(dialog).getByText("P8 · 体验硬化")).not.toBeNull()
    const overview = within(dialog).getByRole("link", { name: /概览/ })
    expect(overview.className).toContain("min-h-11")
    expect(within(dialog).getByRole("button", { name: "关闭导航菜单" }).className).toContain("h-11")
  })
})
