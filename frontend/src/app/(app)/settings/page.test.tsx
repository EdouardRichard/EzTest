import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

const mocks = vi.hoisted(() => ({
  role: "user" as "user" | "admin",
}))

vi.mock("@/features/auth/AuthProvider", () => ({
  useAuth: () => ({
    user: {
      id: 1,
      username: mocks.role === "admin" ? "admin" : "qa01",
      role: mocks.role,
      status: "active",
    },
    loading: false,
    login: vi.fn(),
    logout: vi.fn(),
  }),
}))

vi.mock("next-themes", () => ({
  useTheme: () => ({ theme: "system", setTheme: vi.fn() }),
}))

vi.mock("@/features/settings/SettingsTabs", () => ({
  SettingsTabs: () => <div data-testid="settings-tabs-stub" />,
}))

import SettingsPage from "./page"

describe("/settings route", () => {
  afterEach(() => {
    cleanup()
    mocks.role = "user"
  })

  it("shows the admin tabs for an administrator", () => {
    mocks.role = "admin"
    render(<SettingsPage />)

    expect(screen.getByTestId("settings-tabs-stub")).toBeTruthy()
    expect(screen.queryByText("外观")).toBeNull()
    expect(screen.getByText("管理员")).toBeTruthy()
  })

  it("keeps only the theme settings for a regular user", () => {
    mocks.role = "user"
    render(<SettingsPage />)

    expect(screen.getByText("外观")).toBeTruthy()
    expect(screen.queryByTestId("settings-tabs-stub")).toBeNull()
    expect(screen.queryByText("基础配置")).toBeNull()
    expect(screen.queryByText("审计日志")).toBeNull()
    expect(screen.getByText("普通用户")).toBeTruthy()
  })
})
