import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const mocks = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  createAccount: vi.fn(),
  updateAccount: vi.fn(),
  deleteAccount: vi.fn(),
  listSystems: vi.fn(),
  listAccesses: vi.fn(),
  createAccess: vi.fn(),
  updateAccess: vi.fn(),
  deleteAccess: vi.fn(),
}))

vi.mock("./api", () => ({
  listAccounts: mocks.listAccounts,
  createAccount: mocks.createAccount,
  updateAccount: mocks.updateAccount,
  deleteAccount: mocks.deleteAccount,
  listSystems: mocks.listSystems,
  listAccesses: mocks.listAccesses,
  createAccess: mocks.createAccess,
  updateAccess: mocks.updateAccess,
  deleteAccess: mocks.deleteAccess,
}))

import { ApiError } from "@/lib/api-client"

import { AccountsPanel } from "./AccountsPanel"
import type { AccessDTO, AccountDTO, SystemDTO } from "./types"

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

function makeAccount(
  id: number,
  key: string,
  label: string,
  username: string,
  overrides: Partial<AccountDTO> = {},
): AccountDTO {
  return {
    id,
    key,
    label,
    username,
    kind: "human",
    status: "active",
    notes: "",
    ...overrides,
  }
}

const QA_ACCOUNT = makeAccount(3, "qa01", "QA 01", "qa01")
const SERVICE_ACCOUNT = makeAccount(4, "svc", "SVC", "svc", { kind: "service" })

function makeAccess(
  id: number,
  accountId: number,
  systemId: number,
  overrides: Partial<AccessDTO> = {},
): AccessDTO {
  return {
    id,
    accountId,
    systemId,
    authType: "token",
    credentialSet: true,
    usernameOverride: "",
    loginUrl: "",
    enabled: true,
    concurrencyLimit: 1,
    sessionTtlSeconds: null,
    permissionNote: "",
    ...overrides,
  }
}

const ACCESSES = [
  makeAccess(8, 3, 1),
  makeAccess(9, 3, 2, {
    authType: "password",
    credentialSet: false,
    concurrencyLimit: 2,
    sessionTtlSeconds: 3600,
  }),
]

function deferred<T>() {
  let resolve: (value: T) => void = () => {}
  let reject: (reason?: unknown) => void = () => {}
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

describe("AccountsPanel", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset()
    mocks.listSystems.mockResolvedValue({ items: [SHOP, BLOG], nextCursor: null, hasMore: false })
    mocks.listAccounts.mockResolvedValue({
      items: [QA_ACCOUNT, SERVICE_ACCOUNT],
      nextCursor: null,
      hasMore: false,
    })
    mocks.listAccesses.mockResolvedValue(ACCESSES)
  })

  afterEach(() => {
    cleanup()
  })

  it("lists accounts, loads accesses for the selected account and hides write controls", async () => {
    render(<AccountsPanel canWrite={false} />)

    expect(await screen.findByText("QA 01")).toBeTruthy()
    expect(screen.getByText("SVC")).toBeTruthy()
    await waitFor(() => {
      expect(mocks.listAccounts).toHaveBeenCalledWith({ limit: 100 })
      expect(mocks.listSystems).toHaveBeenCalledWith({ limit: 100 })
    })

    expect(screen.queryByRole("button", { name: "新建账号" })).toBeNull()
    expect(screen.queryByRole("button", { name: "编辑账号 QA 01" })).toBeNull()
    expect(screen.queryByRole("button", { name: "删除账号 QA 01" })).toBeNull()

    fireEvent.click(screen.getByRole("button", { name: "QA 01" }))

    await waitFor(() => expect(mocks.listAccesses).toHaveBeenCalledWith(3, { limit: 100 }))
    expect(await screen.findByText("Shop")).toBeTruthy()
    expect(screen.getByText("Blog")).toBeTruthy()
    expect(screen.getByText("Token")).toBeTruthy()
    expect(screen.getByText("已配置")).toBeTruthy()
    expect(screen.getByText("未配置")).toBeTruthy()
    expect(screen.getByText("继承系统")).toBeTruthy()
    expect(screen.queryByRole("button", { name: "新建 Access" })).toBeNull()
    expect(screen.queryByRole("button", { name: "编辑 Access 8" })).toBeNull()
    expect(screen.queryByRole("button", { name: "删除 Access 8" })).toBeNull()
  })

  it("creates an account and selects it", async () => {
    const created = makeAccount(7, "qa02", "QA 02", "qa02")
    mocks.createAccount.mockResolvedValue(created)
    mocks.listAccounts
      .mockResolvedValueOnce({ items: [QA_ACCOUNT], nextCursor: null, hasMore: false })
      .mockResolvedValue({
        items: [QA_ACCOUNT, created],
        nextCursor: null,
        hasMore: false,
      })

    render(<AccountsPanel canWrite />)
    await screen.findByText("QA 01")

    fireEvent.click(screen.getByRole("button", { name: "新建账号" }))
    fireEvent.change(screen.getByLabelText("账号标识 key"), { target: { value: "qa02" } })
    fireEvent.change(screen.getByLabelText("标签"), { target: { value: "QA 02" } })
    fireEvent.change(screen.getByLabelText("用户名"), { target: { value: "qa02" } })
    fireEvent.click(screen.getByRole("button", { name: "保存账号" }))

    await waitFor(() =>
      expect(mocks.createAccount).toHaveBeenCalledWith({
        key: "qa02",
        label: "QA 02",
        username: "qa02",
        kind: "human",
        notes: "",
      }),
    )
    expect(await screen.findByRole("status")).toBeTruthy()
    await waitFor(() => expect(mocks.listAccesses).toHaveBeenCalledWith(7, { limit: 100 }))
  })

  it("edits an account without resubmitting its immutable key", async () => {
    mocks.updateAccount.mockResolvedValue({ ...QA_ACCOUNT, label: "QA 01 new" })

    render(<AccountsPanel canWrite />)
    await screen.findByText("QA 01")

    fireEvent.click(screen.getByRole("button", { name: "编辑账号 QA 01" }))
    const keyInput = screen.getByLabelText("账号标识 key") as HTMLInputElement
    expect(keyInput.disabled).toBe(true)
    expect(keyInput.value).toBe("qa01")

    fireEvent.change(screen.getByLabelText("标签"), { target: { value: "QA 01 new" } })
    fireEvent.click(screen.getByRole("button", { name: "保存账号" }))

    await waitFor(() =>
      expect(mocks.updateAccount).toHaveBeenCalledWith(3, {
        label: "QA 01 new",
        username: "qa01",
        kind: "human",
        notes: "",
      }),
    )
  })

  it("edits an existing access without resubmitting its stored credential", async () => {
    mocks.updateAccess.mockResolvedValue({ ...ACCESSES[0], concurrencyLimit: 4 })

    render(<AccountsPanel canWrite />)
    await screen.findByText("QA 01")
    fireEvent.click(screen.getByRole("button", { name: "QA 01" }))
    await waitFor(() => expect(mocks.listAccesses).toHaveBeenCalledWith(3, { limit: 100 }))

    fireEvent.click(screen.getByRole("button", { name: "编辑 Access 8" }))
    expect((screen.getByLabelText("会话 TTL（秒）") as HTMLInputElement).value).toBe("")
    fireEvent.change(screen.getByLabelText("并发上限"), { target: { value: "4" } })
    fireEvent.click(screen.getByRole("button", { name: "保存" }))

    await waitFor(() =>
      expect(mocks.updateAccess).toHaveBeenCalledWith(8, {
        systemId: 1,
        authType: "token",
        usernameOverride: "",
        loginUrl: "",
        enabled: true,
        concurrencyLimit: 4,
        sessionTtlSeconds: null,
        permissionNote: "",
      }),
    )
    expect(await screen.findByRole("status")).toBeTruthy()
  })

  it("creates an access for the selected account through the shared form", async () => {
    mocks.createAccess.mockResolvedValue(makeAccess(10, 3, 1))

    render(<AccountsPanel canWrite />)
    await screen.findByText("QA 01")
    fireEvent.click(screen.getByRole("button", { name: "QA 01" }))
    await waitFor(() => expect(mocks.listAccesses).toHaveBeenCalledWith(3, { limit: 100 }))

    fireEvent.click(screen.getByRole("button", { name: "新建 Access" }))
    fireEvent.change(screen.getByLabelText("并发上限"), { target: { value: "3" } })
    fireEvent.click(screen.getByRole("button", { name: "保存" }))

    await waitFor(() =>
      expect(mocks.createAccess).toHaveBeenCalledWith(3, {
        systemId: 1,
        authType: "password",
        usernameOverride: "",
        loginUrl: "",
        enabled: true,
        concurrencyLimit: 3,
        sessionTtlSeconds: null,
        permissionNote: "",
      }),
    )
    expect(await screen.findByRole("status")).toBeTruthy()
    await waitFor(() => expect(mocks.listAccesses).toHaveBeenCalledTimes(2))
  })

  it("clears the previous account's access rows immediately and keeps them cleared after a failed load", async () => {
    const serviceLoad = deferred<AccessDTO[]>()
    mocks.listAccesses.mockResolvedValueOnce(ACCESSES).mockReturnValueOnce(serviceLoad.promise)

    render(<AccountsPanel canWrite />)
    await screen.findByText("QA 01")

    fireEvent.click(screen.getByRole("button", { name: "QA 01" }))
    await waitFor(() => expect(mocks.listAccesses).toHaveBeenCalledWith(3, { limit: 100 }))
    expect(await screen.findByRole("button", { name: "编辑 Access 8" })).toBeTruthy()
    expect(screen.getByText("Shop")).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: "SVC" }))
    await waitFor(() => expect(mocks.listAccesses).toHaveBeenCalledWith(4, { limit: 100 }))

    // Account B is still loading: account A's rows and their edit targets must
    // already be gone so no click can address A's access id under B.
    expect(screen.queryByRole("button", { name: "编辑 Access 8" })).toBeNull()
    expect(screen.queryByText("Shop")).toBeNull()
    expect(screen.getByText("正在加载 Access…")).toBeTruthy()

    await act(async () => {
      serviceLoad.reject(new Error("access load failed"))
      await serviceLoad.promise.catch(() => undefined)
    })

    expect((await screen.findByRole("alert")).textContent).toContain("access load failed")
    expect(screen.queryByRole("button", { name: "编辑 Access 8" })).toBeNull()
    expect(screen.queryByText("Shop")).toBeNull()
    expect(screen.getByText("暂无 Access")).toBeTruthy()
  })

  it("ignores a late response from the previously selected account", async () => {
    const qaLoad = deferred<AccessDTO[]>()
    mocks.listAccesses
      .mockReturnValueOnce(qaLoad.promise)
      .mockResolvedValueOnce([makeAccess(11, 4, 2)])

    render(<AccountsPanel canWrite />)
    await screen.findByText("QA 01")

    fireEvent.click(screen.getByRole("button", { name: "QA 01" }))
    await waitFor(() => expect(mocks.listAccesses).toHaveBeenCalledWith(3, { limit: 100 }))

    fireEvent.click(screen.getByRole("button", { name: "SVC" }))
    await waitFor(() => expect(mocks.listAccesses).toHaveBeenCalledWith(4, { limit: 100 }))
    expect(await screen.findByRole("button", { name: "编辑 Access 11" })).toBeTruthy()

    await act(async () => {
      qaLoad.resolve(ACCESSES)
      await qaLoad.promise
    })

    expect(screen.queryByRole("button", { name: "编辑 Access 8" })).toBeNull()
    expect(screen.getByRole("button", { name: "编辑 Access 11" })).toBeTruthy()
  })

  it("deletes an account only after the inline confirmation", async () => {
    mocks.deleteAccount.mockResolvedValue(undefined)

    render(<AccountsPanel canWrite />)
    await screen.findByText("QA 01")

    fireEvent.click(screen.getByRole("button", { name: "删除账号 QA 01" }))
    expect(mocks.deleteAccount).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole("button", { name: "确认删除账号 QA 01" }))

    await waitFor(() => expect(mocks.deleteAccount).toHaveBeenCalledWith(3))
    const status = await screen.findByRole("status")
    expect(status.textContent).toContain("已删除账号 QA 01。")
    await waitFor(() => expect(screen.queryByText("QA 01")).toBeNull())
  })

  it("shows the has_dependencies reason when an account delete is refused", async () => {
    mocks.deleteAccount.mockRejectedValue(
      new ApiError({
        status: 409,
        code: "conflict",
        message: "account still has accesses",
        details: { reason: "has_dependencies" },
      }),
    )

    render(<AccountsPanel canWrite />)
    await screen.findByText("QA 01")

    fireEvent.click(screen.getByRole("button", { name: "删除账号 QA 01" }))
    fireEvent.click(screen.getByRole("button", { name: "确认删除账号 QA 01" }))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("has_dependencies")
    expect(screen.getByText("QA 01")).toBeTruthy()
  })

  it("deletes an access only after the inline confirmation", async () => {
    mocks.deleteAccess.mockResolvedValue(undefined)

    render(<AccountsPanel canWrite />)
    await screen.findByText("QA 01")
    fireEvent.click(screen.getByRole("button", { name: "QA 01" }))
    await waitFor(() => expect(mocks.listAccesses).toHaveBeenCalledWith(3, { limit: 100 }))

    fireEvent.click(screen.getByRole("button", { name: "删除 Access 8" }))
    expect(mocks.deleteAccess).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole("button", { name: "确认删除 Access 8" }))

    await waitFor(() => expect(mocks.deleteAccess).toHaveBeenCalledWith(8))
    const status = await screen.findByRole("status")
    expect(status.textContent).toContain("已删除 Access #8。")
    await waitFor(() => expect(screen.queryByRole("button", { name: "编辑 Access 8" })).toBeNull())
    expect(screen.getByRole("button", { name: "编辑 Access 9" })).toBeTruthy()
  })

  it("shows the has_dependencies reason when an access delete is refused", async () => {
    mocks.deleteAccess.mockRejectedValue(
      new ApiError({
        status: 409,
        code: "conflict",
        message: "access still has sessions",
        details: { reason: "has_dependencies" },
      }),
    )

    render(<AccountsPanel canWrite />)
    await screen.findByText("QA 01")
    fireEvent.click(screen.getByRole("button", { name: "QA 01" }))
    await waitFor(() => expect(mocks.listAccesses).toHaveBeenCalledWith(3, { limit: 100 }))

    fireEvent.click(screen.getByRole("button", { name: "删除 Access 8" }))
    fireEvent.click(screen.getByRole("button", { name: "确认删除 Access 8" }))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("has_dependencies")
    expect(screen.getByRole("button", { name: "编辑 Access 8" })).toBeTruthy()
  })
})
