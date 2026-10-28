"use client"

import { ArrowLeft, Loader2, Network, RefreshCw } from "lucide-react"
import Link from "next/link"
import { useState } from "react"

import { StatusBadge, type StatusBadgeVariant } from "@/components/ui/StatusBadge"
import { ApiError } from "@/lib/api-client"

import { enterDesign, startClarification } from "./api"
import type { Version, VersionResource, VersionStatus } from "./types"

/** Display metadata for every version status the API may return (spec §6.1). */
export const VERSION_STATUS_META: Record<
  VersionStatus,
  { label: string; variant: StatusBadgeVariant }
> = {
  draft: { label: "草稿", variant: "neutral" },
  clarifying: { label: "澄清中", variant: "info" },
  designing: { label: "设计中", variant: "warning" },
  reviewing: { label: "评审中", variant: "info" },
  executing: { label: "执行中", variant: "warning" },
  continuing: { label: "继续执行", variant: "warning" },
  completed: { label: "已完成", variant: "success" },
  archived: { label: "已归档", variant: "neutral" },
}

/** The stable status order shared by the list filter and badges. */
export const VERSION_STATUS_ORDER: readonly VersionStatus[] = [
  "draft",
  "clarifying",
  "designing",
  "reviewing",
  "executing",
  "continuing",
  "completed",
  "archived",
]

export function versionStatusLabel(status: VersionStatus): string {
  return VERSION_STATUS_META[status].label
}

export function versionStatusVariant(status: VersionStatus): StatusBadgeVariant {
  return VERSION_STATUS_META[status].variant
}

/** Human labels for the closed `details.reason` set the header may receive. */
const ACTION_REASON_LABELS: Record<string, string> = {
  requirements_required: "开始澄清前请至少录入一条需求。",
  version_state_conflict: "当前版本状态不允许该操作，请刷新后重试。",
  ai_unavailable: "AI 服务暂不可用，请稍后再试。",
}

function describeActionError(error: unknown): string {
  if (!(error instanceof ApiError)) {
    return error instanceof Error ? error.message : "未知错误"
  }
  if (error.status === 0) return "无法连接 API 服务，请确认后端已启动。"

  const reason = typeof error.details.reason === "string" ? error.details.reason : ""
  const label = ACTION_REASON_LABELS[reason]
  if (label !== undefined) return label
  if (reason !== "") return `${error.message}（${reason}）`
  return error.message
}

function bindingLabel(binding: VersionResource | null): string {
  if (binding === null) return "未绑定"
  return binding.available ? binding.name : `${binding.name}（已删除）`
}

type PendingWarnings = {
  unclarifiedRequirements: number
  openQuestionCount: number
}

export type VersionHeaderProps = {
  version: Version
  /** True while the parent refetches the version. */
  refreshing: boolean
  /** Refresh callback exposed to the owning container (T17/T18 can call it). */
  onRefresh: () => void
  /** Receives every version returned by a state-changing action. */
  onUpdated: (version: Version) => void
}

/**
 * Version detail header (spec §9.2): back link, name/label, status badge,
 * resolved binding summary (deleted resources stay visible), requirement and
 * open-question counters, the status-gated state action, and a manual refresh
 * callback. Entering design first tries without acknowledgement; a 409
 * `unclarified_requirements` opens an inline confirmation, then the request is
 * retried with `acknowledgeUnclarified=true`.
 */
export function VersionHeader({ version, refreshing, onRefresh, onUpdated }: VersionHeaderProps) {
  const [busy, setBusy] = useState<"start" | "design" | null>(null)
  const [actionError, setActionError] = useState("")
  const [warnings, setWarnings] = useState<string[]>([])
  const [pendingWarnings, setPendingWarnings] = useState<PendingWarnings | null>(null)

  const unclarifiedCount = Math.max(version.requirementCount - version.clarifiedCount, 0)

  async function handleStartClarification(): Promise<void> {
    setBusy("start")
    setActionError("")
    setWarnings([])
    try {
      const updated = await startClarification(version.id)
      onUpdated(updated)
    } catch (caught) {
      setActionError(describeActionError(caught))
    } finally {
      setBusy(null)
    }
  }

  async function handleEnterDesign(acknowledge: boolean): Promise<void> {
    setBusy("design")
    setActionError("")
    setWarnings([])
    try {
      const result = await enterDesign(version.id, acknowledge)
      setPendingWarnings(null)
      onUpdated(result.version)
      setWarnings(result.warnings)
    } catch (caught) {
      if (
        !acknowledge &&
        caught instanceof ApiError &&
        caught.status === 409 &&
        caught.details.reason === "unclarified_requirements"
      ) {
        const ids = caught.details.unclarifiedRequirementIds
        const openQuestionCount = caught.details.openQuestionCount
        setPendingWarnings({
          unclarifiedRequirements: Array.isArray(ids) ? ids.length : 0,
          openQuestionCount: typeof openQuestionCount === "number" ? openQuestionCount : 0,
        })
      } else {
        setActionError(describeActionError(caught))
      }
    } finally {
      setBusy(null)
    }
  }

  return (
    <header className="space-y-3 rounded-2xl border border-border bg-card p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <Link
            href="/versions"
            className="inline-flex items-center gap-1 text-xs font-medium text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <ArrowLeft className="h-3.5 w-3.5" aria-hidden="true" />
            返回版本列表
          </Link>

          <div className="mt-2 flex flex-wrap items-center gap-2">
            <h1 className="text-2xl font-semibold tracking-tight">{version.name}</h1>
            {version.label === "" ? null : (
              <span className="rounded-full border border-border bg-muted px-2 py-0.5 font-mono text-[11px] text-muted-foreground">
                {version.label}
              </span>
            )}
            <StatusBadge variant={versionStatusVariant(version.status)}>
              {versionStatusLabel(version.status)}
            </StatusBadge>
          </div>

          <p className="mt-1 flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
            <span>系统：{bindingLabel(version.bindings.system)}</span>
            <span>环境：{bindingLabel(version.bindings.environment)}</span>
            <span>代码仓：{bindingLabel(version.bindings.codeRepository)}</span>
            {version.codeBranch === null ? null : (
              <span className="font-mono">分支：{version.codeBranch}</span>
            )}
          </p>

          <p className="mt-1 flex flex-wrap gap-x-4 gap-y-1 text-xs">
            <span>需求 {version.requirementCount}</span>
            <span>已确认 {version.clarifiedCount}</span>
            <span>未澄清需求 {unclarifiedCount}</span>
            <span>待澄清问题 {version.openQuestionCount}</span>
          </p>
        </div>

        <div className="flex flex-wrap gap-1.5">
          {version.status === "draft" ? (
            <button
              type="button"
              onClick={() => void handleStartClarification()}
              disabled={busy !== null}
              className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              {busy === "start" ? (
                <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
              ) : null}
              开始澄清
            </button>
          ) : null}

          {version.status === "clarifying" ? (
            <button
              type="button"
              onClick={() => void handleEnterDesign(false)}
              disabled={busy !== null}
              className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              {busy === "design" ? (
                <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
              ) : null}
              进入设计
            </button>
          ) : null}

          <Link
            href={`/versions/${version.id}/mindmap`}
            data-testid="open-mindmap"
            className="inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <Network className="h-4 w-4" aria-hidden="true" />
            用例脑图
          </Link>

          <button
            type="button"
            onClick={onRefresh}
            disabled={refreshing || busy !== null}
            className="inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            {refreshing ? (
              <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
            ) : (
              <RefreshCw className="h-4 w-4" aria-hidden="true" />
            )}
            刷新
          </button>
        </div>
      </div>

      {version.status === "designing" ? (
        <p
          role="status"
          className="rounded-xl border border-amber-500/25 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300"
        >
          设计中 · 含 {unclarifiedCount} 条未澄清需求，仍可在「需求与澄清」Tab 继续澄清与确认。
        </p>
      ) : null}

      {actionError === "" ? null : (
        <p
          role="alert"
          className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
        >
          {actionError}
        </p>
      )}

      {warnings.length === 0 ? null : (
        <div
          role="status"
          className="rounded-xl border border-amber-500/25 bg-amber-500/10 px-3 py-2 text-sm text-amber-700 dark:text-amber-300"
        >
          <p>已进入设计，以下提示请继续跟进：</p>
          <ul className="mt-1 list-disc space-y-0.5 pl-5">
            {warnings.map((warning) => (
              <li key={warning}>{warning}</li>
            ))}
          </ul>
        </div>
      )}

      {pendingWarnings === null ? null : (
        <div
          role="alertdialog"
          aria-modal="true"
          aria-labelledby="enter-design-confirm-title"
          className="rounded-xl border border-amber-500/30 bg-amber-500/10 p-3"
        >
          <h2 id="enter-design-confirm-title" className="text-sm font-semibold">
            确认进入设计
          </h2>
          <p className="mt-1 text-sm text-muted-foreground">
            有 {pendingWarnings.unclarifiedRequirements} 条需求尚未澄清，且仍有{" "}
            {pendingWarnings.openQuestionCount} 个待澄清问题。仍要进入设计吗？
          </p>
          <p className="mt-1 text-xs text-muted-foreground">
            进入后仍可在「需求与澄清」Tab 继续澄清与确认需求。
          </p>
          <div className="mt-3 flex flex-wrap gap-1.5">
            <button
              type="button"
              onClick={() => void handleEnterDesign(true)}
              disabled={busy !== null}
              className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              {busy === "design" ? (
                <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
              ) : null}
              确认进入设计
            </button>
            <button
              type="button"
              onClick={() => setPendingWarnings(null)}
              disabled={busy !== null}
              className="rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              取消
            </button>
          </div>
        </div>
      )}
    </header>
  )
}
