import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

import { teamRoomFixture } from "./test-fixtures"

const api = vi.hoisted(() => ({ getTeamRoom: vi.fn() }))
vi.mock("./api", () => api)

import { TeamRoomPanel } from "./TeamRoomPanel"

describe("TeamRoomPanel", () => {
  beforeEach(() => {
    api.getTeamRoom.mockReset()
    api.getTeamRoom.mockResolvedValue(teamRoomFixture)
  })

  afterEach(cleanup)

  it("stays collapsed until the user opens it and fetches lazily", async () => {
    render(<TeamRoomPanel versionId="2001" />)

    expect(screen.getByTestId("team-room-toggle")).toHaveAttribute("aria-expanded", "false")
    expect(api.getTeamRoom).not.toHaveBeenCalled()

    fireEvent.click(screen.getByTestId("team-room-toggle"))

    await waitFor(() => expect(api.getTeamRoom).toHaveBeenCalledWith("2001", "clarification"))
    expect(screen.getByTestId("team-room-toggle")).toHaveAttribute("aria-expanded", "true")
  })

  it("shows the latest digest and recent posts read-only", async () => {
    render(<TeamRoomPanel versionId="2001" />)
    fireEvent.click(screen.getByTestId("team-room-toggle"))

    expect(await screen.findByText("澄清团队已读取版本上下文，提出 3 条草案。")).toBeInTheDocument()
    expect(screen.getByText("退款时效按 T+1 处理，节假日顺延。")).toBeInTheDocument()
    expect(screen.getByText("决策")).toBeInTheDocument()

    const panel = screen.getByTestId("team-room-panel")
    expect(within(panel).queryByRole("textbox")).not.toBeInTheDocument()
    expect(within(panel).queryByRole("button", { name: /发帖/ })).not.toBeInTheDocument()
  })

  it("shows the empty state when the team has no digest or posts", async () => {
    api.getTeamRoom.mockResolvedValue({ team: "clarification", digest: null, posts: [] })
    render(<TeamRoomPanel versionId="2001" />)
    fireEvent.click(screen.getByTestId("team-room-toggle"))

    expect(await screen.findByText("暂无团队动态")).toBeInTheDocument()
  })

  it("shows an error with retry", async () => {
    api.getTeamRoom
      .mockRejectedValueOnce(
        new ApiError({ status: 500, code: "internal", message: "团队频道暂不可用" }),
      )
      .mockResolvedValueOnce(teamRoomFixture)
    render(<TeamRoomPanel versionId="2001" />)
    fireEvent.click(screen.getByTestId("team-room-toggle"))

    expect(await screen.findByText("团队频道暂不可用")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))

    expect(await screen.findByText("澄清团队已读取版本上下文，提出 3 条草案。")).toBeInTheDocument()
    expect(api.getTeamRoom).toHaveBeenCalledTimes(2)
  })

  it("refetches when the SSE team-room version changes", async () => {
    const { rerender } = render(<TeamRoomPanel versionId="2001" />)
    fireEvent.click(screen.getByTestId("team-room-toggle"))
    await screen.findByText("澄清团队已读取版本上下文，提出 3 条草案。")
    expect(api.getTeamRoom).toHaveBeenCalledTimes(1)

    rerender(<TeamRoomPanel versionId="2001" refreshToken={1} />)

    await waitFor(() => expect(api.getTeamRoom).toHaveBeenCalledTimes(2))
  })
})
