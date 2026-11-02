import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { AuditPanel } from "./AuditPanel"
import type { AuditLogEntry } from "./api"

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

const fetchMock = vi.fn<typeof fetch>()

const loginEntry: AuditLogEntry = {
  id: "9100",
  actor_id: "1",
  action: "auth.login",
  target_kind: "session",
  target_id: "1",
  request_id: "req_audit000001",
  ip: "127.0.0.1",
  user_agent: "vitest",
  metadata: { username: "admin" },
  created_at: "2026-10-31T08:00:00Z",
}

const settingEntry: AuditLogEntry = {
  ...loginEntry,
  id: "9099",
  action: "settings.updated",
  target_kind: "setting",
  target_id: "execution.global_concurrency",
  request_id: "req_audit000002",
  metadata: { key: "execution.global_concurrency" },
}

type RouteHandler = (url: URL) => Response | Promise<Response>

function installFetch(handler: RouteHandler): void {
  fetchMock.mockImplementation(async (input) => handler(new URL(String(input))))
}

function auditPage(items: AuditLogEntry[], nextCursor = "", hasMore = false): Response {
  return jsonResponse({ data: items, next_cursor: nextCursor, has_more: hasMore })
}

describe("AuditPanel", () => {
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

  it("loads the first page with the default page size", async () => {
    installFetch(() => auditPage([loginEntry, settingEntry]))

    render(<AuditPanel />)

    expect(await screen.findByText("auth.login")).toBeVisible()
    expect(screen.getByText("settings.updated")).toBeVisible()
    expect(screen.getByText("execution.global_concurrency")).toBeVisible()
    expect(screen.getByText("req_audit000001")).toBeVisible()

    const url = new URL(String(fetchMock.mock.calls[0][0]))
    expect(url.pathname).toBe("/api/admin/audit-logs")
    expect(url.searchParams.get("limit")).toBe("20")
    expect(url.searchParams.has("action")).toBe(false)
    expect(url.searchParams.has("actor")).toBe(false)
    expect(url.searchParams.has("cursor")).toBe(false)
  })

  it("applies action/actor filters and rejects a non-numeric actor", async () => {
    installFetch((url) => {
      if (url.searchParams.get("action") === "auth.login") return auditPage([loginEntry])
      return auditPage([loginEntry, settingEntry])
    })

    render(<AuditPanel />)
    await screen.findByText("auth.login")

    fireEvent.change(screen.getByLabelText("操作"), { target: { value: "auth.login" } })
    fireEvent.change(screen.getByLabelText("操作者 ID"), { target: { value: "1" } })
    fireEvent.click(screen.getByRole("button", { name: "查询" }))

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
    const filtered = new URL(String(fetchMock.mock.calls[1][0]))
    expect(filtered.searchParams.get("action")).toBe("auth.login")
    expect(filtered.searchParams.get("actor")).toBe("1")
    expect(filtered.searchParams.has("cursor")).toBe(false)
    expect(screen.queryByText("settings.updated")).toBeNull()

    fireEvent.change(screen.getByLabelText("操作者 ID"), { target: { value: "abc" } })
    fireEvent.click(screen.getByRole("button", { name: "查询" }))
    expect(await screen.findByRole("alert")).toHaveTextContent("操作者 ID 必须是数字")
    expect(fetchMock).toHaveBeenCalledTimes(2)

    fireEvent.click(screen.getByRole("button", { name: "重置" }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3))
    const reset = new URL(String(fetchMock.mock.calls[2][0]))
    expect(reset.searchParams.has("action")).toBe(false)
    expect(reset.searchParams.has("actor")).toBe(false)
    expect(await screen.findByText("settings.updated")).toBeVisible()
  })

  it("paginates forward with the cursor and back with the local history", async () => {
    installFetch((url) => {
      const cursor = url.searchParams.get("cursor")
      if (cursor === "9100") return auditPage([settingEntry], "", false)
      return auditPage([loginEntry], "9100", true)
    })

    render(<AuditPanel />)
    await screen.findByText("auth.login")

    fireEvent.click(screen.getByRole("button", { name: "下一页" }))
    expect(await screen.findByText("settings.updated")).toBeVisible()
    const second = new URL(String(fetchMock.mock.calls[1][0]))
    expect(second.searchParams.get("cursor")).toBe("9100")
    expect(screen.getByRole("button", { name: "下一页" })).toBeDisabled()

    fireEvent.click(screen.getByRole("button", { name: "上一页" }))
    expect(await screen.findByText("auth.login")).toBeVisible()
    const third = new URL(String(fetchMock.mock.calls[2][0]))
    expect(third.searchParams.has("cursor")).toBe(false)
  })

  it("shows the error state when the audit request fails", async () => {
    installFetch(() =>
      jsonResponse(
        {
          error: {
            code: "internal",
            message: "audit query failed",
            request_id: "req_auditfail01",
            retryable: true,
            details: {},
          },
        },
        500,
      ),
    )

    render(<AuditPanel />)

    expect(await screen.findByText("审计日志加载失败")).toBeVisible()
    expect(screen.getByText("audit query failed")).toBeVisible()
  })
})
