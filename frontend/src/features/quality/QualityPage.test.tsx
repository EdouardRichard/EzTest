import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { ApiError } from "@/lib/api-client"

import type { EvalRun } from "./types"

const mocks = vi.hoisted(() => ({
  listEvalRuns: vi.fn(),
  useAuth: vi.fn(),
}))

vi.mock("@/features/auth/AuthProvider", () => ({
  useAuth: mocks.useAuth,
}))

vi.mock("./api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./api")>()
  return { ...actual, listEvalRuns: mocks.listEvalRuns }
})

import { QualityPage } from "./QualityPage"
import { formatMetricPercent, formatRunTime, modeLabel, runStatusBadge } from "./QualitySummary"

const RUN_ID = "1844674407370955161"

function evalRun(overrides: Partial<EvalRun> = {}): EvalRun {
  return {
    id: RUN_ID,
    mode: "deterministic",
    corpusVersion: "2026-10-29.1",
    model: "fake-llm",
    status: "failed",
    summary: { passed: false },
    startedAt: "2026-10-29T12:00:00Z",
    finishedAt: "2026-10-29T12:01:30Z",
    metrics: [
      {
        name: "design_coverage",
        value: 0.8,
        threshold: 0.9,
        passed: false,
        detail: { numerator: 4, denominator: 5, baseline: 0.85, reason: "below_threshold" },
      },
      {
        name: "classification_accuracy",
        value: 1,
        threshold: 1,
        passed: true,
        detail: { numerator: 2, denominator: 2, baseline: 1, reason: "ok" },
      },
    ],
    ...overrides,
  }
}

function adminAuth() {
  return {
    user: { id: 1, username: "admin", role: "admin", status: "active" },
    loading: false,
    login: vi.fn(),
    logout: vi.fn(),
  }
}

function userAuth() {
  return {
    user: { id: 2, username: "qa01", role: "user", status: "active" },
    loading: false,
    login: vi.fn(),
    logout: vi.fn(),
  }
}

function page<T>(items: T[]) {
  return items
}

describe("QualityPage", () => {
  beforeEach(() => {
    mocks.listEvalRuns.mockReset()
    mocks.useAuth.mockReset()
    mocks.useAuth.mockReturnValue(adminAuth())
  })

  afterEach(() => {
    cleanup()
  })

  it("renders the latest run metrics, thresholds and verdicts for an admin", async () => {
    mocks.listEvalRuns.mockResolvedValue(page([evalRun()]))

    render(<QualityPage />)

    const summary = await screen.findByTestId("quality-summary")
    expect(within(summary).getByText(/#1844674407370955161/)).toBeTruthy()
    expect(within(summary).getByText("确定性门禁", { exact: false })).toBeTruthy()
    expect(within(summary).getByText("1 项指标未达标")).toBeTruthy()

    const design = screen.getByTestId("quality-metric-design_coverage")
    expect(within(design).getByText("设计覆盖率")).toBeTruthy()
    expect(screen.getByTestId("quality-metric-value-design_coverage").textContent).toBe("80.0%")
    expect(within(design).getByText(/≥/)).toBeTruthy()
    expect(within(design).getByText("90.0%")).toBeTruthy()
    expect(within(design).getByText("未通过")).toBeTruthy()
    expect(within(design).getByText("4/5")).toBeTruthy()

    const accuracy = screen.getByTestId("quality-metric-classification_accuracy")
    expect(within(accuracy).getByText("通过")).toBeTruthy()
    expect(within(accuracy).getByText("2/2")).toBeTruthy()

    const history = screen.getByTestId("quality-history")
    expect(within(history).getByTestId(`quality-history-row-${RUN_ID}`)).toBeTruthy()
    expect(within(history).getByText("1 / 2")).toBeTruthy()
    expect(mocks.listEvalRuns).toHaveBeenCalledTimes(1)
  })

  it("shows each metric's admin-visible baseline", async () => {
    mocks.listEvalRuns.mockResolvedValue(page([evalRun()]))

    render(<QualityPage />)

    const summary = await screen.findByTestId("quality-summary")
    expect(within(summary).getByTestId("quality-metric-baseline-design_coverage").textContent).toBe(
      "85.0%",
    )
    expect(
      within(summary).getByTestId("quality-metric-baseline-classification_accuracy").textContent,
    ).toBe("100.0%")
  })

  it("never reports an error run with no metrics as all metrics passed", async () => {
    mocks.listEvalRuns.mockResolvedValue(
      page([evalRun({ status: "error", metrics: [], summary: { reason: "scenario_failed" } })]),
    )

    render(<QualityPage />)

    const summary = await screen.findByTestId("quality-summary")
    expect(screen.getByTestId("quality-no-metrics")).toBeTruthy()
    expect(within(summary).queryByText("全部指标达标")).toBeNull()
    expect(within(summary).getByTestId("quality-verdict").textContent).toBe("无指标")
  })

  it("shows the empty state before the first evaluation", async () => {
    mocks.listEvalRuns.mockResolvedValue([])
    render(<QualityPage />)
    expect(await screen.findByText("尚未运行评测")).toBeTruthy()
    expect(screen.queryByTestId("quality-summary")).toBeNull()
  })

  it("shows a retryable error with the request id and recovers", async () => {
    mocks.listEvalRuns
      .mockRejectedValueOnce(
        new ApiError({
          status: 500,
          code: "internal",
          message: "internal server error",
          requestId: "req-123",
        }),
      )
      .mockResolvedValueOnce(page([evalRun()]))

    render(<QualityPage />)

    const error = await screen.findByTestId("quality-error")
    expect(error.textContent).toContain("加载评测结果失败")
    expect(error.textContent).toContain("req-123")

    fireEvent.click(within(error).getByRole("button", { name: "重试" }))
    expect(await screen.findByTestId("quality-summary")).toBeTruthy()
    await waitFor(() => expect(mocks.listEvalRuns).toHaveBeenCalledTimes(2))
  })

  it("hides the dashboard from a normal user without calling the admin API", async () => {
    mocks.useAuth.mockReturnValue(userAuth())
    render(<QualityPage />)
    expect(screen.getByTestId("quality-page-denied")).toBeTruthy()
    expect(screen.getByText("仅管理员可查看评测")).toBeTruthy()
    expect(mocks.listEvalRuns).not.toHaveBeenCalled()
  })
})

describe("QualitySummary helpers", () => {
  it("formats ratios, modes and statuses", () => {
    expect(formatMetricPercent(0.8)).toBe("80.0%")
    expect(formatMetricPercent(1)).toBe("100.0%")
    expect(modeLabel("deterministic")).toBe("确定性门禁")
    expect(modeLabel("real_llm")).toBe("真实 LLM 报告")
    expect(runStatusBadge("passed").label).toBe("通过")
    expect(runStatusBadge("failed").variant).toBe("danger")
    expect(formatRunTime(null)).toBe("—")
    expect(formatRunTime("not-a-date")).toBe("not-a-date")
  })
})
