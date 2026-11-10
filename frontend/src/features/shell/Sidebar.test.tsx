import { cleanup, render, screen, within } from "@testing-library/react"
import { renderToString } from "react-dom/server"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

const pathname = vi.hoisted(() => ({ current: "/resources" }))
vi.mock("next/navigation", () => ({ usePathname: () => pathname.current }))

const auth = vi.hoisted(() => ({ role: "admin" as "admin" | "user" }))
vi.mock("@/features/auth/AuthProvider", () => ({
  useAuth: () => ({
    user: {
      id: "9001",
      username: "admin",
      display_name: "管理员",
      role: auth.role,
      status: "active",
    },
    token: "ezt_token",
    loading: false,
    login: vi.fn(),
    logout: vi.fn(),
    changePassword: vi.fn(),
  }),
}))

import { Sidebar } from "./Sidebar"

describe("Sidebar", () => {
  beforeEach(() => {
    auth.role = "admin"
    pathname.current = "/resources"
  })

  afterEach(cleanup)

  it("renders the eight navigation entries grouped into workspace and platform", () => {
    render(<Sidebar />)

    const nav = screen.getByRole("navigation", { name: "主导航" })
    const links = within(nav).getAllByRole("link")
    expect(links).toHaveLength(8)
    expect(links.map((link) => link.textContent?.replace(/P1/g, "").trim())).toEqual([
      "版本",
      "问题",
      "报告",
      "知识",
      "资源",
      "质量",
      "Agent 中心",
      "设置",
    ])
    expect(screen.getByText("工作区")).toBeInTheDocument()
    expect(screen.getByText("平台")).toBeInTheDocument()
    expect(screen.getByText("EzTest")).toBeInTheDocument()
  })

  it("points each link at its route", () => {
    render(<Sidebar />)

    expect(screen.getByRole("link", { name: /^版本/ })).toHaveAttribute("href", "/versions")
    expect(screen.getByRole("link", { name: /^Agent 中心/ })).toHaveAttribute("href", "/agents")
    expect(screen.getByRole("link", { name: /^设置/ })).toHaveAttribute("href", "/settings")
  })

  it("highlights exactly the route that owns the current page", () => {
    render(<Sidebar />)

    expect(screen.getByRole("link", { name: /^资源/ })).toHaveAttribute("aria-current", "page")
    expect(screen.getByRole("link", { name: /^版本/ })).not.toHaveAttribute("aria-current")
  })

  it("keeps the parent section active on nested routes", () => {
    pathname.current = "/versions/1024"
    render(<Sidebar />)

    expect(screen.getByRole("link", { name: /^版本/ })).toHaveAttribute("aria-current", "page")
    expect(screen.getByRole("link", { name: /^资源/ })).not.toHaveAttribute("aria-current")
  })

  it("shows the admin-only platform entries to an administrator", () => {
    render(<Sidebar />)

    const nav = screen.getByRole("navigation", { name: "主导航" })
    expect(within(nav).getAllByRole("link")).toHaveLength(8)
    expect(screen.getByRole("link", { name: /^质量/ })).toBeInTheDocument()
    expect(screen.getByRole("link", { name: /^Agent 中心/ })).toBeInTheDocument()
    expect(screen.getByRole("link", { name: /^设置/ })).toBeInTheDocument()
    expect(screen.getByText("平台")).toBeInTheDocument()
  })

  it("hides the admin-only platform entries from a normal user", () => {
    auth.role = "user"
    render(<Sidebar />)

    const nav = screen.getByRole("navigation", { name: "主导航" })
    const links = within(nav).getAllByRole("link")
    expect(links).toHaveLength(5)
    expect(links.map((link) => link.textContent?.trim())).toEqual([
      "版本",
      "问题",
      "报告",
      "知识",
      "资源",
    ])
    expect(screen.queryByRole("link", { name: /^质量/ })).not.toBeInTheDocument()
    expect(screen.queryByRole("link", { name: /^Agent 中心/ })).not.toBeInTheDocument()
    expect(screen.queryByRole("link", { name: /^设置/ })).not.toBeInTheDocument()
    expect(screen.queryByText("平台")).not.toBeInTheDocument()
  })

  it("renders the non-admin menu on the server render so hydration matches", () => {
    // The admin entries are only revealed after mount (localStorage is not
    // available during SSR); the initial render must never include them even
    // though the authenticated user is an administrator.
    const html = renderToString(<Sidebar />)

    expect(html).toContain("资源")
    expect(html).not.toContain("质量")
    expect(html).not.toContain("Agent 中心")
    expect(html).not.toContain("平台")
  })

  it("shows the controlled-environment notice at the bottom", () => {
    render(<Sidebar />)

    expect(screen.getByText(/开发测试环境 · 受控网络/)).toBeInTheDocument()
    expect(screen.getByText(/AI 可读取测试账号凭据/)).toBeInTheDocument()
  })
})
