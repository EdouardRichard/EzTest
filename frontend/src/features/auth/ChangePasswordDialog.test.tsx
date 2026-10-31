import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

import { ChangePasswordDialog } from "./ChangePasswordDialog"

const auth = vi.hoisted(() => ({
  user: null,
  token: "ezt_token",
  loading: false,
  login: vi.fn(),
  logout: vi.fn(),
  changePassword: vi.fn(),
}))
vi.mock("./AuthProvider", () => ({ useAuth: () => auth }))

describe("ChangePasswordDialog", () => {
  const onClose = vi.fn()

  beforeEach(() => {
    auth.changePassword.mockReset()
    auth.changePassword.mockResolvedValue(undefined)
    onClose.mockReset()
  })

  afterEach(cleanup)

  async function fill(oldPassword: string, newPassword: string, confirm: string): Promise<void> {
    fireEvent.change(await screen.findByLabelText("旧密码"), { target: { value: oldPassword } })
    fireEvent.change(screen.getByLabelText("新密码"), { target: { value: newPassword } })
    fireEvent.change(screen.getByLabelText("确认新密码"), { target: { value: confirm } })
  }

  it("validates password length, mismatch and reuse before calling the API", async () => {
    render(<ChangePasswordDialog open onClose={onClose} />)

    fireEvent.click(await screen.findByRole("button", { name: "保存新密码" }))
    expect(await screen.findByRole("alert")).toHaveTextContent("请填写完整的密码信息")

    await fill("admin123", "short", "short")
    fireEvent.click(screen.getByRole("button", { name: "保存新密码" }))
    expect(await screen.findByRole("alert")).toHaveTextContent("新密码至少 8 个字符")

    await fill("admin123", "newpass123", "different123")
    fireEvent.click(screen.getByRole("button", { name: "保存新密码" }))
    expect(await screen.findByRole("alert")).toHaveTextContent("两次输入的新密码不一致")

    await fill("admin123", "admin123", "admin123")
    fireEvent.click(screen.getByRole("button", { name: "保存新密码" }))
    expect(await screen.findByRole("alert")).toHaveTextContent("新密码不能与旧密码相同")

    expect(auth.changePassword).not.toHaveBeenCalled()
  })

  it("submits the old and new password and confirms success", async () => {
    render(<ChangePasswordDialog open onClose={onClose} />)
    await fill("admin123", "newpass123", "newpass123")

    fireEvent.click(screen.getByRole("button", { name: "保存新密码" }))

    await waitFor(() => expect(auth.changePassword).toHaveBeenCalledWith("admin123", "newpass123"))
    expect(await screen.findByRole("status")).toHaveTextContent("密码已修改")
  })

  it("shows the API error and keeps the dialog open", async () => {
    auth.changePassword.mockRejectedValue(
      new ApiError({
        status: 401,
        code: "unauthenticated",
        message: "旧密码不正确",
        requestId: "req_pw",
      }),
    )
    render(<ChangePasswordDialog open onClose={onClose} />)
    await fill("wrongpass", "newpass123", "newpass123")

    fireEvent.click(screen.getByRole("button", { name: "保存新密码" }))

    expect(await screen.findByRole("alert")).toHaveTextContent("旧密码不正确")
    expect(onClose).not.toHaveBeenCalled()
  })

  it("renders nothing when closed", () => {
    render(<ChangePasswordDialog open={false} onClose={onClose} />)
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
  })
})
