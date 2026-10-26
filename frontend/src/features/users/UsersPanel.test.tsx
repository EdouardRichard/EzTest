import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const mocks = vi.hoisted(() => ({
  apiGet: vi.fn(),
  apiPost: vi.fn(),
  apiPatch: vi.fn(),
}))

vi.mock("@/lib/api-client", () => {
  class ApiError extends Error {
    readonly status: number
    constructor(status: number, message: string) {
      super(message)
      this.name = "ApiError"
      this.status = status
    }
  }
  return {
    ApiError,
    apiGet: mocks.apiGet,
    apiPost: mocks.apiPost,
    apiPatch: mocks.apiPatch,
  }
})

vi.mock("@/features/auth/AuthProvider", () => ({
  useAuth: () => ({
    user: { id: 1, username: "admin", role: "admin", status: "active" },
    loading: false,
    login: vi.fn(),
    logout: vi.fn(),
  }),
}))

import { UsersPanel } from "./UsersPanel"

describe("UsersPanel", () => {
  beforeEach(() => {
    mocks.apiGet.mockReset()
    mocks.apiPost.mockReset()
    mocks.apiPatch.mockReset()
    mocks.apiGet.mockResolvedValue([
      {
        id: 1,
        username: "admin",
        role: "admin",
        status: "active",
        password_hash: "$2a$12$hash-secret",
      },
      { id: 2, username: "qa01", role: "user", status: "active" },
    ])
    mocks.apiPost.mockResolvedValue({
      id: 3,
      username: "qa02",
      role: "user",
      status: "active",
    })
    mocks.apiPatch.mockResolvedValue({
      id: 2,
      username: "qa01",
      role: "user",
      status: "disabled",
    })
  })

  afterEach(() => {
    cleanup()
  })

  it("lists users without rendering password material and creates a user", async () => {
    render(<UsersPanel />)

    await waitFor(() => expect(screen.getByText("qa01")).toBeTruthy())
    expect(screen.getByText("admin")).toBeTruthy()
    // Even if a hostile/buggy payload carried password material, the panel
    // renders only the public id/username/role/status projection.
    expect(document.body.textContent).not.toContain("hash-secret")
    expect(document.body.textContent).not.toContain("password_hash")

    fireEvent.change(screen.getByLabelText("用户名"), { target: { value: "qa02" } })
    fireEvent.change(screen.getByLabelText("初始密码"), {
      target: { value: "correct horse battery" },
    })
    fireEvent.click(screen.getByRole("button", { name: "创建用户" }))

    await waitFor(() =>
      expect(mocks.apiPost).toHaveBeenCalledWith("/api/users", {
        username: "qa02",
        password: "correct horse battery",
      }),
    )
    await waitFor(() => expect(screen.getByText("qa02")).toBeTruthy())
  })
})
