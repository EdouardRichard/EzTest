import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const mocks = vi.hoisted(() => ({
  getDbTarget: vi.fn(),
  upsertDbTarget: vi.fn(),
  deleteDbTarget: vi.fn(),
  testDbTarget: vi.fn(),
}))

vi.mock("./api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./api")>()
  return {
    ...actual,
    getDbTarget: mocks.getDbTarget,
    upsertDbTarget: mocks.upsertDbTarget,
    deleteDbTarget: mocks.deleteDbTarget,
    testDbTarget: mocks.testDbTarget,
  }
})

import { ApiError } from "@/lib/api-client"

import { DBTargetSection } from "./components/DBTargetSection"
import type { DBTargetDTO, DBTargetTestResultDTO } from "./types"

/** Snowflake ids stay decimal strings (D29); the DTO never carries a password. */
const TARGET: DBTargetDTO = {
  id: "1844674407370955162",
  environmentId: "7",
  name: "orders",
  host: "127.0.0.1",
  port: 5432,
  database: "eztest_target",
  username: "eztest_ro",
  credentialSet: true,
  sslmode: "disable",
  connectTimeoutMs: 3000,
  statementTimeoutMs: 5000,
  maxRows: 100,
  enabled: true,
  lastTestedAt: null,
  lastTestStatus: "",
  lastTestMessage: "",
  createdAt: "2026-10-28T00:00:00Z",
  updatedAt: "2026-10-28T00:00:00Z",
}

const TEST_OK: DBTargetTestResultDTO = {
  ok: true,
  latencyMs: 12,
  serverVersion: "PostgreSQL 16.4",
  currentUser: "eztest_ro",
  transactionReadOnly: true,
  warnings: [],
}

function renderSection(canWrite = true) {
  return render(<DBTargetSection environmentId={7} environmentName="Staging" canWrite={canWrite} />)
}

describe("DBTargetSection", () => {
  beforeEach(() => {
    mocks.getDbTarget.mockReset()
    mocks.upsertDbTarget.mockReset()
    mocks.deleteDbTarget.mockReset()
    mocks.testDbTarget.mockReset()
    mocks.getDbTarget.mockResolvedValue(TARGET)
  })

  afterEach(() => {
    cleanup()
  })

  it("renders nothing and never fetches for ordinary users", async () => {
    renderSection(false)

    expect(screen.queryByTestId("db-target-form")).toBeNull()
    expect(screen.queryByTestId("db-target-test-btn")).toBeNull()
    await waitFor(() => expect(mocks.getDbTarget).not.toHaveBeenCalled())
  })

  it("loads the stored target for admins without echoing the password", async () => {
    renderSection()

    expect(await screen.findByDisplayValue("127.0.0.1")).toBeTruthy()
    expect(screen.getByTestId("db-target-form")).toBeTruthy()

    const password = screen.getByLabelText("密码") as HTMLInputElement
    expect(password.type).toBe("password")
    expect(password.value).toBe("")
    expect(screen.getByTestId("db-target-password-state").textContent).toContain("已设置")
    expect(screen.getByLabelText("数据库")).toBeTruthy()
    expect((screen.getByLabelText("端口") as HTMLInputElement).value).toBe("5432")
    expect((screen.getByLabelText("最大行数") as HTMLInputElement).value).toBe("100")
  })

  it("omits a blank password when saving and keeps the credential placeholder", async () => {
    mocks.upsertDbTarget.mockResolvedValue({ ...TARGET, host: "db.internal" })

    renderSection()
    // Wait for the load effect to seed the form before editing it.
    await screen.findByDisplayValue("127.0.0.1")
    fireEvent.change(screen.getByLabelText("主机"), { target: { value: "db.internal" } })
    fireEvent.click(screen.getByRole("button", { name: "保存目标数据库" }))

    await waitFor(() => expect(mocks.upsertDbTarget).toHaveBeenCalledTimes(1))
    const [environmentId, payload] = mocks.upsertDbTarget.mock.calls[0] as [
      number,
      Record<string, unknown>,
    ]
    expect(environmentId).toBe(7)
    expect(payload).toMatchObject({
      name: "orders",
      host: "db.internal",
      port: 5432,
      database: "eztest_target",
      username: "eztest_ro",
      sslmode: "disable",
      connectTimeoutMs: 3000,
      statementTimeoutMs: 5000,
      maxRows: 100,
      enabled: true,
    })
    expect(payload).not.toHaveProperty("password")
    expect((screen.getByLabelText("密码") as HTMLInputElement).value).toBe("")
    expect(await screen.findByRole("status")).toBeTruthy()
  })

  it("sends a new password when typed and clears it after a successful save", async () => {
    mocks.upsertDbTarget.mockResolvedValue(TARGET)

    renderSection()
    // Wait for the load effect to seed the form before editing it.
    await screen.findByDisplayValue("127.0.0.1")
    fireEvent.change(screen.getByLabelText("密码"), { target: { value: "n3w-s3cret" } })
    fireEvent.click(screen.getByRole("button", { name: "保存目标数据库" }))

    await waitFor(() =>
      expect(mocks.upsertDbTarget).toHaveBeenCalledWith(
        7,
        expect.objectContaining({ password: "n3w-s3cret" }),
      ),
    )
    await waitFor(() => expect((screen.getByLabelText("密码") as HTMLInputElement).value).toBe(""))
    expect(screen.getByTestId("db-target-password-state").textContent).toContain("已设置")
  })

  it("creates the first target with the spec defaults and a password", async () => {
    mocks.getDbTarget.mockResolvedValue(null)
    mocks.upsertDbTarget.mockResolvedValue({ ...TARGET, id: "1844674407370955163" })

    renderSection()
    expect(await screen.findByText("尚未配置目标数据库")).toBeTruthy()
    expect(screen.getByTestId("db-target-password-state").textContent).toContain("未设置")
    // The blank form is seeded by the load effect after the first paint; flush it
    // before typing so the keystrokes below are not overwritten by that sync.
    await act(async () => {})

    fireEvent.change(screen.getByLabelText("主机"), { target: { value: "127.0.0.1" } })
    fireEvent.change(screen.getByLabelText("数据库"), { target: { value: "eztest_target" } })
    fireEvent.change(screen.getByLabelText("用户名"), { target: { value: "eztest_ro" } })
    fireEvent.change(screen.getByLabelText("密码"), { target: { value: "s3cret" } })
    fireEvent.click(screen.getByRole("button", { name: "保存目标数据库" }))

    await waitFor(() =>
      expect(mocks.upsertDbTarget).toHaveBeenCalledWith(
        7,
        expect.objectContaining({
          name: "",
          port: 5432,
          sslmode: "disable",
          connectTimeoutMs: 3000,
          statementTimeoutMs: 5000,
          maxRows: 100,
          enabled: true,
          password: "s3cret",
        }),
      ),
    )
  })

  it("tests the unsaved form values and shows latency and read-only state", async () => {
    mocks.testDbTarget.mockResolvedValue(TEST_OK)

    renderSection()
    await screen.findByDisplayValue("127.0.0.1")
    fireEvent.change(screen.getByLabelText("主机"), { target: { value: "db.internal" } })
    fireEvent.click(screen.getByTestId("db-target-test-btn"))

    await waitFor(() => expect(mocks.testDbTarget).toHaveBeenCalledTimes(1))
    const [environmentId, payload] = mocks.testDbTarget.mock.calls[0] as [
      number,
      Record<string, unknown>,
    ]
    expect(environmentId).toBe(7)
    expect(payload).toMatchObject({ host: "db.internal", port: 5432, database: "eztest_target" })
    expect(payload).not.toHaveProperty("password")

    const result = await screen.findByTestId("db-target-result")
    expect(result.textContent).toContain("连接成功")
    expect(result.textContent).toContain("12")
    expect(result.textContent).toContain("PostgreSQL 16.4")
    expect(result.textContent).toContain("只读")
    expect(screen.queryByTestId("db-target-readonly-warning")).toBeNull()
    // The override was never persisted: the stored "recent test" stays untouched.
    expect(screen.getByText(/最近测试：从未测试/)).toBeTruthy()
  })

  it("probes the stored target without an override body when the form is unchanged", async () => {
    mocks.testDbTarget.mockResolvedValue(TEST_OK)

    renderSection()
    await screen.findByDisplayValue("127.0.0.1")
    fireEvent.click(screen.getByTestId("db-target-test-btn"))

    await waitFor(() => expect(mocks.testDbTarget).toHaveBeenCalledTimes(1))
    expect(mocks.testDbTarget.mock.calls[0]).toEqual([7])
    expect(await screen.findByTestId("db-target-result")).toBeTruthy()
    // A stored-config probe persists last_tested, so the summary refreshes locally.
    expect(await screen.findByText(/最近测试：通过/)).toBeTruthy()
  })

  it("sends an override when only the password field changed", async () => {
    mocks.testDbTarget.mockResolvedValue(TEST_OK)

    renderSection()
    await screen.findByDisplayValue("127.0.0.1")
    fireEvent.change(screen.getByLabelText("密码"), { target: { value: "n3w-s3cret" } })
    fireEvent.click(screen.getByTestId("db-target-test-btn"))

    await waitFor(() =>
      expect(mocks.testDbTarget).toHaveBeenCalledWith(
        7,
        expect.objectContaining({ password: "n3w-s3cret" }),
      ),
    )
  })

  it("clears the previous test result after the form changes", async () => {
    mocks.testDbTarget.mockResolvedValue(TEST_OK)

    renderSection()
    await screen.findByDisplayValue("127.0.0.1")
    fireEvent.click(screen.getByTestId("db-target-test-btn"))
    expect(await screen.findByTestId("db-target-result")).toBeTruthy()

    fireEvent.change(screen.getByLabelText("主机"), { target: { value: "db.internal" } })

    await waitFor(() => expect(screen.queryByTestId("db-target-result")).toBeNull())
  })

  it("shows no form and only a retry action when the initial GET fails", async () => {
    mocks.getDbTarget.mockRejectedValue(
      new ApiError({ status: 500, code: "internal", message: "db target read failed" }),
    )

    renderSection()

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("db target read failed")
    expect(screen.queryByTestId("db-target-form")).toBeNull()
    expect(screen.queryByText("尚未配置目标数据库")).toBeNull()
    expect(screen.queryByTestId("db-target-password-state")).toBeNull()

    mocks.getDbTarget.mockResolvedValue(TARGET)
    fireEvent.click(screen.getByTestId("db-target-retry"))

    expect(await screen.findByDisplayValue("127.0.0.1")).toBeTruthy()
    expect(screen.getByTestId("db-target-form")).toBeTruthy()
    await waitFor(() => expect(mocks.getDbTarget).toHaveBeenCalledTimes(2))
  })

  it("keeps the form populated and reports the error when a delete fails", async () => {
    mocks.deleteDbTarget.mockRejectedValue(
      new ApiError({
        status: 409,
        code: "conflict",
        message: "db target is already gone",
        details: { reason: "db_target_missing" },
      }),
    )

    renderSection()
    await screen.findByDisplayValue("127.0.0.1")
    fireEvent.click(screen.getByRole("button", { name: "删除目标数据库" }))
    fireEvent.click(screen.getByRole("button", { name: "确认删除目标数据库" }))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("尚未配置可用")
    expect((screen.getByLabelText("主机") as HTMLInputElement).value).toBe("127.0.0.1")
    expect(screen.queryByText("尚未配置目标数据库")).toBeNull()
  })

  it("shows an alert when the test endpoint itself fails", async () => {
    mocks.testDbTarget.mockRejectedValue(
      new ApiError({ status: 500, code: "internal", message: "probe exploded" }),
    )

    renderSection()
    await screen.findByDisplayValue("127.0.0.1")
    fireEvent.click(screen.getByTestId("db-target-test-btn"))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("probe exploded")
    expect(screen.queryByTestId("db-target-result")).toBeNull()
  })

  it("requires a password when the stored target has no credential", async () => {
    mocks.getDbTarget.mockResolvedValue({ ...TARGET, credentialSet: false })

    renderSection()
    const password = (await screen.findByLabelText("密码")) as HTMLInputElement
    expect(password.placeholder).toContain("必填")

    // The form mounts empty and is filled by the load effect; the password input
    // exists before that effect lands (more likely under full-suite load). Wait
    // for the stored host so save validates the credential, not a blank host.
    await waitFor(() =>
      expect((screen.getByLabelText("主机") as HTMLInputElement).value).toBe(TARGET.host),
    )

    fireEvent.click(screen.getByRole("button", { name: "保存目标数据库" }))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("密码")
    expect(mocks.upsertDbTarget).not.toHaveBeenCalled()
  })

  it.each([
    ["the probe role can write", { ...TEST_OK, warnings: ["role_has_write_privileges"] }, "写权限"],
    ["the transaction is writable", { ...TEST_OK, transactionReadOnly: false }, "只读事务"],
  ])("warns when %s", async (_name, result, expected) => {
    mocks.testDbTarget.mockResolvedValue(result)

    renderSection()
    await screen.findByDisplayValue("127.0.0.1")
    fireEvent.click(screen.getByTestId("db-target-test-btn"))

    const warning = await screen.findByTestId("db-target-readonly-warning")
    expect(warning.textContent).toContain(expected)
  })

  it("shows a classified failure reason and message", async () => {
    mocks.testDbTarget.mockResolvedValue({
      ok: false,
      latencyMs: 4,
      transactionReadOnly: false,
      warnings: [],
      error: { reason: "db_connection_failed", message: "connection refused" },
    } satisfies DBTargetTestResultDTO)

    renderSection()
    await screen.findByDisplayValue("127.0.0.1")
    fireEvent.click(screen.getByTestId("db-target-test-btn"))

    const result = await screen.findByTestId("db-target-result")
    expect(result.textContent).toContain("连接失败")
    expect(result.textContent).toContain("db_connection_failed")
    expect(result.textContent).toContain("connection refused")
  })

  it("shows the admin-only message when saving is forbidden", async () => {
    mocks.upsertDbTarget.mockRejectedValue(
      new ApiError({ status: 403, code: "forbidden", message: "admin only" }),
    )

    renderSection()
    await screen.findByDisplayValue("127.0.0.1")
    fireEvent.click(screen.getByRole("button", { name: "保存目标数据库" }))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("管理员")
    expect((screen.getByLabelText("密码") as HTMLInputElement).value).toBe("")
  })

  it("shows the invalid field of a 422 db_target_invalid response", async () => {
    mocks.upsertDbTarget.mockRejectedValue(
      new ApiError({
        status: 422,
        code: "invalid_argument",
        message: "port must be between 1 and 65535",
        details: { reason: "db_target_invalid", field: "port" },
      }),
    )

    renderSection()
    await screen.findByDisplayValue("127.0.0.1")
    fireEvent.click(screen.getByRole("button", { name: "保存目标数据库" }))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("port")
    expect(alert.textContent).toContain("不合法")
  })

  it("deletes the target only after the inline confirmation", async () => {
    mocks.deleteDbTarget.mockResolvedValue(undefined)

    renderSection()
    await screen.findByDisplayValue("127.0.0.1")
    fireEvent.click(screen.getByRole("button", { name: "删除目标数据库" }))
    expect(mocks.deleteDbTarget).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole("button", { name: "确认删除目标数据库" }))

    await waitFor(() => expect(mocks.deleteDbTarget).toHaveBeenCalledWith(7))
    expect(await screen.findByText("尚未配置目标数据库")).toBeTruthy()
    expect(screen.getByTestId("db-target-password-state").textContent).toContain("未设置")
  })
})
