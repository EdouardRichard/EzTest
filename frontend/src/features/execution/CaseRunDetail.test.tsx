import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
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

function overrideSummary(
  overrides: Partial<CaseRunDetail["case_run"]> = {},
  summary: CaseRunDetail["case_run"]["summary"] = {},
): CaseRunDetail {
  const base = caseRunDetailFixture
  return {
    ...base,
    case_run: {
      ...base.case_run,
      ...overrides,
      summary: { ...base.case_run.summary, ...summary },
    },
  }
}

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

  it("renders a case run with null usage without crashing", () => {
    const detail: CaseRunDetail = {
      ...caseRunDetailFixture,
      case_run: {
        ...caseRunDetailFixture.case_run,
        status: "passed",
        failure_class: "",
        summary: { ...caseRunDetailFixture.case_run.summary, usage: null },
      },
    }

    render(
      <CaseRunDetailView
        versionId="2001"
        detail={detail}
        loading={false}
        error={null}
        onRetry={() => {}}
      />,
    )

    // v0.22 audit §2.5-2: `usage: null` used to crash the whole tab. The
    // terminal row must still render its status/duration and say the telemetry
    // is unavailable instead of throwing.
    expect(screen.getByTestId("case-run-status")).toHaveTextContent("通过")
    expect(screen.getByTestId("case-run-duration")).toHaveTextContent("41.0s")
    expect(screen.getByTestId("case-run-telemetry-unavailable")).toHaveTextContent("遥测不可用")
    expect(screen.queryByTestId("case-run-usage")).not.toBeInTheDocument()
  })

  it("states that telemetry is unavailable on a superseded run", () => {
    const detail: CaseRunDetail = {
      ...caseRunDetailFixture,
      case_run: {
        ...caseRunDetailFixture.case_run,
        status: "superseded",
        failure_class: "",
        summary: {
          verdict_reason: "case_version_superseded",
          terminal_cause: "case_version_superseded",
          next_action: "reconfirm_case_version",
          usage: null,
        },
      },
    }

    render(
      <CaseRunDetailView
        versionId="2001"
        detail={detail}
        loading={false}
        error={null}
        onRetry={() => {}}
      />,
    )

    // v0.22 audit §2.5-2: skipped/superseded rows settle without running and
    // can carry `usage: null`; they are terminal and must state that the
    // telemetry is unavailable instead of silently omitting the line.
    expect(screen.getByTestId("case-run-status")).toHaveTextContent("已取代")
    expect(screen.getByTestId("case-run-telemetry-unavailable")).toHaveTextContent("遥测不可用")
    expect(screen.queryByTestId("case-run-usage")).not.toBeInTheDocument()
  })

  it("explains a blocked run without a last_error using its verdict reason", () => {
    const detail: CaseRunDetail = {
      ...caseRunDetailFixture,
      case_run: {
        ...caseRunDetailFixture.case_run,
        status: "blocked",
        failure_class: "blocked",
        summary: { verdict_reason: "missing_account", evidence_count: 0 },
      },
    }

    render(
      <CaseRunDetailView
        versionId="2001"
        detail={detail}
        loading={false}
        error={null}
        onRetry={() => {}}
      />,
    )

    // A blocked run must state the blocker and the next action even when the
    // older row carries no last_error/next_action fields.
    expect(screen.getByTestId("case-run-incomplete-detail")).toHaveTextContent("缺少测试账号")
    expect(screen.getByTestId("case-run-next-action")).toHaveTextContent("先解决阻塞条件")
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

describe("CaseRunDetail telemetry pending (R2-OBS-01)", () => {
  it("marks telemetry pending and refetches at 5s then 15s for a just-finished run", () => {
    vi.useFakeTimers()
    try {
      vi.setSystemTime(new Date("2026-11-10T12:33:43Z"))
      const onRetry = vi.fn()
      const detail: CaseRunDetail = {
        ...caseRunDetailFixture,
        case_run: {
          ...caseRunDetailFixture.case_run,
          status: "passed",
          failure_class: "",
          // Usage is attached by Finalize after `case_run.finished`: right after
          // the event the snapshot can still answer `usage: null`.
          finished_at: new Date().toISOString(),
          summary: { ...caseRunDetailFixture.case_run.summary, usage: null },
        },
      }

      render(
        <CaseRunDetailView
          versionId="2001"
          detail={detail}
          loading={false}
          error={null}
          onRetry={onRetry}
        />,
      )

      expect(screen.getByTestId("case-run-telemetry-pending")).toHaveTextContent("遥测写入中")
      expect(screen.queryByTestId("case-run-telemetry-unavailable")).not.toBeInTheDocument()
      expect(screen.queryByTestId("case-run-usage")).not.toBeInTheDocument()

      act(() => {
        vi.advanceTimersByTime(5_000)
      })
      expect(onRetry).toHaveBeenCalledTimes(1)

      act(() => {
        vi.advanceTimersByTime(15_000)
      })
      expect(onRetry).toHaveBeenCalledTimes(2)

      // One-shot: the delayed refetch never runs more than twice.
      act(() => {
        vi.advanceTimersByTime(60_000)
      })
      expect(onRetry).toHaveBeenCalledTimes(2)
    } finally {
      vi.useRealTimers()
    }
  })

  it("keeps telemetry unavailable once the write window has passed", () => {
    vi.useFakeTimers()
    try {
      vi.setSystemTime(new Date("2026-11-10T12:33:43Z"))
      const detail = overrideSummary(
        {
          status: "passed",
          failure_class: "",
          finished_at: new Date(Date.now() - 5 * 60_000).toISOString(),
        },
        { usage: null },
      )

      render(
        <CaseRunDetailView
          versionId="2001"
          detail={detail}
          loading={false}
          error={null}
          onRetry={() => {}}
        />,
      )

      expect(screen.getByTestId("case-run-telemetry-unavailable")).toHaveTextContent("遥测不可用")
      expect(screen.queryByTestId("case-run-telemetry-pending")).not.toBeInTheDocument()
    } finally {
      vi.useRealTimers()
    }
  })
})

describe("CaseRunDetail cause labels (X-07)", () => {
  it("localizes an executor_lost failure and never renders the raw enum", () => {
    render(
      <CaseRunDetailView
        versionId="2001"
        detail={overrideSummary(
          { status: "failed", failure_class: "executor_lost" },
          {
            verdict_reason: "agent_failure",
            terminal_cause: "executor_lost",
            last_error: "executor_lost",
            next_action: "rerun",
          },
        )}
        loading={false}
        error={null}
        onRetry={() => {}}
      />,
    )

    expect(screen.getByTestId("case-run-terminal-cause")).toHaveTextContent("执行器失联")
    expect(screen.getByTestId("case-run-incomplete-detail")).toHaveTextContent("执行器失联")
    expect(document.body.textContent).not.toContain("executor_lost")
  })

  it("localizes a legacy running row's raw verdict reason", () => {
    render(
      <CaseRunDetailView
        versionId="2001"
        detail={overrideSummary(
          { status: "running", failure_class: "" },
          { verdict_reason: "executor_lost", last_error: "executor_lost" },
        )}
        loading={false}
        error={null}
        onRetry={() => {}}
      />,
    )

    expect(screen.getByTestId("case-run-status")).toHaveTextContent("执行中")
    expect(screen.getByText(/执行器失联/)).toBeInTheDocument()
    expect(document.body.textContent).not.toContain("executor_lost")
  })

  it.each([
    ["executor_timeout", "执行器超时"],
    ["executor_context_create_failed", "执行器上下文创建失败"],
    ["executor_capability", "执行器能力不足"],
  ])("maps the %s terminal cause to %s", (cause, label) => {
    render(
      <CaseRunDetailView
        versionId="2001"
        detail={overrideSummary(
          { status: "failed", failure_class: "timeout" },
          {
            verdict_reason: cause,
            terminal_cause: cause,
            last_error: cause,
            next_action: "rerun",
          },
        )}
        loading={false}
        error={null}
        onRetry={() => {}}
      />,
    )

    expect(screen.getByTestId("case-run-terminal-cause")).toHaveTextContent(label)
    expect(screen.getByTestId("case-run-incomplete-detail")).toHaveTextContent(label)
    expect(document.body.textContent).not.toContain(cause)
  })

  it("localizes a last_error that carries a stable code prefix", () => {
    render(
      <CaseRunDetailView
        versionId="2001"
        detail={overrideSummary(
          { status: "failed", failure_class: "timeout" },
          {
            verdict_reason: "agent_failure",
            terminal_cause: "executor_timeout",
            last_error: "executor_timeout: context creation exceeded its hard deadline",
            next_action: "rerun",
          },
        )}
        loading={false}
        error={null}
        onRetry={() => {}}
      />,
    )

    expect(screen.getByTestId("case-run-incomplete-detail")).toHaveTextContent(
      "执行器超时: context creation exceeded its hard deadline",
    )
    expect(document.body.textContent).not.toContain("executor_timeout")
  })

  it("prefills the manual issue summary with the localized last_error", () => {
    render(
      <CaseRunDetailView
        versionId="2001"
        detail={overrideSummary(
          { status: "failed", failure_class: "timeout" },
          {
            verdict_reason: "agent_failure",
            terminal_cause: "executor_timeout",
            last_error: "executor_timeout: context creation exceeded its hard deadline",
            next_action: "rerun",
          },
        )}
        loading={false}
        error={null}
        onRetry={() => {}}
      />,
    )

    fireEvent.click(screen.getByRole("button", { name: "创建问题" }))

    const summary = screen.getByLabelText("摘要") as HTMLTextAreaElement
    expect(summary.value).toBe("执行器超时: context creation exceeded its hard deadline")
    expect(document.body.textContent).not.toContain("executor_timeout")
  })
})
