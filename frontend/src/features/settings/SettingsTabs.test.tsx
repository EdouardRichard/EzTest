import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

vi.mock("@/features/users/UsersPanel", () => ({
  UsersPanel: () => <div>users-panel</div>,
}))
vi.mock("@/features/llm-settings/LLMPanel", () => ({
  LLMPanel: () => <div>llm-panel</div>,
}))
vi.mock("@/features/sse-test/SSETestPanel", () => ({
  SSETestPanel: () => <div>sse-panel</div>,
}))
vi.mock("./BasicConfigPanel", () => ({
  BasicConfigPanel: () => <div>basic-config-panel</div>,
}))
vi.mock("./AuditLogPage", () => ({
  AuditLogPage: () => <div>audit-log-panel</div>,
}))

import { SettingsTabs } from "./SettingsTabs"

describe("SettingsTabs", () => {
  afterEach(() => {
    cleanup()
  })

  it("offers the five admin tabs including basic config and audit log", () => {
    render(<SettingsTabs />)

    const tabs = screen.getAllByRole("tab").map((tab) => tab.textContent)
    expect(tabs).toEqual(["用户", "LLM", "SSE 测试", "基础配置", "审计日志"])
    expect(screen.getByText("users-panel")).toBeTruthy()
  })

  it("opens the basic config and audit log panels from their tabs", () => {
    render(<SettingsTabs />)

    fireEvent.mouseDown(screen.getByRole("tab", { name: /基础配置/ }))
    expect(screen.getByText("basic-config-panel")).toBeTruthy()

    fireEvent.mouseDown(screen.getByRole("tab", { name: /审计日志/ }))
    expect(screen.getByText("audit-log-panel")).toBeTruthy()
  })
})
