import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
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

  it("shows the rollback hint for a clarified requirement and hides confirm", () => {
    renderEditor({
      requirement: {
        ...requirementDetailFixture,
        status: "clarified",
        confirmed_at: "2026-10-31T10:00:00Z",
      },
    })

    expect(screen.getByTestId("editor-rollback-hint")).toHaveTextContent(
      "该需求已澄清；如需修改，请先确认回退为草案。",
    )
    expect(screen.queryByTestId("editor-confirm")).not.toBeInTheDocument()
    expect(screen.getByTestId("editor-save")).toBeInTheDocument()
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
