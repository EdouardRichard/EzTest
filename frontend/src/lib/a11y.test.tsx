import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import axe from "axe-core"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { Version } from "@/features/versions/types"

const mocks = vi.hoisted(() => ({
  useAuth: vi.fn(),
  replace: vi.fn(),
  push: vi.fn(),
  setTheme: vi.fn(),
  listVersions: vi.fn(),
  getVersion: vi.fn(),
  fetchVersionEvents: vi.fn(),
  useVersionStream: vi.fn(),
  useBatches: vi.fn(),
  useBatch: vi.fn(),
  useCaseRuns: vi.fn(),
  useCaseRun: vi.fn(),
  useReport: vi.fn(),
  useExecution: vi.fn(),
  useExecutionActions: vi.fn(),
}))

vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: mocks.replace, push: mocks.push }),
  usePathname: () => "/",
  useSearchParams: () => new URLSearchParams(),
}))

vi.mock("next-themes", () => ({
  useTheme: () => ({ theme: "system", setTheme: mocks.setTheme }),
}))

vi.mock("@/features/auth/AuthProvider", () => ({ useAuth: mocks.useAuth }))

// The version APIs are stubbed: axe only inspects the rendered structure, so no
// request should ever leave the test.
vi.mock("@/features/versions/api", () => ({
  getVersion: mocks.getVersion,
  listVersions: mocks.listVersions,
  fetchVersionEvents: mocks.fetchVersionEvents,
  startClarification: vi.fn(),
  enterDesign: vi.fn(),
  createRequirement: vi.fn(),
  getIncrementalPlan: vi.fn(),
  listRequirements: vi.fn(),
  listRelations: vi.fn(),
  getClarification: vi.fn(),
  listClarificationMessages: vi.fn(),
  sendClarificationMessage: vi.fn(),
  startClarificationSession: vi.fn(),
  analyzeClarification: vi.fn(),
  reconClarification: vi.fn(),
  listClarificationQuestions: vi.fn(),
  createClarificationQuestion: vi.fn(),
  updateClarificationQuestion: vi.fn(),
  listDecisions: vi.fn(),
  createDecision: vi.fn(),
  updateDecision: vi.fn(),
}))

vi.mock("@/features/versions/useVersionStream", () => ({
  useVersionStream: mocks.useVersionStream,
}))

vi.mock("@/features/execution/hooks/useExecution", () => ({
  useBatches: mocks.useBatches,
  useBatch: mocks.useBatch,
  useCaseRuns: mocks.useCaseRuns,
  useCaseRun: mocks.useCaseRun,
  useReport: mocks.useReport,
  useExecution: mocks.useExecution,
  useExecutionActions: mocks.useExecutionActions,
}))

vi.mock("@/features/delivery/hooks/useReports", () => ({
  useReports: () => ({
    reports: [],
    items: [],
    nextCursor: "",
    hasMore: false,
    loading: false,
    loadingMore: false,
    error: "",
    streamStatus: "live",
    refresh: vi.fn(),
  }),
  useReport: () => ({
    data: null,
    report: null,
    loading: false,
    error: "",
    streamStatus: "live",
    refresh: vi.fn(),
  }),
}))

// The heavy tab contents are covered by their own suites. This axe gate renders
// the real page shell/header/tab bar plus representative panel content, keeping
// the scan focused on the structure a screen reader navigates.
vi.mock("@/features/versions/Workbench", () => ({
  Workbench: () => (
    <section data-testid="requirements-workbench-slot" aria-label="需求工作台">
      <h2>需求与澄清</h2>
      <p>暂无需求</p>
    </section>
  ),
}))
vi.mock("@/features/versions/OverviewTab", () => ({
  OverviewTab: () => (
    <section data-testid="overview-tab" aria-label="总览">
      <h2>总览</h2>
      <p>暂无执行结果</p>
    </section>
  ),
}))
vi.mock("@/features/mindmap/MindmapPage", () => ({
  MindmapPage: () => (
    <section data-testid="mindmap-tab" aria-label="用例脑图">
      <h2>用例脑图</h2>
      <p>暂无脑图节点</p>
    </section>
  ),
}))
vi.mock("@/features/execution/components/ExplorationTab", () => ({
  ExplorationTab: () => (
    <section data-testid="exploration-tab" aria-label="探索链路">
      <h2>探索链路</h2>
      <p>暂无链路节点</p>
    </section>
  ),
}))
vi.mock("@/features/execution/components/ResultsTab", () => ({
  ResultsTab: () => (
    <section data-testid="results-tab" aria-label="结果与证据">
      <h2>结果与证据</h2>
      <p>暂无执行结果</p>
    </section>
  ),
}))
vi.mock("@/features/execution/components/SessionsTab", () => ({
  SessionsTab: () => <section data-testid="sessions-tab" aria-label="执行会话" />,
}))
vi.mock("@/features/execution/components/BroadcastTab", () => ({
  BroadcastTab: () => <section data-testid="broadcast-tab" aria-label="实时播报" />,
}))
vi.mock("@/features/execution/components/EvidenceViewer", () => ({
  EvidenceViewer: () => null,
}))
vi.mock("@/features/code-intel/CodeChangesTab", () => ({
  CodeChangesTab: () => <section data-testid="code-tab" aria-label="代码与变更" />,
}))
vi.mock("@/features/resources/TakeoverDialog", () => ({
  TakeoverDialog: () => null,
}))

// The admin settings panels have their own suites; keep their forms out of the
// axe gate so the scan stays on the settings page and its tab list.
vi.mock("@/features/users/UsersPanel", () => ({
  UsersPanel: () => (
    <section aria-label="用户管理">
      <h2>用户</h2>
      <p>暂无用户</p>
    </section>
  ),
}))
vi.mock("@/features/llm-settings/LLMPanel", () => ({
  LLMPanel: () => (
    <section aria-label="LLM 配置">
      <h2>LLM</h2>
      <p>暂无模型配置</p>
    </section>
  ),
}))
vi.mock("@/features/sse-test/SSETestPanel", () => ({
  SSETestPanel: () => (
    <section aria-label="SSE 测试">
      <h2>SSE 测试</h2>
      <p>暂无连接</p>
    </section>
  ),
}))

import SettingsPage from "@/app/(app)/settings/page"
import LoginPage from "@/app/login/page"
import { VersionDetailPage } from "@/features/versions/VersionDetailPage"
import { VersionsPage } from "@/features/versions/VersionsPage"

/** A snowflake ID that a JS number cannot hold; it must stay a string verbatim. */
const VERSION_ID = "1844674407370955161"

const VERSION: Version = {
  id: VERSION_ID,
  name: "订单系统 2.0",
  label: "v2.0",
  description: "订单系统第二阶段",
  status: "clarifying",
  systemId: 1,
  environmentId: 2,
  codeRepositoryId: 3,
  codeBranch: "main",
  codeBaselineCommit: "0123456789abcdef0123456789abcdef01234567",
  bindings: {
    system: { id: 1, name: "订单系统", available: true },
    environment: { id: 2, name: "staging", available: true },
    codeRepository: { id: 3, name: "order-web", available: true },
  },
  requirementCount: 3,
  clarifiedCount: 1,
  openQuestionCount: 2,
  revision: 2,
  createdBy: 1,
  updatedBy: 1,
  createdAt: "2026-10-27T10:00:00Z",
  updatedAt: "2026-10-27T10:00:00Z",
}

/** Page objects shared by the version-detail shell assertions. */
function batchesResult() {
  return {
    batches: [],
    nextCursor: "",
    hasMore: false,
    loading: false,
    error: "",
    streamStatus: "live" as const,
    refresh: vi.fn().mockResolvedValue(undefined),
  }
}

function actionsResult() {
  return {
    createBatch: vi.fn(),
    pause: vi.fn(),
    resume: vi.fn(),
    cancel: vi.fn(),
    rerun: vi.fn(),
    exportReport: vi.fn(),
    pending: "" as const,
    error: "",
    clearError: vi.fn(),
  }
}

/**
 * Runs axe on the rendered container and fails on serious/critical impact only.
 * `color-contrast` is disabled because jsdom has no layout/paint engine; the
 * Playwright visual gate measures real contrast (spec §8.3).
 */
async function expectNoSeriousA11yViolations(container: HTMLElement): Promise<void> {
  const results = await axe.run(container, {
    rules: { "color-contrast": { enabled: false } },
  })
  const serious = results.violations.filter(
    (violation) => violation.impact === "serious" || violation.impact === "critical",
  )
  const failures = serious.map((violation) => {
    const targets = violation.nodes.map((node) => node.target.join(" ")).join(", ")
    return `${violation.id} (${violation.impact}): ${targets}`
  })
  expect(failures).toEqual([])
}

describe("accessibility (axe)", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) {
      if (typeof mock === "function" && "mockReset" in mock) mock.mockReset()
    }
    mocks.useAuth.mockReturnValue({ user: null, loading: false, login: vi.fn(), logout: vi.fn() })
    mocks.listVersions.mockResolvedValue({ items: [], nextCursor: "", hasMore: false })
    mocks.getVersion.mockResolvedValue(VERSION)
    mocks.fetchVersionEvents.mockResolvedValue({ events: [], cursor: 0, hasMore: false })
    mocks.useVersionStream.mockReturnValue({ events: [], status: "live", reconnect: vi.fn() })
    mocks.useBatches.mockReturnValue(batchesResult())
    mocks.useBatch.mockReturnValue({
      batch: null,
      loading: false,
      error: "",
      streamStatus: "live",
      refresh: vi.fn(),
    })
    mocks.useCaseRuns.mockReturnValue({
      caseRuns: [],
      nextCursor: "",
      hasMore: false,
      loading: false,
      error: "",
      streamStatus: "live",
      refresh: vi.fn(),
    })
    mocks.useCaseRun.mockReturnValue({
      caseRun: null,
      loading: false,
      error: "",
      streamStatus: "live",
      refresh: vi.fn(),
    })
    mocks.useReport.mockReturnValue({
      report: null,
      loading: false,
      error: "",
      streamStatus: "live",
      refresh: vi.fn(),
    })
    mocks.useExecution.mockReturnValue({
      batches: [],
      activeBatch: null,
      loading: false,
      error: "",
      streamStatus: "live",
      refresh: vi.fn(),
      actions: actionsResult(),
    })
    mocks.useExecutionActions.mockReturnValue(actionsResult())
  })

  afterEach(() => {
    cleanup()
    window.history.replaceState(null, "", "/")
  })

  it("detects a known serious violation so the gate cannot silently pass", async () => {
    const { container } = render(
      // biome-ignore lint/a11y/useAriaPropsSupportedByRole: deliberately malformed markup proves the axe gate detects violations.
      <div aria-checked="true">切换</div>,
    )
    const results = await axe.run(container, {
      rules: { "color-contrast": { enabled: false } },
    })
    const serious = results.violations.filter(
      (violation) => violation.impact === "serious" || violation.impact === "critical",
    )

    expect(serious.length).toBeGreaterThan(0)
  })

  it("has no serious or critical violations on the login page", async () => {
    const { container } = render(<LoginPage />)

    expect(await screen.findByRole("heading", { name: "登录 EzTest" })).toBeTruthy()
    await expectNoSeriousA11yViolations(container)
  })

  it("has no serious or critical violations on the versions list", async () => {
    mocks.listVersions.mockResolvedValue({
      items: [VERSION],
      nextCursor: "",
      hasMore: false,
    })

    const { container } = render(<VersionsPage />)

    expect(await screen.findByText("订单系统 2.0")).toBeTruthy()
    await expectNoSeriousA11yViolations(container)
  })

  it("has no serious or critical violations on the key version-detail tabs", async () => {
    window.history.replaceState(null, "", `/versions/${VERSION_ID}?tab=requirements`)

    const { container } = render(<VersionDetailPage versionId={VERSION_ID} />)

    expect(await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })).toBeTruthy()
    expect(await screen.findByTestId("requirements-workbench-slot")).toBeTruthy()
    await expectNoSeriousA11yViolations(container)

    for (const label of ["用例脑图", "探索链路", "结果与证据"]) {
      fireEvent.mouseDown(screen.getByRole("tab", { name: label }))
      await waitFor(() =>
        expect(screen.getByRole("tab", { name: label }).getAttribute("aria-selected")).toBe("true"),
      )
      await expectNoSeriousA11yViolations(container)
    }
  })

  it("has no serious or critical violations on the admin settings page", async () => {
    mocks.useAuth.mockReturnValue({
      user: { id: 1, username: "admin", role: "admin", status: "active" },
      loading: false,
      login: vi.fn(),
      logout: vi.fn(),
    })

    const { container } = render(<SettingsPage />)

    expect(await screen.findByRole("heading", { name: "设置" })).toBeTruthy()
    expect(await screen.findByRole("tab", { name: "用户" })).toBeTruthy()
    await expectNoSeriousA11yViolations(container)
  })
})
