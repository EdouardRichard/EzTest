import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const mocks = vi.hoisted(() => ({
  listSystems: vi.fn(),
  createSystem: vi.fn(),
  updateSystem: vi.fn(),
}))

vi.mock("./api", () => ({
  listSystems: mocks.listSystems,
  createSystem: mocks.createSystem,
  updateSystem: mocks.updateSystem,
}))

import { ApiError } from "@/lib/api-client"

import { SystemsPanel } from "./SystemsPanel"
import type { SystemDTO } from "./types"

function makeSystem(
  id: number,
  key: string,
  name: string,
  overrides: Partial<SystemDTO> = {},
): SystemDTO {
  return {
    id,
    key,
    name,
    kind: "web",
    baseUrl: "http://localhost:13335",
    defaultSessionTtlSeconds: 3600,
    notes: "",
    status: "active",
    createdAt: "2026-10-27T00:00:00Z",
    updatedAt: "2026-10-27T00:00:00Z",
    ...overrides,
  }
}

const SHOP = makeSystem(1, "shop", "Shop")
const BLOG = makeSystem(2, "blog", "Blog", { kind: "api", baseUrl: "http://localhost:13336" })

describe("SystemsPanel", () => {
  beforeEach(() => {
    mocks.listSystems.mockReset()
    mocks.createSystem.mockReset()
    mocks.updateSystem.mockReset()
    mocks.listSystems.mockResolvedValue({ items: [SHOP, BLOG], nextCursor: null, hasMore: false })
  })

  afterEach(() => {
    cleanup()
  })

  it("lists systems and hides every write control from readonly users", async () => {
    render(<SystemsPanel canWrite={false} selectedId={null} onSelect={vi.fn()} />)

    expect(await screen.findByText("Shop")).toBeTruthy()
    expect(screen.getByText("Blog")).toBeTruthy()
    expect(screen.queryByRole("button", { name: "新建系统" })).toBeNull()
    expect(screen.queryByRole("button", { name: "编辑 Shop" })).toBeNull()
    await waitFor(() => expect(mocks.listSystems).toHaveBeenCalledWith({ limit: 100 }))
  })

  it("creates a system from the labelled inline form and selects it", async () => {
    const created = makeSystem(9, "checkout", "Checkout", {
      baseUrl: "http://localhost:13337",
      defaultSessionTtlSeconds: 7200,
      notes: "支付系统",
    })
    mocks.createSystem.mockResolvedValue(created)
    mocks.listSystems
      .mockResolvedValueOnce({ items: [SHOP], nextCursor: null, hasMore: false })
      .mockResolvedValue({ items: [SHOP, created], nextCursor: null, hasMore: false })
    const onSelect = vi.fn()

    render(<SystemsPanel canWrite selectedId={null} onSelect={onSelect} />)
    await screen.findByText("Shop")

    fireEvent.click(screen.getByRole("button", { name: "新建系统" }))
    fireEvent.change(screen.getByLabelText("系统标识 key"), { target: { value: "checkout" } })
    fireEvent.change(screen.getByLabelText("名称"), { target: { value: "Checkout" } })
    fireEvent.change(screen.getByLabelText("Base URL"), {
      target: { value: "http://localhost:13337" },
    })
    fireEvent.change(screen.getByLabelText("默认会话 TTL（秒）"), { target: { value: "7200" } })
    fireEvent.change(screen.getByLabelText("备注"), { target: { value: "支付系统" } })
    fireEvent.click(screen.getByRole("button", { name: "保存系统" }))

    await waitFor(() =>
      expect(mocks.createSystem).toHaveBeenCalledWith({
        key: "checkout",
        name: "Checkout",
        kind: "web",
        baseUrl: "http://localhost:13337",
        defaultSessionTtlSeconds: 7200,
        notes: "支付系统",
      }),
    )
    await waitFor(() => expect(onSelect).toHaveBeenCalledWith(9))
    expect(await screen.findByRole("status")).toBeTruthy()
    await waitFor(() => expect(mocks.listSystems).toHaveBeenCalledTimes(2))
  })

  it("edits a system without resubmitting its immutable key", async () => {
    mocks.updateSystem.mockResolvedValue({ ...SHOP, name: "Shop 2" })

    render(<SystemsPanel canWrite selectedId={1} onSelect={vi.fn()} />)
    await screen.findByText("Shop")

    fireEvent.click(screen.getByRole("button", { name: "编辑 Shop" }))
    const keyInput = screen.getByLabelText("系统标识 key") as HTMLInputElement
    expect(keyInput.disabled).toBe(true)
    expect(keyInput.value).toBe("shop")

    fireEvent.change(screen.getByLabelText("名称"), { target: { value: "Shop 2" } })
    fireEvent.click(screen.getByRole("button", { name: "保存系统" }))

    await waitFor(() =>
      expect(mocks.updateSystem).toHaveBeenCalledWith(1, {
        name: "Shop 2",
        kind: "web",
        baseUrl: "http://localhost:13335",
        defaultSessionTtlSeconds: 3600,
        notes: "",
      }),
    )
  })

  it("rejects an invalid key before calling the write API", async () => {
    render(<SystemsPanel canWrite selectedId={null} onSelect={vi.fn()} />)
    await screen.findByText("Shop")

    fireEvent.click(screen.getByRole("button", { name: "新建系统" }))
    fireEvent.change(screen.getByLabelText("系统标识 key"), { target: { value: "BAD KEY" } })
    fireEvent.change(screen.getByLabelText("名称"), { target: { value: "Bad" } })
    fireEvent.change(screen.getByLabelText("Base URL"), { target: { value: "http://localhost:1" } })
    fireEvent.click(screen.getByRole("button", { name: "保存系统" }))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("系统标识")
    expect(mocks.createSystem).not.toHaveBeenCalled()
  })

  it("surfaces load failures in an alert", async () => {
    mocks.listSystems.mockRejectedValue(
      new ApiError({ status: 403, code: "forbidden", message: "admin only" }),
    )

    render(<SystemsPanel canWrite={false} selectedId={null} onSelect={vi.fn()} />)

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("管理员")
  })
})
