"use client"

import { ExternalLink, X } from "lucide-react"
import Link from "next/link"
import { Fragment, useEffect, useState } from "react"

import { Badge, type BadgeTone } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { Dialog } from "@/components/ui/Dialog"
import { ErrorState, InlineError } from "@/components/ui/ErrorState"
import { Field } from "@/components/ui/Field"
import { LoadingCard } from "@/components/ui/Spinner"
import { Textarea } from "@/components/ui/Textarea"
import { AnchorPanel } from "@/features/anchors/AnchorPanel"
import { formatDateTime } from "@/lib/format"

import {
  ISSUE_ACTOR_LABELS,
  ISSUE_CATEGORY_LABELS,
  ISSUE_REF_LABELS,
  ISSUE_SEVERITY_LABELS,
  ISSUE_STATUS_LABELS,
  type IssueDetailView,
  type IssueHistoryView,
  type IssueRef,
  type IssueRefKind,
  type IssueStatus,
} from "./types"

export type IssueTransitionAction = "confirm" | "dismiss" | "reopen"

export type IssueDetailSheetProps = {
  versionId: string
  issue: IssueDetailView | null
  loading: boolean
  error: unknown
  /** Global issues reuse the sheet without transitions, merge or anchors. */
  readOnly?: boolean
  onClose: () => void
  onRetry: () => void
  /** Resolves when the transition succeeded; throws ApiError otherwise. */
  onTransition: (action: IssueTransitionAction, reason: string) => Promise<void>
  onMerge: () => void
}

const STATUS_TONE: Record<IssueStatus, BadgeTone> = {
  open: "warning",
  confirmed: "success",
  dismissed: "neutral",
  merged: "info",
}

type TransitionMeta = {
  title: string
  description: string
  reasonLabel: string
  hint: string
  submitLabel: string
  required: boolean
}

const TRANSITION_META: Record<IssueTransitionAction, TransitionMeta> = {
  confirm: {
    title: "确认问题",
    description: "确认后问题进入已确认状态，不会自动触发重测。",
    reasonLabel: "确认原因",
    hint: "可选；会写入状态历史与审计。",
    submitLabel: "确认问题",
    required: false,
  },
  dismiss: {
    title: "驳回问题",
    description: "驳回后问题进入已驳回状态，可在需要时重新打开。",
    reasonLabel: "驳回原因",
    hint: "可选；会写入状态历史与审计。",
    submitLabel: "驳回问题",
    required: false,
  },
  reopen: {
    title: "重开问题",
    description: "重开后问题回到待确认状态，reopen_count 加一。",
    reasonLabel: "重开原因",
    hint: "必填（1–2000 字）；会写入状态历史与事件。",
    submitLabel: "重开问题",
    required: true,
  },
}

function issueHref(versionId: string, issueId: string): string {
  const params = new URLSearchParams({ tab: "issues", issue: issueId })
  return `/versions/${encodeURIComponent(versionId)}?${params.toString()}`
}

function executionHref(
  versionId: string,
  refs: IssueRef[],
  kind: "case_run" | "wave" | "evidence",
  refId: string,
): string {
  const caseRunId = refs.find((ref) => ref.kind === "case_run")?.ref_id
  const waveId = refs.find((ref) => ref.kind === "wave")?.ref_id
  const params = new URLSearchParams({ tab: "execution" })
  if (kind === "wave") {
    params.set("wave", refId)
    return `/versions/${encodeURIComponent(versionId)}?${params.toString()}`
  }
  if (waveId !== undefined) params.set("wave", waveId)
  if (kind === "case_run") {
    params.set("case_run", refId)
  } else {
    if (caseRunId !== undefined) params.set("case_run", caseRunId)
    params.set("evidence", refId)
  }
  return `/versions/${encodeURIComponent(versionId)}?${params.toString()}`
}

/** Builds the destination for one issue ref; `null` means "no deep link". */
function refHref(versionId: string, refs: IssueRef[], ref: IssueRef): string | null {
  switch (ref.kind) {
    case "case_run":
    case "wave":
    case "evidence":
      return executionHref(versionId, refs, ref.kind, ref.ref_id)
    case "report": {
      const params = new URLSearchParams({ tab: "reports", report: ref.ref_id })
      return `/versions/${encodeURIComponent(versionId)}?${params.toString()}`
    }
    case "requirement": {
      const params = new URLSearchParams({ tab: "requirements", requirement: ref.ref_id })
      return `/versions/${encodeURIComponent(versionId)}?${params.toString()}`
    }
    case "case_version": {
      const params = new URLSearchParams({ tab: "design" })
      return `/versions/${encodeURIComponent(versionId)}?${params.toString()}`
    }
    default:
      return null
  }
}

function refLabel(kind: IssueRefKind): string {
  return ISSUE_REF_LABELS[kind]
}

function relatedRefLabel(kind: string): string {
  return ISSUE_REF_LABELS[kind as IssueRefKind] ?? kind
}

function HistoryRow({ entry }: { entry: IssueHistoryView }) {
  const from = entry.from_status === null ? "创建" : ISSUE_STATUS_LABELS[entry.from_status]
  return (
    <li className="rounded-lg border border-border bg-muted/30 px-3 py-2">
      <p className="text-xs font-medium text-foreground">
        {from} → {ISSUE_STATUS_LABELS[entry.to_status]}
      </p>
      <p className="mt-0.5 text-[11px] text-muted-foreground">{entry.reason || "（无原因）"}</p>
      <p className="mt-0.5 text-[11px] text-muted-foreground">
        {ISSUE_ACTOR_LABELS[entry.actor_kind]} · {formatDateTime(entry.created_at)}
      </p>
    </li>
  )
}

function RefsSection({ versionId, issue }: { versionId: string; issue: IssueDetailView }) {
  const counts = Object.entries(issue.related_refs)
  return (
    <section data-testid="issue-refs" aria-label="关联引用" className="space-y-2">
      <h3 className="text-xs font-semibold text-foreground">关联引用</h3>
      {counts.length > 0 ? (
        <div className="flex flex-wrap gap-1.5">
          {counts.map(([kind, count]) => (
            <Badge key={kind} tone="neutral">
              {relatedRefLabel(kind)} {count}
            </Badge>
          ))}
        </div>
      ) : null}
      {issue.refs.length === 0 ? (
        <p className="text-[11px] text-muted-foreground">暂无关联引用。</p>
      ) : (
        <ul className="space-y-1.5">
          {issue.refs.map((ref) => {
            const href = refHref(versionId, issue.refs, ref)
            const label = `${refLabel(ref.kind)} #${ref.ref_id}`
            return (
              <li key={`${ref.kind}-${ref.ref_id}-${ref.role}`} className="flex items-center gap-2">
                {href === null ? (
                  <span className="text-xs text-foreground">{label}</span>
                ) : (
                  <Link
                    href={href}
                    className="inline-flex items-center gap-1 text-xs text-primary underline-offset-2 hover:underline"
                  >
                    {label}
                    <ExternalLink className="size-3" aria-hidden="true" />
                  </Link>
                )}
                <span className="text-[11px] text-muted-foreground">{ref.role}</span>
              </li>
            )
          })}
        </ul>
      )}
    </section>
  )
}

function TriageSection({ versionId, issue }: { versionId: string; issue: IssueDetailView }) {
  const triage = issue.triage
  // P4 has no code anchor: triage only shows the analysis-run projection (P5).
  if (triage === null || triage === undefined) return null
  return (
    <section data-testid="issue-triage" aria-label="分诊摘要" className="space-y-2">
      <h3 className="text-xs font-semibold text-foreground">分诊摘要</h3>
      <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-[11px]">
        {triage.status !== null && triage.status !== undefined ? (
          <>
            <dt className="text-muted-foreground">分诊状态</dt>
            <dd className="text-foreground">{triage.status}</dd>
          </>
        ) : null}
        {triage.classification !== null && triage.classification !== undefined ? (
          <>
            <dt className="text-muted-foreground">建议分类</dt>
            <dd className="text-foreground">{triage.classification}</dd>
          </>
        ) : null}
        {triage.duplicate_of_issue_id !== null && triage.duplicate_of_issue_id !== undefined ? (
          <>
            <dt className="text-muted-foreground">重复建议</dt>
            <dd>
              <Link
                href={issueHref(versionId, triage.duplicate_of_issue_id)}
                className="text-primary underline-offset-2 hover:underline"
              >
                问题 #{triage.duplicate_of_issue_id}
              </Link>
            </dd>
          </>
        ) : null}
        {triage.summary !== null && triage.summary !== undefined && triage.summary.length > 0 ? (
          <>
            <dt className="text-muted-foreground">摘要</dt>
            <dd className="text-foreground">{triage.summary}</dd>
          </>
        ) : null}
      </dl>
    </section>
  )
}

type TransitionDialogProps = {
  action: IssueTransitionAction | null
  issue: IssueDetailView | null
  onClose: () => void
  onSubmit: (action: IssueTransitionAction, reason: string) => Promise<void>
}

function TransitionDialog({ action, issue, onClose, onSubmit }: TransitionDialogProps) {
  const [reason, setReason] = useState("")
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<unknown>(null)

  useEffect(() => {
    if (action === null) return
    setReason("")
    setSubmitting(false)
    setError(null)
  }, [action])

  if (action === null || issue === null) return null
  const meta = TRANSITION_META[action]
  const trimmed = reason.trim()
  const disabled = submitting || (meta.required && trimmed.length === 0)

  async function submit(): Promise<void> {
    if (disabled) return
    setSubmitting(true)
    setError(null)
    try {
      await onSubmit(action as IssueTransitionAction, trimmed)
      onClose()
    } catch (cause) {
      setError(cause)
      setSubmitting(false)
    }
  }

  return (
    <Dialog
      open
      onClose={onClose}
      title={meta.title}
      description={meta.description}
      footer={
        <>
          <Button variant="secondary" size="sm" disabled={submitting} onClick={onClose}>
            取消
          </Button>
          <Button
            size="sm"
            disabled={disabled}
            onClick={() => {
              void submit()
            }}
          >
            {submitting ? "提交中…" : meta.submitLabel}
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <Field
          id={`issue-${action}-reason`}
          label={meta.reasonLabel}
          required={meta.required}
          hint={meta.hint}
        >
          <Textarea
            id={`issue-${action}-reason`}
            value={reason}
            maxLength={2000}
            disabled={submitting}
            placeholder="补充说明（最多 2000 字）"
            onChange={(event) => setReason(event.target.value)}
          />
        </Field>
        {error !== null ? <InlineError error={error} /> : null}
      </div>
    </Dialog>
  )
}

/**
 * Right-hand issue detail sheet (spec §9.2, width ≥360). The issues tab owns
 * data loading; the sheet renders refs/history/triage and the confirm, dismiss,
 * reopen and merge entries. `?issue=` in the URL is the deep-link truth.
 */
export function IssueDetailSheet({
  versionId,
  issue,
  loading,
  error,
  readOnly = false,
  onClose,
  onRetry,
  onTransition,
  onMerge,
}: IssueDetailSheetProps) {
  const [transition, setTransition] = useState<IssueTransitionAction | null>(null)

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape" && transition === null) onClose()
    }
    window.addEventListener("keydown", onKeyDown)
    return () => window.removeEventListener("keydown", onKeyDown)
  }, [onClose, transition])

  const canMerge =
    issue !== null &&
    (issue.status === "open" || issue.status === "confirmed" || issue.status === "dismissed")
  const recurrenceCount = issue?.recurrence_count ?? 0
  const lastRecurredAt = issue?.last_recurred_at ?? null
  const mergedChain = issue?.merged_chain ?? []
  const mergedIntoId = issue?.merged_into_id ?? null

  return (
    <div className="fixed inset-0 z-40">
      <button
        type="button"
        aria-label="关闭详情遮罩"
        onClick={onClose}
        className="absolute inset-0 h-full w-full bg-slate-950/40 backdrop-blur-[1px]"
      />
      <aside
        role="dialog"
        aria-modal="true"
        aria-label="问题详情"
        data-testid="issue-detail-sheet"
        className="absolute inset-y-0 right-0 flex w-full max-w-md flex-col border-l border-border bg-card text-card-foreground shadow-2xl"
      >
        <header className="flex items-start justify-between gap-3 border-b border-border px-5 py-4">
          <div className="min-w-0">
            <p className="text-[11px] text-muted-foreground">
              {issue === null ? "问题详情" : `问题 #${issue.id}`}
            </p>
            <h2 className="truncate text-sm font-semibold text-foreground">
              {issue?.title ?? "正在加载问题…"}
            </h2>
            {issue !== null ? (
              <div className="mt-2 flex flex-wrap items-center gap-1.5">
                <Badge tone={STATUS_TONE[issue.status]}>{ISSUE_STATUS_LABELS[issue.status]}</Badge>
                <Badge tone="neutral">{ISSUE_CATEGORY_LABELS[issue.category]}</Badge>
                <Badge tone="neutral">{ISSUE_SEVERITY_LABELS[issue.severity]}</Badge>
                <span className="text-[11px] text-muted-foreground">
                  重开 {issue.reopen_count} 次
                </span>
                {recurrenceCount > 0 ? (
                  <span className="text-[11px] text-muted-foreground">
                    复发 {recurrenceCount} 次
                  </span>
                ) : null}
              </div>
            ) : null}
          </div>
          <Button variant="ghost" size="sm" aria-label="关闭详情" onClick={onClose}>
            <X className="size-4" aria-hidden="true" />
          </Button>
        </header>

        <div className="flex-1 space-y-4 overflow-y-auto px-5 py-4">
          {loading ? (
            <LoadingCard label="正在加载问题详情…" />
          ) : error !== null ? (
            <ErrorState error={error} title="问题详情加载失败" onRetry={onRetry} />
          ) : issue === null ? (
            <p className="text-xs text-muted-foreground">问题不存在或已被移除。</p>
          ) : (
            <>
              <section aria-label="问题摘要" className="space-y-1">
                <h3 className="text-xs font-semibold text-foreground">摘要</h3>
                <p className="text-xs leading-relaxed text-foreground">
                  {issue.summary.length > 0 ? issue.summary : "（无摘要）"}
                </p>
                <p className="text-[11px] text-muted-foreground">
                  更新于 {formatDateTime(issue.updated_at)} · 来源 {issue.source}
                </p>
              </section>

              {recurrenceCount > 0 ? (
                <section
                  data-testid="issue-recurrence"
                  aria-label="复发信息"
                  className="rounded-lg border border-warning/30 bg-warning/10 px-3 py-2 text-xs text-warning"
                >
                  <p className="font-medium">复发 {recurrenceCount} 次</p>
                  {lastRecurredAt !== null ? (
                    <p className="mt-0.5 text-[11px]">
                      最近复发于 {formatDateTime(lastRecurredAt)}
                    </p>
                  ) : null}
                  <p className="mt-0.5 text-[11px]">
                    相同去重键的新失败会重定向到该问题，并追加复发引用。
                  </p>
                </section>
              ) : null}

              {issue.status === "merged" &&
              (mergedChain.length > 0 || (mergedIntoId !== null && mergedIntoId !== undefined)) ? (
                <div
                  data-testid="issue-merged-chain"
                  className="rounded-lg border border-border bg-muted/40 px-3 py-2 text-xs text-foreground"
                >
                  <p>
                    该问题已合并
                    {issue.merged_at !== null && issue.merged_at !== undefined
                      ? `（${formatDateTime(issue.merged_at)}）`
                      : null}
                  </p>
                  {mergedChain.length > 0 ? (
                    <p className="mt-1 flex flex-wrap items-center gap-1 text-[11px]">
                      <span className="text-muted-foreground">合并链：</span>
                      {mergedChain.map((chainIssueId, index) => (
                        <Fragment key={chainIssueId}>
                          {index > 0 ? <span aria-hidden="true">→</span> : null}
                          <Link
                            href={issueHref(versionId, chainIssueId)}
                            className="text-primary underline-offset-2 hover:underline"
                          >
                            #{chainIssueId}
                          </Link>
                        </Fragment>
                      ))}
                    </p>
                  ) : mergedIntoId !== null && mergedIntoId !== undefined ? (
                    <p className="mt-1 text-[11px]">
                      目标问题{" "}
                      <Link
                        href={issueHref(versionId, mergedIntoId)}
                        className="text-primary underline-offset-2 hover:underline"
                      >
                        #{mergedIntoId}
                      </Link>
                    </p>
                  ) : null}
                </div>
              ) : null}

              {readOnly ? (
                <p className="rounded-lg border border-border bg-muted/40 px-3 py-2 text-xs text-muted-foreground">
                  只读视图；请在所属版本工作区处理该问题。
                </p>
              ) : (
                <div className="flex flex-wrap items-center gap-2">
                  {issue.status === "open" ? (
                    <>
                      <Button size="sm" onClick={() => setTransition("confirm")}>
                        确认
                      </Button>
                      <Button
                        variant="secondary"
                        size="sm"
                        onClick={() => setTransition("dismiss")}
                      >
                        驳回
                      </Button>
                    </>
                  ) : null}
                  {issue.status === "confirmed" || issue.status === "dismissed" ? (
                    <Button size="sm" onClick={() => setTransition("reopen")}>
                      重开
                    </Button>
                  ) : null}
                  {canMerge ? (
                    <Button variant="secondary" size="sm" onClick={onMerge}>
                      合并
                    </Button>
                  ) : null}
                </div>
              )}

              <RefsSection versionId={versionId} issue={issue} />

              {readOnly ? null : (
                <section aria-label="问题代码锚点" className="border-t border-border pt-3">
                  <AnchorPanel
                    versionId={versionId}
                    subjectKind="issue"
                    subjectId={issue.id}
                    title="问题代码锚点"
                  />
                </section>
              )}

              <section data-testid="issue-history" aria-label="状态历史" className="space-y-2">
                <h3 className="text-xs font-semibold text-foreground">状态历史</h3>
                {issue.history.length === 0 ? (
                  <p className="text-[11px] text-muted-foreground">暂无状态历史。</p>
                ) : (
                  <ol className="space-y-1.5">
                    {issue.history.map((entry) => (
                      <HistoryRow
                        key={`${entry.created_at}-${entry.to_status}-${entry.actor_kind}-${entry.reason}`}
                        entry={entry}
                      />
                    ))}
                  </ol>
                )}
              </section>

              <TriageSection versionId={versionId} issue={issue} />
            </>
          )}
        </div>
      </aside>

      <TransitionDialog
        action={transition}
        issue={issue}
        onClose={() => setTransition(null)}
        onSubmit={onTransition}
      />
    </div>
  )
}
