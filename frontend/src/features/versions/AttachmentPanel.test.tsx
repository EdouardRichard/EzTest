import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { ApiError } from "@/lib/api-client"

const mocks = vi.hoisted(() => ({
  listAttachments: vi.fn(),
  uploadAttachment: vi.fn(),
  downloadAttachment: vi.fn(),
  deleteAttachment: vi.fn(),
}))

vi.mock("./api", () => ({
  listAttachments: mocks.listAttachments,
  uploadAttachment: mocks.uploadAttachment,
  downloadAttachment: mocks.downloadAttachment,
  deleteAttachment: mocks.deleteAttachment,
}))

import { AttachmentPanel } from "./AttachmentPanel"
import type { Requirement, RequirementAttachment } from "./types"

const VERSION_ID = "1844674407370955161"
const REQ_ID = "1844674407370956001"
const ATTACHMENT_ID = "1844674407370956201"
const IMAGE_ID = "1844674407370956202"

const REQUIREMENT: Requirement = {
  id: REQ_ID,
  versionId: VERSION_ID,
  title: "订单创建",
  description: "",
  acceptanceCriteria: "",
  priority: "should",
  status: "draft",
  source: "user",
  lastUpdateSource: "user",
  revision: 1,
  confirmedAt: null,
  confirmedBy: null,
  openQuestionCount: 0,
  relationCount: 0,
  attachmentCount: 2,
  createdBy: 1,
  updatedBy: 1,
  createdAt: "2026-10-27T10:00:00Z",
  updatedAt: "2026-10-27T10:00:00Z",
}

const TEXT_ATTACHMENT: RequirementAttachment = {
  id: ATTACHMENT_ID,
  kind: "text",
  filename: "需求说明.md",
  mimeType: "text/markdown",
  sizeBytes: 2048,
  sha256: "a".repeat(64),
  createdAt: "2026-10-27T10:05:00Z",
}

const IMAGE_ATTACHMENT: RequirementAttachment = {
  id: IMAGE_ID,
  kind: "image",
  filename: "原型.png",
  mimeType: "image/png",
  sizeBytes: 1048576,
  sha256: "b".repeat(64),
  createdAt: "2026-10-27T10:06:00Z",
}

let clickSpy: ReturnType<typeof vi.spyOn>
const createObjectURL = vi.fn(() => "blob:mock-url")
const revokeObjectURL = vi.fn()

function renderPanel(requirement: Requirement = REQUIREMENT, onChanged: () => void = vi.fn()) {
  return render(
    <AttachmentPanel versionId={VERSION_ID} requirement={requirement} onChanged={onChanged} />,
  )
}

describe("AttachmentPanel", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset()
    mocks.listAttachments.mockResolvedValue([TEXT_ATTACHMENT, IMAGE_ATTACHMENT])
    Object.defineProperty(URL, "createObjectURL", {
      configurable: true,
      writable: true,
      value: createObjectURL,
    })
    Object.defineProperty(URL, "revokeObjectURL", {
      configurable: true,
      writable: true,
      value: revokeObjectURL,
    })
    createObjectURL.mockClear()
    revokeObjectURL.mockClear()
    clickSpy = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {})
  })

  afterEach(() => {
    clickSpy.mockRestore()
    cleanup()
  })

  it("lists attachment metadata with kind, size and download actions", async () => {
    renderPanel()

    expect(await screen.findByText("需求说明.md")).toBeTruthy()
    expect(screen.getByText("原型.png")).toBeTruthy()
    expect(screen.getByText("文本 · 2.0 KB")).toBeTruthy()
    expect(screen.getByText("图片 · 1.0 MB")).toBeTruthy()
    expect(mocks.listAttachments).toHaveBeenCalledWith(VERSION_ID, REQ_ID)
    expect(screen.getByRole("button", { name: "下载附件 需求说明.md" })).toBeTruthy()
    expect(screen.getByRole("button", { name: "删除附件 需求说明.md" })).toBeTruthy()
    expect(screen.getByLabelText("上传附件")).toBeTruthy()
  })

  it("uploads a file as multipart data and refreshes the list", async () => {
    const onChanged = vi.fn()
    mocks.uploadAttachment.mockResolvedValue(TEXT_ATTACHMENT)
    renderPanel(REQUIREMENT, onChanged)
    await screen.findByText("需求说明.md")

    const file = new File(["# 需求"], "新增说明.md", { type: "text/markdown" })
    fireEvent.change(screen.getByLabelText("上传附件"), { target: { files: [file] } })

    await waitFor(() =>
      expect(mocks.uploadAttachment).toHaveBeenCalledWith(VERSION_ID, REQ_ID, file),
    )
    await waitFor(() => expect(mocks.listAttachments).toHaveBeenCalledTimes(2))
    expect(onChanged).toHaveBeenCalledTimes(1)
  })

  it("downloads attachment content through the authenticated API", async () => {
    const blob = new Blob(["hello"], { type: "text/markdown" })
    mocks.downloadAttachment.mockResolvedValue(blob)
    renderPanel()
    await screen.findByText("需求说明.md")

    fireEvent.click(screen.getByRole("button", { name: "下载附件 需求说明.md" }))

    await waitFor(() =>
      expect(mocks.downloadAttachment).toHaveBeenCalledWith(VERSION_ID, REQ_ID, ATTACHMENT_ID),
    )
    expect(createObjectURL).toHaveBeenCalledWith(blob)
    expect(clickSpy).toHaveBeenCalledTimes(1)
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:mock-url")
  })

  it("deletes a draft attachment and refreshes the list", async () => {
    const onChanged = vi.fn()
    mocks.deleteAttachment.mockResolvedValue(undefined)
    renderPanel(REQUIREMENT, onChanged)
    await screen.findByText("需求说明.md")

    fireEvent.click(screen.getByRole("button", { name: "删除附件 需求说明.md" }))

    await waitFor(() =>
      expect(mocks.deleteAttachment).toHaveBeenCalledWith(VERSION_ID, REQ_ID, ATTACHMENT_ID),
    )
    await waitFor(() => expect(mocks.listAttachments).toHaveBeenCalledTimes(2))
    expect(onChanged).toHaveBeenCalledTimes(1)
  })

  it("keeps a clarified requirement read-only but downloadable", async () => {
    renderPanel({ ...REQUIREMENT, status: "clarified" })

    expect(await screen.findByText("需求说明.md")).toBeTruthy()
    expect(screen.getByText("已确认需求为只读，附件不可上传或删除。")).toBeTruthy()
    expect(screen.queryByLabelText("上传附件")).toBeNull()
    expect(screen.queryByRole("button", { name: "删除附件 需求说明.md" })).toBeNull()
    expect(screen.getByRole("button", { name: "下载附件 需求说明.md" })).toBeTruthy()
  })

  it("keeps the last list and retries after a load failure", async () => {
    mocks.listAttachments
      .mockRejectedValueOnce(new ApiError({ status: 0, code: "network_error", message: "offline" }))
      .mockResolvedValueOnce([TEXT_ATTACHMENT])
    renderPanel()

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("加载附件失败")

    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(await screen.findByText("需求说明.md")).toBeTruthy()
    expect(mocks.listAttachments).toHaveBeenCalledTimes(2)
  })

  it("explains a duplicate upload and keeps the input usable", async () => {
    mocks.uploadAttachment.mockRejectedValue(
      new ApiError({
        status: 409,
        code: "conflict",
        message: "duplicate attachment",
        details: { reason: "duplicate_attachment" },
      }),
    )
    renderPanel()
    await screen.findByText("需求说明.md")

    const file = new File(["# 需求"], "需求说明.md", { type: "text/markdown" })
    fireEvent.change(screen.getByLabelText("上传附件"), { target: { files: [file] } })

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("相同内容的附件已存在。")
    expect((screen.getByLabelText("上传附件") as HTMLInputElement).disabled).toBe(false)
  })
})
