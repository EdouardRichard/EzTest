import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

vi.mock("@/features/auth/AuthProvider", () => ({
  useAuth: () => ({
    user: { id: 1, username: "admin", role: "admin", status: "active" },
    logout: vi.fn(),
  }),
}))

vi.mock("next-themes", () => ({
  useTheme: () => ({ resolvedTheme: "light", setTheme: vi.fn() }),
}))

vi.mock("./VersionSwitcher", () => ({
  VersionSwitcher: () => <div data-testid="version-switcher" />,
}))

vi.mock("./search/SearchCommand", () => ({
  SearchCommand: () => <button data-testid="search-command" type="button" />,
}))

vi.mock("./todos/TodoButton", () => ({
  TodoButton: () => <button data-testid="todo-button" type="button" />,
}))

import { Topbar } from "./Topbar"

afterEach(cleanup)

describe("Topbar", () => {
  it("keeps the P8 shell chips and mounts the version switcher, search and todo centre", () => {
    render(<Topbar navOpen={false} onToggleNav={vi.fn()} />)

    expect(screen.getByTestId("version-switcher")).not.toBeNull()
    expect(screen.getByTestId("search-command")).not.toBeNull()
    expect(screen.getByTestId("todo-button")).not.toBeNull()
    expect(screen.getByText("P8 · 体验硬化")).not.toBeNull()
    expect(screen.getByText("开发环境")).not.toBeNull()
    expect(screen.getByRole("button", { name: "切换到深色主题" })).not.toBeNull()
    expect(screen.getByRole("button", { name: "打开用户菜单" })).not.toBeNull()
  })

  it("exposes the mobile navigation trigger with its expanded state", () => {
    const onToggleNav = vi.fn()
    const view = render(<Topbar navOpen={false} onToggleNav={onToggleNav} />)

    const trigger = screen.getByRole("button", { name: "导航菜单" })
    expect(trigger.getAttribute("aria-expanded")).toBe("false")
    expect(trigger.getAttribute("aria-controls")).toBe("mobile-nav-drawer")
    expect(trigger.className).toContain("lg:hidden")
    fireEvent.click(trigger)
    expect(onToggleNav).toHaveBeenCalledTimes(1)

    view.rerender(<Topbar navOpen onToggleNav={onToggleNav} />)
    expect(trigger.getAttribute("aria-expanded")).toBe("true")
  })
})
