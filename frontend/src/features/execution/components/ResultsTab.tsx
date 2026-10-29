"use client"

import { ChevronDown, ChevronRight, Loader2, RefreshCw } from "lucide-react"
import { Fragment, useMemo, useState } from "react"

import { StatusBadge, type StatusBadgeVariant } from "@/components/ui/StatusBadge"
import type { VersionStreamState } from "@/features/versions/useVersionStream"

import { useCaseRun, useCaseRuns, useExecutionActions } from "../hooks/useExecution"
import type { CaseRun, CaseRunStatus, EvidenceView } from "../types"
import { AssertionRows } from "./AssertionRows"
import { EvidenceViewer } from "./EvidenceViewer"
import { FailureClassBadge } from "./FailureClassBadge"
import { EvidenceThumb, StepDetail } from "./StepDetail"

/** Chinese copy of the eight case-run statuses (spec §5.8). */
const CASE_STATUS_LABELS: Record<CaseRunStatus, string> = {
  pending: "待执行",
  running: "执行中",
  passed: "通过",
  failed: "失败",
  blocked: "阻塞",
  inconclusive: "不确定",
  skipped: "跳过",
  interrupted: "已中断",
}

const CASE_STATUS_VARIANTS: Record<CaseRunStatus, StatusBadgeVariant> = {
  pending: "neutral",
  running: "info",
  passed: "success",
  failed: "danger",
  blocked: "warning",
  inconclusive: "info",
  skipped: "neutral",
  interrupted: "warning",
}

/** Case-run statuses after which a rerun is allowed (spec §9.3, §6.4). */
export const TERMINAL_CASE_STATUSES: readonly CaseRunStatus[] = [
  "passed",
  "failed",
  "blocked",
  "inconclusive",
  "skipped",
  "interrupted",
]

/** Failed and blocked runs are pinned above the rest; ties keep server order. */
const STATUS_PRIORITY: Partial<Record<CaseRunStatus, number>> = {
  failed: 0,
  blocked: 1,
}

export function isTerminalCaseStatus(status: CaseRunStatus): boolean {
  return TERMINAL_CASE_STATUSES.includes(status)
}

/** Sorts failed first, then blocked, then the remaining runs in server order. */
export function sortCaseRuns(caseRuns: CaseRun[]): CaseRun[] {
  return caseRuns
    .map((run, index) => ({ run, index }))
    .sort((left, right) => {
      const delta =
        (STATUS_PRIORITY[left.run.status] ?? 2) - (STATUS_PRIORITY[right.run.status] ?? 2)
      return delta === 0 ? left.index - right.index : delta
    })
    .map((entry) => entry.run)
}

/** Compact duration; unknown/zero durations render an em dash, not "0 s". */
export function formatDuration(durationMs: number): string {
  if (!Number.isFinite(durationMs) || durationMs <= 0) return "—"
  if (durationMs < 1000) return `${Math.round(durationMs)} ms`
  const seconds = durationMs / 1000
  if (seconds < 60) return `${seconds.toFixed(1)} s`
  const minutes = Math.floor(seconds / 60)
  const rest = Math.round(seconds - minutes * 60)
  return `${minutes} 分 ${rest} 秒`
}

function caseTitle(run: CaseRun): string {
  return run.title === "" ? run.case_node_id : run.title
}

type CaseRunRowProps = {
  run: CaseRun
  batchLabel: string
  expanded: boolean
  rerunning: boolean
  onToggle: () => void
  onRerun: () => void
}

/** One results row: case, status, duration, counts, failure class, batch, action. */
function CaseRunRow({ run, batchLabel, expanded, rerunning, onToggle, onRerun }: CaseRunRowProps) {
  const title = caseTitle(run)

  return (
    <tr
      data-testid={`case-run-row-${run.id}`}
      data-status={run.status}
      className="border-t border-border align-top transition-colors hover:bg-muted/20"
    >
      <td className="px-3 py-2">
        <div className="flex items-start gap-1.5">
          <button
            type="button"
            onClick={onToggle}
            aria-expanded={expanded}
            aria-label={`${expanded ? "收起" : "展开"} ${title}`}
            className="mt-0.5 rounded p-0.5 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            {expanded ? (
              <ChevronDown className="h-3.5 w-3.5" aria-hidden="true" />
            ) : (
              <ChevronRight className="h-3.5 w-3.5" aria-hidden="true" />
            )}
          </button>
          <div className="min-w-0">
            <p className="text-sm font-medium text-foreground">{title}</p>
            <p className="mt-0.5 flex flex-wrap gap-x-2 text-[11px] text-muted-foreground">
              <span>第 {run.attempt} 次尝试</span>
              {run.summary === "" ? null : <span>{run.summary}</span>}
            </p>
          </div>
        </div>
      </td>
      <td className="px-3 py-2">
        <StatusBadge variant={CASE_STATUS_VARIANTS[run.status]}>
          {CASE_STATUS_LABELS[run.status]}
        </StatusBadge>
      </td>
      <td className="px-3 py-2 text-xs text-muted-foreground">{formatDuration(run.duration_ms)}</td>
      <td className="px-3 py-2 text-xs text-foreground">{run.steps.length || "—"}</td>
      <td className="px-3 py-2 text-xs text-foreground">{run.assertions.length || "—"}</td>
      <td className="px-3 py-2 text-xs text-foreground">{run.evidence.length || "—"}</td>
      <td className="px-3 py-2">
        <FailureClassBadge failureClass={run.failure_class} />
      </td>
      <td className="px-3 py-2 text-xs text-muted-foreground">{batchLabel}</td>
      <td className="px-3 py-2">
        {isTerminalCaseStatus(run.status) ? (
          <button
            type="button"
            onClick={onRerun}
            disabled={rerunning}
            className="inline-flex items-center gap-1 rounded-lg border border-border bg-card px-2.5 py-1 text-xs font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
          >
            {rerunning ? (
              <Loader2 className="h-3 w-3 animate-spin" aria-hidden="true" />
            ) : (
              <RefreshCw className="h-3 w-3" aria-hidden="true" />
            )}
            {rerunning ? "重跑中…" : "重跑"}
          </button>
        ) : (
          <span className="text-xs text-muted-foreground">—</span>
        )}
      </td>
    </tr>
  )
}

type CaseRunDetailBodyProps = {
  caseRun: CaseRun
  onOpenEvidence: (evidenceId: string) => void
}

/** Steps, assertions and any case-level evidence of one expanded case run. */
function CaseRunDetailBody({ caseRun, onOpenEvidence }: CaseRunDetailBodyProps) {
  const referencedEvidenceIds = new Set<string>()
  for (const step of caseRun.steps) {
    for (const evidenceId of step.evidence_ids) referencedEvidenceIds.add(evidenceId)
  }
  for (const assertion of caseRun.assertions) {
    for (const evidenceId of assertion.evidence_ids) referencedEvidenceIds.add(evidenceId)
  }
  const caseLevelEvidence = caseRun.evidence.filter((item) => !referencedEvidenceIds.has(item.id))

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap gap-x-4 gap-y-1 text-[11px] text-muted-foreground">
        <span>用例修订 {caseRun.case_revision}</span>
        {caseRun.session === null ? null : <span>会话：{caseRun.session.account}</span>}
        {caseRun.pending_takeover === null ? null : (
          <span>待人工处理：{caseRun.pending_takeover.reason}</span>
        )}
        {caseRun.failure_reason === "" ? null : <span>失败原因：{caseRun.failure_reason}</span>}
      </div>

      <section aria-label="步骤结果" className="space-y-1.5">
        <h3 className="text-xs font-semibold text-muted-foreground">步骤</h3>
        {caseRun.steps.length === 0 ? (
          <p className="text-xs text-muted-foreground">本用例没有记录步骤。</p>
        ) : (
          <div className="overflow-x-auto rounded-lg border border-border">
            <table className="w-full min-w-[640px] text-left text-xs">
              <caption className="sr-only">步骤结果</caption>
              <thead className="bg-muted/50 text-muted-foreground">
                <tr>
                  <th scope="col" className="px-3 py-1.5 font-medium">
                    序号
                  </th>
                  <th scope="col" className="px-3 py-1.5 font-medium">
                    动作 · 期望/实际
                  </th>
                  <th scope="col" className="px-3 py-1.5 font-medium">
                    状态
                  </th>
                  <th scope="col" className="px-3 py-1.5 font-medium">
                    证据
                  </th>
                </tr>
              </thead>
              <tbody>
                {caseRun.steps.map((step) => (
                  <StepDetail
                    key={step.seq}
                    step={step}
                    evidence={caseRun.evidence}
                    onOpenEvidence={onOpenEvidence}
                  />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <section aria-label="断言结果" className="space-y-1.5">
        <h3 className="text-xs font-semibold text-muted-foreground">断言</h3>
        {caseRun.assertions.length === 0 ? (
          <p className="text-xs text-muted-foreground">本用例没有记录断言。</p>
        ) : (
          <div className="overflow-x-auto rounded-lg border border-border">
            <table className="w-full min-w-[640px] text-left text-xs">
              <caption className="sr-only">断言结果</caption>
              <thead className="bg-muted/50 text-muted-foreground">
                <tr>
                  <th scope="col" className="px-3 py-1.5 font-medium">
                    断言
                  </th>
                  <th scope="col" className="px-3 py-1.5 font-medium">
                    期望 vs 实际
                  </th>
                  <th scope="col" className="px-3 py-1.5 font-medium">
                    判定
                  </th>
                  <th scope="col" className="px-3 py-1.5 font-medium">
                    证据
                  </th>
                </tr>
              </thead>
              <tbody>
                <AssertionRows
                  assertions={caseRun.assertions}
                  evidence={caseRun.evidence}
                  onOpenEvidence={onOpenEvidence}
                />
              </tbody>
            </table>
          </div>
        )}
      </section>

      {caseLevelEvidence.length === 0 ? null : (
        <section aria-label="用例级证据" className="space-y-1.5">
          <h3 className="text-xs font-semibold text-muted-foreground">其他证据</h3>
          <div className="flex flex-wrap gap-1">
            {caseLevelEvidence.map((item) => (
              <EvidenceThumb
                key={item.id}
                evidenceId={item.id}
                kind={item.kind}
                onOpen={onOpenEvidence}
              />
            ))}
          </div>
        </section>
      )}
    </div>
  )
}

type CaseRunDetailProps = {
  versionId: string
  batchId: string
  caseRunId: string
  stream?: VersionStreamState
}

/** Expanded row: loads the case-run detail lazily and hosts the evidence viewer. */
function CaseRunDetail({ versionId, batchId, caseRunId, stream }: CaseRunDetailProps) {
  const { caseRun, loading, error, refresh } = useCaseRun(versionId, batchId, caseRunId, stream)
  const [selectedEvidence, setSelectedEvidence] = useState<EvidenceView | null>(null)

  function openEvidence(evidenceId: string): void {
    const known = caseRun?.evidence.find((item) => item.id === evidenceId)
    setSelectedEvidence(known ?? { id: evidenceId, kind: "", mime: "", size_bytes: 0, sha256: "" })
  }

  return (
    <tr data-testid={`case-run-detail-${caseRunId}`} className="border-t border-border bg-muted/20">
      <td colSpan={9} className="px-4 py-3">
        {loading ? (
          <p role="status" className="flex items-center gap-2 text-xs text-muted-foreground">
            <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
            正在加载用例详情…
          </p>
        ) : error !== "" ? (
          <div
            role="alert"
            className="space-y-3 rounded-xl border border-destructive/30 bg-destructive/10 px-4 py-3 text-sm text-destructive"
          >
            <p>加载用例详情失败：{error}</p>
            <button
              type="button"
              onClick={() => void refresh()}
              className="rounded-lg border border-border bg-card px-3 py-1.5 text-xs font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              重试
            </button>
          </div>
        ) : caseRun === null ? (
          <p className="text-xs text-muted-foreground">没有可展示的用例详情。</p>
        ) : (
          <CaseRunDetailBody caseRun={caseRun} onOpenEvidence={openEvidence} />
        )}

        {selectedEvidence === null ? null : (
          <EvidenceViewer
            versionId={versionId}
            evidenceId={selectedEvidence.id}
            evidence={selectedEvidence}
            onClose={() => setSelectedEvidence(null)}
          />
        )}
      </td>
    </tr>
  )
}

export type ResultsTabProps = {
  versionId: string
  batchId: string
  /** Batch number for the 批次 column; the raw ID is shown when omitted. */
  batchNo?: number
  /** Version stream owned by the tab container; the tab never opens its own. */
  stream?: VersionStreamState
}

/**
 * Results and evidence tab (spec §9.3): one row per case run with failed and
 * blocked runs pinned first, a lazily expanded `getCaseRun` detail showing the
 * expected vs actual of every step/assertion, evidence thumbnails and a rerun
 * action for terminal runs.
 */
export function ResultsTab({ versionId, batchId, batchNo, stream }: ResultsTabProps) {
  const { caseRuns, loading, error, refresh } = useCaseRuns(versionId, batchId, {}, stream)
  const actions = useExecutionActions(versionId, { onChanged: refresh })
  const [expandedId, setExpandedId] = useState("")

  const ordered = useMemo(() => sortCaseRuns(caseRuns), [caseRuns])
  const batchLabel = batchNo === undefined ? batchId : `#${batchNo}`

  return (
    <section data-testid="results-tab" className="space-y-3">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <h2 className="text-base font-semibold tracking-tight">结果与证据</h2>
          <p className="mt-0.5 text-xs text-muted-foreground">
            失败与阻塞用例置顶；展开行查看步骤、断言与证据详情。
          </p>
        </div>
        <button
          type="button"
          onClick={() => void refresh()}
          disabled={loading}
          className="inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-1.5 text-xs font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
        >
          <RefreshCw className="h-3.5 w-3.5" aria-hidden="true" />
          刷新
        </button>
      </div>

      {actions.error === "" ? null : (
        <p
          role="alert"
          className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
        >
          重跑失败：{actions.error}
        </p>
      )}

      {loading ? (
        <p
          role="status"
          className="flex items-center gap-2 rounded-xl border border-dashed border-border px-4 py-6 text-sm text-muted-foreground"
        >
          <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
          执行结果加载中…
        </p>
      ) : error !== "" ? (
        <div
          role="alert"
          className="space-y-3 rounded-xl border border-destructive/30 bg-destructive/10 px-4 py-4 text-sm text-destructive"
        >
          <p>加载执行结果失败：{error}</p>
          <button
            type="button"
            onClick={() => void refresh()}
            className="rounded-lg border border-border bg-card px-3 py-1.5 text-xs font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            重试
          </button>
        </div>
      ) : ordered.length === 0 ? (
        <p className="rounded-xl border border-dashed border-border px-4 py-8 text-center text-sm text-muted-foreground">
          暂无用例执行结果。
        </p>
      ) : (
        <div className="overflow-x-auto rounded-xl border border-border">
          <table className="w-full min-w-[960px] text-left text-sm">
            <caption className="sr-only">用例执行结果</caption>
            <thead className="bg-muted/50 text-xs text-muted-foreground">
              <tr>
                <th scope="col" className="px-3 py-2 font-medium">
                  用例
                </th>
                <th scope="col" className="px-3 py-2 font-medium">
                  状态
                </th>
                <th scope="col" className="px-3 py-2 font-medium">
                  耗时
                </th>
                <th scope="col" className="px-3 py-2 font-medium">
                  步骤
                </th>
                <th scope="col" className="px-3 py-2 font-medium">
                  断言
                </th>
                <th scope="col" className="px-3 py-2 font-medium">
                  证据
                </th>
                <th scope="col" className="px-3 py-2 font-medium">
                  失败分类
                </th>
                <th scope="col" className="px-3 py-2 font-medium">
                  批次
                </th>
                <th scope="col" className="px-3 py-2 font-medium">
                  操作
                </th>
              </tr>
            </thead>
            <tbody>
              {ordered.map((run) => (
                <Fragment key={run.id}>
                  <CaseRunRow
                    run={run}
                    batchLabel={batchLabel}
                    expanded={expandedId === run.id}
                    rerunning={actions.pending === "rerun"}
                    onToggle={() => setExpandedId((current) => (current === run.id ? "" : run.id))}
                    onRerun={() => {
                      void actions.rerun(batchId, run.id)
                    }}
                  />
                  {expandedId === run.id ? (
                    <CaseRunDetail
                      versionId={versionId}
                      batchId={batchId}
                      caseRunId={run.id}
                      stream={stream}
                    />
                  ) : null}
                </Fragment>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  )
}
