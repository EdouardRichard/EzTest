import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

vi.mock("./api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./api")>()
  return {
    ...actual,
    createDatabase: vi.fn(),
    updateDatabase: vi.fn(),
    deleteDatabase: vi.fn(),
    createAccount: vi.fn(),
    updateAccount: vi.fn(),
    deleteAccount: vi.fn(),
  }
})

import * as api from "./api"
import { DataAccessPanel, type DataAccessPanelProps } from "./DataAccessPanel"
import {
  accountFixture,
  databaseFixture,
  secondSutFixture,
  sutFixture,
  sutListItemFixture,
} from "./test-fixtures"
import type { ResourceScope } from "./types"

const baseScope: ResourceScope = {
  suts: [sutListItemFixture, secondSutFixture],
  sutId: "1001",
  environmentId: "1101",
  detail: sutFixture,
  loading: false,
  detailLoading: false,
  error: null,
  detailError: null,
}

function renderPanel(overrides: Partial<DataAccessPanelProps> = {}): {
  onRefresh: ReturnType<typeof vi.fn>
} {
  const onRefresh = vi.fn().mockResolvedValue(undefined)
  const props: DataAccessPanelProps = {
    scope: baseScope,
    onSelectSut: vi.fn(),
    onSelectEnvironment: vi.fn(),
    onRefresh,
    onRetry: vi.fn(),
    ...overrides,
  }
  render(<DataAccessPanel {...props} />)
  return { onRefresh }
}

describe("DataAccessPanel", () => {
  beforeEach(() => {
    vi.mocked(api.createDatabase).mockReset()
    vi.mocked(api.updateDatabase).mockReset()
    vi.mocked(api.deleteDatabase).mockReset()
    vi.mocked(api.createAccount).mockReset()
    vi.mocked(api.updateAccount).mockReset()
    vi.mocked(api.deleteAccount).mockReset()
  })

  afterEach(cleanup)

  it("shows password_set only, never a stored password value", () => {
    renderPanel()

    expect(screen.getByRole("table", { name: "目标库列表" })).toBeInTheDocument()
    expect(screen.getByRole("table", { name: "测试账号列表" })).toBeInTheDocument()
    expect(screen.getByText("order_db")).toBeInTheDocument()
    expect(screen.getAllByText("buyer").length).toBeGreaterThan(0)
    expect(screen.getAllByText("已设置")).toHaveLength(2)
    expect(screen.queryByText("secret")).not.toBeInTheDocument()
  })

  it("validates the database form and creates a database in the selected environment", async () => {
    vi.mocked(api.createDatabase).mockResolvedValue(databaseFixture)
    const { onRefresh } = renderPanel()

    fireEvent.click(screen.getByRole("button", { name: "新建目标库" }))
    fireEvent.click(screen.getByRole("button", { name: "创建目标库" }))
    expect(await screen.findByText("请输入目标库名称")).toBeInTheDocument()
    expect(api.createDatabase).not.toHaveBeenCalled()

    fireEvent.change(screen.getByLabelText("库名称"), { target: { value: "pay_db" } })
    fireEvent.change(screen.getByLabelText("DSN"), {
      target: { value: "postgres://pay-db:5432/pay" },
    })
    fireEvent.change(screen.getByLabelText("用户名"), { target: { value: "pay" } })
    fireEvent.change(screen.getByLabelText("密码"), { target: { value: "secret" } })
    fireEvent.click(screen.getByRole("button", { name: "创建目标库" }))

    await waitFor(() =>
      expect(api.createDatabase).toHaveBeenCalledWith("1001", {
        environmentId: "1101",
        name: "pay_db",
        dsn: "postgres://pay-db:5432/pay",
        username: "pay",
        password: "secret",
        readOnly: true,
      }),
    )
    expect(onRefresh).toHaveBeenCalled()
  })

  it("keeps a blank password on update and clears it only when requested", async () => {
    vi.mocked(api.updateDatabase).mockResolvedValue(databaseFixture)
    renderPanel()

    fireEvent.click(screen.getByRole("button", { name: "编辑数据库 order_db" }))
    expect(screen.getByLabelText("密码")).toHaveValue("")
    expect(screen.getByText(/密码已设置/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "保存目标库" }))

    await waitFor(() => expect(api.updateDatabase).toHaveBeenCalledTimes(1))
    const keepPatch = vi.mocked(api.updateDatabase).mock.calls[0][1]
    expect(keepPatch).not.toHaveProperty("password")

    fireEvent.click(screen.getByRole("button", { name: "编辑数据库 order_db" }))
    fireEvent.click(screen.getByLabelText("清除已保存的密码"))
    fireEvent.click(screen.getByRole("button", { name: "保存目标库" }))

    await waitFor(() => expect(api.updateDatabase).toHaveBeenCalledTimes(2))
    expect(vi.mocked(api.updateDatabase).mock.calls[1][1]).toMatchObject({ password: "" })
  })

  it("requires an initial password when creating an account and sends roles on update", async () => {
    vi.mocked(api.createAccount).mockResolvedValue(accountFixture)
    vi.mocked(api.updateAccount).mockResolvedValue(accountFixture)
    const { onRefresh } = renderPanel()

    fireEvent.click(screen.getByRole("button", { name: "新建测试账号" }))
    fireEvent.click(screen.getByRole("button", { name: "创建账号" }))
    expect(await screen.findByText("请输入初始密码")).toBeInTheDocument()

    fireEvent.change(screen.getByLabelText("账号用户名"), { target: { value: "qa" } })
    fireEvent.change(screen.getByLabelText("密码"), { target: { value: "secret" } })
    fireEvent.change(screen.getByLabelText("角色（逗号分隔）"), {
      target: { value: "buyer, admin" },
    })
    fireEvent.click(screen.getByRole("button", { name: "创建账号" }))

    await waitFor(() =>
      expect(api.createAccount).toHaveBeenCalledWith("1001", {
        environmentId: "1101",
        username: "qa",
        password: "secret",
        roles: "buyer, admin",
        notes: "",
      }),
    )
    expect(onRefresh).toHaveBeenCalled()

    fireEvent.click(screen.getByRole("button", { name: "编辑账号 buyer" }))
    fireEvent.change(screen.getByLabelText("角色（逗号分隔）"), {
      target: { value: "buyer, order_admin" },
    })
    fireEvent.click(screen.getByRole("button", { name: "保存账号" }))

    await waitFor(() =>
      expect(api.updateAccount).toHaveBeenCalledWith("1601", {
        username: "buyer",
        roles: ["buyer", "order_admin"],
        notes: "",
      }),
    )
  })

  it("surfaces a 409 when an account cannot be deleted because a version references it", async () => {
    vi.mocked(api.deleteAccount).mockRejectedValue(
      new ApiError({
        status: 409,
        code: "conflict",
        message: "account is referenced by a version binding",
      }),
    )
    const { onRefresh } = renderPanel()

    fireEvent.click(screen.getByRole("button", { name: "删除账号 buyer" }))
    fireEvent.click(screen.getByRole("button", { name: "删除账号" }))

    expect(
      await screen.findByText(/account is referenced by a version binding/),
    ).toBeInTheDocument()
    expect(screen.getByText(/操作冲突/)).toBeInTheDocument()
    expect(onRefresh).not.toHaveBeenCalled()
  })

  it("prompts for an environment when the SUT has none", () => {
    renderPanel({
      scope: { ...baseScope, detail: { ...sutFixture, environments: [] }, environmentId: "" },
    })
    expect(screen.getByText("还没有环境")).toBeInTheDocument()
  })
})
