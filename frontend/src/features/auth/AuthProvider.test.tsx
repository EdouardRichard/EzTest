import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { useState } from "react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

const nav = vi.hoisted(() => ({
  replace: vi.fn(),
  push: vi.fn(),
  pathname: "/knowledge",
}))
vi.mock("next/navigation", () => ({
  useRouter: () => ({
    push: nav.push,
    replace: nav.replace,
    refresh: vi.fn(),
    back: vi.fn(),
    forward: vi.fn(),
    prefetch: vi.fn(),
  }),
  usePathname: () => nav.pathname,
  redirect: vi.fn(),
}))

import { clearSession, getStoredUser, getToken, setSession } from "@/lib/auth-storage"
import { AuthProvider, RequireAuth, safeNext, useAuth } from "./AuthProvider"

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

function clearStorage(): void {
  window.localStorage.clear()
  window.sessionStorage.clear()
}

function AuthProbe() {
  const auth = useAuth()
  return (
    <div>
      <span data-testid="loading">{auth.loading ? "loading" : "ready"}</span>
      <span data-testid="user">{auth.user?.username ?? "anon"}</span>
      <span data-testid="token">{auth.token ?? "none"}</span>
      <button
        type="button"
        onClick={() => void auth.login("admin", "test-admin-pass").catch(() => {})}
      >
        probe-login
      </button>
      <button
        type="button"
        onClick={() => void auth.changePassword("test-admin-pass", "newpass123").catch(() => {})}
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
    clearStorage()
    nav.pathname = "/knowledge"
    window.history.replaceState({}, "", "/knowledge")
    fetchMock.mockReset()
    vi.stubGlobal("fetch", fetchMock)
    nav.push.mockReset()
    nav.replace.mockReset()
  })

  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
    clearStorage()
    window.history.replaceState({}, "", "/")
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

  it("discards an expired stored session without calling GET /api/auth/me", async () => {
    setSession("ezt_expired", user, "2020-01-01T00:00:00Z")

    renderProvider()

    await waitFor(() => expect(screen.getByTestId("loading")).toHaveTextContent("ready"))
    expect(screen.getByTestId("user")).toHaveTextContent("anon")
    expect(screen.getByTestId("token")).toHaveTextContent("none")
    expect(fetchMock).not.toHaveBeenCalled()
    expect(getToken()).toBeNull()
  })

  it("clears a stored session when GET /api/auth/me is unauthorized", async () => {
    setSession("ezt_unauthorized", user)
    fetchMock.mockResolvedValue(
      jsonResponse({ error: { code: "unauthenticated", message: "login required" } }, 401),
    )

    renderProvider()

    await waitFor(() => expect(screen.getByTestId("loading")).toHaveTextContent("ready"))
    expect(screen.getByTestId("user")).toHaveTextContent("anon")
    expect(getToken()).toBeNull()
  })

  it("logs in, persists the session with its expiry and calls the login endpoint", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({ data: { token: "ezt_new", expires_at: "2099-01-01T00:00:00Z", user } }),
    )

    renderProvider()
    await waitFor(() => expect(screen.getByTestId("loading")).toHaveTextContent("ready"))
    fireEvent.click(screen.getByRole("button", { name: "probe-login" }))

    await waitFor(() => expect(screen.getByTestId("user")).toHaveTextContent("admin"))
    expect(screen.getByTestId("token")).toHaveTextContent("ezt_new")
    expect(getToken()).toBe("ezt_new")
    expect(getStoredUser()).toEqual(user)
    expect(window.localStorage.getItem("eztest.token_expires_at")).toBe("2099-01-01T00:00:00Z")

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe("http://localhost:13333/api/auth/login")
    expect(init?.method).toBe("POST")
    expect(requestBody([url, init])).toEqual({ username: "admin", password: "test-admin-pass" })
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

  it("logs out through the API and clears the local and legacy sessions", async () => {
    setSession("ezt_a", user)
    fetchMock.mockImplementation(async (input) => {
      const url = String(input)
      if (url.endsWith("/api/auth/me")) return jsonResponse({ data: { user } })
      return new Response(null, { status: 204 })
    })

    renderProvider()
    await waitFor(() => expect(screen.getByTestId("user")).toHaveTextContent("admin"))
    // A stale copy from another build must be removed on sign-out too.
    window.sessionStorage.setItem("eztest.token", "ezt_legacy")
    window.sessionStorage.setItem("eztest.user", JSON.stringify(user))
    fireEvent.click(screen.getByRole("button", { name: "probe-logout" }))

    await waitFor(() => expect(screen.getByTestId("user")).toHaveTextContent("anon"))
    expect(getToken()).toBeNull()
    expect(window.sessionStorage.getItem("eztest.token")).toBeNull()
    expect(window.sessionStorage.getItem("eztest.user")).toBeNull()
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
        old_password: "test-admin-pass",
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

  it("adopts a session created by another tab through the storage event", async () => {
    renderProvider()
    await waitFor(() => expect(screen.getByTestId("loading")).toHaveTextContent("ready"))
    expect(screen.getByTestId("user")).toHaveTextContent("anon")

    act(() => {
      setSession("ezt_other_tab", user, "2099-01-01T00:00:00Z")
      window.dispatchEvent(
        new StorageEvent("storage", {
          key: "eztest.token",
          newValue: "ezt_other_tab",
          storageArea: window.localStorage,
        }),
      )
    })

    await waitFor(() => expect(screen.getByTestId("user")).toHaveTextContent("admin"))
    expect(screen.getByTestId("token")).toHaveTextContent("ezt_other_tab")
  })

  it("signs out when another tab clears the stored session", async () => {
    setSession("ezt_a", user)
    fetchMock.mockResolvedValue(jsonResponse({ data: { user } }))

    renderProvider()
    await waitFor(() => expect(screen.getByTestId("user")).toHaveTextContent("admin"))

    act(() => {
      window.localStorage.removeItem("eztest.token")
      window.localStorage.removeItem("eztest.user")
      window.dispatchEvent(
        new StorageEvent("storage", {
          key: "eztest.token",
          newValue: null,
          storageArea: window.localStorage,
        }),
      )
    })

    await waitFor(() => expect(screen.getByTestId("user")).toHaveTextContent("anon"))
    expect(screen.getByTestId("token")).toHaveTextContent("none")
  })
})

describe("safeNext", () => {
  it("accepts same-origin paths and preserves their query and hash", () => {
    expect(safeNext("/knowledge")).toBe("/knowledge")
    expect(safeNext("/versions/42?tab=requirements#top")).toBe("/versions/42?tab=requirements#top")
    expect(safeNext("/")).toBe("/")
  })

  it("falls back to /versions for missing or unsafe targets", () => {
    expect(safeNext(null)).toBe("/versions")
    expect(safeNext(undefined)).toBe("/versions")
    expect(safeNext("")).toBe("/versions")
    expect(safeNext("knowledge")).toBe("/versions")
    expect(safeNext("//evil.example/login")).toBe("/versions")
    expect(safeNext("/\\evil.example")).toBe("/versions")
    expect(safeNext("https://evil.example/versions")).toBe("/versions")
    expect(safeNext("javascript:alert(1)")).toBe("/versions")
    expect(safeNext("/login")).toBe("/versions")
    expect(safeNext("/login?next=/knowledge")).toBe("/versions")
    expect(safeNext("/login/reset")).toBe("/versions")
  })
})

describe("RequireAuth", () => {
  beforeEach(() => {
    clearStorage()
    nav.pathname = "/knowledge"
    window.history.replaceState({}, "", "/knowledge")
    fetchMock.mockReset()
    vi.stubGlobal("fetch", fetchMock)
    nav.replace.mockReset()
  })

  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
    clearStorage()
    window.history.replaceState({}, "", "/")
  })

  it("redirects to /login with the original target as next", async () => {
    render(
      <AuthProvider>
        <RequireAuth>
          <div>protected content</div>
        </RequireAuth>
      </AuthProvider>,
    )

    await waitFor(() => expect(nav.replace).toHaveBeenCalledWith("/login?next=%2Fknowledge"))
    expect(screen.queryByText("protected content")).not.toBeInTheDocument()
  })

  it("preserves the original query string in next", async () => {
    window.history.replaceState({}, "", "/knowledge?tab=requirements")

    render(
      <AuthProvider>
        <RequireAuth>
          <div>protected content</div>
        </RequireAuth>
      </AuthProvider>,
    )

    await waitFor(() =>
      expect(nav.replace).toHaveBeenCalledWith("/login?next=%2Fknowledge%3Ftab%3Drequirements"),
    )
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
