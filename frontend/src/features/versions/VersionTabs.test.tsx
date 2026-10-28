import { cleanup, fireEvent, render, screen } from "@testing-library/react"
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

  it("keeps the nine tabs and mounts the delivery issues tab instead of the placeholder", () => {
    render(
      <VersionTabs
        versionId={VERSION_ID}
        active="issues"
        onTabChange={vi.fn()}
        {...CONTENT}
        stream={STREAM}
      />,
    )

    expect(screen.getAllByRole("tab")).toHaveLength(9)
    expect(VERSION_TABS.map((tab) => tab.label)).toContain("问题")
    expect(VERSION_TABS.map((tab) => tab.label)).toContain("报告")

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
})
