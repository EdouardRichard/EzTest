import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { AssertionView, CaseRun, EvidenceView, StepResultView } from "../types"

const mocks = vi.hoisted(() => ({
  useCaseRuns: vi.fn(),
  useCaseRun: vi.fn(),
  useExecutionActions: vi.fn(),
  rerun: vi.fn(),
  refresh: vi.fn(),
  detailRefresh: vi.fn(),
}))

vi.mock("../hooks/useExecution", () => ({
  useCaseRuns: mocks.useCaseRuns,
  useCaseRun: mocks.useCaseRun,
  useExecutionActions: mocks.useExecutionActions,
}))

import { ResultsTab } from "./ResultsTab"

/** Snowflake identifiers stay decimal strings end-to-end (spec §6.1). */
const VERSION_ID = "1844674407370955161"
const BATCH_ID = "1844674407370955162"

const PASSED_RUN = "1844674407370955170"
const FAILED_RUN = "1844674407370955171"
const FAILED_RUN_2 = "1844674407370955172"
const BLOCKED_RUN = "1844674407370955173"
const RUNNING_RUN = "1844674407370955174"
const INCONCLUSIVE_RUN = "1844674407370955175"

const ASSERTION_ID = "1844674407370955180"
const DB_ASSERTION_ID = "1844674407370955181"
const EVIDENCE_SCREENSHOT = "1844674407370955190"
const EVIDENCE_HTTP = "1844674407370955191"
const EVIDENCE_CONSOLE = "1844674407370955192"

function evidenceView({
  id,
  ...rest
}: Partial<EvidenceView> & Pick<EvidenceView, "id">): EvidenceView {
  return {
    id,
    kind: "screenshot",
    mime: "image/png",
    size_bytes: 1024,
    sha256: "a".repeat(64),
    ...rest,
  }
}

const SCREENSHOT_EVIDENCE = evidenceView({ id: EVIDENCE_SCREENSHOT })
const HTTP_EVIDENCE = evidenceView({
  id: EVIDENCE_HTTP,
  kind: "http_exchange",
  mime: "application/json",
})
const CONSOLE_EVIDENCE = evidenceView({
  id: EVIDENCE_CONSOLE,
  kind: "console_log",
  mime: "application/json",
})

function step({
  seq,
  ...rest
}: Partial<StepResultView> & Pick<StepResultView, "seq">): StepResultView {
  return {
    seq,
    action: "",
    expected: "",
    actual: "",
    status: "passed",
    evidence_ids: [],
    ...rest,
  }
}

function assertion({
  id,
  ...rest
}: Partial<AssertionView> & Pick<AssertionView, "id">): AssertionView {
  return {
    id,
    kind: "ui",
    target: "",
    expected: "",
    actual: "",
    verdict: "passed",
    evidence_ids: [],
    ...rest,
  }
}

function caseRun(overrides: Partial<CaseRun> & Pick<CaseRun, "id" | "status">): CaseRun {
  const { id, status, ...rest } = overrides
  return {
    id,
    status,
    batch_id: BATCH_ID,
    case_node_id: `node-${id}`,
    case_revision: 1,
    attempt: 1,
    failure_class: null,
    failure_reason: "",
    summary: "",
    duration_ms: 0,
    title: `用例 ${id}`,
    session: null,
    checkpoint: { completed_seqs: [], cleanup_required: false, interrupted_step_seq: null },
    steps: [],
    assertions: [],
    evidence: [],
    pending_takeover: null,
    ...rest,
  }
}

const DETAIL: CaseRun = caseRun({
  id: FAILED_RUN,
  status: "failed",
  failure_class: "application",
  title: "下单流程",
  duration_ms: 1234,
  summary: "提交订单返回 500",
  steps: [
    step({
      seq: 1,
      action: "打开首页",
      expected: "首页可见",
      actual: "首页可见",
      status: "passed",
      evidence_ids: [EVIDENCE_SCREENSHOT],
    }),
    step({
      seq: 2,
      action: "提交订单",
      expected: "创建成功",
      actual: "HTTP 500",
      status: "failed",
      evidence_ids: [EVIDENCE_HTTP],
    }),
  ],
  assertions: [
    assertion({
      id: ASSERTION_ID,
      kind: "ui",
      target: "订单号",
      expected: "存在",
      actual: "缺失",
      verdict: "failed",
      evidence_ids: [EVIDENCE_CONSOLE],
      step_seq: 2,
    }),
  ],
  evidence: [SCREENSHOT_EVIDENCE, HTTP_EVIDENCE, CONSOLE_EVIDENCE],
})

const FAILED_LIST_RUN = caseRun({
  id: FAILED_RUN,
  status: "failed",
  failure_class: "application",
  title: "下单流程",
  duration_ms: 1234,
  summary: "提交订单返回 500",
  steps: DETAIL.steps,
  assertions: DETAIL.assertions,
  evidence: DETAIL.evidence,
})

function listResult(
  caseRuns: CaseRun[],
  overrides: {
    loading?: boolean
    error?: string
    refresh?: () => void
  } = {},
) {
  const { loading = false, error = "", refresh = mocks.refresh } = overrides
  return {
    caseRuns,
    nextCursor: "",
    hasMore: false,
    loading,
    error,
    streamStatus: "live" as const,
    refresh,
  }
}

function renderTab() {
  return render(<ResultsTab versionId={VERSION_ID} batchId={BATCH_ID} batchNo={3} />)
}

describe("ResultsTab", () => {
  beforeEach(() => {
    mocks.useCaseRuns.mockReset()
    mocks.useCaseRun.mockReset()
    mocks.useExecutionActions.mockReset()
    mocks.rerun.mockReset()
    mocks.refresh.mockReset()
    mocks.detailRefresh.mockReset()

    mocks.useCaseRuns.mockReturnValue(listResult([]))
    mocks.useExecutionActions.mockReturnValue({
      rerun: mocks.rerun,
      pending: "",
      error: "",
    })
  })

  afterEach(cleanup)

  it("orders failed and blocked runs first and renders the spec columns", () => {
    const passed = caseRun({ id: PASSED_RUN, status: "passed", title: "登录成功" })
    const inconclusive = caseRun({
      id: INCONCLUSIVE_RUN,
      status: "inconclusive",
      title: "库存查询",
    })
    const blocked = caseRun({
      id: BLOCKED_RUN,
      status: "blocked",
      failure_class: "environment",
      title: "库存同步",
    })
    const failedSecond = caseRun({
      id: FAILED_RUN_2,
      status: "failed",
      failure_class: "session",
      title: "支付回调",
    })
    mocks.useCaseRuns.mockReturnValue(
      listResult([passed, inconclusive, blocked, FAILED_LIST_RUN, failedSecond]),
    )

    renderTab()

    expect(screen.getByTestId("results-tab")).toBeTruthy()
    const rows = screen.getAllByTestId(/^case-run-row-/)
    expect(rows.map((row) => row.getAttribute("data-testid"))).toEqual([
      `case-run-row-${FAILED_RUN}`,
      `case-run-row-${FAILED_RUN_2}`,
      `case-run-row-${BLOCKED_RUN}`,
      `case-run-row-${PASSED_RUN}`,
      `case-run-row-${INCONCLUSIVE_RUN}`,
    ])

    for (const header of ["用例", "状态", "耗时", "步骤", "断言", "证据", "失败分类", "批次"]) {
      expect(screen.getByRole("columnheader", { name: header })).toBeTruthy()
    }

    expect(within(rows[0]).getByText("失败")).toBeTruthy()
    expect(within(rows[2]).getByText("阻塞")).toBeTruthy()
    expect(within(rows[0]).getByText("#3")).toBeTruthy()
    expect(within(rows[0]).getByText("应用缺陷")).toBeTruthy()
    expect(within(rows[2]).getByText("环境问题")).toBeTruthy()
    // Meaning is carried by text + icon, never colour alone.
    expect(
      within(rows[0])
        .getByText("应用缺陷")
        .closest("[data-failure-class]")
        ?.getAttribute("data-failure-class"),
    ).toBe("application")
    expect(within(rows[0]).getByText("应用缺陷").querySelector("svg")).not.toBeNull()
  })

  it("expands a row with getCaseRun and shows steps, expected vs actual and evidence ids", async () => {
    mocks.useCaseRuns.mockReturnValue(listResult([FAILED_LIST_RUN]))
    mocks.useCaseRun.mockReturnValue({
      caseRun: DETAIL,
      loading: false,
      error: "",
      streamStatus: "live",
      refresh: mocks.detailRefresh,
    })

    renderTab()
    expect(mocks.useCaseRun).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole("button", { name: "展开 下单流程" }))
    await waitFor(() =>
      expect(mocks.useCaseRun).toHaveBeenCalledWith(VERSION_ID, BATCH_ID, FAILED_RUN, undefined),
    )

    const firstStep = await screen.findByTestId("step-row-1")
    expect(firstStep.textContent).toContain("打开首页")
    expect(firstStep.textContent).toContain("期望")
    expect(firstStep.textContent).toContain("首页可见")

    const secondStep = screen.getByTestId("step-row-2")
    expect(secondStep.textContent).toContain("提交订单")
    expect(secondStep.textContent).toContain("期望")
    expect(secondStep.textContent).toContain("创建成功")
    expect(secondStep.textContent).toContain("实际")
    expect(secondStep.textContent).toContain("HTTP 500")

    const assertionRow = screen.getByTestId(`assertion-row-${ASSERTION_ID}`)
    expect(assertionRow.textContent).toContain("订单号")
    expect(assertionRow.textContent).toContain("期望")
    expect(assertionRow.textContent).toContain("存在")
    expect(assertionRow.textContent).toContain("实际")
    expect(assertionRow.textContent).toContain("缺失")

    expect(
      screen
        .getByTestId(`evidence-thumb-${EVIDENCE_SCREENSHOT}`)
        .getAttribute("data-evidence-kind"),
    ).toBe("screenshot")
    expect(
      screen.getByTestId(`evidence-thumb-${EVIDENCE_HTTP}`).getAttribute("data-evidence-kind"),
    ).toBe("http_exchange")
    expect(
      screen.getByTestId(`evidence-thumb-${EVIDENCE_CONSOLE}`).getAttribute("data-evidence-kind"),
    ).toBe("console_log")

    fireEvent.click(screen.getByRole("button", { name: "收起 下单流程" }))
    expect(screen.queryByTestId("step-row-1")).toBeNull()
  })

  it("offers rerun only for terminal runs and calls the action", () => {
    const running = caseRun({ id: RUNNING_RUN, status: "running", title: "库存同步" })
    mocks.useCaseRuns.mockReturnValue(listResult([FAILED_LIST_RUN, running]))

    renderTab()

    expect(
      within(screen.getByTestId(`case-run-row-${RUNNING_RUN}`)).queryByRole("button", {
        name: "重跑",
      }),
    ).toBeNull()

    fireEvent.click(
      within(screen.getByTestId(`case-run-row-${FAILED_RUN}`)).getByRole("button", {
        name: "重跑",
      }),
    )
    expect(mocks.rerun).toHaveBeenCalledWith(BATCH_ID, FAILED_RUN)
  })

  it("disables rerun while the action is pending", () => {
    mocks.useCaseRuns.mockReturnValue(listResult([FAILED_LIST_RUN]))
    mocks.useExecutionActions.mockReturnValue({
      rerun: mocks.rerun,
      pending: "rerun",
      error: "",
    })

    renderTab()

    const button = within(screen.getByTestId(`case-run-row-${FAILED_RUN}`)).getByRole("button", {
      name: "重跑中…",
    })
    expect(button.hasAttribute("disabled")).toBe(true)
  })

  it("shows an empty state when the batch has no case runs", () => {
    renderTab()

    expect(screen.getByTestId("results-tab")).toBeTruthy()
    expect(screen.getByText("暂无用例执行结果。")).toBeTruthy()
    expect(screen.queryByRole("table")).toBeNull()
  })

  it("shows a loading state instead of an empty table", () => {
    mocks.useCaseRuns.mockReturnValue(listResult([], { loading: true }))

    renderTab()

    expect(screen.getByText("执行结果加载中…")).toBeTruthy()
    expect(screen.queryByText("暂无用例执行结果。")).toBeNull()
  })

  it("reports a list error and retries the read", () => {
    mocks.useCaseRuns.mockReturnValue(listResult([], { error: "网络错误" }))

    renderTab()

    const alert = screen.getByRole("alert")
    expect(alert.textContent).toContain("加载执行结果失败：网络错误")
    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(mocks.refresh).toHaveBeenCalledTimes(1)
  })

  it("reports a case detail load failure when a row expands", async () => {
    mocks.useCaseRuns.mockReturnValue(listResult([FAILED_LIST_RUN]))
    mocks.useCaseRun.mockReturnValue({
      caseRun: null,
      loading: false,
      error: "详情加载失败",
      streamStatus: "live",
      refresh: mocks.detailRefresh,
    })

    renderTab()
    fireEvent.click(screen.getByRole("button", { name: "展开 下单流程" }))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("加载用例详情失败：详情加载失败")
  })

  it("renders db assertion rows with rows/elapsed/truncated/readonly metadata", async () => {
    const dbAssertion = assertion({
      id: DB_ASSERTION_ID,
      kind: "db",
      target: "public.orders",
      expected: '{"rows":[["1"]]}',
      actual: JSON.stringify({
        columns: ["id", "status"],
        rows: [["1", "paid"]],
        rowCount: 3,
        truncated: true,
        elapsedMs: 12,
      }),
      verdict: "failed",
      failure_reason: "db_readonly_violation",
      evidence_ids: [EVIDENCE_CONSOLE],
    })
    mocks.useCaseRuns.mockReturnValue(listResult([FAILED_LIST_RUN]))
    mocks.useCaseRun.mockReturnValue({
      caseRun: { ...DETAIL, assertions: [dbAssertion] },
      loading: false,
      error: "",
      streamStatus: "live",
      refresh: mocks.detailRefresh,
    })

    renderTab()
    fireEvent.click(screen.getByRole("button", { name: "展开 下单流程" }))

    const row = await screen.findByTestId(`assertion-row-${DB_ASSERTION_ID}`)
    const scoped = within(row)
    expect(scoped.getByTestId("assertion-db-badge").textContent).toBe("db")
    expect(scoped.getByTestId("assertion-db-badge").getAttribute("data-kind")).toBe("db")
    expect(scoped.getByTestId("assertion-db-rows").textContent).toContain("3 行")
    expect(scoped.getByTestId("assertion-db-elapsed").textContent).toContain("12 ms")
    expect(scoped.getByTestId("assertion-db-truncated").textContent).toContain("结果已截断")

    // A read-only violation is a stable reason: show readable copy, not the code.
    const error = scoped.getByTestId("assertion-db-error")
    expect(error.getAttribute("data-reason")).toBe("db_readonly_violation")
    expect(error.textContent).toContain("只读违规")
    expect(error.textContent).not.toContain("db_readonly_violation")
  })
})
