import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

import {
  analysisDetailFixture,
  analysisViewFixture,
  anchorFixture,
  anchorsFixture,
  BASE_COMMIT,
  changeSetFixture,
  diffFixture,
  factsFixture,
  fileWindowFixture,
  HEAD_COMMIT,
  indexRunFixture,
  proposalApiFixture,
  proposalPageFixture,
  repoStatusFixture,
  repositoryFixture,
  reviewFixture,
} from "../test-fixtures"

const api = vi.hoisted(() => ({
  listCodeRepos: vi.fn(),
  triggerIndex: vi.fn(),
  latestIndexRun: vi.fn(),
  listTree: vi.fn(),
  readFile: vi.fn(),
  listDiff: vi.fn(),
  readDiffFile: vi.fn(),
  listFacts: vi.fn(),
  searchCode: vi.fn(),
  getCodeChanges: vi.fn(),
  startAnalysis: vi.fn(),
  latestAnalysis: vi.fn(),
  getAnalysis: vi.fn(),
  patchCandidate: vi.fn(),
  getCodeImpact: vi.fn(),
  listAnchors: vi.fn(),
  createAnchor: vi.fn(),
  validateAnchor: vi.fn(),
  deleteAnchor: vi.fn(),
  listReviews: vi.fn(),
  createReview: vi.fn(),
  generateProposals: vi.fn(),
  listProposals: vi.fn(),
}))
vi.mock("../api", () => api)

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

const codeEvents = vi.hoisted(() => ({
  handlers: null as Record<string, (() => void) | undefined> | null,
  calls: 0,
}))
vi.mock("../useCodeEvents", async () => {
  const React = await import("react")
  return {
    useCodeEvents: (
      _versionId: string,
      handlers: Record<string, (() => void) | undefined>,
    ): string => {
      codeEvents.handlers = handlers
      React.useEffect(() => {
        codeEvents.calls += 1
      }, [])
      return "live"
    },
  }
})

import { CodeTab } from "../CodeTab"

function defaultApiMocks(): void {
  api.listCodeRepos.mockResolvedValue([repoStatusFixture()])
  api.latestAnalysis.mockResolvedValue(analysisViewFixture())
  api.getAnalysis.mockResolvedValue(analysisDetailFixture())
  api.getCodeChanges.mockResolvedValue(changeSetFixture)
  api.listFacts.mockResolvedValue({ items: factsFixture, nextCursor: "", hasMore: false })
  api.listAnchors.mockResolvedValue(anchorsFixture)
  api.listProposals.mockResolvedValue([proposalPageFixture, proposalApiFixture])
  api.readDiffFile.mockResolvedValue(diffFixture())
  api.readFile.mockResolvedValue(fileWindowFixture)
  api.listReviews.mockResolvedValue([reviewFixture])
  api.triggerIndex.mockResolvedValue(indexRunFixture({ status: "queued" }))
  api.startAnalysis.mockResolvedValue(analysisViewFixture({ status: "queued" }))
  api.patchCandidate.mockImplementation(
    (_versionId: string, _analysisId: string, _candidateId: string, selected: boolean) =>
      Promise.resolve({ ...analysisDetailFixture().candidates[0], selected }),
  )
  api.validateAnchor.mockResolvedValue({ ...anchorFixture, status: "stale" })
  api.deleteAnchor.mockResolvedValue(undefined)
  api.createReview.mockResolvedValue(reviewFixture)
  api.generateProposals.mockResolvedValue({
    pages: { created: 1, skipped: 0, ids: ["3001"] },
    apis: { created: 0, skipped: 1, ids: [] },
  })
}

async function renderLoaded(params = ""): Promise<void> {
  search.params = new URLSearchParams(params)
  render(<CodeTab versionId="2001" />)
  await screen.findByRole("region", { name: "代码仓库状态" })
  await waitFor(() => expect(api.listFacts).toHaveBeenCalled())
}

describe("CodeTab", () => {
  beforeEach(() => {
    window.sessionStorage.clear()
    for (const mock of Object.values(api)) mock.mockReset()
    navigation.replace.mockReset()
    search.params = new URLSearchParams()
    codeEvents.handlers = null
    codeEvents.calls = 0
    defaultApiMocks()
  })

  afterEach(cleanup)

  it("loads the bound repository, analysis, changed files, facts, anchors and proposals", async () => {
    await renderLoaded()

    expect(api.listCodeRepos).toHaveBeenCalledWith("2001")
    expect(api.latestAnalysis).toHaveBeenCalledWith("2001", "1201")
    expect(api.getAnalysis).toHaveBeenCalledWith("2001", "7001")
    expect(api.listFacts).toHaveBeenCalledWith("1201", { commit: HEAD_COMMIT, limit: 100 })
    expect(api.listAnchors).toHaveBeenCalledWith("2001")
    expect(api.listProposals).toHaveBeenCalledWith("2001")

    expect(screen.getByLabelText("代码仓库")).toHaveValue("1201")
    const repositoryStatus = screen.getByRole("region", { name: "代码仓库状态" })
    expect(repositoryStatus).toHaveTextContent("基线 commit")
    expect(repositoryStatus).toHaveTextContent("aaaaaaa")

    const analysis = screen.getByRole("region", { name: "变更分析" })
    expect(analysis).toHaveTextContent("已完成")
    expect(analysis).toHaveTextContent("分析 #7001")
    expect(analysis).toHaveTextContent("bbbbbbb")

    const files = screen.getByRole("region", { name: "变更文件" })
    expect(files).toHaveTextContent("src/pages/RefundPage.tsx")
    expect(files).toHaveTextContent("src/api/refund.ts")
    expect(screen.getByRole("region", { name: "代码事实" })).toHaveTextContent("/refund")
    expect(screen.getByRole("region", { name: "影响候选" })).toHaveTextContent("6001")
    expect(screen.getByRole("region", { name: "代码锚点" })).toHaveTextContent(
      "src/pages/RefundPage.tsx",
    )
    expect(screen.getByRole("region", { name: "页面/API 提议" })).toHaveTextContent("退款页")
    expect(screen.getByText("版本通道已连接")).toBeInTheDocument()
  })

  it("shows a loading state while the repository list is pending", () => {
    api.listCodeRepos.mockReturnValue(new Promise(() => {}))

    render(<CodeTab versionId="2001" />)

    expect(screen.getByText("正在加载代码仓库…")).toBeInTheDocument()
  })

  it("shows a retryable error when the repositories cannot be loaded", async () => {
    api.listCodeRepos
      .mockRejectedValueOnce(
        new ApiError({ status: 503, code: "unavailable", message: "仓库服务不可用" }),
      )
      .mockResolvedValueOnce([repoStatusFixture()])

    render(<CodeTab versionId="2001" />)
    expect(await screen.findByText("代码仓库加载失败")).toBeInTheDocument()
    expect(screen.getByText("仓库服务不可用")).toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: "重试" }))

    expect(await screen.findByLabelText("代码仓库")).toHaveValue("1201")
    expect(api.listCodeRepos).toHaveBeenCalledTimes(2)
  })

  it("shows the unbound-repository empty state with a refresh CTA", async () => {
    api.listCodeRepos.mockResolvedValue([])

    render(<CodeTab versionId="2001" />)

    expect(await screen.findByText("未绑定代码仓库")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "刷新状态" })).toBeInTheDocument()
    expect(api.latestAnalysis).not.toHaveBeenCalled()
  })

  it("warns when there is no baseline and triggers a first index", async () => {
    api.listCodeRepos.mockResolvedValue([
      repoStatusFixture({ baseline_commit: null, latest_index: null }),
    ])
    api.latestAnalysis.mockResolvedValue(null)

    render(<CodeTab versionId="2001" />)
    await screen.findByRole("region", { name: "代码仓库状态" })
    await waitFor(() => expect(screen.getByLabelText("代码仓库")).toHaveValue("1201"))
    await waitFor(() => expect(api.latestAnalysis).toHaveBeenCalledWith("2001", "1201"))

    expect(screen.getByText("无代码基线")).toBeInTheDocument()
    expect(
      within(screen.getByRole("region", { name: "索引状态" })).getByText("尚未索引"),
    ).toBeInTheDocument()
    expect(api.getCodeChanges).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole("button", { name: "触发索引" }))

    await waitFor(() => expect(api.triggerIndex).toHaveBeenCalledWith("2001", { repoId: "1201" }))
    expect(await screen.findByText("索引运行已入队")).toBeInTheDocument()
  })

  it("moves from queued to running to done through SSE refreshes", async () => {
    api.listCodeRepos.mockReset()
    api.listCodeRepos.mockResolvedValue([
      repoStatusFixture({ baseline_commit: null, latest_index: null }),
    ])
    api.latestAnalysis.mockResolvedValue(null)
    render(<CodeTab versionId="2001" />)
    await screen.findByRole("region", { name: "代码仓库状态" })
    await waitFor(() => expect(screen.getByLabelText("代码仓库")).toHaveValue("1201"))

    expect(
      within(screen.getByRole("region", { name: "索引状态" })).getByText("尚未索引"),
    ).toBeInTheDocument()

    api.listCodeRepos.mockResolvedValue([
      repoStatusFixture({
        latest_index: indexRunFixture({ status: "running", files_indexed: 0, files_total: 2 }),
        index_in_progress: true,
      }),
    ])
    fireEvent.click(screen.getByRole("button", { name: "触发索引" }))
    await waitFor(() =>
      expect(
        within(screen.getByRole("region", { name: "索引状态" })).getByText("运行中"),
      ).toBeInTheDocument(),
    )

    api.listCodeRepos.mockResolvedValue([
      repoStatusFixture({ latest_index: indexRunFixture({ status: "done" }) }),
    ])
    act(() => codeEvents.handlers?.onCodeChanged?.())

    expect(await screen.findByText("文件 2/2")).toBeInTheDocument()
    await waitFor(() =>
      expect(
        within(screen.getByRole("region", { name: "索引状态" })).getByText("已完成"),
      ).toBeInTheDocument(),
    )
  })

  it("toggles a candidate optimistically and rolls back on PATCH failure", async () => {
    await renderLoaded()

    const checkbox = screen.getByLabelText("选择影响候选 需求 6001")
    expect(checkbox).not.toBeChecked()

    fireEvent.click(checkbox)
    expect(api.patchCandidate).toHaveBeenCalledWith("2001", "7001", "8101", true)
    await waitFor(() => expect(checkbox).toBeChecked())

    api.patchCandidate.mockRejectedValueOnce(
      new ApiError({ status: 409, code: "candidate_conflict", message: "候选已被他人修改" }),
    )
    fireEvent.click(checkbox)

    expect(await screen.findByText("候选已被他人修改")).toBeInTheDocument()
    await waitFor(() => expect(checkbox).toBeChecked())
  })

  it("opens the diff drawer with binary/large markers and closes on Escape", async () => {
    await renderLoaded()
    api.readDiffFile.mockResolvedValueOnce(
      diffFixture({
        path: "public/refund.png",
        binary: true,
        patch: null,
        additions: 0,
        deletions: 0,
      }),
    )

    fireEvent.click(screen.getByRole("button", { name: /public\/refund\.png/ }))

    const dialog = await screen.findByRole("dialog", { name: "public/refund.png" })
    expect(within(dialog).getByText("二进制文件不显示 patch")).toBeInTheDocument()
    expect(api.readDiffFile).toHaveBeenCalledWith("1201", {
      base: BASE_COMMIT,
      head: HEAD_COMMIT,
      path: "public/refund.png",
    })

    fireEvent.keyDown(window, { key: "Escape" })
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())

    api.readDiffFile.mockResolvedValueOnce(diffFixture({ large: true, patch: null }))
    fireEvent.click(screen.getByRole("button", { name: /src\/pages\/RefundPage\.tsx/ }))
    expect(await screen.findByText("patch 超过 256 KiB，已截断")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "关闭" }))
  })

  it("reviews a fact with a verdict and reason", async () => {
    await renderLoaded()

    const row = screen.getByRole("button", { name: "查看事实 /refund" }).closest("li")
    expect(row).not.toBeNull()
    fireEvent.click(within(row as HTMLElement).getByRole("button", { name: "复核" }))

    const dialog = await screen.findByRole("dialog", { name: "事实复核" })
    expect(within(dialog).getByText(/行号准确/)).toBeInTheDocument()

    fireEvent.change(within(dialog).getByLabelText("复核说明（可选）"), {
      target: { value: "行号准确" },
    })
    fireEvent.click(within(dialog).getByRole("button", { name: "标记正确" }))

    await waitFor(() =>
      expect(api.createReview).toHaveBeenCalledWith("2001", "9101", {
        verdict: "correct",
        reason: "行号准确",
      }),
    )
    expect(await screen.findByText("事实复核已记录")).toBeInTheDocument()
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())
  })

  it("honours ?repo/?analysis/?candidate/?anchor/?fact deep links", async () => {
    api.latestAnalysis.mockResolvedValue(analysisViewFixture({ id: "7001" }))
    api.getAnalysis.mockResolvedValue(
      analysisDetailFixture({ analysis: analysisViewFixture({ id: "7002" }) }),
    )

    search.params = new URLSearchParams(
      "tab=code&repo=1201&analysis=7002&candidate=8101&anchor=8201&fact=9101",
    )
    render(<CodeTab versionId="2001" />)
    await screen.findByRole("region", { name: "代码仓库状态" })

    expect(screen.getByLabelText("代码仓库")).toHaveValue("1201")
    await waitFor(() => expect(api.getAnalysis).toHaveBeenCalledWith("2001", "7002"))
    expect(await screen.findByText(/分析 #7002/)).toBeInTheDocument()

    expect(screen.getByLabelText("选择影响候选 需求 6001").closest("li")).toHaveAttribute(
      "aria-current",
      "true",
    )
    expect(screen.getByRole("listitem", { name: "锚点 8201" })).toHaveAttribute(
      "aria-current",
      "true",
    )
    const factRow = screen.getByRole("button", { name: "查看事实 /refund" }).closest("li")
    expect(factRow).toHaveAttribute("aria-current", "true")
    expect(navigation.replace).not.toHaveBeenCalled()
  })

  it("writes repo/candidate/anchor/fact/analysis deep links back to the URL", async () => {
    api.listCodeRepos.mockResolvedValue([
      repoStatusFixture(),
      repoStatusFixture({
        repo_id: "1301",
        repository: { ...repositoryFixture, id: "1301", name: "payment-web" },
      }),
    ])
    await renderLoaded()

    fireEvent.change(screen.getByLabelText("代码仓库"), { target: { value: "1301" } })
    expect(navigation.replace).toHaveBeenLastCalledWith("/versions/2001?tab=code&repo=1301")

    fireEvent.click(await screen.findByRole("button", { name: "定位影响候选 需求 6001" }))
    expect(navigation.replace).toHaveBeenLastCalledWith("/versions/2001?tab=code&candidate=8101")

    fireEvent.click(await screen.findByRole("button", { name: "定位锚点 8201" }))
    expect(navigation.replace).toHaveBeenLastCalledWith("/versions/2001?tab=code&anchor=8201")

    fireEvent.click(await screen.findByRole("button", { name: "查看事实 /refund" }))
    expect(navigation.replace).toHaveBeenLastCalledWith("/versions/2001?tab=code&fact=9101")
    expect(api.readFile).toHaveBeenCalledWith("1201", {
      commit: HEAD_COMMIT,
      path: "src/pages/RefundPage.tsx",
      startLine: 12,
      endLine: 16,
    })

    fireEvent.click(screen.getByRole("button", { name: "触发分析" }))
    await waitFor(() =>
      expect(api.startAnalysis).toHaveBeenCalledWith("2001", { repoId: "1301", force: true }),
    )
    expect(navigation.replace).toHaveBeenLastCalledWith("/versions/2001?tab=code&analysis=7001")
  })

  it("debounces code.* SSE events into a single snapshot reload", async () => {
    await renderLoaded()
    expect(api.listFacts).toHaveBeenCalledTimes(1)

    act(() => {
      codeEvents.handlers?.onCodeChanged?.()
      codeEvents.handlers?.onCodeChanged?.()
    })

    await waitFor(() => expect(api.listFacts).toHaveBeenCalledTimes(2), { timeout: 2000 })
    expect(api.listAnchors).toHaveBeenCalledTimes(2)
    expect(codeEvents.calls).toBe(1)
  })

  it("shows degraded and truncated states", async () => {
    api.latestAnalysis.mockResolvedValue(analysisViewFixture({ degraded: true }))
    api.getAnalysis.mockResolvedValue(
      analysisDetailFixture({ analysis: analysisViewFixture({ degraded: true }) }),
    )
    api.listCodeRepos.mockResolvedValue([
      repoStatusFixture({ latest_index: indexRunFixture({ truncated: true }) }),
    ])

    await renderLoaded()

    expect(screen.getByText("AI 事实层已降级")).toBeInTheDocument()
    expect(screen.getByText("AI 推理降级")).toBeInTheDocument()
    expect(screen.getAllByText(/已截断/).length).toBeGreaterThan(0)
  })

  it("shows the no-changes empty state for a done analysis without files", async () => {
    api.getAnalysis.mockResolvedValue(analysisDetailFixture({ changed_files: [] }))

    await renderLoaded()

    expect(
      within(screen.getByRole("region", { name: "变更文件" })).getByText("暂无变更文件"),
    ).toBeInTheDocument()
  })
})
