import { describe, expect, it } from "vitest"

import { activityText } from "./activity"

describe("activityText", () => {
  it.each(["thinking", "tool", "started", "running"])(
    "renders a known active status %s",
    (status) => {
      expect(activityText({ status })).toBe("AI 正在处理…")
    },
  )

  it("passes an explicit activity or message through", () => {
    expect(activityText({ status: "thinking", activity: "正在读取设计图" })).toBe("正在读取设计图")
    expect(activityText({ status: "tool", message: "正在调用 case.graph_propose" })).toBe(
      "正在调用 case.graph_propose",
    )
  })

  it("prefers the explicit activity over the generic message", () => {
    expect(
      activityText({ status: "tool", activity: "正在生成用例", message: "generic fallback" }),
    ).toBe("正在生成用例")
  })

  it.each(["finished", "failed", "done", "cancelled", "", "telemetry-v2"])(
    "stays idle for the %s status instead of showing the generic placeholder",
    (status) => {
      expect(activityText({ status })).toBeNull()
    },
  )

  it("stays idle for a terminal status even when it carries text", () => {
    expect(activityText({ status: "finished", activity: "正在生成用例" })).toBeNull()
    expect(activityText({ status: "failed", message: "模型调用失败" })).toBeNull()
  })

  it("stays idle when the payload carries no status", () => {
    expect(activityText({ activity: "没有状态的旧事件" })).toBeNull()
    expect(activityText({})).toBeNull()
  })

  it("ignores blank explicit text and falls back to the placeholder", () => {
    expect(activityText({ status: "thinking", activity: "   ", message: "" })).toBe("AI 正在处理…")
  })
})
