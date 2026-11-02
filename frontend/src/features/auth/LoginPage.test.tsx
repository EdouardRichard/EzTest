import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

const nav = vi.hoisted(() => ({ replace: vi.fn(), push: vi.fn() }))
vi.mock("next/navigation", () => ({
  useRouter: () => ({
    push: nav.push,
    replace: nav.replace,
    refresh: vi.fn(),
    back: vi.fn(),
    forward: vi.fn(),
    prefetch: vi.fn(),
  }),
  usePathname: () => "/login",
  redirect: vi.fn(),
}))

import { getToken, setSession } from "@/lib/auth-storage"
import { AuthProvider } from "./AuthProvider"
import { LoginPage } from "./LoginPage"

const admin = {
  id: "9001",
  username: "admin",
  display_name: "管理员",
  role: "admin" as const,
  status: "active" as const,
}

const fetchMock = vi.fn<typeof fetch>()

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function renderLoginPage(): void {
  render(
    <AuthProvider>
      <LoginPage />
    </AuthProvider>,
  )
}

async function fillCredentials(): Promise<void> {
  fireEvent.change(await screen.findByLabelText("用户名"), { target: { value: "admin" } })
  fireEvent.change(screen.getByLabelText("密码"), { target: { value: "admin123" } })
}

describe("LoginPage", () => {
  beforeEach(() => {
    window.sessionStorage.clear()
    fetchMock.mockReset()
    vi.stubGlobal("fetch", fetchMock)
    nav.push.mockReset()
    nav.replace.mockReset()
  })

  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
    window.sessionStorage.clear()
  })

  it("shows the fixed default credential hint and password field", async () => {
    renderLoginPage()

    expect(
      await screen.findByText("首次启动默认 admin / admin123，登录后请在设置中修改密码"),
    ).toBeInTheDocument()
    expect(screen.getByLabelText("密码")).toHaveAttribute("type", "password")
  })

  it("does not offer a bootstrap or initialize-admin form", async () => {
    renderLoginPage()

    await screen.findByLabelText("用户名")
    expect(screen.queryByText(/初始化管理员|bootstrap|初始化账号/i)).not.toBeInTheDocument()
  })

  it("requires both fields before calling the API", async () => {
    renderLoginPage()
    fireEvent.click(await screen.findByRole("button", { name: "登录" }))

    expect(await screen.findByRole("alert")).toHaveTextContent("请输入用户名和密码")
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it("surfaces the API login error", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ error: { code: "unauthenticated", message: "用户名或密码错误" } }, 401),
    )
    renderLoginPage()
    await fillCredentials()

    fireEvent.click(screen.getByRole("button", { name: "登录" }))

    expect(await screen.findByRole("alert")).toHaveTextContent("用户名或密码错误")
    expect(getToken()).toBeNull()
    expect(nav.push).not.toHaveBeenCalled()
  })

  it("persists the session and enters /versions on success", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        data: { token: "ezt_login", expires_at: "2026-11-07T00:00:00Z", user: admin },
      }),
    )
    renderLoginPage()
    await fillCredentials()

    fireEvent.click(screen.getByRole("button", { name: "登录" }))

    await waitFor(() => expect(nav.push).toHaveBeenCalledWith("/versions"))
    expect(getToken()).toBe("ezt_login")
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/auth/login")
    expect(JSON.parse(String(init?.body))).toEqual({ username: "admin", password: "admin123" })
  })

  it("sends an already authenticated visitor to /versions", async () => {
    setSession("ezt_stored", admin)
    fetchMock.mockResolvedValue(jsonResponse({ data: { user: admin } }))

    renderLoginPage()

    await waitFor(() => expect(nav.replace).toHaveBeenCalledWith("/versions"))
  })
})
