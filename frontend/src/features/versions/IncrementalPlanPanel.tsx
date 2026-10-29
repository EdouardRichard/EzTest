"use client"

import { AlertTriangle, Loader2, Plus, RefreshCw } from "lucide-react"
import { useCallback, useEffect, useRef, useState } from "react"

import { StatusBadge } from "@/components/ui/StatusBadge"
import { ApiError } from "@/lib/api-client"

import { createIncrementalBatch, getIncrementalPlan } from "./api"
import type {
  CreatedBatch,
  IncrementalPlan,
  IncrementalPlanAffectedCase,
  IncrementalPlanNewCase,
} from "./types"

/** Human labels for the plan confidence enum; unknown values stay verbatim. */
const CONFIDENCE_LABELS: Record<string, string> = {
  high: "高",
  medium: "中",
  low: "低",
}

/** Human labels for the mindmap case priority enum (P0–P3). */
const PRIORITY_LABELS: Record<string, string> = {
  P0: "P0 紧急",
  P1: "P1 高",
  P2: "P2 中",
  P3: "P3 低",
}

/** Human labels for the supplemented case status enum. */
const NODE_STATUS_LABELS: Record<string, string> = {
  draft: "草稿",
  reviewed: "已评审",
  executed: "已执行",
  supplemented: "已补全",
  merged: "已合并",
  rejected: "已驳回",
  needs_revalidation: "待重新验证",
}

/** Human labels for the affected-case source enum (spec §8.1). */
const AFFECTED_SOURCE_LABELS: Record<string, string> = {
  requirement: "需求变更",
  code: "代码变更",
}

/** Human labels for the continuing phase of the plan. */
const PLAN_PHASE_LABELS: Record<string, string> = {
  clarifying: "增量澄清",
  designing: "增量设计",
  executing: "增量执行",
}

/** Stable `details.reason` values of the incremental batch gate (spec §5.3). */
const REASON_LABELS: Record<string, string> = {
  continuing_batch_conflict: "版本不在增量执行阶段，或已有活跃批次。请刷新后重试。",
  no_incremental_cases: "请至少选择一个新增或受影响用例。",
  autonomy_pending: "上一批次尚未完成自主收尾，请稍后重试。",
  baseline_required: "版本缺少冻结基线，无法创建增量批次。",
  environment_required: "请先绑定环境再创建增量批次。",
}

function describePlanError(error: unknown): string {
  if (!(error instanceof ApiError)) {
    return error instanceof Error ? error.message : "未知错误"
  }
  if (error.status === 0) return "无法连接 API 服务，请确认后端已启动。"
  const reason = typeof error.details.reason === "string" ? error.details.reason : ""
  const label = REASON_LABELS[reason]
  if (label !== undefined) return label
  if (reason !== "") return `${error.message}（${reason}）`
  return error.message
}

function confidenceLabel(confidence: string): string {
  return CONFIDENCE_LABELS[confidence] ?? confidence
}

function priorityLabel(priority: string): string {
  return PRIORITY_LABELS[priority] ?? priority
}

function nodeStatusLabel(status: string): string {
  return NODE_STATUS_LABELS[status] ?? status
}

function affectedSourceLabel(source: string): string {
  return AFFECTED_SOURCE_LABELS[source] ?? source
}

function planPhaseLabel(phase: string): string {
  if (phase === "") return "增量设计"
  return PLAN_PHASE_LABELS[phase] ?? phase
}

function isHighOrMedium(confidence: string): boolean {
  return confidence === "high" || confidence === "medium"
}

/**
 * Initial selection (spec §7.6): the backend `defaultCaseIds` wins; when an
 * older/empty plan omits it, all new cases plus high/medium affected cases are
 * selected, never low-confidence ones.
 */
function initialSelection(plan: IncrementalPlan): Set<string> {
  if (plan.defaultCaseIds.length > 0) return new Set(plan.defaultCaseIds)
  const ids = new Set(plan.newCases.map((item) => item.nodeId))
  for (const item of plan.affectedCases) {
    if (isHighOrMedium(item.confidence)) ids.add(item.nodeId)
  }
  return ids
}

/** Selected ids in display order: new cases first, then affected cases. */
function selectedCaseIds(plan: IncrementalPlan, selected: ReadonlySet<string>): string[] {
  const ids: string[] = []
  for (const item of plan.newCases) if (selected.has(item.nodeId)) ids.push(item.nodeId)
  for (const item of plan.affectedCases) if (selected.has(item.nodeId)) ids.push(item.nodeId)
  return ids
}

function isEmptyPlan(plan: IncrementalPlan): boolean {
  return plan.run === null && plan.newCases.length === 0 && plan.affectedCases.length === 0
}

type CaseCheckboxProps = {
  nodeId: string
  title: string
  confidence: string
  description: string
  checked: boolean
  testId: string
  onToggle: (nodeId: string) => void
}

function CaseCheckbox({
  nodeId,
  title,
  confidence,
  description,
  checked,
  testId,
  onToggle,
}: CaseCheckboxProps) {
  return (
    <label className="flex items-start gap-2 rounded-xl border border-border bg-card px-3 py-2 text-sm">
      <input
        type="checkbox"
        data-testid={testId}
        value={nodeId}
        checked={checked}
        onChange={() => onToggle(nodeId)}
        className="mt-0.5 h-4 w-4 shrink-0 rounded border-border"
      />
      <span className="min-w-0 space-y-0.5">
        <span className="flex flex-wrap items-center gap-1.5">
          <span className="break-all font-medium">{title}</span>
          <StatusBadge
            variant={
              confidence === "high" ? "success" : confidence === "low" ? "neutral" : "warning"
            }
          >
            {confidenceLabel(confidence)}
          </StatusBadge>
        </span>
        <span className="block text-xs text-muted-foreground">{description}</span>
      </span>
    </label>
  )
}

function NewCaseRow({
  item,
  checked,
  onToggle,
}: {
  item: IncrementalPlanNewCase
  checked: boolean
  onToggle: (nodeId: string) => void
}) {
  return (
    <CaseCheckbox
      nodeId={item.nodeId}
      title={item.title}
      confidence={item.confidence}
      description={`${priorityLabel(item.priority)} · ${nodeStatusLabel(item.status)}`}
      checked={checked}
      testId="incremental-plan-new-case"
      onToggle={onToggle}
    />
  )
}

function AffectedCaseRow({
  item,
  checked,
  onToggle,
}: {
  item: IncrementalPlanAffectedCase
  checked: boolean
  onToggle: (nodeId: string) => void
}) {
  const reason = item.reason === "" ? "未提供受影响原因" : item.reason
  return (
    <CaseCheckbox
      nodeId={item.nodeId}
      title={item.title}
      confidence={item.confidence}
      description={`${affectedSourceLabel(item.source)} · ${reason}`}
      checked={checked}
      testId="incremental-plan-affected-case"
      onToggle={onToggle}
    />
  )
}

export type IncrementalPlanPanelProps = {
  versionId: string
  /** Receives the created batch so the container can jump to results. */
  onCreated?: (batch: CreatedBatch) => void
}

/**
 * Incremental plan panel of a continuing version (spec §7.6/§10.1): new and
 * affected cases with confidence/reason, the unmapped-change and warning
 * block, the default high/medium selection and the incremental batch creation.
 * The panel never invents plan data: a version without a design run renders an
 * explicit empty state.
 */
export function IncrementalPlanPanel({ versionId, onCreated }: IncrementalPlanPanelProps) {
  const [plan, setPlan] = useState<IncrementalPlan | null>(null)
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState("")
  const [saving, setSaving] = useState(false)
  const [actionError, setActionError] = useState("")
  const [createdBatch, setCreatedBatch] = useState<CreatedBatch | null>(null)
  const requestRef = useRef(0)

  const load = useCallback(async (): Promise<void> => {
    const requestId = ++requestRef.current
    setLoading(true)
    setLoadError("")
    setActionError("")
    try {
      const data = await getIncrementalPlan(versionId)
      if (requestRef.current !== requestId) return
      setPlan(data)
      setSelected(initialSelection(data))
      setCreatedBatch(null)
    } catch (caught) {
      if (requestRef.current === requestId) setLoadError(describePlanError(caught))
    } finally {
      if (requestRef.current === requestId) setLoading(false)
    }
  }, [versionId])

  useEffect(() => {
    setPlan(null)
    setSelected(new Set())
    void load()
    return () => {
      requestRef.current += 1
    }
  }, [load])

  function toggleCase(nodeId: string): void {
    setSelected((previous) => {
      const next = new Set(previous)
      if (next.has(nodeId)) next.delete(nodeId)
      else next.add(nodeId)
      return next
    })
  }

  async function handleCreate(): Promise<void> {
    if (plan === null || plan.parentBatchId === null) return
    const caseIds = selectedCaseIds(plan, selected)
    if (caseIds.length === 0) {
      setActionError("请至少选择一个新增或受影响用例。")
      return
    }

    setSaving(true)
    setActionError("")
    setCreatedBatch(null)
    try {
      const batch = await createIncrementalBatch(versionId, {
        caseIds,
        parentBatchId: plan.parentBatchId,
      })
      setCreatedBatch(batch)
      onCreated?.(batch)
    } catch (caught) {
      setActionError(`增量批次创建失败：${describePlanError(caught)}`)
    } finally {
      setSaving(false)
    }
  }

  if (loading && plan === null) {
    return (
      <section
        data-testid="incremental-plan"
        aria-label="增量计划"
        className="rounded-2xl border border-border bg-card p-4 text-sm text-muted-foreground"
      >
        <p className="flex items-center gap-2">
          <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
          正在加载增量计划…
        </p>
      </section>
    )
  }

  if (loadError !== "") {
    return (
      <section
        data-testid="incremental-plan"
        aria-label="增量计划"
        className="space-y-2 rounded-2xl border border-border bg-card p-4"
      >
        <div
          role="alert"
          className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
        >
          <p>加载增量计划失败：{loadError}</p>
          <button
            type="button"
            onClick={() => void load()}
            className="mt-1.5 inline-flex items-center gap-1 rounded-lg border border-border bg-card px-2.5 py-1 text-xs font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <RefreshCw className="h-3 w-3" aria-hidden="true" />
            重试
          </button>
        </div>
      </section>
    )
  }

  if (plan === null || isEmptyPlan(plan)) {
    return (
      <section
        data-testid="incremental-plan"
        aria-label="增量计划"
        className="space-y-2 rounded-2xl border border-border bg-card p-4"
      >
        <h2 className="text-base font-semibold">增量计划</h2>
        <p className="rounded-xl border border-dashed border-border px-3 py-6 text-sm text-muted-foreground">
          <strong className="block text-foreground">暂无可用的增量计划</strong>
          增量设计完成后，新增与受影响用例会展示在这里。
        </p>
      </section>
    )
  }

  const selectedCount = selectedCaseIds(plan, selected).length
  const canCreate = plan.parentBatchId !== null && selectedCount > 0 && !saving

  return (
    <section
      data-testid="incremental-plan"
      aria-label="增量计划"
      className="space-y-3 rounded-2xl border border-border bg-card p-4"
    >
      <header className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h2 className="text-base font-semibold">增量计划</h2>
          <p className="mt-0.5 text-xs text-muted-foreground">
            {`父批次 ${plan.parentBatchId ?? "—"} · 已选 ${selectedCount}/${plan.newCases.length + plan.affectedCases.length} 个用例`}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-1.5">
          {plan.degraded ? <StatusBadge variant="warning">降级</StatusBadge> : null}
          <StatusBadge variant="info">{planPhaseLabel(plan.phase)}</StatusBadge>
        </div>
      </header>

      {plan.degraded ? (
        <p
          role="status"
          className="rounded-xl border border-amber-500/25 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300"
        >
          AI 分析不可用，计划可能不完整；以下为已生成的用例与变更。
        </p>
      ) : null}

      <div className="grid gap-3 lg:grid-cols-2">
        <div className="space-y-1.5">
          <h3 className="text-sm font-medium">{`新增用例（${plan.newCases.length}）`}</h3>
          {plan.newCases.length === 0 ? (
            <p className="text-xs text-muted-foreground">本次补全没有新增用例。</p>
          ) : (
            plan.newCases.map((item) => (
              <NewCaseRow
                key={item.nodeId}
                item={item}
                checked={selected.has(item.nodeId)}
                onToggle={toggleCase}
              />
            ))
          )}
        </div>

        <div className="space-y-1.5">
          <h3 className="text-sm font-medium">{`受影响用例（${plan.affectedCases.length}）`}</h3>
          {plan.affectedCases.length === 0 ? (
            <p className="text-xs text-muted-foreground">没有需要重新验证的受影响用例。</p>
          ) : (
            plan.affectedCases.map((item) => (
              <AffectedCaseRow
                key={item.nodeId}
                item={item}
                checked={selected.has(item.nodeId)}
                onToggle={toggleCase}
              />
            ))
          )}
        </div>
      </div>

      {plan.unmappedChanges.length > 0 ? (
        <div
          data-testid="incremental-plan-unmapped"
          className="rounded-xl border border-amber-500/25 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300"
        >
          <h3 className="flex items-center gap-1 font-medium">
            <AlertTriangle className="h-3.5 w-3.5" aria-hidden="true" />
            未映射变更
          </h3>
          <ul className="mt-1 list-disc space-y-0.5 pl-5">
            {plan.unmappedChanges.map((change) => (
              <li key={`${change.path}:${change.summary}`}>
                <span className="font-mono">{change.path}</span>
                {change.summary === "" ? null : ` · ${change.summary}`}
              </li>
            ))}
          </ul>
        </div>
      ) : null}

      {plan.warnings.length > 0 ? (
        <ul className="space-y-1">
          {plan.warnings.map((warning) => (
            <li
              key={`${warning.code}:${warning.message}`}
              data-testid="incremental-plan-warning"
              className="rounded-xl border border-amber-500/25 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300"
            >
              {warning.message === "" ? warning.code : warning.message}
            </li>
          ))}
        </ul>
      ) : null}

      {actionError === "" ? null : (
        <p
          role="alert"
          className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
        >
          {actionError}
        </p>
      )}

      {createdBatch === null ? null : (
        <p
          role="status"
          className="rounded-xl border border-emerald-500/30 bg-emerald-500/10 px-3 py-2 text-xs text-emerald-700 dark:text-emerald-300"
        >
          {`已创建增量批次 Batch ${createdBatch.batch_no}（${createdBatch.status}）。`}
        </p>
      )}

      <div className="flex flex-wrap items-center justify-end gap-1.5">
        {plan.parentBatchId === null ? (
          <p className="mr-auto text-xs text-amber-700 dark:text-amber-300">
            缺少父批次，暂时无法创建增量批次。
          </p>
        ) : null}
        <button
          type="button"
          data-testid="incremental-plan-create-batch"
          onClick={() => void handleCreate()}
          disabled={!canCreate}
          className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3.5 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          {saving ? (
            <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
          ) : (
            <Plus className="h-4 w-4" aria-hidden="true" />
          )}
          {saving ? "创建中…" : "创建增量批次"}
        </button>
      </div>
    </section>
  )
}
