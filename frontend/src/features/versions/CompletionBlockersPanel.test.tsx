import { cleanup, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import type { Requirement } from "@/features/clarification/types"
import type { ReportView } from "@/features/delivery/types"
import type { VersionStage } from "@/features/design/types"

import { CompletionBlockersPanel } from "./CompletionBlockersPanel"
import type { Version } from "./types"

const clarificationApi = vi.hoisted(() => ({ listRequirements: vi.fn() }))
vi.mock("@/features/clarification/api", () => clarificationApi)

const deliveryApi = vi.hoisted(() => ({ listReports: vi.fn() }))
vi.mock("@/features/delivery/api", () => deliveryApi)

const VERSION_ID = "378400925858922496"
const UPDATED_AT = "2026-11-10T12:00:00Z"

const REPORTED_COUNTS = { total: 12, draft: 0, clarified: 1, closed: 11, obsolete: 0 }
const CLOSED_COUNTS = { total: 12, draft: 0, clarified: 0, closed: 12, obsolete: 0 }

function versionFixture(overrides: Partial<Version> = {}): Version {
  return {
    id: VERSION_ID,
    title: "R2-MAIN",
    status: "running",
    auto_design_new_requirements: false,
    bindings: [],
    requirement_counts: REPORTED_COUNTS,
    created_by: "9001",
    created_at: "2026-11-01T09:00:00Z",
    updated_at: UPDATED_AT,
    ...overrides,
  }
}

function requirementFixture(overrides: Partial<Requirement> = {}): Requirement {
  return {
    id: "6001",
    current_version_id: "6101",
    revision: 1,
    title: "素材上传与光标插入",
    status: "clarified",
    priority: "P2",
    targets: { version_suts: [], pages: [], apis: [] },
    updated_at: "2026-11-10T11:00:00Z",
    confirmed_at: null,
    ...overrides,
  }
}

function reportFixture(overrides: Partial<ReportView> = {}): ReportView {
  return {
    id: "8101",
    version_id: VERSION_ID,
    kind: "version",
    status: "published",
    current_revision: {
      id: "8201",
      report_id: "8101",
      revision: 1,
      status: "published",
      as_of: "2026-11-10T11:30:00Z",
      input_fingerprint: "fingerprint",
      template_version: "v1",
      render_status: "done",
      html_available: true,
      pdf_available: true,
      pdf_status: "done",
      created_at: "2026-11-10T11:30:00Z",
    },
    created_at: "2026-11-10T11:30:00Z",
    updated_at: "2026-11-10T11:30:00Z",
    ...overrides,
  }
}

type PanelOverrides = {
  version?: Version
  stage?: VersionStage | null
  pendingQuestions?: number
  pendingDecisions?: number
  refreshKey?: string
}

function renderPanel(overrides: PanelOverrides = {}) {
  const props = {
    version: overrides.version ?? versionFixture(),
    stage: overrides.stage === undefined ? "reporting" : overrides.stage,
    pendingQuestions: overrides.pendingQuestions ?? 0,
    pendingDecisions: overrides.pendingDecisions ?? 0,
    refreshKey: overrides.refreshKey ?? `${UPDATED_AT}|1`,
  }
  return render(<CompletionBlockersPanel {...props} />)
}

describe("CompletionBlockersPanel", () => {
  beforeEach(() => {
    clarificationApi.listRequirements.mockReset()
    clarificationApi.listRequirements.mockResolvedValue([])
    deliveryApi.listReports.mockReset()
    deliveryApi.listReports.mockResolvedValue([])
  })

  afterEach(cleanup)

  it("explains why a reporting version is blocked on the open requirement", async () => {
    clarificationApi.listRequirements.mockImplementation(
      (_versionId: string, options: { status?: string }) =>
        Promise.resolve(options.status === "clarified" ? [requirementFixture()] : []),
    )
    deliveryApi.listReports.mockResolvedValue([reportFixture()])

    renderPanel()

    const panel = await screen.findByTestId("completion-readiness")
    expect(panel).toHaveTextContent("完成条件")
    await waitFor(() => expect(panel).toHaveTextContent("等待需求闭环"))
    await waitFor(() =>
      expect(screen.getByTestId("completion-report")).toHaveTextContent(
        "已发布（版本总结 v1，PDF 已生成）",
      ),
    )
    expect(screen.getByTestId("completion-requirements")).toHaveTextContent("已闭环 11/12")

    const row = await screen.findByTestId("completion-unclosed-requirement")
    expect(row).toHaveTextContent("素材上传与光标插入")
    expect(row).toHaveTextContent("已澄清")
    expect(within(row).getByRole("link")).toHaveAttribute(
      "href",
      `/versions/${VERSION_ID}?tab=requirements&requirement=6001`,
    )
    expect(within(panel).getByRole("link", { name: "去需求与澄清 →" })).toHaveAttribute(
      "href",
      `/versions/${VERSION_ID}?tab=requirements`,
    )
    expect(screen.getByTestId("completion-next-step")).toHaveTextContent("版本完成要求全部需求闭环")
    expect(clarificationApi.listRequirements).toHaveBeenCalledWith(VERSION_ID, {
      status: "draft",
      limit: 100,
    })
    expect(clarificationApi.listRequirements).toHaveBeenCalledWith(VERSION_ID, {
      status: "clarified",
      limit: 100,
    })
  })

  it("does not render outside the reporting stage", () => {
    for (const stage of ["created", "clarifying", "executing", "completed", null] as const) {
      renderPanel({ stage })
      expect(screen.queryByTestId("completion-readiness")).not.toBeInTheDocument()
      cleanup()
    }
    expect(clarificationApi.listRequirements).not.toHaveBeenCalled()
    expect(deliveryApi.listReports).not.toHaveBeenCalled()
  })

  it("does not render when the reporting version is no longer running", () => {
    renderPanel({ version: versionFixture({ status: "completed" }) })

    expect(screen.queryByTestId("completion-readiness")).not.toBeInTheDocument()
  })

  it("shows 即将完成 when every requirement is closed and the report is published", async () => {
    deliveryApi.listReports.mockResolvedValue([reportFixture()])

    renderPanel({ version: versionFixture({ requirement_counts: CLOSED_COUNTS }) })

    const panel = await screen.findByTestId("completion-readiness")
    await waitFor(() => expect(panel).toHaveTextContent("即将完成"))
    expect(screen.getByTestId("completion-requirements")).toHaveTextContent("已闭环 12/12")
    expect(screen.getByTestId("completion-next-step")).toHaveTextContent(
      "通常 1 分钟内会自动完成版本",
    )
    expect(screen.queryByTestId("completion-unclosed-requirement")).not.toBeInTheDocument()
  })

  it("waits for the report when the version summary is still a draft", async () => {
    deliveryApi.listReports.mockResolvedValue([
      reportFixture({ status: "draft", current_revision: null }),
    ])

    renderPanel({ version: versionFixture({ requirement_counts: CLOSED_COUNTS }) })

    const panel = await screen.findByTestId("completion-readiness")
    await waitFor(() => expect(panel).toHaveTextContent("等待报告发布"))
    expect(screen.getByTestId("completion-report")).toHaveTextContent("未发布")
    expect(screen.getByTestId("completion-next-step")).toHaveTextContent(
      "报告生成并发布后，版本会自动进入完成。",
    )
  })

  it("asks for a republish when the version report is stale", async () => {
    deliveryApi.listReports.mockResolvedValue([reportFixture({ status: "stale" })])

    renderPanel({ version: versionFixture({ requirement_counts: CLOSED_COUNTS }) })

    const panel = await screen.findByTestId("completion-readiness")
    await waitFor(() => expect(panel).toHaveTextContent("等待报告发布"))
    expect(screen.getByTestId("completion-report")).toHaveTextContent("已过期（需重新发布）")
  })

  it("keeps the version counts and guidance when the requirement detail request fails", async () => {
    clarificationApi.listRequirements.mockRejectedValue(new Error("requirements unavailable"))
    deliveryApi.listReports.mockResolvedValue([reportFixture()])

    renderPanel()

    const panel = await screen.findByTestId("completion-readiness")
    await waitFor(() => expect(panel).toHaveTextContent("等待需求闭环"))
    expect(screen.getByTestId("completion-requirements")).toHaveTextContent("已闭环 11/12")
    expect(screen.getByTestId("completion-next-step")).toHaveTextContent("版本完成要求全部需求闭环")
    expect(await within(panel).findByRole("alert")).toHaveTextContent("未闭环需求详情加载失败")
  })

  it("keeps rendering when the report request fails", async () => {
    clarificationApi.listRequirements.mockResolvedValue([])
    deliveryApi.listReports.mockRejectedValue(new Error("reports unavailable"))

    renderPanel()

    const panel = await screen.findByTestId("completion-readiness")
    await waitFor(() => expect(panel).toHaveTextContent("等待报告发布"))
    await waitFor(() =>
      expect(screen.getByTestId("completion-report")).toHaveTextContent("状态未知"),
    )
    expect(within(panel).getByRole("alert")).toHaveTextContent("报告状态读取失败")
  })

  it("links pending questions and human decisions to the overview", async () => {
    deliveryApi.listReports.mockResolvedValue([reportFixture()])

    renderPanel({ pendingQuestions: 2, pendingDecisions: 1 })

    const questions = await screen.findByTestId("completion-pending-questions")
    expect(questions).toHaveTextContent("2")
    expect(within(questions).getByRole("link")).toHaveAttribute(
      "href",
      `/versions/${VERSION_ID}?tab=overview`,
    )
    const decisions = screen.getByTestId("completion-pending-decisions")
    expect(decisions).toHaveTextContent("1")
    expect(within(decisions).getByRole("link")).toHaveAttribute(
      "href",
      `/versions/${VERSION_ID}?tab=overview`,
    )
  })

  it("does not render pending rows when both counts are zero", async () => {
    deliveryApi.listReports.mockResolvedValue([reportFixture()])

    renderPanel()

    await screen.findByTestId("completion-readiness")
    expect(screen.queryByTestId("completion-pending-questions")).not.toBeInTheDocument()
    expect(screen.queryByTestId("completion-pending-decisions")).not.toBeInTheDocument()
  })

  it("shows 等 N 条 when more requirements are unclosed than were fetched", async () => {
    clarificationApi.listRequirements.mockImplementation(
      (_versionId: string, options: { status?: string }) =>
        Promise.resolve(
          options.status === "clarified"
            ? [requirementFixture()]
            : [requirementFixture({ id: "6002", title: "素材上传失败提示", status: "draft" })],
        ),
    )
    deliveryApi.listReports.mockResolvedValue([reportFixture()])

    renderPanel({
      version: versionFixture({
        requirement_counts: { total: 12, draft: 1, clarified: 2, closed: 9, obsolete: 0 },
      }),
    })

    const requirements = await screen.findByTestId("completion-requirements")
    await waitFor(() => expect(requirements).toHaveTextContent("已闭环 9/12"))
    expect(await screen.findAllByTestId("completion-unclosed-requirement")).toHaveLength(2)
    expect(requirements).toHaveTextContent("等 1 条")
  })

  it("refetches requirements and reports when refreshKey changes", async () => {
    deliveryApi.listReports.mockResolvedValue([reportFixture()])

    const view = renderPanel({ refreshKey: "a" })
    await screen.findByTestId("completion-readiness")
    await waitFor(() => expect(clarificationApi.listRequirements).toHaveBeenCalledTimes(2))
    const reportCalls = deliveryApi.listReports.mock.calls.length

    view.rerender(
      <CompletionBlockersPanel
        version={versionFixture()}
        stage="reporting"
        pendingQuestions={0}
        pendingDecisions={0}
        refreshKey="b"
      />,
    )

    await waitFor(() => expect(clarificationApi.listRequirements).toHaveBeenCalledTimes(4))
    await waitFor(() => expect(deliveryApi.listReports).toHaveBeenCalledTimes(reportCalls + 1))
  })
})
