import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

const api = vi.hoisted(() => ({ getLatestContextPacket: vi.fn() }))
vi.mock("./api", () => api)

// The card is admin-only: it must never request the packet for a normal user,
// even if a caller mounts it without the page-level gate.
const auth = vi.hoisted(() => ({ isAdmin: true }))
vi.mock("@/features/auth/useIsAdmin", () => ({ useIsAdmin: () => auth.isAdmin }))

import type { ContextPacketView } from "./api"
import { ContextPacketCard } from "./ContextPacketCard"

const packet: ContextPacketView = {
  prefix_hash: "c".repeat(64),
  cached_tokens: 4096,
  cache_hit: true,
  invalidation_reason: "stage_boundary",
  briefing_hash: "d".repeat(64),
  segments: [
    {
      name: "system",
      role: "system",
      content: "你是一个测试代理",
      prefix: true,
      tokens: 1200,
    },
    {
      name: "briefing",
      role: "user",
      content: "版本简报：3 条待设计需求",
      prefix: false,
      tokens: 320,
    },
  ],
}

describe("ContextPacketCard", () => {
  beforeEach(() => {
    api.getLatestContextPacket.mockReset()
    auth.isAdmin = true
  })

  afterEach(cleanup)

  it("renders the latest context packet cache summary and segments", async () => {
    api.getLatestContextPacket.mockResolvedValue(packet)

    render(<ContextPacketCard versionId="2001" />)

    expect(await screen.findByTestId("context-packet")).toBeInTheDocument()
    expect(screen.getByText(/4096/)).toBeInTheDocument()
    expect(screen.getByText("命中")).toBeInTheDocument()
    expect(screen.getByText(/stage_boundary/)).toBeInTheDocument()
    expect(screen.getByText(/2 段/)).toBeInTheDocument()
    expect(api.getLatestContextPacket).toHaveBeenCalledWith("2001")
  })

  it("never requests the packet for a non-admin and shows the admin-only notice", async () => {
    auth.isAdmin = false

    render(<ContextPacketCard versionId="2001" />)

    expect(await screen.findByText(/上下文包仅管理员可读/)).toBeInTheDocument()
    expect(api.getLatestContextPacket).not.toHaveBeenCalled()
  })

  it("shows the admin-only notice for a 403", async () => {
    api.getLatestContextPacket.mockRejectedValue(
      new ApiError({
        status: 403,
        code: "forbidden",
        message: "需要管理员权限",
        requestId: "req_packet",
      }),
    )

    render(<ContextPacketCard versionId="2001" />)

    expect(await screen.findByText(/上下文包仅管理员可读/)).toBeInTheDocument()
    expect(screen.queryByRole("alert")).not.toBeInTheDocument()
  })

  it("shows an explicit empty state for a 404 with no recorded packet", async () => {
    api.getLatestContextPacket.mockRejectedValue(
      new ApiError({
        status: 404,
        code: "not_found",
        message: "上下文包不存在",
        requestId: "req_packet",
        details: { kind: "context_packet" },
      }),
    )

    render(<ContextPacketCard versionId="2001" />)

    expect(await screen.findByText("该版本暂无上下文包")).toBeInTheDocument()
  })

  it("shows the load error for an unexpected failure", async () => {
    api.getLatestContextPacket.mockRejectedValue(
      new ApiError({
        status: 503,
        code: "unavailable",
        message: "上下文包读取服务未装配",
        requestId: "req_packet",
      }),
    )

    render(<ContextPacketCard versionId="2001" />)

    expect(await screen.findByText("上下文包读取服务未装配")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "重试" })).toBeInTheDocument()
  })
})
