import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { AgentCenterCatalog, AgentCenterRole, AgentCenterSession } from "./agent-api"

const auth = vi.hoisted(() => ({ role: "admin" as "admin" | "user" }))

vi.mock("@/features/auth/AuthProvider", () => ({
  useAuth: () => ({
    user: { id: 1, username: "tester", role: auth.role, status: "active" },
    loading: false,
    login: vi.fn(),
    logout: vi.fn(),
  }),
}))

const mocks = vi.hoisted(() => ({
  getAgentCenterCatalog: vi.fn(),
  getAgentCenterQuality: vi.fn(),
  listAgentCenterSessions: vi.fn(),
}))

vi.mock("./agent-api", () => ({
  getAgentCenterCatalog: mocks.getAgentCenterCatalog,
  getAgentCenterQuality: mocks.getAgentCenterQuality,
  listAgentCenterSessions: mocks.listAgentCenterSessions,
}))

vi.mock("./AgentConsole", () => ({
  AgentConsole: () => <div data-testid="agent-console-mock">agent-console-mock</div>,
}))

import { AgentCenterPage } from "./AgentCenterPage"

type RoleSeed = {
  role: string
  name: string
  phase: string
  profile: string | null
  mode: "agent" | "deterministic" | "manual_review"
  note?: string
}

// The 13 logical roles of spec §8.1, in order. The backend pins the same
// catalogue; the page test keeps its own fixture so a UI regression is visible.
const ROLE_SEEDS: RoleSeed[] = [
  {
    role: "clarification-dialog",
    name: "澄清对话",
    phase: "P3",
    profile: "clarification-dialog",
    mode: "agent",
  },
  {
    role: "requirement-analysis",
    name: "需求分析",
    phase: "P3",
    profile: "requirement-analysis",
    mode: "agent",
  },
  {
    role: "context-recon",
    name: "上下文侦察",
    phase: "P3",
    profile: "context-recon",
    mode: "agent",
  },
  {
    role: "app-exploration",
    name: "应用探索",
    phase: "P5",
    profile: "app-exploration",
    mode: "agent",
  },
  { role: "case-design", name: "测试设计", phase: "P5", profile: "case-design", mode: "agent" },
  { role: "case-review", name: "设计评审", phase: "P5", profile: "case-review", mode: "agent" },
  {
    role: "case-data-plan",
    name: "数据/前置规划",
    phase: "P5",
    profile: "case-data-plan",
    mode: "agent",
  },
  { role: "execution-agent", name: "执行", phase: "P6", profile: "execution-agent", mode: "agent" },
  {
    role: "supplement-agent",
    name: "补全",
    phase: "P7",
    profile: "supplement-agent",
    mode: "agent",
  },
  {
    role: "failure-analysis-agent",
    name: "失败分析",
    phase: "P7",
    profile: "failure-analysis-agent",
    mode: "agent",
  },
  {
    role: "issue-triage-agent",
    name: "问题整理",
    phase: "P7",
    profile: "issue-triage-agent",
    mode: "agent",
  },
  {
    role: "report",
    name: "报告整理",
    phase: "P7",
    profile: null,
    mode: "deterministic",
    note: "无独立 profile，由确定性服务生成",
  },
  {
    role: "knowledge",
    name: "知识沉淀",
    phase: "P8",
    profile: null,
    mode: "manual_review",
    note: "无独立 profile，人工审核入库",
  },
]

function makeRole(seed: RoleSeed, index: number): AgentCenterRole {
  return {
    order: index + 1,
    role: seed.role,
    name: seed.name,
    phase: seed.phase,
    mode: seed.mode,
    note: seed.note ?? "",
    profile_key: seed.profile,
    profile_id: seed.profile === null ? null : index + 1,
    enabled: seed.profile !== null,
    model: seed.profile === null ? "" : "deepseek-chat",
    prompt_version: null,
    allowed_tools: seed.profile === null ? [] : ["tool.one"],
    stats: { run_count: 4, succeeded_count: 3, failed_count: 1, success_rate: 0.75 },
  }
}

const ROLES: AgentCenterRole[] = ROLE_SEEDS.map(makeRole)

const SESSION: AgentCenterSession = {
  id: 9,
  role: "case-design",
  title: "设计会话",
  status: "done",
  running: false,
  created_at: "2026-10-29T00:00:00Z",
  updated_at: "2026-10-29T01:00:00Z",
  last_activity_at: "2026-10-29T01:00:00Z",
}

function catalog(roles: AgentCenterRole[] = ROLES): AgentCenterCatalog {
  return { roles, latest_eval: null }
}

describe("AgentCenterPage", () => {
  beforeEach(() => {
    auth.role = "admin"
    mocks.getAgentCenterCatalog.mockReset()
    mocks.getAgentCenterQuality.mockReset()
    mocks.listAgentCenterSessions.mockReset()
    mocks.getAgentCenterCatalog.mockResolvedValue(catalog())
    mocks.getAgentCenterQuality.mockResolvedValue({ latest: null })
    mocks.listAgentCenterSessions.mockResolvedValue({ items: [SESSION] })
  })

  afterEach(() => {
    cleanup()
    window.history.replaceState(null, "", "/")
  })

  it("renders the 13-role catalogue and the admin configuration tab", async () => {
    render(<AgentCenterPage />)

    expect(await screen.findByRole("heading", { name: "Agent 中心" })).toBeTruthy()
    expect(await screen.findAllByTestId("agent-role-row")).toHaveLength(13)
    expect(screen.queryByTestId("readonly-banner")).toBeNull()
    expect(screen.getAllByRole("button", { name: /编辑/ })).toHaveLength(11)
    expect(screen.getByRole("tab", { name: /配置与控制台/ })).toBeTruthy()
  })

  it("marks a regular user read-only and hides every write control", async () => {
    auth.role = "user"

    render(<AgentCenterPage />)

    expect(await screen.findAllByTestId("agent-role-row")).toHaveLength(13)
    expect(screen.getByTestId("readonly-banner")).toBeTruthy()
    expect(screen.queryAllByRole("button", { name: /编辑/ })).toHaveLength(0)
    expect(screen.queryByRole("tab", { name: /配置/ })).toBeNull()
    expect(screen.getByText("尚未运行评测")).toBeTruthy()

    fireEvent.mouseDown(screen.getByRole("tab", { name: "运行观察" }))
    expect(await screen.findByText("设计会话")).toBeTruthy()
    expect(screen.queryAllByRole("button", { name: /打开控制台/ })).toHaveLength(0)
  })

  it("shows empty states for an empty catalogue, no evaluation and no runs", async () => {
    mocks.getAgentCenterCatalog.mockResolvedValue(catalog([]))
    mocks.listAgentCenterSessions.mockResolvedValue({ items: [] })

    render(<AgentCenterPage />)

    expect(await screen.findByText("暂无 Agent 角色")).toBeTruthy()
    expect(screen.getByText("尚未运行评测")).toBeTruthy()

    fireEvent.mouseDown(screen.getByRole("tab", { name: "运行观察" }))
    expect(await screen.findByText("暂无运行记录")).toBeTruthy()
    expect(screen.queryAllByTestId("agent-center-session-row")).toHaveLength(0)
  })

  it("reports a catalogue load error and retries", async () => {
    mocks.getAgentCenterCatalog.mockRejectedValueOnce(new Error("catalog down"))

    render(<AgentCenterPage />)

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("catalog down")

    fireEvent.click(screen.getByRole("button", { name: "重试" }))

    expect(await screen.findAllByTestId("agent-role-row")).toHaveLength(13)
    expect(mocks.getAgentCenterCatalog).toHaveBeenCalledTimes(2)
  })

  it("passes role, status and limit filters to the run-observation API", async () => {
    render(<AgentCenterPage />)
    await screen.findAllByTestId("agent-role-row")

    fireEvent.mouseDown(screen.getByRole("tab", { name: "运行观察" }))
    await screen.findByText("设计会话")

    fireEvent.change(screen.getByLabelText("按角色筛选"), { target: { value: "case-design" } })
    await waitFor(() =>
      expect(mocks.listAgentCenterSessions).toHaveBeenLastCalledWith(
        expect.objectContaining({ role: "case-design" }),
      ),
    )

    fireEvent.change(screen.getByLabelText("按状态筛选"), { target: { value: "running" } })
    await waitFor(() =>
      expect(mocks.listAgentCenterSessions).toHaveBeenLastCalledWith(
        expect.objectContaining({ role: "case-design", status: "running" }),
      ),
    )

    fireEvent.change(screen.getByLabelText("运行记录数量"), { target: { value: "50" } })
    await waitFor(() =>
      expect(mocks.listAgentCenterSessions).toHaveBeenLastCalledWith(
        expect.objectContaining({ role: "case-design", status: "running", limit: 50 }),
      ),
    )
  })

  it("renders the latest quality metrics for administrators", async () => {
    mocks.getAgentCenterQuality.mockResolvedValue({
      latest: {
        run_id: "7",
        passed: true,
        metrics: { design_coverage: 0.95, issue_false_positive_rate: 0.01 },
        thresholds: { design_coverage: 0.9 },
        created_at: "2026-10-29T02:00:00Z",
      },
    })

    render(<AgentCenterPage />)

    expect(await screen.findByText("设计覆盖率")).toBeTruthy()
    expect(screen.getByText("95.0%")).toBeTruthy()
    expect(screen.getByText("问题误报率")).toBeTruthy()
    expect(screen.getByText("通过")).toBeTruthy()
  })

  it("shows a loading state while the catalogue is pending", () => {
    mocks.getAgentCenterCatalog.mockReturnValue(new Promise<AgentCenterCatalog>(() => {}))

    render(<AgentCenterPage />)

    expect(screen.getByRole("status").textContent).toContain("正在加载 Agent 中心")
    expect(screen.queryAllByTestId("agent-role-row")).toHaveLength(0)
  })

  it("lets an administrator open the existing console on a run-observation row", async () => {
    render(<AgentCenterPage />)
    await screen.findAllByTestId("agent-role-row")

    fireEvent.mouseDown(screen.getByRole("tab", { name: "运行观察" }))
    await screen.findByText("设计会话")

    fireEvent.click(screen.getByRole("button", { name: "打开控制台" }))

    expect(await screen.findByTestId("agent-console-mock")).toBeTruthy()
    expect(window.location.search).toBe("?session=9")
  })
})
