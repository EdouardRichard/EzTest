import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

const api = vi.hoisted(() => ({
  listCodeAnchors: vi.fn(),
  listVersionCodeRepos: vi.fn(),
  createCodeAnchor: vi.fn(),
  validateCodeAnchor: vi.fn(),
  deleteCodeAnchor: vi.fn(),
}))
vi.mock("../api", () => api)

import { AnchorPanel } from "../AnchorPanel"
import { anchorFixture, brokenAnchorFixture, staleAnchorFixture } from "../test-fixtures"

const copyMock = vi.fn<(text: string) => Promise<void>>()

function renderPanel() {
  return render(
    <AnchorPanel
      versionId="2001"
      subjectKind="case_version"
      subjectId="7002"
      title="用例代码锚点"
    />,
  )
}

describe("AnchorPanel", () => {
  beforeEach(() => {
    window.sessionStorage.clear()
    for (const fn of Object.values(api)) fn.mockReset()
    api.listCodeAnchors.mockResolvedValue([anchorFixture, staleAnchorFixture, brokenAnchorFixture])
    api.listVersionCodeRepos.mockResolvedValue([{ id: "5001", name: "order-center-web" }])
    api.createCodeAnchor.mockResolvedValue(anchorFixture)
    api.validateCodeAnchor.mockResolvedValue({ ...staleAnchorFixture, status: "valid" })
    api.deleteCodeAnchor.mockResolvedValue(undefined)
    copyMock.mockReset()
    copyMock.mockResolvedValue(undefined)
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText: copyMock },
    })
  })

  afterEach(cleanup)

  it("stays collapsed until expanded, then lists valid/stale/broken anchors", async () => {
    renderPanel()

    expect(screen.getByTestId("anchor-panel-toggle")).toBeInTheDocument()
    expect(api.listCodeAnchors).not.toHaveBeenCalled()

    fireEvent.click(screen.getByTestId("anchor-panel-toggle"))

    expect(await screen.findByText("src/pages/OrdersPage.tsx:10-24")).toBeInTheDocument()
    expect(api.listCodeAnchors).toHaveBeenCalledWith("2001", {
      subjectKind: "case_version",
      subjectId: "7002",
    })
    expect(screen.getByText("正常")).toBeInTheDocument()
    expect(screen.getByText("内容已变")).toBeInTheDocument()
    expect(screen.getByText("文件/行失效")).toBeInTheDocument()
    expect(screen.getByText("src/api/refund.ts:10-24")).toBeInTheDocument()
    expect(screen.getByText("src/legacy/old.ts:10-24")).toBeInTheDocument()
  })

  it("adds an anchor with the selected repository and path:line", async () => {
    renderPanel()
    fireEvent.click(screen.getByTestId("anchor-panel-toggle"))
    await screen.findByText("src/pages/OrdersPage.tsx:10-24")

    fireEvent.click(screen.getByRole("button", { name: "添加锚点" }))
    expect(await screen.findByLabelText("代码仓库")).toHaveValue("5001")
    fireEvent.change(screen.getByLabelText("文件路径"), {
      target: { value: "src/pages/RefundPage.tsx" },
    })
    fireEvent.change(screen.getByLabelText("起始行"), { target: { value: "12" } })
    fireEvent.change(screen.getByLabelText("结束行"), { target: { value: "30" } })
    fireEvent.click(screen.getByRole("button", { name: "保存锚点" }))

    await waitFor(() =>
      expect(api.createCodeAnchor).toHaveBeenCalledWith("2001", {
        subjectKind: "case_version",
        subjectId: "7002",
        repoId: "5001",
        filePath: "src/pages/RefundPage.tsx",
        startLine: 12,
        endLine: 30,
      }),
    )
    expect(await screen.findByRole("status")).toHaveTextContent("锚点已创建")
    await waitFor(() => expect(api.listCodeAnchors).toHaveBeenCalledTimes(2))
  })

  it("validates the add form before calling the API", async () => {
    renderPanel()
    fireEvent.click(screen.getByTestId("anchor-panel-toggle"))
    await screen.findByText("src/pages/OrdersPage.tsx:10-24")

    fireEvent.click(screen.getByRole("button", { name: "添加锚点" }))
    await screen.findByLabelText("代码仓库")
    fireEvent.change(screen.getByLabelText("起始行"), { target: { value: "8" } })
    fireEvent.change(screen.getByLabelText("结束行"), { target: { value: "4" } })
    fireEvent.click(screen.getByRole("button", { name: "保存锚点" }))

    expect(await screen.findByText("文件路径不能为空")).toBeInTheDocument()
    expect(screen.getByText("结束行不能小于起始行")).toBeInTheDocument()
    expect(api.createCodeAnchor).not.toHaveBeenCalled()
  })

  it("re-validates one anchor and updates its status", async () => {
    renderPanel()
    fireEvent.click(screen.getByTestId("anchor-panel-toggle"))
    const row = await screen.findByTestId("anchor-row-8102")

    fireEvent.click(within(row).getByRole("button", { name: "校验" }))

    await waitFor(() => expect(api.validateCodeAnchor).toHaveBeenCalledWith("2001", "8102"))
    expect(await within(row).findByText("正常")).toBeInTheDocument()
  })

  it("deletes one anchor and copies its path", async () => {
    renderPanel()
    fireEvent.click(screen.getByTestId("anchor-panel-toggle"))
    const row = await screen.findByTestId("anchor-row-8101")

    fireEvent.click(within(row).getByRole("button", { name: "复制路径" }))
    await waitFor(() => expect(copyMock).toHaveBeenCalledWith("src/pages/OrdersPage.tsx:10-24"))
    expect(await screen.findByRole("status")).toHaveTextContent("已复制路径")

    fireEvent.click(within(row).getByRole("button", { name: "删除" }))
    await waitFor(() => expect(api.deleteCodeAnchor).toHaveBeenCalledWith("2001", "8101"))
    await waitFor(() => expect(screen.queryByTestId("anchor-row-8101")).not.toBeInTheDocument())
  })

  it("shows a retryable error when the anchor list fails", async () => {
    api.listCodeAnchors
      .mockRejectedValueOnce(
        new ApiError({ status: 404, code: "not_found", message: "锚点列表不可用" }),
      )
      .mockResolvedValueOnce([anchorFixture])

    renderPanel()
    fireEvent.click(screen.getByTestId("anchor-panel-toggle"))

    expect(await screen.findByText("锚点列表不可用")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(await screen.findByText("src/pages/OrdersPage.tsx:10-24")).toBeInTheDocument()
  })
})
