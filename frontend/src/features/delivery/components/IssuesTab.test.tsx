import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { ApiError } from "@/lib/api-client"

import type { IssueDetail, IssueSummary } from "../types"

const mocks = vi.hoisted(() => ({
  listIssues: vi.fn(),
  getIssue: vi.fn(),
  patchIssue: vi.fn(),
  listReportBatches: vi.fn(),
}))

vi.mock("../api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../api")>()
  return {
    ...actual,
    listIssues: mocks.listIssues,
    getIssue: mocks.getIssue,
    patchIssue: mocks.patchIssue,
    listReportBatches: mocks.listReportBatches,
  }
})

import { IssuesTab } from "./IssuesTab"

const VERSION_ID = "1844674407370955161"
const ISSUE_ID = "1844674407370955162"
const BATCH_ID = "1844674407370955163"
const CASE_RUN_ID = "1844674407370955164"
const CASE_NODE_ID = "1844674407370955165"
const EVIDENCE_ID = "1844674407370955166"
const REQUIREMENT_ID = "1844674407370955167"

const STREAM = { events: [], status: "live" as const }

function issueSummary(overrides: Partial<IssueSummary> = {}): IssueSummary {
  return {
    id: ISSUE_ID,
    versionId: VERSION_ID,
    batchId: BATCH_ID,
    caseRunId: CASE_RUN_ID,
    caseNodeId: CASE_NODE_ID,
    stepResultId: null,
    source: "ai",
    category: "defect",
    severity: "major",
    status: "open",
    confidence: "high",
    failureClass: "application",
    title: "登录按钮无响应",
    rerunSuggestion: "检查登录接口超时",
    recurrenceCount: 2,
    revision: 3,
    refs: [
      { kind: "evidence", id: EVIDENCE_ID },
      { kind: "case_run", id: CASE_RUN_ID },
      { kind: "case_node", id: CASE_NODE_ID },
      { kind: "requirement", id: REQUIREMENT_ID },
      { kind: "batch", id: BATCH_ID },
    ],
    codeRefs: [{ anchorId: "9", path: "src/login.ts", line: 42, kind: "file_line", label: "登录" }],
    dismissedReason: "",
    createdBy: "1",
    confirmedBy: null,
    confirmedAt: null,
    dismissedBy: null,
    dismissedAt: null,
    createdAt: "2026-10-28T00:00:00Z",
    updatedAt: "2026-10-28T00:00:00Z",
    ...overrides,
  }
}

function issueDetail(overrides: Partial<IssueDetail> = {}): IssueDetail {
  return {
    ...issueSummary(),
    description: "点击登录按钮后页面无变化",
    reproSteps: "1. 打开登录页\n2. 点击登录",
    expected: "进入首页",
    actual: "停留在登录页",
    case: { caseRunId: CASE_RUN_ID, caseNodeId: CASE_NODE_ID },
    requirement: { id: REQUIREMENT_ID },
    batch: { id: BATCH_ID },
    ...overrides,
  }
}

function emptyPage() {
  return { items: [], nextCursor: "", hasMore: false }
}

const BATCHES = {
  items: [
    { id: BATCH_ID, batchNo: 2, status: "done", kind: "incremental" },
    { id: "1844674407370955170", batchNo: 1, status: "done", kind: "baseline" },
  ],
  nextCursor: "",
  hasMore: false,
}

describe("IssuesTab", () => {
  beforeEach(() => {
    mocks.listIssues.mockReset()
    mocks.getIssue.mockReset()
    mocks.patchIssue.mockReset()
    mocks.listReportBatches.mockReset()
    mocks.listReportBatches.mockResolvedValue(BATCHES)
    mocks.listIssues.mockResolvedValue({
      items: [issueSummary()],
      nextCursor: "c2",
      hasMore: true,
    })
  })

  afterEach(() => {
    cleanup()
  })

  it("lists issues with severity, status, evidence and code content", async () => {
    render(<IssuesTab versionId={VERSION_ID} stream={STREAM} />)

    const row = await screen.findByTestId(`issue-row-${ISSUE_ID}`)
    expect(within(row).getByText("登录按钮无响应")).toBeTruthy()
    expect(within(row).getByText("严重")).toBeTruthy()
    expect(within(row).getByText("待处理")).toBeTruthy()
    expect(within(row).getByText("src/login.ts:42")).toBeTruthy()
    expect(within(row).getByText("证据 1")).toBeTruthy()
    expect(within(row).getByText("Batch #2")).toBeTruthy()
    expect(row.getAttribute("data-issue-id")).toBe(ISSUE_ID)
  })

  it("applies status/severity/category filters and the title search", async () => {
    render(<IssuesTab versionId={VERSION_ID} stream={STREAM} />)
    await screen.findByTestId(`issue-row-${ISSUE_ID}`)

    fireEvent.change(screen.getByTestId("issue-status-filter"), {
      target: { value: "confirmed" },
    })
    await waitFor(() =>
      expect(mocks.listIssues).toHaveBeenLastCalledWith(
        VERSION_ID,
        expect.objectContaining({ status: "confirmed" }),
        expect.anything(),
      ),
    )

    fireEvent.change(screen.getByTestId("issue-severity-filter"), {
      target: { value: "blocker" },
    })
    await waitFor(() =>
      expect(mocks.listIssues).toHaveBeenLastCalledWith(
        VERSION_ID,
        expect.objectContaining({ status: "confirmed", severity: "blocker" }),
        expect.anything(),
      ),
    )

    fireEvent.change(screen.getByTestId("issue-category-filter"), {
      target: { value: "requirement_gap" },
    })
    await waitFor(() =>
      expect(mocks.listIssues).toHaveBeenLastCalledWith(
        VERSION_ID,
        expect.objectContaining({ category: "requirement_gap" }),
        expect.anything(),
      ),
    )

    fireEvent.change(screen.getByTestId("issue-search"), { target: { value: "登录" } })
    fireEvent.click(screen.getByRole("button", { name: "搜索" }))
    await waitFor(() =>
      expect(mocks.listIssues).toHaveBeenLastCalledWith(
        VERSION_ID,
        expect.objectContaining({ q: "登录" }),
        expect.anything(),
      ),
    )

    fireEvent.change(screen.getByTestId("issue-batch-filter"), {
      target: { value: BATCH_ID },
    })
    await waitFor(() =>
      expect(mocks.listIssues).toHaveBeenLastCalledWith(
        VERSION_ID,
        expect.objectContaining({ batchId: BATCH_ID }),
        expect.anything(),
      ),
    )
  })

  it("clears a typed but unsubmitted search when filters are reset", async () => {
    render(<IssuesTab versionId={VERSION_ID} stream={STREAM} />)
    await screen.findByTestId(`issue-row-${ISSUE_ID}`)

    const search = screen.getByTestId("issue-search") as HTMLInputElement
    fireEvent.change(search, { target: { value: "未提交关键字" } })
    expect(search.value).toBe("未提交关键字")

    fireEvent.click(screen.getByTestId("issue-filters-reset"))
    await waitFor(() => expect(search.value).toBe(""))
  })

  it("loads the next cursor page and keeps previous rows", async () => {
    mocks.listIssues
      .mockResolvedValueOnce({
        items: [issueSummary()],
        nextCursor: "c2",
        hasMore: true,
      })
      .mockResolvedValueOnce({
        items: [issueSummary({ id: "1844674407370955180", title: "第二个问题" })],
        nextCursor: "",
        hasMore: false,
      })

    render(<IssuesTab versionId={VERSION_ID} stream={STREAM} />)
    await screen.findByTestId(`issue-row-${ISSUE_ID}`)
    fireEvent.click(screen.getByTestId("issues-load-more"))

    await screen.findByTestId("issue-row-1844674407370955180")
    expect(screen.getByTestId(`issue-row-${ISSUE_ID}`)).toBeTruthy()
    expect(mocks.listIssues).toHaveBeenLastCalledWith(
      VERSION_ID,
      expect.objectContaining({ cursor: "c2" }),
      expect.anything(),
    )
  })

  it("opens the detail and confirms the issue under the revision CAS", async () => {
    mocks.getIssue
      .mockResolvedValueOnce(issueDetail())
      .mockResolvedValueOnce(
        issueDetail({ status: "confirmed", confirmedBy: "1", confirmedAt: "2026-10-28T01:00:00Z" }),
      )
    mocks.patchIssue.mockResolvedValue(issueDetail({ status: "confirmed", confirmedBy: "1" }))

    render(<IssuesTab versionId={VERSION_ID} stream={STREAM} />)
    fireEvent.click(await screen.findByTestId(`issue-row-${ISSUE_ID}`))

    const detail = await screen.findByTestId("issue-detail")
    expect(within(detail).getByText("点击登录按钮后页面无变化")).toBeTruthy()
    expect(within(detail).getByText(/打开登录页/)).toBeTruthy()
    expect(within(detail).getByText("进入首页")).toBeTruthy()
    expect(within(detail).getByText("停留在登录页")).toBeTruthy()
    expect(within(detail).getByTestId("issue-evidence-link")).toBeTruthy()
    expect(within(detail).getByTestId("issue-code-ref")).toBeTruthy()

    fireEvent.click(screen.getByTestId("issue-confirm-btn"))
    await waitFor(() =>
      expect(mocks.patchIssue).toHaveBeenCalledWith(VERSION_ID, ISSUE_ID, {
        status: "confirmed",
        expectedRevision: 3,
      }),
    )
    await waitFor(() => expect(screen.getByTestId("issue-detail").textContent).toContain("已确认"))
    expect(mocks.getIssue).toHaveBeenCalledTimes(2)
  })

  it("requires a dismissal reason, then refreshes the issue", async () => {
    mocks.getIssue.mockResolvedValueOnce(issueDetail()).mockResolvedValueOnce(
      issueDetail({
        status: "dismissed",
        dismissedReason: "环境问题",
        dismissedBy: "2",
        dismissedAt: "2026-10-28T02:00:00Z",
      }),
    )
    mocks.patchIssue.mockResolvedValue(issueDetail({ status: "dismissed" }))

    render(<IssuesTab versionId={VERSION_ID} stream={STREAM} />)
    fireEvent.click(await screen.findByTestId(`issue-row-${ISSUE_ID}`))
    fireEvent.click(await screen.findByTestId("issue-dismiss-btn"))

    const dialog = screen.getByTestId("issue-dismiss-dialog")
    const submit = within(dialog).getByRole("button", { name: "确认驳回" })
    expect(submit.hasAttribute("disabled")).toBe(true)

    fireEvent.change(within(dialog).getByTestId("issue-dismiss-reason"), {
      target: { value: "环境问题" },
    })
    expect(submit.hasAttribute("disabled")).toBe(false)
    fireEvent.click(submit)

    await waitFor(() =>
      expect(mocks.patchIssue).toHaveBeenCalledWith(VERSION_ID, ISSUE_ID, {
        status: "dismissed",
        expectedRevision: 3,
        comment: "环境问题",
      }),
    )
    await waitFor(() => expect(screen.queryByTestId("issue-dismiss-dialog")).toBeNull())
    await waitFor(() => expect(screen.getByTestId("issue-detail").textContent).toContain("已驳回"))
  })

  it("explains a revision CAS conflict and refreshes the detail", async () => {
    mocks.getIssue.mockResolvedValue(issueDetail())
    mocks.patchIssue.mockRejectedValueOnce(
      new ApiError({
        status: 409,
        code: "conflict",
        message: "revision mismatch",
        details: { reason: "issue_state_conflict" },
      }),
    )

    render(<IssuesTab versionId={VERSION_ID} stream={STREAM} />)
    fireEvent.click(await screen.findByTestId(`issue-row-${ISSUE_ID}`))
    fireEvent.click(await screen.findByTestId("issue-confirm-btn"))

    const alert = await screen.findByTestId("issue-action-error")
    expect(alert.textContent).toContain("已被更新")
    await waitFor(() => expect(mocks.getIssue).toHaveBeenCalledTimes(2))
  })

  it("shows the empty and AI-degraded states", async () => {
    mocks.listIssues.mockResolvedValue(emptyPage())

    render(<IssuesTab versionId={VERSION_ID} stream={STREAM} degraded />)
    expect(await screen.findByText("当前批次暂无问题")).toBeTruthy()
    expect(screen.getByText("AI 分析不可用，问题可能不完整")).toBeTruthy()
  })

  it("shows an error with a retry action", async () => {
    mocks.listIssues
      .mockRejectedValueOnce(new ApiError({ status: 403, code: "forbidden", message: "forbidden" }))
      .mockResolvedValueOnce(emptyPage())

    render(<IssuesTab versionId={VERSION_ID} stream={STREAM} />)
    const alert = await screen.findByTestId("issues-error")
    expect(alert.textContent).toContain("无权限")
    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(await screen.findByText("当前批次暂无问题")).toBeTruthy()
  })

  it("opens a deep-linked issue in controlled mode", async () => {
    mocks.getIssue.mockResolvedValue(issueDetail())
    const onIssueChange = vi.fn()

    render(
      <IssuesTab
        versionId={VERSION_ID}
        stream={STREAM}
        issueId={ISSUE_ID}
        onIssueChange={onIssueChange}
      />,
    )

    expect(await screen.findByTestId("issue-detail")).toBeTruthy()
    expect(mocks.getIssue).toHaveBeenCalledWith(VERSION_ID, ISSUE_ID, expect.anything())
    fireEvent.click(screen.getByRole("button", { name: "关闭问题详情" }))
    expect(onIssueChange).toHaveBeenCalledWith("")
  })

  it("opens evidence through the caller and links cases/code from the detail", async () => {
    mocks.getIssue.mockResolvedValue(issueDetail())
    const onOpenEvidence = vi.fn()
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    })

    render(<IssuesTab versionId={VERSION_ID} stream={STREAM} onOpenEvidence={onOpenEvidence} />)
    fireEvent.click(await screen.findByTestId(`issue-row-${ISSUE_ID}`))
    const detail = await screen.findByTestId("issue-detail")

    fireEvent.click(within(detail).getByTestId("issue-evidence-link"))
    expect(onOpenEvidence).toHaveBeenCalledWith(EVIDENCE_ID)

    const codeRef = within(detail).getByTestId("issue-code-ref")
    expect(codeRef.getAttribute("href")).toContain("tab=code")
    expect(codeRef.getAttribute("href")).toContain("src%2Flogin.ts")
    expect(codeRef.getAttribute("href")).toContain("line=42")

    const caseLink = within(detail).getByTestId("issue-case-link")
    expect(caseLink.getAttribute("href")).toContain(`caseRun=${CASE_RUN_ID}`)

    fireEvent.click(within(detail).getByRole("button", { name: "复制 Markdown" }))
    await waitFor(() => expect(writeText).toHaveBeenCalledTimes(1))
    expect(writeText.mock.calls[0]?.[0]).toContain("登录按钮无响应")
  })
})
