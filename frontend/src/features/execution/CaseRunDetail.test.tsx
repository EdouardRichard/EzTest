import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it } from "vitest"
import "@testing-library/jest-dom/vitest"

import { CaseRunDetailView } from "./CaseRunDetail"
import { caseRunDetailFixture } from "./test-fixtures"
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
  })
})
