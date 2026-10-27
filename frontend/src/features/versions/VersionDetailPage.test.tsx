import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { ApiError } from "@/lib/api-client"

const codeIntelMocks = vi.hoisted(() => ({
  getVersionChanges: vi.fn(),
  getLatestIndexRun: vi.fn(),
  getLatestAnalysis: vi.fn(),
  startAnalysis: vi.fn(),
  getFileDiff: vi.fn(),
}))

vi.mock("@/features/code-intel/api", () => ({
  getVersionChanges: codeIntelMocks.getVersionChanges,
  getLatestIndexRun: codeIntelMocks.getLatestIndexRun,
  getLatestAnalysis: codeIntelMocks.getLatestAnalysis,
  startAnalysis: codeIntelMocks.startAnalysis,
  getFileDiff: codeIntelMocks.getFileDiff,
}))

const mocks = vi.hoisted(() => ({
  getVersion: vi.fn(),
  startClarification: vi.fn(),
  enterDesign: vi.fn(),
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
  useVersionStream: vi.fn(),
}))

vi.mock("./api", () => ({
  getVersion: mocks.getVersion,
  startClarification: mocks.startClarification,
  enterDesign: mocks.enterDesign,
  listRequirements: mocks.listRequirements,
  listRelations: mocks.listRelations,
  getClarification: mocks.getClarification,
  listClarificationMessages: mocks.listClarificationMessages,
  sendClarificationMessage: mocks.sendClarificationMessage,
  startClarificationSession: mocks.startClarificationSession,
  analyzeClarification: mocks.analyzeClarification,
  reconClarification: mocks.reconClarification,
  listClarificationQuestions: mocks.listClarificationQuestions,
  createClarificationQuestion: mocks.createClarificationQuestion,
  updateClarificationQuestion: mocks.updateClarificationQuestion,
  listDecisions: mocks.listDecisions,
  createDecision: mocks.createDecision,
  updateDecision: mocks.updateDecision,
}))

// The workbench owns the version stream; this suite only covers the tab shell.
vi.mock("./useVersionStream", () => ({ useVersionStream: mocks.useVersionStream }))

// The bindings form reads the P2 resource APIs; the container test stays focused
// on the tab shell and header actions, while T17's own suite covers bindings.
vi.mock("./VersionBindingsForm", () => ({
  VersionBindingsForm: () => null,
}))

import VersionDetailRoutePage from "@/app/(app)/versions/[id]/page"

import type { Analysis, ChangeList, IndexRun } from "@/features/code-intel/types"

import type { Version } from "./types"
import { VersionDetailPage } from "./VersionDetailPage"

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

const TAB_LABELS = [
  "需求与澄清",
  "总览",
  "执行会话",
  "探索链路",
  "实时播报",
  "结果与证据",
  "问题",
  "报告",
  "代码与变更",
]

const PLACEHOLDER_TABS = [
  ["执行会话", "P6"],
  ["探索链路", "P6"],
  ["实时播报", "P6"],
  ["结果与证据", "P6"],
  ["问题", "P7"],
  ["报告", "P6"],
] as const

const CODE_BASE = "0123456789abcdef0123456789abcdef01234567"
const CODE_HEAD = "89abcdef0123456789abcdef0123456789abcdef"

const CODE_CHANGES: ChangeList = {
  changeSetId: "1844674407370955163",
  baseCommit: CODE_BASE,
  headCommit: CODE_HEAD,
  stats: { files: 1, additions: 12, deletions: 1, truncated: false },
  files: [
    {
      path: "src/api/refund.ts",
      oldPath: null,
      status: "M",
      additions: 12,
      deletions: 1,
      binary: false,
      headBlobHash: "git-sha1:1111111111111111111111111111111111111111",
    },
  ],
  nextCursor: null,
  hasMore: false,
}

const CODE_INDEX: IndexRun = {
  runId: "1844674407370955164",
  repoId: 3,
  commit: CODE_HEAD,
  status: "done",
  stats: { files: 20, truncated: false },
  startedAt: "2026-10-28T00:50:00Z",
  finishedAt: "2026-10-28T00:52:03Z",
}

const CODE_ANALYSIS: Analysis = {
  id: "1844674407370955162",
  versionId: VERSION_ID,
  repoId: 3,
  baseCommit: CODE_BASE,
  headCommit: CODE_HEAD,
  status: "done",
  degraded: false,
  analysisVersion: 1,
  model: "deepseek-chat",
  promptVersion: "p4.1",
  changeSetId: "1844674407370955163",
  changeSummary: "退款接口新增 reason 参数",
  stats: null,
  factVersions: [],
  candidates: [],
  unmapped: [],
  errors: [],
  createdAt: "2026-10-28T01:00:00Z",
  startedAt: "2026-10-28T01:00:00Z",
  finishedAt: "2026-10-28T01:02:03Z",
}

describe("VersionDetailPage", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset()
    mocks.getVersion.mockResolvedValue(VERSION)
    // The mounted workbench loads these on mount; empty data keeps the shell
    // assertions deterministic.
    mocks.listRequirements.mockResolvedValue({ items: [], nextCursor: "", hasMore: false })
    mocks.listRelations.mockResolvedValue([])
    mocks.getClarification.mockResolvedValue({
      session: null,
      questions: { open: 0, answered: 0, dismissed: 0 },
      decisions: { proposed: 0, accepted: 0, rejected: 0 },
    })
    mocks.listClarificationMessages.mockResolvedValue({
      items: [],
      nextCursor: "",
      hasMore: false,
    })
    mocks.listClarificationQuestions.mockResolvedValue({
      items: [],
      nextCursor: "",
      hasMore: false,
    })
    mocks.listDecisions.mockResolvedValue({ items: [], nextCursor: "", hasMore: false })
    mocks.useVersionStream.mockReturnValue({ events: [], status: "live", reconnect: vi.fn() })
    for (const mock of Object.values(codeIntelMocks)) mock.mockReset()
    codeIntelMocks.getVersionChanges.mockResolvedValue(CODE_CHANGES)
    codeIntelMocks.getLatestIndexRun.mockResolvedValue(CODE_INDEX)
    codeIntelMocks.getLatestAnalysis.mockResolvedValue(CODE_ANALYSIS)
  })

  afterEach(() => {
    cleanup()
    window.history.replaceState(null, "", "/")
  })

  it("renders the nine tabs with requirements active by default", async () => {
    render(<VersionDetailPage versionId={VERSION_ID} />)

    expect(await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })).toBeTruthy()
    expect(mocks.getVersion).toHaveBeenCalledWith(VERSION_ID)

    const tabs = screen.getAllByRole("tab")
    expect(tabs.map((tab) => tab.textContent)).toEqual(TAB_LABELS)
    expect(screen.getByRole("tab", { name: "需求与澄清" }).getAttribute("aria-selected")).toBe(
      "true",
    )
    expect(screen.getByTestId("requirements-workbench-slot")).toBeTruthy()
  })

  it("opens the overview tab from the ?tab=overview deep link", async () => {
    window.history.replaceState(null, "", `/versions/${VERSION_ID}?tab=overview`)

    render(<VersionDetailPage versionId={VERSION_ID} />)
    await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })

    await waitFor(() =>
      expect(screen.getByRole("tab", { name: "总览" }).getAttribute("aria-selected")).toBe("true"),
    )
    expect(screen.getByRole("heading", { name: "需求" })).toBeTruthy()
    expect(screen.getByText("已确认 1 · 未澄清 2")).toBeTruthy()
    expect(screen.getByRole("heading", { name: "待澄清问题" })).toBeTruthy()
    expect(screen.getByRole("heading", { name: "关系" })).toBeTruthy()
    expect(screen.getByText("关系数量由需求工作台加载，总览不做推测。")).toBeTruthy()
    expect(screen.getByRole("heading", { name: "资源绑定" })).toBeTruthy()
    expect(screen.getByText("订单系统")).toBeTruthy()

    expect(screen.getByRole("heading", { name: "执行批次" })).toBeTruthy()
    expect(screen.getByText("暂无批次数据，执行引擎在 P6 阶段接入。")).toBeTruthy()
    expect(screen.getByRole("heading", { name: "用例脑图" })).toBeTruthy()
    expect(screen.getByText("尚未生成用例，脑图在 P5 阶段开放。")).toBeTruthy()
    expect(screen.getByRole("heading", { name: "问题" })).toBeTruthy()
    expect(screen.getByText("暂无问题，问题中心在 P7 阶段接入。")).toBeTruthy()
  })

  it("switches tabs, syncs ?tab= and returns from a placeholder tab", async () => {
    render(<VersionDetailPage versionId={VERSION_ID} />)
    await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })

    fireEvent.mouseDown(screen.getByRole("tab", { name: "执行会话" }))

    await waitFor(() => expect(window.location.search).toContain("tab=sessions"))
    expect(await screen.findByRole("heading", { name: "执行会话" })).toBeTruthy()
    expect(screen.getByText("阶段 P6")).toBeTruthy()
    expect(screen.getByText("该 Tab 尚未接入数据，不展示占位假数据。")).toBeTruthy()
    expect(screen.queryAllByRole("table")).toHaveLength(0)

    fireEvent.click(screen.getByRole("button", { name: "返回需求与澄清工作台" }))

    await waitFor(() =>
      expect(screen.getByRole("tab", { name: "需求与澄清" }).getAttribute("aria-selected")).toBe(
        "true",
      ),
    )
    expect(screen.getByTestId("requirements-workbench-slot")).toBeTruthy()
    await waitFor(() => expect(window.location.search).toContain("tab=requirements"))
  })

  it("labels every future tab with its owning phase and renders no fake data", async () => {
    render(<VersionDetailPage versionId={VERSION_ID} />)
    await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })

    for (const [label, phase] of PLACEHOLDER_TABS) {
      fireEvent.mouseDown(screen.getByRole("tab", { name: label }))

      expect(await screen.findByRole("heading", { name: label })).toBeTruthy()
      expect(screen.getByText(`阶段 ${phase}`)).toBeTruthy()
      expect(screen.queryAllByRole("table")).toHaveLength(0)
      expect(screen.queryAllByRole("row")).toHaveLength(0)
    }
  })

  it("mounts the real code changes tab instead of the P4 placeholder", async () => {
    render(<VersionDetailPage versionId={VERSION_ID} />)
    await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })

    fireEvent.mouseDown(screen.getByRole("tab", { name: "代码与变更" }))

    expect(await screen.findByRole("heading", { name: "变更文件" })).toBeTruthy()
    expect(screen.getByText("src/api/refund.ts")).toBeTruthy()
    expect(screen.queryByText("阶段 P4")).toBeNull()
    expect(screen.queryByText(/代码与变更在 P4 阶段接入/)).toBeNull()
    expect(screen.queryByText("该 Tab 尚未接入数据，不展示占位假数据。")).toBeNull()

    expect(codeIntelMocks.getVersionChanges).toHaveBeenCalledWith(VERSION_ID)
    expect(codeIntelMocks.getLatestIndexRun).toHaveBeenCalledWith(3)
    expect(codeIntelMocks.getLatestAnalysis).toHaveBeenCalledWith(VERSION_ID)
  })

  it("shows the header status, binding summary and action for the current state", async () => {
    mocks.getVersion.mockResolvedValue({
      ...VERSION,
      bindings: {
        ...VERSION.bindings,
        codeRepository: { id: 3, name: "order-web", available: false },
      },
    })

    render(<VersionDetailPage versionId={VERSION_ID} />)
    await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })

    expect(screen.getByText("澄清中")).toBeTruthy()
    expect(screen.getByText("系统：订单系统")).toBeTruthy()
    expect(screen.getByText("环境：staging")).toBeTruthy()
    expect(screen.getByText("代码仓：order-web（已删除）")).toBeTruthy()
    expect(screen.getByText("未澄清需求 2")).toBeTruthy()
    expect(screen.getByText("待澄清问题 2")).toBeTruthy()
    expect(screen.getByRole("button", { name: "进入设计" })).toBeTruthy()
    expect(screen.queryByRole("button", { name: "开始澄清" })).toBeNull()
    expect(screen.getByRole("link", { name: "返回版本列表" }).getAttribute("href")).toBe(
      "/versions",
    )
  })

  it("starts clarification from the draft header and refreshes on demand", async () => {
    mocks.getVersion.mockResolvedValue({ ...VERSION, status: "draft" })
    mocks.startClarification.mockResolvedValue({
      ...VERSION,
      status: "clarifying",
      revision: 3,
    })

    render(<VersionDetailPage versionId={VERSION_ID} />)
    await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })
    const versionHeader = screen.getAllByRole("banner")[0]
    expect(within(versionHeader).getByText("草稿")).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: "开始澄清" }))

    await waitFor(() => expect(mocks.startClarification).toHaveBeenCalledWith(VERSION_ID))
    expect(await screen.findByText("澄清中")).toBeTruthy()
    expect(screen.getByRole("button", { name: "进入设计" })).toBeTruthy()

    fireEvent.click(screen.getByRole("button", { name: "刷新" }))
    await waitFor(() => expect(mocks.getVersion).toHaveBeenCalledTimes(2))
  })

  it("asks for confirmation when entering design with unclarified requirements", async () => {
    mocks.enterDesign.mockRejectedValueOnce(
      new ApiError({
        status: 409,
        code: "conflict",
        message: "unclarified requirements",
        details: {
          reason: "unclarified_requirements",
          unclarifiedRequirementIds: ["1001", "1002"],
          openQuestionCount: 1,
        },
      }),
    )
    mocks.enterDesign.mockResolvedValueOnce({
      version: { ...VERSION, status: "designing", revision: 6 },
      warnings: ["2 个需求未澄清"],
    })

    render(<VersionDetailPage versionId={VERSION_ID} />)
    await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })

    fireEvent.click(screen.getByRole("button", { name: "进入设计" }))

    const confirm = await screen.findByRole("alertdialog")
    expect(confirm.textContent).toContain("2 条需求")
    expect(confirm.textContent).toContain("1 个待澄清问题")
    expect(mocks.enterDesign).toHaveBeenCalledWith(VERSION_ID, false)

    fireEvent.click(within(confirm).getByRole("button", { name: "确认进入设计" }))

    await waitFor(() => expect(mocks.enterDesign).toHaveBeenLastCalledWith(VERSION_ID, true))
    expect(await screen.findByText("设计中")).toBeTruthy()
    expect(await screen.findByText("2 个需求未澄清")).toBeTruthy()
  })

  it("keeps the last data, reports a load failure and retries", async () => {
    mocks.getVersion
      .mockRejectedValueOnce(
        new ApiError({ status: 0, code: "network_error", message: "connection refused" }),
      )
      .mockResolvedValueOnce(VERSION)

    render(<VersionDetailPage versionId={VERSION_ID} />)

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("无法连接 API 服务")

    fireEvent.click(screen.getByRole("button", { name: "重试" }))

    expect(await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })).toBeTruthy()
    expect(mocks.getVersion).toHaveBeenCalledTimes(2)
  })

  it("renders the detail route with the string id from the dynamic segment", async () => {
    const route = await VersionDetailRoutePage({ params: Promise.resolve({ id: VERSION_ID }) })

    render(route)

    expect(await screen.findByRole("heading", { level: 1, name: /订单系统 2.0/ })).toBeTruthy()
    expect(mocks.getVersion).toHaveBeenCalledWith(VERSION_ID)
  })
})
