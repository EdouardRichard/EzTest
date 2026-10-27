import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { ApiError } from "@/lib/api-client"

const mocks = vi.hoisted(() => ({
  listCodeRepos: vi.fn(),
  createCodeRepo: vi.fn(),
  updateCodeRepo: vi.fn(),
  deleteCodeRepo: vi.fn(),
  validateCodeRepo: vi.fn(),
}))

const authState = vi.hoisted(() => ({ role: "admin" as "admin" | "user" }))

vi.mock("./api", () => ({
  listCodeRepos: mocks.listCodeRepos,
  createCodeRepo: mocks.createCodeRepo,
  updateCodeRepo: mocks.updateCodeRepo,
  deleteCodeRepo: mocks.deleteCodeRepo,
  validateCodeRepo: mocks.validateCodeRepo,
}))

vi.mock("@/features/auth/AuthProvider", () => ({
  useAuth: () => ({
    user: { id: 1, username: "admin", role: authState.role, status: "active" },
    loading: false,
    login: vi.fn(),
    logout: vi.fn(),
  }),
}))

import { CodeReposPage } from "./CodeReposPage"

const REPO = {
  id: 1,
  key: "eztest",
  name: "EzTest",
  localPath: "D:/repo",
  fixedBranch: "dev",
  includeGlobs: ["src/**"],
  excludeGlobs: [".git/**"],
  status: "active",
  lastCommit: "abc",
  lastValidatedAt: "2026-10-27T08:00:00Z",
  lastValidationError: "",
  lastIndexedAt: null,
}

function apiError(status: number, reason: string): ApiError {
  return new ApiError({
    status,
    code: status === 422 ? "invalid_argument" : "conflict",
    message: "validation failed",
    details: { reason, field: "fixedBranch" },
  })
}

describe("CodeReposPage", () => {
  beforeEach(() => {
    authState.role = "admin"
    mocks.listCodeRepos.mockReset()
    mocks.createCodeRepo.mockReset()
    mocks.updateCodeRepo.mockReset()
    mocks.deleteCodeRepo.mockReset()
    mocks.validateCodeRepo.mockReset()
    mocks.listCodeRepos.mockResolvedValue({ items: [REPO], nextCursor: null, hasMore: false })
  })

  afterEach(() => {
    cleanup()
  })

  it("renders a repo row with its last commit and the index placeholder", async () => {
    render(<CodeReposPage />)

    expect(await screen.findByText("EzTest")).toBeTruthy()
    expect(screen.getByText("D:/repo")).toBeTruthy()
    expect(screen.getByText("dev")).toBeTruthy()
    expect(screen.getByText("abc")).toBeTruthy()
    expect(screen.getByText(/未索引（P4）/)).toBeTruthy()
  })

  it("hides every write action for a readonly user and shows the readonly badge", async () => {
    authState.role = "user"

    render(<CodeReposPage />)

    expect(await screen.findByText("EzTest")).toBeTruthy()
    expect(screen.getByText("只读")).toBeTruthy()
    expect(screen.queryByRole("button", { name: "新建代码仓" })).toBeNull()
    expect(screen.queryByRole("button", { name: "编辑" })).toBeNull()
    expect(screen.queryByRole("button", { name: "删除" })).toBeNull()
    expect(screen.queryByRole("button", { name: "重新校验" })).toBeNull()
    expect(screen.queryByLabelText("本地路径")).toBeNull()
  })

  it("shows the 422 reason when a re-validation fails", async () => {
    mocks.validateCodeRepo.mockRejectedValueOnce(apiError(422, "branch_not_found"))

    render(<CodeReposPage />)
    await screen.findByText("EzTest")

    fireEvent.click(screen.getByRole("button", { name: "重新校验" }))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("branch_not_found")
    expect(alert.textContent).toContain("分支不存在")
  })

  it("refreshes the commit after a successful validation and announces it", async () => {
    mocks.validateCodeRepo.mockResolvedValueOnce({
      ...REPO,
      lastCommit: "def456",
      lastValidatedAt: "2026-10-27T09:30:00Z",
    })

    render(<CodeReposPage />)
    await screen.findByText("abc")

    fireEvent.click(screen.getByRole("button", { name: "重新校验" }))

    await waitFor(() => expect(mocks.validateCodeRepo).toHaveBeenCalledWith(1))
    expect(await screen.findByText("def456")).toBeTruthy()
    const status = screen.getByRole("status")
    expect(status.textContent).toContain("已重新校验")
  })

  it("creates a repo and sends comma-separated globs as arrays", async () => {
    const created = {
      ...REPO,
      id: 2,
      key: "eztest-web",
      name: "EzTest Web",
      localPath: "D:/repo2",
      fixedBranch: "main",
    }
    mocks.listCodeRepos.mockResolvedValue({
      items: [REPO, created],
      nextCursor: null,
      hasMore: false,
    })
    mocks.createCodeRepo.mockResolvedValueOnce(created)

    render(<CodeReposPage />)
    await screen.findByText("EzTest")

    fireEvent.click(screen.getByRole("button", { name: "新建代码仓" }))
    fireEvent.change(screen.getByLabelText("标识"), { target: { value: "eztest-web" } })
    fireEvent.change(screen.getByLabelText("名称"), { target: { value: "EzTest Web" } })
    fireEvent.change(screen.getByLabelText("本地路径"), { target: { value: "D:/repo2" } })
    fireEvent.change(screen.getByLabelText("固定分支"), { target: { value: "main" } })
    fireEvent.change(screen.getByLabelText(/包含 Globs/), {
      target: { value: "src/**, app/**" },
    })
    fireEvent.change(screen.getByLabelText(/排除 Globs/), {
      target: { value: ".git/**, node_modules/**" },
    })
    fireEvent.click(screen.getByRole("button", { name: "保存" }))

    await waitFor(() =>
      expect(mocks.createCodeRepo).toHaveBeenCalledWith({
        key: "eztest-web",
        name: "EzTest Web",
        localPath: "D:/repo2",
        fixedBranch: "main",
        includeGlobs: ["src/**", "app/**"],
        excludeGlobs: [".git/**", "node_modules/**"],
      }),
    )
    expect(await screen.findByText("EzTest Web")).toBeTruthy()
    expect(screen.getByRole("status").textContent).toContain("已创建")
  })

  it("edits a repo without sending the immutable key", async () => {
    mocks.updateCodeRepo.mockResolvedValueOnce({ ...REPO, localPath: "D:/moved-repo" })

    render(<CodeReposPage />)
    await screen.findByText("EzTest")

    fireEvent.click(screen.getByRole("button", { name: "编辑" }))
    expect((screen.getByLabelText("标识") as HTMLInputElement).disabled).toBe(true)
    expect((screen.getByLabelText("名称") as HTMLInputElement).value).toBe("EzTest")

    fireEvent.change(screen.getByLabelText("本地路径"), { target: { value: "D:/moved-repo" } })
    fireEvent.click(screen.getByRole("button", { name: "保存" }))

    await waitFor(() => expect(mocks.updateCodeRepo).toHaveBeenCalledWith(1, expect.anything()))
    const [, payload] = mocks.updateCodeRepo.mock.calls[0] as [number, Record<string, unknown>]
    expect(payload).not.toHaveProperty("key")
    expect(payload.localPath).toBe("D:/moved-repo")
  })

  it("deletes a repo only after confirmation", async () => {
    mocks.deleteCodeRepo.mockResolvedValueOnce(undefined)

    render(<CodeReposPage />)
    await screen.findByText("EzTest")

    fireEvent.click(screen.getByRole("button", { name: "删除" }))
    expect(mocks.deleteCodeRepo).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole("button", { name: "确认删除" }))

    await waitFor(() => expect(mocks.deleteCodeRepo).toHaveBeenCalledWith(1))
  })

  it("shows the 409 reason when a save fails", async () => {
    mocks.createCodeRepo.mockRejectedValueOnce(apiError(409, "duplicate_key"))

    render(<CodeReposPage />)
    await screen.findByText("EzTest")

    fireEvent.click(screen.getByRole("button", { name: "新建代码仓" }))
    fireEvent.change(screen.getByLabelText("标识"), { target: { value: "eztest" } })
    fireEvent.change(screen.getByLabelText("名称"), { target: { value: "EzTest" } })
    fireEvent.change(screen.getByLabelText("本地路径"), { target: { value: "D:/repo" } })
    fireEvent.change(screen.getByLabelText("固定分支"), { target: { value: "dev" } })
    fireEvent.click(screen.getByRole("button", { name: "保存" }))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("duplicate_key")
  })
})
