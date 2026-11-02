import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { useState } from "react"
import { afterEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { HumanRequestBanner } from "./HumanRequestBanner"
import { humanRequestFixture } from "./test-fixtures"
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
})
