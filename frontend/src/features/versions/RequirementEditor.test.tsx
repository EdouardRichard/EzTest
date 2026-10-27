import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { ApiError } from "@/lib/api-client"

const mocks = vi.hoisted(() => ({
  createRequirement: vi.fn(),
  updateRequirement: vi.fn(),
  deleteRequirement: vi.fn(),
  confirmRequirement: vi.fn(),
  reopenRequirement: vi.fn(),
  listAttachments: vi.fn(),
  uploadAttachment: vi.fn(),
  downloadAttachment: vi.fn(),
  deleteAttachment: vi.fn(),
}))

vi.mock("./api", () => ({
  createRequirement: mocks.createRequirement,
  updateRequirement: mocks.updateRequirement,
  deleteRequirement: mocks.deleteRequirement,
  confirmRequirement: mocks.confirmRequirement,
  reopenRequirement: mocks.reopenRequirement,
  listAttachments: mocks.listAttachments,
  uploadAttachment: mocks.uploadAttachment,
  downloadAttachment: mocks.downloadAttachment,
  deleteAttachment: mocks.deleteAttachment,
}))

import { RequirementEditor } from "./RequirementEditor"
import type { Requirement } from "./types"

const VERSION_ID = "1844674407370955161"
const REQ_ID = "1844674407370956001"

function requirement(overrides: Partial<Requirement> = {}): Requirement {
  return {
    id: REQ_ID,
    versionId: VERSION_ID,
    title: "订单创建",
    description: "用户提交订单后创建订单记录。",
    acceptanceCriteria: "订单出现在订单列表。",
    priority: "must",
    status: "draft",
    source: "user",
    lastUpdateSource: "user",
    revision: 3,
    confirmedAt: null,
    confirmedBy: null,
    openQuestionCount: 0,
    relationCount: 1,
    attachmentCount: 1,
    createdBy: 1,
    updatedBy: 1,
    createdAt: "2026-10-27T10:00:00Z",
    updatedAt: "2026-10-27T10:00:00Z",
    ...overrides,
  }
}

function renderEditor(props: Partial<Parameters<typeof RequirementEditor>[0]> = {}) {
  return render(
    <RequirementEditor
      versionId={VERSION_ID}
      requirement={null}
      creating={false}
      onCreated={vi.fn()}
      onUpdated={vi.fn()}
      onDeleted={vi.fn()}
      onCancelCreate={vi.fn()}
      onReload={vi.fn()}
      onAttachmentsChanged={vi.fn()}
      {...props}
    />,
  )
}

describe("RequirementEditor", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset()
    mocks.listAttachments.mockResolvedValue([])
  })

  afterEach(cleanup)

  it("edits a draft requirement and saves with its revision", async () => {
    const updated = requirement({ title: "订单创建（修订）", revision: 4 })
    mocks.updateRequirement.mockResolvedValue(updated)
    const onUpdated = vi.fn()

    renderEditor({ requirement: requirement(), onUpdated })
    await screen.findByText("暂无附件")

    expect(screen.getByRole("heading", { name: "编辑需求" })).toBeTruthy()
    expect((screen.getByLabelText("标题") as HTMLInputElement).value).toBe("订单创建")
    expect((screen.getByLabelText("描述") as HTMLTextAreaElement).value).toContain("创建订单记录")
    expect((screen.getByLabelText("验收标准") as HTMLTextAreaElement).value).toContain("订单列表")
    expect((screen.getByLabelText("优先级") as HTMLSelectElement).value).toBe("must")
    expect(screen.getByText("待澄清问题 0")).toBeTruthy()

    fireEvent.change(screen.getByLabelText("标题"), { target: { value: "订单创建（修订）" } })
    fireEvent.click(screen.getByRole("button", { name: "保存需求" }))

    await waitFor(() =>
      expect(mocks.updateRequirement).toHaveBeenCalledWith(VERSION_ID, REQ_ID, {
        revision: 3,
        title: "订单创建（修订）",
        description: "用户提交订单后创建订单记录。",
        acceptanceCriteria: "订单出现在订单列表。",
        priority: "must",
      }),
    )
    expect(onUpdated).toHaveBeenCalledWith(updated)
  })

  it("disables confirmation while the requirement has open questions", async () => {
    const { rerender } = renderEditor({
      requirement: requirement({ openQuestionCount: 2 }),
    })
    await screen.findByText("暂无附件")

    const blocked = screen.getByRole("button", { name: "确认需求" }) as HTMLButtonElement
    expect(blocked.disabled).toBe(true)
    expect(screen.getByText("仍有 2 个待澄清问题，确认前需先解决。")).toBeTruthy()
    expect(mocks.confirmRequirement).not.toHaveBeenCalled()

    const confirmed = requirement({
      openQuestionCount: 0,
      status: "clarified",
      confirmedAt: "2026-10-27T12:00:00Z",
      confirmedBy: 1,
      revision: 4,
    })
    mocks.confirmRequirement.mockResolvedValue(confirmed)
    const onUpdated = vi.fn()
    rerender(
      <RequirementEditor
        versionId={VERSION_ID}
        requirement={requirement({ openQuestionCount: 0 })}
        creating={false}
        onCreated={vi.fn()}
        onUpdated={onUpdated}
        onDeleted={vi.fn()}
        onCancelCreate={vi.fn()}
        onReload={vi.fn()}
        onAttachmentsChanged={vi.fn()}
      />,
    )

    const enabled = screen.getByRole("button", { name: "确认需求" }) as HTMLButtonElement
    expect(enabled.disabled).toBe(false)

    fireEvent.click(enabled)
    await waitFor(() => expect(mocks.confirmRequirement).toHaveBeenCalledWith(VERSION_ID, REQ_ID))
    expect(onUpdated).toHaveBeenCalledWith(confirmed)
  })

  it("renders a clarified requirement read-only and reopens it", async () => {
    const reopened = requirement({
      status: "draft",
      revision: 5,
      confirmedAt: null,
      confirmedBy: null,
    })
    mocks.reopenRequirement.mockResolvedValue(reopened)
    const onUpdated = vi.fn()

    renderEditor({
      requirement: requirement({
        status: "clarified",
        revision: 4,
        confirmedAt: "2026-10-27T12:00:00Z",
        confirmedBy: 1,
      }),
      onUpdated,
    })
    await screen.findByText("暂无附件")

    expect(screen.getByText("已确认需求为只读，可重开后继续编辑。")).toBeTruthy()
    expect((screen.getByLabelText("标题") as HTMLInputElement).readOnly).toBe(true)
    expect((screen.getByLabelText("描述") as HTMLTextAreaElement).readOnly).toBe(true)
    expect((screen.getByLabelText("优先级") as HTMLSelectElement).disabled).toBe(true)
    expect(screen.queryByRole("button", { name: "保存需求" })).toBeNull()
    expect(screen.queryByRole("button", { name: "确认需求" })).toBeNull()

    fireEvent.click(screen.getByRole("button", { name: "重开需求" }))
    await waitFor(() => expect(mocks.reopenRequirement).toHaveBeenCalledWith(VERSION_ID, REQ_ID))
    expect(onUpdated).toHaveBeenCalledWith(reopened)
  })

  it("creates a requirement after validating the title", async () => {
    const created = requirement({ id: "1844674407370956009", title: "退款审核", revision: 1 })
    mocks.createRequirement.mockResolvedValue(created)
    const onCreated = vi.fn()

    renderEditor({ creating: true, onCreated })

    expect(screen.getByRole("heading", { name: "新增需求" })).toBeTruthy()
    fireEvent.click(screen.getByRole("button", { name: "创建需求" }))
    expect(screen.getByRole("alert").textContent).toContain("请输入需求标题。")
    expect(mocks.createRequirement).not.toHaveBeenCalled()

    fireEvent.change(screen.getByLabelText("标题"), { target: { value: "退款审核" } })
    fireEvent.change(screen.getByLabelText("描述"), { target: { value: "客服审核退款申请。" } })
    fireEvent.click(screen.getByRole("button", { name: "创建需求" }))

    await waitFor(() =>
      expect(mocks.createRequirement).toHaveBeenCalledWith(VERSION_ID, {
        title: "退款审核",
        description: "客服审核退款申请。",
        acceptanceCriteria: "",
        priority: "should",
      }),
    )
    expect(onCreated).toHaveBeenCalledWith(created)
  })

  it("keeps the draft and asks to refresh after a 409 revision conflict", async () => {
    mocks.updateRequirement.mockRejectedValue(
      new ApiError({
        status: 409,
        code: "conflict",
        message: "revision conflict",
        details: { reason: "revision_conflict" },
      }),
    )
    const onReload = vi.fn()

    renderEditor({ requirement: requirement(), onReload })
    await screen.findByText("暂无附件")

    const title = screen.getByLabelText("标题") as HTMLInputElement
    fireEvent.change(title, { target: { value: "本地尚未保存的标题" } })
    fireEvent.click(screen.getByRole("button", { name: "保存需求" }))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("该需求已被更新，请刷新后重试。")
    expect((screen.getByLabelText("标题") as HTMLInputElement).value).toBe("本地尚未保存的标题")

    fireEvent.click(screen.getByRole("button", { name: "刷新" }))
    expect(onReload).toHaveBeenCalledTimes(1)
  })

  it("deletes a draft requirement after confirmation", async () => {
    mocks.deleteRequirement.mockResolvedValue(undefined)
    const onDeleted = vi.fn()

    renderEditor({ requirement: requirement(), onDeleted })
    await screen.findByText("暂无附件")

    fireEvent.click(screen.getByRole("button", { name: "删除需求" }))
    const dialog = screen.getByRole("alertdialog")
    expect(dialog.textContent).toContain("确认删除需求「订单创建」")

    fireEvent.click(screen.getByRole("button", { name: "确认删除" }))
    await waitFor(() => expect(mocks.deleteRequirement).toHaveBeenCalledWith(VERSION_ID, REQ_ID))
    expect(onDeleted).toHaveBeenCalledWith(REQ_ID)
  })

  it("shows a selection hint when no requirement is selected", () => {
    renderEditor()

    expect(screen.getByText("未选择需求")).toBeTruthy()
    expect(
      screen.getByText("从左侧需求列表选择一条需求进行编辑，或点击「新增需求」。"),
    ).toBeTruthy()
  })
})
