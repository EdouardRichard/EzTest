import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { setToken } from "@/lib/auth-storage"
import type { ConnectSSEOptions } from "@/lib/sse-client"

import { SSETestPanel } from "./SSETestPanel"

const mocks = vi.hoisted(() => ({
  connectSSE: vi.fn(),
  stop: vi.fn(),
  apiGet: vi.fn(),
  apiPost: vi.fn(),
}))

vi.mock("@/lib/sse-client", () => ({
  connectSSE: mocks.connectSSE,
}))

vi.mock("@/lib/api-client", () => {
  class ApiError extends Error {
    readonly status: number
    constructor(status: number, message: string) {
      super(message)
      this.name = "ApiError"
      this.status = status
    }
  }
  return {
    ApiError,
    apiGet: mocks.apiGet,
    apiPost: mocks.apiPost,
    resolveApiUrl: (path: string) => `http://localhost:13333${path}`,
  }
})

describe("SSETestPanel", () => {
  let options: ConnectSSEOptions | null = null

  beforeEach(() => {
    sessionStorage.clear()
    setToken("ezt_admin")
    options = null
    mocks.connectSSE.mockReset()
    mocks.stop.mockReset()
    mocks.apiGet.mockReset()
    mocks.apiPost.mockReset()
    mocks.connectSSE.mockImplementation((next: ConnectSSEOptions) => {
      options = next
      next.onStatus("connecting")
      return mocks.stop
    })
    mocks.apiGet.mockResolvedValue({ items: [], snapshot_cursor: 0, has_more: false })
    mocks.apiPost.mockResolvedValue({ seqs: [1, 2, 3] })
  })

  afterEach(() => {
    cleanup()
  })

  it("connects, renders live events, sends three test events, and disconnects", async () => {
    render(<SSETestPanel />)
    expect(screen.getAllByText("未连接").length).toBeGreaterThan(0)

    fireEvent.click(screen.getByRole("button", { name: "连接" }))

    await waitFor(() => expect(mocks.connectSSE).toHaveBeenCalledTimes(1))
    expect(mocks.apiGet).toHaveBeenCalledWith("/api/stream/events?channel=system&limit=50")
    expect(options).not.toBeNull()
    expect(options?.url).toBe("http://localhost:13333/api/stream?channel=system")
    expect(options?.token).toBe("ezt_admin")
    expect(options?.lastEventId).toBeUndefined()

    act(() => {
      options?.onStatus("live")
      options?.onEvent({
        id: "1",
        event: "system.test",
        data: JSON.stringify({
          seq: 1,
          channel: "system",
          type: "system.test",
          summary: "admin test event",
          ts: "2026-10-27T02:30:00Z",
        }),
      })
    })

    expect(screen.getAllByText("实时").length).toBeGreaterThan(0)
    expect(screen.getByText("admin test event")).toBeTruthy()
    const lastSeqLabel = screen.getByText("最后 seq", { exact: true })
    expect(lastSeqLabel.nextElementSibling?.textContent).toBe("1")

    fireEvent.click(screen.getByRole("button", { name: "发送 3 条测试事件" }))
    await waitFor(() => expect(mocks.apiPost).toHaveBeenCalledTimes(1))
    expect(mocks.apiPost).toHaveBeenCalledWith(
      "/api/admin/stream/test-events",
      expect.objectContaining({ count: 3 }),
    )
    await waitFor(() => expect(screen.getByText(/已发送 3 条测试事件/)).toBeTruthy())

    fireEvent.click(screen.getByRole("button", { name: "断开" }))
    expect(mocks.stop).toHaveBeenCalledTimes(1)
    act(() => options?.onStatus("closed"))
    expect(screen.getAllByText("未连接").length).toBeGreaterThan(0)
  })
})
