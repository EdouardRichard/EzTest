import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { IndexRun, RepoSummary } from "@/features/code-intel/types"
import { ApiError } from "@/lib/api-client"

const mocks = vi.hoisted(() => ({
  listCodeRepos: vi.fn(),
  createCodeRepo: vi.fn(),
  updateCodeRepo: vi.fn(),
  deleteCodeRepo: vi.fn(),
  validateCodeRepo: vi.fn(),
}))

const indexMocks = vi.hoisted(() => ({
  getCodeRepos: vi.fn(),
  getLatestIndexRun: vi.fn(),
  startIndex: vi.fn(),
}))

const authState = vi.hoisted(() => ({ role: "admin" as "admin" | "user" }))

vi.mock("./api", () => ({
  listCodeRepos: mocks.listCodeRepos,
  createCodeRepo: mocks.createCodeRepo,
  updateCodeRepo: mocks.updateCodeRepo,
  deleteCodeRepo: mocks.deleteCodeRepo,
  validateCodeRepo: mocks.validateCodeRepo,
}))

vi.mock("@/features/code-intel/api", () => ({
  getCodeRepos: indexMocks.getCodeRepos,
  getLatestIndexRun: indexMocks.getLatestIndexRun,
  startIndex: indexMocks.startIndex,
}))

vi.mock("@/features/auth/AuthProvider", () => ({
  useAuth: () => ({
    user: { id: 1, username: "admin", role: authState.role, status: "active" },
    loading: false,
    login: vi.fn(),
    logout: vi.fn(),
  }),
}))

import { CodeReposPage } from "./CodeReposPage"

const REPO = {
  id: 1,
  key: "eztest",
  name: "EzTest",
  localPath: "D:/repo",
  fixedBranch: "dev",
  includeGlobs: ["src/**"],
  excludeGlobs: [".git/**"],
  status: "active",
  lastCommit: "abc",
  lastValidatedAt: "2026-10-27T08:00:00Z",
  lastValidationError: "",
  lastIndexedAt: null,
}

/** A snowflake-shaped run id that must stay a string end to end (spec §7.1). */
const RUN_ID = "1844674407370955164"
const INDEX_COMMIT = "def456789abcdef0123456789abcdef012345678"
const INDEX_FINISHED_AT = "2026-10-27T09:32:03Z"

const DONE_INDEX_RUN: IndexRun = {
  runId: RUN_ID,
  repoId: REPO.id,
  commit: INDEX_COMMIT,
  status: "done",
  stats: { files: 1200, truncated: false },
  startedAt: "2026-10-27T09:30:00Z",
  finishedAt: INDEX_FINISHED_AT,
}

function repoSummary(overrides: Partial<RepoSummary> = {}): RepoSummary {
  return {
    id: REPO.id,
    key: REPO.key,
    name: REPO.name,
    status: REPO.status,
    fixedBranch: REPO.fixedBranch,
    lastCommit: REPO.lastCommit,
    lastIndexedAt: INDEX_FINISHED_AT,
    index: DONE_INDEX_RUN,
    ...overrides,
  }
}

const UNINDEXED_SUMMARY = repoSummary({ lastIndexedAt: null, index: null })

const RUNNING_INDEX_RUN: IndexRun = {
  ...DONE_INDEX_RUN,
  status: "running",
  stats: undefined,
  finishedAt: null,
}

function runningSummary(): RepoSummary {
  return repoSummary({ index: RUNNING_INDEX_RUN })
}

/** Resolves the initial effect promises while fake timers are installed. */
async function flushPromises(): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0)
  })
}

async function advanceTimers(ms: number): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms)
  })
}

function apiError(status: number, reason: string): ApiError {
  return new ApiError({
    status,
    code: status === 422 ? "invalid_argument" : "conflict",
    message: "validation failed",
    details: { reason, field: "fixedBranch" },
  })
}

function indexError(status: number, reason: string): ApiError {
  return new ApiError({
    status,
    code: status === 409 ? "conflict" : "invalid_argument",
    message: "index trigger failed",
    details: { reason },
  })
}

describe("CodeReposPage", () => {
  beforeEach(() => {
    authState.role = "admin"
    mocks.listCodeRepos.mockReset()
    mocks.createCodeRepo.mockReset()
    mocks.updateCodeRepo.mockReset()
    mocks.deleteCodeRepo.mockReset()
    mocks.validateCodeRepo.mockReset()
    indexMocks.getCodeRepos.mockReset()
    indexMocks.getLatestIndexRun.mockReset()
    indexMocks.startIndex.mockReset()
    mocks.listCodeRepos.mockResolvedValue({ items: [REPO], nextCursor: null, hasMore: false })
    indexMocks.getCodeRepos.mockResolvedValue([UNINDEXED_SUMMARY])
  })

  afterEach(() => {
    cleanup()
  })

  it("shows the last indexed time and commit and drops the P4 placeholder", async () => {
    mocks.listCodeRepos.mockResolvedValue({
      items: [{ ...REPO, lastValidatedAt: null }],
      nextCursor: null,
      hasMore: false,
    })
    indexMocks.getCodeRepos.mockResolvedValue([repoSummary()])

    render(<CodeReposPage />)

    expect(await screen.findByText("EzTest")).toBeTruthy()
    const row = screen.getByText("EzTest").closest("tr") as HTMLTableRowElement
    const scope = within(row)
    expect(scope.getByText("已索引")).toBeTruthy()
    expect(scope.getByText(/2026/)).toBeTruthy()
    expect(scope.getByText("def45678")).toBeTruthy()
    expect(scope.queryByText(/未索引（P4）/)).toBeNull()
    expect(scope.queryByText(/未索引/)).toBeNull()
  })

  it("shows the failure reason for a failed index run", async () => {
    indexMocks.getCodeRepos.mockResolvedValue([
      repoSummary({
        lastIndexedAt: null,
        index: {
          ...DONE_INDEX_RUN,
          status: "failed",
          stats: undefined,
          errors: [{ code: "git_unavailable", message: "Git 仓库不可访问" }],
        },
      }),
    ])

    render(<CodeReposPage />)

    expect(await screen.findByText("EzTest")).toBeTruthy()
    const row = screen.getByText("EzTest").closest("tr") as HTMLTableRowElement
    expect(within(row).getByText("索引失败")).toBeTruthy()
    expect(within(row).getByText(/Git 仓库不可访问/)).toBeTruthy()
    expect(within(row).getByRole("button", { name: "刷新索引" })).toBeTruthy()
  })

  it("lets admins refresh the index and polls until it is done", async () => {
    vi.useFakeTimers()
    try {
      mocks.listCodeRepos
        .mockResolvedValueOnce({ items: [REPO], nextCursor: null, hasMore: false })
        .mockResolvedValue({
          items: [{ ...REPO, lastIndexedAt: INDEX_FINISHED_AT }],
          nextCursor: null,
          hasMore: false,
        })
      indexMocks.getCodeRepos
        .mockResolvedValueOnce([UNINDEXED_SUMMARY])
        .mockResolvedValue([repoSummary()])
      indexMocks.startIndex.mockResolvedValue({
        runId: RUN_ID,
        status: "queued",
        commit: INDEX_COMMIT,
      })
      indexMocks.getLatestIndexRun
        .mockResolvedValueOnce({
          ...DONE_INDEX_RUN,
          status: "running",
          stats: undefined,
          finishedAt: null,
        })
        .mockResolvedValueOnce(DONE_INDEX_RUN)

      render(<CodeReposPage />)
      await flushPromises()
      expect(screen.getByText("未索引")).toBeTruthy()

      fireEvent.click(screen.getByRole("button", { name: "刷新索引" }))
      await flushPromises()

      expect(indexMocks.startIndex).toHaveBeenCalledWith(REPO.id)
      expect(typeof indexMocks.startIndex.mock.calls[0]?.[0]).toBe("number")
      expect(screen.getByText("排队中")).toBeTruthy()
      expect((screen.getByRole("button", { name: "刷新索引" }) as HTMLButtonElement).disabled).toBe(
        true,
      )

      await act(async () => {
        await vi.advanceTimersByTimeAsync(1500)
      })
      await flushPromises()
      expect(indexMocks.getLatestIndexRun).toHaveBeenCalledWith(REPO.id)
      expect(indexMocks.getLatestIndexRun).toHaveBeenCalledTimes(1)
      expect(screen.getByText("索引中")).toBeTruthy()

      await act(async () => {
        await vi.advanceTimersByTimeAsync(1500)
      })
      await flushPromises()
      expect(indexMocks.getLatestIndexRun).toHaveBeenCalledTimes(2)
      expect(screen.getByText("已索引")).toBeTruthy()
      expect(screen.getByText("def45678")).toBeTruthy()
      expect((screen.getByRole("button", { name: "刷新索引" }) as HTMLButtonElement).disabled).toBe(
        false,
      )

      // Terminal status stops the 1.5s poll and refreshes both lists.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(6000)
      })
      expect(indexMocks.getLatestIndexRun).toHaveBeenCalledTimes(2)
      expect(mocks.listCodeRepos).toHaveBeenCalledTimes(2)
      expect(indexMocks.getCodeRepos).toHaveBeenCalledTimes(2)
    } finally {
      vi.useRealTimers()
    }
  })

  it("stops polling after 60 attempts and asks for a manual refresh", async () => {
    vi.useFakeTimers()
    try {
      indexMocks.startIndex.mockResolvedValue({
        runId: RUN_ID,
        status: "queued",
        commit: INDEX_COMMIT,
      })
      indexMocks.getLatestIndexRun.mockResolvedValue({
        ...DONE_INDEX_RUN,
        status: "running",
        stats: undefined,
        finishedAt: null,
      })

      render(<CodeReposPage />)
      await flushPromises()

      fireEvent.click(screen.getByRole("button", { name: "刷新索引" }))
      await flushPromises()

      await act(async () => {
        await vi.advanceTimersByTimeAsync(1500 * 60)
      })
      await flushPromises()

      expect(indexMocks.getLatestIndexRun).toHaveBeenCalledTimes(60)
      expect(screen.getByText(/已停止自动刷新/)).toBeTruthy()

      await act(async () => {
        await vi.advanceTimersByTimeAsync(6000)
      })
      expect(indexMocks.getLatestIndexRun).toHaveBeenCalledTimes(60)
    } finally {
      vi.useRealTimers()
    }
  })

  it("stops polling on unmount", async () => {
    vi.useFakeTimers()
    try {
      indexMocks.startIndex.mockResolvedValue({
        runId: RUN_ID,
        status: "queued",
        commit: INDEX_COMMIT,
      })
      indexMocks.getLatestIndexRun.mockResolvedValue({
        ...DONE_INDEX_RUN,
        status: "running",
        stats: undefined,
        finishedAt: null,
      })

      const view = render(<CodeReposPage />)
      await flushPromises()

      fireEvent.click(screen.getByRole("button", { name: "刷新索引" }))
      await flushPromises()
      await act(async () => {
        await vi.advanceTimersByTimeAsync(1500)
      })
      await flushPromises()
      expect(indexMocks.getLatestIndexRun).toHaveBeenCalledTimes(1)

      view.unmount()
      const listCalls = mocks.listCodeRepos.mock.calls.length
      await act(async () => {
        await vi.advanceTimersByTimeAsync(6000)
      })
      expect(indexMocks.getLatestIndexRun).toHaveBeenCalledTimes(1)
      expect(mocks.listCodeRepos).toHaveBeenCalledTimes(listCalls)
    } finally {
      vi.useRealTimers()
    }
  })

  it("shows an index error toast and does not poll when the trigger fails", async () => {
    indexMocks.startIndex.mockRejectedValueOnce(indexError(409, "index_in_progress"))

    render(<CodeReposPage />)
    await screen.findByText("EzTest")

    fireEvent.click(screen.getByRole("button", { name: "刷新索引" }))

    const toast = await screen.findByRole("alert")
    expect(toast.textContent).toContain("已有索引任务在运行")
    expect(indexMocks.getLatestIndexRun).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole("button", { name: "关闭提示" }))
    await waitFor(() => expect(screen.queryByRole("alert")).toBeNull())
  })

  it("hides every write action for a readonly user and shows the readonly badge", async () => {
    authState.role = "user"

    render(<CodeReposPage />)

    expect(await screen.findByText("EzTest")).toBeTruthy()
    expect(screen.getByText("只读")).toBeTruthy()
    expect(screen.queryByRole("button", { name: "新建代码仓" })).toBeNull()
    expect(screen.queryByRole("button", { name: "编辑" })).toBeNull()
    expect(screen.queryByRole("button", { name: "删除" })).toBeNull()
    expect(screen.queryByRole("button", { name: "重新校验" })).toBeNull()
    expect(screen.queryByRole("button", { name: "刷新索引" })).toBeNull()
    expect(screen.queryByLabelText("本地路径")).toBeNull()
  })

  it("shows the 422 reason when a re-validation fails", async () => {
    mocks.validateCodeRepo.mockRejectedValueOnce(apiError(422, "branch_not_found"))

    render(<CodeReposPage />)
    await screen.findByText("EzTest")

    fireEvent.click(screen.getByRole("button", { name: "重新校验" }))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("branch_not_found")
    expect(alert.textContent).toContain("分支不存在")
  })

  it("refreshes the commit after a successful validation and announces it", async () => {
    mocks.validateCodeRepo.mockResolvedValueOnce({
      ...REPO,
      lastCommit: "def456",
      lastValidatedAt: "2026-10-27T09:30:00Z",
    })

    render(<CodeReposPage />)
    await screen.findByText("abc")

    fireEvent.click(screen.getByRole("button", { name: "重新校验" }))

    await waitFor(() => expect(mocks.validateCodeRepo).toHaveBeenCalledWith(1))
    expect(await screen.findByText("def456")).toBeTruthy()
    const status = screen.getByRole("status")
    expect(status.textContent).toContain("已重新校验")
  })

  it("creates a repo and sends comma-separated globs as arrays", async () => {
    const created = {
      ...REPO,
      id: 2,
      key: "eztest-web",
      name: "EzTest Web",
      localPath: "D:/repo2",
      fixedBranch: "main",
    }
    mocks.listCodeRepos.mockResolvedValue({
      items: [REPO, created],
      nextCursor: null,
      hasMore: false,
    })
    mocks.createCodeRepo.mockResolvedValueOnce(created)

    render(<CodeReposPage />)
    await screen.findByText("EzTest")

    fireEvent.click(screen.getByRole("button", { name: "新建代码仓" }))
    fireEvent.change(screen.getByLabelText("标识"), { target: { value: "eztest-web" } })
    fireEvent.change(screen.getByLabelText("名称"), { target: { value: "EzTest Web" } })
    fireEvent.change(screen.getByLabelText("本地路径"), { target: { value: "D:/repo2" } })
    fireEvent.change(screen.getByLabelText("固定分支"), { target: { value: "main" } })
    fireEvent.change(screen.getByLabelText(/包含 Globs/), {
      target: { value: "src/**, app/**" },
    })
    fireEvent.change(screen.getByLabelText(/排除 Globs/), {
      target: { value: ".git/**, node_modules/**" },
    })
    fireEvent.click(screen.getByRole("button", { name: "保存" }))

    await waitFor(() =>
      expect(mocks.createCodeRepo).toHaveBeenCalledWith({
        key: "eztest-web",
        name: "EzTest Web",
        localPath: "D:/repo2",
        fixedBranch: "main",
        includeGlobs: ["src/**", "app/**"],
        excludeGlobs: [".git/**", "node_modules/**"],
      }),
    )
    expect(await screen.findByText("EzTest Web")).toBeTruthy()
    expect(screen.getByRole("status").textContent).toContain("已创建")
  })

  it("prefills the documented default exclude globs for a new repository", async () => {
    const created = {
      ...REPO,
      id: 3,
      key: "eztest-defaults",
      name: "EzTest Defaults",
      localPath: "D:/repo3",
      fixedBranch: "main",
    }
    mocks.listCodeRepos.mockResolvedValue({
      items: [REPO, created],
      nextCursor: null,
      hasMore: false,
    })
    mocks.createCodeRepo.mockResolvedValueOnce(created)

    render(<CodeReposPage />)
    await screen.findByText("EzTest")

    fireEvent.click(screen.getByRole("button", { name: "新建代码仓" }))
    const excludeInput = screen.getByLabelText(/排除 Globs/) as HTMLInputElement
    expect(excludeInput.value).toBe(".git/**, node_modules/**, dist/**, build/**, vendor/**")

    fireEvent.change(screen.getByLabelText("标识"), { target: { value: "eztest-defaults" } })
    fireEvent.change(screen.getByLabelText("名称"), { target: { value: "EzTest Defaults" } })
    fireEvent.change(screen.getByLabelText("本地路径"), { target: { value: "D:/repo3" } })
    fireEvent.click(screen.getByRole("button", { name: "保存" }))

    await waitFor(() =>
      expect(mocks.createCodeRepo).toHaveBeenCalledWith(
        expect.objectContaining({
          excludeGlobs: [".git/**", "node_modules/**", "dist/**", "build/**", "vendor/**"],
        }),
      ),
    )
  })

  it("sends an empty exclude list when the prefilled defaults are cleared", async () => {
    const created = {
      ...REPO,
      id: 4,
      key: "eztest-no-excludes",
      name: "EzTest No Excludes",
      localPath: "D:/repo4",
      fixedBranch: "main",
    }
    mocks.listCodeRepos.mockResolvedValue({
      items: [REPO, created],
      nextCursor: null,
      hasMore: false,
    })
    mocks.createCodeRepo.mockResolvedValueOnce(created)

    render(<CodeReposPage />)
    await screen.findByText("EzTest")

    fireEvent.click(screen.getByRole("button", { name: "新建代码仓" }))
    fireEvent.change(screen.getByLabelText("标识"), { target: { value: "eztest-no-excludes" } })
    fireEvent.change(screen.getByLabelText("名称"), { target: { value: "EzTest No Excludes" } })
    fireEvent.change(screen.getByLabelText("本地路径"), { target: { value: "D:/repo4" } })
    fireEvent.change(screen.getByLabelText(/排除 Globs/), { target: { value: "" } })
    fireEvent.click(screen.getByRole("button", { name: "保存" }))

    await waitFor(() =>
      expect(mocks.createCodeRepo).toHaveBeenCalledWith(
        expect.objectContaining({ excludeGlobs: [] }),
      ),
    )
  })

  it("edits a repo without sending the immutable key", async () => {
    mocks.updateCodeRepo.mockResolvedValueOnce({ ...REPO, localPath: "D:/moved-repo" })

    render(<CodeReposPage />)
    await screen.findByText("EzTest")

    fireEvent.click(screen.getByRole("button", { name: "编辑" }))
    expect((screen.getByLabelText("标识") as HTMLInputElement).disabled).toBe(true)
    expect((screen.getByLabelText("名称") as HTMLInputElement).value).toBe("EzTest")

    fireEvent.change(screen.getByLabelText("本地路径"), { target: { value: "D:/moved-repo" } })
    fireEvent.click(screen.getByRole("button", { name: "保存" }))

    await waitFor(() => expect(mocks.updateCodeRepo).toHaveBeenCalledWith(1, expect.anything()))
    const [, payload] = mocks.updateCodeRepo.mock.calls[0] as [number, Record<string, unknown>]
    expect(payload).not.toHaveProperty("key")
    expect(payload.localPath).toBe("D:/moved-repo")
  })

  it("deletes a repo only after confirmation", async () => {
    mocks.deleteCodeRepo.mockResolvedValueOnce(undefined)

    render(<CodeReposPage />)
    await screen.findByText("EzTest")

    fireEvent.click(screen.getByRole("button", { name: "删除" }))
    expect(mocks.deleteCodeRepo).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole("button", { name: "确认删除" }))

    await waitFor(() => expect(mocks.deleteCodeRepo).toHaveBeenCalledWith(1))
  })

  it("shows the 409 reason when a save fails", async () => {
    mocks.createCodeRepo.mockRejectedValueOnce(apiError(409, "duplicate_key"))

    render(<CodeReposPage />)
    await screen.findByText("EzTest")

    fireEvent.click(screen.getByRole("button", { name: "新建代码仓" }))
    fireEvent.change(screen.getByLabelText("标识"), { target: { value: "eztest" } })
    fireEvent.change(screen.getByLabelText("名称"), { target: { value: "EzTest" } })
    fireEvent.change(screen.getByLabelText("本地路径"), { target: { value: "D:/repo" } })
    fireEvent.change(screen.getByLabelText("固定分支"), { target: { value: "dev" } })
    fireEvent.click(screen.getByRole("button", { name: "保存" }))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("duplicate_key")
  })

  it("polls an active index run discovered on the initial list load", async () => {
    vi.useFakeTimers()
    try {
      indexMocks.getCodeRepos.mockResolvedValue([runningSummary()])
      indexMocks.getLatestIndexRun.mockResolvedValueOnce(DONE_INDEX_RUN)

      render(<CodeReposPage />)
      await flushPromises()

      expect(indexMocks.startIndex).not.toHaveBeenCalled()
      expect(screen.getByText("索引中")).toBeTruthy()
      expect(indexMocks.getLatestIndexRun).not.toHaveBeenCalled()

      await advanceTimers(1500)
      expect(indexMocks.getLatestIndexRun).toHaveBeenCalledWith(REPO.id)
      expect(screen.getByText("已索引")).toBeTruthy()

      // The terminal status ends the 1.5s poll for the discovered run.
      await advanceTimers(6000)
      expect(indexMocks.getLatestIndexRun).toHaveBeenCalledTimes(1)
    } finally {
      vi.useRealTimers()
    }
  })

  it("polls the active run after a 409 index trigger and keeps the reason toast", async () => {
    vi.useFakeTimers()
    try {
      indexMocks.startIndex.mockRejectedValueOnce(indexError(409, "index_in_progress"))
      indexMocks.getCodeRepos
        .mockResolvedValueOnce([UNINDEXED_SUMMARY])
        .mockResolvedValue([runningSummary()])
      indexMocks.getLatestIndexRun
        .mockResolvedValueOnce(RUNNING_INDEX_RUN)
        .mockResolvedValue(DONE_INDEX_RUN)

      render(<CodeReposPage />)
      await flushPromises()
      fireEvent.click(screen.getByRole("button", { name: "刷新索引" }))
      await flushPromises()

      const toast = screen.getByRole("alert")
      expect(toast.textContent).toContain("已有索引任务在运行")
      // The 409 recovery reload discovered the run another tab/admin started.
      expect(indexMocks.getCodeRepos.mock.calls.length).toBeGreaterThanOrEqual(2)
      expect(screen.getByText("索引中")).toBeTruthy()

      await advanceTimers(1500)
      expect(indexMocks.getLatestIndexRun).toHaveBeenCalledTimes(1)
      expect(screen.getByText("索引中")).toBeTruthy()

      await advanceTimers(1500)
      expect(indexMocks.getLatestIndexRun).toHaveBeenCalledTimes(2)
      expect(screen.getByText("已索引")).toBeTruthy()
    } finally {
      vi.useRealTimers()
    }
  })

  it("clears the exhausted hint when a refresh settles with a terminal run", async () => {
    vi.useFakeTimers()
    try {
      indexMocks.startIndex.mockResolvedValue({
        runId: RUN_ID,
        status: "queued",
        commit: INDEX_COMMIT,
      })
      indexMocks.getLatestIndexRun.mockResolvedValue(RUNNING_INDEX_RUN)

      render(<CodeReposPage />)
      await flushPromises()
      fireEvent.click(screen.getByRole("button", { name: "刷新索引" }))
      await flushPromises()

      await advanceTimers(1500 * 60)
      expect(indexMocks.getLatestIndexRun).toHaveBeenCalledTimes(60)
      expect(screen.getByText(/已停止自动刷新/)).toBeTruthy()

      // A manual refresh that observes the terminal run clears the stale hint.
      indexMocks.getLatestIndexRun.mockResolvedValue(DONE_INDEX_RUN)
      indexMocks.getCodeRepos.mockResolvedValue([repoSummary()])
      fireEvent.click(screen.getByRole("button", { name: "刷新" }))
      await flushPromises()

      expect(screen.queryByText(/已停止自动刷新/)).toBeNull()
      expect(screen.getByText("已索引")).toBeTruthy()
    } finally {
      vi.useRealTimers()
    }
  })

  it("clears a previous index read error after a successful load", async () => {
    indexMocks.getCodeRepos.mockRejectedValueOnce(new Error("索引状态读取失败"))
    mocks.createCodeRepo.mockResolvedValue({
      ...REPO,
      id: 2,
      key: "eztest-web",
      name: "EzTest Web",
      localPath: "D:/repo2",
      fixedBranch: "main",
    })

    render(<CodeReposPage />)

    const toast = await screen.findByRole("alert")
    expect(toast.textContent).toContain("索引状态读取失败")

    // Creating a repository ends in a successful list/index read, which
    // supersedes the stale read error without a manual dismiss.
    fireEvent.click(screen.getByRole("button", { name: "新建代码仓" }))
    fireEvent.change(screen.getByLabelText("标识"), { target: { value: "eztest-web" } })
    fireEvent.change(screen.getByLabelText("名称"), { target: { value: "EzTest Web" } })
    fireEvent.change(screen.getByLabelText("本地路径"), { target: { value: "D:/repo2" } })
    fireEvent.click(screen.getByRole("button", { name: "保存" }))

    await waitFor(() => expect(screen.queryByRole("alert")).toBeNull())
  })
})
