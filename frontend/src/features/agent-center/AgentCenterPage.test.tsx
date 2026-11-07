import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

const agentCenterApi = vi.hoisted(() => ({
  getAgentCatalog: vi.fn(),
  listAgentRuns: vi.fn(),
  getAgentCenterQuality: vi.fn(),
  listAgentProfiles: vi.fn(),
  listAgentTools: vi.fn(),
  listLLMModels: vi.fn(),
}))
vi.mock("./api", () => agentCenterApi)

const auth = vi.hoisted(() => ({ isAdmin: false }))
vi.mock("@/features/auth/useIsAdmin", () => ({ useIsAdmin: () => auth.isAdmin }))

const navigation = vi.hoisted(() => ({ replace: vi.fn() }))
vi.mock("next/navigation", () => ({ useRouter: () => navigation }))

vi.mock("./ProfileEditor", () => ({
  ProfileEditor: ({ profileKey }: { profileKey: string }) => (
    <div data-testid="agent-center-profile-editor">editor:{profileKey}</div>
  ),
}))

import { AgentCenterPage } from "./AgentCenterPage"
import type { AgentCatalog, AgentCenterQuality, AgentRunView, ProfileView } from "./types"

function catalogFixture(): AgentCatalog {
  const teams: AgentCatalog["teams"] = [
    { team: "conversation", label: "对话 Host 与后台作业", profile_keys: [] },
    { team: "execution", label: "执行 / 分析", profile_keys: [] },
  ]
  const profiles: AgentCatalog["profiles"] = []
  const keysByTeam: Record<string, string[]> = {
    conversation: ["clarification_host", "design_host", "design_author", "design_critic"],
    execution: [
      "case_executor",
      "failure_analyst",
      "coverage_analyst",
      "issue_triager",
      "report_writer",
      "version_steward",
      "knowledge_curator",
    ],
  }
  let order = 0
  for (const team of teams) {
    for (const key of keysByTeam[team.team] ?? []) {
      order += 1
      profiles.push({
        profile_key: key,
        name: `Profile ${key}`,
        team: team.team,
        order,
        mode: "agent",
        model: null,
        enabled: true,
        budgets: { max_turns: 12, soft_seconds: 300, hard_seconds: 600 },
        allowed_tools_count: 9,
        prompt_version: 1,
        prompt_updated_at: "2026-11-01T09:00:00Z",
        updated_at: "2026-11-01T09:05:00Z",
        stats: {
          run_count: key === "clarification_host" ? 3 : 0,
          done_count: key === "clarification_host" ? 2 : 0,
          failed_count: key === "clarification_host" ? 1 : 0,
          aborted_count: 0,
          interrupted_count: 0,
          success_rate: key === "clarification_host" ? 2 / 3 : 0,
          last_run_at: key === "clarification_host" ? "2026-11-01T10:00:00Z" : null,
        },
      })
    }
    team.profile_keys = keysByTeam[team.team] ?? []
  }
  return { profiles, teams }
}

const runFixture: AgentRunView = {
  id: "31",
  profile_key: "case_executor",
  work_item_id: "501",
  work_type: "execute.case",
  version_id: "7",
  version_title: "订单系统 Q4",
  conversation_id: null,
  status: "done",
  terminal_reason: "goal_met",
  turns: 9,
  usage: { input_tokens: 1200, output_tokens: 300 },
  started_at: "2026-11-01T09:00:00Z",
  finished_at: "2026-11-01T09:02:00Z",
}

const qualityFixture: AgentCenterQuality = {
  latest: {
    id: "9",
    mode: "real_llm",
    status: "failed",
    created_at: "2026-11-01T11:00:00Z",
    metrics_count: 7,
    metrics_passed: 6,
  },
  run_stats: {
    run_count: 6,
    done_count: 4,
    failed_count: 1,
    aborted_count: 1,
    interrupted_count: 0,
    success_rate: 0.8,
    last_run_at: "2026-11-01T10:00:00Z",
  },
}

const profileViewFixture: ProfileView = {
  id: "101",
  key: "clarification_host",
  name: "澄清 Host",
  system_prompt: "[profile:clarification_host] secret prompt",
  allowed_tools: ["requirement.read"],
  model: null,
  budgets: { max_turns: 12, soft_seconds: 300, hard_seconds: 600 },
  enabled: true,
  prompt_version: 1,
  created_at: "2026-10-01T00:00:00Z",
  updated_at: "2026-10-02T00:00:00Z",
}

describe("AgentCenterPage", () => {
  beforeEach(() => {
    auth.isAdmin = false
    agentCenterApi.getAgentCatalog.mockReset()
    agentCenterApi.listAgentRuns.mockReset()
    agentCenterApi.getAgentCenterQuality.mockReset()
    agentCenterApi.listAgentProfiles.mockReset()
    agentCenterApi.listAgentTools.mockReset()
    agentCenterApi.listLLMModels.mockReset()
    navigation.replace.mockReset()

    agentCenterApi.getAgentCatalog.mockResolvedValue(catalogFixture())
    agentCenterApi.listAgentRuns.mockResolvedValue({
      items: [runFixture],
      nextCursor: "",
      hasMore: false,
    })
    agentCenterApi.getAgentCenterQuality.mockResolvedValue(qualityFixture)
    agentCenterApi.listAgentProfiles.mockResolvedValue([profileViewFixture])
    agentCenterApi.listAgentTools.mockResolvedValue([])
    agentCenterApi.listLLMModels.mockResolvedValue([])
  })

  afterEach(cleanup)

  it("renders the 2-Host conversation team and the execution area for a normal user", async () => {
    render(<AgentCenterPage />)

    expect(await screen.findByTestId("agent-center-catalog")).toBeInTheDocument()
    expect(agentCenterApi.getAgentCatalog).toHaveBeenCalledTimes(1)

    const conversation = screen.getByTestId("agent-center-team-conversation")
    const execution = screen.getByTestId("agent-center-team-execution")
    expect(conversation).toHaveTextContent("对话 Host 与后台作业")
    expect(execution).toHaveTextContent("执行 / 分析")
    expect(screen.getAllByTestId(/agent-center-profile-/)).toHaveLength(11)

    // Two Hosts only; the design author/critic are background jobs, not Hosts.
    for (const key of ["clarification_host", "design_host"]) {
      const row = within(conversation).getByTestId(`agent-center-profile-${key}`)
      expect(row).toHaveTextContent(`Profile ${key}`)
      expect(row).toHaveTextContent("对话 Host")
    }
    for (const key of ["design_author", "design_critic"]) {
      const job = within(conversation).getByTestId(`agent-center-profile-${key}`)
      expect(job).toHaveTextContent("后台作业")
      expect(job).toHaveTextContent("无对话入口")
      expect(within(job).queryByRole("button", { name: /对话/ })).not.toBeInTheDocument()
      expect(within(job).queryByRole("link", { name: /对话/ })).not.toBeInTheDocument()
    }

    // Execution/analysis runs are listed as profiles, not as conversation Agents.
    for (const key of [
      "case_executor",
      "failure_analyst",
      "coverage_analyst",
      "issue_triager",
      "report_writer",
      "version_steward",
      "knowledge_curator",
    ]) {
      expect(within(execution).getByTestId(`agent-center-profile-${key}`)).toBeInTheDocument()
    }
    expect(within(execution).queryByText("对话 Host")).not.toBeInTheDocument()

    // Removed Scout/Curator/Planner/case_author profiles never surface.
    expect(
      screen.queryByText(/context_scout|requirement_curator|design_planner|data_planner|case_author/),
    ).not.toBeInTheDocument()
    expect(screen.queryByText(/explorer/)).not.toBeInTheDocument()

    // UI copy no longer calls background jobs "Agent" or advertises 14 profiles.
    expect(screen.getByText(/2 个对话 Host/)).toBeInTheDocument()
    expect(screen.queryByText(/14 个固定 Agent profile/)).not.toBeInTheDocument()
    expect(screen.queryByText(/暂无 Agent profile/)).not.toBeInTheDocument()

    // Normal users never see prompt bodies or admin-only tabs/buttons.
    expect(screen.queryByText(/secret prompt/)).not.toBeInTheDocument()
    expect(screen.queryByRole("tab", { name: "配置" })).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "编辑" })).not.toBeInTheDocument()
    expect(screen.queryByTestId("agent-center-prompt")).not.toBeInTheDocument()
  })

  it("switches to runs and filters by profile/status", async () => {
    render(<AgentCenterPage initialTab="runs" />)

    expect(await screen.findByTestId("agent-center-runs")).toBeInTheDocument()
    expect(agentCenterApi.listAgentRuns).toHaveBeenCalledTimes(1)
    const row = await screen.findByTestId("agent-center-run-31")
    expect(row).toHaveTextContent("execute.case")
    expect(row).toHaveTextContent("订单系统 Q4")
    expect(row).toHaveTextContent("完成")
    expect(screen.queryByTestId("agent-center-run-transcript")).not.toBeInTheDocument()

    fireEvent.change(screen.getByTestId("agent-center-run-filter-status"), {
      target: { value: "failed" },
    })
    await waitFor(() =>
      expect(agentCenterApi.listAgentRuns).toHaveBeenLastCalledWith(
        expect.objectContaining({ status: "failed" }),
      ),
    )
    expect(navigation.replace).toHaveBeenCalledWith("/agents?tab=runs")
  })

  it("shows the latest eval and run stats on the quality tab", async () => {
    render(<AgentCenterPage initialTab="quality" />)

    expect(await screen.findByTestId("agent-center-quality")).toBeInTheDocument()
    expect(await screen.findByTestId("agent-center-quality-latest")).toHaveTextContent("6/7")
    expect(screen.getByTestId("agent-center-quality-stats")).toHaveTextContent("完成4")
    expect(screen.getByTestId("agent-center-quality-stats")).toHaveTextContent("80%")
  })

  it("shows the empty eval state instead of fabricated zeros", async () => {
    agentCenterApi.getAgentCenterQuality.mockResolvedValue({
      ...qualityFixture,
      latest: null,
    })
    render(<AgentCenterPage initialTab="quality" />)
    expect(await screen.findByTestId("agent-center-quality-empty")).toHaveTextContent(
      "尚未运行评测",
    )
  })

  it("opens the admin config tab and the profile editor", async () => {
    auth.isAdmin = true
    render(<AgentCenterPage initialTab="config" />)

    expect(await screen.findByTestId("agent-center-config")).toBeInTheDocument()
    expect(agentCenterApi.listAgentProfiles).toHaveBeenCalledTimes(1)
    expect(agentCenterApi.listAgentTools).toHaveBeenCalledTimes(1)

    const edit = await screen.findByTestId("agent-center-edit-clarification_host")
    fireEvent.click(edit)

    expect(await screen.findByTestId("agent-center-profile-editor")).toHaveTextContent(
      "editor:clarification_host",
    )
    expect(navigation.replace).toHaveBeenCalledWith("/agents?tab=config&profile=clarification_host")
  })

  it("retries a failed catalog load", async () => {
    agentCenterApi.getAgentCatalog.mockRejectedValueOnce(new Error("目录不可用"))
    render(<AgentCenterPage />)

    expect(await screen.findByText("目录不可用")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(await screen.findByTestId("agent-center-catalog")).toBeInTheDocument()
    expect(agentCenterApi.getAgentCatalog).toHaveBeenCalledTimes(2)
  })

  it("renders the runs empty state", async () => {
    agentCenterApi.listAgentRuns.mockResolvedValue({ items: [], nextCursor: "", hasMore: false })
    render(<AgentCenterPage initialTab="runs" />)
    expect(await screen.findByTestId("agent-center-runs-empty")).toHaveTextContent(
      "尚无运行记录",
    )
  })
})
