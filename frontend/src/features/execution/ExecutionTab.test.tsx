import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

const api = vi.hoisted(() => ({
  getOverview: vi.fn(),
  listWaves: vi.fn(),
  getWave: vi.fn(),
  listCaseRuns: vi.fn(),
  getCaseRun: vi.fn(),
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
  caseRunDetailFixture,
  caseRunsFixture,
  doneWaveFixture,
  emptyOverviewFixture,
  executionOverviewFixture,
  humanRequestFixture,
  offlineExecutorOverviewFixture,
  pausedWaveFixture,
  runningWaveFixture,
} from "./test-fixtures"

function mockWorkspace() {
  api.getOverview.mockResolvedValue(executionOverviewFixture)
  api.listWaves.mockResolvedValue([runningWaveFixture, doneWaveFixture])
  api.listCaseRuns.mockResolvedValue(caseRunsFixture)
  api.getCaseRun.mockResolvedValue(caseRunDetailFixture)
  api.listHumanRequests.mockResolvedValue([])
  api.pauseWave.mockResolvedValue(pausedWaveFixture)
  api.resumeWave.mockResolvedValue(runningWaveFixture)
  api.cancelWave.mockResolvedValue({ ...runningWaveFixture, status: "cancelled" })
  api.pauseCaseRun.mockResolvedValue({ ...caseRunsFixture[3], status: "paused" })
  api.resumeCaseRun.mockResolvedValue(caseRunsFixture[3])
  api.resolveHumanRequest.mockResolvedValue({ ...humanRequestFixture, status: "resolved" })
  api.cancelHumanRequest.mockResolvedValue({ ...humanRequestFixture, status: "cancelled" })
}

describe("ExecutionTab", () => {
  beforeEach(() => {
    for (const fn of Object.values(api)) fn.mockReset()
    navigation.replace.mockReset()
    events.handlers = null
    mockWorkspace()
  })

  afterEach(cleanup)

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
    expect(api.listHumanRequests).toHaveBeenCalledWith("2001", { status: "open" })
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
    expect(toolbar).toHaveTextContent("版本通道已连接")
    expect(screen.getByTestId("wave-list")).toBeInTheDocument()
    expect(screen.getByTestId("live-trace")).toBeInTheDocument()
    expect(screen.getByTestId("results-panel")).toBeInTheDocument()
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
})
