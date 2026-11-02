import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { evidenceScreenshotFixture } from "@/features/execution/test-fixtures"
import { ApiError } from "@/lib/api-client"
import { type AuthUser, clearSession, setSession } from "@/lib/auth-storage"

const visualApi = vi.hoisted(() => ({
  getVisualOverview: vi.fn(),
  listVisualAssertions: vi.fn(),
  listVisualDiffs: vi.fn(),
  getVisualDiff: vi.fn(),
  approveVisualBaseline: vi.fn(),
  getVisualMetrics: vi.fn(),
  upsertVisualAssertion: vi.fn(),
}))
vi.mock("../api", () => visualApi)

const executionApi = vi.hoisted(() => ({
  listWaves: vi.fn(),
  listCaseRuns: vi.fn(),
  createIntent: vi.fn(),
  getEvidence: vi.fn(),
  downloadEvidence: vi.fn(),
}))
vi.mock("@/features/execution/api", () => executionApi)

vi.mock("@/features/clarification/useVersionEvents", () => ({
  useVersionEvents: () => "live",
}))

import { passedCaseRunFixture, runningWaveFixture } from "@/features/execution/test-fixtures"
import {
  visualAssertionFixture,
  visualAssertionWithoutBaselineFixture,
  visualBaselineFixture,
  visualDiffDetailFixture,
  visualDiffFailedFixture,
  visualDiffInconclusiveFixture,
  visualOverviewFixture,
} from "../test-fixtures"
import { VisualView } from "../VisualView"

const ADMIN_USER: AuthUser = {
  id: "9001",
  username: "admin",
  display_name: "管理员",
  role: "admin",
  status: "active",
}

const createObjectURL = vi.fn<(blob: Blob) => string>()
const revokeObjectURL = vi.fn<(url: string) => void>()

function mockWorkspace() {
  visualApi.getVisualOverview.mockResolvedValue(visualOverviewFixture)
  visualApi.listVisualAssertions.mockResolvedValue([visualAssertionFixture])
  visualApi.listVisualDiffs.mockResolvedValue([visualDiffFailedFixture])
  visualApi.getVisualDiff.mockResolvedValue(visualDiffDetailFixture)
  visualApi.approveVisualBaseline.mockResolvedValue(visualBaselineFixture)
  executionApi.listWaves.mockResolvedValue([runningWaveFixture])
  executionApi.listCaseRuns.mockResolvedValue([passedCaseRunFixture])
  executionApi.createIntent.mockResolvedValue({ id: "9101" })
  executionApi.getEvidence.mockImplementation((_versionId: string, id: string) =>
    Promise.resolve({
      ...evidenceScreenshotFixture,
      id,
      kind: "visual_actual",
      filename: `actual-${id}.png`,
    }),
  )
  executionApi.downloadEvidence.mockResolvedValue({
    blob: new Blob(["png"], { type: "image/png" }),
    filename: "actual.png",
    contentType: "image/png",
  })
}

describe("VisualView", () => {
  beforeEach(() => {
    window.sessionStorage.clear()
    setSession("ezt_admin", ADMIN_USER)
    for (const fn of Object.values(visualApi)) fn.mockReset()
    for (const fn of Object.values(executionApi)) fn.mockReset()
    mockWorkspace()
    createObjectURL.mockReset()
    revokeObjectURL.mockReset()
    createObjectURL.mockReturnValue("blob:visual")
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
  })

  afterEach(() => {
    cleanup()
    clearSession()
  })

  it("renders the wave, four-state comparison, baseline and overview", async () => {
    render(<VisualView versionId="2001" />)

    expect(await screen.findByTestId("visual-view")).toBeInTheDocument()
    expect(screen.getByRole("heading", { name: "视觉断言" })).toBeInTheDocument()
    expect(await screen.findByText("退款 T+1 到账")).toBeInTheDocument()
    expect(visualApi.listVisualAssertions).toHaveBeenCalledWith("2001", {})
    expect(visualApi.listVisualDiffs).toHaveBeenCalledWith("2001", { limit: 100 })
    expect(executionApi.listCaseRuns).toHaveBeenCalledWith("2001", "9201")

    expect(screen.getByText("基线覆盖 1/2")).toBeInTheDocument()
    expect(screen.getByText("基线已批准")).toBeInTheDocument()
    expect(screen.getByText("失败")).toBeInTheDocument()
    expect(screen.getByText("像素差异超过阈值")).toBeInTheDocument()
    expect(screen.getByText("1.2%")).toBeInTheDocument()
    expect(screen.getByRole("link", { name: /质量指标/ })).toHaveAttribute(
      "href",
      "/quality?version=2001",
    )
  })

  it("opens the diff viewer and approves the actual evidence as a baseline", async () => {
    render(<VisualView versionId="2001" />)
    await screen.findByText("退款 T+1 到账")

    fireEvent.click(screen.getByRole("button", { name: "查看差异" }))
    expect(await screen.findByTestId("visual-actual-image")).toBeInTheDocument()
    expect(visualApi.getVisualDiff).toHaveBeenCalledWith("2001", "7302")
    fireEvent.click(screen.getByRole("button", { name: "关闭" }))

    fireEvent.click(screen.getByRole("button", { name: "批准基线" }))
    expect(await screen.findByText("证据 #9401")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "确认批准" }))

    await waitFor(() =>
      expect(visualApi.approveVisualBaseline).toHaveBeenCalledWith("2001", "7101", "9401"),
    )
    await waitFor(() => expect(visualApi.listVisualAssertions).toHaveBeenCalledTimes(2))
  })

  it("hides baseline approval from a non-admin session", async () => {
    clearSession()
    render(<VisualView versionId="2001" />)
    await screen.findByText("退款 T+1 到账")

    expect(screen.queryByRole("button", { name: "批准基线" })).not.toBeInTheDocument()
  })

  it("prompts to approve a baseline when the comparison is inconclusive", async () => {
    visualApi.listVisualAssertions.mockResolvedValue([visualAssertionWithoutBaselineFixture])
    visualApi.listVisualDiffs.mockResolvedValue([
      { ...visualDiffInconclusiveFixture, visual_assertion_id: "7102" },
    ])
    executionApi.listCaseRuns.mockResolvedValue([passedCaseRunFixture])

    render(<VisualView versionId="2001" />)

    expect(await screen.findByText("缺失视觉基线")).toBeInTheDocument()
    expect(screen.getByText("缺失基线")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "批准基线" })).toBeInTheDocument()
  })

  it("creates a retest intent from a case-run row", async () => {
    render(<VisualView versionId="2001" />)
    await screen.findByText("退款 T+1 到账")

    fireEvent.click(screen.getByRole("button", { name: "复测" }))

    await waitFor(() =>
      expect(executionApi.createIntent).toHaveBeenCalledWith("2001", {
        case_version_id: "7002",
        purpose: "retest",
      }),
    )
    expect(await screen.findByRole("status")).toHaveTextContent("已创建复测意图")
  })

  it("shows the empty state when no visual assertion is configured", async () => {
    visualApi.listVisualAssertions.mockResolvedValue([])
    visualApi.listVisualDiffs.mockResolvedValue([])

    render(<VisualView versionId="2001" />)

    expect(await screen.findByText("暂无视觉断言")).toBeInTheDocument()
    expect(screen.getByText(/确认带视觉断言的用例并执行后/)).toBeInTheDocument()
  })

  it("shows a retryable error state when the visual projection fails", async () => {
    visualApi.getVisualOverview
      .mockRejectedValueOnce(
        new ApiError({ status: 500, code: "internal", message: "视觉概览不可用", retryable: true }),
      )
      .mockResolvedValueOnce(visualOverviewFixture)

    render(<VisualView versionId="2001" />)

    expect(await screen.findByText("视觉概览不可用")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(await screen.findByText("退款 T+1 到账")).toBeInTheDocument()
  })

  it("shows a loading state while the visual projection loads", () => {
    visualApi.getVisualOverview.mockReturnValue(new Promise(() => {}))
    visualApi.listVisualAssertions.mockReturnValue(new Promise(() => {}))
    visualApi.listVisualDiffs.mockReturnValue(new Promise(() => {}))

    render(<VisualView versionId="2001" />)

    expect(screen.getByText("正在加载视觉工作区…")).toBeInTheDocument()
  })
})
