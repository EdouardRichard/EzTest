import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { useState } from "react"
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
  usePathname: () => "/",
  redirect: vi.fn(),
}))

import { clearSession, getStoredUser, getToken, setSession } from "@/lib/auth-storage"
import { AuthProvider, RequireAuth, useAuth } from "./AuthProvider"

const user = {
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

function requestBody(call: [RequestInfo | URL, RequestInit | undefined]): unknown {
  const body = call[1]?.body
  return typeof body === "string" ? JSON.parse(body) : null
}

function AuthProbe() {
  const auth = useAuth()
  return (
    <div>
      <span data-testid="loading">{auth.loading ? "loading" : "ready"}</span>
      <span data-testid="user">{auth.user?.username ?? "anon"}</span>
      <span data-testid="token">{auth.token ?? "none"}</span>
      <button type="button" onClick={() => void auth.login("admin", "admin123").catch(() => {})}>
        probe-login
      </button>
      <button
        type="button"
        onClick={() => void auth.changePassword("admin123", "newpass123").catch(() => {})}
      >
        probe-change-password
      </button>
      <button type="button" onClick={() => void auth.logout().catch(() => {})}>
        probe-logout
      </button>
    </div>
  )
}

function LoginErrorProbe() {
  const auth = useAuth()
  const [error, setError] = useState("")
  return (
    <div>
      <button
        type="button"
        onClick={() =>
          void auth.login("admin", "nope").catch((caught: unknown) => {
            setError(caught instanceof Error ? caught.message : "login failed")
          })
        }
      >
        probe-login
      </button>
      <span data-testid="error">{error}</span>
    </div>
  )
}

function renderProvider(): void {
  render(
    <AuthProvider>
      <AuthProbe />
    </AuthProvider>,
  )
}

describe("AuthProvider", () => {
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

  it("settles to a signed-out state without calling the API", async () => {
    renderProvider()

    await waitFor(() => expect(screen.getByTestId("loading")).toHaveTextContent("ready"))
    expect(screen.getByTestId("user")).toHaveTextContent("anon")
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it("restores a stored session from GET /api/auth/me", async () => {
    setSession("ezt_stored", user)
    fetchMock.mockResolvedValue(jsonResponse({ data: { user } }))

    renderProvider()

    await waitFor(() => expect(screen.getByTestId("user")).toHaveTextContent("admin"))
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/auth/me")
    expect(new Headers(init?.headers).get("Authorization")).toBe("Bearer ezt_stored")
    expect(screen.getByTestId("token")).toHaveTextContent("ezt_stored")
  })

  it("clears a stored session when GET /api/auth/me is unauthorized", async () => {
    setSession("ezt_expired", user)
    fetchMock.mockResolvedValue(
      jsonResponse({ error: { code: "unauthenticated", message: "login required" } }, 401),
    )

    renderProvider()

    await waitFor(() => expect(screen.getByTestId("loading")).toHaveTextContent("ready"))
    expect(screen.getByTestId("user")).toHaveTextContent("anon")
    expect(getToken()).toBeNull()
  })

  it("logs in, persists the session and calls the login endpoint", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ data: { token: "ezt_new", expires_at: "2026-11-07T00:00:00Z", user } }),
    )

    renderProvider()
    await waitFor(() => expect(screen.getByTestId("loading")).toHaveTextContent("ready"))
    fireEvent.click(screen.getByRole("button", { name: "probe-login" }))

    await waitFor(() => expect(screen.getByTestId("user")).toHaveTextContent("admin"))
    expect(screen.getByTestId("token")).toHaveTextContent("ezt_new")
    expect(getToken()).toBe("ezt_new")
    expect(getStoredUser()).toEqual(user)

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/auth/login")
    expect(init?.method).toBe("POST")
    expect(requestBody([url, init])).toEqual({ username: "admin", password: "admin123" })
  })

  it("propagates login failures without persisting a session", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ error: { code: "unauthenticated", message: "用户名或密码错误" } }, 401),
    )

    render(
      <AuthProvider>
        <LoginErrorProbe />
      </AuthProvider>,
    )
    fireEvent.click(screen.getByRole("button", { name: "probe-login" }))

    await waitFor(() => expect(screen.getByTestId("error")).toHaveTextContent("用户名或密码错误"))
    expect(getToken()).toBeNull()
  })

  it("logs out through the API and clears the local session", async () => {
    setSession("ezt_a", user)
    fetchMock.mockImplementation(async (input) => {
      const url = String(input)
      if (url.endsWith("/api/auth/me")) return jsonResponse({ data: { user } })
      return new Response(null, { status: 204 })
    })

    renderProvider()
    await waitFor(() => expect(screen.getByTestId("user")).toHaveTextContent("admin"))
    fireEvent.click(screen.getByRole("button", { name: "probe-logout" }))

    await waitFor(() => expect(screen.getByTestId("user")).toHaveTextContent("anon"))
    expect(getToken()).toBeNull()
    const logoutCall = fetchMock.mock.calls.find((call) =>
      String(call[0]).endsWith("/api/auth/logout"),
    )
    expect(logoutCall?.[1]?.method).toBe("POST")
  })

  it("posts the old and new password to POST /api/auth/password", async () => {
    setSession("ezt_a", user)
    fetchMock.mockImplementation(async (input) => {
      const url = String(input)
      if (url.endsWith("/api/auth/me")) return jsonResponse({ data: { user } })
      return new Response(null, { status: 204 })
    })

    renderProvider()
    await waitFor(() => expect(screen.getByTestId("user")).toHaveTextContent("admin"))
    fireEvent.click(screen.getByRole("button", { name: "probe-change-password" }))

    await waitFor(() => {
      const changeCall = fetchMock.mock.calls.find((call) =>
        String(call[0]).endsWith("/api/auth/password"),
      )
      expect(changeCall).toBeDefined()
      expect(changeCall?.[1]?.method).toBe("POST")
      expect(requestBody([changeCall?.[0] ?? "", changeCall?.[1]])).toEqual({
        old_password: "admin123",
        new_password: "newpass123",
      })
    })
  })

  it("reacts to the auth-cleared event", async () => {
    setSession("ezt_a", user)
    fetchMock.mockResolvedValue(jsonResponse({ data: { user } }))

    renderProvider()
    await waitFor(() => expect(screen.getByTestId("user")).toHaveTextContent("admin"))

    clearSession()

    await waitFor(() => expect(screen.getByTestId("user")).toHaveTextContent("anon"))
    expect(screen.getByTestId("token")).toHaveTextContent("none")
  })
})

describe("RequireAuth", () => {
  beforeEach(() => {
    window.sessionStorage.clear()
    fetchMock.mockReset()
    vi.stubGlobal("fetch", fetchMock)
    nav.replace.mockReset()
  })

  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
    window.sessionStorage.clear()
  })

  it("redirects to /login when there is no session", async () => {
    render(
      <AuthProvider>
        <RequireAuth>
          <div>protected content</div>
        </RequireAuth>
      </AuthProvider>,
    )

    await waitFor(() => expect(nav.replace).toHaveBeenCalledWith("/login"))
    expect(screen.queryByText("protected content")).not.toBeInTheDocument()
  })

  it("renders children for an authenticated session", async () => {
    setSession("ezt_a", user)
    fetchMock.mockResolvedValue(jsonResponse({ data: { user } }))

    render(
      <AuthProvider>
        <RequireAuth>
          <div>protected content</div>
        </RequireAuth>
      </AuthProvider>,
    )

    await waitFor(() => expect(screen.getByText("protected content")).toBeInTheDocument())
    expect(nav.replace).not.toHaveBeenCalled()
  })
})
