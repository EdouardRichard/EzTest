import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import {
  ChainDegraded,
  ChainEmpty,
  ChainError,
  ChainSkeleton,
  ChainStreamStatus,
  ChainTruncated,
} from "./ChainStates"

afterEach(() => {
  cleanup()
})

describe("ChainStates", () => {
  it("renders a loading skeleton with a status role", () => {
    render(<ChainSkeleton />)

    const skeleton = screen.getByTestId("chain-skeleton")
    expect(skeleton.getAttribute("role")).toBe("status")
    expect(skeleton.textContent).toContain("加载")
  })

  it("separates the empty version from the filtered-empty result", () => {
    const { rerender } = render(<ChainEmpty />)
    expect(screen.getByTestId("exploration-empty").textContent).toContain("暂无探索链路")

    rerender(<ChainEmpty filtered />)
    expect(screen.queryByTestId("exploration-empty")).toBeNull()
    expect(screen.getByTestId("exploration-filtered-empty").textContent).toContain(
      "没有符合筛选条件",
    )
  })

  it("names the failure and offers a retry", () => {
    const onRetry = vi.fn()
    render(<ChainError message="limit must be an integer" onRetry={onRetry} />)

    const error = screen.getByTestId("exploration-error")
    expect(error.getAttribute("role")).toBe("alert")
    expect(error.textContent).toContain("limit must be an integer")

    fireEvent.click(screen.getByTestId("chain-error-retry"))
    expect(onRetry).toHaveBeenCalledTimes(1)
  })

  it("keeps the previous data visible as a degraded banner", () => {
    const onRetry = vi.fn()
    render(<ChainDegraded message="无权限读取问题" onRetry={onRetry} />)

    const degraded = screen.getByTestId("exploration-degraded")
    expect(degraded.getAttribute("role")).toBe("alert")
    expect(degraded.textContent).toContain("保留上次数据")
    expect(degraded.textContent).toContain("无权限读取问题")

    fireEvent.click(screen.getByTestId("chain-degraded-retry"))
    expect(onRetry).toHaveBeenCalledTimes(1)
  })

  it("shows the real pre-truncation counts in the truncation banner", () => {
    render(<ChainTruncated shown={3} total={91} layers={["用例", "结果"]} />)

    const banner = screen.getByTestId("chain-truncated")
    expect(banner.getAttribute("role")).toBe("status")
    expect(banner.textContent).toContain("已显示前 3 条")
    expect(banner.textContent).toContain("共 91 条")
    expect(banner.textContent).toContain("请收窄筛选")
    expect(banner.textContent).toContain("用例、结果")
  })

  it("marks the stream as 重连中 while it reconnects and 未连接 when closed", () => {
    const { rerender } = render(<ChainStreamStatus status="reconnecting" />)
    const status = screen.getByTestId("chain-stream-status")
    expect(status.getAttribute("data-state")).toBe("reconnecting")
    expect(status.textContent).toContain("重连中")
    expect(status.getAttribute("role")).toBe("status")

    rerender(<ChainStreamStatus status="connecting" />)
    expect(screen.getByTestId("chain-stream-status").textContent).toContain("重连中")

    rerender(<ChainStreamStatus status="live" />)
    expect(screen.getByTestId("chain-stream-status").textContent).toContain("实时")

    rerender(<ChainStreamStatus status="closed" />)
    expect(screen.getByTestId("chain-stream-status").textContent).toContain("未连接")
  })
})
