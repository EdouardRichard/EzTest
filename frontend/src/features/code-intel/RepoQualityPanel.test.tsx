import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { ApiError } from "@/lib/api-client"

import type { RepoQuality } from "./types"

const mocks = vi.hoisted(() => ({
  getRepoQuality: vi.fn(),
}))

vi.mock("./api", () => ({
  getRepoQuality: mocks.getRepoQuality,
}))

import { RepoQualityPanel } from "./RepoQualityPanel"

const REPO_ID = 3

function qualityFixture(overrides: Partial<RepoQuality> = {}): RepoQuality {
  return {
    repoId: REPO_ID,
    generatedAt: "2026-10-29T10:00:00Z",
    evidence: { checks: 20, failures: 5, passRate: 0.75 },
    invalidFacts: 2,
    confidence: { high: 4, medium: 2, low: 1, total: 7 },
    reviews: { total: 3, correct: 2, incorrect: 1 },
    impact: {
      precision: 0.75,
      recall: 0.8,
      available: true,
      evalRunId: "9201",
      evaluatedAt: "2026-10-29T08:00:00Z",
    },
    cacheHitRate: 0.5,
    analysis: {
      id: "9101",
      headCommit: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
      analyzedAt: "2026-10-29T09:00:00Z",
    },
    indexedCommit: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
    ...overrides,
  }
}

function emptyQuality(): RepoQuality {
  return qualityFixture({
    evidence: { checks: 0, failures: 0, passRate: null },
    invalidFacts: 0,
    confidence: { high: 0, medium: 0, low: 0, total: 0 },
    reviews: { total: 0, correct: 0, incorrect: 0 },
    impact: {
      precision: null,
      recall: null,
      available: false,
      reason: "no_eval_run",
      evalRunId: null,
      evaluatedAt: null,
    },
    cacheHitRate: null,
    analysis: null,
    indexedCommit: "",
  })
}

describe("RepoQualityPanel", () => {
  beforeEach(() => {
    mocks.getRepoQuality.mockReset()
  })

  afterEach(() => {
    cleanup()
  })

  it("renders the repository quality metrics from the real aggregate", async () => {
    mocks.getRepoQuality.mockResolvedValue(qualityFixture())

    render(<RepoQualityPanel repoId={REPO_ID} />)

    expect(await screen.findByRole("heading", { name: "代码质量" })).toBeTruthy()
    expect(mocks.getRepoQuality).toHaveBeenCalledWith(REPO_ID)

    const evidence = screen.getByTestId("quality-evidence-pass-rate")
    expect(evidence.textContent).toContain("75.0%")
    expect(evidence.textContent).toContain("20 次校验")
    expect(evidence.textContent).toContain("5 次失败")

    expect(screen.getByTestId("quality-invalid-facts").textContent).toContain("2 条")
    expect(screen.getByTestId("quality-confidence").textContent).toContain("高 4 · 中 2 · 低 1")
    expect(screen.getByTestId("quality-reviews").textContent).toContain(
      "共 3 条（正确 2 · 错误 1）",
    )
    expect(screen.getByTestId("quality-impact").textContent).toContain("precision 75.0%")
    expect(screen.getByTestId("quality-impact").textContent).toContain("recall 80.0%")
    expect(screen.getByTestId("quality-cache-hit-rate").textContent).toContain("50.0%")
  })

  it("shows an empty state instead of fabricating zero rates", async () => {
    mocks.getRepoQuality.mockResolvedValue(emptyQuality())

    render(<RepoQualityPanel repoId={REPO_ID} />)

    expect(await screen.findByText("暂无质量数据")).toBeTruthy()
    // No metric grid is rendered from the all-zero payload.
    expect(screen.queryByTestId("quality-evidence-pass-rate")).toBeNull()
    expect(screen.queryByTestId("quality-impact")).toBeNull()
  })

  it("shows a loading state while the aggregate is in flight", () => {
    mocks.getRepoQuality.mockReturnValue(new Promise(() => undefined))

    render(<RepoQualityPanel repoId={REPO_ID} />)

    const status = screen.getByRole("status")
    expect(status.textContent).toContain("正在加载质量指标")
  })

  it("marks the evaluation as unavailable without inventing precision", async () => {
    mocks.getRepoQuality.mockResolvedValue(
      qualityFixture({
        impact: {
          precision: null,
          recall: null,
          available: false,
          reason: "no_eval_run",
          evalRunId: null,
          evaluatedAt: null,
        },
      }),
    )

    render(<RepoQualityPanel repoId={REPO_ID} />)

    const impact = await screen.findByTestId("quality-impact")
    expect(impact.textContent).toContain("评测不可用（尚未运行评测）")
    expect(impact.textContent).not.toContain("precision")
    expect(impact.textContent).not.toContain("recall")
  })

  it("shows an error and retries the aggregate", async () => {
    mocks.getRepoQuality.mockRejectedValueOnce(
      new ApiError({ status: 503, code: "unavailable", message: "quality store down" }),
    )
    mocks.getRepoQuality.mockResolvedValueOnce(qualityFixture())

    render(<RepoQualityPanel repoId={REPO_ID} />)

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("加载质量指标失败")
    fireEvent.click(screen.getByRole("button", { name: "重试质量指标" }))

    await waitFor(() => expect(screen.getByTestId("quality-evidence-pass-rate")).toBeTruthy())
    expect(mocks.getRepoQuality).toHaveBeenCalledTimes(2)
  })
})
