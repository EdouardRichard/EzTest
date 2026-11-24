import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

import {
  confirmedRequirementDetailFixture,
  requirementDetailFixture,
  requirementVersionFixture,
} from "./test-fixtures"
import type { RequirementDetail } from "./types"

const api = vi.hoisted(() => ({
  updateRequirement: vi.fn(),
  confirmRequirement: vi.fn(),
  reopenRequirement: vi.fn(),
  rollbackRequirement: vi.fn(),
  getAttachmentPendingDetails: vi.fn(),
}))
vi.mock("./api", () => api)

import { RequirementEditor } from "./RequirementEditor"

// v0.2.4-B (T21): the editor is the V1 natural-language form. The deleted
// SUT/page/API target selection is gone; the assertions below cover the V1
// fields (user_story / rules / structured AC / sut_description / scope_out /
// account_roles_hint) and the write DTO that omits server-assigned AC ids.

const updatedDetail: RequirementDetail = {
  ...requirementDetailFixture,
  revision: 2,
  current_version_id: "6102",
  current_version: {
    ...requirementVersionFixture,
    id: "6102",
    revision: 2,
    title: "退款到账时效（补充）",
  },
  history: [requirementVersionFixture],
}

function renderEditor(overrides: Partial<Parameters<typeof RequirementEditor>[0]> = {}) {
  const props = {
    versionId: "2001",
    requirement: requirementDetailFixture,
    onUpdated: vi.fn(),
    ...overrides,
  }
  return { props, ...render(<RequirementEditor {...props} />) }
}

describe("RequirementEditor", () => {
  beforeEach(() => {
    api.updateRequirement.mockReset()
    api.confirmRequirement.mockReset()
    api.reopenRequirement.mockReset()
    api.rollbackRequirement.mockReset()
    api.updateRequirement.mockResolvedValue(updatedDetail)
    api.confirmRequirement.mockResolvedValue({
      confirmed: true,
      requirement: confirmedRequirementDetailFixture,
    })
  })

  afterEach(cleanup)

  it("prefills the current V1 requirement version", () => {
    renderEditor()

    expect(screen.getByTestId("requirement-editor")).toHaveTextContent("退款到账时效")
    expect(screen.getByTestId("editor-title")).toHaveValue("退款到账时效")
    expect(screen.getByTestId("editor-story-as-a")).toHaveValue("买家")
    expect(screen.getByTestId("editor-story-i-want")).toHaveValue(
      "退款审核通过后 T+1 个工作日原路退回",
    )
    expect(screen.getByTestId("editor-story-so-that")).toHaveValue("我能及时收到退款")
    expect(screen.getByTestId("editor-rules")).toHaveValue("节假日顺延。")
    // The server-assigned AC id is shown read-only, never as an editable input.
    expect(screen.getByTestId("editor-ac-0-id")).toHaveTextContent("AC-1")
    expect(screen.getByTestId("editor-ac-0-given")).toHaveValue("退款审核通过")
    expect(screen.getByTestId("editor-ac-0-when")).toHaveValue("T+1 个工作日")
    expect(screen.getByTestId("editor-ac-0-then")).toHaveValue("金额与订单实付一致原路到账")
    expect(screen.getByTestId("editor-sut-description")).toHaveValue("支付网关退款链路")
    expect(screen.getByTestId("editor-scope-out")).toHaveValue("")
    expect(screen.getByTestId("editor-account-roles")).toHaveValue("buyer")
    expect(screen.getByTestId("editor-priority")).toHaveValue("P1")
  })

  it("saves the V1 form through PATCH, omitting server-assigned AC ids", async () => {
    const onUpdated = vi.fn()
    renderEditor({ onUpdated })

    fireEvent.change(screen.getByTestId("editor-title"), {
      target: { value: "退款到账时效（补充）" },
    })
    fireEvent.change(screen.getByTestId("editor-ac-0-then"), {
      target: { value: "金额与订单实付一致原路到账（更新）" },
    })
    fireEvent.click(screen.getByTestId("editor-save"))

    await waitFor(() => expect(api.updateRequirement).toHaveBeenCalledTimes(1))
    const [versionId, requirementId, payload] = api.updateRequirement.mock.calls[0]
    expect(versionId).toBe("2001")
    expect(requirementId).toBe("6001")
    expect(payload).toMatchObject({
      title: "退款到账时效（补充）",
      user_story: {
        as_a: "买家",
        i_want: "退款审核通过后 T+1 个工作日原路退回",
        so_that: "我能及时收到退款",
      },
      rules: "节假日顺延。",
      priority: "P1",
      sut_description: "支付网关退款链路",
      scope_out: "",
      account_roles_hint: ["buyer"],
    })
    // The write DTO is {given,when,then} only — toEqual is exact, so a leaked
    // server-assigned id (or local key) would fail this assertion.
    expect(payload.acceptance_criteria).toEqual([
      { given: "退款审核通过", when: "T+1 个工作日", then: "金额与订单实付一致原路到账（更新）" },
    ])
    expect(onUpdated).toHaveBeenCalledWith(updatedDetail)
    expect(await screen.findByRole("status")).toHaveTextContent("已保存为草案 v2")
  })

  it("adds and removes acceptance criteria, keeping ids read-only and out of the save body", async () => {
    renderEditor()

    expect(screen.getByTestId("editor-ac-0-id")).toHaveTextContent("AC-1")
    // A newly added row has no id until the server assigns one on save.
    fireEvent.click(screen.getByTestId("editor-ac-add"))
    expect(screen.getByTestId("editor-ac-1-id")).toHaveTextContent("新条目")
    fireEvent.change(screen.getByTestId("editor-ac-1-given"), { target: { value: "订单已支付" } })
    fireEvent.change(screen.getByTestId("editor-ac-1-when"), { target: { value: "申请退款" } })
    fireEvent.change(screen.getByTestId("editor-ac-1-then"), { target: { value: "退款单创建" } })

    // Removing the first row re-indexes the new one to position 0.
    fireEvent.click(screen.getByTestId("editor-ac-remove-0"))
    expect(screen.queryByTestId("editor-ac-1-given")).not.toBeInTheDocument()
    expect(screen.getByTestId("editor-ac-0-given")).toHaveValue("订单已支付")

    fireEvent.click(screen.getByTestId("editor-save"))
    await waitFor(() => expect(api.updateRequirement).toHaveBeenCalledTimes(1))
    const payload = api.updateRequirement.mock.calls[0][2]
    expect(payload.acceptance_criteria).toEqual([
      { given: "订单已支付", when: "申请退款", then: "退款单创建" },
    ])
  })

  it("trims prose fields in the save body to match the trimmed validation", async () => {
    renderEditor()

    fireEvent.change(screen.getByTestId("editor-title"), { target: { value: "  退款到账时效  " } })
    fireEvent.change(screen.getByTestId("editor-rules"), { target: { value: "  节假日顺延。  " } })
    fireEvent.change(screen.getByTestId("editor-scope-out"), {
      target: { value: "  不含跨境退款  " },
    })
    fireEvent.change(screen.getByTestId("editor-ac-0-given"), {
      target: { value: "  退款审核通过  " },
    })
    fireEvent.click(screen.getByTestId("editor-save"))

    await waitFor(() => expect(api.updateRequirement).toHaveBeenCalledTimes(1))
    const payload = api.updateRequirement.mock.calls[0][2]
    expect(payload.title).toBe("退款到账时效")
    expect(payload.rules).toBe("节假日顺延。")
    expect(payload.scope_out).toBe("不含跨境退款")
    expect(payload.acceptance_criteria).toEqual([
      { given: "退款审核通过", when: "T+1 个工作日", then: "金额与订单实付一致原路到账" },
    ])
  })

  it("validates required fields before calling the API", async () => {
    renderEditor()

    fireEvent.change(screen.getByTestId("editor-title"), { target: { value: "   " } })
    fireEvent.click(screen.getByTestId("editor-save"))

    expect(await screen.findByText("标题不能为空")).toBeInTheDocument()
    expect(api.updateRequirement).not.toHaveBeenCalled()
  })

  it("requires a complete acceptance criterion before saving", async () => {
    renderEditor()

    fireEvent.click(screen.getByTestId("editor-ac-add"))
    fireEvent.click(screen.getByTestId("editor-save"))

    expect(
      await screen.findByText("每条验收标准的 Given / When / Then 都不能为空"),
    ).toBeInTheDocument()
    expect(api.updateRequirement).not.toHaveBeenCalled()
  })

  it("confirms the requirement and freezes the draft", async () => {
    const onUpdated = vi.fn()
    renderEditor({ onUpdated })

    fireEvent.click(screen.getByTestId("editor-confirm"))

    await waitFor(() => expect(api.confirmRequirement).toHaveBeenCalledWith("2001", "6001"))
    expect(onUpdated).toHaveBeenCalledWith(confirmedRequirementDetailFixture)
    expect(await screen.findByRole("status")).toHaveTextContent("已确认")
  })

  it("makes a clarified requirement read-only and offers rollback instead of save", () => {
    renderEditor({
      requirement: {
        ...requirementDetailFixture,
        status: "clarified",
        confirmed_at: "2026-10-31T10:00:00Z",
      },
    })

    expect(screen.getByTestId("editor-rollback-hint")).toHaveTextContent(
      "该需求已澄清；编辑前需先回退为草案（会生成新 revision 并重新确认）。",
    )
    // C-06: a clarified requirement can only change through the audited
    // rollback path, so every V1 field is disabled and there is no save action.
    expect(screen.getByTestId("editor-title")).toBeDisabled()
    expect(screen.getByTestId("editor-story-as-a")).toBeDisabled()
    expect(screen.getByTestId("editor-story-i-want")).toBeDisabled()
    expect(screen.getByTestId("editor-story-so-that")).toBeDisabled()
    expect(screen.getByTestId("editor-rules")).toBeDisabled()
    expect(screen.getByTestId("editor-ac-0-given")).toBeDisabled()
    expect(screen.getByTestId("editor-ac-0-when")).toBeDisabled()
    expect(screen.getByTestId("editor-ac-0-then")).toBeDisabled()
    expect(screen.getByTestId("editor-sut-description")).toBeDisabled()
    expect(screen.getByTestId("editor-scope-out")).toBeDisabled()
    expect(screen.getByTestId("editor-account-roles")).toBeDisabled()
    expect(screen.getByTestId("editor-priority")).toBeDisabled()
    expect(screen.getByTestId("editor-ac-add")).toBeDisabled()
    expect(screen.getByTestId("editor-ac-remove-0")).toBeDisabled()
    expect(screen.queryByTestId("editor-save")).not.toBeInTheDocument()
    expect(screen.queryByTestId("editor-confirm")).not.toBeInTheDocument()
    expect(screen.getByTestId("editor-rollback")).toBeInTheDocument()
  })

  it("rolls a clarified requirement back to draft with a required reason", async () => {
    const onUpdated = vi.fn()
    const clarifiedDetail: RequirementDetail = {
      ...requirementDetailFixture,
      status: "clarified",
      confirmed_at: "2026-10-31T10:00:00Z",
    }
    const rolledBack: RequirementDetail = {
      ...clarifiedDetail,
      status: "draft",
      revision: 2,
      current_version_id: "6102",
      current_version: {
        ...requirementVersionFixture,
        id: "6102",
        revision: 2,
      },
      history: [requirementVersionFixture],
      confirmed_at: null,
    }
    api.rollbackRequirement.mockResolvedValue(rolledBack)
    renderEditor({ requirement: clarifiedDetail, onUpdated })

    fireEvent.click(screen.getByTestId("editor-rollback"))
    const dialog = await screen.findByRole("dialog", { name: "回退为草案" })
    const submit = within(dialog).getByRole("button", { name: "回退为草案" })
    // The reason is mandatory: no request before it is typed.
    expect(submit).toBeDisabled()
    expect(api.rollbackRequirement).not.toHaveBeenCalled()

    fireEvent.change(within(dialog).getByLabelText("回退原因"), {
      target: { value: "验收口径变更，需要重新澄清" },
    })
    fireEvent.click(submit)

    await waitFor(() =>
      expect(api.rollbackRequirement).toHaveBeenCalledWith("2001", "6001", {
        reason: "验收口径变更，需要重新澄清",
      }),
    )
    expect(api.updateRequirement).not.toHaveBeenCalled()
    expect(onUpdated).toHaveBeenCalledWith(rolledBack)
    expect(await screen.findByRole("status")).toHaveTextContent("已回退为草案 v2")
  })

  it("shows rollback API errors inline in the dialog", async () => {
    api.rollbackRequirement.mockRejectedValue(
      new ApiError({
        status: 409,
        code: "state_conflict",
        message: "需求状态已变化，请刷新",
      }),
    )
    renderEditor({
      requirement: { ...requirementDetailFixture, status: "clarified" },
    })

    fireEvent.click(screen.getByTestId("editor-rollback"))
    const dialog = await screen.findByRole("dialog", { name: "回退为草案" })
    fireEvent.change(within(dialog).getByLabelText("回退原因"), {
      target: { value: "重新澄清" },
    })
    fireEvent.click(within(dialog).getByRole("button", { name: "回退为草案" }))

    expect(await screen.findByText("需求状态已变化，请刷新")).toBeInTheDocument()
    expect(api.rollbackRequirement).toHaveBeenCalledTimes(1)
  })

  it("reopens a closed requirement with an explicit reason dialog", async () => {
    const onUpdated = vi.fn()
    const closedDetail: RequirementDetail = {
      ...requirementDetailFixture,
      status: "closed",
    }
    const reopened = { ...closedDetail, status: "draft" as const }
    api.reopenRequirement.mockResolvedValue(reopened)
    renderEditor({ requirement: closedDetail, onUpdated })

    expect(screen.getByTestId("editor-closed-hint")).toHaveTextContent(
      "该需求已闭环；重新打开后会回到草案",
    )
    expect(screen.queryByTestId("editor-confirm")).not.toBeInTheDocument()
    expect(screen.queryByTestId("editor-save")).not.toBeInTheDocument()

    fireEvent.click(screen.getByTestId("editor-reopen"))
    const dialog = await screen.findByRole("dialog", { name: "重新打开需求" })
    const submit = within(dialog).getByRole("button", { name: "重新打开需求" })
    expect(submit).toBeDisabled()

    fireEvent.change(within(dialog).getByLabelText("重开原因"), {
      target: { value: "缺陷修复后重测" },
    })
    fireEvent.click(submit)

    await waitFor(() =>
      expect(api.reopenRequirement).toHaveBeenCalledWith("2001", "6001", {
        reason: "缺陷修复后重测",
      }),
    )
    expect(onUpdated).toHaveBeenCalledWith(reopened)
    expect(await screen.findByRole("status")).toHaveTextContent("已重新打开为草案")
  })

  it("shows API validation errors inline", async () => {
    api.updateRequirement.mockRejectedValue(
      new ApiError({
        status: 400,
        code: "invalid_argument",
        message: "被测系统描述不能为空",
        details: { field: "sut_description" },
      }),
    )
    renderEditor()

    fireEvent.click(screen.getByTestId("editor-save"))

    expect(await screen.findByText("被测系统描述不能为空")).toBeInTheDocument()
  })
})
