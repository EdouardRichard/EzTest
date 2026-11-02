import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

const nav = vi.hoisted(() => ({ replace: vi.fn() }))
vi.mock("next/navigation", () => ({
  useRouter: () => ({
    push: vi.fn(),
    replace: nav.replace,
    refresh: vi.fn(),
    back: vi.fn(),
    forward: vi.fn(),
    prefetch: vi.fn(),
  }),
  usePathname: () => "/settings",
}))

const auth = vi.hoisted(() => ({
  user: {
    id: "1",
    username: "admin",
    display_name: "系统管理员",
    role: "admin" as "admin" | "user",
    status: "active" as "active" | "disabled",
    created_at: "2026-10-31T08:00:00Z",
    updated_at: "2026-10-31T08:00:00Z",
  },
  token: "ezt_token",
  loading: false,
  login: vi.fn(),
  logout: vi.fn(),
  changePassword: vi.fn(),
}))
vi.mock("@/features/auth/AuthProvider", () => ({ useAuth: () => auth }))

vi.mock("./SystemSettings", () => ({ SystemSettings: () => <div>系统设置面板</div> }))
vi.mock("./LLMSettings", () => ({ LLMSettings: () => <div>LLM 配置面板</div> }))
vi.mock("./UsersPanel", () => ({ UsersPanel: () => <div>用户管理面板</div> }))
vi.mock("./AuditPanel", () => ({ AuditPanel: () => <div>审计日志面板</div> }))

import { normalizeSettingsTab, SettingsPage } from "./SettingsPage"

describe("SettingsPage", () => {
  beforeEach(() => {
    nav.replace.mockReset()
    auth.user = {
      id: "1",
      username: "admin",
      display_name: "系统管理员",
      role: "admin",
      status: "active",
      created_at: "2026-10-31T08:00:00Z",
      updated_at: "2026-10-31T08:00:00Z",
    }
  })

  afterEach(cleanup)

  it("shows the four tabs with the general panel by default", () => {
    render(<SettingsPage />)

    expect(screen.getByRole("tab", { name: "常规设置" })).toHaveAttribute("aria-selected", "true")
    expect(screen.getByRole("tab", { name: "LLM 配置" })).toBeVisible()
    expect(screen.getByRole("tab", { name: "用户管理" })).toBeVisible()
    expect(screen.getByRole("tab", { name: "审计日志" })).toBeVisible()
    expect(screen.getByText("系统设置面板")).toBeVisible()
    expect(screen.queryByText("LLM 配置面板")).toBeNull()
  })

  it("opens the tab named by the deep link", () => {
    render(<SettingsPage initialTab="audit" />)

    expect(screen.getByRole("tab", { name: "审计日志" })).toHaveAttribute("aria-selected", "true")
    expect(screen.getByText("审计日志面板")).toBeVisible()
  })

  it("falls back to general for an unknown tab", () => {
    render(<SettingsPage initialTab="bogus" />)

    expect(screen.getByRole("tab", { name: "常规设置" })).toHaveAttribute("aria-selected", "true")
    expect(screen.getByText("系统设置面板")).toBeVisible()
  })

  it("updates the URL and panel when a tab is clicked", () => {
    render(<SettingsPage />)

    fireEvent.click(screen.getByRole("tab", { name: "用户管理" }))

    expect(nav.replace).toHaveBeenCalledWith("/settings?tab=users")
    expect(screen.getByText("用户管理面板")).toBeVisible()
    expect(screen.queryByText("系统设置面板")).toBeNull()
  })

  it("blocks the console for non-admin users", () => {
    auth.user = {
      id: "9001",
      username: "qa01",
      display_name: "QA 01",
      role: "user",
      status: "active",
      created_at: "2026-10-31T08:00:00Z",
      updated_at: "2026-10-31T08:00:00Z",
    }

    render(<SettingsPage />)

    expect(screen.getByText("设置中心仅管理员可用")).toBeVisible()
    expect(screen.queryByRole("tab")).toBeNull()
  })

  it("normalizes tab names", () => {
    expect(normalizeSettingsTab("llm")).toBe("llm")
    expect(normalizeSettingsTab("users")).toBe("users")
    expect(normalizeSettingsTab("audit")).toBe("audit")
    expect(normalizeSettingsTab("general")).toBe("general")
    expect(normalizeSettingsTab(undefined)).toBe("general")
    expect(normalizeSettingsTab("")).toBe("general")
  })
})
