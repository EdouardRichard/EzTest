import { describe, expect, it } from "vitest"

import {
  ASSERTION_KIND_LABEL,
  ASSERTION_VERDICT_LABEL,
  CASE_RUN_STATUS_LABEL,
  CASE_RUN_STATUS_TONE,
  CASE_RUN_TELEMETRY_TERMINAL_STATUSES,
  CASE_RUN_TERMINAL_CAUSE_LABEL,
  CASE_RUN_TERMINAL_STATUSES,
  causeTextLabel,
  EVIDENCE_KIND_LABEL,
  EXECUTOR_STATUS_LABEL,
  FAILURE_CLASS_LABEL,
  failureClassLabel,
  HUMAN_REQUEST_KIND_LABEL,
  HUMAN_REQUEST_STATUS_LABEL,
  HUMAN_REQUEST_STATUS_TONE,
  isTelemetryPending,
  nextActionLabel,
  PAUSE_MODE_LABEL,
  STEP_STATUS_LABEL,
  TELEMETRY_PENDING_WINDOW_MS,
  terminalCauseLabel,
  WAVE_STATUS_LABEL,
  WAVE_STATUS_TONE,
  WAVE_TRIGGER_LABEL,
} from "./labels"
import type { CaseRun, CaseRunStatus, FailureClass } from "./types"

describe("case run labels", () => {
  it("labels the four terminal verdicts and every process state", () => {
    expect(CASE_RUN_STATUS_LABEL.passed).toBe("通过")
    expect(CASE_RUN_STATUS_LABEL.failed).toBe("失败")
    expect(CASE_RUN_STATUS_LABEL.blocked).toBe("阻塞")
    expect(CASE_RUN_STATUS_LABEL.inconclusive).toBe("未定")
    expect(CASE_RUN_STATUS_LABEL.pending).toBe("待执行")
    expect(CASE_RUN_STATUS_LABEL.running).toBe("执行中")
    expect(CASE_RUN_STATUS_LABEL.paused).toBe("已暂停")
    expect(CASE_RUN_STATUS_LABEL.skipped).toBe("已跳过")
    expect(CASE_RUN_STATUS_LABEL.interrupted).toBe("已中断")
    expect(CASE_RUN_STATUS_LABEL.superseded).toBe("已取代")
    expect(Object.keys(CASE_RUN_STATUS_LABEL)).toHaveLength(10)
  })

  it("maps double-encodes status as text and tone", () => {
    const statuses = Object.keys(CASE_RUN_STATUS_LABEL) as CaseRunStatus[]
    for (const status of statuses) {
      expect(CASE_RUN_STATUS_TONE[status]).toBeTruthy()
    }
    expect(CASE_RUN_STATUS_TONE.passed).toBe("success")
    expect(CASE_RUN_STATUS_TONE.failed).toBe("danger")
    expect(CASE_RUN_STATUS_TONE.blocked).toBe("warning")
    expect(CASE_RUN_STATUS_TONE.running).toBe("info")
  })

  it("marks the four verdicts as terminal and process states as not", () => {
    expect(CASE_RUN_TERMINAL_STATUSES).toEqual(["passed", "failed", "blocked", "inconclusive"])
  })

  it("treats skipped and superseded rows as telemetry-terminal too", () => {
    expect(CASE_RUN_TELEMETRY_TERMINAL_STATUSES).toEqual([
      "passed",
      "failed",
      "blocked",
      "inconclusive",
      "skipped",
      "superseded",
    ])
  })
})

describe("telemetry pending window (R2-OBS-01)", () => {
  const finishedAt = "2026-11-10T12:33:43Z"
  const finishedMs = Date.parse(finishedAt)
  const run: Pick<CaseRun, "status" | "finished_at" | "summary"> = {
    status: "passed",
    finished_at: finishedAt,
    summary: { usage: null },
  }

  it("treats a just-finished terminal run without usage as telemetry pending", () => {
    expect(TELEMETRY_PENDING_WINDOW_MS).toBe(120_000)
    expect(isTelemetryPending(run, finishedMs + 30_000)).toBe(true)
    // The write window is bounded: older runs really have no telemetry.
    expect(isTelemetryPending(run, finishedMs + TELEMETRY_PENDING_WINDOW_MS)).toBe(false)
    expect(isTelemetryPending(run, finishedMs + 5 * 60_000)).toBe(false)
  })

  it("is not pending once usage arrives, on a non-verdict status, or without a valid end", () => {
    expect(
      isTelemetryPending(
        {
          ...run,
          summary: {
            usage: { turns: 1, total_tokens: 1, prompt_tokens: 1, completion_tokens: 0 },
          },
        },
        finishedMs + 30_000,
      ),
    ).toBe(false)
    expect(isTelemetryPending({ ...run, status: "skipped" }, finishedMs + 30_000)).toBe(false)
    expect(
      isTelemetryPending({ ...run, status: "running", finished_at: null }, finishedMs + 30_000),
    ).toBe(false)
    expect(isTelemetryPending({ ...run, finished_at: null }, finishedMs + 30_000)).toBe(false)
    expect(isTelemetryPending({ ...run, finished_at: "not-a-date" }, finishedMs + 30_000)).toBe(
      false,
    )
    // A finished_at in the future (clock skew) is not a pending write window.
    expect(isTelemetryPending(run, finishedMs - 1_000)).toBe(false)
  })
})

describe("failure class labels", () => {
  it("labels all nine failure classes from spec §4.5", () => {
    expect(FAILURE_CLASS_LABEL).toEqual({
      application: "应用缺陷",
      environment: "环境异常",
      session: "会话/登录",
      model: "模型异常",
      automation: "自动化异常",
      timeout: "超时",
      executor_lost: "执行器失联",
      blocked: "无法开始",
      unknown: "未知",
    })
    expect(Object.keys(FAILURE_CLASS_LABEL)).toHaveLength(9)
  })

  it("renders a dash for the empty (no class) value", () => {
    expect(failureClassLabel("")).toBe("—")
    expect(failureClassLabel("application")).toBe("应用缺陷")
    expect(failureClassLabel("executor_lost" as FailureClass)).toBe("执行器失联")
  })
})

describe("terminal cause labels (X-01/X-02/X-07)", () => {
  it("labels the new executor causes from v0.2.3", () => {
    expect(CASE_RUN_TERMINAL_CAUSE_LABEL.executor_timeout).toBe("执行器超时")
    expect(CASE_RUN_TERMINAL_CAUSE_LABEL.executor_context_create_failed).toBe(
      "执行器上下文创建失败",
    )
    expect(CASE_RUN_TERMINAL_CAUSE_LABEL.executor_capability).toBe("执行器能力不足")
    expect(CASE_RUN_TERMINAL_CAUSE_LABEL.executor_unavailable).toBe("执行器不可用")
    expect(CASE_RUN_TERMINAL_CAUSE_LABEL.lease_expired).toBe("执行租约过期")
    // R2-NEW-01: an expired dispatch lease on a run no agent worker claimed is a
    // queue stall, not an execution/environment failure.
    expect(CASE_RUN_TERMINAL_CAUSE_LABEL.dispatch_timeout).toBe("执行排队超时（未被 Agent 领取）")
  })

  it("humanizes stable cause/failure enum values but leaves prose alone", () => {
    expect(causeTextLabel("executor_timeout")).toBe("执行器超时")
    expect(causeTextLabel("executor_context_create_failed")).toBe("执行器上下文创建失败")
    expect(causeTextLabel("executor_capability")).toBe("执行器能力不足")
    expect(causeTextLabel("executor_lost")).toBe("执行器失联")
    expect(causeTextLabel("agent_failure")).toBe("Agent 运行失败")
    expect(causeTextLabel("missing_account")).toBe("缺少测试账号")
    expect(causeTextLabel("登录后跳转超时")).toBe("登录后跳转超时")
    expect(causeTextLabel("")).toBeNull()
    expect(causeTextLabel(undefined)).toBeNull()
  })

  it("localizes a leading stable code and keeps the remaining detail", () => {
    expect(causeTextLabel("executor_timeout: context creation exceeded its hard deadline")).toBe(
      "执行器超时: context creation exceeded its hard deadline",
    )
    expect(causeTextLabel("executor_unavailable: dial tcp 127.0.0.1:13336: refused")).toBe(
      "执行器不可用: dial tcp 127.0.0.1:13336: refused",
    )
    expect(causeTextLabel("executor_lost:")).toBe("执行器失联")
    // Unknown codes and prose that merely contains a colon stay verbatim.
    expect(causeTextLabel("token budget: used 1 of 2 tokens")).toBe(
      "token budget: used 1 of 2 tokens",
    )
    expect(causeTextLabel("https://sut.test:8443 path")).toBe("https://sut.test:8443 path")
  })

  it("does not resolve inherited object properties as labels", () => {
    expect(causeTextLabel("toString")).toBe("toString")
    expect(failureClassLabel("constructor")).toBe("constructor")
    expect(terminalCauseLabel("toString")).toBe("toString")
    expect(nextActionLabel("hasOwnProperty")).toBe("hasOwnProperty")
  })
})

describe("wave labels", () => {
  it("labels every wave status with a tone", () => {
    expect(WAVE_STATUS_LABEL).toEqual({
      planned: "计划中",
      running: "执行中",
      paused: "已暂停",
      done: "已完成",
      failed: "失败",
      cancelled: "已取消",
    })
    expect(WAVE_STATUS_TONE.running).toBe("info")
    expect(WAVE_STATUS_TONE.paused).toBe("warning")
    expect(WAVE_STATUS_TONE.done).toBe("success")
    expect(WAVE_STATUS_TONE.failed).toBe("danger")
  })

  it("labels triggers and pause modes", () => {
    expect(WAVE_TRIGGER_LABEL).toEqual({ auto: "自动", manual: "手动", retest: "复测" })
    expect(PAUSE_MODE_LABEL).toEqual({ graceful: "优雅暂停", immediate: "立即暂停" })
  })
})

describe("human request labels", () => {
  it("labels every v0.22 kind, status and tone", () => {
    expect(HUMAN_REQUEST_KIND_LABEL).toEqual({
      clarification_question: "澄清提问",
      approval: "审批",
      blocked_recovery: "阻塞恢复",
      login_takeover: "登录接管",
      conflict: "冲突处理",
      other: "其他",
    })
    expect(Object.keys(HUMAN_REQUEST_KIND_LABEL)).toHaveLength(6)
    expect(HUMAN_REQUEST_STATUS_LABEL).toEqual({
      pending: "待处理",
      answered: "已处理",
      cancelled: "已取消",
      expired: "已过期",
    })
    expect(HUMAN_REQUEST_STATUS_TONE.pending).toBe("warning")
    expect(HUMAN_REQUEST_STATUS_TONE.answered).toBe("success")
  })
})

describe("detail labels", () => {
  it("labels evidence kinds", () => {
    expect(EVIDENCE_KIND_LABEL.screenshot).toBe("截图")
    expect(EVIDENCE_KIND_LABEL.snapshot).toBe("快照")
    expect(EVIDENCE_KIND_LABEL.console).toBe("控制台")
    expect(EVIDENCE_KIND_LABEL.network).toBe("网络")
    expect(EVIDENCE_KIND_LABEL.http).toBe("HTTP")
    expect(EVIDENCE_KIND_LABEL.db).toBe("数据库")
    expect(EVIDENCE_KIND_LABEL.download).toBe("下载")
    expect(EVIDENCE_KIND_LABEL.text).toBe("文本")
  })

  it("labels step statuses, assertion verdicts and assertion kinds", () => {
    expect(STEP_STATUS_LABEL.pending).toBe("待执行")
    expect(STEP_STATUS_LABEL.running).toBe("执行中")
    expect(STEP_STATUS_LABEL.passed).toBe("通过")
    expect(STEP_STATUS_LABEL.failed).toBe("失败")
    expect(STEP_STATUS_LABEL.blocked).toBe("阻塞")
    expect(STEP_STATUS_LABEL.skipped).toBe("已跳过")

    expect(ASSERTION_VERDICT_LABEL.passed).toBe("通过")
    expect(ASSERTION_VERDICT_LABEL.failed).toBe("失败")
    expect(ASSERTION_VERDICT_LABEL.inconclusive).toBe("未定")
    expect(ASSERTION_VERDICT_LABEL.skipped).toBe("已跳过")

    expect(ASSERTION_KIND_LABEL.ui).toBe("UI")
    expect(ASSERTION_KIND_LABEL.api).toBe("API")
    expect(ASSERTION_KIND_LABEL.db).toBe("数据库")
    expect(ASSERTION_KIND_LABEL.text).toBe("文本")
    expect(ASSERTION_KIND_LABEL.visual).toBe("视觉")
  })

  it("labels executor states", () => {
    expect(EXECUTOR_STATUS_LABEL.active).toBe("在线")
    expect(EXECUTOR_STATUS_LABEL.draining).toBe("排空中")
    expect(EXECUTOR_STATUS_LABEL.offline).toBe("离线")
  })
})
