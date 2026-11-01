"use client"

import { ArrowLeft, Database, FileCode2, Server } from "lucide-react"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { type ReactNode, useCallback, useEffect, useMemo, useRef, useState } from "react"

import { Badge } from "@/components/ui/Badge"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import { Tabs } from "@/components/ui/Tabs"
import { ChainTab } from "@/features/chain/ChainTab"
import { RequirementsTab } from "@/features/clarification/RequirementsTab"
import { useVersionEvents } from "@/features/clarification/useVersionEvents"
import { CodeTab } from "@/features/code-intel/CodeTab"
import { listIssues, listReports } from "@/features/delivery/api"
import { IssuesTab } from "@/features/delivery/IssuesTab"
import { ReportsTab } from "@/features/delivery/ReportsTab"
import { DesignTab } from "@/features/design/DesignTab"
import { ExecutionTab } from "@/features/execution/ExecutionTab"
import { TodoDrawer } from "@/features/shell/TodoDrawer"
import { formatDateTime } from "@/lib/format"

import { getVersion } from "./api"
import { parseVersionTab, VERSION_TABS } from "./tabs"
import type { Version, VersionBinding, VersionTab } from "./types"
import { VersionHeader } from "./VersionHeader"

/** SSE bursts (issue.created + report.published + version.closed) collapse into one refresh. */
const SSE_REFRESH_DEBOUNCE_MS = 350

type CountProps = {
  icon: ReactNode
  label: string
  value: string
}

function OverviewStat({ icon, label, value }: CountProps) {
  return (
    <div className="rounded-lg border border-border bg-card px-3 py-2.5">
      <dt className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
        <span className="text-muted-foreground" aria-hidden="true">
          {icon}
        </span>
        {label}
      </dt>
      <dd className="mt-0.5 text-sm font-semibold text-foreground">{value}</dd>
    </div>
  )
}

function BindingCard({ binding, index }: { binding: VersionBinding; index: number }) {
  const repositoryName = binding.repository?.name ?? "未绑定代码仓"
  return (
    <article className="surface-card p-4">
      <header className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <h2 className="text-sm font-semibold text-foreground">
            绑定 {index + 1} · {binding.sut.name}
          </h2>
          <p className="mt-0.5 text-[11px] text-muted-foreground">
            {binding.environment.name} · {binding.environment.base_url}
          </p>
        </div>
        <Badge tone={binding.environment.status === "active" ? "success" : "neutral"}>
          {binding.environment.status === "active" ? "环境启用" : "环境停用"}
        </Badge>
      </header>

      <dl className="mt-3 grid grid-cols-2 gap-2 text-[11px] sm:grid-cols-4">
        <div>
          <dt className="text-muted-foreground">页面</dt>
          <dd className="font-medium text-foreground">{binding.pages.length} 个页面</dd>
        </div>
        <div>
          <dt className="text-muted-foreground">API</dt>
          <dd className="font-medium text-foreground">{binding.apis.length} 个 API</dd>
        </div>
        <div>
          <dt className="text-muted-foreground">目标库</dt>
          <dd className="font-medium text-foreground">{binding.databases.length} 个目标库</dd>
        </div>
        <div>
          <dt className="text-muted-foreground">测试账号</dt>
          <dd className="font-medium text-foreground">{binding.accounts.length} 个测试账号</dd>
        </div>
      </dl>

      <div className="mt-3 flex flex-wrap items-center gap-2 border-t border-border pt-3 text-[11px]">
        <span className="text-muted-foreground">代码仓</span>
        <span className="font-medium text-foreground">{repositoryName}</span>
        {binding.pages.length > 0 ? (
          <span className="text-muted-foreground">
            页面范围：{binding.pages.map((page) => page.name).join("、")}
          </span>
        ) : null}
      </div>
    </article>
  )
}

function VersionOverview({ version }: { version: Version }) {
  const totals = version.bindings.reduce(
    (sum, binding) => ({
      pages: sum.pages + binding.pages.length,
      apis: sum.apis + binding.apis.length,
      databases: sum.databases + binding.databases.length,
      accounts: sum.accounts + binding.accounts.length,
      repositories: sum.repositories + (binding.repository ? 1 : 0),
    }),
    { pages: 0, apis: 0, databases: 0, accounts: 0, repositories: 0 },
  )

  return (
    <div className="space-y-4">
      <dl className="grid grid-cols-2 gap-2 sm:grid-cols-5">
        <OverviewStat
          icon={<FileCode2 className="size-3.5" />}
          label="页面"
          value={`${totals.pages} 个页面`}
        />
        <OverviewStat
          icon={<Server className="size-3.5" />}
          label="API"
          value={`${totals.apis} 个 API`}
        />
        <OverviewStat
          icon={<Database className="size-3.5" />}
          label="目标库"
          value={`${totals.databases} 个目标库`}
        />
        <OverviewStat
          icon={<Database className="size-3.5" />}
          label="测试账号"
          value={`${totals.accounts} 个测试账号`}
        />
        <OverviewStat
          icon={<FileCode2 className="size-3.5" />}
          label="代码仓"
          value={`${totals.repositories} 个代码仓`}
        />
      </dl>

      <section aria-label="绑定概览" className="space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 className="text-sm font-semibold text-foreground">SUT 绑定概览</h2>
          <span className="text-[11px] text-muted-foreground">
            共 {version.bindings.length} 个 SUT · 更新于 {formatDateTime(version.updated_at)}
          </span>
        </div>

        {version.bindings.length === 0 ? (
          <EmptyState
            title="该版本暂无 SUT 绑定"
            description="P0 创建接口要求至少一个绑定，请检查数据或重新创建版本。"
          />
        ) : (
          <div className="grid gap-3 lg:grid-cols-2">
            {version.bindings.map((binding, index) => (
              <BindingCard key={binding.id} binding={binding} index={index} />
            ))}
          </div>
        )}
      </section>

      <section className="rounded-xl border border-dashed border-border bg-card/60 px-4 py-3 text-xs leading-relaxed text-muted-foreground">
        需求与澄清（P1）、用例设计（P2）、执行（P3）、探索链路/问题/报告（P4）与代码与变更（P5）
        已在对应 Tab 提供。
      </section>
    </div>
  )
}

export type VersionDetailPageProps = {
  versionId: string
  initialTab?: VersionTab
  /** Deep link `?conversation=` forwarded to the clarification workspace. */
  initialConversationId?: string
  /** Deep link `?requirement=` forwarded to the clarification workspace. */
  initialRequirementId?: string
}

/** `/versions/{id}` shell: header, eight URL-backed tabs and the overview. */
export function VersionDetailPage({
  versionId,
  initialTab,
  initialConversationId,
  initialRequirementId,
}: VersionDetailPageProps) {
  const router = useRouter()
  const searchParams = useSearchParams()
  const [version, setVersion] = useState<Version | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<unknown>(null)
  const [activeTab, setActiveTab] = useState<VersionTab>(() => parseVersionTab(initialTab))
  const [issueCount, setIssueCount] = useState<number | undefined>(undefined)
  const [reportRevision, setReportRevision] = useState<number | undefined>(undefined)
  const [todoOpen, setTodoOpen] = useState(false)

  // `?case=` / `?conversation=` deep links for the design workspace. The route
  // already forwards `conversation` for clarification; read the URL here so the
  // case deep link works without changing `src/app` routing.
  const designConversationId =
    initialConversationId ?? searchParams.get("conversation") ?? undefined
  const designCaseId = searchParams.get("case") ?? undefined
  // `?wave=` / `?case_run=` select a wave/case run inside the execution tab.
  const executionWaveId = searchParams.get("wave") ?? undefined
  const executionCaseRunId = searchParams.get("case_run") ?? undefined

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      setVersion(await getVersion(versionId))
    } catch (cause) {
      setVersion(null)
      setError(cause)
    } finally {
      setLoading(false)
    }
  }, [versionId])

  /** Silent projection refresh used by SSE events (no full-page loading flash). */
  const refreshVersion = useCallback(async () => {
    try {
      setVersion(await getVersion(versionId))
    } catch {
      // Keep the last projection; the next event or retry refreshes it.
    }
  }, [versionId])

  // Tab counts come from the real list projections; a failed/missing query
  // leaves the count absent instead of rendering a misleading zero (spec §9.1).
  const refreshCounts = useCallback(async () => {
    try {
      const items = await listIssues(versionId, { status: "open", limit: 100 })
      setIssueCount(items.filter((issue) => issue.status === "open").length)
    } catch {
      setIssueCount(undefined)
    }
    try {
      const items = await listReports(versionId, { kind: "version", limit: 20 })
      const versionReport = items.find((report) => report.kind === "version")
      setReportRevision(versionReport?.current_revision?.revision)
    } catch {
      setReportRevision(undefined)
    }
  }, [versionId])

  useEffect(() => {
    void load()
  }, [load])

  useEffect(() => {
    setActiveTab(parseVersionTab(initialTab))
  }, [initialTab])

  useEffect(() => {
    void refreshCounts()
  }, [refreshCounts])

  const refreshTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const pendingRefresh = useRef({ version: false, counts: false })

  const runPendingRefresh = useCallback(() => {
    const pending = pendingRefresh.current
    pendingRefresh.current = { version: false, counts: false }
    if (pending.version) void refreshVersion()
    if (pending.counts) void refreshCounts()
  }, [refreshVersion, refreshCounts])

  const scheduleRefresh = useCallback(
    (includeVersion: boolean) => {
      pendingRefresh.current = {
        version: pendingRefresh.current.version || includeVersion,
        counts: true,
      }
      if (refreshTimer.current !== null) clearTimeout(refreshTimer.current)
      refreshTimer.current = setTimeout(() => {
        refreshTimer.current = null
        runPendingRefresh()
      }, SSE_REFRESH_DEBOUNCE_MS)
    },
    [runPendingRefresh],
  )

  const refreshVersionAndCounts = useCallback(() => scheduleRefresh(true), [scheduleRefresh])
  const refreshCountsOnly = useCallback(() => scheduleRefresh(false), [scheduleRefresh])

  const refreshSnapshotNow = useCallback(() => {
    if (refreshTimer.current !== null) {
      clearTimeout(refreshTimer.current)
      refreshTimer.current = null
    }
    pendingRefresh.current = { version: true, counts: true }
    runPendingRefresh()
  }, [runPendingRefresh])

  useEffect(
    () => () => {
      if (refreshTimer.current !== null) clearTimeout(refreshTimer.current)
    },
    [],
  )

  // One page-level subscription keeps the header projection and the tab counts
  // live while the mounted tab owns its own workspace subscription.
  useVersionEvents(versionId, {
    onVersionChanged: refreshVersionAndCounts,
    onRequirementChanged: refreshVersionAndCounts,
    onIssueChanged: refreshCountsOnly,
    onReportChanged: refreshCountsOnly,
    onStreamReset: refreshSnapshotNow,
  })

  const tabItems = useMemo(
    () =>
      VERSION_TABS.map((tab) => {
        if (tab.value === "issues" && issueCount !== undefined) return { ...tab, count: issueCount }
        if (tab.value === "reports" && reportRevision !== undefined) {
          return { ...tab, count: reportRevision }
        }
        return tab
      }),
    [issueCount, reportRevision],
  )

  function handleTabChange(value: string): void {
    const next = parseVersionTab(value)
    setActiveTab(next)
    const params = new URLSearchParams({ tab: next })
    if (next === "requirements") {
      if (initialConversationId) params.set("conversation", initialConversationId)
      if (initialRequirementId) params.set("requirement", initialRequirementId)
    }
    if (next === "design") {
      const conversationId = searchParams.get("conversation") ?? initialConversationId
      const caseId = searchParams.get("case")
      if (conversationId) params.set("conversation", conversationId)
      if (caseId) params.set("case", caseId)
    }
    if (next === "execution") {
      const waveId = searchParams.get("wave")
      const caseRunId = searchParams.get("case_run")
      if (waveId) params.set("wave", waveId)
      if (caseRunId) params.set("case_run", caseRunId)
    }
    if (next === "issues") {
      const issueId = searchParams.get("issue")
      if (issueId) params.set("issue", issueId)
    }
    if (next === "reports") {
      const reportId = searchParams.get("report")
      const revision = searchParams.get("revision")
      if (reportId) params.set("report", reportId)
      if (revision) params.set("revision", revision)
    }
    if (next === "chain") {
      for (const key of ["node", "issue", "report", "wave"]) {
        const value = searchParams.get(key)
        if (value) params.set(key, value)
      }
    }
    if (next === "code") {
      for (const key of ["repo", "analysis", "candidate", "anchor", "fact"]) {
        const value = searchParams.get(key)
        if (value) params.set(key, value)
      }
    }
    router.replace(`/versions/${encodeURIComponent(versionId)}?${params.toString()}`)
  }

  if (loading) {
    return (
      <div className="space-y-4">
        <Link
          href="/versions"
          className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
        >
          <ArrowLeft className="size-3.5" aria-hidden="true" />
          返回版本列表
        </Link>
        <LoadingCard label="正在加载版本详情…" />
      </div>
    )
  }

  if (error !== null || version === null) {
    return (
      <div className="space-y-4">
        <Link
          href="/versions"
          className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
        >
          <ArrowLeft className="size-3.5" aria-hidden="true" />
          返回版本列表
        </Link>
        <ErrorState error={error} title="版本详情加载失败" onRetry={() => void load()} />
      </div>
    )
  }

  return (
    <div className="space-y-5">
      <Link
        href="/versions"
        className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
      >
        <ArrowLeft className="size-3.5" aria-hidden="true" />
        返回版本列表
      </Link>

      <VersionHeader
        version={version}
        onOpenTodo={() => setTodoOpen(true)}
        onReopened={(next) => setVersion(next)}
      />

      <Tabs
        items={tabItems}
        value={activeTab}
        onValueChange={handleTabChange}
        ariaLabel="版本工作区"
      />

      {activeTab === "overview" ? (
        <VersionOverview version={version} />
      ) : activeTab === "requirements" ? (
        <RequirementsTab
          versionId={versionId}
          initialConversationId={initialConversationId}
          initialRequirementId={initialRequirementId}
        />
      ) : activeTab === "design" ? (
        <DesignTab
          versionId={versionId}
          initialCaseId={designCaseId}
          initialConversationId={designConversationId}
        />
      ) : activeTab === "execution" ? (
        <ExecutionTab
          versionId={versionId}
          initialWaveId={executionWaveId}
          initialCaseRunId={executionCaseRunId}
        />
      ) : activeTab === "chain" ? (
        <ChainTab versionId={versionId} onVersionChanged={refreshVersionAndCounts} />
      ) : activeTab === "issues" ? (
        <IssuesTab versionId={versionId} />
      ) : activeTab === "reports" ? (
        <ReportsTab versionId={versionId} />
      ) : (
        <CodeTab versionId={versionId} />
      )}

      <TodoDrawer versionId={versionId} open={todoOpen} onClose={() => setTodoOpen(false)} />
    </div>
  )
}
