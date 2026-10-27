import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const mocks = vi.hoisted(() => ({
  listEnvironments: vi.fn(),
  createEnvironment: vi.fn(),
  updateEnvironment: vi.fn(),
}))

vi.mock("./api", () => ({
  listEnvironments: mocks.listEnvironments,
  createEnvironment: mocks.createEnvironment,
  updateEnvironment: mocks.updateEnvironment,
}))

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
    mocks.listEnvironments.mockResolvedValue([STAGING])
  })

  afterEach(() => {
    cleanup()
  })

  it("lists the system's environments and hides write controls from readonly users", async () => {
    render(<EnvironmentsPanel systemId={1} canWrite={false} />)

    expect(await screen.findByText("Staging")).toBeTruthy()
    expect(screen.queryByRole("button", { name: "新建环境" })).toBeNull()
    expect(screen.queryByRole("button", { name: "编辑 Staging" })).toBeNull()
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
})
