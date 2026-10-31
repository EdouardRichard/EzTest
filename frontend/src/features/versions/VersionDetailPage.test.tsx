import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

const api = vi.hoisted(() => ({
  listVersions: vi.fn(),
  getVersion: vi.fn(),
  createVersion: vi.fn(),
  getResourceCatalog: vi.fn(),
}))
vi.mock("./api", () => api)

const navigation = vi.hoisted(() => ({
  push: vi.fn(),
  replace: vi.fn(),
  refresh: vi.fn(),
  back: vi.fn(),
  forward: vi.fn(),
  prefetch: vi.fn(),
}))
vi.mock("next/navigation", () => ({
  useRouter: () => navigation,
  usePathname: () => "/versions/2001",
}))

import { versionFixture } from "./test-fixtures"
import { VersionDetailPage } from "./VersionDetailPage"

describe("VersionDetailPage", () => {
  beforeEach(() => {
    api.getVersion.mockReset()
    api.getVersion.mockResolvedValue(versionFixture)
    navigation.replace.mockReset()
  })

  afterEach(cleanup)

  it("renders the header, SUT chips, all eight tabs and the binding overview", async () => {
    render(<VersionDetailPage versionId="2001" />)

    expect(
      await screen.findByRole("heading", { name: "订单中心 · 2026 Q4 迭代" }),
    ).toBeInTheDocument()
    expect(api.getVersion).toHaveBeenCalledWith("2001")

    expect(screen.getByText("运行中")).toBeInTheDocument()
    expect(screen.getByText("订单中心")).toBeInTheDocument()
    expect(screen.getByText("支付网关")).toBeInTheDocument()

    expect(screen.getAllByRole("tab")).toHaveLength(8)
    expect(screen.getByRole("tab", { name: "总览" })).toHaveAttribute("aria-selected", "true")

    const overview = screen.getByRole("region", { name: "绑定概览" })
    expect(overview).toHaveTextContent("测试环境")
    expect(overview).toHaveTextContent("https://order.test")
    expect(overview).toHaveTextContent("2 个页面")
    expect(overview).toHaveTextContent("1 个 API")
    expect(overview).toHaveTextContent("1 个目标库")
    expect(overview).toHaveTextContent("2 个测试账号")
    expect(overview).toHaveTextContent("order-center-web")
    expect(overview).toHaveTextContent("未绑定代码仓")
  })

  it("honours a deep-linked tab and renders the P1 placeholder", async () => {
    render(<VersionDetailPage versionId="2001" initialTab="execution" />)

    await screen.findByRole("heading", { name: "订单中心 · 2026 Q4 迭代" })

    expect(screen.getByRole("tab", { name: "执行" })).toHaveAttribute("aria-selected", "true")
    expect(screen.getByText("执行将在 P1 阶段提供")).toBeInTheDocument()
    expect(screen.queryByRole("region", { name: "绑定概览" })).not.toBeInTheDocument()
  })

  it("falls back to the overview tab for an unknown deep link", async () => {
    render(<VersionDetailPage versionId="2001" initialTab={"unknown" as never} />)

    await screen.findByRole("heading", { name: "订单中心 · 2026 Q4 迭代" })
    expect(screen.getByRole("tab", { name: "总览" })).toHaveAttribute("aria-selected", "true")
  })

  it("switches tabs, updates the URL and shows the matching placeholder", async () => {
    render(<VersionDetailPage versionId="2001" />)
    await screen.findByRole("heading", { name: "订单中心 · 2026 Q4 迭代" })

    fireEvent.click(screen.getByRole("tab", { name: "用例设计" }))

    expect(navigation.replace).toHaveBeenCalledWith("/versions/2001?tab=design")
    expect(screen.getByRole("tab", { name: "用例设计" })).toHaveAttribute("aria-selected", "true")
    expect(screen.getByText("用例设计将在 P1 阶段提供")).toBeInTheDocument()
  })

  it("shows a retryable error state when the version cannot be loaded", async () => {
    api.getVersion
      .mockRejectedValueOnce(
        new ApiError({
          status: 404,
          code: "not_found",
          message: "版本不存在",
          requestId: "req_version",
        }),
      )
      .mockResolvedValueOnce(versionFixture)

    render(<VersionDetailPage versionId="2001" />)

    expect(await screen.findByText("版本不存在")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))

    await screen.findByRole("heading", { name: "订单中心 · 2026 Q4 迭代" })
    expect(api.getVersion).toHaveBeenCalledTimes(2)
  })
})
