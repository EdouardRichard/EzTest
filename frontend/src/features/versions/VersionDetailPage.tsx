"use client"

import { ArrowLeft, Database, FileCode2, Server } from "lucide-react"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { type ReactNode, useCallback, useEffect, useState } from "react"

import { Badge, type BadgeTone } from "@/components/ui/Badge"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import { Tabs } from "@/components/ui/Tabs"
import { RequirementsTab } from "@/features/clarification/RequirementsTab"
import { DesignTab } from "@/features/design/DesignTab"
import { formatDateTime } from "@/lib/format"

import { getVersion } from "./api"
import { parseVersionTab, VERSION_TABS } from "./tabs"
import type { Version, VersionBinding, VersionStatus, VersionTab } from "./types"

const STATUS_META: Record<VersionStatus, { label: string; tone: BadgeTone }> = {
  initial: { label: "初始", tone: "info" },
  running: { label: "运行中", tone: "success" },
  completed: { label: "已完成", tone: "neutral" },
}

type PlaceholderTab = Exclude<VersionTab, "overview" | "requirements" | "design">

type PlaceholderCopy = {
  label: string
  phase: "P3" | "P4" | "P5"
  description: string
}

const PLACEHOLDER_COPY: Record<PlaceholderTab, PlaceholderCopy> = {
  execution: {
    label: "执行",
    phase: "P3",
    description: "波次执行、实时记录、证据与人工接管由 P3 阶段提供。",
  },
  chain: {
    label: "探索链路",
    phase: "P4",
    description: "跨 SUT 的探索链路图、过滤与历史回放由 P4 阶段提供。",
  },
  issues: {
    label: "问题",
    phase: "P4",
    description: "失败归类、问题去重与复测闭环由 P4 阶段提供。",
  },
  reports: {
    label: "报告",
    phase: "P4",
    description: "波次/版本报告、HTML/PDF 发布与 as-of 修订由 P4 阶段提供。",
  },
  code: {
    label: "代码与变更",
    phase: "P5",
    description: "代码仓扫描、变更影响与调用链分析由 P5 阶段提供。",
  },
}

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
        需求与澄清已在 P1
        提供；用例设计（P2）、执行（P3）、探索链路/问题/报告（P4）与代码与变更（P5）
        将按阶段逐步提供。
      </section>
    </div>
  )
}

function TabPlaceholder({ tab }: { tab: PlaceholderTab }) {
  const copy = PLACEHOLDER_COPY[tab]
  return (
    <section aria-label={`${copy.label}占位`} className="surface-card p-6">
      <EmptyState
        title={`${copy.label}将在 ${copy.phase} 阶段提供`}
        description={copy.description}
        action={<Badge tone="warning">{copy.phase} 阶段提供</Badge>}
      />
    </section>
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

  // `?case=` / `?conversation=` deep links for the design workspace. The route
  // already forwards `conversation` for clarification; read the URL here so the
  // case deep link works without changing `src/app` routing.
  const designConversationId =
    initialConversationId ?? searchParams.get("conversation") ?? undefined
  const designCaseId = searchParams.get("case") ?? undefined

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

  useEffect(() => {
    void load()
  }, [load])

  useEffect(() => {
    setActiveTab(parseVersionTab(initialTab))
  }, [initialTab])

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

  const statusMeta = STATUS_META[version.status]

  return (
    <div className="space-y-5">
      <Link
        href="/versions"
        className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
      >
        <ArrowLeft className="size-3.5" aria-hidden="true" />
        返回版本列表
      </Link>

      <header className="space-y-3">
        <div className="flex flex-wrap items-center gap-2">
          <h1 className="text-xl font-semibold text-foreground">{version.title}</h1>
          <Badge tone={statusMeta.tone}>{statusMeta.label}</Badge>
          {version.auto_design_new_requirements ? <Badge tone="info">新需求自动设计</Badge> : null}
        </div>

        <fieldset className="flex flex-wrap items-center gap-2">
          <legend className="sr-only">版本 SUT</legend>
          {version.bindings.map((binding) => (
            <Badge key={binding.id} tone="info">
              {binding.sut.name}
            </Badge>
          ))}
          <span className="text-[11px] text-muted-foreground">
            创建于 {formatDateTime(version.created_at)}
          </span>
        </fieldset>
      </header>

      <Tabs
        items={VERSION_TABS}
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
      ) : (
        <TabPlaceholder tab={activeTab} />
      )}
    </div>
  )
}
