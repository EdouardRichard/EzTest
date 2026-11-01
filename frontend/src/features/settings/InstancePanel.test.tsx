import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import type { Instance } from "./api"

const api = vi.hoisted(() => ({
  listGCRuns: vi.fn(),
  getGCRun: vi.fn(),
  createGCRun: vi.fn(),
  listInstances: vi.fn(),
  drainInstance: vi.fn(),
  activateInstance: vi.fn(),
}))
vi.mock("./api", () => api)

const auth = vi.hoisted(() => ({ role: "admin" as "admin" | "user" }))
vi.mock("@/features/auth/AuthProvider", () => ({
  useAuth: () => ({ user: { role: auth.role } }),
}))

import { InstancePanel } from "./InstancePanel"

/* --------------------------------- fixtures -------------------------------- */

const activeInstance: Instance = {
  id: "5001",
  instance_key: "host-5001",
  hostname: "api-host-1",
  pid: 4242,
  api_port: 13333,
  node_id: 1,
  status: "active",
  version: "0.2.0-p7",
  last_error: null,
  started_at: "2026-11-01T05:00:00Z",
  heartbeat_at: "2026-11-01T06:00:00Z",
  draining_at: null,
  stopped_at: null,
  created_at: "2026-11-01T05:00:00Z",
  updated_at: "2026-11-01T06:00:00Z",
}

const drainingInstance: Instance = {
  ...activeInstance,
  id: "5002",
  instance_key: "host-5002",
  hostname: "api-host-2",
  pid: 4343,
  api_port: 13334,
  node_id: 2,
  status: "draining",
  heartbeat_at: "2026-11-01T05:59:00Z",
  draining_at: "2026-11-01T05:59:30Z",
}

function mockApi(): void {
  api.listInstances.mockResolvedValue({
    items: [activeInstance, drainingInstance],
    nextCursor: "",
    hasMore: false,
  })
  api.drainInstance.mockResolvedValue(drainingInstance)
  api.activateInstance.mockResolvedValue(activeInstance)
}

function table(): HTMLElement {
  return screen.getByRole("table", { name: "API 实例列表" })
}

function rowOf(id: string): HTMLElement {
  const cell = screen.getByTestId(`instance-${id}`)
  const row = cell.closest("tr")
  if (row === null) throw new Error(`no row for instance ${id}`)
  return row
}

describe("InstancePanel", () => {
  beforeEach(() => {
    auth.role = "admin"
    for (const fn of Object.values(api)) fn.mockReset()
    mockApi()
  })

  afterEach(cleanup)

  it("is hidden for non-admin users and issues no request", () => {
    auth.role = "user"

    render(<InstancePanel />)

    expect(screen.queryByTestId("instance-panel")).not.toBeInTheDocument()
    expect(api.listInstances).not.toHaveBeenCalled()
  })

  it("lists node, status, heartbeat, pid/port and version", async () => {
    render(<InstancePanel />)

    await screen.findByRole("table", { name: "API 实例列表" })
    const row = rowOf("5001")
    expect(within(row).getByText("4242")).toBeInTheDocument()
    expect(within(row).getByText("13333")).toBeInTheDocument()
    expect(within(row).getByText("0.2.0-p7")).toBeInTheDocument()
    expect(within(row).getByText("运行中")).toBeInTheDocument()
    expect(within(row).getByText(/2026-11-01/)).toBeInTheDocument()
    expect(within(table()).getByText("api-host-1")).toBeInTheDocument()
    expect(within(row).getByTestId("instance-node-5001")).toHaveTextContent("1")
  })

  it("drains an active instance through the admin API and refreshes", async () => {
    render(<InstancePanel />)
    await screen.findByRole("table", { name: "API 实例列表" })

    fireEvent.click(within(rowOf("5001")).getByRole("button", { name: "排空" }))

    await waitFor(() => expect(api.drainInstance).toHaveBeenCalledWith("5001"))
    await waitFor(() => expect(api.listInstances).toHaveBeenCalledTimes(2))
  })

  it("activates a draining instance through the admin API", async () => {
    render(<InstancePanel />)
    await screen.findByRole("table", { name: "API 实例列表" })

    fireEvent.click(within(rowOf("5002")).getByRole("button", { name: "激活" }))

    await waitFor(() => expect(api.activateInstance).toHaveBeenCalledWith("5002"))
  })

  it("disables the drain button while the request is running", async () => {
    let resolveDrain: (value: Instance) => void = () => {}
    api.drainInstance.mockReturnValue(
      new Promise<Instance>((resolve) => {
        resolveDrain = resolve
      }),
    )
    render(<InstancePanel />)
    await screen.findByRole("table", { name: "API 实例列表" })

    fireEvent.click(within(rowOf("5001")).getByRole("button", { name: "排空" }))

    await waitFor(() =>
      expect(within(rowOf("5001")).getByRole("button", { name: "排空中…" })).toBeDisabled(),
    )
    resolveDrain(drainingInstance)
    await waitFor(() => expect(api.listInstances).toHaveBeenCalledTimes(2))
  })
})
