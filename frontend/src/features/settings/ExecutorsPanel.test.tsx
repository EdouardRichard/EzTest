import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

const api = vi.hoisted(() => ({
  listAdminExecutors: vi.fn(),
}))
vi.mock("./api", () => api)

import type { AdminExecutor } from "./api"
import { ExecutorsPanel } from "./ExecutorsPanel"

function executor(overrides: Partial<AdminExecutor>): AdminExecutor {
  return {
    id: "1",
    name: "executor-local",
    endpoint: "http://127.0.0.1:13336",
    status: "active",
    capacity: 8,
    active_contexts: 3,
    labels: { region: "local" },
    last_heartbeat: "2026-11-01T08:00:00Z",
    version: "0.22.0",
    last_error: "",
    ...overrides,
  }
}

describe("ExecutorsPanel", () => {
  beforeEach(() => {
    api.listAdminExecutors.mockReset()
  })

  afterEach(cleanup)

  it("renders the executor health and status read-only", async () => {
    api.listAdminExecutors.mockResolvedValue([
      executor({}),
      executor({
        id: "2",
        name: "executor-offline",
        status: "offline",
        active_contexts: 0,
        endpoint: "http://127.0.0.1:13337",
      }),
    ])

    render(<ExecutorsPanel />)

    expect(await screen.findByText("executor-local")).toBeInTheDocument()
    expect(screen.getByText("executor-offline")).toBeInTheDocument()
    expect(screen.getByText("http://127.0.0.1:13336")).toBeInTheDocument()
    expect(screen.getByTestId("executor-status-1")).toHaveTextContent("运行中")
    expect(screen.getByTestId("executor-status-2")).toHaveTextContent("离线")
    expect(screen.getByTestId("executor-health-1")).toHaveTextContent("3/8")
    expect(screen.getByTestId("executor-health-2")).toHaveTextContent("0/8")
    expect(api.listAdminExecutors).toHaveBeenCalledTimes(1)
    // Read-only admin surface: no lifecycle write action is offered.
    expect(screen.queryByRole("button", { name: /排空|激活/ })).not.toBeInTheDocument()
  })

  it("shows an explicit empty state when no executor is registered", async () => {
    api.listAdminExecutors.mockResolvedValue([])

    render(<ExecutorsPanel />)

    expect(await screen.findByText("暂无执行器")).toBeInTheDocument()
  })

  it("shows the load error and retries the executor roster", async () => {
    api.listAdminExecutors
      .mockRejectedValueOnce(new Error("执行器列表不可用"))
      .mockResolvedValueOnce([executor({})])

    render(<ExecutorsPanel />)

    expect(await screen.findByText("执行器列表不可用")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))

    expect(await screen.findByText("executor-local")).toBeInTheDocument()
    expect(api.listAdminExecutors).toHaveBeenCalledTimes(2)
  })
})
