import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const mocks = vi.hoisted(() => ({
  apiGetPage: vi.fn(),
}))

vi.mock("@/lib/api-client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api-client")>()
  return { ...actual, apiGetPage: mocks.apiGetPage }
})

import { AuditLogPage } from "./AuditLogPage"
import type { AuditEntry } from "./audit-types"

const ENTRY: AuditEntry = {
  id: 12,
  actor_user_id: 1,
  action: "version.requirement_created",
  target_type: "requirement",
  target_id: "77",
  request_id: "req_audit_1",
  ip: "203.0.113.7",
  user_agent: "eztest/1.0",
  metadata: { status: "draft" },
  created_at: "2026-10-27T04:00:00Z",
}

const SECOND: AuditEntry = {
  ...ENTRY,
  id: 11,
  action: "auth.login_failed",
  target_type: "user",
  target_id: "",
  request_id: "",
  metadata: {},
}

function page(items: AuditEntry[], nextCursor = "", hasMore = false) {
  return { items, nextCursor, hasMore }
}

describe("AuditLogPage", () => {
  beforeEach(() => {
    mocks.apiGetPage.mockReset()
  })

  afterEach(() => {
    cleanup()
  })

  it("loads the newest page and shows the entry details on demand", async () => {
    mocks.apiGetPage.mockResolvedValue(page([ENTRY]))
    render(<AuditLogPage />)

    expect(await screen.findByText("version.requirement_created")).toBeTruthy()
    expect(screen.getByText("req_audit_1")).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: /查看记录 12/ }))
    expect(screen.getByText("记录详情")).toBeTruthy()
    expect(screen.getByText("requirement")).toBeTruthy()
    expect(screen.getByText("77")).toBeTruthy()
    expect(screen.getByText(/"status": "draft"/)).toBeTruthy()
  })

  it("sends actor/action/from/to filters to the API", async () => {
    mocks.apiGetPage.mockResolvedValue(page([]))
    render(<AuditLogPage />)
    await screen.findByText("没有符合条件的审计记录")

    fireEvent.change(screen.getByLabelText("操作者 ID"), { target: { value: "7" } })
    fireEvent.change(screen.getByLabelText("操作"), { target: { value: "version.created" } })
    fireEvent.change(screen.getByLabelText("开始时间"), {
      target: { value: "2026-10-27T08:00" },
    })
    fireEvent.change(screen.getByLabelText("结束时间"), {
      target: { value: "2026-10-28T08:00" },
    })
    fireEvent.click(screen.getByRole("button", { name: "筛选" }))

    await waitFor(() => expect(mocks.apiGetPage).toHaveBeenCalledTimes(2))
    const path = mocks.apiGetPage.mock.calls[1][0] as string
    const url = new URL(path, "http://localhost")
    expect(url.pathname).toBe("/api/admin/audit")
    expect(url.searchParams.get("actor")).toBe("7")
    expect(url.searchParams.get("action")).toBe("version.created")
    expect(url.searchParams.get("from")).toBe(new Date("2026-10-27T08:00").toISOString())
    expect(url.searchParams.get("to")).toBe(new Date("2026-10-28T08:00").toISOString())
    expect(url.searchParams.get("limit")).toBe("20")
  })

  it("pages forward with the response cursor and appends the older entries", async () => {
    mocks.apiGetPage
      .mockResolvedValueOnce(page([ENTRY], "cursor-1", true))
      .mockResolvedValueOnce(page([SECOND], "", false))
    render(<AuditLogPage />)

    expect(await screen.findByText("version.requirement_created")).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "加载更多" }))

    expect(await screen.findByText("auth.login_failed")).toBeTruthy()
    expect(screen.getByText("version.requirement_created")).toBeTruthy()
    const secondPath = mocks.apiGetPage.mock.calls[1][0] as string
    const url = new URL(secondPath, "http://localhost")
    expect(url.searchParams.get("cursor")).toBe("cursor-1")
    expect(screen.queryByRole("button", { name: "加载更多" })).toBeNull()
  })

  it("shows the empty state when the page has no entries", async () => {
    mocks.apiGetPage.mockResolvedValue(page([]))
    render(<AuditLogPage />)
    expect(await screen.findByText("没有符合条件的审计记录")).toBeTruthy()
  })

  it("shows a retryable error with the request id", async () => {
    class TestApiError extends Error {
      readonly status = 500
      readonly requestId = "req_failed_1"
      readonly code = "internal"
      readonly retryable = true
      readonly details = {}
    }
    mocks.apiGetPage
      .mockRejectedValueOnce(new TestApiError("the API is unavailable"))
      .mockResolvedValueOnce(page([ENTRY]))
    render(<AuditLogPage />)

    expect(await screen.findByText("无法加载审计日志")).toBeTruthy()
    expect(screen.getByText(/req_failed_1/)).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))

    expect(await screen.findByText("version.requirement_created")).toBeTruthy()
    expect(mocks.apiGetPage).toHaveBeenCalledTimes(2)
  })

  it("exports the loaded entries as CSV and Markdown", async () => {
    mocks.apiGetPage.mockResolvedValue(page([ENTRY]))
    const createObjectURL = vi.fn().mockReturnValue("blob:audit")
    const revokeObjectURL = vi.fn()
    Object.defineProperty(URL, "createObjectURL", { configurable: true, value: createObjectURL })
    Object.defineProperty(URL, "revokeObjectURL", { configurable: true, value: revokeObjectURL })
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {})
    render(<AuditLogPage />)
    await screen.findByText("version.requirement_created")

    fireEvent.click(screen.getByRole("button", { name: "导出 CSV" }))
    expect(createObjectURL).toHaveBeenCalledTimes(1)
    const csvBlob = createObjectURL.mock.calls[0][0] as Blob
    expect(await csvBlob.text()).toContain("version.requirement_created")
    expect(click).toHaveBeenCalled()

    fireEvent.click(screen.getByRole("button", { name: "导出 Markdown" }))
    expect(createObjectURL).toHaveBeenCalledTimes(2)
    const markdownBlob = createObjectURL.mock.calls[1][0] as Blob
    expect(await markdownBlob.text()).toContain("| 12 |")
    expect(revokeObjectURL).toHaveBeenCalled()

    click.mockRestore()
  })
})
