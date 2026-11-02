import { describe, expect, it } from "vitest"

import {
  ASSERTION_KIND_LABEL,
  ASSERTION_VERDICT_LABEL,
  CASE_RUN_STATUS_LABEL,
  CASE_RUN_STATUS_TONE,
  CASE_RUN_TERMINAL_STATUSES,
  EVIDENCE_KIND_LABEL,
  EXECUTOR_STATUS_LABEL,
  FAILURE_CLASS_LABEL,
  failureClassLabel,
  HUMAN_REQUEST_KIND_LABEL,
  HUMAN_REQUEST_STATUS_LABEL,
  HUMAN_REQUEST_STATUS_TONE,
  PAUSE_MODE_LABEL,
  STEP_STATUS_LABEL,
  WAVE_STATUS_LABEL,
  WAVE_STATUS_TONE,
  WAVE_TRIGGER_LABEL,
} from "./labels"
import type { CaseRunStatus, FailureClass } from "./types"

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
      executor_lost: "执行器丢失",
      blocked: "无法开始",
      unknown: "未知",
    })
    expect(Object.keys(FAILURE_CLASS_LABEL)).toHaveLength(9)
  })

  it("renders a dash for the empty (no class) value", () => {
    expect(failureClassLabel("")).toBe("—")
    expect(failureClassLabel("application")).toBe("应用缺陷")
    expect(failureClassLabel("executor_lost" as FailureClass)).toBe("执行器丢失")
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
  it("labels kinds, statuses and tones", () => {
    expect(HUMAN_REQUEST_KIND_LABEL).toEqual({
      login_takeover: "登录接管",
      conflict: "冲突处理",
      other: "其他",
    })
    expect(HUMAN_REQUEST_STATUS_LABEL).toEqual({
      open: "待处理",
      resolved: "已处理",
      cancelled: "已取消",
      expired: "已过期",
    })
    expect(HUMAN_REQUEST_STATUS_TONE.open).toBe("warning")
    expect(HUMAN_REQUEST_STATUS_TONE.resolved).toBe("success")
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
