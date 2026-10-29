import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import type { PendingTakeover } from "../types"

import { HumanGateCard } from "./HumanGateCard"

const TAKEOVER: PendingTakeover = {
  id: "1844674407370955164",
  reason: "mfa",
  account: "ops@example.com",
  system: "订单系统",
  instructions: { text: "请在 5 分钟内完成短信验证" },
  expires_at: "2026-10-28T10:00:00Z",
}

const ADMIN = { role: "admin" } as const
const USER = { role: "user" } as const

describe("HumanGateCard", () => {
  afterEach(() => {
    cleanup()
  })

  it("renders reason, account, system, instructions and expiry", () => {
    render(<HumanGateCard takeover={TAKEOVER} caseTitle="下单成功" {...ADMIN} />)

    expect(screen.getByTestId("human-gate-card")).toBeTruthy()
    expect(screen.getByText(/多因素认证/)).toBeTruthy()
    expect(screen.getByText("ops@example.com")).toBeTruthy()
    expect(screen.getByText("订单系统")).toBeTruthy()
    expect(screen.getByText("请在 5 分钟内完成短信验证")).toBeTruthy()
    expect(screen.getByText(/下单成功/)).toBeTruthy()
    expect(
      screen.getByText(new Date("2026-10-28T10:00:00Z").toLocaleString("zh-CN", { hour12: false })),
    ).toBeTruthy()
  })

  it("hides takeover completion for normal user", () => {
    const onComplete = vi.fn()
    render(<HumanGateCard takeover={TAKEOVER} {...USER} onComplete={onComplete} />)

    expect(screen.getByTestId("human-gate-card")).toBeTruthy()
    expect(screen.queryByTestId("human-gate-complete")).toBeNull()
    expect(screen.getByText("仅管理员可完成接管")).toBeTruthy()
  })

  it("lets an admin complete the takeover through the callback", () => {
    const onComplete = vi.fn()
    render(<HumanGateCard takeover={TAKEOVER} {...ADMIN} onComplete={onComplete} />)

    fireEvent.click(screen.getByTestId("human-gate-complete"))

    expect(onComplete).toHaveBeenCalledWith(TAKEOVER)
  })
})
