import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import type { ComponentProps } from "react"
import { afterEach, describe, expect, it, vi } from "vitest"

import type { VersionEvent } from "@/features/versions/types"

import type { CaseRun, ExecutionSession } from "../types"

import { SessionsTabContent } from "./SessionsTab"

const VERSION_ID = "1844674407370955161"
const BATCH_ID = "1844674407370955162"

function makeSession(overrides: Partial<ExecutionSession>): ExecutionSession {
  return {
    id: "1",
    channel: `version:${VERSION_ID}`,
    title: "执行会话",
    status: "running",
    running: false,
    agent_role: "executor",
    profile_key: "exec",
    current_case_run_id: null,
    case_node_id: null,
    case_title: null,
    last_activity_at: "2026-10-28T08:00:00Z",
    created_at: "2026-10-28T08:00:00Z",
    updated_at: "2026-10-28T08:00:00Z",
    ...overrides,
  }
}

function activity(
  seq: number,
  sessionId: string,
  detail: Record<string, unknown>,
  summary = `activity ${seq}`,
  evidence: string[] = [],
): VersionEvent {
  return {
    seq,
    channel: `version:${VERSION_ID}`,
    type: "agent.activity",
    version_id: VERSION_ID,
    batch_id: BATCH_ID,
    case_id: null,
    step_id: null,
    agent_role: "executor",
    session_id: sessionId,
    ts: "2026-10-28T08:00:00Z",
    summary,
    detail,
    refs: { evidence, case: [], requirement: [], code: [] },
  }
}

const SESSIONS: ExecutionSession[] = [
  makeSession({
    id: "s1",
    title: "登录用例",
    status: "running",
    running: true,
    current_case_run_id: "cr1",
    case_node_id: "case-1",
    case_title: "下单成功",
  }),
  makeSession({
    id: "s2",
    title: "取消用例",
    status: "waiting",
    current_case_run_id: "cr2",
    case_node_id: "case-2",
    case_title: "取消订单",
  }),
  makeSession({
    id: "s3",
    title: "复盘会话",
    status: "done",
    agent_role: "reviewer",
    profile_key: "review",
    current_case_run_id: "cr3",
    case_node_id: "case-3",
    case_title: "复盘",
  }),
]

const EVENTS: VersionEvent[] = [
  activity(1, "s1", { kind: "run_started", run_id: 9, profile_key: "exec" }),
  activity(2, "s1", {
    kind: "text",
    run_id: 9,
    message_id: 10,
    part: 0,
    text: "正在分析订单用例",
  }),
  activity(3, "s1", {
    kind: "tool_use",
    run_id: 9,
    call_id: "call-1",
    tool: "browser.goto",
    args: '{"url":"http://fixture/p6/home"}',
  }),
  activity(
    4,
    "s1",
    {
      kind: "tool_result",
      run_id: 9,
      call_id: "call-1",
      tool: "browser.goto",
      status: "error",
      error_code: "nav_failed",
      result: "boom",
      message_id: 11,
    },
    "activity 4",
    ["ev-1"],
  ),
  activity(5, "s2", {
    kind: "text",
    run_id: 10,
    message_id: 20,
    part: 0,
    text: "检查取消流程",
  }),
  activity(6, "s3", {
    kind: "text",
    run_id: 11,
    message_id: 30,
    part: 0,
    text: "复盘完成",
  }),
]

const CASE_RUNS: CaseRun[] = [
  {
    id: "cr1",
    batch_id: BATCH_ID,
    case_node_id: "case-1",
    case_revision: 1,
    attempt: 1,
    status: "running",
    failure_class: null,
    failure_reason: "",
    summary: "",
    duration_ms: 0,
    title: "下单成功",
    session: null,
    checkpoint: { completed_seqs: [], cleanup_required: false, interrupted_step_seq: null },
    steps: [],
    assertions: [],
    evidence: [],
    pending_takeover: null,
  },
]

const USER_ROLE = { role: "user" } as const

function renderTab(overrides: Partial<ComponentProps<typeof SessionsTabContent>> = {}) {
  return render(
    <SessionsTabContent
      sessions={SESSIONS}
      caseRuns={CASE_RUNS}
      events={EVENTS}
      streamStatus="live"
      {...overrides}
    />,
  )
}

describe("SessionsTab", () => {
  afterEach(() => {
    cleanup()
  })

  it("groups sessions by agent role and shows current case", () => {
    renderTab()

    expect(screen.getByTestId("sessions-tab")).toBeTruthy()
    expect(screen.getByRole("heading", { level: 3, name: "executor" })).toBeTruthy()
    expect(screen.getByRole("heading", { level: 3, name: "reviewer" })).toBeTruthy()
    expect(screen.getByTestId("session-row-s1").textContent).toContain("下单成功")
    expect(screen.getByTestId("session-row-s1").textContent).toContain("进行中")
    expect(screen.getByTestId("session-row-s3").textContent).toContain("复盘")

    fireEvent.click(screen.getByTestId("session-row-s2"))
    expect(screen.getByTestId("execution-transcript").textContent).toContain("检查取消流程")
    expect(screen.getByTestId("execution-transcript").textContent).not.toContain("正在分析订单用例")
  })

  it("groups sessions by current case when group-by is case", () => {
    renderTab()

    fireEvent.change(screen.getByTestId("session-group-by"), { target: { value: "case" } })

    expect(screen.getByRole("heading", { level: 3, name: /下单成功/ })).toBeTruthy()
    expect(screen.getByRole("heading", { level: 3, name: /取消订单/ })).toBeTruthy()
    expect(screen.getByRole("heading", { level: 3, name: /复盘/ })).toBeTruthy()
  })

  it("filters sessions by agent, case and search text", () => {
    renderTab()

    fireEvent.change(screen.getByTestId("session-filter-agent"), {
      target: { value: "reviewer" },
    })
    expect(screen.queryByTestId("session-row-s1")).toBeNull()
    expect(screen.getByTestId("session-row-s3")).toBeTruthy()

    fireEvent.change(screen.getByTestId("session-filter-agent"), { target: { value: "" } })
    fireEvent.change(screen.getByTestId("session-filter-case"), {
      target: { value: "取消订单" },
    })
    expect(screen.queryByTestId("session-row-s1")).toBeNull()
    expect(screen.getByTestId("session-row-s2")).toBeTruthy()

    fireEvent.change(screen.getByTestId("session-filter-case"), { target: { value: "" } })
    fireEvent.change(screen.getByTestId("session-search"), { target: { value: "取消" } })
    expect(screen.queryByTestId("session-row-s1")).toBeNull()
    expect(screen.getByTestId("session-row-s2")).toBeTruthy()
  })

  it("filters the transcript by activity type and errors only", () => {
    renderTab()

    fireEvent.change(screen.getByTestId("session-filter-kind"), { target: { value: "tool" } })
    expect(screen.getByTestId("execution-transcript").textContent).toContain("工具 browser.goto")
    expect(screen.getByTestId("execution-transcript").textContent).not.toContain("正在分析订单用例")

    fireEvent.change(screen.getByTestId("session-filter-kind"), { target: { value: "" } })
    fireEvent.click(screen.getByTestId("session-filter-errors"))
    expect(screen.getByTestId("execution-transcript").textContent).toContain("工具 browser.goto")
    expect(screen.getByTestId("execution-transcript").textContent).not.toContain("正在分析订单用例")
  })

  it("shows the empty state when there is no execution session", () => {
    const { rerender } = renderTab({ sessions: [], caseRuns: [], events: [] })

    expect(screen.getByText("暂无执行会话")).toBeTruthy()
    expect(screen.queryByText("暂无消息")).toBeNull()

    rerender(
      <SessionsTabContent sessions={[]} caseRuns={[]} events={[]} streamStatus="reconnecting" />,
    )
    expect(screen.queryByText("暂无执行会话")).toBeNull()
    expect(screen.getAllByText("正在连接执行会话…").length).toBeGreaterThan(0)
  })

  it("shows reconnecting status without claiming the transcript is empty", () => {
    const { rerender } = renderTab({ events: [] })

    rerender(
      <SessionsTabContent
        sessions={SESSIONS}
        caseRuns={CASE_RUNS}
        events={[]}
        streamStatus="reconnecting"
      />,
    )
    expect(screen.getByTestId("stream-status").textContent).toContain("重连中")
    expect(screen.queryByText("暂无消息")).toBeNull()
    expect(screen.getByText("正在连接会话…")).toBeTruthy()

    rerender(
      <SessionsTabContent
        sessions={SESSIONS}
        caseRuns={CASE_RUNS}
        events={[]}
        streamStatus="closed"
      />,
    )
    expect(screen.getByTestId("stream-status").textContent).toContain("连接已断开")
    expect(screen.queryByText("暂无消息")).toBeNull()
    expect(screen.getAllByText("连接已断开").length).toBeGreaterThan(0)
  })

  it("invokes the evidence callback instead of importing an evidence viewer", () => {
    const onOpenEvidence = vi.fn()
    renderTab({ onOpenEvidence })

    fireEvent.click(screen.getByTestId("session-evidence-ev-1"))

    expect(onOpenEvidence).toHaveBeenCalledWith("ev-1")
  })

  it("renders the human gate card from a pending takeover for admins only", () => {
    const onCompleteTakeover = vi.fn()
    const caseRuns: CaseRun[] = [
      {
        ...CASE_RUNS[0],
        pending_takeover: {
          id: "t-1",
          reason: "mfa",
          account: "ops@example.com",
          system: "订单系统",
          instructions: { text: "请完成短信验证" },
          expires_at: "2026-10-28T10:00:00Z",
        },
      },
    ]

    const { rerender } = renderTab({ caseRuns, role: "admin", onCompleteTakeover })
    expect(screen.getByTestId("human-gate-card")).toBeTruthy()
    fireEvent.click(screen.getByTestId("human-gate-complete"))
    expect(onCompleteTakeover).toHaveBeenCalledWith(caseRuns[0].pending_takeover)

    rerender(
      <SessionsTabContent
        sessions={SESSIONS}
        caseRuns={caseRuns}
        events={EVENTS}
        streamStatus="live"
        {...USER_ROLE}
        onCompleteTakeover={onCompleteTakeover}
      />,
    )
    expect(screen.getByTestId("human-gate-card")).toBeTruthy()
    expect(screen.queryByTestId("human-gate-complete")).toBeNull()
  })
})
