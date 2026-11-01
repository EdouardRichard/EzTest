import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

const visualApi = vi.hoisted(() => ({
  getVisualMetrics: vi.fn(),
}))
vi.mock("../api", () => visualApi)

const versionsApi = vi.hoisted(() => ({ listVersions: vi.fn() }))
vi.mock("@/features/versions/api", () => versionsApi)

const navigation = vi.hoisted(() => ({ replace: vi.fn() }))
vi.mock("next/navigation", () => ({ useRouter: () => navigation }))

import { versionSummaryFixture } from "@/features/versions/test-fixtures"

import { visualMetricsFixture } from "../test-fixtures"
import { VisualQualityPage } from "../VisualQualityPage"

describe("VisualQualityPage", () => {
  beforeEach(() => {
    visualApi.getVisualMetrics.mockReset()
    versionsApi.listVersions.mockReset()
    navigation.replace.mockReset()
    visualApi.getVisualMetrics.mockResolvedValue(visualMetricsFixture)
    versionsApi.listVersions.mockResolvedValue([
      versionSummaryFixture,
      { ...versionSummaryFixture, id: "2002", title: "订单中心 · 2027 Q1 迭代" },
    ])
  })

  afterEach(cleanup)

  it("loads the selected version metrics and switches scope through the URL", async () => {
    render(<VisualQualityPage initialVersionId="2001" />)

    expect(await screen.findByRole("heading", { name: "质量" })).toBeInTheDocument()
    expect(await screen.findByTestId("quality-metrics-panel")).toHaveTextContent("基线覆盖率")
    expect(visualApi.getVisualMetrics).toHaveBeenCalledWith("2001")

    const selector = await screen.findByLabelText("版本")
    expect(selector).toHaveValue("2001")

    fireEvent.change(selector, { target: { value: "" } })
    await waitFor(() => expect(visualApi.getVisualMetrics).toHaveBeenLastCalledWith(undefined))
    expect(navigation.replace).toHaveBeenCalledWith("/quality")

    fireEvent.change(selector, { target: { value: "2002" } })
    await waitFor(() => expect(visualApi.getVisualMetrics).toHaveBeenLastCalledWith("2002"))
    expect(navigation.replace).toHaveBeenCalledWith("/quality?version=2002")
  })

  it("falls back to global metrics when the version list fails", async () => {
    versionsApi.listVersions.mockRejectedValue(new Error("版本列表不可用"))

    render(<VisualQualityPage />)

    expect(await screen.findByText("版本列表不可用")).toBeInTheDocument()
    expect(await screen.findByTestId("quality-metrics-panel")).toBeInTheDocument()
    expect(visualApi.getVisualMetrics).toHaveBeenCalledWith(undefined)
  })
})
