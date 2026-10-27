import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const mocks = vi.hoisted(() => ({
  listSystems: vi.fn(),
  createSystem: vi.fn(),
  updateSystem: vi.fn(),
  listEnvironments: vi.fn(),
  createEnvironment: vi.fn(),
  updateEnvironment: vi.fn(),
  listAppModels: vi.fn(),
  importAppModels: vi.fn(),
  createAppModel: vi.fn(),
  updateAppModel: vi.fn(),
  listAccounts: vi.fn(),
  createAccount: vi.fn(),
  updateAccount: vi.fn(),
  listAccesses: vi.fn(),
  createAccess: vi.fn(),
  updateAccess: vi.fn(),
}))

const auth = vi.hoisted(() => ({ role: "user" as "user" | "admin" }))

vi.mock("@/features/auth/AuthProvider", () => ({
  useAuth: () => ({
    user: { id: 1, username: "qa01", role: auth.role, status: "active" },
    loading: false,
    login: vi.fn(),
    logout: vi.fn(),
  }),
}))

vi.mock("./api", () => ({
  listSystems: mocks.listSystems,
  createSystem: mocks.createSystem,
  updateSystem: mocks.updateSystem,
  listEnvironments: mocks.listEnvironments,
  createEnvironment: mocks.createEnvironment,
  updateEnvironment: mocks.updateEnvironment,
  listAppModels: mocks.listAppModels,
  importAppModels: mocks.importAppModels,
  createAppModel: mocks.createAppModel,
  updateAppModel: mocks.updateAppModel,
  listAccounts: mocks.listAccounts,
  createAccount: mocks.createAccount,
  updateAccount: mocks.updateAccount,
  listAccesses: mocks.listAccesses,
  createAccess: mocks.createAccess,
  updateAccess: mocks.updateAccess,
}))

import { ResourcesPage } from "./ResourcesPage"
import type { EnvironmentDTO, SystemDTO } from "./types"

function makeSystem(id: number, key: string, name: string): SystemDTO {
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
  }
}

const SHOP = makeSystem(1, "shop", "Shop")
const BLOG = makeSystem(2, "blog", "Blog")

function makeEnvironment(id: number, systemId: number, name: string): EnvironmentDTO {
  return {
    id,
    systemId,
    key: `env-${id}`,
    name,
    baseUrl: "http://localhost:13335",
    kind: "staging",
    isProd: false,
    allowedHosts: [],
    loginCheck: null,
    allowAutoLogin: false,
    requireApproval: false,
  }
}

describe("ResourcesPage", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset()
    auth.role = "user"
    mocks.listSystems.mockResolvedValue({ items: [], nextCursor: null, hasMore: false })
    mocks.listEnvironments.mockResolvedValue([])
    mocks.listAppModels.mockResolvedValue({ items: [], nextCursor: null, hasMore: false })
    mocks.listAccounts.mockResolvedValue({ items: [], nextCursor: null, hasMore: false })
    mocks.listAccesses.mockResolvedValue([])
  })

  afterEach(() => {
    cleanup()
    window.history.replaceState(null, "", "/")
  })

  it("shows the readonly badge and all four tab labels for a normal user", async () => {
    mocks.listSystems.mockResolvedValue({ items: [SHOP], nextCursor: null, hasMore: false })

    render(<ResourcesPage />)

    expect(await screen.findByText("Shop")).toBeTruthy()
    expect(screen.getByText("只读")).toBeTruthy()
    for (const name of ["系统与环境", "应用模型", "账号与访问", "会话"]) {
      expect(screen.getByRole("tab", { name })).toBeTruthy()
    }
    expect(screen.queryByRole("button", { name: "新建系统" })).toBeNull()
  })

  it("opens the tab named by the ?tab= deep link", async () => {
    window.history.replaceState(null, "", "/resources?tab=sessions")

    render(<ResourcesPage />)

    await waitFor(() =>
      expect(screen.getByRole("tab", { name: "会话" }).getAttribute("aria-selected")).toBe("true"),
    )
    expect(await screen.findByRole("heading", { name: "会话面板即将上线" })).toBeTruthy()
  })

  it("switches tabs and records the active tab in the URL", async () => {
    render(<ResourcesPage />)
    await screen.findByText("暂无系统")

    fireEvent.mouseDown(screen.getByRole("tab", { name: "应用模型" }))

    expect(await screen.findByRole("heading", { name: "请选择系统" })).toBeTruthy()
    await waitFor(() => expect(window.location.search).toContain("tab=app-models"))
  })

  it("shows admin write controls without the readonly badge", async () => {
    auth.role = "admin"
    mocks.listSystems.mockResolvedValue({ items: [SHOP], nextCursor: null, hasMore: false })

    render(<ResourcesPage />)

    expect(await screen.findByRole("button", { name: "新建系统" })).toBeTruthy()
    expect(screen.queryByText("只读")).toBeNull()
  })

  it("selects the system named by the ?system= deep link and loads its environments", async () => {
    window.history.replaceState(null, "", "/resources?tab=systems&system=2")
    mocks.listSystems.mockResolvedValue({
      items: [SHOP, BLOG],
      nextCursor: null,
      hasMore: false,
    })
    mocks.listEnvironments.mockResolvedValue([makeEnvironment(5, 2, "Blog 预发")])

    render(<ResourcesPage />)

    expect(await screen.findByText("Blog 预发")).toBeTruthy()
    expect(mocks.listEnvironments).toHaveBeenCalledWith(2)
    const blogRow = screen.getByRole("button", { name: "Blog" })
    expect(blogRow.getAttribute("aria-current")).toBe("true")

    fireEvent.click(screen.getByRole("button", { name: "Shop" }))
    await waitFor(() => expect(window.location.search).toContain("system=1"))
  })
})
