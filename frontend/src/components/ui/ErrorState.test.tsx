import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import { ErrorState } from "@/components/ui/ErrorState"

describe("ErrorState", () => {
  afterEach(cleanup)

  it("renders the failure copy with a retry action", () => {
    const onRetry = vi.fn()
    render(<ErrorState title="加载版本失败" message="无法连接 API 服务" onRetry={onRetry} />)

    const alert = screen.getByRole("alert")
    expect(alert.textContent).toContain("加载版本失败")
    expect(alert.textContent).toContain("无法连接 API 服务")

    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(onRetry).toHaveBeenCalledTimes(1)
  })

  it("surfaces the request id for support correlation", () => {
    render(<ErrorState title="加载失败" message="服务不可用" requestId="req-123" />)

    expect(screen.getByText("请求 ID：req-123")).toBeTruthy()
    expect(screen.queryByRole("button")).toBeNull()
  })

  it("accepts a custom retry label", () => {
    render(
      <ErrorState title="加载失败" message="服务不可用" retryLabel="重新加载" onRetry={vi.fn()} />,
    )

    expect(screen.getByRole("button", { name: "重新加载" })).toBeTruthy()
  })
})
