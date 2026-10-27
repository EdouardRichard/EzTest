import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { Requirement } from "@/features/versions/types"

import type { Anchor } from "./types"

const mocks = vi.hoisted(() => ({
  listRequirements: vi.fn(),
  listAnchors: vi.fn(),
  createAnchor: vi.fn(),
  deleteAnchor: vi.fn(),
}))

vi.mock("@/features/versions/api", () => ({
  listRequirements: mocks.listRequirements,
}))

vi.mock("./api", () => ({
  listAnchors: mocks.listAnchors,
  createAnchor: mocks.createAnchor,
  deleteAnchor: mocks.deleteAnchor,
}))

import { AnchorDialog } from "./AnchorDialog"

const VERSION_ID = "1844674407370955161"
const REQ_ID = "1844674407370956001"
const ANCHOR_ID = "1844674407370955301"
const HEAD = "89abcdef0123456789abcdef0123456789abcdef"
const FILE_PATH = "src/api/refund.ts"

const REQUIREMENT: Requirement = {
  id: REQ_ID,
  versionId: VERSION_ID,
  title: "退款需求",
  description: "",
  acceptanceCriteria: "",
  priority: "must",
  status: "draft",
  source: "user",
  lastUpdateSource: "user",
  revision: 1,
  confirmedAt: null,
  confirmedBy: null,
  openQuestionCount: 0,
  relationCount: 0,
  attachmentCount: 0,
  createdBy: 1,
  updatedBy: 1,
  createdAt: "2026-10-27T10:00:00Z",
  updatedAt: "2026-10-27T10:00:00Z",
}

const ANCHOR: Anchor = {
  id: ANCHOR_ID,
  versionId: VERSION_ID,
  subjectKind: "requirement",
  subjectId: REQ_ID,
  filePath: FILE_PATH,
  startLine: 10,
  endLine: 20,
  commitSha: HEAD,
  confidence: "high",
  createdByKind: "user",
  createdBy: 1,
  createdAt: "2026-10-28T01:00:00Z",
}

function requirementsPage(items: Requirement[]) {
  return { items, nextCursor: "", hasMore: false }
}

function renderDialog(onClose = vi.fn(), onCreated = vi.fn()) {
  render(
    <AnchorDialog
      versionId={VERSION_ID}
      filePath={FILE_PATH}
      onClose={onClose}
      onCreated={onCreated}
    />,
  )
  return { onClose, onCreated }
}

describe("AnchorDialog", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset()
    mocks.listRequirements.mockResolvedValue(requirementsPage([REQUIREMENT]))
    mocks.listAnchors.mockResolvedValue([])
  })

  afterEach(() => {
    cleanup()
  })

  it("prefills the changed file and creates a requirement anchor with a line range", async () => {
    const created: Anchor = { ...ANCHOR }
    mocks.createAnchor.mockResolvedValue(created)
    mocks.listAnchors.mockResolvedValueOnce([]).mockResolvedValue([created])
    const { onCreated } = renderDialog()

    const dialog = screen.getByRole("dialog", { name: "关联需求锚点" })
    expect(dialog).toBeTruthy()
    expect(mocks.listRequirements).toHaveBeenCalledWith(VERSION_ID, { limit: 100 })
    expect((screen.getByLabelText("文件路径") as HTMLInputElement).value).toBe(FILE_PATH)

    const createButton = screen.getByRole("button", { name: "创建锚点" })
    expect((createButton as HTMLButtonElement).disabled).toBe(true)

    const requirement = await screen.findByRole("option", { name: /退款需求/ })
    expect((requirement as HTMLOptionElement).value).toBe(REQ_ID)
    fireEvent.change(screen.getByLabelText("需求"), { target: { value: REQ_ID } })

    await waitFor(() =>
      expect(mocks.listAnchors).toHaveBeenCalledWith(VERSION_ID, {
        subjectKind: "requirement",
        subjectId: REQ_ID,
      }),
    )

    fireEvent.change(screen.getByLabelText("起始行（可选）"), { target: { value: "10" } })
    fireEvent.change(screen.getByLabelText("结束行（可选）"), { target: { value: "20" } })
    fireEvent.click(screen.getByRole("button", { name: "创建锚点" }))

    await waitFor(() =>
      expect(mocks.createAnchor).toHaveBeenCalledWith(VERSION_ID, {
        subjectKind: "requirement",
        subjectId: REQ_ID,
        filePath: FILE_PATH,
        startLine: 10,
        endLine: 20,
      }),
    )
    expect(onCreated).toHaveBeenCalledTimes(1)
    expect(await screen.findByRole("button", { name: `删除锚点 ${FILE_PATH}:10-20` })).toBeTruthy()
  })

  it("lists existing anchors for the selected requirement and deletes one", async () => {
    mocks.listAnchors.mockResolvedValueOnce([ANCHOR]).mockResolvedValue([])
    mocks.deleteAnchor.mockResolvedValue(undefined)
    const { onCreated } = renderDialog()

    await screen.findByRole("option", { name: /退款需求/ })
    fireEvent.change(screen.getByLabelText("需求"), { target: { value: REQ_ID } })

    const remove = await screen.findByRole("button", { name: `删除锚点 ${FILE_PATH}:10-20` })
    fireEvent.click(remove)

    await waitFor(() => expect(mocks.deleteAnchor).toHaveBeenCalledWith(VERSION_ID, ANCHOR_ID))
    await waitFor(() => expect(onCreated).toHaveBeenCalledTimes(1))
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: `删除锚点 ${FILE_PATH}:10-20` })).toBeNull(),
    )
  })

  it("validates the line range and closes on Escape", async () => {
    const { onClose } = renderDialog()

    await screen.findByRole("option", { name: /退款需求/ })
    fireEvent.change(screen.getByLabelText("需求"), { target: { value: REQ_ID } })
    fireEvent.change(screen.getByLabelText("起始行（可选）"), { target: { value: "20" } })
    fireEvent.change(screen.getByLabelText("结束行（可选）"), { target: { value: "10" } })
    fireEvent.click(screen.getByRole("button", { name: "创建锚点" }))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("起始行不能大于结束行")
    expect(mocks.createAnchor).not.toHaveBeenCalled()

    fireEvent.keyDown(screen.getByRole("dialog", { name: "关联需求锚点" }), { key: "Escape" })
    expect(onClose).toHaveBeenCalledTimes(1)
  })
})
