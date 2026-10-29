import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const mocks = vi.hoisted(() => ({
  listEnvironments: vi.fn(),
  createEnvironment: vi.fn(),
  updateEnvironment: vi.fn(),
  deleteEnvironment: vi.fn(),
  getDbTarget: vi.fn(),
  upsertDbTarget: vi.fn(),
  deleteDbTarget: vi.fn(),
  testDbTarget: vi.fn(),
}))

vi.mock("./api", () => ({
  listEnvironments: mocks.listEnvironments,
  createEnvironment: mocks.createEnvironment,
  updateEnvironment: mocks.updateEnvironment,
  deleteEnvironment: mocks.deleteEnvironment,
  getDbTarget: mocks.getDbTarget,
  upsertDbTarget: mocks.upsertDbTarget,
  deleteDbTarget: mocks.deleteDbTarget,
  testDbTarget: mocks.testDbTarget,
  describeDBTargetError: (error: unknown) => (error instanceof Error ? error.message : "未知错误"),
}))

import { ApiError } from "@/lib/api-client"

import { EnvironmentsPanel } from "./EnvironmentsPanel"
import type { EnvironmentDTO } from "./types"

function makeEnvironment(
  id: number,
  systemId: number,
  key: string,
  name: string,
  overrides: Partial<EnvironmentDTO> = {},
): EnvironmentDTO {
  return {
    id,
    systemId,
    key,
    name,
    baseUrl: "http://localhost:13335",
    kind: "staging",
    isProd: false,
    allowedHosts: [],
    loginCheck: null,
    allowAutoLogin: false,
    requireApproval: false,
    ...overrides,
  }
}

const STAGING = makeEnvironment(1, 1, "staging", "Staging")

describe("EnvironmentsPanel", () => {
  beforeEach(() => {
    mocks.listEnvironments.mockReset()
    mocks.createEnvironment.mockReset()
    mocks.updateEnvironment.mockReset()
    mocks.deleteEnvironment.mockReset()
    mocks.getDbTarget.mockReset()
    mocks.upsertDbTarget.mockReset()
    mocks.deleteDbTarget.mockReset()
    mocks.testDbTarget.mockReset()
    mocks.listEnvironments.mockResolvedValue([STAGING])
    mocks.getDbTarget.mockResolvedValue(null)
  })

  afterEach(() => {
    cleanup()
  })

  it("lists the system's environments and hides write controls from readonly users", async () => {
    render(<EnvironmentsPanel systemId={1} canWrite={false} />)

    expect(await screen.findByText("Staging")).toBeTruthy()
    expect(screen.queryByRole("button", { name: "新建环境" })).toBeNull()
    expect(screen.queryByRole("button", { name: "编辑 Staging" })).toBeNull()
    expect(screen.queryByRole("button", { name: "删除环境 Staging" })).toBeNull()
    await waitFor(() => expect(mocks.listEnvironments).toHaveBeenCalledWith(1))
  })

  it("renders login_check fields and submits them with the environment", async () => {
    const created = makeEnvironment(3, 1, "local", "Local", {
      baseUrl: "http://127.0.0.1:13335",
      allowedHosts: ["127.0.0.1", "localhost"],
      loginCheck: {
        url: "http://127.0.0.1:13335/account",
        expectStatus: [200, 302],
        bodyContains: "Fixture Account",
      },
    })
    mocks.createEnvironment.mockResolvedValue(created)

    render(<EnvironmentsPanel systemId={1} canWrite />)
    await screen.findByText("Staging")

    fireEvent.click(screen.getByRole("button", { name: "新建环境" }))
    fireEvent.change(screen.getByLabelText("环境标识 key"), { target: { value: "local" } })
    fireEvent.change(screen.getByLabelText("名称"), { target: { value: "Local" } })
    fireEvent.change(screen.getByLabelText("Base URL"), {
      target: { value: "http://127.0.0.1:13335" },
    })
    fireEvent.change(screen.getByLabelText("允许主机（逗号分隔）"), {
      target: { value: "127.0.0.1, localhost" },
    })
    fireEvent.change(screen.getByLabelText("登录检查 URL"), {
      target: { value: "http://127.0.0.1:13335/account" },
    })
    fireEvent.change(screen.getByLabelText("期望状态码（逗号分隔）"), {
      target: { value: "200, 302" },
    })
    fireEvent.change(screen.getByLabelText("响应体包含"), {
      target: { value: "Fixture Account" },
    })
    fireEvent.click(screen.getByRole("button", { name: "保存环境" }))

    await waitFor(() =>
      expect(mocks.createEnvironment).toHaveBeenCalledWith(1, {
        key: "local",
        name: "Local",
        kind: "staging",
        baseUrl: "http://127.0.0.1:13335",
        isProd: false,
        allowedHosts: ["127.0.0.1", "localhost"],
        loginCheck: {
          url: "http://127.0.0.1:13335/account",
          expectStatus: [200, 302],
          bodyContains: "Fixture Account",
        },
        allowAutoLogin: false,
        requireApproval: false,
      }),
    )
    expect(await screen.findByRole("status")).toBeTruthy()
    await waitFor(() => expect(mocks.listEnvironments).toHaveBeenCalledTimes(2))
  })

  it("submits a null login_check when no login URL is configured", async () => {
    mocks.createEnvironment.mockResolvedValue(makeEnvironment(4, 1, "dev", "Dev"))

    render(<EnvironmentsPanel systemId={1} canWrite />)
    await screen.findByText("Staging")

    fireEvent.click(screen.getByRole("button", { name: "新建环境" }))
    fireEvent.change(screen.getByLabelText("环境标识 key"), { target: { value: "dev" } })
    fireEvent.change(screen.getByLabelText("名称"), { target: { value: "Dev" } })
    fireEvent.change(screen.getByLabelText("Base URL"), {
      target: { value: "http://localhost:13338" },
    })
    fireEvent.click(screen.getByRole("button", { name: "保存环境" }))

    await waitFor(() =>
      expect(mocks.createEnvironment).toHaveBeenCalledWith(
        1,
        expect.objectContaining({ loginCheck: null }),
      ),
    )
  })

  it("pre-fills login_check fields when editing an environment", async () => {
    const configured = makeEnvironment(1, 1, "staging", "Staging", {
      loginCheck: {
        url: "http://localhost:13335/account",
        expectStatus: [200, 302],
        bodyContains: "Fixture Account",
      },
    })
    mocks.listEnvironments.mockResolvedValue([configured])
    mocks.updateEnvironment.mockResolvedValue(configured)

    render(<EnvironmentsPanel systemId={1} canWrite />)
    await screen.findByText("Staging")

    fireEvent.click(screen.getByRole("button", { name: "编辑 Staging" }))
    const urlInput = screen.getByLabelText("登录检查 URL") as HTMLInputElement
    const statusInput = screen.getByLabelText("期望状态码（逗号分隔）") as HTMLInputElement
    expect(urlInput.value).toBe("http://localhost:13335/account")
    expect(statusInput.value).toBe("200, 302")

    fireEvent.change(screen.getByLabelText("响应体包含"), { target: { value: "Welcome" } })
    fireEvent.click(screen.getByRole("button", { name: "保存环境" }))

    await waitFor(() =>
      expect(mocks.updateEnvironment).toHaveBeenCalledWith(1, {
        name: "Staging",
        kind: "staging",
        baseUrl: "http://localhost:13335",
        isProd: false,
        allowedHosts: [],
        loginCheck: {
          url: "http://localhost:13335/account",
          expectStatus: [200, 302],
          bodyContains: "Welcome",
        },
        allowAutoLogin: false,
        requireApproval: false,
      }),
    )
  })

  it("reloads the list when the selected system changes", async () => {
    const { rerender } = render(<EnvironmentsPanel systemId={1} canWrite={false} />)
    await waitFor(() => expect(mocks.listEnvironments).toHaveBeenCalledWith(1))

    rerender(<EnvironmentsPanel systemId={2} canWrite={false} />)

    await waitFor(() => expect(mocks.listEnvironments).toHaveBeenCalledWith(2))
  })

  it("accepts a relative login_check path that the backend joins to the base URL", async () => {
    mocks.createEnvironment.mockResolvedValue(makeEnvironment(5, 1, "local", "Local"))

    render(<EnvironmentsPanel systemId={1} canWrite />)
    await screen.findByText("Staging")

    fireEvent.click(screen.getByRole("button", { name: "新建环境" }))
    fireEvent.change(screen.getByLabelText("环境标识 key"), { target: { value: "local" } })
    fireEvent.change(screen.getByLabelText("名称"), { target: { value: "Local" } })
    fireEvent.change(screen.getByLabelText("Base URL"), {
      target: { value: "http://localhost:13335" },
    })
    fireEvent.change(screen.getByLabelText("登录检查 URL"), { target: { value: "/account" } })
    fireEvent.click(screen.getByRole("button", { name: "保存环境" }))

    await waitFor(() =>
      expect(mocks.createEnvironment).toHaveBeenCalledWith(
        1,
        expect.objectContaining({
          loginCheck: { url: "/account", expectStatus: [200], bodyContains: "" },
        }),
      ),
    )
  })

  it("rejects a login_check URL that is neither absolute nor rooted", async () => {
    render(<EnvironmentsPanel systemId={1} canWrite />)
    await screen.findByText("Staging")

    fireEvent.click(screen.getByRole("button", { name: "新建环境" }))
    fireEvent.change(screen.getByLabelText("环境标识 key"), { target: { value: "local" } })
    fireEvent.change(screen.getByLabelText("名称"), { target: { value: "Local" } })
    fireEvent.change(screen.getByLabelText("Base URL"), {
      target: { value: "http://localhost:13335" },
    })
    fireEvent.change(screen.getByLabelText("登录检查 URL"), { target: { value: "account" } })
    fireEvent.click(screen.getByRole("button", { name: "保存环境" }))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("相对路径")
    expect(mocks.createEnvironment).not.toHaveBeenCalled()
  })

  it("rejects a login_check URL longer than 2048 characters", async () => {
    render(<EnvironmentsPanel systemId={1} canWrite />)
    await screen.findByText("Staging")

    fireEvent.click(screen.getByRole("button", { name: "新建环境" }))
    fireEvent.change(screen.getByLabelText("环境标识 key"), { target: { value: "local" } })
    fireEvent.change(screen.getByLabelText("名称"), { target: { value: "Local" } })
    fireEvent.change(screen.getByLabelText("Base URL"), {
      target: { value: "http://localhost:13335" },
    })
    fireEvent.change(screen.getByLabelText("登录检查 URL"), {
      target: { value: `/${"a".repeat(2048)}` },
    })
    fireEvent.click(screen.getByRole("button", { name: "保存环境" }))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("2048")
    expect(mocks.createEnvironment).not.toHaveBeenCalled()
  })

  it("deletes an environment only after the inline confirmation", async () => {
    mocks.deleteEnvironment.mockResolvedValue(undefined)

    render(<EnvironmentsPanel systemId={1} canWrite />)
    await screen.findByText("Staging")

    fireEvent.click(screen.getByRole("button", { name: "删除环境 Staging" }))
    expect(mocks.deleteEnvironment).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole("button", { name: "确认删除环境 Staging" }))

    await waitFor(() => expect(mocks.deleteEnvironment).toHaveBeenCalledWith(1))
    const status = await screen.findByRole("status")
    expect(status.textContent).toContain("已删除环境 Staging。")
    await waitFor(() => expect(screen.queryByText("Staging")).toBeNull())
  })

  it("shows the has_dependencies reason when an environment delete is refused", async () => {
    mocks.deleteEnvironment.mockRejectedValue(
      new ApiError({
        status: 409,
        code: "conflict",
        message: "environment still has sessions",
        details: { reason: "has_dependencies" },
      }),
    )

    render(<EnvironmentsPanel systemId={1} canWrite />)
    await screen.findByText("Staging")

    fireEvent.click(screen.getByRole("button", { name: "删除环境 Staging" }))
    fireEvent.click(screen.getByRole("button", { name: "确认删除环境 Staging" }))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("has_dependencies")
    expect(screen.getByText("Staging")).toBeTruthy()
  })

  it("opens the admin-only DB target section for an environment", async () => {
    render(<EnvironmentsPanel systemId={1} canWrite />)
    await screen.findByText("Staging")

    expect(screen.queryByTestId("db-target-form")).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "目标数据库 Staging" }))

    expect(await screen.findByTestId("db-target-form")).toBeTruthy()
    await waitFor(() => expect(mocks.getDbTarget).toHaveBeenCalledWith(1))
    expect(
      screen.getByText("Staging 的只读 PostgreSQL 目标；仅管理员可维护，密码只写不回显。"),
    ).toBeTruthy()
  })

  it("hides the DB target entry point from readonly users", async () => {
    render(<EnvironmentsPanel systemId={1} canWrite={false} />)
    await screen.findByText("Staging")

    expect(screen.queryByRole("button", { name: "目标数据库 Staging" })).toBeNull()
    expect(screen.queryByTestId("db-target-form")).toBeNull()
    expect(mocks.getDbTarget).not.toHaveBeenCalled()
  })

  it("closes the DB target section when the selected system changes", async () => {
    mocks.listEnvironments.mockImplementation(async (systemId: number) =>
      systemId === 1 ? [STAGING] : [makeEnvironment(9, 2, "other", "Other")],
    )

    const { rerender } = render(<EnvironmentsPanel systemId={1} canWrite />)
    await screen.findByText("Staging")
    fireEvent.click(screen.getByRole("button", { name: "目标数据库 Staging" }))
    await screen.findByTestId("db-target-form")

    rerender(<EnvironmentsPanel systemId={2} canWrite />)

    expect(await screen.findByText("Other")).toBeTruthy()
    await waitFor(() => expect(screen.queryByTestId("db-target-section")).toBeNull())
  })
})
