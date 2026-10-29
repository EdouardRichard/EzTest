import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

const delivery = vi.hoisted(() => ({
  issuesProps: vi.fn(),
  reportsProps: vi.fn(),
}))

const reportState = vi.hoisted(() => ({
  report: null as null | { content: { degraded: boolean } },
}))

vi.mock("@/features/delivery/hooks/useReports", () => ({
  useReports: () => ({
    reports: reportState.report === null ? [] : [{ id: "1844674407370955501" }],
    loading: false,
    error: "",
  }),
  useReport: () => ({ report: reportState.report, loading: false, error: "" }),
}))

vi.mock("@/features/delivery/components/IssuesTab", () => ({
  IssuesTab: (props: {
    versionId: string
    stream?: { events: unknown[] }
    issueId?: string
    onIssueChange?: (issueId: string) => void
    degraded?: boolean
  }) => {
    delivery.issuesProps(props)
    return <div data-testid="issues-tab">issues for {props.versionId}</div>
  },
}))
vi.mock("@/features/delivery/components/ReportsTab", () => ({
  ReportsTab: (props: {
    versionId: string
    stream?: { events: unknown[] }
    reportId?: string
    onReportChange?: (reportId: string) => void
    batchId?: string
    onBatchChange?: (batchId: string) => void
  }) => {
    delivery.reportsProps(props)
    return <div data-testid="report-tab">reports for {props.versionId}</div>
  },
}))

import { VERSION_TABS, VersionTabs } from "./VersionTabs"

const VERSION_ID = "1844674407370955161"

const CONTENT = {
  requirements: <div data-testid="requirements-slot" />,
  overview: <div data-testid="overview-slot" />,
}

const STREAM = { events: [], status: "live" as const }

describe("VersionTabs", () => {
  afterEach(() => {
    cleanup()
    reportState.report = null
  })

  it("keeps the ten tabs in spec order and mounts the delivery issues tab instead of the placeholder", () => {
    render(
      <VersionTabs
        versionId={VERSION_ID}
        active="issues"
        onTabChange={vi.fn()}
        {...CONTENT}
        stream={STREAM}
      />,
    )

    expect(screen.getAllByRole("tab")).toHaveLength(10)
    // Spec §7.1: the mindmap tab sits right after requirements.
    expect(VERSION_TABS.map((tab) => tab.label)).toEqual([
      "需求与澄清",
      "用例脑图",
      "总览",
      "执行会话",
      "探索链路",
      "实时播报",
      "结果与证据",
      "问题",
      "报告",
      "代码与变更",
    ])
    expect(screen.getAllByRole("tab").map((tab) => tab.textContent)).toEqual(
      VERSION_TABS.map((tab) => tab.label),
    )

    expect(screen.getByTestId("issues-tab").textContent).toContain(VERSION_ID)
    expect(delivery.issuesProps).toHaveBeenCalledWith(
      expect.objectContaining({ versionId: VERSION_ID, stream: STREAM }),
    )
    expect(screen.queryByText("阶段 P7")).toBeNull()
    expect(screen.queryByText("该 Tab 尚未接入数据，不展示占位假数据。")).toBeNull()
  })

  it("mounts the delivery reports tab instead of the P6 report slot", () => {
    render(
      <VersionTabs
        versionId={VERSION_ID}
        active="report"
        onTabChange={vi.fn()}
        {...CONTENT}
        stream={STREAM}
      />,
    )

    expect(screen.getByTestId("report-tab").textContent).toContain(VERSION_ID)
    expect(delivery.reportsProps).toHaveBeenCalledWith(
      expect.objectContaining({ versionId: VERSION_ID, stream: STREAM }),
    )
  })

  it("forwards the controlled issue deep link and the latest-report degraded hint", () => {
    reportState.report = { content: { degraded: true } }
    const onIssueChange = vi.fn()
    render(
      <VersionTabs
        versionId={VERSION_ID}
        active="issues"
        onTabChange={vi.fn()}
        {...CONTENT}
        stream={STREAM}
        issueId="1844674407370955410"
        onIssueChange={onIssueChange}
      />,
    )

    expect(delivery.issuesProps).toHaveBeenLastCalledWith(
      expect.objectContaining({
        versionId: VERSION_ID,
        issueId: "1844674407370955410",
        onIssueChange,
        degraded: true,
      }),
    )
  })

  it("forwards the controlled report id and batch selection to the reports tab", () => {
    const onReportChange = vi.fn()
    const onBatchChange = vi.fn()
    render(
      <VersionTabs
        versionId={VERSION_ID}
        active="report"
        onTabChange={vi.fn()}
        {...CONTENT}
        stream={STREAM}
        reportId="1844674407370955501"
        onReportChange={onReportChange}
        batchId="1844674407370955171"
        onBatchChange={onBatchChange}
      />,
    )

    expect(delivery.reportsProps).toHaveBeenLastCalledWith(
      expect.objectContaining({
        versionId: VERSION_ID,
        reportId: "1844674407370955501",
        onReportChange,
        batchId: "1844674407370955171",
        onBatchChange,
      }),
    )
  })

  it("keeps the container-owned slots for requirements, overview and the P6 tabs", () => {
    const onTabChange = vi.fn()
    const { rerender } = render(
      <VersionTabs
        versionId={VERSION_ID}
        active="requirements"
        onTabChange={onTabChange}
        {...CONTENT}
        sessions={<div data-testid="sessions-slot" />}
      />,
    )

    expect(screen.getByTestId("requirements-slot")).toBeTruthy()

    fireEvent.mouseDown(screen.getByRole("tab", { name: "执行会话" }))
    expect(onTabChange).toHaveBeenCalledWith("sessions")

    rerender(
      <VersionTabs
        versionId={VERSION_ID}
        active="sessions"
        onTabChange={onTabChange}
        {...CONTENT}
        sessions={<div data-testid="sessions-slot" />}
      />,
    )
    expect(screen.getByTestId("sessions-slot")).toBeTruthy()
  })

  it("mounts the embedded mindmap slot under the version-tab-mindmap test id", () => {
    const onTabChange = vi.fn()
    const { rerender } = render(
      <VersionTabs
        versionId={VERSION_ID}
        active="requirements"
        onTabChange={onTabChange}
        {...CONTENT}
        mindmap={<div data-testid="mindmap-slot">脑图内容</div>}
      />,
    )

    fireEvent.mouseDown(screen.getByRole("tab", { name: "用例脑图" }))
    expect(onTabChange).toHaveBeenCalledWith("mindmap")

    rerender(
      <VersionTabs
        versionId={VERSION_ID}
        active="mindmap"
        onTabChange={onTabChange}
        {...CONTENT}
        mindmap={<div data-testid="mindmap-slot">脑图内容</div>}
      />,
    )
    expect(screen.getByRole("tab", { name: "用例脑图" }).getAttribute("aria-selected")).toBe("true")
    const panel = screen.getByTestId("version-tab-mindmap")
    expect(within(panel).getByTestId("mindmap-slot")).toBeTruthy()
  })
})
