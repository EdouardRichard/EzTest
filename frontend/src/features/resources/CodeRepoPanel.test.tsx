import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

vi.mock("./api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./api")>()
  return {
    ...actual,
    listRepositories: vi.fn(),
    createRepository: vi.fn(),
    updateRepository: vi.fn(),
    deleteRepository: vi.fn(),
  }
})

import * as api from "./api"
import { CodeRepoPanel } from "./CodeRepoPanel"
import { repositoryFixture } from "./test-fixtures"

describe("CodeRepoPanel", () => {
  beforeEach(() => {
    vi.mocked(api.listRepositories).mockReset()
    vi.mocked(api.createRepository).mockReset()
    vi.mocked(api.updateRepository).mockReset()
    vi.mocked(api.deleteRepository).mockReset()
    vi.mocked(api.listRepositories).mockResolvedValue([repositoryFixture])
  })

  afterEach(cleanup)

  it("lists code repositories with their branch and rules", async () => {
    render(<CodeRepoPanel />)

    expect(await screen.findByRole("table", { name: "代码仓列表" })).toBeInTheDocument()
    expect(screen.getByText("order-center-web")).toBeInTheDocument()
    expect(screen.getByText("D:/code/order-center-web")).toBeInTheDocument()
    expect(screen.getByText("main")).toBeInTheDocument()
    expect(screen.getByText(/包含 1 条/)).toBeInTheDocument()
  })

  it("shows empty, loading and retryable error states", async () => {
    vi.mocked(api.listRepositories).mockResolvedValueOnce([])
    const { unmount } = render(<CodeRepoPanel />)
    expect(await screen.findByText("还没有代码仓")).toBeInTheDocument()
    unmount()

    vi.mocked(api.listRepositories).mockRejectedValueOnce(
      new ApiError({
        status: 503,
        code: "unavailable",
        message: "代码仓列表暂不可用",
        retryable: true,
      }),
    )
    render(<CodeRepoPanel />)
    expect(await screen.findByText("代码仓列表暂不可用")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(await screen.findByText("order-center-web")).toBeInTheDocument()
  })

  it("validates the repository form and creates a repository", async () => {
    vi.mocked(api.createRepository).mockResolvedValue(repositoryFixture)
    render(<CodeRepoPanel />)
    await screen.findByText("order-center-web")

    fireEvent.click(screen.getByRole("button", { name: "新建代码仓" }))
    fireEvent.click(screen.getByRole("button", { name: "创建代码仓" }))
    expect(await screen.findByText("请输入仓库名称")).toBeInTheDocument()
    expect(api.createRepository).not.toHaveBeenCalled()

    fireEvent.change(screen.getByLabelText("仓库名称"), { target: { value: "pay-center-web" } })
    fireEvent.change(screen.getByLabelText("仓库路径"), {
      target: { value: "D:/code/pay-center-web" },
    })
    fireEvent.change(screen.getByLabelText("分支"), { target: { value: "main" } })
    fireEvent.change(screen.getByLabelText("包含规则（逗号分隔）"), {
      target: { value: "src/**, tests/**" },
    })
    fireEvent.click(screen.getByRole("button", { name: "创建代码仓" }))

    await waitFor(() =>
      expect(api.createRepository).toHaveBeenCalledWith({
        name: "pay-center-web",
        path: "D:/code/pay-center-web",
        branch: "main",
        include: "src/**, tests/**",
        exclude: "",
      }),
    )
    await waitFor(() => expect(api.listRepositories).toHaveBeenCalledTimes(2))
  })

  it("surfaces a 409 when a repository is still bound to a version", async () => {
    vi.mocked(api.deleteRepository).mockRejectedValue(
      new ApiError({
        status: 409,
        code: "conflict",
        message: "repository is referenced by a version binding",
      }),
    )
    render(<CodeRepoPanel />)
    await screen.findByText("order-center-web")

    fireEvent.click(screen.getByRole("button", { name: "删除代码仓 order-center-web" }))
    fireEvent.click(screen.getByRole("button", { name: "删除代码仓" }))

    expect(
      await screen.findByText(/repository is referenced by a version binding/),
    ).toBeInTheDocument()
    expect(screen.getByText(/操作冲突/)).toBeInTheDocument()
    expect(api.deleteRepository).toHaveBeenCalledWith("1201")
    expect(api.listRepositories).toHaveBeenCalledTimes(1)
  })
})
