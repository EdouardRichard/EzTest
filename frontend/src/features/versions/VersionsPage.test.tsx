import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const mocks = vi.hoisted(() => ({
  listVersions: vi.fn(),
  createVersion: vi.fn(),
  listSystems: vi.fn(),
  listEnvironments: vi.fn(),
  listCodeRepos: vi.fn(),
}))

const router = vi.hoisted(() => ({
  push: vi.fn(),
  replace: vi.fn(),
  refresh: vi.fn(),
  back: vi.fn(),
  prefetch: vi.fn(),
  forward: vi.fn(),
}))

vi.mock("./api", () => ({
  listVersions: mocks.listVersions,
  createVersion: mocks.createVersion,
}))

vi.mock("@/features/resources/api", () => ({
  listSystems: mocks.listSystems,
  listEnvironments: mocks.listEnvironments,
}))

vi.mock("@/features/code-repos/api", () => ({
  listCodeRepos: mocks.listCodeRepos,
}))

vi.mock("next/navigation", () => ({
  useRouter: () => router,
}))

import VersionsRoutePage from "@/app/(app)/versions/page"

import type { Version } from "./types"
import { VersionsPage } from "./VersionsPage"

/** A snowflake ID that a JS number cannot hold; it must stay a string verbatim. */
const VERSION_ID = "1844674407370955161"
const OTHER_VERSION_ID = "1844674407370955162"

const VERSION: Version = {
  id: VERSION_ID,
  name: "订单系统 2.0",
  label: "v2.0",
  description: "订单系统第二阶段",
  status: "clarifying",
  systemId: 1,
  environmentId: 2,
  codeRepositoryId: 3,
  codeBranch: "main",
  codeBaselineCommit: "0123456789abcdef0123456789abcdef01234567",
  bindings: {
    system: { id: 1, name: "订单系统", available: true },
    environment: { id: 2, name: "staging", available: true },
    codeRepository: { id: 3, name: "order-web", available: true },
  },
  requirementCount: 3,
  clarifiedCount: 1,
  openQuestionCount: 2,
  revision: 2,
  createdBy: 1,
  updatedBy: 1,
  createdAt: "2026-10-27T10:00:00Z",
  updatedAt: "2026-10-27T10:00:00Z",
}

const OTHER_VERSION: Version = {
  ...VERSION,
  id: OTHER_VERSION_ID,
  name: "订单系统 3.0",
  label: "v3.0",
  status: "draft",
  requirementCount: 0,
  clarifiedCount: 0,
  openQuestionCount: 0,
}

const SYSTEM = {
  id: 1,
  key: "shop",
  name: "Shop",
  kind: "web",
  baseUrl: "http://localhost:13335",
  defaultSessionTtlSeconds: 3600,
  notes: "",
  status: "active",
  createdAt: "2026-10-27T00:00:00Z",
  updatedAt: "2026-10-27T00:00:00Z",
}

const ENVIRONMENT = {
  id: 2,
  systemId: 1,
  key: "staging",
  name: "staging",
  baseUrl: "http://localhost:13335",
  kind: "staging",
  isProd: false,
  allowedHosts: [],
  loginCheck: null,
  allowAutoLogin: false,
  requireApproval: false,
}

const CODE_REPO = {
  id: 3,
  key: "order-web",
  name: "order-web",
  localPath: "D:/repo",
  fixedBranch: "main",
  includeGlobs: ["src/**"],
  excludeGlobs: [".git/**"],
  status: "active",
  lastCommit: "abc",
  lastValidatedAt: null,
  lastValidationError: "",
  lastIndexedAt: null,
}

function page<T>(items: T[], nextCursor = "", hasMore = false) {
  return { items, nextCursor, hasMore }
}

describe("VersionsPage", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset()
    for (const mock of Object.values(router)) mock.mockReset()

    mocks.listVersions.mockResolvedValue(page([VERSION]))
    mocks.createVersion.mockResolvedValue(VERSION)
    mocks.listSystems.mockResolvedValue(page([SYSTEM]))
    mocks.listEnvironments.mockResolvedValue([ENVIRONMENT])
    mocks.listCodeRepos.mockResolvedValue(page([CODE_REPO]))
  })

  afterEach(() => {
    cleanup()
    window.history.replaceState(null, "", "/")
  })

  it("renders version rows with name, label, status and requirement counts", async () => {
    render(<VersionsPage />)

    const row = await screen.findByRole("row", { name: /订单系统 2.0/ })
    expect(within(row).getByText("v2.0")).toBeTruthy()
    expect(within(row).getByText("澄清中")).toBeTruthy()
    expect(within(row).getByText("3")).toBeTruthy()
    expect(within(row).getByText("1")).toBeTruthy()
    expect(within(row).getByText("2")).toBeTruthy()
    expect(within(row).getByRole("link", { name: "查看" }).getAttribute("href")).toBe(
      `/versions/${VERSION_ID}`,
    )
    expect(screen.getByRole("columnheader", { name: "待澄清" })).toBeTruthy()
    expect(mocks.listVersions).toHaveBeenCalledWith({ limit: 20 })
  })

  it("filters by status and reloads the first page", async () => {
    mocks.listVersions
      .mockResolvedValueOnce(page([VERSION]))
      .mockResolvedValueOnce(page([OTHER_VERSION]))

    render(<VersionsPage />)
    await screen.findByText("订单系统 2.0")

    fireEvent.change(screen.getByLabelText("状态筛选"), { target: { value: "draft" } })

    await waitFor(() =>
      expect(mocks.listVersions).toHaveBeenLastCalledWith({ status: "draft", limit: 20 }),
    )
    expect(await screen.findByText("订单系统 3.0")).toBeTruthy()
    expect(screen.queryByText("订单系统 2.0")).toBeNull()
  })

  it("appends the next cursor page when loading more", async () => {
    mocks.listVersions
      .mockResolvedValueOnce(page([VERSION], "cursor-2", true))
      .mockResolvedValueOnce(page([OTHER_VERSION]))

    render(<VersionsPage />)
    await screen.findByText("订单系统 2.0")

    fireEvent.click(screen.getByRole("button", { name: "加载更多" }))

    await waitFor(() =>
      expect(mocks.listVersions).toHaveBeenLastCalledWith({ cursor: "cursor-2", limit: 20 }),
    )
    expect(await screen.findByText("订单系统 3.0")).toBeTruthy()
    expect(screen.getByText("订单系统 2.0")).toBeTruthy()
  })

  it("shows the empty state when no version exists", async () => {
    mocks.listVersions.mockResolvedValue(page([]))

    render(<VersionsPage />)

    expect(await screen.findByText("暂无版本")).toBeTruthy()
    expect(screen.getByRole("button", { name: "新建版本" })).toBeTruthy()
  })

  it("rejects an empty name in the create dialog", async () => {
    render(<VersionsPage />)
    await screen.findByText("订单系统 2.0")

    fireEvent.click(screen.getByRole("button", { name: "新建版本" }))
    const dialog = await screen.findByRole("dialog")
    fireEvent.click(within(dialog).getByRole("button", { name: "创建版本" }))

    const alert = await within(dialog).findByRole("alert")
    expect(alert.textContent).toContain("请输入版本名称")
    expect(mocks.createVersion).not.toHaveBeenCalled()
    expect(screen.getByRole("dialog")).toBeTruthy()
  })

  it("cascades system → environment → code repo, creates and navigates", async () => {
    render(<VersionsPage />)
    await screen.findByText("订单系统 2.0")

    fireEvent.click(screen.getByRole("button", { name: "新建版本" }))
    const dialog = await screen.findByRole("dialog")

    await within(dialog).findByRole("option", { name: "Shop" })
    const environmentSelect = within(dialog).getByLabelText("环境") as HTMLSelectElement
    expect(environmentSelect.disabled).toBe(true)

    fireEvent.change(within(dialog).getByLabelText("系统"), { target: { value: "1" } })
    await waitFor(() => expect(mocks.listEnvironments).toHaveBeenCalledWith(1))
    await within(dialog).findByRole("option", { name: "staging" })
    expect((within(dialog).getByLabelText("环境") as HTMLSelectElement).disabled).toBe(false)

    fireEvent.change(within(dialog).getByLabelText("环境"), { target: { value: "2" } })
    await within(dialog).findByRole("option", { name: "order-web" })
    fireEvent.change(within(dialog).getByLabelText("代码仓"), { target: { value: "3" } })
    fireEvent.change(within(dialog).getByLabelText("名称"), { target: { value: "订单系统 2.0" } })
    fireEvent.change(within(dialog).getByLabelText("标识"), { target: { value: "v2.0" } })
    fireEvent.click(within(dialog).getByRole("button", { name: "创建版本" }))

    await waitFor(() =>
      expect(mocks.createVersion).toHaveBeenCalledWith({
        name: "订单系统 2.0",
        label: "v2.0",
        systemId: 1,
        environmentId: 2,
        codeRepositoryId: 3,
      }),
    )
    expect(router.push).toHaveBeenCalledWith(`/versions/${VERSION_ID}`)
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
  })

  it("navigates to the detail route when a row is clicked", async () => {
    render(<VersionsPage />)

    const row = await screen.findByRole("row", { name: /订单系统 2.0/ })
    fireEvent.click(row)

    expect(router.push).toHaveBeenCalledWith(`/versions/${VERSION_ID}`)
  })

  it("renders the versions feature page from the /versions route", async () => {
    render(<VersionsRoutePage />)

    expect(await screen.findByRole("heading", { level: 1, name: "版本" })).toBeTruthy()
    expect(await screen.findByText("订单系统 2.0")).toBeTruthy()
  })
})
