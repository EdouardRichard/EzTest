import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const mocks = vi.hoisted(() => ({
  listAppModels: vi.fn(),
  importAppModels: vi.fn(),
  createAppModel: vi.fn(),
  updateAppModel: vi.fn(),
}))

vi.mock("./api", () => ({
  listAppModels: mocks.listAppModels,
  importAppModels: mocks.importAppModels,
  createAppModel: mocks.createAppModel,
  updateAppModel: mocks.updateAppModel,
}))

import { ApiError } from "@/lib/api-client"

import { AppModelsPanel } from "./AppModelsPanel"
import type { AppModelNodeDTO } from "./types"

function makeNode(
  id: number,
  kind: string,
  key: string,
  name: string,
  path: string,
  method = "",
  source = "manual",
): AppModelNodeDTO {
  return {
    id,
    systemId: 1,
    parentId: null,
    kind,
    key,
    name,
    path,
    method,
    source,
    meta: {},
  }
}

const ROUTE = makeNode(5, "route", "route:/orders", "Orders", "/orders")
const API_NODE = makeNode(
  6,
  "api",
  "api:GET /api/orders",
  "订单查询",
  "/api/orders",
  "GET",
  "import",
)

function page(items: AppModelNodeDTO[]) {
  return { items, nextCursor: null, hasMore: false }
}

describe("AppModelsPanel", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset()
    mocks.listAppModels.mockResolvedValue(page([ROUTE, API_NODE]))
  })

  afterEach(() => {
    cleanup()
  })

  it("lists nodes and hides every write control from readonly users", async () => {
    render(<AppModelsPanel systemId={1} canWrite={false} />)

    expect(await screen.findByText("route:/orders")).toBeTruthy()
    expect(screen.getByText("api:GET /api/orders")).toBeTruthy()
    expect(screen.getByText("Orders")).toBeTruthy()
    expect(screen.getByText("GET")).toBeTruthy()
    expect(screen.getByText("manual")).toBeTruthy()
    expect(screen.getByText("import")).toBeTruthy()
    expect(screen.getByRole("columnheader", { name: "来源" })).toBeTruthy()

    expect(screen.queryByRole("button", { name: "新建节点" })).toBeNull()
    expect(screen.queryByRole("button", { name: "JSON 导入" })).toBeNull()
    expect(screen.queryByRole("button", { name: "编辑 Orders" })).toBeNull()
  })

  it("requests the maximum page size and reloads when the kind filter changes", async () => {
    render(<AppModelsPanel systemId={1} canWrite={false} />)
    await screen.findByText("route:/orders")

    expect(mocks.listAppModels).toHaveBeenCalledWith(1, { limit: 100 })

    fireEvent.change(screen.getByLabelText("按类型筛选"), { target: { value: "api" } })

    await waitFor(() =>
      expect(mocks.listAppModels).toHaveBeenLastCalledWith(1, { kind: "api", limit: 100 }),
    )
  })

  it("imports app model JSON and reports created/updated counts", async () => {
    mocks.importAppModels.mockResolvedValue({ created: 2, updated: 1 })

    render(<AppModelsPanel systemId={1} canWrite />)
    await screen.findByText("route:/orders")

    fireEvent.click(screen.getByRole("button", { name: "JSON 导入" }))
    expect(screen.getByRole("dialog")).toBeTruthy()

    const nodes = [{ key: "route:/cart", kind: "route", name: "Cart", path: "/cart" }]
    fireEvent.change(screen.getByLabelText("JSON"), {
      target: { value: JSON.stringify({ nodes }) },
    })
    fireEvent.click(screen.getByRole("button", { name: "导入" }))

    await waitFor(() => expect(mocks.importAppModels).toHaveBeenCalledWith(1, nodes))
    const status = await screen.findByRole("status")
    expect(status.textContent).toContain("新建 2")
    expect(status.textContent).toContain("更新 1")
    await waitFor(() => expect(mocks.listAppModels).toHaveBeenCalledTimes(2))
  })

  it("reports malformed JSON without calling the import API", async () => {
    render(<AppModelsPanel systemId={1} canWrite />)
    await screen.findByText("route:/orders")

    fireEvent.click(screen.getByRole("button", { name: "JSON 导入" }))
    fireEvent.change(screen.getByLabelText("JSON"), { target: { value: '{"nodes":[' } })
    fireEvent.click(screen.getByRole("button", { name: "导入" }))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("JSON")
    expect(mocks.importAppModels).not.toHaveBeenCalled()
  })

  it("rejects import nodes without a key and kind", async () => {
    render(<AppModelsPanel systemId={1} canWrite />)
    await screen.findByText("route:/orders")

    fireEvent.click(screen.getByRole("button", { name: "JSON 导入" }))
    fireEvent.change(screen.getByLabelText("JSON"), {
      target: { value: '{"nodes":[{"name":"Cart"}]}' },
    })
    fireEvent.click(screen.getByRole("button", { name: "导入" }))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("key")
    expect(mocks.importAppModels).not.toHaveBeenCalled()
  })

  it("shows the 422 reason returned by the import API", async () => {
    mocks.importAppModels.mockRejectedValue(
      new ApiError({
        status: 422,
        code: "invalid_argument",
        message: "route keys must start with route:",
        details: { reason: "invalid_app_model_key", field: "key" },
      }),
    )

    render(<AppModelsPanel systemId={1} canWrite />)
    await screen.findByText("route:/orders")

    fireEvent.click(screen.getByRole("button", { name: "JSON 导入" }))
    fireEvent.change(screen.getByLabelText("JSON"), {
      target: { value: '{"nodes":[{"key":"page:/x","kind":"route"}]}' },
    })
    fireEvent.click(screen.getByRole("button", { name: "导入" }))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("invalid_app_model_key")
    expect(alert.textContent).toContain("route keys must start with route:")
  })

  it("rejects an import file larger than 5 MiB", async () => {
    render(<AppModelsPanel systemId={1} canWrite />)
    await screen.findByText("route:/orders")

    fireEvent.click(screen.getByRole("button", { name: "JSON 导入" }))
    const oversized = new File([new Uint8Array(5 * 1024 * 1024 + 1)], "big.json", {
      type: "application/json",
    })
    fireEvent.change(screen.getByLabelText("JSON 文件"), { target: { files: [oversized] } })

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("5 MiB")
    expect(mocks.importAppModels).not.toHaveBeenCalled()
  })

  it("creates a node with its immutable key and kind", async () => {
    mocks.createAppModel.mockResolvedValue(
      makeNode(7, "component", "component:Cart", "Cart", "/cart"),
    )

    render(<AppModelsPanel systemId={1} canWrite />)
    await screen.findByText("route:/orders")

    fireEvent.click(screen.getByRole("button", { name: "新建节点" }))
    fireEvent.change(screen.getByLabelText("节点类型"), { target: { value: "component" } })
    fireEvent.change(screen.getByLabelText("节点 key"), {
      target: { value: "component:Cart" },
    })
    fireEvent.change(screen.getByLabelText("名称"), { target: { value: "Cart" } })
    fireEvent.change(screen.getByLabelText("路径"), { target: { value: "/cart" } })
    fireEvent.click(screen.getByRole("button", { name: "保存节点" }))

    await waitFor(() =>
      expect(mocks.createAppModel).toHaveBeenCalledWith(1, {
        key: "component:Cart",
        kind: "component",
        name: "Cart",
        path: "/cart",
        method: "",
      }),
    )
    expect(await screen.findByRole("status")).toBeTruthy()
  })

  it("edits mutable node fields without resubmitting key and kind", async () => {
    mocks.updateAppModel.mockResolvedValue({ ...ROUTE, name: "Orders 2" })

    render(<AppModelsPanel systemId={1} canWrite />)
    await screen.findByText("route:/orders")

    fireEvent.click(screen.getByRole("button", { name: "编辑 Orders" }))
    const keyInput = screen.getByLabelText("节点 key") as HTMLInputElement
    const kindSelect = screen.getByLabelText("节点类型") as HTMLSelectElement
    expect(keyInput.disabled).toBe(true)
    expect(kindSelect.disabled).toBe(true)
    expect(keyInput.value).toBe("route:/orders")

    fireEvent.change(screen.getByLabelText("名称"), { target: { value: "Orders 2" } })
    fireEvent.click(screen.getByRole("button", { name: "保存节点" }))

    await waitFor(() =>
      expect(mocks.updateAppModel).toHaveBeenCalledWith(5, {
        name: "Orders 2",
        path: "/orders",
        method: "",
      }),
    )
  })
})
