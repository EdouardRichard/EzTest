import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import type { VersionEvent } from "@/features/clarification/types"
import type { RunWave } from "@/features/execution/types"
import { versionFixture } from "@/features/versions/test-fixtures"
import { ApiError } from "@/lib/api-client"

import type { ChainEdgeView, ChainNodeView, ChainView } from "./types"

const api = vi.hoisted(() => ({
  getChain: vi.fn(),
  listChainWaves: vi.fn(),
  listChainEvents: vi.fn(),
}))
vi.mock("./api", () => api)

const versionsApi = vi.hoisted(() => ({ getVersion: vi.fn() }))
vi.mock("@/features/versions/api", () => versionsApi)

const sse = vi.hoisted(() => ({
  handlers: null as Record<string, (event: unknown) => void> | null,
  status: "live",
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
      return sse.status
    },
  }
})

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

import { ChainTab } from "./ChainTab"

/* --------------------------------- fixtures -------------------------------- */

const created = "2026-10-31T10:00:00Z"

const nodes: ChainNodeView[] = [
  {
    id: "requirement:6001",
    kind: "requirement",
    ref_id: "6001",
    label: "退款到账时效",
    status: "closed",
    created_at: created,
    meta: { closure: "closed", case_count: 1 },
  },
  {
    id: "scenario:5001",
    kind: "scenario",
    ref_id: "5001",
    label: "正常退款",
    status: "active",
    requirement_id: "6001",
    created_at: created,
    meta: {},
  },
  {
    id: "case_version:7001",
    kind: "case_version",
    ref_id: "7001",
    label: "退款 T+1 到账",
    status: "confirmed",
    version_sut_id: "3001",
    requirement_id: "6001",
    created_at: created,
    meta: { result: "failed", revision: 2 },
  },
  {
    id: "wave:9201",
    kind: "wave",
    ref_id: "9201",
    label: "波次 #9201",
    status: "done",
    version_sut_id: "3001",
    created_at: created,
    meta: { total: 3, passed: 2, failed: 1 },
  },
  {
    id: "case_run:9301",
    kind: "case_run",
    ref_id: "9301",
    label: "退款 T+1 到账 · run",
    status: "failed",
    version_sut_id: "3001",
    wave_id: "9201",
    case_version_id: "7001",
    created_at: created,
    meta: {
      result: "failed",
      failure_class: "application",
      evidence_count: 2,
      step_count: 4,
      assertion_count: 3,
    },
  },
  {
    id: "issue:9101",
    kind: "issue",
    ref_id: "9101",
    label: "退款金额与订单金额不一致",
    status: "open",
    version_sut_id: "3001",
    case_run_id: "9301",
    issue_id: "9101",
    created_at: created,
    meta: {
      severity: "major",
      issue_status: "open",
      category: "api",
      evidence_count: 2,
      code_ref_count: 1,
      merged_into_id: null,
    },
  },
  {
    id: "report:8101",
    kind: "report",
    ref_id: "8101",
    label: "波次报告 #9201",
    status: "published",
    wave_id: "9201",
    report_id: "8101",
    created_at: created,
    meta: { kind: "wave", revision: 1, as_of: created },
  },
]

const edges: ChainEdgeView[] = [
  { from: "requirement:6001", to: "scenario:5001", kind: "has_scenario" },
  { from: "scenario:5001", to: "case_version:7001", kind: "has_case" },
  { from: "case_version:7001", to: "wave:9201", kind: "executed_by" },
  { from: "wave:9201", to: "case_run:9301", kind: "contains" },
  { from: "case_run:9301", to: "issue:9101", kind: "raised" },
  { from: "wave:9201", to: "report:8101", kind: "reports" },
]

function chainFixture(overrides: Partial<ChainView> = {}): ChainView {
  return {
    version_id: "2001",
    generated_at: "2026-11-01T06:00:00Z",
    snapshot_cursor: 42,
    filters: {
      sut_id: null,
      page_id: null,
      wave_id: null,
      status: null,
      result: null,
      event_from: null,
      event_to: null,
      q: null,
      abnormal_only: null,
      issue_status: null,
      issue_severity: null,
      requirement_limit: null,
    },
    counts: {
      requirements: 1,
      scenarios: 1,
      cases: 1,
      waves: 1,
      case_runs: 1,
      issues: 1,
      reports: 1,
      evidence: 2,
      by_case_run_status: {
        passed: 1,
        failed: 1,
        blocked: 0,
        inconclusive: 0,
        skipped: 0,
        interrupted: 0,
      },
      by_issue_severity: { major: 1, minor: 0, trivial: 0 },
      by_issue_status: { open: 1, confirmed: 0, dismissed: 0, merged: 0 },
      coverage: { requirements: 1, covered: 1, uncovered: 0 },
    },
    nodes,
    edges,
    truncated: false,
    truncation: { max_nodes: 300, max_edges: 600, max_requirements: 50, reason: "" },
    replay: {
      mode: "live",
      wave_id: null,
      event_from: null,
      event_to: null,
      approximate: false,
      event_limit: 20,
      truncated: false,
    },
    degraded_layers: [],
    truncated_layers: {
      requirements: false,
      cases: false,
      case_runs: false,
      issues: false,
      reports: false,
    },
    ...overrides,
  }
}

const waves: RunWave[] = [
  {
    id: "9202",
    version_id: "2001",
    status: "running",
    trigger: "auto",
    counts: {
      total: 3,
      pending: 1,
      running: 1,
      paused: 0,
      passed: 1,
      failed: 0,
      blocked: 0,
      inconclusive: 0,
      skipped: 0,
      interrupted: 0,
      superseded: 0,
      evidence: 1,
    },
    started_at: "2026-11-01T05:00:00Z",
    finished_at: null,
    paused_at: null,
    pause_mode: "",
    pause_reason: "",
    created_at: "2026-11-01T05:00:00Z",
  },
  {
    id: "9201",
    version_id: "2001",
    status: "done",
    trigger: "auto",
    counts: {
      total: 3,
      pending: 0,
      running: 0,
      paused: 0,
      passed: 2,
      failed: 1,
      blocked: 0,
      inconclusive: 0,
      skipped: 0,
      interrupted: 0,
      superseded: 0,
      evidence: 2,
    },
    started_at: "2026-10-31T10:00:00Z",
    finished_at: "2026-10-31T11:00:00Z",
    paused_at: null,
    pause_mode: "",
    pause_reason: "",
    created_at: created,
  },
]

const events: VersionEvent[] = [
  {
    seq: 40,
    channel: "version:2001",
    type: "wave.finished",
    scope: { version_id: "2001" },
    payload: { wave_id: "9201" },
    actor_id: null,
    request_id: "req_evt",
    created_at: "2026-10-31T11:00:00Z",
  },
  {
    seq: 41,
    channel: "version:2001",
    type: "issue.created",
    scope: { version_id: "2001" },
    payload: { issue_id: "9101" },
    actor_id: null,
    request_id: "req_evt",
    created_at: "2026-10-31T11:01:00Z",
  },
]

function mockWorkspace(): void {
  api.getChain.mockImplementation((_versionId: string, options: Record<string, unknown> = {}) =>
    Promise.resolve(
      chainFixture(
        options.waveId === undefined || options.waveId === null
          ? {}
          : {
              replay: {
                mode: "wave",
                wave_id: String(options.waveId),
                event_from: 12,
                event_to: 40,
                approximate: true,
              },
            },
      ),
    ),
  )
  api.listChainWaves.mockResolvedValue(waves)
  api.listChainEvents.mockResolvedValue(events)
  versionsApi.getVersion.mockResolvedValue(versionFixture)
  sse.handlers = null
  sse.calls = 0
}

async function renderChain(): Promise<void> {
  render(<ChainTab versionId="2001" />)
  await screen.findByTestId("chain-graph")
}

function sheet(): HTMLElement {
  return screen.getByRole("dialog", { name: "链路节点详情" })
}

describe("ChainTab", () => {
  beforeEach(() => {
    for (const fn of Object.values(api)) fn.mockReset()
    versionsApi.getVersion.mockReset()
    navigation.replace.mockReset()
    search.params = new URLSearchParams()
    mockWorkspace()
  })

  afterEach(() => {
    cleanup()
    vi.useRealTimers()
  })

  it("shows a loading state while the chain loads", () => {
    api.getChain.mockReturnValue(new Promise<never>(() => {}))

    render(<ChainTab versionId="2001" />)

    expect(screen.getByText("正在加载探索链路…")).toBeInTheDocument()
  })

  it("renders nodes, edges, legend and filtered counts", async () => {
    await renderChain()

    expect(api.getChain).toHaveBeenCalledWith("2001", expect.objectContaining({ limit: 300 }))
    expect(await screen.findByTestId("chain-node-requirement:6001")).toHaveTextContent(
      "退款到账时效",
    )
    expect(screen.getByTestId("chain-node-issue:9101")).toHaveTextContent(
      "退款金额与订单金额不一致",
    )

    const graph = screen.getByTestId("chain-graph")
    expect(graph).toHaveAttribute("data-node-count", "7")
    expect(graph).toHaveAttribute("data-edge-count", "6")

    const legend = screen.getByTestId("chain-legend")
    expect(within(legend).getByText("需求")).toBeInTheDocument()
    expect(within(legend).getByText("问题")).toBeInTheDocument()
    expect(within(legend).getByText("报告")).toBeInTheDocument()

    const stats = screen.getByTestId("chain-stats")
    expect(stats).toHaveTextContent("需求")
    expect(stats).toHaveTextContent("用例")
    expect(stats).toHaveTextContent("波次")
    expect(stats).toHaveTextContent("证据")
    expect(stats).toHaveTextContent("问题")
    expect(stats).toHaveTextContent("覆盖")
  })

  it("passes SUT, page, wave, status and result filters to the chain API", async () => {
    await renderChain()

    fireEvent.change(screen.getByLabelText("SUT"), { target: { value: "3001" } })
    await waitFor(() =>
      expect(api.getChain).toHaveBeenLastCalledWith(
        "2001",
        expect.objectContaining({ sutId: "3001" }),
      ),
    )

    fireEvent.change(screen.getByLabelText("页面"), { target: { value: "1301" } })
    await waitFor(() =>
      expect(api.getChain).toHaveBeenLastCalledWith(
        "2001",
        expect.objectContaining({ sutId: "3001", pageId: "1301" }),
      ),
    )

    fireEvent.change(screen.getByLabelText("状态"), { target: { value: "failed" } })
    await waitFor(() =>
      expect(api.getChain).toHaveBeenLastCalledWith(
        "2001",
        expect.objectContaining({ status: "failed" }),
      ),
    )

    fireEvent.change(screen.getByLabelText("结果"), { target: { value: "failed" } })
    await waitFor(() =>
      expect(api.getChain).toHaveBeenLastCalledWith(
        "2001",
        expect.objectContaining({ result: "failed" }),
      ),
    )

    fireEvent.change(screen.getByLabelText("波次"), { target: { value: "9201" } })
    await waitFor(() =>
      expect(api.getChain).toHaveBeenLastCalledWith(
        "2001",
        expect.objectContaining({ waveId: "9201" }),
      ),
    )
  })

  it("shows the empty state when the chain has no nodes", async () => {
    api.getChain.mockResolvedValue(
      chainFixture({
        nodes: [],
        edges: [],
        counts: {
          requirements: 0,
          scenarios: 0,
          cases: 0,
          waves: 0,
          case_runs: 0,
          issues: 0,
          reports: 0,
          evidence: 0,
        },
      }),
    )

    render(<ChainTab versionId="2001" />)

    expect(await screen.findByText("暂无探索链路")).toBeInTheDocument()
  })

  it("shows a retryable error state when the chain fails", async () => {
    api.getChain
      .mockRejectedValueOnce(
        new ApiError({
          status: 500,
          code: "internal",
          message: "链路暂不可用",
          requestId: "req_chain",
        }),
      )
      .mockResolvedValueOnce(chainFixture())

    render(<ChainTab versionId="2001" />)

    expect(await screen.findByText("探索链路加载失败")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(await screen.findByTestId("chain-graph")).toBeInTheDocument()
  })

  it("shows a truncation alert with the budget reason and limits", async () => {
    api.getChain.mockResolvedValue(
      chainFixture({
        truncated: true,
        truncation: {
          max_nodes: 120,
          max_edges: 240,
          max_requirements: 20,
          reason: "node_budget",
        },
      }),
    )

    await renderChain()

    const alert = screen.getByRole("alert")
    expect(alert).toHaveTextContent("链路已按预算截断")
    expect(alert).toHaveTextContent("节点预算")
    expect(alert).toHaveTextContent("120")
    expect(within(alert).getByRole("status")).toHaveTextContent("节点预算")
  })

  it("selects a node, opens the detail sheet and writes ?node= into the URL", async () => {
    await renderChain()

    fireEvent.click(await screen.findByTestId("chain-node-issue:9101"))

    const detail = sheet()
    expect(within(detail).getByText(/^问题/)).toBeInTheDocument()
    expect(within(detail).getByText("退款金额与订单金额不一致")).toBeInTheDocument()
    expect(within(detail).getByRole("link", { name: "打开问题 #9101" })).toHaveAttribute(
      "href",
      "/versions/2001?tab=issues&issue=9101",
    )
    expect(navigation.replace).toHaveBeenCalledWith("/versions/2001?tab=chain&node=issue%3A9101")
  })

  it("restores a ?node= deep link, keeps focus_node on the request and does not rewrite the URL", async () => {
    search.params = new URLSearchParams({ tab: "chain", node: "issue:9101" })

    render(<ChainTab versionId="2001" />)

    expect(await screen.findByRole("dialog", { name: "链路节点详情" })).toBeInTheDocument()
    expect(api.getChain).toHaveBeenCalledWith(
      "2001",
      expect.objectContaining({ focusNode: "issue:9101" }),
    )
    expect(navigation.replace).not.toHaveBeenCalled()
  })

  it("maps ?issue= and ?report= deep links onto the matching chain node", async () => {
    search.params = new URLSearchParams({ tab: "chain", issue: "9101" })

    render(<ChainTab versionId="2001" />)

    await waitFor(() =>
      expect(api.getChain).toHaveBeenCalledWith(
        "2001",
        expect.objectContaining({ focusNode: "issue:9101" }),
      ),
    )
    const detail = await screen.findByRole("dialog", { name: "链路节点详情" })
    expect(within(detail).getByRole("link", { name: "打开问题 #9101" })).toBeInTheDocument()
  })

  it("shows report and wave deep links in the detail sheet", async () => {
    search.params = new URLSearchParams({ tab: "chain", node: "report:8101" })

    render(<ChainTab versionId="2001" />)

    const detail = await screen.findByRole("dialog", { name: "链路节点详情" })
    expect(within(detail).getByRole("link", { name: "打开报告 #8101" })).toHaveAttribute(
      "href",
      "/versions/2001?tab=reports&report=8101",
    )
    expect(within(detail).getByRole("link", { name: "打开波次 #9201" })).toHaveAttribute(
      "href",
      "/versions/2001?tab=execution&wave=9201",
    )
  })

  it("refetches on SSE events while live follow is enabled and pauses when it is off", async () => {
    await renderChain()
    await waitFor(() => expect(api.getChain).toHaveBeenCalledTimes(1))

    const follow = screen.getByRole("button", { name: "实时跟随" })
    expect(follow).toHaveAttribute("aria-pressed", "true")

    act(() => {
      sse.handlers?.onWaveChanged?.({ type: "wave.finished" })
    })
    await waitFor(() => expect(api.getChain).toHaveBeenCalledTimes(2), { timeout: 2000 })

    fireEvent.click(follow)
    expect(screen.getByRole("button", { name: "实时跟随" })).toHaveAttribute(
      "aria-pressed",
      "false",
    )

    act(() => {
      sse.handlers?.onWaveChanged?.({ type: "wave.finished" })
    })
    await new Promise((resolve) => setTimeout(resolve, 450))
    expect(api.getChain).toHaveBeenCalledTimes(2)
  })

  it("subscribes once and refetches on P4 issue/report/requirement/version events", async () => {
    await renderChain()
    await waitFor(() => expect(api.getChain).toHaveBeenCalledTimes(1))
    expect(sse.calls).toBe(1)

    for (const handler of [
      "onIssueChanged",
      "onReportChanged",
      "onRequirementChanged",
      "onVersionChanged",
    ] as const) {
      expect(typeof sse.handlers?.[handler]).toBe("function")
    }

    act(() => {
      sse.handlers?.onVersionChanged?.({ type: "version.reopened" })
    })
    await waitFor(() => expect(api.getChain).toHaveBeenCalledTimes(2), { timeout: 2000 })

    act(() => {
      sse.handlers?.onReportChanged?.({ type: "report.stale" })
    })
    await waitFor(() => expect(api.getChain).toHaveBeenCalledTimes(3), { timeout: 2000 })
  })

  it("notifies the host when the version projection changes", async () => {
    const onVersionChanged = vi.fn()
    render(<ChainTab versionId="2001" onVersionChanged={onVersionChanged} />)
    await screen.findByTestId("chain-graph")

    act(() => {
      sse.handlers?.onVersionChanged?.({ type: "version.closed" })
    })

    expect(onVersionChanged).toHaveBeenCalledTimes(1)
    await waitFor(() => expect(api.getChain).toHaveBeenCalledTimes(2), { timeout: 2000 })
  })

  it("shows an approximate history replay label when a wave is selected", async () => {
    await renderChain()

    fireEvent.change(screen.getByLabelText("历史波次"), { target: { value: "9201" } })

    await waitFor(() =>
      expect(api.getChain).toHaveBeenLastCalledWith(
        "2001",
        expect.objectContaining({ waveId: "9201" }),
      ),
    )
    expect(await screen.findByTestId("chain-replay-badge")).toHaveTextContent("近似")
    expect(screen.getAllByText(/近似/).length).toBeGreaterThan(0)
    expect(screen.queryByText(/as-of/)).not.toBeInTheDocument()
  })

  it("renders the version event timeline", async () => {
    await renderChain()

    const timeline = await screen.findByTestId("chain-event-timeline")
    expect(timeline).toHaveTextContent("wave.finished")
    expect(timeline).toHaveTextContent("issue.created")
    expect(api.listChainEvents).toHaveBeenCalledWith("2001", expect.objectContaining({ limit: 20 }))
  })

  it("debounces q and maps P6 filters to the API and URL", async () => {
    await renderChain()

    fireEvent.change(screen.getByLabelText("搜索"), { target: { value: "退款" } })
    await waitFor(
      () =>
        expect(api.getChain).toHaveBeenLastCalledWith(
          "2001",
          expect.objectContaining({ q: "退款" }),
        ),
      { timeout: 2000 },
    )
    expect(navigation.replace).toHaveBeenLastCalledWith(
      expect.stringContaining("q=%E9%80%80%E6%AC%BE"),
    )

    fireEvent.click(screen.getByTestId("chain-filter-abnormal"))
    fireEvent.change(screen.getByLabelText("问题状态"), { target: { value: "confirmed" } })
    fireEvent.change(screen.getByLabelText("严重级"), { target: { value: "major" } })
    fireEvent.change(screen.getByLabelText("需求上限"), { target: { value: "100" } })

    await waitFor(() =>
      expect(api.getChain).toHaveBeenLastCalledWith(
        "2001",
        expect.objectContaining({
          q: "退款",
          abnormalOnly: true,
          issueStatus: "confirmed",
          issueSeverity: "major",
          requirementLimit: 100,
        }),
      ),
    )
    const lastUrl = navigation.replace.mock.calls.at(-1)?.[0] as string
    expect(lastUrl).toContain("abnormal_only=true")
    expect(lastUrl).toContain("issue_status=confirmed")
    expect(lastUrl).toContain("issue_severity=major")
    expect(lastUrl).toContain("requirement_limit=100")
  })

  it("restores P6 filters from the URL deep link without rewriting it", async () => {
    search.params = new URLSearchParams({
      tab: "chain",
      q: "退款",
      abnormal_only: "true",
      issue_status: "open",
      issue_severity: "major",
      requirement_limit: "100",
    })

    render(<ChainTab versionId="2001" />)

    await waitFor(() =>
      expect(api.getChain).toHaveBeenCalledWith(
        "2001",
        expect.objectContaining({
          q: "退款",
          abnormalOnly: true,
          issueStatus: "open",
          issueSeverity: "major",
          requirementLimit: 100,
        }),
      ),
    )
    expect(navigation.replace).not.toHaveBeenCalled()
  })

  it("renders typed counts, coverage, degraded layers and per-layer truncation", async () => {
    api.getChain.mockResolvedValue(
      chainFixture({
        counts: {
          requirements: 3,
          scenarios: 2,
          cases: 2,
          waves: 1,
          case_runs: 2,
          issues: 1,
          reports: 1,
          evidence: 2,
          by_case_run_status: {
            passed: 2,
            failed: 1,
            blocked: 1,
            inconclusive: 0,
            skipped: 0,
            interrupted: 1,
          },
          by_issue_severity: { major: 1, minor: 0, trivial: 0 },
          by_issue_status: { open: 1, confirmed: 0, dismissed: 0, merged: 0 },
          coverage: { requirements: 3, covered: 2, uncovered: 1 },
        },
        degraded_layers: ["design", "delivery"],
        truncated_layers: {
          requirements: true,
          cases: false,
          case_runs: false,
          issues: true,
          reports: false,
        },
        replay: {
          mode: "range",
          wave_id: null,
          event_from: 10,
          event_to: 30,
          approximate: true,
          event_limit: 5,
          truncated: true,
        },
      }),
    )

    await renderChain()

    const stats = screen.getByTestId("chain-stats")
    expect(stats).toHaveTextContent("覆盖 2/3")
    expect(screen.getByTestId("chain-run-status-counts")).toHaveTextContent("通过 2")
    expect(screen.getByTestId("chain-run-status-counts")).toHaveTextContent("中断 1")
    expect(screen.getByTestId("chain-coverage-counts")).toHaveTextContent("未覆盖 1")
    expect(screen.getByTestId("chain-issue-status-counts")).toHaveTextContent("待确认 1")
    expect(screen.getByTestId("chain-issue-severity-counts")).toHaveTextContent("严重 1")

    const degraded = screen.getByTestId("chain-degraded-banner")
    expect(degraded).toHaveTextContent("设计")
    expect(degraded).toHaveTextContent("交付")
    expect(within(degraded).getByTestId("chain-reconnect")).toBeInTheDocument()

    const layerTruncation = screen.getByTestId("chain-layer-truncation-banner")
    expect(layerTruncation).toHaveTextContent("需求")
    expect(layerTruncation).toHaveTextContent("问题")

    expect(screen.getByTestId("chain-replay-truncated")).toHaveTextContent("event_limit")
    expect(api.listChainEvents).toHaveBeenCalledWith("2001", { limit: 5 })
  })

  it("shows P6 typed meta in the node list and detail sheet", async () => {
    await renderChain()

    fireEvent.click(screen.getByRole("button", { name: "列表视图" }))
    expect(screen.getByRole("button", { name: "列表视图" })).toHaveAttribute("aria-pressed", "true")
    const list = screen.getByTestId("chain-list-view")
    expect(within(list).getByText("退款到账时效")).toBeInTheDocument()

    fireEvent.click(screen.getByTestId("chain-list-node-issue:9101"))
    const detail = sheet()
    const meta = within(detail).getByTestId("chain-detail-meta")
    expect(meta).toHaveTextContent("severity")
    expect(meta).toHaveTextContent("major")
    expect(meta).toHaveTextContent("issue_status")
    expect(meta).toHaveTextContent("open")
    expect(meta).toHaveTextContent("category")
    expect(meta).toHaveTextContent("api")
  })

  it("keeps the last graph on a refresh error and reconnects", async () => {
    await renderChain()
    await waitFor(() => expect(api.getChain).toHaveBeenCalledTimes(1))

    api.getChain.mockRejectedValueOnce(
      new ApiError({ status: 503, code: "provider_unavailable", message: "down", requestId: "r" }),
    )
    act(() => {
      sse.handlers?.onWaveChanged?.({ type: "wave.finished" })
    })

    expect(await screen.findByTestId("chain-refresh-error")).toBeInTheDocument()
    expect(screen.getByTestId("chain-graph")).toBeInTheDocument()
    expect(screen.queryByText("暂无探索链路")).not.toBeInTheDocument()

    fireEvent.click(screen.getByTestId("chain-refresh-reconnect"))
    await waitFor(() => expect(api.getChain).toHaveBeenCalledTimes(3), { timeout: 2000 })
  })
})
