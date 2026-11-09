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

function clearStorage(): void {
  window.localStorage.clear()
  window.sessionStorage.clear()
}

function renderLoginPage(nextPath?: string): void {
  render(
    <AuthProvider>
      <LoginPage nextPath={nextPath} />
    </AuthProvider>,
  )
}

async function fillCredentials(): Promise<void> {
  fireEvent.change(await screen.findByLabelText("用户名"), { target: { value: "admin" } })
  fireEvent.change(screen.getByLabelText("密码"), { target: { value: "test-admin-pass" } })
}

async function submitLogin(): Promise<void> {
  await fillCredentials()
  fireEvent.click(screen.getByRole("button", { name: "登录" }))
}

describe("LoginPage", () => {
  beforeEach(() => {
    clearStorage()
    fetchMock.mockReset()
    vi.stubGlobal("fetch", fetchMock)
    nav.push.mockReset()
    nav.replace.mockReset()
  })

  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
    clearStorage()
  })

  it("does not expose deployment variables or initial-password guidance", async () => {
    renderLoginPage()

    expect(await screen.findByLabelText("用户名")).toBeInTheDocument()
    expect(screen.getByText("使用平台账号进入工作区")).toBeInTheDocument()
    expect(screen.queryByText(/EZTEST_INITIAL_ADMIN_PASSWORD/)).not.toBeInTheDocument()
    expect(screen.queryByText(/初始管理员密码/)).not.toBeInTheDocument()
    expect(screen.queryByText(/部署配置|部署时/)).not.toBeInTheDocument()
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

  it("maps a 401 to the Chinese credential error instead of the raw API message", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ error: { code: "unauthenticated", message: "invalid credentials" } }, 401),
    )
    renderLoginPage()
    await submitLogin()

    expect(await screen.findByRole("alert")).toHaveTextContent("用户名或密码错误")
    expect(getToken()).toBeNull()
    expect(nav.push).not.toHaveBeenCalled()
  })

  it("maps a disabled account 403 to its own Chinese message", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ error: { code: "account_disabled", message: "account is disabled" } }, 403),
    )
    renderLoginPage()
    await submitLogin()

    expect(await screen.findByRole("alert")).toHaveTextContent("账号已被禁用，请联系管理员")
    expect(getToken()).toBeNull()
  })

  it("maps 429 to a Chinese rate-limit message", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ error: { code: "rate_limited", message: "too many requests" } }, 429),
    )
    renderLoginPage()
    await submitLogin()

    expect(await screen.findByRole("alert")).toHaveTextContent("登录尝试过于频繁，请稍后再试")
  })

  it("maps a network failure to a Chinese connectivity message", async () => {
    fetchMock.mockRejectedValue(new TypeError("Failed to fetch"))
    renderLoginPage()
    await submitLogin()

    expect(await screen.findByRole("alert")).toHaveTextContent("网络连接失败，请检查网络后重试")
    expect(getToken()).toBeNull()
  })

  it("persists the session with its expiry and enters /versions on success", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        data: { token: "ezt_login", expires_at: "2099-01-01T00:00:00Z", user: admin },
      }),
    )
    renderLoginPage()
    await submitLogin()

    await waitFor(() => expect(nav.push).toHaveBeenCalledWith("/versions"))
    expect(getToken()).toBe("ezt_login")
    expect(window.localStorage.getItem("eztest.token_expires_at")).toBe("2099-01-01T00:00:00Z")
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/auth/login")
    expect(JSON.parse(String(init?.body))).toEqual({
      username: "admin",
      password: "test-admin-pass",
    })
  })

  it("returns to the original target carried in next", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        data: { token: "ezt_login", expires_at: "2099-01-01T00:00:00Z", user: admin },
      }),
    )
    renderLoginPage("/knowledge?tab=requirements")
    await submitLogin()

    await waitFor(() => expect(nav.push).toHaveBeenCalledWith("/knowledge?tab=requirements"))
  })

  it("ignores an unsafe next target", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        data: { token: "ezt_login", expires_at: "2099-01-01T00:00:00Z", user: admin },
      }),
    )
    renderLoginPage("//evil.example/login")
    await submitLogin()

    await waitFor(() => expect(nav.push).toHaveBeenCalledWith("/versions"))
  })

  it("sends an already authenticated visitor to /versions", async () => {
    setSession("ezt_stored", admin)
    fetchMock.mockResolvedValue(jsonResponse({ data: { user: admin } }))

    renderLoginPage()

    await waitFor(() => expect(nav.replace).toHaveBeenCalledWith("/versions"))
  })

  it("sends an already authenticated visitor to next", async () => {
    setSession("ezt_stored", admin)
    fetchMock.mockResolvedValue(jsonResponse({ data: { user: admin } }))

    renderLoginPage("/knowledge")

    await waitFor(() => expect(nav.replace).toHaveBeenCalledWith("/knowledge"))
  })
})
