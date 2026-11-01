import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

const qualityApi = vi.hoisted(() => ({
  getQualityOverview: vi.fn(),
  listEvalRuns: vi.fn(),
}))
vi.mock("./api", () => qualityApi)

const versionsApi = vi.hoisted(() => ({ listVersions: vi.fn() }))
vi.mock("@/features/versions/api", () => versionsApi)

const visualApi = vi.hoisted(() => ({ getVisualMetrics: vi.fn() }))
vi.mock("@/features/visual/api", () => visualApi)

const auth = vi.hoisted(() => ({ isAdmin: false }))
vi.mock("@/features/auth/useIsAdmin", () => ({ useIsAdmin: () => auth.isAdmin }))

const navigation = vi.hoisted(() => ({ replace: vi.fn() }))
vi.mock("next/navigation", () => ({ useRouter: () => navigation }))

import { versionSummaryFixture } from "@/features/versions/test-fixtures"
import { visualMetricsFixture } from "@/features/visual/test-fixtures"

import { QualityPage } from "./QualityPage"
import type { EvalRun, QualityOverview } from "./types"

const overviewFixture: QualityOverview = {
  scope: { version_id: null },
  generated_at: "2026-11-01T12:00:00Z",
  operational: [
    {
      name: "requirement_closure_rate",
      value: 0.5,
      numerator: 3,
      denominator: 6,
      available: true,
      definition: "已闭环需求 / 可闭环需求",
    },
    {
      name: "case_confirmation_rate",
      value: null,
      numerator: 0,
      denominator: 0,
      available: false,
      definition: "已确认用例版本 / 当前用例版本",
    },
    {
      name: "execution_pass_rate",
      value: 0.75,
      numerator: 3,
      denominator: 4,
      available: true,
      definition: "通过用例运行 / 已结束用例运行",
    },
    {
      name: "issue_validity_rate",
      value: 0.6,
      numerator: 3,
      denominator: 5,
      available: true,
      definition: "已确认问题 / 已确认+已驳回问题",
    },
    {
      name: "evidence_completeness_rate",
      value: 1,
      numerator: 4,
      denominator: 4,
      available: true,
      definition: "含证据的已结束用例运行 / 已结束用例运行",
    },
    {
      name: "context_compaction_count",
      value: 12,
      numerator: 12,
      denominator: 1,
      available: true,
      definition: "context.compacted 事件计数",
    },
    {
      name: "task_failure_retry_rate",
      value: 0.25,
      numerator: 1,
      denominator: 4,
      available: true,
      definition: "重试次数大于 1 的工作项 / 已尝试工作项",
    },
  ],
  latest_eval: {
    id: "9",
    mode: "real_llm",
    status: "failed",
    created_at: "2026-11-01T11:00:00Z",
    metrics_count: 7,
    metrics_passed: 6,
  },
}

const evalRunFixture: EvalRun = {
  id: "9",
  mode: "real_llm",
  corpus_version: "v02-2026-11-01.1",
  model: "deepseek-flash",
  status: "failed",
  summary: { version_id: "2001" },
  metrics: [
    {
      name: "design_coverage",
      value: 0.8,
      threshold: 0.8,
      passed: true,
      detail: {
        numerator: 8,
        denominator: 10,
        threshold_passed: true,
        baseline: 0.7,
        regressed: false,
        reason: "ok",
        zero_denominator: false,
      },
    },
    {
      name: "issue_false_positive_rate",
      value: 0.2,
      threshold: 0.15,
      passed: false,
      detail: {
        numerator: 2,
        denominator: 10,
        threshold_passed: false,
        regressed: false,
        reason: "above_threshold",
        zero_denominator: false,
      },
    },
  ],
  started_at: "2026-11-01T10:58:00Z",
  finished_at: "2026-11-01T11:00:00Z",
  created_at: "2026-11-01T11:00:00Z",
}

describe("QualityPage", () => {
  beforeEach(() => {
    auth.isAdmin = false
    qualityApi.getQualityOverview.mockReset()
    qualityApi.listEvalRuns.mockReset()
    versionsApi.listVersions.mockReset()
    visualApi.getVisualMetrics.mockReset()
    navigation.replace.mockReset()

    qualityApi.getQualityOverview.mockResolvedValue(overviewFixture)
    qualityApi.listEvalRuns.mockResolvedValue([evalRunFixture])
    versionsApi.listVersions.mockResolvedValue([
      versionSummaryFixture,
      { ...versionSummaryFixture, id: "2002", title: "订单中心 · 2027 Q1 迭代" },
    ])
    visualApi.getVisualMetrics.mockResolvedValue(visualMetricsFixture)
  })

  afterEach(cleanup)

  it("renders operational metrics and the visual panel for every logged-in user without requesting eval runs", async () => {
    render(<QualityPage />)

    expect(await screen.findByRole("heading", { name: "质量" })).toBeInTheDocument()
    const closure = await screen.findByTestId("quality-operational-requirement_closure_rate")
    expect(closure).toHaveTextContent("需求闭环率")
    expect(closure).toHaveTextContent("50%")

    const confirmation = screen.getByTestId("quality-operational-case_confirmation_rate")
    expect(confirmation).toHaveTextContent("不可用")
    expect(confirmation).not.toHaveTextContent("0%")

    const compaction = screen.getByTestId("quality-operational-context_compaction_count")
    expect(compaction).toHaveTextContent("12")

    expect(await screen.findByTestId("quality-metrics-panel")).toHaveTextContent("基线覆盖率")
    expect(visualApi.getVisualMetrics).toHaveBeenCalledWith(undefined)

    expect(screen.getByTestId("quality-eval-admin-only")).toHaveTextContent("评测看板仅管理员可见")
    expect(qualityApi.listEvalRuns).not.toHaveBeenCalled()
  })

  it("shows the admin eval board with runs, thresholds and baselines", async () => {
    auth.isAdmin = true
    render(<QualityPage />)

    expect(await screen.findByTestId("quality-eval-runs")).toHaveTextContent("v02-2026-11-01.1")
    expect(screen.getByTestId("quality-eval-run-9")).toHaveTextContent("deepseek-flash")
    expect(screen.getByTestId("quality-eval-latest")).toHaveTextContent("6/7")
    expect(qualityApi.listEvalRuns).toHaveBeenCalledTimes(1)

    const design = screen.getByTestId("quality-eval-metric-design_coverage")
    expect(design).toHaveTextContent("80%")
    expect(design).toHaveTextContent("70%")
    expect(design).toHaveTextContent("通过")

    const falsePositive = screen.getByTestId("quality-eval-metric-issue_false_positive_rate")
    expect(falsePositive).toHaveTextContent("20%")
    expect(falsePositive).toHaveTextContent("15%")
    expect(falsePositive).toHaveTextContent("高于阈值")

    expect(screen.getByTestId("quality-eval-baseline")).toHaveTextContent("基线")
    expect(screen.getByTestId("quality-eval-baseline")).toHaveTextContent("70%")
    expect(screen.queryByTestId("quality-eval-admin-only")).not.toBeInTheDocument()
  })

  it("shows the empty eval state instead of zeros", async () => {
    auth.isAdmin = true
    qualityApi.listEvalRuns.mockResolvedValue([])
    qualityApi.getQualityOverview.mockResolvedValue({ ...overviewFixture, latest_eval: null })
    render(<QualityPage />)

    expect(await screen.findByText("尚未运行评测")).toBeInTheDocument()
    expect(screen.getByTestId("quality-eval-empty")).toHaveTextContent("尚未运行评测")
    expect(screen.queryByTestId("quality-eval-metrics")).not.toBeInTheDocument()
  })

  it("retries a failed overview request", async () => {
    qualityApi.getQualityOverview.mockRejectedValueOnce(new Error("概览服务不可用"))
    render(<QualityPage />)

    expect(await screen.findByText("质量概览加载失败")).toBeInTheDocument()
    expect(screen.getByText("概览服务不可用")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))

    expect(await screen.findByTestId("quality-operational-metrics")).toBeInTheDocument()
    expect(qualityApi.getQualityOverview).toHaveBeenCalledTimes(2)
  })

  it("switches the version scope through the URL", async () => {
    render(<QualityPage initialVersionId="2001" />)

    expect(await screen.findByTestId("quality-operational-metrics")).toBeInTheDocument()
    expect(qualityApi.getQualityOverview).toHaveBeenCalledWith("2001")
    expect(visualApi.getVisualMetrics).toHaveBeenCalledWith("2001")

    const selector = await screen.findByLabelText("版本")
    expect(selector).toHaveValue("2001")

    fireEvent.change(selector, { target: { value: "" } })
    await waitFor(() => expect(qualityApi.getQualityOverview).toHaveBeenLastCalledWith(undefined))
    expect(navigation.replace).toHaveBeenCalledWith("/quality")

    fireEvent.change(selector, { target: { value: "2002" } })
    await waitFor(() => expect(qualityApi.getQualityOverview).toHaveBeenLastCalledWith("2002"))
    expect(navigation.replace).toHaveBeenCalledWith("/quality?version=2002")
  })
})
