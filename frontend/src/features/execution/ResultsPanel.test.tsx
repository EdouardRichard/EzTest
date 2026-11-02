import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

const api = vi.hoisted(() => ({
  createIntent: vi.fn(),
  downloadEvidence: vi.fn(),
}))
vi.mock("./api", () => api)

import { ResultsPanel } from "./ResultsPanel"
import {
  caseRunDetailFixture,
  caseRunsFixture,
  intentFixture,
  passedCaseRunFixture,
  runningCaseRunFixture,
} from "./test-fixtures"
import type { CaseRun, CaseRunDetail } from "./types"

type PanelOverrides = {
  caseRuns?: CaseRun[]
  selectedCaseRunId?: string | null
  detail?: CaseRunDetail | null
  detailLoading?: boolean
  detailError?: unknown
  onSelect?: (id: string) => void
  onPause?: (caseRun: CaseRun) => void
  onResume?: (caseRun: CaseRun) => void
  onRerunComplete?: () => void
  onRetryDetail?: () => void
}

function renderPanel(overrides: PanelOverrides = {}) {
  const onSelect = overrides.onSelect ?? vi.fn()
  const onPause = overrides.onPause ?? vi.fn()
  const onResume = overrides.onResume ?? vi.fn()
  const onRerunComplete = overrides.onRerunComplete ?? vi.fn()
  const onRetryDetail = overrides.onRetryDetail ?? vi.fn()
  render(
    <ResultsPanel
      versionId="2001"
      caseRuns={overrides.caseRuns ?? caseRunsFixture}
      selectedCaseRunId={overrides.selectedCaseRunId ?? null}
      onSelect={onSelect}
      onPause={onPause}
      onResume={onResume}
      onRerunComplete={onRerunComplete}
      detail={overrides.detail ?? null}
      detailLoading={overrides.detailLoading ?? false}
      detailError={overrides.detailError ?? null}
      onRetryDetail={onRetryDetail}
    />,
  )
  return { onSelect, onPause, onResume, onRerunComplete, onRetryDetail }
}

function row(testId: string): HTMLElement {
  return screen.getByTestId(testId)
}

describe("ResultsPanel", () => {
  beforeEach(() => {
    api.createIntent.mockReset()
    api.downloadEvidence.mockReset()
    api.createIntent.mockResolvedValue(intentFixture)
  })

  afterEach(cleanup)

  it("pins failed and blocked case runs above the other results", () => {
    renderPanel()

    const rows = screen.getAllByTestId(/^case-run-row-/)
    expect(rows.map((element) => element.getAttribute("data-case-run-id"))).toEqual([
      "9302",
      "9303",
      "9301",
      "9304",
    ])
  })

  it("renders the verdict, failure class, assertion progress, evidence and duration", () => {
    renderPanel()

    const failed = row("case-run-row-9302")
    expect(within(failed).getByText("失败")).toBeInTheDocument()
    expect(within(failed).getByText("应用缺陷")).toBeInTheDocument()
    expect(within(failed).getByText("断言 1/2")).toBeInTheDocument()
    expect(within(failed).getByText("证据 3")).toBeInTheDocument()
    expect(within(failed).getByText("41.0s")).toBeInTheDocument()
    expect(within(failed).getByText("SUT 1001")).toBeInTheDocument()
    expect(within(failed).getByText("SUT 1002")).toBeInTheDocument()

    const passed = row("case-run-row-9301")
    expect(within(passed).getByText("通过")).toBeInTheDocument()
    expect(within(passed).getByText("断言 2/2")).toBeInTheDocument()
  })

  it("shows the four-state verdict badges", () => {
    const inconclusive: CaseRun = {
      ...passedCaseRunFixture,
      id: "9305",
      title: "超时用例",
      status: "inconclusive",
      failure_class: "",
    }
    renderPanel({ caseRuns: [passedCaseRunFixture, inconclusive] })

    expect(row("case-run-row-9305")).toHaveTextContent("未定")
    expect(row("case-run-row-9301")).toHaveTextContent("通过")
  })

  it("expands the selected case run into steps, assertions and evidence", () => {
    const onSelect = vi.fn()
    renderPanel({ selectedCaseRunId: "9302", detail: caseRunDetailFixture, onSelect })

    const failed = row("case-run-row-9302")
    expect(within(failed).getByText("收起")).toBeInTheDocument()
    expect(screen.getByText("步骤 1")).toBeInTheDocument()
    expect(screen.getByText("打开订单详情页")).toBeInTheDocument()
    expect(screen.getByText("¥5,000.00")).toBeInTheDocument()
    expect(screen.getByText("¥5,200.00")).toBeInTheDocument()
    expect(screen.getByText(/退款金额与实付金额不一致/)).toBeInTheDocument()
    expect(screen.getByText(/step-3\.png/)).toBeInTheDocument()
    expect(screen.getByText(/console\.log/)).toBeInTheDocument()

    fireEvent.click(within(failed).getByRole("button", { name: /收起/ }))
    expect(onSelect).toHaveBeenCalledWith("9302")
  })

  it("shows a loading state for the expanded detail", () => {
    renderPanel({ selectedCaseRunId: "9302", detailLoading: true })
    expect(screen.getByText("正在加载用例详情…")).toBeInTheDocument()
  })

  it("offers retest for terminal rows and calls createIntent with purpose=retest", async () => {
    const onRerunComplete = vi.fn()
    renderPanel({ onRerunComplete })

    const failed = row("case-run-row-9302")
    fireEvent.click(within(failed).getByRole("button", { name: "重跑" }))

    expect(within(failed).getByText("确认重跑？")).toBeInTheDocument()
    fireEvent.click(within(failed).getByRole("button", { name: "确认重跑" }))

    await waitFor(() => {
      expect(api.createIntent).toHaveBeenCalledWith("2001", {
        case_version_id: "7004",
        purpose: "retest",
      })
    })
    expect(onRerunComplete).toHaveBeenCalledTimes(1)
  })

  it("shows retryable detail errors and pause/resume actions", () => {
    const onRetryDetail = vi.fn()
    const onPause = vi.fn()
    const onResume = vi.fn()
    renderPanel({
      caseRuns: [runningCaseRunFixture, { ...passedCaseRunFixture, status: "paused" }],
      selectedCaseRunId: "9304",
      detailError: new ApiError({
        status: 500,
        code: "internal",
        message: "用例详情暂不可用",
        requestId: "req_detail",
      }),
      onRetryDetail,
      onPause,
      onResume,
    })

    expect(screen.getByText("用例详情暂不可用")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(onRetryDetail).toHaveBeenCalledTimes(1)

    fireEvent.click(within(row("case-run-row-9304")).getByRole("button", { name: "暂停" }))
    expect(onPause).toHaveBeenCalledWith(expect.objectContaining({ id: "9304" }))

    fireEvent.click(within(row("case-run-row-9301")).getByRole("button", { name: "恢复" }))
    expect(onResume).toHaveBeenCalledWith(expect.objectContaining({ status: "paused" }))
  })

  it("shows the empty state when the wave has no case runs", () => {
    renderPanel({ caseRuns: [] })

    expect(screen.getByText("暂无执行结果")).toBeInTheDocument()
    expect(screen.queryAllByTestId(/^case-run-row-/)).toHaveLength(0)
  })
})
