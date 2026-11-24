import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

const api = vi.hoisted(() => ({
  listJudgments: vi.fn(),
  overrideJudgment: vi.fn(),
}))
vi.mock("./api", () => api)

import { JudgmentReport } from "./JudgmentReport"
import type { JudgmentReportView } from "./types"

const report: JudgmentReportView = {
  cases: [
    {
      case_id: "7001",
      current_version_id: "7002",
      review_state: "pass",
      judgment: {
        id: "9001",
        case_id: "7001",
        case_version_id: "7002",
        content_hash: "a".repeat(64),
        round: 1,
        verdict: "pass",
        findings: [],
        ac_coverage_matrix: { "AC-1": "covered" },
        created_at: "2026-11-18T12:00:00Z",
      },
    },
    {
      case_id: "7003",
      current_version_id: "7004",
      review_state: "escalated_fail",
      judgment: {
        id: "9002",
        case_id: "7003",
        case_version_id: "7004",
        content_hash: "b".repeat(64),
        round: 2,
        verdict: "fail",
        findings: [
          { dimension: "coverage", severity: "blocker", message: "AC-2 未覆盖", case_ref: "7003" },
        ],
        ac_coverage_matrix: { "AC-2": "missing" },
        created_at: "2026-11-18T12:00:00Z",
      },
    },
    {
      case_id: "7005",
      current_version_id: "7006",
      review_state: "",
      judgment: null,
    },
  ],
}

describe("JudgmentReport override gating", () => {
  afterEach(cleanup)

  it("shows override for escalated_fail when admin", () => {
    render(<JudgmentReport reviewState="escalated_fail" isAdmin />)
    expect(screen.getByTestId("override-btn")).toBeInTheDocument()
  })

  it("shows override for judge_unavailable when admin", () => {
    render(<JudgmentReport reviewState="judge_unavailable" isAdmin />)
    expect(screen.getByTestId("override-btn")).toBeInTheDocument()
  })

  it("hides override for pass even when admin", () => {
    render(<JudgmentReport reviewState="pass" isAdmin />)
    expect(screen.queryByTestId("override-btn")).toBeNull()
  })

  it("hides override for pending / reworking / degraded", () => {
    for (const state of ["pending", "reworking", ""]) {
      const { unmount } = render(<JudgmentReport reviewState={state} isAdmin />)
      expect(screen.queryByTestId("override-btn")).toBeNull()
      unmount()
    }
  })

  it("hides override for non-admin even when escalated", () => {
    render(<JudgmentReport reviewState="escalated_fail" isAdmin={false} />)
    expect(screen.queryByTestId("override-btn")).toBeNull()
  })
})

describe("JudgmentReport report mode", () => {
  beforeEach(() => {
    api.listJudgments.mockReset()
    api.overrideJudgment.mockReset()
  })
  afterEach(cleanup)

  it("fetches the report and renders verdict / findings / round per case", async () => {
    api.listJudgments.mockResolvedValue(report)
    render(<JudgmentReport versionId="2001" isAdmin />)

    expect(await screen.findByTestId("judgment-report")).toBeInTheDocument()
    expect(api.listJudgments).toHaveBeenCalledWith("2001", undefined)
    // verdict + round of the escalated case
    expect(screen.getByText(/不通过/)).toBeInTheDocument()
    expect(screen.getByText(/AC-2 未覆盖/)).toBeInTheDocument()
    expect(screen.getByText(/第 2 轮/)).toBeInTheDocument()
    // degraded review_state tolerated as unknown
    expect(screen.getByText(/审查未知/)).toBeInTheDocument()
  })

  it("filters cases by review_state", async () => {
    api.listJudgments.mockResolvedValue(report)
    render(<JudgmentReport versionId="2001" isAdmin />)
    await screen.findByTestId("judgment-report")

    fireEvent.click(screen.getByTestId("judgment-filter-escalated_fail"))
    expect(screen.queryByText(/AC-2 未覆盖/)).toBeInTheDocument()
    // the pass case finding-less row is filtered out
    expect(screen.queryByTestId("judgment-case-7001")).toBeNull()
    expect(screen.getByTestId("judgment-case-7003")).toBeInTheDocument()
  })

  it("override submit calls the API with the reason and refreshes", async () => {
    api.listJudgments.mockResolvedValue(report)
    api.overrideJudgment.mockResolvedValue({
      id: "9100",
      case_id: "7003",
      case_version_id: "7004",
      content_hash: "b".repeat(64),
      condition: "overridden",
      actor_id: "1",
      reason: "人工复核通过",
      created_at: "2026-11-18T13:00:00Z",
    })
    const onOverridden = vi.fn()
    render(<JudgmentReport versionId="2001" isAdmin onOverridden={onOverridden} />)
    await screen.findByTestId("judgment-report")

    fireEvent.click(screen.getByTestId("override-btn"))
    fireEvent.change(screen.getByLabelText(/决断原因/), { target: { value: "人工复核通过" } })
    fireEvent.click(screen.getByTestId("override-submit"))

    await waitFor(() =>
      expect(api.overrideJudgment).toHaveBeenCalledWith("2001", "7003", "人工复核通过"),
    )
    await waitFor(() => expect(onOverridden).toHaveBeenCalled())
  })

  it("does not offer override to a non-admin in report mode", async () => {
    api.listJudgments.mockResolvedValue(report)
    render(<JudgmentReport versionId="2001" isAdmin={false} />)
    await screen.findByTestId("judgment-report")
    expect(screen.queryByTestId("override-btn")).toBeNull()
  })
})

describe("JudgmentReport findings rendering", () => {
  afterEach(cleanup)

  it("renders identical findings without a duplicate React key", async () => {
    // Whole-branch review minor: the finding key was dimension-severity-message,
    // so a judge that legitimately reports the same finding twice produced two
    // identical keys and a React duplicate-key warning.
    const duplicated: JudgmentReportView = {
      cases: [
        {
          case_id: "7001",
          current_version_id: "7002",
          review_state: "escalated_fail",
          judgment: {
            id: "9001",
            case_id: "7001",
            case_version_id: "7002",
            content_hash: "a".repeat(64),
            round: 2,
            verdict: "fail",
            findings: [
              {
                dimension: "coverage",
                severity: "blocker",
                message: "AC-2 未覆盖",
                case_ref: "7001",
              },
              {
                dimension: "coverage",
                severity: "blocker",
                message: "AC-2 未覆盖",
                case_ref: "7001",
              },
            ],
            ac_coverage_matrix: {},
            created_at: "2026-11-18T12:00:00Z",
          },
        },
      ],
    }
    api.listJudgments.mockReset()
    api.listJudgments.mockResolvedValue(duplicated)
    const errors = vi.spyOn(console, "error").mockImplementation(() => {})
    try {
      render(<JudgmentReport versionId="2001" isAdmin />)
      await screen.findByTestId("judgment-case-7001")
      expect(screen.getAllByText("AC-2 未覆盖")).toHaveLength(2)
      const keyWarning = errors.mock.calls
        .map((call) => call.map((arg) => String(arg)).join(" "))
        .filter((line) => line.includes("same key") || line.includes('unique "key"'))
      expect(keyWarning).toEqual([])
    } finally {
      errors.mockRestore()
    }
  })
})
