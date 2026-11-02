import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

const pathname = vi.hoisted(() => ({ current: "/issues" }))
vi.mock("next/navigation", () => ({
  usePathname: () => pathname.current,
  useRouter: () => ({
    push: vi.fn(),
    replace: vi.fn(),
    refresh: vi.fn(),
    back: vi.fn(),
    forward: vi.fn(),
    prefetch: vi.fn(),
  }),
}))

const auth = vi.hoisted(() => ({
  user: {
    id: "9001",
    username: "admin",
    display_name: "管理员",
    role: "admin" as const,
    status: "active" as const,
  },
  token: "ezt_token",
  loading: false,
  login: vi.fn(),
  logout: vi.fn(),
  changePassword: vi.fn(),
}))
vi.mock("@/features/auth/AuthProvider", () => ({ useAuth: () => auth }))

const sse = vi.hoisted(() => ({ cleanup: vi.fn() }))
vi.mock("@/lib/sse-client", () => ({
  connectSSE: () => sse.cleanup,
}))

import { AppShell } from "./AppShell"

describe("AppShell", () => {
  beforeEach(() => {
    pathname.current = "/issues"
  })

  afterEach(cleanup)

  it("composes the sidebar, topbar and page content", () => {
    render(
      <AppShell>
        <div>placeholder content</div>
      </AppShell>,
    )

    expect(screen.getByRole("navigation", { name: "主导航" })).toBeInTheDocument()
    expect(screen.getByTestId("topbar-title")).toHaveTextContent("问题")
    expect(screen.getByRole("main")).toHaveTextContent("placeholder content")
    expect(screen.getByText(/开发测试环境 · 受控网络/)).toBeInTheDocument()
  })
})
