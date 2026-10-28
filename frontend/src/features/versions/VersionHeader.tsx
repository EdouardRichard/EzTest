"use client"

import {
  ArrowLeft,
  FileText,
  Loader2,
  Network,
  Pause,
  Plus,
  RefreshCw,
  UserRoundCheck,
} from "lucide-react"
import Link from "next/link"
import { useState } from "react"

import { StatusBadge, type StatusBadgeVariant } from "@/components/ui/StatusBadge"
import { BatchSelector } from "@/features/execution/components/BatchSelector"
import { PauseDialog } from "@/features/execution/components/PauseDialog"
import { PausedBanner } from "@/features/execution/components/PausedBanner"
import { StartBatchButton } from "@/features/execution/components/StartBatchButton"
import {
  type UseExecutionActionsResult,
  useCaseRuns,
} from "@/features/execution/hooks/useExecution"
import type { Batch, PauseMode } from "@/features/execution/types"
import { ApiError } from "@/lib/api-client"

import { enterDesign, startClarification } from "./api"
import { ContinuingBanner } from "./ContinuingBanner"
import { IncrementalPlanPanel } from "./IncrementalPlanPanel"
import type { Version, VersionResource, VersionStatus } from "./types"
import type { VersionStreamState } from "./useVersionStream"

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
  continuing: { label: "继续中", variant: "warning" },
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

/**
 * Header-level execution projection owned by `VersionDetailPage`: the batch
 * page, the selected batch, the control actions and the shared version stream.
 * Pause/resume never update optimistically — the action refreshes the page and
 * the paused banner follows the server projection.
 */
export type VersionHeaderExecution = {
  batches: Batch[]
  activeBatch: Batch | null
  selectedBatchId: string
  stream: VersionStreamState
  actions: UseExecutionActionsResult
  onBatchChange: (batchId: string) => void
  onOpenReport: () => void
  onOpenSessions: () => void
  /** Switches to the results tab after the incremental batch is created. */
  onOpenResults?: () => void
}

export type VersionHeaderProps = {
  version: Version
  /** True while the parent refetches the version. */
  refreshing: boolean
  /** Refresh callback exposed to the owning container (T17/T18 can call it). */
  onRefresh: () => void
  /** Receives every version returned by a state-changing action. */
  onUpdated: (version: Version) => void
  /** Opens the requirements tab for the completed/continuing 新增需求 entry. */
  onAddRequirement?: () => void
  /** Opens the code-changes tab for the continuing 查看变更 entry. */
  onViewChanges?: () => void
  /** Opens the requirements tab for the continuing 去澄清 action. */
  onOpenClarification?: () => void
  /** P6 execution controls; absent while the header is used without them. */
  execution?: VersionHeaderExecution
}

/** Banner for blocked case runs that are waiting for a human takeover. */
function HumanPendingBanner({
  versionId,
  batchId,
  stream,
  onOpenSessions,
}: {
  versionId: string
  batchId: string
  stream: VersionStreamState
  onOpenSessions: () => void
}) {
  const { caseRuns } = useCaseRuns(versionId, batchId, { status: "blocked" }, stream)
  const pending = caseRuns.filter((caseRun) => caseRun.pending_takeover !== null).length
  if (pending === 0) return null

  return (
    <div
      data-testid="human-pending-banner"
      role="status"
      className="flex flex-wrap items-center gap-2 rounded-xl border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-sm text-amber-800 dark:text-amber-200"
    >
      <UserRoundCheck className="h-4 w-4 shrink-0" aria-hidden="true" />
      <span>{`有 ${pending} 个用例等待人工接管`}</span>
      <button
        type="button"
        onClick={onOpenSessions}
        className="ml-auto rounded-lg border border-amber-500/40 bg-card px-2.5 py-1 text-xs font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        前往执行会话
      </button>
    </div>
  )
}

/**
 * Version detail header (spec §9.2): back link, name/label, status badge,
 * resolved binding summary (deleted resources stay visible), requirement and
 * open-question counters, the status-gated state action, and a manual refresh
 * callback. Entering design first tries without acknowledgement; a 409
 * `unclarified_requirements` opens an inline confirmation, then the request is
 * retried with `acknowledgeUnclarified=true`.
 */
export function VersionHeader({
  version,
  refreshing,
  onRefresh,
  onUpdated,
  onAddRequirement,
  onViewChanges,
  onOpenClarification,
  execution,
}: VersionHeaderProps) {
  const [busy, setBusy] = useState<"start" | "design" | null>(null)
  const [actionError, setActionError] = useState("")
  const [warnings, setWarnings] = useState<string[]>([])
  const [pendingWarnings, setPendingWarnings] = useState<PendingWarnings | null>(null)
  const [pauseDialogOpen, setPauseDialogOpen] = useState(false)
  const [pauseDialogError, setPauseDialogError] = useState("")
  // The plan panel belongs to one continuing episode. Keying the open state by
  // version+status closes it on any transition without an effect.
  const planKey = `${version.id}:${version.status}`
  const [planState, setPlanState] = useState<{ key: string; open: boolean }>({
    key: planKey,
    open: false,
  })
  const planOpen = planState.key === planKey && planState.open

  const unclarifiedCount = Math.max(version.requirementCount - version.clarifiedCount, 0)
  const executable = version.status === "executing" || version.status === "continuing"
  const activeBatch = execution?.activeBatch ?? null
  const executionPending = execution?.actions.pending ?? ""
  const selectedBatch = execution?.selectedBatchId ?? ""
  const continuing = version.status === "continuing"
  // The active incremental batch already owns the next batch number, so the
  // badge must use it; before allocation (clarify/design) it falls back to the
  // server `batch_no + 1` rule (spec §10.1 / review ruling).
  const continuingBatchNo = execution?.activeBatch?.batch_no ?? (version.batch_no ?? 0) + 1
  const continuingLabel = `继续中（第 ${continuingBatchNo} 批）`
  const showAddRequirement = version.status === "completed" || continuing

  function togglePlan(): void {
    setPlanState({ key: planKey, open: !planOpen })
  }

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

  async function handleStartBatch(): Promise<void> {
    if (execution === undefined) return
    setActionError("")
    await execution.actions.createBatch()
  }

  async function handlePause(mode: PauseMode): Promise<void> {
    if (execution === undefined || execution.activeBatch === null) return
    setPauseDialogError("")
    const ok = await execution.actions.pause(execution.activeBatch.id, mode)
    if (ok) {
      setPauseDialogOpen(false)
    } else {
      setPauseDialogError(execution.actions.error || "暂停失败，请稍后重试。")
    }
  }

  async function handleResume(): Promise<void> {
    if (execution === undefined || execution.activeBatch === null) return
    setActionError("")
    await execution.actions.resume(execution.activeBatch.id)
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
            {continuing ? (
              <StatusBadge variant="warning">
                <span data-testid="continuing-badge">{continuingLabel}</span>
              </StatusBadge>
            ) : (
              <StatusBadge variant={versionStatusVariant(version.status)}>
                {versionStatusLabel(version.status)}
              </StatusBadge>
            )}
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

          {execution !== undefined && (executable || version.status === "completed") ? (
            <BatchSelector
              batches={execution.batches}
              value={selectedBatch}
              onChange={execution.onBatchChange}
            />
          ) : null}

          <StartBatchButton
            versionStatus={version.status}
            hasActiveBatch={activeBatch !== null}
            busy={executionPending === "createBatch"}
            disabled={execution === undefined || execution.actions.pending !== ""}
            onStart={() => void handleStartBatch()}
          />

          {executable && (activeBatch === null || !activeBatch.paused) ? (
            <button
              type="button"
              data-testid="exec-pause-btn"
              onClick={() => {
                setPauseDialogError("")
                setPauseDialogOpen(true)
              }}
              // No active batch (or a paused version) has nothing to pause; the
              // control stays visible but disabled so the rule is explicit.
              disabled={
                executionPending !== "" ||
                execution === undefined ||
                activeBatch === null ||
                version.paused === true
              }
              className="inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <Pause className="h-4 w-4" aria-hidden="true" />
              暂停
            </button>
          ) : null}

          {showAddRequirement ? (
            <button
              type="button"
              data-testid="add-requirement-btn"
              onClick={() => onAddRequirement?.()}
              className="inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <Plus className="h-4 w-4" aria-hidden="true" />
              新增需求
            </button>
          ) : null}

          {continuing ? (
            <button
              type="button"
              data-testid="view-changes-btn"
              onClick={() => onViewChanges?.()}
              className="inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <FileText className="h-4 w-4" aria-hidden="true" />
              查看变更
            </button>
          ) : null}

          {version.status === "completed" && execution !== undefined ? (
            <button
              type="button"
              data-testid="exec-report-entry"
              onClick={execution.onOpenReport}
              className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <FileText className="h-4 w-4" aria-hidden="true" />
              查看报告
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

      {continuing ? (
        <ContinuingBanner
          version={version}
          activeBatch={activeBatch}
          onClarify={onOpenClarification}
          onOpenPlan={togglePlan}
          onOpenSessions={execution?.onOpenSessions}
        />
      ) : null}

      {continuing && planOpen ? (
        <IncrementalPlanPanel
          versionId={version.id}
          onCreated={() => {
            setPlanState({ key: planKey, open: false })
            execution?.onOpenResults?.()
          }}
        />
      ) : null}

      {execution !== undefined && activeBatch?.paused ? (
        <PausedBanner
          batch={activeBatch}
          busy={executionPending === "resume"}
          error={execution.actions.error}
          onResume={() => void handleResume()}
        />
      ) : null}

      {execution !== undefined && activeBatch !== null ? (
        <HumanPendingBanner
          versionId={version.id}
          batchId={activeBatch.id}
          stream={execution.stream}
          onOpenSessions={execution.onOpenSessions}
        />
      ) : null}

      {execution !== undefined && execution.actions.error !== "" && activeBatch?.paused !== true ? (
        <p
          role="alert"
          className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
        >
          {execution.actions.error}
        </p>
      ) : null}

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

      <PauseDialog
        open={pauseDialogOpen}
        busy={executionPending === "pause"}
        error={pauseDialogError}
        onConfirm={(mode) => void handlePause(mode)}
        onClose={() => setPauseDialogOpen(false)}
      />
    </header>
  )
}
