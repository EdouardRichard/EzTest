import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import type { User } from "./api"
import { UsersPanel } from "./UsersPanel"

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

const fetchMock = vi.fn<typeof fetch>()

const admin: User = {
  id: "1",
  username: "admin",
  display_name: "系统管理员",
  role: "admin",
  status: "active",
  created_at: "2026-10-31T08:00:00Z",
  updated_at: "2026-10-31T08:00:00Z",
}

const user: User = {
  id: "9001",
  username: "qa01",
  display_name: "QA 01",
  role: "user",
  status: "active",
  created_at: "2026-10-31T08:00:00Z",
  updated_at: "2026-10-31T08:00:00Z",
}

type RouteHandler = (url: URL, init: RequestInit) => Response | Promise<Response>

function installFetch(handler: RouteHandler): void {
  fetchMock.mockImplementation(async (input, init) => {
    const url = new URL(String(input))
    return handler(url, init ?? {})
  })
}

function method(init: RequestInit): string {
  return init.method ?? "GET"
}

function readBody(init: RequestInit): unknown {
  return typeof init.body === "string" ? (JSON.parse(init.body) as unknown) : undefined
}

function userRow(username: string): HTMLElement {
  const row = screen.getByText(username).closest("tr")
  if (!(row instanceof HTMLElement)) throw new Error(`row not found for ${username}`)
  return row
}

function usersPage(items: User[], nextCursor = "", hasMore = false): Response {
  return jsonResponse({ data: items, next_cursor: nextCursor, has_more: hasMore })
}

describe("UsersPanel", () => {
  beforeEach(() => {
    window.sessionStorage.clear()
    fetchMock.mockReset()
    vi.stubGlobal("fetch", fetchMock)
    vi.stubEnv("NEXT_PUBLIC_API_BASE_URL", "")
  })

  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
    vi.unstubAllEnvs()
    window.sessionStorage.clear()
  })

  it("lists users with role and status badges and protects the admin row", async () => {
    installFetch((url) => {
      if (url.pathname === "/api/admin/users") return usersPage([admin, user])
      return jsonResponse({ data: null })
    })

    render(<UsersPanel />)

    expect(await screen.findByText("qa01")).toBeVisible()
    expect(within(userRow("admin")).getByText("管理员")).toBeVisible()
    expect(within(userRow("qa01")).getByText("普通用户")).toBeVisible()
    expect(within(userRow("qa01")).getByText("启用")).toBeVisible()
    expect(within(userRow("admin")).queryByRole("button", { name: "禁用" })).toBeNull()
    expect(within(userRow("qa01")).getByRole("button", { name: "禁用" })).toBeVisible()
  })

  it("validates the create form and appends the created user", async () => {
    const created: User = { ...user, id: "9002", username: "qa02", display_name: "QA 02" }
    installFetch((url, init) => {
      if (url.pathname === "/api/admin/users" && method(init) === "POST") {
        return jsonResponse({ data: created }, 201)
      }
      return usersPage([admin])
    })

    render(<UsersPanel />)
    await screen.findByText("admin")

    fireEvent.click(screen.getByRole("button", { name: "创建用户" }))
    const dialog = screen.getByRole("dialog")

    fireEvent.click(within(dialog).getByRole("button", { name: "创建" }))
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("请输入用户名")

    fireEvent.change(within(dialog).getByLabelText("用户名"), { target: { value: "qa" } })
    fireEvent.click(within(dialog).getByRole("button", { name: "创建" }))
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("用户名需为 3–64 位")

    fireEvent.change(within(dialog).getByLabelText("用户名"), { target: { value: "qa02" } })
    fireEvent.click(within(dialog).getByRole("button", { name: "创建" }))
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("请输入显示名")

    fireEvent.change(within(dialog).getByLabelText("显示名"), { target: { value: "QA 02" } })
    fireEvent.click(within(dialog).getByRole("button", { name: "创建" }))
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("密码至少 8 个字符")

    fireEvent.change(within(dialog).getByLabelText("密码"), { target: { value: "qa-password" } })
    fireEvent.click(within(dialog).getByRole("button", { name: "创建" }))

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())
    const post = fetchMock.mock.calls.find((call) => method(call[1] ?? {}) === "POST")
    expect(readBody(post?.[1] ?? {})).toEqual({
      username: "qa02",
      display_name: "QA 02",
      password: "qa-password",
    })
    expect(await screen.findByText("qa02")).toBeVisible()
  })

  it("disables an active user and shows the new status", async () => {
    const disabled: User = { ...user, status: "disabled" }
    installFetch((url, init) => {
      if (url.pathname === "/api/admin/users/9001" && method(init) === "PATCH") {
        return jsonResponse({ data: disabled })
      }
      return usersPage([admin, user])
    })

    render(<UsersPanel />)
    await screen.findByText("qa01")

    fireEvent.click(within(userRow("qa01")).getByRole("button", { name: "禁用" }))

    await waitFor(() => expect(within(userRow("qa01")).getByText("已禁用")).toBeVisible())
    const patch = fetchMock.mock.calls.find((call) => method(call[1] ?? {}) === "PATCH")
    expect(String(patch?.[0])).toBe("http://localhost:13333/api/admin/users/9001")
    expect(readBody(patch?.[1] ?? {})).toEqual({ status: "disabled" })
    expect(within(userRow("qa01")).getByRole("button", { name: "启用" })).toBeVisible()
  })

  it("resets a user's password after validating both fields", async () => {
    installFetch((url, init) => {
      if (url.pathname === "/api/admin/users/9001/reset-password" && method(init) === "POST") {
        return new Response(null, { status: 204 })
      }
      return usersPage([admin, user])
    })

    render(<UsersPanel />)
    await screen.findByText("qa01")

    fireEvent.click(within(userRow("qa01")).getByRole("button", { name: "重置密码" }))
    const dialog = screen.getByRole("dialog")

    fireEvent.click(within(dialog).getByRole("button", { name: "确认重置" }))
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("请输入新密码")

    fireEvent.change(within(dialog).getByLabelText("新密码"), { target: { value: "short" } })
    fireEvent.click(within(dialog).getByRole("button", { name: "确认重置" }))
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("密码至少 8 个字符")

    fireEvent.change(within(dialog).getByLabelText("新密码"), {
      target: { value: "reset-password" },
    })
    fireEvent.change(within(dialog).getByLabelText("确认新密码"), {
      target: { value: "different-password" },
    })
    fireEvent.click(within(dialog).getByRole("button", { name: "确认重置" }))
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("两次输入的密码不一致")

    fireEvent.change(within(dialog).getByLabelText("确认新密码"), {
      target: { value: "reset-password" },
    })
    fireEvent.click(within(dialog).getByRole("button", { name: "确认重置" }))

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())
    const post = fetchMock.mock.calls.find(
      (call) => method(call[1] ?? {}) === "POST" && String(call[0]).includes("reset-password"),
    )
    expect(readBody(post?.[1] ?? {})).toEqual({ new_password: "reset-password" })
    expect(screen.getByRole("status")).toHaveTextContent("已重置 qa01 的密码")
  })

  it("loads the next cursor page and appends it", async () => {
    const second: User = { ...user, id: "8990", username: "qa00", display_name: "QA 00" }
    installFetch((url) => {
      if (url.pathname !== "/api/admin/users") return jsonResponse({ data: null })
      if (url.searchParams.get("cursor") === "9001") {
        return usersPage([second], "", false)
      }
      return usersPage([admin, user], "9001", true)
    })

    render(<UsersPanel />)
    await screen.findByText("qa01")

    fireEvent.click(screen.getByRole("button", { name: "加载更多" }))

    expect(await screen.findByText("qa00")).toBeVisible()
    expect(screen.queryByRole("button", { name: "加载更多" })).toBeNull()
    const secondCall = fetchMock.mock.calls[1]
    const url = new URL(String(secondCall[0]))
    expect(url.searchParams.get("cursor")).toBe("9001")
  })
})
