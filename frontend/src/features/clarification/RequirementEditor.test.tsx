import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

import {
  confirmedRequirementDetailFixture,
  requirementDetailFixture,
  requirementVersionFixture,
} from "./test-fixtures"
import type { RequirementDetail, TargetOptions } from "./types"

const api = vi.hoisted(() => ({
  updateRequirement: vi.fn(),
  confirmRequirement: vi.fn(),
  reopenRequirement: vi.fn(),
  rollbackRequirement: vi.fn(),
  getAttachmentPendingDetails: vi.fn(),
}))
vi.mock("./api", () => api)

import { RequirementEditor } from "./RequirementEditor"

const TARGET_OPTIONS: TargetOptions = {
  suts: [
    { id: "1001", label: "订单中心" },
    { id: "1002", label: "支付网关" },
  ],
  pages: [
    { id: "1301", label: "订单列表" },
    { id: "2301", label: "退款详情" },
  ],
  apis: [{ id: "2401", label: "POST /api/payments" }],
}

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
    targetOptions: TARGET_OPTIONS,
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

  it("prefills the current requirement version and its targets", () => {
    renderEditor()

    expect(screen.getByTestId("requirement-editor")).toHaveTextContent("退款到账时效")
    expect(screen.getByTestId("editor-title")).toHaveValue("退款到账时效")
    expect(screen.getByTestId("editor-description")).toHaveValue(
      "退款审核通过后 T+1 个工作日原路退回；节假日顺延。",
    )
    expect(screen.getByTestId("editor-acceptance")).toHaveValue(
      "审核通过后 T+1 个工作日到账；金额与订单实付一致。",
    )
    expect(screen.getByTestId("editor-priority")).toHaveValue("P1")
    expect(screen.getByTestId("editor-target-sut-1002")).toBeChecked()
    expect(screen.getByTestId("editor-target-page-2301")).toBeChecked()
    expect(screen.getByTestId("editor-target-page-1301")).not.toBeChecked()
  })

  it("saves through PATCH and reports the new draft revision", async () => {
    const onUpdated = vi.fn()
    renderEditor({ onUpdated })

    fireEvent.change(screen.getByTestId("editor-title"), {
      target: { value: "退款到账时效（补充）" },
    })
    fireEvent.click(screen.getByTestId("editor-target-page-1301"))
    fireEvent.click(screen.getByTestId("editor-save"))

    await waitFor(() => expect(api.updateRequirement).toHaveBeenCalledTimes(1))
    const [versionId, requirementId, payload] = api.updateRequirement.mock.calls[0]
    expect(versionId).toBe("2001")
    expect(requirementId).toBe("6001")
    expect(payload).toMatchObject({
      title: "退款到账时效（补充）",
      description: "退款审核通过后 T+1 个工作日原路退回；节假日顺延。",
      acceptance_criteria: "审核通过后 T+1 个工作日到账；金额与订单实付一致。",
      priority: "P1",
    })
    expect(payload.targets).toEqual({
      version_suts: ["1002"],
      pages: ["2301", "1301"],
      apis: [],
    })
    expect(onUpdated).toHaveBeenCalledWith(updatedDetail)
    expect(await screen.findByRole("status")).toHaveTextContent("已保存为草案 v2")
  })

  it("validates required fields before calling the API", async () => {
    renderEditor()

    fireEvent.change(screen.getByTestId("editor-title"), { target: { value: "   " } })
    fireEvent.click(screen.getByTestId("editor-save"))

    expect(await screen.findByText("标题不能为空")).toBeInTheDocument()
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
    // rollback path, so every field is disabled and there is no save action.
    expect(screen.getByTestId("editor-title")).toBeDisabled()
    expect(screen.getByTestId("editor-description")).toBeDisabled()
    expect(screen.getByTestId("editor-acceptance")).toBeDisabled()
    expect(screen.getByTestId("editor-priority")).toBeDisabled()
    expect(screen.getByTestId("editor-target-sut-1002")).toBeDisabled()
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
        message: "目标页面不属于该版本",
        details: { field: "targets.pages" },
      }),
    )
    renderEditor()

    fireEvent.click(screen.getByTestId("editor-save"))

    expect(await screen.findByText("目标页面不属于该版本")).toBeInTheDocument()
  })
})
