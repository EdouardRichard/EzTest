import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

import type { IssueDetailView, IssueView } from "./types"

const api = vi.hoisted(() => ({
  listIssues: vi.fn(),
  getIssue: vi.fn(),
  confirmIssue: vi.fn(),
  dismissIssue: vi.fn(),
  reopenIssue: vi.fn(),
  mergeIssue: vi.fn(),
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
const search = vi.hoisted(() => ({ params: new URLSearchParams() }))
vi.mock("next/navigation", () => ({
  useRouter: () => navigation,
  useSearchParams: () => search.params,
}))

const sse = vi.hoisted(() => ({
  handlers: null as Record<string, (event: unknown) => void> | null,
  calls: 0,
}))
vi.mock("@/features/clarification/useVersionEvents", async () => {
  const React = await import("react")
  return {
    useVersionEvents: (
      _versionId: string,
      handlers: Record<string, (event: unknown) => void>,
    ): string => {
      sse.handlers = handlers
      React.useEffect(() => {
        sse.calls += 1
      }, [])
      return "live"
    },
  }
})

import { IssuesTab } from "./IssuesTab"

/* --------------------------------- fixtures -------------------------------- */

const created = "2026-11-01T02:00:00Z"
const updated = "2026-11-01T03:00:00Z"

const openIssue: IssueView = {
  id: "9101",
  version_id: "2001",
  title: "结算页 · step 3: ui failure",
  summary: "点击提交后未跳转到支付页",
  status: "open",
  category: "ui",
  severity: "major",
  source: "failure_candidate",
  expected_revision: 1,
  merged_into_id: null,
  merged_at: null,
  reopen_count: 0,
  refs: [
    { kind: "case_run", ref_id: "9302", version_sut_id: "3001", role: "source" },
    { kind: "wave", ref_id: "9201", version_sut_id: "3001", role: "context" },
  ],
  triage: null,
  created_at: created,
  updated_at: updated,
}

const confirmedIssue: IssueView = {
  id: "9102",
  version_id: "2001",
  title: "退款接口 · step 2: api failure",
  summary: "退款金额与订单金额不一致",
  status: "confirmed",
  category: "api",
  severity: "minor",
  source: "failure_candidate",
  expected_revision: 2,
  merged_into_id: null,
  merged_at: null,
  reopen_count: 1,
  refs: [
    { kind: "case_run", ref_id: "9303", version_sut_id: "3002", role: "source" },
    { kind: "wave", ref_id: "9201", version_sut_id: "3002", role: "context" },
  ],
  triage: null,
  created_at: created,
  updated_at: updated,
}

const mergedIssue: IssueView = {
  id: "9103",
  version_id: "2001",
  title: "支付回调 · step 1: db failure",
  summary: "回调写入重复",
  status: "merged",
  category: "db",
  severity: "trivial",
  source: "failure_candidate",
  expected_revision: 3,
  merged_into_id: "9102",
  merged_at: updated,
  reopen_count: 0,
  refs: [{ kind: "case_run", ref_id: "9304", version_sut_id: "3001", role: "source" }],
  triage: null,
  created_at: created,
  updated_at: updated,
}

function detailOf(issue: IssueView): IssueDetailView {
  return {
    ...issue,
    history: [
      {
        from_status: null,
        to_status: "open",
        reason: "失败执行自动生成",
        actor_id: null,
        actor_kind: "system",
        created_at: created,
      },
    ],
    related_refs: { case_run: 1, wave: 1 },
  }
}

const openDetailFixture: IssueDetailView = {
  ...detailOf(openIssue),
  refs: [
    { kind: "case_run", ref_id: "9302", version_sut_id: "3001", role: "source" },
    { kind: "wave", ref_id: "9201", version_sut_id: "3001", role: "context" },
    { kind: "evidence", ref_id: "9501", version_sut_id: "3001", role: "attachment" },
  ],
  related_refs: { case_run: 1, wave: 1, evidence: 1 },
  history: [
    {
      from_status: null,
      to_status: "open",
      reason: "失败执行自动生成",
      actor_id: null,
      actor_kind: "system",
      created_at: created,
    },
    {
      from_status: "open",
      to_status: "confirmed",
      reason: "本地可稳定复现",
      actor_id: "1001",
      actor_kind: "user",
      created_at: updated,
    },
  ],
  triage: {
    analysis_run_id: "9901",
    status: "done",
    classification: "product_bug",
    duplicate_of_issue_id: "9102",
    summary: "与 9102 可能重复",
  },
}

function mockWorkspace(): void {
  api.listIssues.mockResolvedValue([openIssue, confirmedIssue])
  api.getIssue.mockImplementation((_versionId: string, issueId: string) => {
    if (issueId === openIssue.id) return Promise.resolve(openDetailFixture)
    const found = [confirmedIssue, mergedIssue].find((issue) => issue.id === issueId)
    if (found === undefined) {
      return Promise.reject(new ApiError({ status: 404, code: "not_found", message: "问题不存在" }))
    }
    return Promise.resolve(detailOf(found))
  })
  api.confirmIssue.mockResolvedValue({ ...openIssue, status: "confirmed" })
  api.dismissIssue.mockResolvedValue({ ...openIssue, status: "dismissed" })
  api.reopenIssue.mockResolvedValue({ ...confirmedIssue, status: "open", reopen_count: 2 })
  api.mergeIssue.mockResolvedValue({ ...openIssue, status: "merged", merged_into_id: "9102" })
}

function rowOf(issueId: string): HTMLElement {
  const row = screen.getByTestId(`issue-${issueId}`).closest("tr")
  if (row === null) throw new Error(`no table row for issue ${issueId}`)
  return row
}

async function openDetail(issueId: string): Promise<HTMLElement> {
  fireEvent.click(await screen.findByTestId(`issue-${issueId}`))
  return screen.findByRole("dialog", { name: "问题详情" })
}

describe("IssuesTab", () => {
  beforeEach(() => {
    for (const fn of Object.values(api)) fn.mockReset()
    navigation.replace.mockReset()
    search.params = new URLSearchParams()
    sse.handlers = null
    sse.calls = 0
    mockWorkspace()
  })

  afterEach(cleanup)

  it("shows a loading state while the issue list loads", () => {
    api.listIssues.mockReturnValue(new Promise<never>(() => {}))

    render(<IssuesTab versionId="2001" />)

    expect(screen.getByText("正在加载问题…")).toBeInTheDocument()
  })

  it("loads the issues into the table with status, category, severity, SUT, refs and reopen counts", async () => {
    render(<IssuesTab versionId="2001" />)

    expect(await screen.findByTestId("issue-9101")).toBeInTheDocument()
    expect(api.listIssues).toHaveBeenCalledWith("2001", { limit: 20 })

    const row = rowOf("9101")
    expect(row).toHaveTextContent("结算页 · step 3: ui failure")
    expect(row).toHaveTextContent("待确认")
    expect(row).toHaveTextContent("界面")
    expect(row).toHaveTextContent("严重")
    expect(row).toHaveTextContent("SUT 3001")
    expect(row).toHaveTextContent("2 个引用")
    expect(row).toHaveTextContent("0")
  })

  it("orders open issues before confirmed, dismissed and merged issues", async () => {
    // The API page is id DESC, so the confirmed issue arrives first.
    api.listIssues.mockResolvedValue([confirmedIssue, openIssue])

    render(<IssuesTab versionId="2001" />)

    await screen.findByTestId("issue-9101")
    const ids = screen.getAllByTestId(/^issue-\d+$/).map((cell) => cell.dataset.testid)
    expect(ids).toEqual(["issue-9101", "issue-9102"])
  })

  it("passes status, category, severity and SUT filters to the API", async () => {
    render(<IssuesTab versionId="2001" />)
    await screen.findByTestId("issue-9101")

    fireEvent.change(screen.getByLabelText("状态"), { target: { value: "confirmed" } })
    await waitFor(() => {
      expect(api.listIssues).toHaveBeenLastCalledWith("2001", {
        status: "confirmed",
        limit: 20,
      })
    })

    fireEvent.change(screen.getByLabelText("类别"), { target: { value: "api" } })
    await waitFor(() => {
      expect(api.listIssues).toHaveBeenLastCalledWith("2001", {
        status: "confirmed",
        category: "api",
        limit: 20,
      })
    })

    fireEvent.change(screen.getByLabelText("严重度"), { target: { value: "minor" } })
    await waitFor(() => {
      expect(api.listIssues).toHaveBeenLastCalledWith("2001", {
        status: "confirmed",
        category: "api",
        severity: "minor",
        limit: 20,
      })
    })

    fireEvent.change(screen.getByLabelText("SUT"), { target: { value: "3002" } })
    await waitFor(() => {
      expect(api.listIssues).toHaveBeenLastCalledWith("2001", {
        status: "confirmed",
        category: "api",
        severity: "minor",
        versionSutId: "3002",
        limit: 20,
      })
    })
  })

  it("refetches the list and open detail once for a burst of issue events", async () => {
    render(<IssuesTab versionId="2001" />)
    const sheet = await openDetail("9101")
    expect(within(sheet).getByText("点击提交后未跳转到支付页")).toBeInTheDocument()

    // One subscription per rendered tab: the hook is mounted exactly once.
    expect(sse.calls).toBe(1)
    expect(sse.handlers).not.toBeNull()

    act(() => {
      sse.handlers?.onIssueChanged?.({ type: "issue.created" })
      sse.handlers?.onIssueChanged?.({ type: "issue.updated" })
    })

    await waitFor(() => expect(api.listIssues).toHaveBeenCalledTimes(2), { timeout: 2000 })
    await waitFor(() => expect(api.getIssue).toHaveBeenCalledTimes(2), { timeout: 2000 })
    expect(sse.calls).toBe(1)
  })

  it("refetches the list and open detail on stream.reset without a new subscription", async () => {
    render(<IssuesTab versionId="2001" />)
    await openDetail("9101")
    expect(sse.calls).toBe(1)

    act(() => {
      sse.handlers?.onStreamReset?.({ reason: "cursor_too_old", snapshot_cursor: 99 })
    })

    await waitFor(() => expect(api.listIssues).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(api.getIssue).toHaveBeenCalledTimes(2))
    expect(sse.calls).toBe(1)
  })

  it("opens the detail sheet from a row and writes ?issue= into the URL", async () => {
    render(<IssuesTab versionId="2001" />)

    const sheet = await openDetail("9101")

    expect(api.getIssue).toHaveBeenCalledWith("2001", "9101")
    expect(within(sheet).getByText("点击提交后未跳转到支付页")).toBeInTheDocument()
    expect(navigation.replace).toHaveBeenCalledWith("/versions/2001?tab=issues&issue=9101")
  })

  it("closes the detail sheet and removes ?issue= from the URL", async () => {
    render(<IssuesTab versionId="2001" />)
    const sheet = await openDetail("9101")

    fireEvent.click(within(sheet).getByRole("button", { name: "关闭详情" }))

    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: "问题详情" })).not.toBeInTheDocument()
    })
    expect(navigation.replace).toHaveBeenLastCalledWith("/versions/2001?tab=issues")
  })

  it("opens the deep-linked ?issue= detail without rewriting the URL", async () => {
    search.params = new URLSearchParams("tab=issues&issue=9102")

    render(<IssuesTab versionId="2001" />)

    const sheet = await screen.findByRole("dialog", { name: "问题详情" })
    expect(api.getIssue).toHaveBeenCalledWith("2001", "9102")
    expect(within(sheet).getByText("退款接口 · step 2: api failure")).toBeInTheDocument()
    expect(navigation.replace).not.toHaveBeenCalled()
  })

  it("renders refs deep links, status history and the triage summary", async () => {
    render(<IssuesTab versionId="2001" />)
    const sheet = await openDetail("9101")

    const refs = within(sheet).getByTestId("issue-refs")
    expect(within(refs).getByRole("link", { name: "用例运行 #9302" })).toHaveAttribute(
      "href",
      "/versions/2001?tab=execution&wave=9201&case_run=9302",
    )
    expect(within(refs).getByRole("link", { name: "波次 #9201" })).toHaveAttribute(
      "href",
      "/versions/2001?tab=execution&wave=9201",
    )
    expect(within(refs).getByRole("link", { name: "证据 #9501" })).toHaveAttribute(
      "href",
      "/versions/2001?tab=execution&wave=9201&case_run=9302&evidence=9501",
    )

    const historySection = within(sheet).getByTestId("issue-history")
    expect(within(historySection).getByText("失败执行自动生成")).toBeInTheDocument()
    expect(within(historySection).getByText("本地可稳定复现")).toBeInTheDocument()
    expect(historySection).toHaveTextContent("用户")

    const triage = within(sheet).getByTestId("issue-triage")
    expect(within(triage).getByText("product_bug")).toBeInTheDocument()
    expect(within(triage).getByText("与 9102 可能重复")).toBeInTheDocument()
    expect(within(triage).getByRole("link", { name: "问题 #9102" })).toHaveAttribute(
      "href",
      "/versions/2001?tab=issues&issue=9102",
    )
  })

  it("confirms an issue with a reason and refreshes the list and detail", async () => {
    render(<IssuesTab versionId="2001" />)
    const sheet = await openDetail("9101")

    fireEvent.click(within(sheet).getByRole("button", { name: "确认" }))
    const dialog = await screen.findByRole("dialog", { name: "确认问题" })
    fireEvent.change(within(dialog).getByLabelText("确认原因"), {
      target: { value: "确认可复现" },
    })
    fireEvent.click(within(dialog).getByRole("button", { name: "确认问题" }))

    await waitFor(() => {
      expect(api.confirmIssue).toHaveBeenCalledWith("2001", "9101", {
        reason: "确认可复现",
        expected_revision: 1,
      })
    })
    await waitFor(() => expect(api.listIssues).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(api.getIssue).toHaveBeenCalledTimes(2))
    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: "确认问题" })).not.toBeInTheDocument()
    })
  })

  it("dismisses an issue with a reason and refreshes the list", async () => {
    render(<IssuesTab versionId="2001" />)
    const sheet = await openDetail("9101")

    fireEvent.click(within(sheet).getByRole("button", { name: "驳回" }))
    const dialog = await screen.findByRole("dialog", { name: "驳回问题" })
    fireEvent.change(within(dialog).getByLabelText("驳回原因"), {
      target: { value: "环境数据构造错误" },
    })
    fireEvent.click(within(dialog).getByRole("button", { name: "驳回问题" }))

    await waitFor(() => {
      expect(api.dismissIssue).toHaveBeenCalledWith("2001", "9101", {
        reason: "环境数据构造错误",
        expected_revision: 1,
      })
    })
    await waitFor(() => expect(api.listIssues).toHaveBeenCalledTimes(2))
  })

  it("requires a reason before reopening a confirmed issue", async () => {
    render(<IssuesTab versionId="2001" />)
    const sheet = await openDetail("9102")

    fireEvent.click(within(sheet).getByRole("button", { name: "重开" }))
    const dialog = await screen.findByRole("dialog", { name: "重开问题" })
    const submit = within(dialog).getByRole("button", { name: "重开问题" })
    expect(submit).toBeDisabled()

    fireEvent.change(within(dialog).getByLabelText("重开原因"), {
      target: { value: "复测仍然失败" },
    })
    expect(submit).not.toBeDisabled()
    fireEvent.click(submit)

    await waitFor(() => {
      expect(api.reopenIssue).toHaveBeenCalledWith("2001", "9102", {
        reason: "复测仍然失败",
        expected_revision: 2,
      })
    })
    await waitFor(() => expect(api.listIssues).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(api.getIssue).toHaveBeenCalledTimes(2))
  })

  it("links a merged issue to its main issue", async () => {
    api.listIssues.mockResolvedValue([openIssue, mergedIssue])
    render(<IssuesTab versionId="2001" />)
    const sheet = await openDetail("9103")

    expect(within(sheet).getByText(/已合并至问题/)).toBeInTheDocument()
    expect(within(sheet).getByRole("link", { name: "#9102" })).toHaveAttribute(
      "href",
      "/versions/2001?tab=issues&issue=9102",
    )
    expect(within(sheet).queryByRole("button", { name: "重开" })).not.toBeInTheDocument()
    expect(within(sheet).queryByRole("button", { name: "合并" })).not.toBeInTheDocument()
  })

  it("merges an issue into a filtered target and refreshes the list", async () => {
    api.listIssues.mockResolvedValue([openIssue, confirmedIssue, mergedIssue])
    render(<IssuesTab versionId="2001" />)
    const sheet = await openDetail("9101")

    fireEvent.click(within(sheet).getByRole("button", { name: "合并" }))
    const dialog = await screen.findByRole("dialog", { name: "合并问题" })
    await waitFor(() => {
      expect(api.listIssues).toHaveBeenCalledWith("2001", { limit: 100 })
    })

    // Merged issues and the source issue are not merge candidates.
    expect(within(dialog).queryByText(mergedIssue.title)).not.toBeInTheDocument()
    expect(within(dialog).queryByText(openIssue.title)).not.toBeInTheDocument()

    fireEvent.change(within(dialog).getByLabelText("搜索目标问题"), {
      target: { value: "退款" },
    })
    fireEvent.click(within(dialog).getByTestId("merge-target-9102"))
    fireEvent.click(within(dialog).getByRole("button", { name: "合并" }))

    await waitFor(() => {
      expect(api.mergeIssue).toHaveBeenCalledWith("2001", "9101", {
        target_issue_id: "9102",
        expected_revision: 1,
      })
    })
    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: "合并问题" })).not.toBeInTheDocument()
    })
    await waitFor(() => {
      expect(api.listIssues).toHaveBeenLastCalledWith("2001", { limit: 20 })
    })
    await waitFor(() => expect(api.getIssue).toHaveBeenCalledTimes(2))
  })

  it("shows the merge 409 error inline and keeps the dialog open", async () => {
    render(<IssuesTab versionId="2001" />)
    const sheet = await openDetail("9101")
    api.mergeIssue.mockRejectedValueOnce(
      new ApiError({
        status: 409,
        code: "state_conflict",
        message: "merge cycle",
        details: { reason: "merge_cycle" },
      }),
    )

    fireEvent.click(within(sheet).getByRole("button", { name: "合并" }))
    const dialog = await screen.findByRole("dialog", { name: "合并问题" })
    await waitFor(() => expect(within(dialog).getByTestId("merge-target-9102")).toBeInTheDocument())
    fireEvent.click(within(dialog).getByTestId("merge-target-9102"))
    fireEvent.click(within(dialog).getByRole("button", { name: "合并" }))

    expect(
      await within(dialog).findByText("合并会形成环，已阻止。请选择其他目标问题。"),
    ).toBeInTheDocument()
    expect(screen.getByRole("dialog", { name: "合并问题" })).toBeInTheDocument()
    expect(api.listIssues).toHaveBeenCalledTimes(2)
  })

  it("shows the empty state when the version has no issues", async () => {
    api.listIssues.mockResolvedValue([])

    render(<IssuesTab versionId="2001" />)

    expect(await screen.findByText("暂无问题")).toBeInTheDocument()
    expect(screen.getByText("执行失败后会自动生成问题候选。")).toBeInTheDocument()
  })

  it("shows a retryable error state when the issue list fails", async () => {
    api.listIssues
      .mockRejectedValueOnce(
        new ApiError({
          status: 500,
          code: "internal",
          message: "问题列表暂不可用",
          requestId: "req_issues",
        }),
      )
      .mockResolvedValue([openIssue])

    render(<IssuesTab versionId="2001" />)

    expect(await screen.findByText("问题列表加载失败")).toBeInTheDocument()
    expect(screen.getByText("问题列表暂不可用")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))

    expect(await screen.findByTestId("issue-9101")).toBeInTheDocument()
    expect(api.listIssues).toHaveBeenCalledTimes(2)
  })
})
