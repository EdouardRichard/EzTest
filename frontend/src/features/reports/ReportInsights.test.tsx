import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import type { Version } from "@/features/versions/types"

const versionsApi = vi.hoisted(() => ({ getVersion: vi.fn() }))
vi.mock("@/features/versions/api", () => versionsApi)

const deliveryApi = vi.hoisted(() => ({ downloadHTML: vi.fn() }))
vi.mock("@/features/delivery/api", () => deliveryApi)

const executionApi = vi.hoisted(() => ({ downloadEvidence: vi.fn() }))
vi.mock("@/features/execution/api", () => executionApi)
vi.mock("@/features/execution/EvidenceViewer", () => ({ revokeSoon: vi.fn() }))

const reportsApi = vi.hoisted(() => ({ waiveClose: vi.fn(), waiveRequirement: vi.fn() }))
vi.mock("./api", () => reportsApi)

const clarificationApi = vi.hoisted(() => ({ listRequirements: vi.fn() }))
vi.mock("@/features/clarification/api", () => clarificationApi)

const admin = vi.hoisted(() => ({ value: false }))
vi.mock("@/features/auth/useIsAdmin", () => ({ useIsAdmin: () => admin.value }))

import { ReportInsights } from "./ReportInsights"

const HTML = `<!doctype html><html><body><main data-template-version="3">
<section data-section="issues"><article class="defect-card" data-issue-id="9001">
<div class="card-element" data-element="requirement"><h4>① 关联需求</h4><p>需求 #7001</p></div>
<div class="card-element" data-element="actual"><h4>⑤ 实际结果</h4><a class="evidence-link" href="/api/versions/2001/evidence/5001/content">查看证据 #5001</a></div>
</article></section></main></body></html>`

function versionFixture(overrides: Partial<Version> = {}): Version {
  return {
    id: "2001",
    title: "订单系统 Q4",
    status: "running",
    auto_design_new_requirements: false,
    environment_id: "1001",
    binding: {
      environment: { id: "1001", name: "测试环境", status: "active" } as never,
      pages: [],
      apis: [],
      databases: [],
      accounts: [],
      repositories: [],
    },
    created_by: "1",
    created_at: "2026-11-01T00:00:00Z",
    updated_at: "2026-11-01T00:00:00Z",
    ...overrides,
  }
}

function renderInsights(overrides: Partial<Parameters<typeof ReportInsights>[0]> = {}): void {
  render(
    <ReportInsights
      versionId="2001"
      reportId="8101"
      revisionNumber={2}
      htmlAvailable
      onChanged={vi.fn()}
      {...overrides}
    />,
  )
}

describe("ReportInsights wiring", () => {
  beforeEach(() => {
    admin.value = false
    versionsApi.getVersion.mockResolvedValue(versionFixture())
    deliveryApi.downloadHTML.mockResolvedValue({
      blob: new Blob([HTML], { type: "text/html" }),
      filename: "report.html",
      contentType: "text/html",
    })
    executionApi.downloadEvidence.mockResolvedValue({
      blob: new Blob(["png"], { type: "image/png" }),
      filename: "evidence.png",
      contentType: "image/png",
    })
    clarificationApi.listRequirements.mockResolvedValue([])
    vi.stubGlobal("URL", Object.assign(URL, { createObjectURL: vi.fn(() => "blob:report") }))
  })
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it("renders the authoritative report HTML and the unverified badge for a waived version", async () => {
    versionsApi.getVersion.mockResolvedValue(
      versionFixture({ status: "completed", closure_kind: "waived", unverified: true }),
    )
    renderInsights()
    await waitFor(() => expect(screen.getByTestId("unverified-badge")).toBeInTheDocument())
    await waitFor(() => {
      const root = screen.getByTestId("report-content").shadowRoot
      expect(root?.querySelector("article.defect-card[data-issue-id='9001']")).not.toBeNull()
    })
    expect(deliveryApi.downloadHTML).toHaveBeenCalledWith("2001", "8101", 2)
  })

  it("shows no badge and no waiver operation for a normal version viewed by a user", async () => {
    renderInsights()
    await waitFor(() => expect(screen.getByTestId("report-content")).toBeInTheDocument())
    expect(screen.queryByTestId("unverified-badge")).not.toBeInTheDocument()
    expect(screen.queryByTestId("waiver-panel")).not.toBeInTheDocument()
  })

  it("opens report evidence through the authenticated content download", async () => {
    const openSpy = vi.spyOn(window, "open").mockImplementation(() => null)
    renderInsights()
    await waitFor(() => {
      const root = screen.getByTestId("report-content").shadowRoot
      expect(root?.querySelector("a.evidence-link")).not.toBeNull()
    })
    const link = screen
      .getByTestId("report-content")
      .shadowRoot?.querySelector("a.evidence-link") as HTMLAnchorElement
    fireEvent.click(link)
    await waitFor(() => expect(executionApi.downloadEvidence).toHaveBeenCalledWith("2001", "5001"))
    await waitFor(() => expect(openSpy).toHaveBeenCalled())
    openSpy.mockRestore()
  })

  it("refreshes the version and notifies the parent after an admin waive-close", async () => {
    admin.value = true
    reportsApi.waiveClose.mockResolvedValue({
      version_id: "2001",
      status: "completed",
      closure_kind: "waived",
      unverified: true,
      close_reason: "剩余风险已评估",
      report_id: "8101",
      report_revision: 3,
    })
    const onChanged = vi.fn()
    renderInsights({ onChanged })
    fireEvent.click(await screen.findByTestId("waive-close-button"))
    fireEvent.change(screen.getByLabelText(/豁免理由/), { target: { value: "剩余风险已评估" } })
    fireEvent.click(screen.getByRole("button", { name: "确认豁免关闭" }))
    await waitFor(() => expect(onChanged).toHaveBeenCalled())
    await waitFor(() => expect(versionsApi.getVersion).toHaveBeenCalledTimes(2))
  })

  it("shows an empty report body when the revision HTML is unavailable", async () => {
    renderInsights({ htmlAvailable: false })
    await waitFor(() => expect(screen.getByTestId("report-content-empty")).toBeInTheDocument())
    expect(deliveryApi.downloadHTML).not.toHaveBeenCalled()
  })
})
