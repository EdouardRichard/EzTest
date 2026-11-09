import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

const designApi = vi.hoisted(() => ({ getDesignCase: vi.fn() }))
vi.mock("./api", () => designApi)

const visualApi = vi.hoisted(() => ({
  listVisualAssertions: vi.fn(),
  upsertVisualAssertion: vi.fn(),
  listVisualBaselines: vi.fn(),
  approveVisualBaseline: vi.fn(),
  getVisualDiff: vi.fn(),
  getVisualMetrics: vi.fn(),
}))
vi.mock("@/features/visual/api", () => visualApi)

import { visualAssertionFixture } from "@/features/visual/test-fixtures"
import { CaseDetailPanel } from "./CaseDetailPanel"
import { designCaseDetailFixture } from "./test-fixtures"

function visualDetail(options: { caseAssertionId?: string } = {}) {
  return {
    ...designCaseDetailFixture,
    selected: {
      ...designCaseDetailFixture.selected,
      assertions: [
        ...designCaseDetailFixture.selected.assertions,
        {
          step_seq: 2,
          kind: "visual" as const,
          target: "#order-total",
          expected: "订单总额像素一致",
          target_version_sut_id: "1002",
          target_api_id: null,
          database_id: null,
          ...(options.caseAssertionId !== undefined
            ? { case_assertion_id: options.caseAssertionId }
            : {}),
        },
      ],
    },
  }
}

function renderPanel() {
  render(
    <CaseDetailPanel
      versionId="2001"
      caseId="7001"
      onConfirm={vi.fn()}
      onRequestChange={vi.fn()}
    />,
  )
}

describe("CaseDetailPanel visual config and anchors", () => {
  beforeEach(() => {
    designApi.getDesignCase.mockReset()
    for (const fn of Object.values(visualApi)) fn.mockReset()
    visualApi.upsertVisualAssertion.mockResolvedValue({
      ...visualAssertionFixture,
      config_revision: 2,
    })
    visualApi.listVisualAssertions.mockResolvedValue([
      {
        ...visualAssertionFixture,
        step_seq: 2,
        assertion_seq: 1,
        case_assertion_id: "6101",
      },
    ])
  })

  afterEach(cleanup)

  it("offers a request-AI empty state when the case has no visual assertion (D-08)", async () => {
    designApi.getDesignCase.mockResolvedValue(designCaseDetailFixture)
    const onRequestChange = vi.fn()

    render(
      <CaseDetailPanel
        versionId="2001"
        caseId="7001"
        onConfirm={vi.fn()}
        onRequestChange={onRequestChange}
      />,
    )

    // The section stays discoverable instead of disappearing: the user learns
    // why there is no editor and how to get a visual assertion generated.
    expect(await screen.findByText("视觉断言配置")).toBeInTheDocument()
    expect(screen.getByTestId("visual-assertions-empty")).toHaveTextContent(/该用例暂无视觉断言/)
    expect(screen.getByTestId("visual-assertions-empty")).toHaveTextContent(/由设计生成/)
    // Nothing to configure, so the visual config API is not called.
    expect(visualApi.listVisualAssertions).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole("button", { name: "请求 AI 添加视觉断言" }))

    expect(onRequestChange).toHaveBeenCalledTimes(1)
    const draft = onRequestChange.mock.calls[0][0] as string
    expect(draft).toContain("退款 T+1 到账")
    expect(draft).toContain("视觉基线断言")
  })

  it("maps an existing config by step/assertion and saves it from the case detail", async () => {
    designApi.getDesignCase.mockResolvedValue(visualDetail())

    renderPanel()

    expect(await screen.findByText("视觉断言配置")).toBeInTheDocument()
    await waitFor(() =>
      expect(visualApi.listVisualAssertions).toHaveBeenCalledWith("2001", {
        caseVersionId: "7002",
      }),
    )
    expect(screen.getByTestId("anchor-panel-toggle")).toHaveTextContent("用例代码锚点")

    fireEvent.click(screen.getByRole("button", { name: "配置视觉断言" }))
    expect(screen.getByTestId("visual-config-editor")).toHaveTextContent("视觉断言 2.1")
    fireEvent.click(screen.getByRole("button", { name: "保存视觉配置" }))

    await waitFor(() =>
      expect(visualApi.upsertVisualAssertion).toHaveBeenCalledWith(
        "2001",
        "6101",
        expect.objectContaining({ selector: "#order-total" }),
      ),
    )
  })

  it("uses the design assertion id when the API exposes it", async () => {
    designApi.getDesignCase.mockResolvedValue(visualDetail({ caseAssertionId: "6199" }))
    visualApi.listVisualAssertions.mockResolvedValue([])

    renderPanel()

    await screen.findByText("视觉断言配置")
    fireEvent.click(screen.getByRole("button", { name: "配置视觉断言" }))

    expect(screen.queryByTestId("visual-config-missing-id")).not.toBeInTheDocument()
    expect(screen.getByRole("button", { name: "保存视觉配置" })).toBeEnabled()
  })

  it("saves an empty region as null so an unchanged config never supersedes the baseline", async () => {
    designApi.getDesignCase.mockResolvedValue(visualDetail({ caseAssertionId: "6199" }))
    // The API renders an absent region as `{}`; the editor must not turn that
    // into a full-image crop on save (which would bump config_revision and
    // supersede an approved baseline without a real change).
    visualApi.listVisualAssertions.mockResolvedValue([
      {
        ...visualAssertionFixture,
        step_seq: 2,
        assertion_seq: 1,
        region: {},
      },
    ])

    renderPanel()

    await screen.findByText("视觉断言配置")
    fireEvent.click(screen.getByRole("button", { name: "配置视觉断言" }))
    fireEvent.click(screen.getByRole("button", { name: "保存视觉配置" }))

    await waitFor(() =>
      expect(visualApi.upsertVisualAssertion).toHaveBeenCalledWith(
        "2001",
        "6199",
        expect.objectContaining({ region: null }),
      ),
    )
  })

  it("stays readable when neither the design API nor a config exposes the assertion id", async () => {
    designApi.getDesignCase.mockResolvedValue(visualDetail())
    visualApi.listVisualAssertions.mockResolvedValue([])

    renderPanel()

    await screen.findByText("视觉断言配置")
    fireEvent.click(screen.getByRole("button", { name: "配置视觉断言" }))

    expect(screen.getByTestId("visual-config-missing-id")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "保存视觉配置" })).toBeDisabled()
  })
})
