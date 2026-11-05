"use client"

import { RefreshCw } from "lucide-react"
import { useEffect, useState } from "react"

import { Alert } from "@/components/ui/Alert"
import { Badge, type BadgeTone } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { ErrorState } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import type { VersionStage } from "@/features/design/types"
import { getVersionBinding } from "./api"
import { VERSION_STAGE_LABEL } from "./stage"
import type { VersionBindingOverview } from "./types"

export type BindingOverviewProps = {
  versionId: string
  /**
   * Optional edit entry point. W0 has no binding-edit flow yet (deferred to
   * W1), so the page does not pass it and the button is disabled instead of
   * being an enabled no-op.
   */
  onEdit?: (overview: VersionBindingOverview) => void
  /** Nonce bumped by SSE (`version.binding.changed`) to re-read the overview. */
  refreshKey?: number
}

function stageTone(stage: string): BadgeTone {
  if (stage === "completed") return "neutral"
  if (stage === "executing" || stage === "reporting" || stage === "paused") return "warning"
  if (stage === "blocked") return "danger"
  return "info"
}

function stageLabel(stage: string): string {
  return VERSION_STAGE_LABEL[stage as VersionStage] ?? stage
}

/** Why binding writes are currently disabled and how to make them writable again. */
function stageReadOnlyMessage(stage: string): string {
  if (stage === "completed") {
    return "已完成版本只读，如需调整绑定请克隆新版本。"
  }
  if (stage === "paused") {
    return "版本已暂停：暂停期间绑定只读。恢复后若仍处于执行/报告阶段，需回退到设计阶段才能修改绑定；改动只影响下一个 wave。"
  }
  if (stage === "blocked") {
    return "版本已阻塞：阻塞期间绑定只读。解除阻塞后若仍处于执行/报告阶段，需回退到设计阶段才能修改绑定；改动只影响下一个 wave。"
  }
  return `当前阶段为「${stageLabel(stage)}」，执行中/报告阶段需暂停或回退到设计阶段后才能修改绑定；改动只影响下一个 wave。`
}

function resourceName(resources: Array<{ id: string; name: string }>, id: string): string {
  return resources.find((resource) => resource.id === id)?.name ?? `#${id}`
}

type ChipRowProps = {
  kind: string
  ids: string[]
  resources: Array<{ id: string; name: string }>
}

function ExclusionRow({ kind, ids, resources }: ChipRowProps) {
  return (
    <div className="flex flex-wrap items-center gap-1.5 text-[11px]">
      <span className="w-14 shrink-0 text-muted-foreground">{kind}</span>
      {ids.length === 0 ? (
        <span className="text-muted-foreground">未排除</span>
      ) : (
        ids.map((id) => (
          <Badge key={id} tone="warning">
            {resourceName(resources, id)}
          </Badge>
        ))
      )}
    </div>
  )
}

type CountCardProps = {
  label: string
  counts: { approved: number; proposed: number; excluded: number; included: number }
}

function CountCard({ label, counts }: CountCardProps) {
  return (
    <div className="rounded-lg border border-border bg-card p-3">
      <p className="text-[11px] text-muted-foreground">{label}</p>
      <p className="mt-1 text-lg font-semibold text-foreground">{counts.included}</p>
      <p className="text-[11px] text-muted-foreground">
        已确认 {counts.approved} · 待确认 {counts.proposed} · 排除 {counts.excluded}
      </p>
    </div>
  )
}

/**
 * v0.22 SUT binding overview (R-C4-1/FE-5): the derived SUT/environment, the
 * effective repositories with roles and overrides, the enabled account subset,
 * the scope exclusion chips and the stage gate. The edit entry point is only
 * enabled while `editable` (the backend gate); completed versions are read-only.
 */
export function BindingOverview({ versionId, onEdit, refreshKey = 0 }: BindingOverviewProps) {
  const [overview, setOverview] = useState<VersionBindingOverview | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<unknown>(null)
  const [reloadToken, setReloadToken] = useState(0)

  // biome-ignore lint/correctness/useExhaustiveDependencies: refreshKey is the SSE-driven reload nonce and reloadToken the explicit refresh counter; both intentionally retrigger the read.
  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)
    void getVersionBinding(versionId)
      .then((value) => {
        if (!cancelled) setOverview(value)
      })
      .catch((cause: unknown) => {
        if (!cancelled) {
          setOverview(null)
          setError(cause)
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [versionId, refreshKey, reloadToken])

  if (loading) {
    return <LoadingCard label="正在加载绑定概览…" />
  }

  if (error !== null) {
    return (
      <ErrorState
        title="绑定概览加载失败"
        error={error}
        onRetry={() => setReloadToken((current) => current + 1)}
      />
    )
  }

  if (overview === null) {
    return <LoadingCard label="正在加载绑定概览…" />
  }

  const editable = overview.editable && overview.stage !== "completed"
  const readOnly = !editable
  const editAvailable = editable && typeof onEdit === "function"
  const editHint = readOnly
    ? "当前阶段绑定只读，需暂停或回退后才能编辑"
    : typeof onEdit === "function"
      ? undefined
      : "绑定编辑将在 W1 提供（当前为只读展示）"
  const excludedTotal =
    overview.scope_exclude.pages.length +
    overview.scope_exclude.apis.length +
    overview.scope_exclude.databases.length

  return (
    <section aria-label="SUT 绑定概览" className="space-y-3">
      <header className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <h2 className="text-sm font-semibold text-foreground">SUT 绑定概览</h2>
          <p className="mt-1 text-[11px] text-muted-foreground">SUT：{overview.sut.name}</p>
          <p className="text-[11px] text-muted-foreground">
            环境：{overview.environment.name}（{overview.environment.base_url}）
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Badge tone={stageTone(overview.stage)}>{stageLabel(overview.stage)}</Badge>
          <Button
            variant="secondary"
            size="sm"
            disabled={!editAvailable}
            title={editHint}
            onClick={() => onEdit?.(overview)}
          >
            编辑绑定
          </Button>
          <Button
            variant="ghost"
            size="sm"
            aria-label="刷新绑定"
            onClick={() => setReloadToken((current) => current + 1)}
          >
            <RefreshCw className="size-3.5" aria-hidden="true" />
          </Button>
        </div>
      </header>

      {editable && typeof onEdit !== "function" ? (
        <p className="text-[11px] text-muted-foreground" data-testid="binding-edit-deferred">
          绑定编辑将在 W1 提供（当前为只读展示）。
        </p>
      ) : null}

      {readOnly ? (
        <Alert tone="warning" title="当前阶段绑定只读">
          {stageReadOnlyMessage(overview.stage)}
        </Alert>
      ) : null}

      <div className="grid gap-2 sm:grid-cols-3">
        <CountCard label="页面" counts={overview.counts.pages} />
        <CountCard label="API" counts={overview.counts.apis} />
        <CountCard label="目标库" counts={overview.counts.databases} />
      </div>

      <section aria-label="代码仓" className="rounded-xl border border-border bg-card p-3">
        <h3 className="mb-2 text-xs font-semibold text-foreground">代码仓（角色 / 覆盖）</h3>
        {overview.repositories.length === 0 ? (
          <p className="text-[11px] text-muted-foreground">该 SUT 未关联代码仓</p>
        ) : (
          <ul className="space-y-1.5">
            {overview.repositories.map((row) => (
              <li
                key={row.repository.id}
                className="flex flex-wrap items-center gap-1.5 text-xs text-foreground"
              >
                <span className="font-medium">{row.repository.name}</span>
                {row.repository.branch ? (
                  <span className="font-mono text-[11px] text-muted-foreground">
                    {row.repository.branch}
                  </span>
                ) : null}
                {row.role ? <Badge tone="neutral">{row.role}</Badge> : null}
                <Badge tone={row.source === "override" ? "info" : "neutral"}>
                  {row.source === "override" ? "版本覆盖" : "SUT 继承"}
                </Badge>
                {row.included ? (
                  <Badge tone="success">已包含</Badge>
                ) : (
                  <Badge tone="warning">已排除</Badge>
                )}
              </li>
            ))}
          </ul>
        )}
        {overview.code_baseline_commit ? (
          <p className="mt-2 font-mono text-[11px] text-muted-foreground">
            代码基线：{overview.code_baseline_commit}
          </p>
        ) : null}
      </section>

      <section aria-label="测试账号" className="rounded-xl border border-border bg-card p-3">
        <h3 className="mb-1 text-xs font-semibold text-foreground">测试账号</h3>
        <p className="mb-2 text-[11px] text-muted-foreground">仅显示启用账号</p>
        {overview.accounts.length === 0 ? (
          <p className="text-[11px] text-muted-foreground">未关联测试账号（执行将被拒绝）</p>
        ) : (
          <div className="flex flex-wrap gap-1.5">
            {overview.accounts.map((account) => (
              <Badge key={account.id} tone="info">
                {account.username}
                {account.roles.length > 0 ? ` · ${account.roles.join(" / ")}` : ""}
              </Badge>
            ))}
          </div>
        )}
      </section>

      <section aria-label="范围排除" className="rounded-xl border border-border bg-card p-3">
        <h3 className="mb-2 text-xs font-semibold text-foreground">范围排除</h3>
        {excludedTotal === 0 ? (
          <p className="mb-1.5 text-[11px] text-muted-foreground">未设置排除项（默认包含全部）</p>
        ) : null}
        <div className="space-y-1.5">
          <ExclusionRow
            kind="页面"
            ids={overview.scope_exclude.pages}
            resources={overview.environment.pages}
          />
          <ExclusionRow
            kind="API"
            ids={overview.scope_exclude.apis}
            resources={overview.environment.apis}
          />
          <ExclusionRow
            kind="目标库"
            ids={overview.scope_exclude.databases}
            resources={overview.environment.databases}
          />
        </div>
      </section>
    </section>
  )
}
