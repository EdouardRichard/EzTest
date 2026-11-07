import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

const api = vi.hoisted(() => ({
  getResourceCatalog: vi.fn(),
  listSutRepositories: vi.fn(),
  createVersion: vi.fn(),
  updateVersionBinding: vi.fn(),
  getVersionBinding: vi.fn(),
  bindingErrorDetails: vi.fn(() => null),
}))
vi.mock("./api", () => api)

import { CreateVersionDialog } from "./CreateVersionDialog"
import { bindingOverviewFixture, catalogFixture, versionFixture } from "./test-fixtures"

function renderDialog() {
  const onClose = vi.fn()
  const onCreated = vi.fn()
  render(<CreateVersionDialog open onClose={onClose} onCreated={onCreated} />)
  return { onClose, onCreated }
}

async function walkToConfirm(): Promise<void> {
  await screen.findByLabelText("版本名称")
  fireEvent.change(screen.getByLabelText("版本名称"), { target: { value: "新迭代" } })
  fireEvent.change(screen.getByLabelText("环境"), { target: { value: "1101" } })
  fireEvent.click(screen.getByRole("button", { name: "下一步" }))
  await screen.findByRole("group", { name: /代码仓/ })
  fireEvent.click(screen.getByRole("button", { name: "下一步" }))
  await screen.findByRole("group", { name: /页面/ })
  fireEvent.click(screen.getByRole("button", { name: "下一步" }))
  await screen.findByText("确认创建")
}

describe("CreateVersionDialog", () => {
  beforeEach(() => {
    api.getResourceCatalog.mockReset()
    api.listSutRepositories.mockReset()
    api.createVersion.mockReset()
    api.updateVersionBinding.mockReset()
    api.getVersionBinding.mockReset()
    api.getResourceCatalog.mockResolvedValue(catalogFixture)
    api.listSutRepositories.mockResolvedValue([])
    api.createVersion.mockResolvedValue(versionFixture)
    api.updateVersionBinding.mockResolvedValue(bindingOverviewFixture)
  })

  afterEach(cleanup)

  it("hosts the environment-only wizard and returns the created version", async () => {
    const { onCreated } = renderDialog()
    await screen.findByRole("dialog", { name: "新建版本" })
    await walkToConfirm()

    fireEvent.click(screen.getByRole("button", { name: "创建版本" }))

    await waitFor(() => expect(api.createVersion).toHaveBeenCalledTimes(1))
    expect(api.createVersion).toHaveBeenCalledWith({
      title: "新迭代",
      description: "",
      goal: "",
      environment_id: "1101",
    })
    await waitFor(() =>
      expect(api.updateVersionBinding).toHaveBeenCalledWith("2001", {
        repository_ids: [],
        account_ids: ["1601", "1602"],
        scope_exclude: { pages: [], apis: [], databases: [] },
      }),
    )
    await waitFor(() => expect(onCreated).toHaveBeenCalledWith(versionFixture))
  })

  it("validates the version name and environment before leaving step 1", async () => {
    renderDialog()
    await screen.findByLabelText("版本名称")

    fireEvent.click(screen.getByRole("button", { name: "下一步" }))
    expect(await screen.findByText("请输入版本名称")).toBeInTheDocument()
    expect(screen.getByText("请选择环境")).toBeInTheDocument()
    expect(api.createVersion).not.toHaveBeenCalled()
  })

  it("shows the catalog error and retries", async () => {
    api.getResourceCatalog.mockRejectedValueOnce(
      new ApiError({
        status: 503,
        code: "unavailable",
        message: "资源目录暂不可用",
        requestId: "req_catalog",
        retryable: true,
      }),
    )
    renderDialog()

    expect(await screen.findByText("资源目录暂不可用")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))

    expect(await screen.findByLabelText("版本名称")).toBeInTheDocument()
    expect(api.getResourceCatalog).toHaveBeenCalledTimes(2)
  })

  it("cancels through the wizard callback", async () => {
    const { onClose } = renderDialog()
    await screen.findByLabelText("版本名称")
    fireEvent.click(screen.getByRole("button", { name: "取消" }))
    expect(onClose).toHaveBeenCalledTimes(1)
  })
})
