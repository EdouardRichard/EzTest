import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"
import { formatDateTime } from "@/lib/format"

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
  usePathname: () => "/versions",
}))

import { catalogFixture, versionFixture, versionSummaryFixture } from "./test-fixtures"
import { VersionsPage } from "./VersionsPage"

describe("VersionsPage", () => {
  beforeEach(() => {
    api.listVersions.mockReset()
    api.getResourceCatalog.mockReset()
    api.createVersion.mockReset()
    api.listVersions.mockResolvedValue([versionSummaryFixture])
    api.getResourceCatalog.mockResolvedValue(catalogFixture)
    api.createVersion.mockResolvedValue(versionFixture)
    navigation.push.mockReset()
  })

  afterEach(cleanup)

  it("lists versions with status, SUT count and timestamps", async () => {
    render(<VersionsPage />)

    const link = await screen.findByRole("link", { name: /订单中心 · 2026 Q4 迭代/ })
    expect(link).toHaveAttribute("href", "/versions/2001?tab=overview")
    expect(screen.getByText("运行中")).toBeInTheDocument()
    expect(screen.getByText("2 个 SUT")).toBeInTheDocument()
    expect(screen.getAllByText(formatDateTime(versionSummaryFixture.updated_at))).toHaveLength(2)
  })

  it("shows the empty state, opens the create dialog and navigates to the new version", async () => {
    api.listVersions.mockResolvedValue([])
    render(<VersionsPage />)

    expect(await screen.findByText("还没有版本")).toBeInTheDocument()
    fireEvent.click(screen.getAllByRole("button", { name: "新建版本" })[0] as HTMLElement)

    await screen.findByRole("dialog", { name: "新建版本" })
    await screen.findByLabelText("SUT")
    fireEvent.change(screen.getByLabelText("版本标题"), { target: { value: "新迭代" } })
    fireEvent.change(screen.getByLabelText("SUT"), { target: { value: "1001" } })
    fireEvent.change(screen.getByLabelText("环境"), { target: { value: "1101" } })
    fireEvent.click(screen.getByRole("button", { name: "创建版本" }))

    await waitFor(() => expect(navigation.push).toHaveBeenCalledWith("/versions/2001?tab=overview"))
  })

  it("shows a retryable list error", async () => {
    api.listVersions
      .mockRejectedValueOnce(
        new ApiError({
          status: 503,
          code: "unavailable",
          message: "版本列表暂不可用",
          requestId: "req_versions",
          retryable: true,
        }),
      )
      .mockResolvedValueOnce([versionSummaryFixture])

    render(<VersionsPage />)

    expect(await screen.findByText("版本列表暂不可用")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))

    expect(await screen.findByRole("link", { name: /订单中心 · 2026 Q4 迭代/ })).toBeInTheDocument()
    expect(api.listVersions).toHaveBeenCalledTimes(2)
  })
})
