import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it } from "vitest"
import "@testing-library/jest-dom/vitest"

import { CaseRunDetailView } from "./CaseRunDetail"
import { blockedCaseRunFixture, caseRunDetailFixture } from "./test-fixtures"
import type { CaseRunDetail } from "./types"

function incompleteDetail(overrides: Partial<CaseRunDetail["case_run"]> = {}): CaseRunDetail {
  const base = caseRunDetailFixture
  return {
    ...base,
    case_run: {
      ...base.case_run,
      status: "inconclusive",
      failure_class: "",
      summary: {
        verdict_reason: "incomplete",
        terminal_cause: "token_budget",
        last_error:
          "token budget: used 222159 of 200000 tokens after 15 turns; the case recorded 1 of 4 steps before the run ended",
        next_action: "rerun",
        assertion_total: 1,
        assertion_failed: 0,
        step_total: 4,
        step_failed: 0,
        evidence_count: 2,
      },
      ...overrides,
    },
  }
}

function blockedDetail(): CaseRunDetail {
  const base = caseRunDetailFixture
  return {
    ...base,
    case_run: {
      ...base.case_run,
      id: blockedCaseRunFixture.id,
      status: "blocked",
      failure_class: "blocked",
      summary: {
        ...blockedCaseRunFixture.summary,
        verdict_reason: "missing_account",
        terminal_cause: "blocked",
        next_action: "resolve_blocker",
        last_error: "the admin account is not configured",
        usage: {
          turns: 12,
          total_tokens: 180000,
          prompt_tokens: 160000,
          completion_tokens: 20000,
        },
      },
    },
  }
}

afterEach(cleanup)

describe("CaseRunDetail incomplete cause", () => {
  it("explains an incomplete run with its concrete budget cause", () => {
    render(
      <CaseRunDetailView
        versionId="2001"
        detail={incompleteDetail()}
        loading={false}
        error={null}
        onRetry={() => {}}
      />,
    )

    expect(screen.getByTestId("case-run-terminal-cause")).toHaveTextContent("Token 预算用尽")
    const detail = screen.getByTestId("case-run-incomplete-detail")
    expect(detail).toHaveTextContent("222159")
    expect(detail).toHaveTextContent("1 of 4")
  })

  it("offers the human next action for an incomplete run", () => {
    render(
      <CaseRunDetailView
        versionId="2001"
        detail={incompleteDetail()}
        loading={false}
        error={null}
        onRetry={() => {}}
      />,
    )

    expect(screen.getByTestId("case-run-next-action")).toHaveTextContent("重跑用例")
  })

  it("does not invent a cause or next action for a clean verdict", () => {
    render(
      <CaseRunDetailView
        versionId="2001"
        detail={caseRunDetailFixture}
        loading={false}
        error={null}
        onRetry={() => {}}
      />,
    )

    expect(screen.queryByTestId("case-run-terminal-cause")).not.toBeInTheDocument()
    expect(screen.queryByTestId("case-run-incomplete-detail")).not.toBeInTheDocument()
    expect(screen.queryByTestId("case-run-next-action")).not.toBeInTheDocument()
    expect(screen.queryByTestId("case-run-usage")).not.toBeInTheDocument()
  })

  it("explains a blocked run with its cause, usage and next action", () => {
    render(
      <CaseRunDetailView
        versionId="2001"
        detail={blockedDetail()}
        loading={false}
        error={null}
        onRetry={() => {}}
      />,
    )

    expect(screen.getByTestId("case-run-terminal-cause")).toHaveTextContent("阻塞")
    expect(screen.getByTestId("case-run-incomplete-detail")).toHaveTextContent(
      "the admin account is not configured",
    )
    expect(screen.getByTestId("case-run-next-action")).toHaveTextContent("先解决阻塞条件")
    const usage = screen.getByTestId("case-run-usage")
    expect(usage).toHaveTextContent("180000")
    expect(usage).toHaveTextContent("12 轮")
  })
})
