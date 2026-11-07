import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

const versionApi = vi.hoisted(() => ({ getDiscoveryObservations: vi.fn() }))
vi.mock(import("./api"), async (importOriginal) => {
  const actual = await importOriginal<typeof import("./api")>()
  return { ...actual, ...versionApi }
})

const resourcesApi = vi.hoisted(() => ({
  confirmPageProposal: vi.fn(),
  rejectPageProposal: vi.fn(),
  confirmApiProposal: vi.fn(),
  rejectApiProposal: vi.fn(),
}))
vi.mock("@/features/resources/api", () => resourcesApi)

const auth = vi.hoisted(() => ({ isAdmin: false }))
vi.mock("@/features/auth/useIsAdmin", () => ({ useIsAdmin: () => auth.isAdmin }))

import { DiscoveryPanel } from "./DiscoveryPanel"
import type { DiscoveryObservation } from "./types"

function observation(overrides: Partial<DiscoveryObservation>): DiscoveryObservation {
  return {
    observation_id: "9001",
    kind: "page",
    method: "",
    host: "shop.example.com",
    resource_template: "/orders/{id}",
    status_code: 200,
    mime: "text/html",
    source_kind: "execution",
    occurrences: 3,
    decision: "proposed",
    wave_id: "9201",
    case_run_id: "9301",
    version_sut_id: "5201",
    environment_id: "7201",
    resource_id: "11",
    fact_id: "6101",
    first_seen_at: "2026-11-01T08:00:00Z",
    last_seen_at: "2026-11-01T08:05:00Z",
    created_at: "2026-11-01T08:00:00Z",
    updated_at: "2026-11-01T08:00:00Z",
    ...overrides,
  }
}

const proposedPage = observation({})
const proposedApi = observation({
  observation_id: "9002",
  kind: "api",
  method: "POST",
  resource_template: "/api/orders",
  mime: "application/json",
  decision: "proposed",
  resource_id: "12",
  case_run_id: null,
})

describe("DiscoveryPanel", () => {
  beforeEach(() => {
    versionApi.getDiscoveryObservations.mockReset()
    resourcesApi.confirmPageProposal.mockReset()
    resourcesApi.rejectPageProposal.mockReset()
    resourcesApi.confirmApiProposal.mockReset()
    resourcesApi.rejectApiProposal.mockReset()
    resourcesApi.confirmPageProposal.mockResolvedValue({ id: "11", status: "approved" })
    resourcesApi.rejectPageProposal.mockResolvedValue({ id: "11", status: "rejected" })
    resourcesApi.confirmApiProposal.mockResolvedValue({ id: "12", status: "approved" })
    resourcesApi.rejectApiProposal.mockResolvedValue({ id: "12", status: "rejected" })
    auth.isAdmin = true
  })

  afterEach(cleanup)

  it("renders observations and approves a proposed page", async () => {
    versionApi.getDiscoveryObservations.mockResolvedValue([proposedPage])
    const onChanged = vi.fn()

    render(<DiscoveryPanel versionId="2001" onChanged={onChanged} />)

    expect(await screen.findByText("/orders/{id}")).toBeInTheDocument()
    expect(screen.getByText("shop.example.com")).toBeInTheDocument()
    expect(screen.getByText("3")).toBeInTheDocument()
    expect(screen.getByText("proposed")).toBeInTheDocument()
    // The default query only asks for the rows still awaiting a decision.
    expect(versionApi.getDiscoveryObservations).toHaveBeenCalledWith("2001", {
      decision: "proposed",
      limit: 50,
    })
    // Provenance is a deep link into the execution workspace.
    expect(screen.getByRole("link", { name: "wave 9201" })).toHaveAttribute(
      "href",
      expect.stringContaining("wave=9201"),
    )
    expect(screen.getByRole("link", { name: "case_run 9301" })).toHaveAttribute(
      "href",
      expect.stringContaining("case_run=9301"),
    )

    fireEvent.click(screen.getByRole("button", { name: "批准" }))

    await waitFor(() => expect(resourcesApi.confirmPageProposal).toHaveBeenCalledWith("11"))
    expect(resourcesApi.confirmApiProposal).not.toHaveBeenCalled()
    expect(resourcesApi.rejectPageProposal).not.toHaveBeenCalled()
    await waitFor(() => expect(onChanged).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(versionApi.getDiscoveryObservations).toHaveBeenCalledTimes(2))
  })

  it("rejects a proposed API with the API decision function", async () => {
    versionApi.getDiscoveryObservations.mockResolvedValue([proposedApi])

    render(<DiscoveryPanel versionId="2001" />)

    expect(await screen.findByText("/api/orders")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "拒绝" }))

    await waitFor(() => expect(resourcesApi.rejectApiProposal).toHaveBeenCalledWith("12"))
    expect(resourcesApi.rejectPageProposal).not.toHaveBeenCalled()
    expect(resourcesApi.confirmApiProposal).not.toHaveBeenCalled()
  })

  it("hides the proposed-row decision actions for a non-admin", async () => {
    auth.isAdmin = false
    versionApi.getDiscoveryObservations.mockResolvedValue([proposedPage, proposedApi])

    render(<DiscoveryPanel versionId="2001" />)

    await screen.findByText("/orders/{id}")
    expect(screen.queryByRole("button", { name: "批准" })).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "拒绝" })).not.toBeInTheDocument()
    expect(screen.getAllByText("proposed")).toHaveLength(2)
  })

  it("shows an explicit empty state when nothing was discovered", async () => {
    versionApi.getDiscoveryObservations.mockResolvedValue([])

    render(<DiscoveryPanel versionId="2001" />)

    expect(await screen.findByText("该版本暂无探索发现")).toBeInTheDocument()
  })

  it("switches the kind filter and reloads on refreshKey", async () => {
    versionApi.getDiscoveryObservations.mockResolvedValue([proposedPage])
    const { rerender } = render(<DiscoveryPanel versionId="2001" />)
    await screen.findByText("/orders/{id}")
    expect(versionApi.getDiscoveryObservations).toHaveBeenLastCalledWith("2001", {
      decision: "proposed",
      limit: 50,
    })

    fireEvent.click(screen.getByRole("button", { name: "API" }))
    await waitFor(() =>
      expect(versionApi.getDiscoveryObservations).toHaveBeenLastCalledWith("2001", {
        kind: "api",
        decision: "proposed",
        limit: 50,
      }),
    )

    const calls = versionApi.getDiscoveryObservations.mock.calls.length
    rerender(<DiscoveryPanel versionId="2001" refreshKey={1} />)
    await waitFor(() =>
      expect(versionApi.getDiscoveryObservations.mock.calls.length).toBe(calls + 1),
    )
  })

  it("shows the list error and retries the read", async () => {
    versionApi.getDiscoveryObservations
      .mockRejectedValueOnce(
        new ApiError({
          status: 503,
          code: "unavailable",
          message: "探索发现读取失败",
          requestId: "req_discovery",
        }),
      )
      .mockResolvedValueOnce([proposedPage])

    render(<DiscoveryPanel versionId="2001" />)

    expect(await screen.findByText("探索发现读取失败")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(await screen.findByText("/orders/{id}")).toBeInTheDocument()
  })

  it("keeps the row on a failed decision and reports the action error", async () => {
    resourcesApi.confirmPageProposal.mockRejectedValue(
      new ApiError({
        status: 409,
        code: "conflict",
        message: "资源状态已变化",
        requestId: "req_confirm",
      }),
    )
    versionApi.getDiscoveryObservations.mockResolvedValue([proposedPage])

    render(<DiscoveryPanel versionId="2001" />)

    await screen.findByText("/orders/{id}")
    fireEvent.click(screen.getByRole("button", { name: "批准" }))

    expect(await screen.findByText("资源状态已变化")).toBeInTheDocument()
    expect(screen.getByText("/orders/{id}")).toBeInTheDocument()
    expect(versionApi.getDiscoveryObservations).toHaveBeenCalledTimes(1)
  })

  it("clears a stale action error when the panel reloads", async () => {
    resourcesApi.confirmPageProposal.mockRejectedValue(
      new ApiError({
        status: 409,
        code: "conflict",
        message: "资源状态已变化",
        requestId: "req_confirm",
      }),
    )
    versionApi.getDiscoveryObservations.mockResolvedValue([proposedPage])

    const { rerender } = render(<DiscoveryPanel versionId="2001" />)
    await screen.findByText("/orders/{id}")
    fireEvent.click(screen.getByRole("button", { name: "批准" }))
    expect(await screen.findByText("资源状态已变化")).toBeInTheDocument()

    // An SSE/refresh reload (or a filter switch) must not keep showing a stale
    // action error from a previous decision attempt.
    rerender(<DiscoveryPanel versionId="2001" refreshKey={1} />)
    await waitFor(() => expect(screen.queryByText("资源状态已变化")).not.toBeInTheDocument())
  })

  it("marks the active filters with aria-pressed", async () => {
    versionApi.getDiscoveryObservations.mockResolvedValue([proposedPage])

    render(<DiscoveryPanel versionId="2001" />)
    await screen.findByText("/orders/{id}")

    expect(screen.getByRole("button", { name: "全部类型" })).toHaveAttribute("aria-pressed", "true")
    expect(screen.getByRole("button", { name: "API" })).toHaveAttribute("aria-pressed", "false")
    expect(screen.getByRole("button", { name: "仅待处理" })).toHaveAttribute("aria-pressed", "true")
    expect(screen.getByRole("button", { name: "全部状态" })).toHaveAttribute(
      "aria-pressed",
      "false",
    )

    fireEvent.click(screen.getByRole("button", { name: "API" }))
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "API" })).toHaveAttribute("aria-pressed", "true"),
    )
    expect(screen.getByRole("button", { name: "全部类型" })).toHaveAttribute(
      "aria-pressed",
      "false",
    )
  })
})
