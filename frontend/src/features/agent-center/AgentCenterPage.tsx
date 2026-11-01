"use client"

import { useRouter } from "next/navigation"
import { useCallback, useEffect, useState } from "react"

import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState } from "@/components/ui/ErrorState"
import { Select } from "@/components/ui/Select"
import { LoadingCard } from "@/components/ui/Spinner"
import { Tabs } from "@/components/ui/Tabs"
import { useIsAdmin } from "@/features/auth/useIsAdmin"
import { formatDateTime } from "@/lib/format"

import {
  getAgentCatalog,
  getAgentCenterQuality,
  listAgentProfiles,
  listAgentRuns,
  listAgentTools,
  listLLMModels,
} from "./api"
import { ProfileEditor } from "./ProfileEditor"
import {
  AGENT_RUN_STATUS_LABEL,
  AGENT_RUN_STATUS_TONE,
  type AgentCatalog,
  type AgentCenterQuality,
  type AgentRunStatus,
  type AgentRunView,
  type AgentTool,
  type CatalogProfile,
  EVAL_STATUS_LABEL,
  EVAL_STATUS_TONE,
  formatRate,
  formatUsage,
  type LLMModel,
  type ProfileView,
} from "./types"

export type AgentCenterPageProps = {
  /** Deep link `?tab=` from the agents route. */
  initialTab?: string
  /** Deep link `?profile=` for the admin config drawer. */
  initialProfile?: string
}

type AgentCenterTab = "catalog" | "runs" | "quality" | "config"

const TAB_VALUES: AgentCenterTab[] = ["catalog", "runs", "quality", "config"]
const RUN_PAGE_LIMIT = 20

/**
 * P6 Agent center: read-only catalog/runs/quality for every logged-in user and
 * an admin-only configuration tab with the prompt version editor. Prompts and
 * transcripts never appear in the read models; normal users never request the
 * admin endpoints.
 */
export function AgentCenterPage({ initialTab, initialProfile }: AgentCenterPageProps) {
  const router = useRouter()
  const isAdmin = useIsAdmin()

  const [tab, setTab] = useState<AgentCenterTab>(() => resolveInitialTab(initialTab, isAdmin))
  const [selectedProfile, setSelectedProfile] = useState<string | null>(() =>
    isAdmin && resolveInitialTab(initialTab, isAdmin) === "config"
      ? (initialProfile ?? null)
      : null,
  )

  const [catalog, setCatalog] = useState<AgentCatalog | null>(null)
  const [catalogLoading, setCatalogLoading] = useState(true)
  const [catalogError, setCatalogError] = useState<unknown>(null)

  const [runs, setRuns] = useState<AgentRunView[] | null>(null)
  const [runsLoading, setRunsLoading] = useState(false)
  const [runsError, setRunsError] = useState<unknown>(null)
  const [runProfileFilter, setRunProfileFilter] = useState("")
  const [runStatusFilter, setRunStatusFilter] = useState("")
  const [runsHasMore, setRunsHasMore] = useState(false)
  const [runsCursor, setRunsCursor] = useState("")
  const [runsLoadingMore, setRunsLoadingMore] = useState(false)

  const [quality, setQuality] = useState<AgentCenterQuality | null>(null)
  const [qualityLoading, setQualityLoading] = useState(false)
  const [qualityError, setQualityError] = useState<unknown>(null)

  const [profiles, setProfiles] = useState<ProfileView[] | null>(null)
  const [profilesLoading, setProfilesLoading] = useState(false)
  const [profilesError, setProfilesError] = useState<unknown>(null)
  const [tools, setTools] = useState<AgentTool[]>([])
  const [models, setModels] = useState<LLMModel[]>([])

  const [reloadToken, setReloadToken] = useState(0)

  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadToken re-runs the explicit refetch
  useEffect(() => {
    let cancelled = false
    setCatalogLoading(true)
    setCatalogError(null)
    void getAgentCatalog()
      .then((next) => {
        if (!cancelled) setCatalog(next)
      })
      .catch((cause: unknown) => {
        if (!cancelled) {
          setCatalog(null)
          setCatalogError(cause)
        }
      })
      .finally(() => {
        if (!cancelled) setCatalogLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [reloadToken])

  // biome-ignore lint/correctness/useExhaustiveDependencies: filters/reload re-run the explicit refetch
  useEffect(() => {
    if (tab !== "runs") return undefined
    let cancelled = false
    setRunsLoading(true)
    setRunsError(null)
    void listAgentRuns({
      profileKey: runProfileFilter.length > 0 ? runProfileFilter : undefined,
      status: runStatusFilter.length > 0 ? runStatusFilter : undefined,
      limit: RUN_PAGE_LIMIT,
    })
      .then((page) => {
        if (cancelled) return
        setRuns(page.items)
        setRunsHasMore(page.hasMore)
        setRunsCursor(page.nextCursor)
      })
      .catch((cause: unknown) => {
        if (!cancelled) {
          setRuns(null)
          setRunsError(cause)
        }
      })
      .finally(() => {
        if (!cancelled) setRunsLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [tab, runProfileFilter, runStatusFilter, reloadToken])

  // biome-ignore lint/correctness/useExhaustiveDependencies: reload re-runs the explicit refetch
  useEffect(() => {
    if (tab !== "quality") return undefined
    let cancelled = false
    setQualityLoading(true)
    setQualityError(null)
    void getAgentCenterQuality()
      .then((next) => {
        if (!cancelled) setQuality(next)
      })
      .catch((cause: unknown) => {
        if (!cancelled) {
          setQuality(null)
          setQualityError(cause)
        }
      })
      .finally(() => {
        if (!cancelled) setQualityLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [tab, reloadToken])

  // biome-ignore lint/correctness/useExhaustiveDependencies: reload re-runs the explicit refetch
  useEffect(() => {
    if (tab !== "config" || !isAdmin) return undefined
    let cancelled = false
    setProfilesLoading(true)
    setProfilesError(null)
    void listAgentProfiles()
      .then((next) => {
        if (!cancelled) setProfiles(next)
      })
      .catch((cause: unknown) => {
        if (!cancelled) {
          setProfiles(null)
          setProfilesError(cause)
        }
      })
      .finally(() => {
        if (!cancelled) setProfilesLoading(false)
      })
    void listAgentTools()
      .then((next) => {
        if (!cancelled) setTools(next)
      })
      .catch(() => {
        if (!cancelled) setTools([])
      })
    void listLLMModels()
      .then((next) => {
        if (!cancelled) setModels(next)
      })
      .catch(() => {
        if (!cancelled) setModels([])
      })
    return () => {
      cancelled = true
    }
  }, [tab, isAdmin, reloadToken])

  function syncLocation(next: AgentCenterTab, profileKey?: string | null): void {
    const params = new URLSearchParams({ tab: next })
    if (next === "config" && profileKey !== undefined && profileKey !== null) {
      params.set("profile", profileKey)
    }
    router.replace(`/agents?${params.toString()}`)
  }

  function changeTab(next: AgentCenterTab): void {
    setTab(next)
    syncLocation(next, next === "config" ? selectedProfile : null)
  }

  function openEditor(profileKey: string): void {
    setSelectedProfile(profileKey)
    syncLocation("config", profileKey)
  }

  function closeEditor(): void {
    setSelectedProfile(null)
    syncLocation("config")
  }

  function changeRunStatus(next: string): void {
    setRunStatusFilter(next)
    syncLocation("runs")
  }

  function changeRunProfile(next: string): void {
    setRunProfileFilter(next)
    syncLocation("runs")
  }

  const refresh = useCallback(() => setReloadToken((token) => token + 1), [])

  async function loadMoreRuns(): Promise<void> {
    if (runsCursor.length === 0) return
    setRunsLoadingMore(true)
    try {
      const page = await listAgentRuns({
        profileKey: runProfileFilter.length > 0 ? runProfileFilter : undefined,
        status: runStatusFilter.length > 0 ? runStatusFilter : undefined,
        before: runsCursor,
        limit: RUN_PAGE_LIMIT,
      })
      setRuns((current) => [...(current ?? []), ...page.items])
      setRunsHasMore(page.hasMore)
      setRunsCursor(page.nextCursor)
    } catch (cause) {
      setRunsError(cause)
    } finally {
      setRunsLoadingMore(false)
    }
  }

  const tabs = [
    { value: "catalog", label: "目录" },
    { value: "runs", label: "运行观察" },
    { value: "quality", label: "质量" },
    ...(isAdmin ? [{ value: "config", label: "配置" }] : []),
  ]

  return (
    <div data-testid="agent-center-page" className="space-y-5">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <p className="text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">
            全局工作区
          </p>
          <div className="mt-1 flex items-center gap-2">
            <h1 className="text-xl font-semibold text-foreground">Agent 中心</h1>
            {isAdmin ? <Badge tone="info">管理员</Badge> : null}
          </div>
          <p className="mt-1 max-w-2xl text-xs leading-relaxed text-muted-foreground">
            14 个固定 Agent profile 的目录、运行观察与质量投影。普通用户只读，且不会返回 prompt 或
            transcript；配置与提示词版本仅管理员可编辑。
          </p>
        </div>
        <Button variant="secondary" size="sm" onClick={refresh}>
          刷新
        </Button>
      </header>

      <Tabs
        ariaLabel="Agent 中心"
        items={tabs}
        value={tab}
        onValueChange={(next) => changeTab(next as AgentCenterTab)}
      />

      {tab === "catalog" ? (
        <CatalogPanel
          catalog={catalog}
          loading={catalogLoading}
          error={catalogError}
          onRetry={refresh}
        />
      ) : null}

      {tab === "runs" ? (
        <RunsPanel
          catalog={catalog}
          runs={runs}
          loading={runsLoading}
          error={runsError}
          profileFilter={runProfileFilter}
          statusFilter={runStatusFilter}
          hasMore={runsHasMore}
          loadingMore={runsLoadingMore}
          onProfileChange={changeRunProfile}
          onStatusChange={changeRunStatus}
          onRetry={refresh}
          onLoadMore={() => void loadMoreRuns()}
        />
      ) : null}

      {tab === "quality" ? (
        <QualityPanel
          quality={quality}
          loading={qualityLoading}
          error={qualityError}
          onRetry={refresh}
        />
      ) : null}

      {tab === "config" && isAdmin ? (
        <ConfigPanel
          profiles={profiles}
          loading={profilesLoading}
          error={profilesError}
          onRetry={refresh}
          onEdit={openEditor}
        />
      ) : null}

      {isAdmin && selectedProfile !== null ? (
        <ProfileEditor
          profileKey={selectedProfile}
          tools={tools}
          models={models}
          onClose={closeEditor}
          onSaved={refresh}
        />
      ) : null}
    </div>
  )
}

/* -------------------------------- catalog -------------------------------- */

function CatalogPanel({
  catalog,
  loading,
  error,
  onRetry,
}: {
  catalog: AgentCatalog | null
  loading: boolean
  error: unknown
  onRetry: () => void
}) {
  if (loading && catalog === null) return <LoadingCard label="正在加载 Agent 目录…" />
  if (error !== null) {
    return <ErrorState error={error} title="Agent 目录加载失败" onRetry={onRetry} />
  }
  if (catalog === null || catalog.profiles.length === 0) {
    return (
      <div data-testid="agent-center-catalog-empty">
        <EmptyState
          title="暂无 Agent profile"
          description="启动 seed 后会显示 14 个固定 profile。"
        />
      </div>
    )
  }
  const profileByKey = new Map(catalog.profiles.map((profile) => [profile.profile_key, profile]))
  return (
    <div data-testid="agent-center-catalog" className="space-y-4">
      {catalog.teams.map((team) => {
        const teamProfiles = team.profile_keys
          .map((key) => profileByKey.get(key))
          .filter((profile): profile is CatalogProfile => profile !== undefined)
        return (
          <section
            key={team.team}
            data-testid={`agent-center-team-${team.team}`}
            aria-label={team.label}
            className="surface-card space-y-2 p-4"
          >
            <header className="flex flex-wrap items-center justify-between gap-2">
              <h2 className="text-sm font-semibold text-foreground">{team.label}</h2>
              <span className="text-[11px] text-muted-foreground">
                {teamProfiles.length} 个 profile
              </span>
            </header>
            {teamProfiles.length === 0 ? (
              <p className="text-[11px] text-muted-foreground">
                {team.note ?? "该团队暂无 profile。"}
              </p>
            ) : (
              <div className="overflow-x-auto">
                <table className="w-full min-w-[860px] border-separate border-spacing-0 text-left text-xs">
                  <thead>
                    <tr className="text-[11px] uppercase tracking-wide text-muted-foreground">
                      <th className="border-b border-border px-2 py-1.5 font-medium">Profile</th>
                      <th className="border-b border-border px-2 py-1.5 font-medium">模型</th>
                      <th className="border-b border-border px-2 py-1.5 font-medium">预算</th>
                      <th className="border-b border-border px-2 py-1.5 font-medium">工具</th>
                      <th className="border-b border-border px-2 py-1.5 font-medium">提示词</th>
                      <th className="border-b border-border px-2 py-1.5 font-medium">运行统计</th>
                      <th className="border-b border-border px-2 py-1.5 font-medium">更新</th>
                    </tr>
                  </thead>
                  <tbody>
                    {teamProfiles.map((profile) => (
                      <tr
                        key={profile.profile_key}
                        data-testid={`agent-center-profile-${profile.profile_key}`}
                      >
                        <td className="border-b border-border px-2 py-1.5">
                          <p className="font-medium text-foreground">{profile.name}</p>
                          <p className="font-mono text-[10px] text-muted-foreground">
                            {profile.profile_key}
                          </p>
                        </td>
                        <td className="border-b border-border px-2 py-1.5 text-muted-foreground">
                          {profile.model ?? "默认"}
                        </td>
                        <td className="border-b border-border px-2 py-1.5 text-muted-foreground">
                          {profile.budgets.max_turns} 轮 · {profile.budgets.soft_seconds}s/
                          {profile.budgets.hard_seconds}s
                        </td>
                        <td className="border-b border-border px-2 py-1.5 text-muted-foreground">
                          {profile.allowed_tools_count}
                        </td>
                        <td className="border-b border-border px-2 py-1.5 text-muted-foreground">
                          v{profile.prompt_version}
                          {profile.prompt_updated_at !== null ? (
                            <span className="ml-1 text-[10px]">
                              {formatDateTime(profile.prompt_updated_at)}
                            </span>
                          ) : null}
                        </td>
                        <td className="border-b border-border px-2 py-1.5 text-muted-foreground">
                          {profile.stats.run_count} run · {formatRate(profile.stats.success_rate)}
                        </td>
                        <td className="border-b border-border px-2 py-1.5 text-muted-foreground">
                          {formatDateTime(profile.updated_at)}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </section>
        )
      })}
    </div>
  )
}

/* ---------------------------------- runs --------------------------------- */

function RunsPanel({
  catalog,
  runs,
  loading,
  error,
  profileFilter,
  statusFilter,
  hasMore,
  loadingMore,
  onProfileChange,
  onStatusChange,
  onRetry,
  onLoadMore,
}: {
  catalog: AgentCatalog | null
  runs: AgentRunView[] | null
  loading: boolean
  error: unknown
  profileFilter: string
  statusFilter: string
  hasMore: boolean
  loadingMore: boolean
  onProfileChange: (value: string) => void
  onStatusChange: (value: string) => void
  onRetry: () => void
  onLoadMore: () => void
}) {
  return (
    <section data-testid="agent-center-runs" className="surface-card space-y-3 p-4">
      <header className="flex flex-wrap items-end justify-between gap-2">
        <div>
          <h2 className="text-sm font-semibold text-foreground">运行观察</h2>
          <p className="mt-0.5 text-[11px] text-muted-foreground">
            仅展示 run 元数据；不返回 message/transcript。
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Select
            data-testid="agent-center-run-filter-profile"
            aria-label="profile"
            value={profileFilter}
            onChange={(event) => onProfileChange(event.target.value)}
            className="h-8 w-48 text-xs"
          >
            <option value="">全部 profile</option>
            {(catalog?.profiles ?? []).map((profile) => (
              <option key={profile.profile_key} value={profile.profile_key}>
                {profile.name}
              </option>
            ))}
          </Select>
          <Select
            data-testid="agent-center-run-filter-status"
            aria-label="status"
            value={statusFilter}
            onChange={(event) => onStatusChange(event.target.value)}
            className="h-8 w-36 text-xs"
          >
            <option value="">全部状态</option>
            {(Object.keys(AGENT_RUN_STATUS_LABEL) as AgentRunStatus[]).map((status) => (
              <option key={status} value={status}>
                {AGENT_RUN_STATUS_LABEL[status]}
              </option>
            ))}
          </Select>
          <Button variant="secondary" size="sm" onClick={onRetry}>
            刷新
          </Button>
        </div>
      </header>

      {loading && runs === null ? (
        <LoadingCard label="正在加载 Agent 运行…" />
      ) : error !== null ? (
        <ErrorState error={error} title="Agent 运行加载失败" onRetry={onRetry} />
      ) : runs === null || runs.length === 0 ? (
        <div data-testid="agent-center-runs-empty">
          <EmptyState
            title="尚无 Agent 运行记录"
            description="Agent 开始处理 work item 后会出现在这里。"
          />
        </div>
      ) : (
        <>
          <div className="overflow-x-auto">
            <table className="w-full min-w-[980px] border-separate border-spacing-0 text-left text-xs">
              <thead>
                <tr className="text-[11px] uppercase tracking-wide text-muted-foreground">
                  <th className="border-b border-border px-2 py-1.5 font-medium">Run</th>
                  <th className="border-b border-border px-2 py-1.5 font-medium">Profile</th>
                  <th className="border-b border-border px-2 py-1.5 font-medium">版本</th>
                  <th className="border-b border-border px-2 py-1.5 font-medium">状态</th>
                  <th className="border-b border-border px-2 py-1.5 font-medium">Turns</th>
                  <th className="border-b border-border px-2 py-1.5 font-medium">开始</th>
                  <th className="border-b border-border px-2 py-1.5 font-medium">结束</th>
                  <th className="border-b border-border px-2 py-1.5 font-medium">终态原因</th>
                  <th className="border-b border-border px-2 py-1.5 font-medium">Usage</th>
                </tr>
              </thead>
              <tbody>
                {runs.map((run) => (
                  <tr key={run.id} data-testid={`agent-center-run-${run.id}`}>
                    <td className="border-b border-border px-2 py-1.5 font-mono text-foreground">
                      #{run.id}
                    </td>
                    <td className="border-b border-border px-2 py-1.5 text-foreground">
                      {run.profile_key}
                      {run.work_type !== null ? (
                        <span className="block text-[10px] text-muted-foreground">
                          {run.work_type}
                        </span>
                      ) : null}
                    </td>
                    <td className="border-b border-border px-2 py-1.5 text-muted-foreground">
                      {run.version_id === null
                        ? "—"
                        : run.version_title !== undefined && run.version_title.length > 0
                          ? `${run.version_title} (#${run.version_id})`
                          : `#${run.version_id}`}
                    </td>
                    <td className="border-b border-border px-2 py-1.5">
                      <Badge tone={AGENT_RUN_STATUS_TONE[run.status]}>
                        {AGENT_RUN_STATUS_LABEL[run.status] ?? run.status}
                      </Badge>
                    </td>
                    <td className="border-b border-border px-2 py-1.5 text-muted-foreground">
                      {run.turns}
                    </td>
                    <td className="border-b border-border px-2 py-1.5 text-muted-foreground">
                      {formatDateTime(run.started_at)}
                    </td>
                    <td className="border-b border-border px-2 py-1.5 text-muted-foreground">
                      {formatDateTime(run.finished_at)}
                    </td>
                    <td className="border-b border-border px-2 py-1.5 text-muted-foreground">
                      {run.terminal_reason ?? "—"}
                    </td>
                    <td className="border-b border-border px-2 py-1.5 text-muted-foreground">
                      {formatUsage(run.usage)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {hasMore ? (
            <div className="flex justify-center">
              <Button variant="secondary" size="sm" disabled={loadingMore} onClick={onLoadMore}>
                {loadingMore ? "加载中…" : "加载更多"}
              </Button>
            </div>
          ) : null}
        </>
      )}
    </section>
  )
}

/* --------------------------------- quality ------------------------------- */

function QualityPanel({
  quality,
  loading,
  error,
  onRetry,
}: {
  quality: AgentCenterQuality | null
  loading: boolean
  error: unknown
  onRetry: () => void
}) {
  if (loading && quality === null) return <LoadingCard label="正在加载 Agent 质量…" />
  if (error !== null) {
    return <ErrorState error={error} title="Agent 质量加载失败" onRetry={onRetry} />
  }
  if (quality === null) return null
  return (
    <div data-testid="agent-center-quality" className="grid grid-cols-1 gap-4 lg:grid-cols-2">
      <section className="surface-card space-y-2 p-4" aria-label="最新评测">
        <h2 className="text-sm font-semibold text-foreground">最新评测</h2>
        {quality.latest === null ? (
          <div data-testid="agent-center-quality-empty">
            <EmptyState
              title="尚未运行评测"
              description="真实 LLM 评测 run 产生后会显示最新摘要。"
            />
          </div>
        ) : (
          <div data-testid="agent-center-quality-latest" className="space-y-2">
            <div className="flex flex-wrap items-center gap-2">
              <span className="font-mono text-sm text-foreground">#{quality.latest.id}</span>
              <Badge tone={EVAL_STATUS_TONE[quality.latest.status]}>
                {EVAL_STATUS_LABEL[quality.latest.status]}
              </Badge>
              <span className="text-[11px] text-muted-foreground">
                {quality.latest.metrics_passed}/{quality.latest.metrics_count} 指标通过
              </span>
            </div>
            <p className="text-[11px] text-muted-foreground">
              生成于 {formatDateTime(quality.latest.created_at)} · {quality.latest.mode}
            </p>
          </div>
        )}
      </section>

      <section className="surface-card space-y-2 p-4" aria-label="运行统计">
        <h2 className="text-sm font-semibold text-foreground">运行统计</h2>
        <dl
          data-testid="agent-center-quality-stats"
          className="grid grid-cols-2 gap-2 sm:grid-cols-3"
        >
          <Stat label="运行" value={String(quality.run_stats.run_count)} />
          <Stat label="完成" value={String(quality.run_stats.done_count)} />
          <Stat label="失败" value={String(quality.run_stats.failed_count)} />
          <Stat label="中止" value={String(quality.run_stats.aborted_count)} />
          <Stat label="中断" value={String(quality.run_stats.interrupted_count)} />
          <Stat label="成功率" value={formatRate(quality.run_stats.success_rate)} />
        </dl>
        <p className="text-[11px] text-muted-foreground">
          最近运行：{formatDateTime(quality.run_stats.last_run_at)}
        </p>
      </section>
    </div>
  )
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-lg border border-border bg-card px-3 py-2">
      <dt className="text-[11px] text-muted-foreground">{label}</dt>
      <dd className="mt-0.5 text-sm font-semibold text-foreground">{value}</dd>
    </div>
  )
}

/* --------------------------------- config -------------------------------- */

function ConfigPanel({
  profiles,
  loading,
  error,
  onRetry,
  onEdit,
}: {
  profiles: ProfileView[] | null
  loading: boolean
  error: unknown
  onRetry: () => void
  onEdit: (profileKey: string) => void
}) {
  return (
    <section data-testid="agent-center-config" className="surface-card space-y-3 p-4">
      <header className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h2 className="text-sm font-semibold text-foreground">配置</h2>
          <p className="mt-0.5 text-[11px] text-muted-foreground">
            仅管理员；修改 model/budgets/allowed_tools 与提示词版本，下一次 run 生效。
          </p>
        </div>
        <Button variant="secondary" size="sm" onClick={onRetry}>
          刷新
        </Button>
      </header>

      {loading && profiles === null ? (
        <LoadingCard label="正在加载 profile 配置…" />
      ) : error !== null ? (
        <ErrorState error={error} title="profile 配置加载失败" onRetry={onRetry} />
      ) : profiles === null || profiles.length === 0 ? (
        <EmptyState title="暂无 profile" description="启动 seed 后会显示 14 个固定 profile。" />
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full min-w-[900px] border-separate border-spacing-0 text-left text-xs">
            <thead>
              <tr className="text-[11px] uppercase tracking-wide text-muted-foreground">
                <th className="border-b border-border px-2 py-1.5 font-medium">Profile</th>
                <th className="border-b border-border px-2 py-1.5 font-medium">模型</th>
                <th className="border-b border-border px-2 py-1.5 font-medium">预算</th>
                <th className="border-b border-border px-2 py-1.5 font-medium">工具</th>
                <th className="border-b border-border px-2 py-1.5 font-medium">提示词</th>
                <th className="border-b border-border px-2 py-1.5 font-medium">更新</th>
                <th className="border-b border-border px-2 py-1.5 font-medium">操作</th>
              </tr>
            </thead>
            <tbody>
              {profiles.map((profile) => (
                <tr key={profile.key} data-testid={`agent-center-config-profile-${profile.key}`}>
                  <td className="border-b border-border px-2 py-1.5">
                    <p className="font-medium text-foreground">{profile.name}</p>
                    <p className="font-mono text-[10px] text-muted-foreground">{profile.key}</p>
                  </td>
                  <td className="border-b border-border px-2 py-1.5 text-muted-foreground">
                    {profile.model ?? "默认"}
                  </td>
                  <td className="border-b border-border px-2 py-1.5 text-muted-foreground">
                    {profile.budgets.max_turns} 轮
                  </td>
                  <td className="border-b border-border px-2 py-1.5 text-muted-foreground">
                    {profile.allowed_tools.length}
                  </td>
                  <td className="border-b border-border px-2 py-1.5 text-muted-foreground">
                    v{profile.prompt_version}
                  </td>
                  <td className="border-b border-border px-2 py-1.5 text-muted-foreground">
                    {formatDateTime(profile.updated_at)}
                  </td>
                  <td className="border-b border-border px-2 py-1.5">
                    <Button
                      data-testid={`agent-center-edit-${profile.key}`}
                      variant="secondary"
                      size="sm"
                      onClick={() => onEdit(profile.key)}
                    >
                      编辑
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  )
}

function resolveInitialTab(initial: string | undefined, isAdmin: boolean): AgentCenterTab {
  if (initial !== undefined && TAB_VALUES.includes(initial as AgentCenterTab)) {
    if (initial === "config" && !isAdmin) return "catalog"
    return initial as AgentCenterTab
  }
  return "catalog"
}
