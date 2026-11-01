import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { evidenceScreenshotFixture } from "@/features/execution/test-fixtures"
import { ApiError } from "@/lib/api-client"

const visualApi = vi.hoisted(() => ({
  listVisualAssertions: vi.fn(),
  listVisualBaselines: vi.fn(),
  approveVisualBaseline: vi.fn(),
  getVisualDiff: vi.fn(),
  getVisualMetrics: vi.fn(),
  upsertVisualAssertion: vi.fn(),
}))
vi.mock("../api", () => visualApi)

const executionApi = vi.hoisted(() => ({
  getEvidence: vi.fn(),
  downloadEvidence: vi.fn(),
}))
vi.mock("@/features/execution/api", () => executionApi)

import { BaselineApprovalDialog } from "../components/BaselineApprovalDialog"
import { VisualConfigEditor } from "../components/VisualConfigEditor"
import { VisualDiffViewer } from "../components/VisualDiffViewer"
import { QualityMetricsPanel } from "../QualityMetricsPanel"
import {
  visualAssertionFixture,
  visualBaselineFixture,
  visualDiffDetailFixture,
  visualDiffFailedFixture,
  visualMetricsEmptyFixture,
  visualMetricsFixture,
} from "../test-fixtures"

const assertionTarget = {
  step_seq: 1,
  assertion_seq: 2,
  expected: "订单总额",
  case_assertion_id: "6101",
}

function download(blob: Blob, filename: string) {
  return { blob, filename, contentType: blob.type }
}

describe("VisualConfigEditor", () => {
  beforeEach(() => {
    visualApi.upsertVisualAssertion.mockReset()
    visualApi.listVisualAssertions.mockReset()
  })

  afterEach(cleanup)

  it("prefills the active config and saves the normalized camelCase body", async () => {
    visualApi.upsertVisualAssertion.mockResolvedValue({
      ...visualAssertionFixture,
      config_revision: 2,
    })
    const onSaved = vi.fn()

    render(
      <VisualConfigEditor
        versionId="2001"
        caseVersionId="7002"
        caseAssertionId="6101"
        versionSutId="1002"
        assertion={assertionTarget}
        initial={visualAssertionFixture}
        onSaved={onSaved}
      />,
    )

    expect(screen.getByTestId("visual-config-editor")).toHaveTextContent("视觉断言 1.2")
    expect(screen.getByLabelText("Selector")).toHaveValue("#order-total")
    expect(screen.getByLabelText("阈值（%）")).toHaveValue(0.5)
    expect(screen.getByLabelText("视口宽")).toHaveValue(1280)
    expect(screen.getByLabelText("视口高")).toHaveValue(720)
    expect(screen.getByLabelText("设备像素比")).toHaveValue(1)

    fireEvent.change(screen.getByLabelText("阈值（%）"), { target: { value: "1.5" } })
    fireEvent.click(screen.getByRole("checkbox", { name: "启用区域裁剪" }))
    fireEvent.change(screen.getByLabelText("区域 X"), { target: { value: "0.1" } })
    fireEvent.change(screen.getByLabelText("区域 Y"), { target: { value: "0.2" } })
    fireEvent.change(screen.getByLabelText("区域宽"), { target: { value: "0.6" } })
    fireEvent.change(screen.getByLabelText("区域高"), { target: { value: "0.4" } })
    fireEvent.click(screen.getByRole("button", { name: "保存视觉配置" }))

    await waitFor(() =>
      expect(visualApi.upsertVisualAssertion).toHaveBeenCalledWith("2001", "6101", {
        versionSutId: "1002",
        selector: "#order-total",
        region: { x: 0.1, y: 0.2, width: 0.6, height: 0.4 },
        threshold: 0.015,
        viewport: { width: 1280, height: 720 },
        deviceScaleFactor: 1,
        waitPolicy: { network_idle: true, fonts_ready: true },
      }),
    )
    expect(onSaved).toHaveBeenCalledWith(expect.objectContaining({ config_revision: 2 }))
    expect(screen.getByRole("status")).toHaveTextContent("视觉配置已保存（revision 2）")
  })

  it("copies the matching assertion config from the previous case version", async () => {
    visualApi.listVisualAssertions.mockResolvedValue([
      {
        ...visualAssertionFixture,
        id: "7099",
        case_assertion_id: "6099",
        selector: "#legacy-total",
        threshold: 0.01,
        viewport_width: 1000,
        viewport_height: 600,
        device_scale_factor: 2,
        wait_policy: { stable_frames: 2 },
      },
    ])
    visualApi.upsertVisualAssertion.mockResolvedValue({
      ...visualAssertionFixture,
      config_revision: 3,
    })

    render(
      <VisualConfigEditor
        versionId="2001"
        caseVersionId="7002"
        previousCaseVersionId="7000"
        caseAssertionId="6101"
        versionSutId="1002"
        assertion={assertionTarget}
      />,
    )

    fireEvent.click(screen.getByRole("button", { name: "复制上一版本配置" }))

    await waitFor(() =>
      expect(visualApi.listVisualAssertions).toHaveBeenCalledWith("2001", {
        caseVersionId: "7000",
      }),
    )
    expect(await screen.findByRole("status")).toHaveTextContent("已复制上一版本配置，保存后生效")
    expect(screen.getByLabelText("Selector")).toHaveValue("#legacy-total")
    expect(screen.getByLabelText("阈值（%）")).toHaveValue(1)
    expect(screen.getByLabelText("视口宽")).toHaveValue(1000)
    expect(screen.getByLabelText("设备像素比")).toHaveValue(2)
  })

  it("rejects out-of-range values and invalid wait-policy JSON without calling the API", async () => {
    render(
      <VisualConfigEditor
        versionId="2001"
        caseVersionId="7002"
        caseAssertionId="6101"
        assertion={assertionTarget}
      />,
    )

    fireEvent.change(screen.getByLabelText("阈值（%）"), { target: { value: "20" } })
    fireEvent.change(screen.getByLabelText("视口宽"), { target: { value: "0" } })
    fireEvent.change(screen.getByLabelText("等待策略（JSON）"), { target: { value: "{oops" } })
    fireEvent.click(screen.getByRole("button", { name: "保存视觉配置" }))

    expect(await screen.findByText("阈值需在 0–10% 之间")).toBeInTheDocument()
    expect(screen.getByText("视口宽度需在 1–10000 之间")).toBeInTheDocument()
    expect(screen.getByText("等待策略必须是 JSON 对象")).toBeInTheDocument()
    expect(visualApi.upsertVisualAssertion).not.toHaveBeenCalled()
  })

  it("disables saving when the design assertion id is unknown", () => {
    render(
      <VisualConfigEditor
        versionId="2001"
        caseVersionId="7002"
        assertion={{ ...assertionTarget, case_assertion_id: undefined }}
      />,
    )

    expect(screen.getByTestId("visual-config-missing-id")).toHaveTextContent("无法确定设计断言 ID")
    expect(screen.getByRole("button", { name: "保存视觉配置" })).toBeDisabled()
  })
})

describe("BaselineApprovalDialog", () => {
  beforeEach(() => {
    visualApi.approveVisualBaseline.mockReset()
    visualApi.listVisualBaselines.mockReset()
    executionApi.getEvidence.mockReset()
  })

  afterEach(cleanup)

  it("lists candidate actual evidence and approves the selected baseline", async () => {
    executionApi.getEvidence.mockImplementation((_versionId: string, id: string) =>
      Promise.resolve({
        ...evidenceScreenshotFixture,
        id,
        kind: "visual_actual",
        filename: `actual-${id}.png`,
      }),
    )
    visualApi.approveVisualBaseline.mockResolvedValue(visualBaselineFixture)
    const onApproved = vi.fn()
    const onClose = vi.fn()

    render(
      <BaselineApprovalDialog
        versionId="2001"
        assertion={visualAssertionFixture}
        evidenceIds={["9401", "9402"]}
        open
        onClose={onClose}
        onApproved={onApproved}
      />,
    )

    expect(await screen.findByText("证据 #9401")).toBeInTheDocument()
    expect(screen.getByText("证据 #9402")).toBeInTheDocument()
    expect(screen.getByLabelText("证据 #9402")).not.toBeChecked()

    fireEvent.click(screen.getByLabelText("证据 #9402"))
    fireEvent.click(screen.getByRole("button", { name: "确认批准" }))

    await waitFor(() =>
      expect(visualApi.approveVisualBaseline).toHaveBeenCalledWith("2001", "7101", "9402"),
    )
    expect(onApproved).toHaveBeenCalledWith(visualBaselineFixture)
    expect(onClose).toHaveBeenCalled()
  })

  it("surfaces the server rejection inside the dialog", async () => {
    executionApi.getEvidence.mockResolvedValue({
      ...evidenceScreenshotFixture,
      kind: "visual_actual",
    })
    visualApi.approveVisualBaseline.mockRejectedValue(
      new ApiError({
        status: 422,
        code: "baseline_evidence_invalid",
        message: "证据尺寸与视口不一致",
      }),
    )

    render(
      <BaselineApprovalDialog
        versionId="2001"
        assertion={visualAssertionFixture}
        evidenceIds={["9401"]}
        open
        onClose={vi.fn()}
        onApproved={vi.fn()}
      />,
    )

    await screen.findByText("证据 #9401")
    fireEvent.click(screen.getByRole("button", { name: "确认批准" }))

    expect(await screen.findByText("证据尺寸与视口不一致")).toBeInTheDocument()
  })
})

describe("VisualDiffViewer", () => {
  const createObjectURL = vi.fn<(blob: Blob) => string>()
  const revokeObjectURL = vi.fn<(url: string) => void>()

  beforeEach(() => {
    visualApi.getVisualDiff.mockReset()
    executionApi.downloadEvidence.mockReset()
    createObjectURL.mockReset()
    revokeObjectURL.mockReset()
    createObjectURL
      .mockReturnValueOnce("blob:actual")
      .mockReturnValueOnce("blob:diff")
      .mockReturnValue("blob:other")
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
    executionApi.downloadEvidence.mockResolvedValue(
      download(new Blob(["png"], { type: "image/png" }), "visual.png"),
    )
  })

  afterEach(cleanup)

  it("loads the diff detail and renders actual/diff images with ratio and boxes", async () => {
    visualApi.getVisualDiff.mockResolvedValue(visualDiffDetailFixture)
    executionApi.downloadEvidence.mockResolvedValue(
      download(new Blob(["png"], { type: "image/png" }), "visual.png"),
    )

    render(
      <VisualDiffViewer versionId="2001" diff={visualDiffFailedFixture} open onClose={vi.fn()} />,
    )

    expect(await screen.findByTestId("visual-actual-image")).toHaveAttribute("src", "blob:actual")
    expect(screen.getByTestId("visual-diff-image")).toHaveAttribute("src", "blob:diff")
    expect(visualApi.getVisualDiff).toHaveBeenCalledWith("2001", "7302")
    expect(executionApi.downloadEvidence).toHaveBeenCalledWith("2001", "9401")
    expect(executionApi.downloadEvidence).toHaveBeenCalledWith("2001", "9402")
    expect(screen.getByText("差异比例")).toBeInTheDocument()
    expect(screen.getByText("1.2%")).toBeInTheDocument()
    expect(screen.getByText("不匹配区域 2 块")).toBeInTheDocument()
    expect(screen.getByText("1280×720 @1x")).toBeInTheDocument()
  })

  it("shows a retryable error when the detail request fails", async () => {
    visualApi.getVisualDiff
      .mockRejectedValueOnce(
        new ApiError({ status: 404, code: "not_found", message: "比对不存在" }),
      )
      .mockResolvedValueOnce(visualDiffDetailFixture)

    render(
      <VisualDiffViewer versionId="2001" diff={visualDiffFailedFixture} open onClose={vi.fn()} />,
    )

    expect(await screen.findByText("比对不存在")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(await screen.findByTestId("visual-actual-image")).toBeInTheDocument()
  })
})

describe("QualityMetricsPanel", () => {
  beforeEach(() => {
    visualApi.getVisualMetrics.mockReset()
  })

  afterEach(cleanup)

  it("renders the four-state totals, coverage, pass rate and latency", async () => {
    visualApi.getVisualMetrics.mockResolvedValue(visualMetricsFixture)

    render(<QualityMetricsPanel versionId="2001" />)

    expect(await screen.findByTestId("quality-metrics-panel")).toHaveTextContent("断言总数2")
    expect(screen.getByText("基线覆盖率")).toBeInTheDocument()
    expect(screen.getByText("50%")).toBeInTheDocument()
    expect(screen.getByText("通过率")).toBeInTheDocument()
    expect(screen.getByText("75%")).toBeInTheDocument()
    expect(screen.getByText("未定率")).toBeInTheDocument()
    expect(screen.getByText("20%")).toBeInTheDocument()
    expect(screen.getByText("p50 120 ms · p95 340 ms")).toBeInTheDocument()
    expect(visualApi.getVisualMetrics).toHaveBeenCalledWith("2001")
  })

  it("shows the empty state when no visual assertion exists", async () => {
    visualApi.getVisualMetrics.mockResolvedValue(visualMetricsEmptyFixture)

    render(<QualityMetricsPanel versionId="2001" />)

    expect(await screen.findByText("暂无视觉数据")).toBeInTheDocument()
  })

  it("retries a failed metrics request", async () => {
    visualApi.getVisualMetrics
      .mockRejectedValueOnce(
        new ApiError({ status: 500, code: "internal", message: "指标暂不可用", retryable: true }),
      )
      .mockResolvedValueOnce(visualMetricsFixture)

    render(<QualityMetricsPanel versionId="2001" />)

    expect(await screen.findByText("指标暂不可用")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(await screen.findByTestId("quality-metrics-panel")).toHaveTextContent("断言总数2")
  })
})
