import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

const api = vi.hoisted(() => ({
  getOverview: vi.fn(),
  listWaves: vi.fn(),
  getWave: vi.fn(),
  listCaseRuns: vi.fn(),
  getCaseRun: vi.fn(),
  getEvidence: vi.fn(),
  downloadEvidence: vi.fn(),
  listHumanRequests: vi.fn(),
  pauseWave: vi.fn(),
  resumeWave: vi.fn(),
  cancelWave: vi.fn(),
  pauseCaseRun: vi.fn(),
  resumeCaseRun: vi.fn(),
  resolveHumanRequest: vi.fn(),
  cancelHumanRequest: vi.fn(),
}))
vi.mock("./api", () => api)

const navigation = vi.hoisted(() => ({ replace: vi.fn() }))
vi.mock("next/navigation", () => ({ useRouter: () => navigation }))

type EventHandlers = Record<string, (...args: unknown[]) => void>
const events = vi.hoisted(() => ({ handlers: null as EventHandlers | null }))
vi.mock("@/features/clarification/useVersionEvents", () => ({
  useVersionEvents: (_versionId: string, handlers: EventHandlers) => {
    events.handlers = handlers
    return "live"
  },
}))

import { ExecutionTab } from "./ExecutionTab"
import {
  blockedCaseRunFixture,
  caseRunDetailFixture,
  caseRunsFixture,
  clarificationQuestionFixture,
  doneWaveFixture,
  emptyOverviewFixture,
  evidenceTextFixture,
  executionOverviewFixture,
  failedCaseRunFixture,
  humanRequestFixture,
  offlineExecutorOverviewFixture,
  passedCaseRunFixture,
  pausedWaveFixture,
  runningCaseRunFixture,
  runningWaveFixture,
} from "./test-fixtures"
import type { CaseRun, ExecutionOverview, RunWave, WaveCounts } from "./types"

function zeroWaveCounts(): WaveCounts {
  return {
    total: 0,
    pending: 0,
    running: 0,
    paused: 0,
    passed: 0,
    failed: 0,
    blocked: 0,
    inconclusive: 0,
    skipped: 0,
    interrupted: 0,
    superseded: 0,
    evidence: 0,
    by_version_sut: {},
  }
}

/** A running wave whose persisted counts projection is still all zeros. */
function staleWave(overrides: Partial<RunWave> = {}): RunWave {
  return { ...runningWaveFixture, counts: zeroWaveCounts(), ...overrides }
}

/** Five real case runs: 2 passed / 1 failed / 1 blocked / 1 running. */
function fiveCaseRuns(): CaseRun[] {
  return [
    passedCaseRunFixture,
    {
      ...passedCaseRunFixture,
      id: "9311",
      intent_id: "9111",
      case_id: "7011",
      case_version_id: "7012",
      title: "退款金额四舍五入",
    },
    {
      ...blockedCaseRunFixture,
      summary: {
        ...blockedCaseRunFixture.summary,
        terminal_cause: "blocked",
        last_error: "缺少管理员账号",
        next_action: "resolve_blocker",
      },
    },
    failedCaseRunFixture,
    runningCaseRunFixture,
  ]
}

function mockWorkspace() {
  api.getOverview.mockResolvedValue(executionOverviewFixture)
  api.listWaves.mockResolvedValue([runningWaveFixture, doneWaveFixture])
  api.listCaseRuns.mockResolvedValue(caseRunsFixture)
  api.getCaseRun.mockResolvedValue(caseRunDetailFixture)
  api.getEvidence.mockResolvedValue(evidenceTextFixture)
  api.downloadEvidence.mockResolvedValue({
    blob: new Blob(["PASS login as admin"], { type: "text/plain" }),
    filename: "console.log",
    contentType: "text/plain",
  })
  api.listHumanRequests.mockResolvedValue([])
  api.pauseWave.mockResolvedValue(pausedWaveFixture)
  api.resumeWave.mockResolvedValue(runningWaveFixture)
  api.cancelWave.mockResolvedValue({ ...runningWaveFixture, status: "cancelled" })
  api.pauseCaseRun.mockResolvedValue({ ...caseRunsFixture[3], status: "paused" })
  api.resumeCaseRun.mockResolvedValue(caseRunsFixture[3])
  api.resolveHumanRequest.mockResolvedValue({ ...humanRequestFixture, status: "answered" })
  api.cancelHumanRequest.mockResolvedValue({ ...humanRequestFixture, status: "cancelled" })
}

describe("ExecutionTab", () => {
  beforeEach(() => {
    for (const fn of Object.values(api)) fn.mockReset()
    navigation.replace.mockReset()
    events.handlers = null
    mockWorkspace()
  })

  afterEach(() => {
    cleanup()
    window.history.replaceState({}, "", "/")
  })

  it("shows a loading state while the execution snapshot loads", () => {
    const pending = new Promise<never>(() => {})
    api.getOverview.mockReturnValue(pending)
    api.listWaves.mockReturnValue(pending)
    api.listHumanRequests.mockReturnValue(pending)

    render(<ExecutionTab versionId="2001" />)

    expect(screen.getByText("正在加载执行工作区…")).toBeInTheDocument()
  })

  it("loads the overview, waves and case runs into the three-column workspace", async () => {
    render(<ExecutionTab versionId="2001" />)

    expect(await screen.findByRole("heading", { name: "波次 #9201" })).toBeInTheDocument()
    expect(api.getOverview).toHaveBeenCalledWith("2001")
    expect(api.listWaves).toHaveBeenCalledWith("2001")
    expect(api.listHumanRequests).toHaveBeenCalledWith("2001", { status: "pending" })
    await waitFor(() => {
      expect(api.listCaseRuns).toHaveBeenCalledWith("2001", "9201")
    })
    await waitFor(() => {
      expect(api.getCaseRun).toHaveBeenCalledWith("2001", "9301")
    })

    const toolbar = screen.getByTestId("execution-toolbar")
    expect(toolbar).toHaveTextContent("通过 2")
    expect(toolbar).toHaveTextContent("失败 1")
    expect(toolbar).toHaveTextContent("阻塞 0")
    expect(toolbar).toHaveTextContent("待执行 1")
    expect(toolbar).toHaveTextContent("已取代 0")
    expect(toolbar).toHaveTextContent("版本通道已连接")
    expect(screen.getByTestId("wave-list")).toBeInTheDocument()
    expect(screen.getByTestId("live-trace")).toBeInTheDocument()
    expect(screen.getByTestId("results-panel")).toBeInTheDocument()
  })

  it("surfaces a superseded confirmed case version instead of hiding the drop", async () => {
    render(<ExecutionTab versionId="2001" />)
    await screen.findByRole("heading", { name: "波次 #9201" })

    events.handlers?.onExecutionChanged?.({
      type: "execution.intent_superseded",
      payload: {
        reason: "case_version_superseded",
        replanned: true,
        replacement_case_version_id: "7004",
      },
    } as never)

    expect(
      await screen.findByText(/已确认用例版本被更新的已确认版本取代，已自动重新排入执行队列/),
    ).toBeInTheDocument()
  })

  it("shows the empty state before any case is confirmed", async () => {
    api.getOverview.mockResolvedValue(emptyOverviewFixture)
    api.listWaves.mockResolvedValue([])
    api.listCaseRuns.mockResolvedValue([])

    render(<ExecutionTab versionId="2001" />)

    expect(await screen.findByText("确认用例后自动入队组波")).toBeInTheDocument()
    expect(screen.getByText("暂无波次")).toBeInTheDocument()
  })

  it("shows a retryable error state when the snapshot fails", async () => {
    api.getOverview
      .mockRejectedValueOnce(
        new ApiError({
          status: 404,
          code: "not_found",
          message: "版本执行信息不存在",
          requestId: "req_exec",
        }),
      )
      .mockResolvedValue(executionOverviewFixture)

    render(<ExecutionTab versionId="2001" />)

    expect(await screen.findByText("版本执行信息不存在")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))

    expect(await screen.findByRole("heading", { name: "波次 #9201" })).toBeInTheDocument()
    expect(api.getOverview).toHaveBeenCalledTimes(2)
  })

  it("honours the ?wave= deep link without rewriting the URL on mount", async () => {
    render(<ExecutionTab versionId="2001" initialWaveId="9199" />)

    await waitFor(() => {
      expect(api.listCaseRuns).toHaveBeenCalledWith("2001", "9199")
    })
    expect(await screen.findByRole("heading", { name: "波次 #9199" })).toBeInTheDocument()
    expect(navigation.replace).not.toHaveBeenCalled()
  })

  it("honours the ?case_run= deep link and opens that case run detail", async () => {
    render(<ExecutionTab versionId="2001" initialWaveId="9201" initialCaseRunId="9302" />)

    await waitFor(() => {
      expect(api.getCaseRun).toHaveBeenCalledWith("2001", "9302")
    })
    const trace = await screen.findByTestId("live-trace")
    expect(within(trace).getByText("步骤 1")).toBeInTheDocument()
    expect(within(trace).getByText("打开订单详情页")).toBeInTheDocument()
    expect(navigation.replace).not.toHaveBeenCalled()
  })

  it("debounces SSE execution events into a single snapshot refetch", async () => {
    render(<ExecutionTab versionId="2001" />)
    await screen.findByRole("heading", { name: "波次 #9201" })
    expect(events.handlers).not.toBeNull()
    expect(api.getOverview).toHaveBeenCalledTimes(1)

    events.handlers?.onWaveChanged?.({ type: "wave.paused" })
    events.handlers?.onCaseRunChanged?.({ type: "case_run.finished" })
    events.handlers?.onEvidenceCreated?.({ type: "evidence.created" })

    await waitFor(() => expect(api.getOverview).toHaveBeenCalledTimes(2), { timeout: 3000 })
    expect(api.listWaves).toHaveBeenCalledTimes(2)
  })

  it("switches the selected wave and writes the deep link into the URL", async () => {
    render(<ExecutionTab versionId="2001" />)
    await screen.findByRole("heading", { name: "波次 #9201" })

    fireEvent.click(screen.getByRole("button", { name: /#9199/ }))

    await waitFor(() => {
      expect(api.listCaseRuns).toHaveBeenCalledWith("2001", "9199")
    })
    expect(navigation.replace).toHaveBeenCalledWith("/versions/2001?tab=execution&wave=9199")
  })

  it("writes ?case_run= when a result row is selected", async () => {
    render(<ExecutionTab versionId="2001" />)
    await screen.findByRole("heading", { name: "波次 #9201" })

    const failedRow = await screen.findByTestId("case-run-row-9302")
    fireEvent.click(within(failedRow).getByRole("button", { name: /跨系统退款金额一致/ }))

    await waitFor(() => {
      expect(api.getCaseRun).toHaveBeenCalledWith("2001", "9302")
    })
    expect(navigation.replace).toHaveBeenCalledWith(
      "/versions/2001?tab=execution&wave=9201&case_run=9302",
    )
  })

  it("shows the paused banner with mode/reason and resumes the wave", async () => {
    api.getOverview.mockResolvedValue({
      ...executionOverviewFixture,
      active_wave: pausedWaveFixture,
    })
    api.listWaves.mockResolvedValue([pausedWaveFixture, doneWaveFixture])

    render(<ExecutionTab versionId="2001" />)

    expect(await screen.findByText("执行已暂停")).toBeInTheDocument()
    expect(screen.getByText(/优雅暂停/)).toBeInTheDocument()
    expect(screen.getByText(/等待登录接管/)).toBeInTheDocument()

    fireEvent.click(screen.getAllByRole("button", { name: "恢复" })[0])

    await waitFor(() => {
      expect(api.resumeWave).toHaveBeenCalledWith("2001", "9202")
    })
  })

  it("renders the login-takeover banner for open human requests", async () => {
    api.listHumanRequests.mockResolvedValue([humanRequestFixture])

    render(<ExecutionTab versionId="2001" />)

    expect(await screen.findByText(/支付网关需要 2FA 验证码/)).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "完成" })).toBeInTheDocument()
  })

  it("keeps clarification questions out of the login-takeover banner and resume gate", async () => {
    api.getOverview.mockResolvedValue({
      ...executionOverviewFixture,
      active_wave: pausedWaveFixture,
    })
    api.listWaves.mockResolvedValue([pausedWaveFixture, doneWaveFixture])
    // Mixed pending rows: GET /human-requests returns every kind. The takeover
    // deadline is far in the future so the 完成 action stays enabled.
    api.listHumanRequests.mockResolvedValue([
      clarificationQuestionFixture,
      { ...humanRequestFixture, expires_at: "2099-01-01T00:00:00Z" },
    ])

    render(<ExecutionTab versionId="2001" />)

    // Only the takeover is actionable here; the clarification question is a
    // version todo and never renders as a takeover (R-B3-4).
    expect(await screen.findByText(/支付网关需要 2FA 验证码/)).toBeInTheDocument()
    expect(screen.queryByText(/请确认退款口径/)).not.toBeInTheDocument()

    // A pending clarification question must not block the wave resume ...
    fireEvent.click(screen.getAllByRole("button", { name: "恢复" })[0])
    expect(api.resumeWave).not.toHaveBeenCalled()
    // ... the pending takeover still does.
    expect(await screen.findByText("请先处理待处理的人工请求，再恢复波次。")).toBeInTheDocument()

    // 完成 resolves the takeover only; the question id never reaches the
    // kind-blind P3 resolve route.
    fireEvent.click(screen.getByRole("button", { name: "完成" }))
    await waitFor(() => expect(api.resolveHumanRequest).toHaveBeenCalledTimes(1))
    expect(api.resolveHumanRequest).toHaveBeenCalledWith("9501", {})
  })

  it("lets a wave resume when only a clarification question is pending", async () => {
    api.getOverview.mockResolvedValue({
      ...executionOverviewFixture,
      active_wave: pausedWaveFixture,
    })
    api.listWaves.mockResolvedValue([pausedWaveFixture, doneWaveFixture])
    api.listHumanRequests.mockResolvedValue([clarificationQuestionFixture])

    render(<ExecutionTab versionId="2001" />)

    expect(await screen.findByText("执行已暂停")).toBeInTheDocument()
    expect(screen.queryByTestId("human-request-banner")).not.toBeInTheDocument()

    fireEvent.click(screen.getAllByRole("button", { name: "恢复" })[0])
    await waitFor(() => expect(api.resumeWave).toHaveBeenCalledWith("2001", "9202"))
    expect(screen.queryByText("请先处理待处理的人工请求，再恢复波次。")).not.toBeInTheDocument()
  })

  it("warns when the executor is offline", async () => {
    api.getOverview.mockResolvedValue(offlineExecutorOverviewFixture)

    render(<ExecutionTab versionId="2001" />)

    expect(await screen.findByText(/等待执行器恢复/)).toBeInTheDocument()
  })

  it("opens the pause dialog for the wave and submits the pause", async () => {
    render(<ExecutionTab versionId="2001" />)
    await screen.findByRole("heading", { name: "波次 #9201" })

    fireEvent.click(
      within(screen.getByTestId("execution-toolbar")).getByRole("button", { name: "暂停" }),
    )
    expect(await screen.findByRole("dialog", { name: "暂停执行" })).toBeInTheDocument()
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "暂停" }))

    await waitFor(() => {
      expect(api.pauseWave).toHaveBeenCalledWith("2001", "9201", {
        mode: "graceful",
        reason: "",
      })
    })
    await waitFor(() => expect(api.getOverview).toHaveBeenCalledTimes(2))
  })

  it("shows non-zero running counts derived from real case-run fields", async () => {
    const wave = staleWave()
    api.getOverview.mockResolvedValue({
      ...executionOverviewFixture,
      active_wave: wave,
      counts: wave.counts,
    })
    api.listWaves.mockResolvedValue([wave, doneWaveFixture])
    api.listCaseRuns.mockResolvedValue(fiveCaseRuns())

    render(<ExecutionTab versionId="2001" />)

    // v0.22 audit §2.5-3: the backend projection stays all-zero while a wave is
    // running. The tab must fall back to the loaded case runs, not show 0/0.
    // The toolbar owns the derived counters; the compact strip only adds the
    // states the toolbar does not show.
    const counts = await screen.findByTestId("execution-counts")
    expect(counts).toHaveTextContent("未定 0")
    expect(counts).toHaveTextContent("证据 8")
    expect(counts).not.toHaveTextContent("共 5 条")
    expect(counts).not.toHaveTextContent("通过 2")

    const toolbar = screen.getByTestId("execution-toolbar")
    expect(toolbar).toHaveTextContent("共 5 条")
    expect(toolbar).toHaveTextContent("通过 2")
    expect(toolbar).toHaveTextContent("失败 1")
  })

  it("keeps the server evidence and by-SUT breakdown in the counts fallback", async () => {
    const wave = staleWave({
      counts: {
        ...zeroWaveCounts(),
        evidence: 7,
        by_version_sut: {
          "1099": { total: 0, passed: 0, failed: 0, blocked: 0, inconclusive: 0 },
        },
      },
    })
    api.getOverview.mockResolvedValue({
      ...executionOverviewFixture,
      active_wave: wave,
      counts: wave.counts,
    })
    api.listWaves.mockResolvedValue([wave])
    api.listCaseRuns.mockResolvedValue(fiveCaseRuns())

    render(<ExecutionTab versionId="2001" />)

    // The loaded page may be paginated; its derived aggregate must not erase a
    // server-provided evidence/by-SUT projection.
    const counts = await screen.findByTestId("execution-counts")
    expect(counts).toHaveTextContent("证据 7")

    const toolbar = screen.getByTestId("execution-toolbar")
    expect(toolbar).toHaveTextContent("通过 2")
    expect(within(toolbar).getByRole("option", { name: "SUT 1099" })).toBeInTheDocument()
  })

  it("drops the previous wave's derived counts while the next wave loads", async () => {
    const first = staleWave()
    const second = staleWave({ id: "9199", status: "running" })
    let resolveSecond: (caseRuns: CaseRun[]) => void = () => {}
    api.getOverview.mockResolvedValue({
      ...executionOverviewFixture,
      active_wave: first,
      counts: zeroWaveCounts(),
    })
    api.listWaves.mockResolvedValue([first, second])
    api.listCaseRuns.mockImplementation((_versionId: string, waveId: string) => {
      if (waveId === "9201") return Promise.resolve(fiveCaseRuns())
      return new Promise<CaseRun[]>((resolve) => {
        resolveSecond = resolve
      })
    })

    render(<ExecutionTab versionId="2001" />)
    await screen.findByTestId("execution-counts")

    fireEvent.click(screen.getByRole("button", { name: /#9199/ }))

    // The fallback belongs to the loaded wave: while the next wave's case runs
    // are in flight the previous wave's numbers must not linger, and the empty
    // state must not flash before the fetch settles.
    await waitFor(() => expect(screen.queryByTestId("execution-counts")).not.toBeInTheDocument())
    expect(screen.queryByTestId("execution-counts-empty")).not.toBeInTheDocument()
    expect(screen.getByTestId("execution-toolbar")).toHaveTextContent("共 0 条")

    await act(async () => {
      resolveSecond([])
    })

    expect(await screen.findByTestId("execution-counts-empty")).toBeInTheDocument()
  })

  it("keeps non-zero counts after the wave completes", async () => {
    const wave = staleWave({
      status: "done",
      finished_at: "2026-10-31T12:02:00Z",
    })
    api.getOverview.mockResolvedValue({
      ...executionOverviewFixture,
      active_wave: null,
      counts: zeroWaveCounts(),
    })
    api.listWaves.mockResolvedValue([wave])
    api.listCaseRuns.mockResolvedValue(fiveCaseRuns())

    render(<ExecutionTab versionId="2001" />)

    const counts = await screen.findByTestId("execution-counts")
    expect(counts).toHaveTextContent("未定 0")
    expect(counts).toHaveTextContent("证据 8")

    const toolbar = screen.getByTestId("execution-toolbar")
    expect(toolbar).toHaveTextContent("通过 2")
    expect(toolbar).toHaveTextContent("失败 1")
    expect(toolbar).toHaveTextContent("共 5 条")
  })

  it("renders an explicit empty state instead of a fake pass for zero data", async () => {
    const wave = staleWave()
    api.getOverview.mockResolvedValue({
      ...executionOverviewFixture,
      active_wave: wave,
      counts: wave.counts,
    })
    api.listWaves.mockResolvedValue([wave])
    api.listCaseRuns.mockResolvedValue([])

    render(<ExecutionTab versionId="2001" />)

    const empty = await screen.findByTestId("execution-counts-empty")
    expect(empty).toHaveTextContent("暂无执行数据")
    expect(screen.queryByTestId("execution-counts")).not.toBeInTheDocument()
  })

  it("opens the evidence deep link from ?evidence=", async () => {
    window.history.replaceState(
      {},
      "",
      "/versions/2001?tab=execution&wave=9201&case_run=9302&evidence=9402",
    )

    render(<ExecutionTab versionId="2001" />)

    // The deep link opens the immutable evidence dialog directly, even before a
    // user expands the owning case run.
    expect(await screen.findByRole("dialog", { name: "证据 #9402" })).toBeInTheDocument()
    expect(api.getEvidence).toHaveBeenCalledWith("2001", "9402")
    expect(await screen.findByTestId("evidence-text")).toHaveTextContent("PASS login as admin")
    // The deep link also focuses the case run that owns the evidence (9302)
    // once the wave's case runs are loaded.
    await waitFor(() => expect(api.getCaseRun).toHaveBeenCalledWith("2001", "9302"))
  })

  it("notices a missing evidence deep link without crashing", async () => {
    api.getEvidence.mockRejectedValue(
      new ApiError({
        status: 404,
        code: "not_found",
        message: "证据不存在",
        requestId: "req_evidence",
      }),
    )

    render(<ExecutionTab versionId="2001" initialEvidenceId="9999" />)

    const notice = await screen.findByTestId("evidence-deep-link-notice")
    expect(notice).toHaveTextContent("未找到证据 #9999")
    // The workspace itself keeps rendering behind the non-fatal notice.
    expect(await screen.findByRole("heading", { name: "波次 #9201" })).toBeInTheDocument()
  })

  it("shows a blocked case run with its blocker reason and next action", async () => {
    api.listCaseRuns.mockResolvedValue(fiveCaseRuns())

    render(<ExecutionTab versionId="2001" />)

    const blocked = await screen.findByTestId("blocked-case-run-9303")
    expect(blocked).toHaveTextContent("缺少管理员账号")
    expect(blocked).toHaveTextContent("先解决阻塞条件")
  })

  it("keeps the login takeover as the single blocked surface for its case run", async () => {
    // The blocked case 9303 already owns an open login-takeover request; the
    // takeover banner is the actionable surface and the generic blocked banner
    // must not stack on top of it.
    api.listHumanRequests.mockResolvedValue([
      { ...humanRequestFixture, expires_at: "2099-01-01T00:00:00Z" },
    ])

    render(<ExecutionTab versionId="2001" />)

    expect(await screen.findByTestId("human-request-banner")).toBeInTheDocument()
    expect(screen.queryByTestId("blocked-case-run-9303")).not.toBeInTheDocument()
  })

  it("does not stack a case_run.blocked notice on a login takeover for its own case run", async () => {
    api.listHumanRequests.mockResolvedValue([
      { ...humanRequestFixture, expires_at: "2099-01-01T00:00:00Z" },
    ])
    // Keep the blocked snapshot row out of the loaded wave: only the per-run
    // takeover guard may suppress the event, not the snapshot banner.
    api.listCaseRuns.mockResolvedValue([
      passedCaseRunFixture,
      failedCaseRunFixture,
      runningCaseRunFixture,
    ])

    render(<ExecutionTab versionId="2001" />)
    await screen.findByTestId("human-request-banner")

    await act(async () => {
      events.handlers?.onCaseRunChanged?.({
        type: "case_run.blocked",
        payload: {
          case_run_id: "9303",
          reason: "no_executor",
          action: "等待执行器注册后重试",
        },
      } as never)
    })

    // The takeover and the event share case run 9303: the takeover banner is
    // the single actionable surface for it.
    expect(screen.queryByTestId("case-run-blocked-event-notice")).not.toBeInTheDocument()
  })

  it("still shows a case_run.blocked notice for another case run during a takeover", async () => {
    api.listHumanRequests.mockResolvedValue([
      { ...humanRequestFixture, expires_at: "2099-01-01T00:00:00Z" },
    ])

    render(<ExecutionTab versionId="2001" />)
    await screen.findByTestId("human-request-banner")

    await act(async () => {
      events.handlers?.onCaseRunChanged?.({
        type: "case_run.blocked",
        payload: {
          case_run_id: "9399",
          reason: "no_executor",
          action: "等待执行器注册后重试",
        },
      } as never)
    })

    // The pending takeover owns 9303 only; an unrelated run's dispatch-health
    // warning must not be swallowed by the global takeover state.
    const notice = await screen.findByTestId("case-run-blocked-event-notice")
    expect(notice).toHaveTextContent("用例 #9399")
    expect(notice).toHaveTextContent("没有可用的执行器")
    expect(notice).toHaveTextContent("等待执行器注册后重试")
  })

  it("renders a generic reason when a blocked event carries no reason", async () => {
    render(<ExecutionTab versionId="2001" />)
    await screen.findByRole("heading", { name: "波次 #9201" })

    await act(async () => {
      events.handlers?.onCaseRunChanged?.({
        type: "case_run.blocked",
        payload: { case_run_id: "9399", reason: "", action: "" },
      } as never)
    })

    const notice = await screen.findByTestId("case-run-blocked-event-notice")
    expect(notice).toHaveTextContent("阻塞原因未知")
  })

  it("clears a stale blocked notice once its case run recovers", async () => {
    render(<ExecutionTab versionId="2001" />)
    await screen.findByRole("heading", { name: "波次 #9201" })

    await act(async () => {
      events.handlers?.onCaseRunChanged?.({
        type: "case_run.blocked",
        payload: { case_run_id: "9399", reason: "no_executor", action: "" },
      } as never)
    })
    expect(await screen.findByTestId("case-run-blocked-event-notice")).toBeInTheDocument()

    // The next snapshot shows the run started and passed; the dispatch warning
    // is no longer current and must not linger.
    api.listCaseRuns.mockResolvedValue([
      ...caseRunsFixture,
      { ...passedCaseRunFixture, id: "9399", title: "恢复后的用例" },
    ])
    await act(async () => {
      events.handlers?.onWaveChanged?.({ type: "wave.resumed" } as never)
    })

    await waitFor(
      () => expect(screen.queryByTestId("case-run-blocked-event-notice")).not.toBeInTheDocument(),
      { timeout: 3000 },
    )
  })

  it("consumes case_run.blocked events with reason and action", async () => {
    render(<ExecutionTab versionId="2001" />)
    await screen.findByRole("heading", { name: "波次 #9201" })

    events.handlers?.onCaseRunChanged?.({
      type: "case_run.blocked",
      payload: {
        case_run_id: "9399",
        reason: "no_executor",
        action: "等待执行器注册后重试",
      },
    } as never)

    const notice = await screen.findByTestId("case-run-blocked-event-notice")
    expect(notice).toHaveTextContent("没有可用的执行器")
    expect(notice).toHaveTextContent("等待执行器注册后重试")
  })

  it("shows the execution TTL and quota settings without crashing", async () => {
    render(<ExecutionTab versionId="2001" />)

    const settings = await screen.findByTestId("execution-settings")
    expect(settings).toHaveTextContent("人工请求 TTL 30 分钟")
    expect(settings).toHaveTextContent("全局 3")
    expect(settings).toHaveTextContent("每 SUT 2")
    expect(settings).toHaveTextContent("用例超时")
  })

  it("renders when the execution settings are missing", async () => {
    const overview: Record<string, unknown> = { ...executionOverviewFixture }
    delete overview.human_request_timeout_seconds
    delete overview.quotas
    api.getOverview.mockResolvedValue(overview as unknown as ExecutionOverview)

    render(<ExecutionTab versionId="2001" />)

    expect(await screen.findByTestId("execution-settings")).toHaveTextContent("不可用")
    expect(await screen.findByRole("heading", { name: "波次 #9201" })).toBeInTheDocument()
  })

  it("treats a null quota projection as unavailable instead of crashing", async () => {
    api.getOverview.mockResolvedValue({
      ...executionOverviewFixture,
      quotas: null,
    } as unknown as ExecutionOverview)

    render(<ExecutionTab versionId="2001" />)

    expect(await screen.findByTestId("execution-settings")).toHaveTextContent("并发配额 不可用")
    expect(await screen.findByRole("heading", { name: "波次 #9201" })).toBeInTheDocument()
  })
})
