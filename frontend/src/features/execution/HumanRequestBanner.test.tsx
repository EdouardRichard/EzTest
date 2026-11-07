import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { useState } from "react"
import { afterEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { HumanRequestBanner } from "./HumanRequestBanner"
import {
  approvalHumanRequestFixture,
  blockedRecoveryHumanRequestFixture,
  clarificationQuestionFixture,
  conflictHumanRequestFixture,
  humanRequestFixture,
} from "./test-fixtures"
import type { HumanRequest, HumanRequestResponseInput } from "./types"

const NOW = Date.parse("2026-10-31T12:00:00Z")

function Harness(props: {
  onResolve?: (id: string, response: HumanRequestResponseInput) => Promise<void>
  onCancel?: (id: string) => Promise<void>
  initial?: HumanRequest[]
  now?: number
}) {
  const [requests, setRequests] = useState<HumanRequest[]>(props.initial ?? [humanRequestFixture])
  const onResolve =
    props.onResolve ??
    (async () => {
      setRequests([])
    })
  const onCancel =
    props.onCancel ??
    (async () => {
      setRequests([])
    })

  return (
    <HumanRequestBanner
      requests={requests}
      now={props.now ?? NOW}
      onResolve={async (id, response) => {
        await onResolve(id, response)
        setRequests([])
      }}
      onCancel={async (id) => {
        await onCancel(id)
        setRequests([])
      }}
    />
  )
}

describe("HumanRequestBanner", () => {
  afterEach(cleanup)

  it("renders the reason, target SUT, account role and expiry countdown", () => {
    render(<Harness />)

    expect(screen.getByRole("alert")).toBeInTheDocument()
    expect(screen.getByText("登录接管")).toBeInTheDocument()
    expect(screen.getByText(/支付网关需要 2FA 验证码/)).toBeInTheDocument()
    expect(screen.getByText("系统 1002")).toBeInTheDocument()
    expect(screen.getByText("账号角色 admin")).toBeInTheDocument()
    expect(screen.getByText("https://pay.test/p3/login?mfa=1")).toBeInTheDocument()
    expect(screen.getByText("剩余 30m 00s")).toBeInTheDocument()
    expect(screen.getByLabelText("一次性验证码")).toBeInTheDocument()
  })

  it("submits the one-time code and disappears after a successful resolve", async () => {
    const onResolve = vi.fn().mockResolvedValue(undefined)
    render(<Harness onResolve={onResolve} />)

    fireEvent.change(screen.getByLabelText("一次性验证码"), { target: { value: "246810" } })
    fireEvent.click(screen.getByRole("button", { name: "完成" }))

    await waitFor(() => {
      expect(onResolve).toHaveBeenCalledWith("9501", { code: "246810" })
    })
    await waitFor(() => expect(screen.queryByRole("alert")).not.toBeInTheDocument())
  })

  it("cancels the request and disappears", async () => {
    const onCancel = vi.fn().mockResolvedValue(undefined)
    render(<Harness onCancel={onCancel} />)

    fireEvent.click(screen.getByRole("button", { name: "取消请求" }))

    await waitFor(() => expect(onCancel).toHaveBeenCalledWith("9501"))
    await waitFor(() => expect(screen.queryByRole("alert")).not.toBeInTheDocument())
  })

  it("shows an expired request as read-only", () => {
    render(<Harness now={Date.parse("2026-10-31T12:31:00Z")} />)

    expect(screen.getByText("已过期")).toBeInTheDocument()
    expect(screen.getByLabelText("一次性验证码")).toBeDisabled()
    expect(screen.getByRole("button", { name: "完成" })).toBeDisabled()
  })

  it("keeps the banner and shows the error when resolve fails", async () => {
    const onResolve = vi.fn().mockRejectedValue(new Error("验证码错误"))
    render(<Harness onResolve={onResolve} />)

    fireEvent.click(screen.getByRole("button", { name: "完成" }))

    expect(await screen.findByText("验证码错误")).toBeInTheDocument()
    expect(screen.getAllByRole("alert").length).toBeGreaterThan(0)
  })

  it("ignores a pending clarification question: it is not a login takeover", () => {
    render(<Harness initial={[clarificationQuestionFixture]} />)

    expect(screen.queryByRole("alert")).not.toBeInTheDocument()
    expect(screen.queryByText(/请确认退款口径/)).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "完成" })).not.toBeInTheDocument()
  })

  it("renders the takeover when a clarification question is pending too", () => {
    render(<Harness initial={[clarificationQuestionFixture, humanRequestFixture]} />)

    expect(screen.getByText("登录接管")).toBeInTheDocument()
    expect(screen.getByText(/支付网关需要 2FA 验证码/)).toBeInTheDocument()
    expect(screen.queryByText(/请确认退款口径/)).not.toBeInTheDocument()
  })
})

describe("HumanRequestBanner decision requests (R-W2-1)", () => {
  afterEach(cleanup)

  it("renders an approval and rejects with the typed reason", async () => {
    const onResolve = vi.fn().mockResolvedValue(undefined)
    render(
      <HumanRequestBanner
        requests={[approvalHumanRequestFixture]}
        onResolve={onResolve}
        onCancel={vi.fn()}
      />,
    )

    expect(screen.getByText("发布前请确认版本可以上线")).toBeInTheDocument()
    expect(screen.getByText("审批")).toBeInTheDocument()

    fireEvent.change(screen.getByLabelText("原因"), { target: { value: "no" } })
    fireEvent.click(screen.getByRole("button", { name: "拒绝" }))

    await waitFor(() => {
      expect(onResolve).toHaveBeenCalledWith("9601", { decision: "rejected", reason: "no" })
    })
  })

  it("approves blocked_recovery and conflict requests with the reason", async () => {
    const onResolve = vi.fn().mockResolvedValue(undefined)
    render(
      <HumanRequestBanner
        requests={[blockedRecoveryHumanRequestFixture, conflictHumanRequestFixture]}
        onResolve={onResolve}
        onCancel={vi.fn()}
      />,
    )

    expect(screen.getByText("阻塞恢复")).toBeInTheDocument()
    expect(screen.getByText("冲突处理")).toBeInTheDocument()
    expect(screen.getByText("执行器离线，确认恢复后继续")).toBeInTheDocument()
    expect(screen.getByText("需求与事实对退款口径的描述冲突")).toBeInTheDocument()

    const blocked = screen.getByTestId("human-decision-card-9602")
    fireEvent.change(within(blocked).getByLabelText("原因"), { target: { value: "ok" } })
    fireEvent.click(within(blocked).getByRole("button", { name: "批准" }))

    await waitFor(() => {
      expect(onResolve).toHaveBeenCalledWith("9602", { decision: "approved", reason: "ok" })
    })
  })

  it("requires a reason before a rejection is submitted", async () => {
    const onResolve = vi.fn().mockResolvedValue(undefined)
    render(
      <HumanRequestBanner
        requests={[approvalHumanRequestFixture]}
        onResolve={onResolve}
        onCancel={vi.fn()}
      />,
    )

    fireEvent.click(screen.getByRole("button", { name: "拒绝" }))

    expect(await screen.findByText("拒绝时必须填写原因")).toBeInTheDocument()
    expect(onResolve).not.toHaveBeenCalled()
  })

  it("renders decision requests read-only when the caller asks for it", () => {
    render(
      <HumanRequestBanner
        requests={[approvalHumanRequestFixture]}
        onResolve={vi.fn()}
        onCancel={vi.fn()}
        readOnly
      />,
    )

    expect(screen.getByLabelText("原因")).toBeDisabled()
    expect(screen.getByRole("button", { name: "批准" })).toBeDisabled()
    expect(screen.getByRole("button", { name: "拒绝" })).toBeDisabled()
  })

  it("never renders clarification questions as decision cards", () => {
    render(
      <HumanRequestBanner
        requests={[clarificationQuestionFixture]}
        onResolve={vi.fn()}
        onCancel={vi.fn()}
      />,
    )

    expect(screen.queryByTestId("human-decision-card-9503")).not.toBeInTheDocument()
    expect(screen.queryByText("澄清提问")).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "批准" })).not.toBeInTheDocument()
  })
})
