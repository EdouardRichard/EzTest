"use client"

import * as Tabs from "@radix-ui/react-tabs"
import { Activity, Bot, Gauge, RefreshCw } from "lucide-react"
import { useEffect, useMemo, useState } from "react"

import { EmptyState } from "@/components/ui/EmptyState"
import { StatusBadge, type StatusBadgeVariant } from "@/components/ui/StatusBadge"
import { useAuth } from "@/features/auth/AuthProvider"
import { ApiError } from "@/lib/api-client"
import { AgentConsole } from "./AgentConsole"
import type {
  AgentCenterCatalog,
  AgentCenterEval,
  AgentCenterRole,
  AgentCenterRoleMode,
  AgentCenterSession,
  AgentCenterSessionStatus,
} from "./agent-api"
import { getAgentCenterCatalog, getAgentCenterQuality, listAgentCenterSessions } from "./agent-api"
import { ReadonlyBanner } from "./ReadonlyBanner"

const SESSION_LIMIT_OPTIONS = [20, 50, 100] as const

const STATUS_OPTIONS: { value: AgentCenterSessionStatus; label: string }[] = [
  { value: "running", label: "运行中" },
  { value: "idle", label: "空闲" },
  { value: "done", label: "完成" },
  { value: "failed", label: "失败" },
  { value: "aborted", label: "已中止" },
  { value: "interrupted", label: "已中断" },
]

const STATUS_LABELS: Record<AgentCenterSessionStatus, string> = {
  running: "运行中",
  idle: "空闲",
  done: "完成",
  failed: "失败",
  aborted: "已中止",
  interrupted: "已中断",
}

const STATUS_VARIANTS: Record<AgentCenterSessionStatus, StatusBadgeVariant> = {
  running: "info",
  idle: "neutral",
  done: "success",
  failed: "danger",
  aborted: "warning",
  interrupted: "warning",
}

/** Human labels of the five deterministic evaluation metrics (spec §9.1). */
const METRIC_LABELS: Record<string, string> = {
  design_coverage: "设计覆盖率",
  impact_recall: "影响命中率",
  impact_precision: "影响精确率",
  classification_accuracy: "失败分类准确率",
  issue_false_positive_rate: "问题误报率",
  supplement_hit_rate: "补充命中率",
}

const TAB_TRIGGER_CLASS =
  "rounded-lg px-3 py-1.5 text-sm font-medium text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring data-[state=active]:bg-card data-[state=active]:text-foreground data-[state=active]:shadow-sm"

/** Reads `?session=` so an admin deep link opens the console tab directly. */
function hasSessionParam(): boolean {
  return new URLSearchParams(window.location.search).has("session")
}

function describeLoadError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 0) return "无法连接 API 服务，请确认后端已启动。"
    return error.message
  }
  return error instanceof Error ? error.message : "未知错误"
}

/** Renders an evaluation ratio as a one-decimal percentage. */
function formatPercent(value: number): string {
  return `${(value * 100).toFixed(1)}%`
}

function formatTime(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString("zh-CN", { hour12: false })
}

function modeLabel(mode: AgentCenterRoleMode): string {
  switch (mode) {
    case "deterministic":
      return "确定性生成"
    case "manual_review":
      return "人工审核"
    default:
      return "Agent"
  }
}

/** The role name for a session row, falling back to the raw profile key. */
function roleLabel(roles: AgentCenterRole[], role: string): string {
  return roles.find((item) => item.role === role)?.name ?? role
}

/**
 * The P8 Agent centre (/agents): the 13 logical roles, the read-only run
 * observation and the latest quality metrics. Administrators additionally reach
 * the existing P1 configuration/console tab and see edit/open actions; regular
 * users get the same data without any write control (spec §8.1/D60).
 */
export function AgentCenterPage() {
  const { user } = useAuth()
  const isAdmin = user?.role === "admin"

  const [catalog, setCatalog] = useState<AgentCenterCatalog | null>(null)
  const [quality, setQuality] = useState<AgentCenterEval | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState("")
  const [qualityError, setQualityError] = useState("")
  const [reloadToken, setReloadToken] = useState(0)

  const [sessions, setSessions] = useState<AgentCenterSession[]>([])
  const [sessionsLoading, setSessionsLoading] = useState(true)
  const [sessionsError, setSessionsError] = useState("")
  const [roleFilter, setRoleFilter] = useState("")
  const [statusFilter, setStatusFilter] = useState("")
  const [limitFilter, setLimitFilter] = useState<number>(SESSION_LIMIT_OPTIONS[0])

  // A deep link from the run observation opens the existing console tab; the
  // console itself reads `?session=` on mount.
  const [tab, setTab] = useState<string>(() =>
    isAdmin && hasSessionParam() ? "console" : "overview",
  )

  useEffect(() => {
    // Reading the retry token marks this effect as reload-driven.
    void reloadToken
    let cancelled = false

    async function load() {
      setLoading(true)
      setError("")
      try {
        const loaded = await getAgentCenterCatalog()
        if (!cancelled) setCatalog(loaded)
      } catch (caught) {
        if (!cancelled) setError(describeLoadError(caught))
      } finally {
        if (!cancelled) setLoading(false)
      }
    }

    void load()
    return () => {
      cancelled = true
    }
  }, [reloadToken])

  // Quality is a separate read: an evaluation outage must not hide the role
  // catalogue, so its failure only affects the quality card.
  useEffect(() => {
    // Reading the retry token marks this effect as reload-driven.
    void reloadToken
    let cancelled = false

    async function load() {
      setQualityError("")
      try {
        const loaded = await getAgentCenterQuality()
        if (!cancelled) setQuality(loaded.latest)
      } catch (caught) {
        if (!cancelled) setQualityError(describeLoadError(caught))
      }
    }

    void load()
    return () => {
      cancelled = true
    }
  }, [reloadToken])

  useEffect(() => {
    // Reading the retry token marks this effect as reload-driven.
    void reloadToken
    let cancelled = false

    async function load() {
      setSessionsLoading(true)
      setSessionsError("")
      try {
        const page = await listAgentCenterSessions({
          role: roleFilter || undefined,
          status: statusFilter || undefined,
          limit: limitFilter,
        })
        if (!cancelled) setSessions(Array.isArray(page.items) ? page.items : [])
      } catch (caught) {
        if (!cancelled) setSessionsError(describeLoadError(caught))
      } finally {
        if (!cancelled) setSessionsLoading(false)
      }
    }

    void load()
    return () => {
      cancelled = true
    }
  }, [roleFilter, statusFilter, limitFilter, reloadToken])

  const roles = catalog?.roles ?? []
  const summary = useMemo(() => {
    let enabledProfiles = 0
    let runCount = 0
    let succeeded = 0
    let failed = 0
    for (const role of roles) {
      if (role.enabled) enabledProfiles += 1
      runCount += role.stats.run_count
      succeeded += role.stats.succeeded_count
      failed += role.stats.failed_count
    }
    const terminal = succeeded + failed
    return {
      enabledProfiles,
      runCount,
      successRate: terminal === 0 ? null : succeeded / terminal,
    }
  }, [roles])

  function retry() {
    setReloadToken((token) => token + 1)
  }

  function openSession(id: number) {
    const url = new URL(window.location.href)
    url.searchParams.set("session", String(id))
    window.history.replaceState(null, "", `${url.pathname}${url.search}${url.hash}`)
    setTab("console")
  }

  const header = (
    <header className="flex flex-wrap items-start justify-between gap-3">
      <div>
        <div className="flex items-center gap-2">
          <h1 className="text-2xl font-semibold tracking-tight">Agent 中心</h1>
          <StatusBadge variant="info">P8</StatusBadge>
        </div>
        <p className="mt-1 text-sm text-muted-foreground">
          13 个逻辑角色的配置与运行观察、质量指标。管理员可调整配置，普通用户只读。
        </p>
      </div>
    </header>
  )

  if (loading) {
    return (
      <div className="space-y-6">
        {header}
        {isAdmin ? null : <ReadonlyBanner />}
        <p role="status" className="text-sm text-muted-foreground">
          正在加载 Agent 中心…
        </p>
      </div>
    )
  }

  if (error) {
    return (
      <div className="space-y-6">
        {header}
        {isAdmin ? null : <ReadonlyBanner />}
        <div
          role="alert"
          className="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
        >
          <span>Agent 中心加载失败：{error}</span>
          <button
            type="button"
            onClick={retry}
            className="inline-flex items-center gap-1.5 rounded-lg border border-destructive/40 px-2.5 py-1 font-medium focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <RefreshCw className="h-3.5 w-3.5" aria-hidden="true" />
            重试
          </button>
        </div>
      </div>
    )
  }

  return (
    <div className="space-y-6" data-testid="agent-center-page">
      {header}
      {isAdmin ? null : <ReadonlyBanner />}

      <Tabs.Root value={tab} onValueChange={setTab}>
        <Tabs.List
          aria-label="Agent 中心视图"
          className="inline-flex gap-1 rounded-xl bg-muted p-1"
        >
          <Tabs.Trigger value="overview" className={TAB_TRIGGER_CLASS}>
            角色目录
          </Tabs.Trigger>
          <Tabs.Trigger value="sessions" className={TAB_TRIGGER_CLASS}>
            运行观察
          </Tabs.Trigger>
          {isAdmin ? (
            <Tabs.Trigger value="console" className={TAB_TRIGGER_CLASS}>
              配置与控制台
            </Tabs.Trigger>
          ) : null}
        </Tabs.List>

        <Tabs.Content value="overview" className="space-y-4 pt-3">
          <section aria-label="Agent 中心概览" className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
            <SummaryCard label="逻辑角色" value={String(roles.length)} />
            <SummaryCard label="已启用 profile" value={String(summary.enabledProfiles)} />
            <SummaryCard label="运行总数" value={String(summary.runCount)} />
            <SummaryCard
              label="总体成功率"
              value={summary.successRate === null ? "—" : formatPercent(summary.successRate)}
            />
          </section>

          <section
            aria-label="质量指标"
            data-testid="agent-center-quality"
            className="rounded-2xl border border-border bg-card p-5 shadow-sm"
          >
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div className="flex items-center gap-2">
                <Gauge className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
                <h2 className="text-base font-semibold">质量指标</h2>
              </div>
              {quality === null ? null : (
                <StatusBadge variant={quality.passed ? "success" : "danger"}>
                  {quality.passed ? "通过" : "未通过"}
                </StatusBadge>
              )}
            </div>
            {qualityError ? (
              <p role="alert" className="mt-3 text-sm text-destructive">
                质量指标加载失败：{qualityError}
              </p>
            ) : quality === null ? (
              <p data-testid="agent-quality-empty" className="mt-3 text-sm text-muted-foreground">
                尚未运行评测
              </p>
            ) : (
              <ul className="mt-3 grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
                {Object.entries(quality.metrics).map(([key, value]) => (
                  <li
                    key={key}
                    className="flex items-center justify-between rounded-xl border border-border bg-background px-3 py-2 text-sm"
                  >
                    <span className="text-muted-foreground">{METRIC_LABELS[key] ?? key}</span>
                    <span className="font-semibold tabular-nums">{formatPercent(value)}</span>
                  </li>
                ))}
              </ul>
            )}
          </section>

          <section aria-label="13 角色目录" className="space-y-3">
            <div className="flex flex-wrap items-start justify-between gap-3">
              <div>
                <h2 className="text-base font-semibold">13 角色目录</h2>
                <p className="mt-1 text-sm text-muted-foreground">
                  11 个角色由 Agent profile 驱动；报告整理与知识沉淀没有独立
                  profile，按确定性/人工流程标注。
                </p>
              </div>
            </div>

            {roles.length === 0 ? (
              <EmptyState
                icon={Bot}
                title="暂无 Agent 角色"
                description="服务端没有返回 Agent 角色目录，请稍后重试。"
              />
            ) : (
              <div className="overflow-x-auto rounded-2xl border border-border bg-card shadow-sm">
                <table className="w-full min-w-[880px] text-left text-sm">
                  <caption className="sr-only">Agent 逻辑角色目录</caption>
                  <thead className="border-b border-border bg-muted/50 text-xs text-muted-foreground">
                    <tr>
                      <th scope="col" className="px-3 py-2 font-medium">
                        #
                      </th>
                      <th scope="col" className="px-3 py-2 font-medium">
                        角色
                      </th>
                      <th scope="col" className="px-3 py-2 font-medium">
                        阶段
                      </th>
                      <th scope="col" className="px-3 py-2 font-medium">
                        Profile
                      </th>
                      <th scope="col" className="px-3 py-2 font-medium">
                        状态
                      </th>
                      <th scope="col" className="px-3 py-2 font-medium">
                        模型
                      </th>
                      <th scope="col" className="px-3 py-2 font-medium">
                        工具
                      </th>
                      <th scope="col" className="px-3 py-2 font-medium">
                        运行
                      </th>
                      <th scope="col" className="px-3 py-2 font-medium">
                        成功率
                      </th>
                      {isAdmin ? (
                        <th scope="col" className="px-3 py-2 font-medium">
                          操作
                        </th>
                      ) : null}
                    </tr>
                  </thead>
                  <tbody>
                    {roles.map((role) => (
                      <tr
                        key={role.role}
                        data-testid="agent-role-row"
                        className="border-b border-border/60 last:border-0"
                      >
                        <td className="px-3 py-2 tabular-nums text-muted-foreground">
                          {role.order}
                        </td>
                        <td className="px-3 py-2">
                          <span className="font-medium">{role.name}</span>
                          {role.note ? (
                            <span className="block text-xs text-muted-foreground">{role.note}</span>
                          ) : null}
                        </td>
                        <td className="px-3 py-2 text-muted-foreground">{role.phase}</td>
                        <td className="px-3 py-2">
                          {role.profile_key === null ? (
                            <span className="text-muted-foreground">无独立 profile</span>
                          ) : (
                            <span className="break-all font-mono text-xs">{role.profile_key}</span>
                          )}
                        </td>
                        <td className="px-3 py-2">
                          {role.profile_key === null ? (
                            <StatusBadge variant="neutral" dot={false}>
                              {modeLabel(role.mode)}
                            </StatusBadge>
                          ) : (
                            <StatusBadge variant={role.enabled ? "success" : "warning"}>
                              {role.enabled ? "已启用" : "未启用"}
                            </StatusBadge>
                          )}
                        </td>
                        <td className="px-3 py-2 text-muted-foreground">
                          {role.model === "" ? "默认模型" : role.model}
                        </td>
                        <td className="px-3 py-2 tabular-nums text-muted-foreground">
                          {role.allowed_tools.length}
                        </td>
                        <td className="px-3 py-2 tabular-nums">{role.stats.run_count}</td>
                        <td className="px-3 py-2 tabular-nums">
                          {role.stats.run_count === 0
                            ? "—"
                            : formatPercent(role.stats.success_rate)}
                        </td>
                        {isAdmin ? (
                          <td className="px-3 py-2">
                            {role.profile_key === null ? (
                              <span className="text-xs text-muted-foreground">—</span>
                            ) : (
                              <button
                                type="button"
                                aria-label={`编辑 ${role.name} 配置`}
                                onClick={() => setTab("console")}
                                className="rounded-lg border border-border px-2 py-1 text-xs font-medium transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                              >
                                编辑
                              </button>
                            )}
                          </td>
                        ) : null}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </section>
        </Tabs.Content>

        <Tabs.Content value="sessions" className="space-y-3 pt-3">
          <div className="flex flex-wrap items-end gap-3">
            <label className="text-xs font-medium text-muted-foreground">
              <span className="mb-1 block">角色</span>
              <select
                aria-label="按角色筛选"
                value={roleFilter}
                onChange={(event) => setRoleFilter(event.target.value)}
                className="rounded-lg border border-border bg-card px-2.5 py-1.5 text-sm text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                <option value="">全部角色</option>
                {roles.map((role) => (
                  <option key={role.role} value={role.role}>
                    {role.name}
                  </option>
                ))}
              </select>
            </label>

            <label className="text-xs font-medium text-muted-foreground">
              <span className="mb-1 block">状态</span>
              <select
                aria-label="按状态筛选"
                value={statusFilter}
                onChange={(event) => setStatusFilter(event.target.value)}
                className="rounded-lg border border-border bg-card px-2.5 py-1.5 text-sm text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                <option value="">全部状态</option>
                {STATUS_OPTIONS.map((option) => (
                  <option key={option.value} value={option.value}>
                    {option.label}
                  </option>
                ))}
              </select>
            </label>

            <label className="text-xs font-medium text-muted-foreground">
              <span className="mb-1 block">数量</span>
              <select
                aria-label="运行记录数量"
                value={String(limitFilter)}
                onChange={(event) => setLimitFilter(Number(event.target.value))}
                className="rounded-lg border border-border bg-card px-2.5 py-1.5 text-sm text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                {SESSION_LIMIT_OPTIONS.map((option) => (
                  <option key={option} value={String(option)}>
                    {option}
                  </option>
                ))}
              </select>
            </label>
          </div>

          {sessionsError ? (
            <div
              role="alert"
              className="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
            >
              <span>运行记录加载失败：{sessionsError}</span>
              <button
                type="button"
                onClick={retry}
                className="rounded-lg border border-destructive/40 px-2.5 py-1 font-medium focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                重试
              </button>
            </div>
          ) : null}

          {sessionsLoading ? (
            <p className="text-sm text-muted-foreground">正在加载运行记录…</p>
          ) : sessions.length === 0 ? (
            <EmptyState
              icon={Activity}
              title="暂无运行记录"
              description="当前筛选条件下没有 Agent 会话运行记录。"
            />
          ) : (
            <div className="overflow-x-auto rounded-2xl border border-border bg-card shadow-sm">
              <table className="w-full min-w-[680px] text-left text-sm">
                <caption className="sr-only">Agent 运行观察</caption>
                <thead className="border-b border-border bg-muted/50 text-xs text-muted-foreground">
                  <tr>
                    <th scope="col" className="px-3 py-2 font-medium">
                      会话
                    </th>
                    <th scope="col" className="px-3 py-2 font-medium">
                      角色
                    </th>
                    <th scope="col" className="px-3 py-2 font-medium">
                      状态
                    </th>
                    <th scope="col" className="px-3 py-2 font-medium">
                      最近活动
                    </th>
                    {isAdmin ? (
                      <th scope="col" className="px-3 py-2 font-medium">
                        操作
                      </th>
                    ) : null}
                  </tr>
                </thead>
                <tbody>
                  {sessions.map((session) => (
                    <tr
                      key={session.id}
                      data-testid="agent-center-session-row"
                      className="border-b border-border/60 last:border-0"
                    >
                      <td className="px-3 py-2 font-medium">
                        {session.title === "" ? "未命名会话" : session.title}
                      </td>
                      <td className="px-3 py-2 text-muted-foreground">
                        {roleLabel(roles, session.role)}
                      </td>
                      <td className="px-3 py-2">
                        <StatusBadge variant={STATUS_VARIANTS[session.status] ?? "neutral"}>
                          {STATUS_LABELS[session.status] ?? session.status}
                        </StatusBadge>
                      </td>
                      <td className="px-3 py-2 text-muted-foreground">
                        {formatTime(session.last_activity_at)}
                      </td>
                      {isAdmin ? (
                        <td className="px-3 py-2">
                          <button
                            type="button"
                            onClick={() => openSession(session.id)}
                            className="rounded-lg border border-border px-2 py-1 text-xs font-medium transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                          >
                            打开控制台
                          </button>
                        </td>
                      ) : null}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Tabs.Content>

        {isAdmin ? (
          <Tabs.Content value="console" className="pt-3">
            <AgentConsole />
          </Tabs.Content>
        ) : null}
      </Tabs.Root>
    </div>
  )
}

/** One overview metric card. */
function SummaryCard({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-2xl border border-border bg-card px-4 py-3 shadow-sm">
      <p className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
        {label}
      </p>
      <p className="mt-1 text-2xl font-semibold tabular-nums">{value}</p>
    </div>
  )
}
