import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

import type { GlobalIssueView } from "./global-api"
import type { IssueDetailView } from "./types"

const globalApi = vi.hoisted(() => ({
  listGlobalIssues: vi.fn(),
}))
vi.mock("./global-api", () => globalApi)

const api = vi.hoisted(() => ({
  getIssue: vi.fn(),
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

import { GlobalIssuesPage } from "./GlobalIssuesPage"

/* --------------------------------- fixtures -------------------------------- */

const created = "2026-11-01T02:00:00Z"
const updated = "2026-11-01T06:00:00Z"

const openIssue: GlobalIssueView = {
  id: "9101",
  version_id: "2001",
  version_title: "结算改版",
  url: "/versions/2001?tab=issues&issue=9101",
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
  refs: [],
  triage: null,
  created_at: created,
  updated_at: updated,
}

const mergedIssue: GlobalIssueView = {
  id: "9103",
  version_id: "2002",
  version_title: "登录改版",
  url: "/versions/2002?tab=issues&issue=9103",
  title: "支付回调 · step 1: db failure",
  summary: "回调写入重复",
  status: "merged",
  category: "db",
  severity: "trivial",
  source: "failure_candidate",
  expected_revision: 3,
  merged_into_id: "9102",
  merged_at: updated,
  reopen_count: 2,
  refs: [],
  triage: null,
  created_at: created,
  updated_at: updated,
}

const openDetail: IssueDetailView = {
  ...openIssue,
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
  related_refs: { case_run: 1 },
}

function page(items: GlobalIssueView[], nextCursor = "", hasMore = false) {
  return { items, nextCursor, hasMore }
}

function apiError(status: number, message: string): ApiError {
  return new ApiError({ status, code: "unavailable", message })
}

/* ---------------------------------- setup ---------------------------------- */

beforeEach(() => {
  vi.clearAllMocks()
  search.params = new URLSearchParams()
  globalApi.listGlobalIssues.mockResolvedValue(page([]))
  api.getIssue.mockResolvedValue(openDetail)
})

afterEach(() => {
  cleanup()
})

/* ---------------------------------- tests ---------------------------------- */

describe("GlobalIssuesPage", () => {
  it("renders cross-version rows with version titles and workspace deep links", async () => {
    globalApi.listGlobalIssues.mockResolvedValue(page([openIssue, mergedIssue]))
    render(<GlobalIssuesPage />)

    expect(screen.getByTestId("global-issues-page")).toBeInTheDocument()
    expect(await screen.findByText(openIssue.title)).toBeInTheDocument()
    expect(screen.getByText("结算改版")).toBeInTheDocument()
    expect(screen.getByText("登录改版")).toBeInTheDocument()

    const link = screen.getByTestId("global-issue-link-9101")
    expect(link).toHaveAttribute("href", "/versions/2001?tab=issues&issue=9101")
    expect(screen.getByTestId("global-issue-link-9103")).toHaveAttribute(
      "href",
      "/versions/2002?tab=issues&issue=9103",
    )

    const table = screen.getByRole("table")
    expect(within(table).getByText("已合并")).toBeInTheDocument()
    expect(screen.getByTestId("global-issue-merged-9103")).toHaveTextContent("已合并至 #9102")
    expect(screen.getByTestId("global-issue-reopen-9103")).toHaveTextContent("重开 2 次")
  })

  it("forwards the status filter and mirrors it in the URL", async () => {
    globalApi.listGlobalIssues.mockResolvedValue(page([openIssue]))
    render(<GlobalIssuesPage />)
    await screen.findByText(openIssue.title)

    fireEvent.change(screen.getByLabelText("状态"), { target: { value: "merged" } })

    await waitFor(() => {
      expect(globalApi.listGlobalIssues).toHaveBeenLastCalledWith(
        expect.objectContaining({ status: "merged" }),
      )
    })
    expect(navigation.replace).toHaveBeenLastCalledWith("/issues?status=merged")
  })

  it("applies the title query only on submit", async () => {
    globalApi.listGlobalIssues.mockResolvedValue(page([openIssue]))
    render(<GlobalIssuesPage />)
    await screen.findByText(openIssue.title)

    const input = screen.getByLabelText("搜索标题")
    fireEvent.change(input, { target: { value: "  结算  " } })
    expect(globalApi.listGlobalIssues).toHaveBeenCalledTimes(1)

    fireEvent.click(screen.getByTestId("global-issues-search"))

    await waitFor(() => {
      expect(globalApi.listGlobalIssues).toHaveBeenLastCalledWith(
        expect.objectContaining({ q: "结算" }),
      )
    })
    expect(navigation.replace).toHaveBeenLastCalledWith("/issues?q=%E7%BB%93%E7%AE%97")
  })

  it("follows the version_id deep link and shows a clearable chip", async () => {
    search.params = new URLSearchParams("version_id=2001&status=open")
    globalApi.listGlobalIssues.mockResolvedValue(page([openIssue]))
    render(<GlobalIssuesPage />)

    await waitFor(() => {
      expect(globalApi.listGlobalIssues).toHaveBeenCalledWith(
        expect.objectContaining({ versionId: "2001", status: "open" }),
      )
    })
    expect(await screen.findByText("版本 2001")).toBeInTheDocument()

    fireEvent.click(screen.getByLabelText("清除版本筛选"))
    await waitFor(() => {
      expect(globalApi.listGlobalIssues).toHaveBeenLastCalledWith(
        expect.not.objectContaining({ versionId: expect.anything() }),
      )
    })
  })

  it("loads the next page with the exclusive before cursor", async () => {
    globalApi.listGlobalIssues
      .mockResolvedValueOnce(page([openIssue], "9100", true))
      .mockResolvedValueOnce(page([mergedIssue], "", false))
    render(<GlobalIssuesPage />)

    await screen.findByText(openIssue.title)
    fireEvent.click(screen.getByTestId("global-issues-load-more"))

    await waitFor(() => {
      expect(globalApi.listGlobalIssues).toHaveBeenLastCalledWith(
        expect.objectContaining({ before: "9100" }),
      )
    })
    expect(await screen.findByText(mergedIssue.title)).toBeInTheDocument()
    expect(screen.getByText(openIssue.title)).toBeInTheDocument()
    expect(screen.queryByTestId("global-issues-load-more")).not.toBeInTheDocument()
  })

  it("shows the empty state", async () => {
    render(<GlobalIssuesPage />)
    expect(await screen.findByText("暂无问题")).toBeInTheDocument()
  })

  it("shows the error state and retries", async () => {
    globalApi.listGlobalIssues
      .mockRejectedValueOnce(apiError(503, "global issues unavailable"))
      .mockResolvedValueOnce(page([openIssue]))
    render(<GlobalIssuesPage />)

    expect(await screen.findByText("问题列表加载失败")).toBeInTheDocument()
    expect(screen.getByText("global issues unavailable")).toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(await screen.findByText(openIssue.title)).toBeInTheDocument()
  })

  it("opens a read-only detail sheet with no write actions", async () => {
    globalApi.listGlobalIssues.mockResolvedValue(page([openIssue]))
    render(<GlobalIssuesPage />)

    fireEvent.click(await screen.findByTestId("global-issue-9101"))

    expect(await screen.findByTestId("issue-detail-sheet")).toBeInTheDocument()
    expect(api.getIssue).toHaveBeenCalledWith("2001", "9101")
    const sheet = screen.getByTestId("issue-detail-sheet")
    expect(within(sheet).getByText(openIssue.title)).toBeInTheDocument()
    for (const label of ["确认", "驳回", "重开", "合并"]) {
      expect(within(sheet).queryByRole("button", { name: label })).not.toBeInTheDocument()
    }
    expect(within(sheet).queryByRole("button", { name: "合并" })).not.toBeInTheDocument()
  })
})
