import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

const api = vi.hoisted(() => ({
  listVersions: vi.fn(),
  getVersion: vi.fn(),
  createVersion: vi.fn(),
  getResourceCatalog: vi.fn(),
}))
vi.mock("./api", () => api)

import { CreateVersionDialog } from "./CreateVersionDialog"
import { catalogFixture, versionFixture } from "./test-fixtures"

function renderDialog() {
  const onClose = vi.fn()
  const onCreated = vi.fn()
  render(<CreateVersionDialog open onClose={onClose} onCreated={onCreated} />)
  return { onClose, onCreated }
}

async function waitForWizard(): Promise<void> {
  await screen.findByRole("dialog", { name: "新建版本" })
  await screen.findByLabelText("SUT")
}

describe("CreateVersionDialog", () => {
  beforeEach(() => {
    api.getResourceCatalog.mockReset()
    api.createVersion.mockReset()
    api.getResourceCatalog.mockResolvedValue(catalogFixture)
    api.createVersion.mockResolvedValue(versionFixture)
  })

  afterEach(cleanup)

  it("submits the exact contract payload for multiple SUT bindings", async () => {
    const { onCreated } = renderDialog()
    await waitForWizard()

    fireEvent.change(screen.getByLabelText("版本标题"), {
      target: { value: "  订单中心 · 2026 Q4 迭代  " },
    })
    fireEvent.change(screen.getByLabelText("SUT"), { target: { value: "1001" } })
    fireEvent.change(screen.getByLabelText("环境"), { target: { value: "1101" } })
    fireEvent.click(screen.getByLabelText(/订单列表/))
    fireEvent.click(screen.getByLabelText(/订单详情/))
    fireEvent.click(screen.getByLabelText(/查询订单/))
    fireEvent.click(screen.getByLabelText(/order_db/))
    fireEvent.click(screen.getByLabelText(/buyer/))
    fireEvent.change(screen.getByLabelText("代码仓（可选）"), { target: { value: "1201" } })

    fireEvent.click(screen.getByRole("button", { name: "添加 SUT 绑定" }))
    const sutSelects = screen.getAllByLabelText("SUT")
    const environmentSelects = screen.getAllByLabelText("环境")
    expect(sutSelects).toHaveLength(2)
    fireEvent.change(sutSelects[1] as HTMLElement, { target: { value: "1002" } })
    fireEvent.change(environmentSelects[1] as HTMLElement, { target: { value: "2101" } })
    fireEvent.click(screen.getByLabelText(/支付结果/))

    fireEvent.click(screen.getByRole("button", { name: "创建版本" }))

    await waitFor(() => expect(api.createVersion).toHaveBeenCalledTimes(1))
    expect(api.createVersion).toHaveBeenCalledWith({
      title: "订单中心 · 2026 Q4 迭代",
      bindings: [
        {
          sut_id: "1001",
          environment_id: "1101",
          code_repository_id: "1201",
          page_ids: ["1301", "1302"],
          api_ids: ["1401"],
          database_ids: ["1501"],
          accounts: [{ account_id: "1601" }],
        },
        {
          sut_id: "1002",
          environment_id: "2101",
          page_ids: ["2301"],
          api_ids: [],
          database_ids: [],
          accounts: [],
        },
      ],
    })
    await waitFor(() => expect(onCreated).toHaveBeenCalledWith(versionFixture))
  })

  it("validates the title, SUT and environment before calling the API", async () => {
    renderDialog()
    await waitForWizard()

    fireEvent.click(screen.getByRole("button", { name: "创建版本" }))
    expect(await screen.findByText("请输入版本标题")).toBeInTheDocument()
    expect(screen.getByText("请选择 SUT", { selector: "p" })).toBeInTheDocument()

    fireEvent.change(screen.getByLabelText("版本标题"), { target: { value: "迭代" } })
    fireEvent.change(screen.getByLabelText("SUT"), { target: { value: "1001" } })
    fireEvent.click(screen.getByRole("button", { name: "创建版本" }))
    expect(await screen.findByText("请选择环境", { selector: "p" })).toBeInTheDocument()

    expect(api.createVersion).not.toHaveBeenCalled()
  })

  it("allows submitting with no pages, APIs, databases or accounts selected", async () => {
    renderDialog()
    await waitForWizard()

    fireEvent.change(screen.getByLabelText("版本标题"), { target: { value: "最小版本" } })
    fireEvent.change(screen.getByLabelText("SUT"), { target: { value: "1001" } })
    fireEvent.change(screen.getByLabelText("环境"), { target: { value: "1101" } })
    fireEvent.click(screen.getByRole("button", { name: "创建版本" }))

    await waitFor(() => expect(api.createVersion).toHaveBeenCalledTimes(1))
    expect(api.createVersion).toHaveBeenCalledWith({
      title: "最小版本",
      bindings: [
        {
          sut_id: "1001",
          environment_id: "1101",
          page_ids: [],
          api_ids: [],
          database_ids: [],
          accounts: [],
        },
      ],
    })
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

    await waitForWizard()
    expect(api.getResourceCatalog).toHaveBeenCalledTimes(2)
  })

  it("removes a binding and shows the multi-select groups per environment", async () => {
    renderDialog()
    await waitForWizard()

    fireEvent.click(screen.getByRole("button", { name: "添加 SUT 绑定" }))
    expect(screen.getAllByLabelText("SUT")).toHaveLength(2)
    fireEvent.click(screen.getByRole("button", { name: "移除绑定 2" }))
    expect(screen.getAllByLabelText("SUT")).toHaveLength(1)

    fireEvent.change(screen.getByLabelText("SUT"), { target: { value: "1001" } })
    fireEvent.change(screen.getByLabelText("环境"), { target: { value: "1101" } })
    const pageGroup = screen.getByRole("group", { name: /页面/ })
    expect(within(pageGroup).getByLabelText(/订单列表/)).toBeInTheDocument()
    expect(screen.getByRole("group", { name: /目标库/ })).toBeInTheDocument()
    expect(screen.getByRole("group", { name: /测试账号/ })).toBeInTheDocument()
  })
})
