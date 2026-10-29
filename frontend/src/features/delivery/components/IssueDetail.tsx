"use client"

import { Copy, X } from "lucide-react"
import { useState } from "react"

import { StatusBadge } from "@/components/ui/StatusBadge"

import {
  ISSUE_CATEGORY_LABELS,
  ISSUE_CONFIDENCE_LABELS,
  ISSUE_SEVERITY_LABELS,
  ISSUE_SOURCE_LABELS,
  ISSUE_STATUS_LABELS,
  issueStatusVariant,
  labelOf,
  severityVariant,
} from "../labels"
import type { IssueCodeRef, IssueDetail as IssueDetailView } from "../types"
import { IssueRefs } from "./IssueRefs"

const BUTTON_CLASS =
  "inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"

/** Plain-text Markdown/工单 copy of one issue; it never calls an external tool. */
function issueMarkdown(issue: IssueDetailView): string {
  const lines = [
    `# ${issue.title}`,
    "",
    `- 严重级：${labelOf(ISSUE_SEVERITY_LABELS, issue.severity)}`,
    `- 状态：${labelOf(ISSUE_STATUS_LABELS, issue.status)}`,
    `- 置信度：${labelOf(ISSUE_CONFIDENCE_LABELS, issue.confidence)}`,
    `- 分类：${labelOf(ISSUE_CATEGORY_LABELS, issue.category)}`,
    `- 来源：${labelOf(ISSUE_SOURCE_LABELS, issue.source)}`,
    `- 复发次数：${issue.recurrenceCount}`,
  ]
  if (issue.description !== "") lines.push("", "## 描述", "", issue.description)
  if (issue.reproSteps !== "") lines.push("", "## 复现步骤", "", issue.reproSteps)
  if (issue.expected !== "") lines.push("", "## 期望", "", issue.expected)
  if (issue.actual !== "") lines.push("", "## 实际", "", issue.actual)
  if (issue.rerunSuggestion !== "") lines.push("", "## 建议", "", issue.rerunSuggestion)
  return lines.join("\n")
}

export type IssueDetailProps = {
  /** Version id used by the case/code deep links; never re-parsed. */
  versionId: string
  issue: IssueDetailView
  /** Batch number when the parent already loaded the batch list. */
  batchNo?: number
  busy: boolean
  actionError: string
  notice: string
  onConfirm: () => void
  onDismiss: () => void
  onClose: () => void
  onOpenEvidence?: (evidenceId: string) => void
  onOpenCase?: (caseNodeId: string) => void
  onOpenCodeRef?: (ref: IssueCodeRef) => void
}

/**
 * Issue detail drawer (spec §10.3): refs, evidence links, code anchors,
 * environment/session fingerprints, and the confirm/dismiss actions. Once an
 * issue is terminal the actions stay disabled and the operator/time are shown.
 */
export function IssueDetail({
  versionId,
  issue,
  batchNo,
  busy,
  actionError,
  notice,
  onConfirm,
  onDismiss,
  onClose,
  onOpenEvidence,
  onOpenCase,
  onOpenCodeRef,
}: IssueDetailProps) {
  const [copied, setCopied] = useState(false)
  const [copyError, setCopyError] = useState("")
  const terminal = issue.status !== "open"
  const batchId = issue.batch?.id ?? issue.batchId ?? ""

  async function handleCopy(): Promise<void> {
    const clipboard = navigator.clipboard
    if (clipboard === undefined) {
      setCopyError("当前浏览器不支持复制。")
      return
    }
    try {
      await clipboard.writeText(issueMarkdown(issue))
      setCopyError("")
      setCopied(true)
    } catch {
      setCopyError("复制失败，请手动选择文本。")
    }
  }

  return (
    <aside
      role="dialog"
      aria-modal="true"
      aria-label="问题详情"
      data-testid="issue-detail"
      data-issue-id={issue.id}
      className="rounded-2xl border border-border bg-card p-4 shadow-xl"
    >
      <header className="flex items-start justify-between gap-3">
        <div className="min-w-0 space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <StatusBadge variant={severityVariant(issue.severity)}>
              {labelOf(ISSUE_SEVERITY_LABELS, issue.severity)}
            </StatusBadge>
            <StatusBadge variant={issueStatusVariant(issue.status)}>
              {labelOf(ISSUE_STATUS_LABELS, issue.status)}
            </StatusBadge>
            <StatusBadge variant="neutral" dot={false}>
              {labelOf(ISSUE_CATEGORY_LABELS, issue.category)}
            </StatusBadge>
            <StatusBadge variant="info" dot={false}>
              {labelOf(ISSUE_CONFIDENCE_LABELS, issue.confidence)}
            </StatusBadge>
            <span className="text-[11px] text-muted-foreground">revision {issue.revision}</span>
          </div>
          <h3 className="text-base font-semibold">{issue.title}</h3>
          <p className="flex flex-wrap gap-x-2 text-[11px] text-muted-foreground">
            <span>{labelOf(ISSUE_SOURCE_LABELS, issue.source)}</span>
            <span aria-hidden="true">·</span>
            <span>复发 {issue.recurrenceCount} 次</span>
            <span aria-hidden="true">·</span>
            <span>{batchNo === undefined ? `Batch ${batchId || "—"}` : `Batch #${batchNo}`}</span>
            <span aria-hidden="true">·</span>
            <time dateTime={issue.createdAt}>{issue.createdAt}</time>
          </p>
        </div>
        <button
          type="button"
          aria-label="关闭问题详情"
          onClick={onClose}
          className="rounded-lg border border-border p-1.5 text-muted-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <X className="h-4 w-4" aria-hidden="true" />
        </button>
      </header>

      <div className="mt-4 grid gap-4 lg:grid-cols-2">
        <section className="space-y-3">
          {issue.description === "" ? null : (
            <div>
              <h4 className="text-xs font-semibold text-muted-foreground">描述</h4>
              <p className="mt-1 whitespace-pre-wrap break-words text-sm">{issue.description}</p>
            </div>
          )}
          <div>
            <h4 className="text-xs font-semibold text-muted-foreground">复现步骤</h4>
            <pre className="mt-1 whitespace-pre-wrap break-words font-mono text-xs">
              <code>{issue.reproSteps === "" ? "未记录" : issue.reproSteps}</code>
            </pre>
          </div>
          <div className="grid gap-2 sm:grid-cols-2">
            <div>
              <h4 className="text-xs font-semibold text-muted-foreground">期望</h4>
              <p className="mt-1 whitespace-pre-wrap break-words text-sm">
                {issue.expected === "" ? "未记录" : issue.expected}
              </p>
            </div>
            <div>
              <h4 className="text-xs font-semibold text-muted-foreground">实际</h4>
              <p className="mt-1 whitespace-pre-wrap break-words text-sm">
                {issue.actual === "" ? "未记录" : issue.actual}
              </p>
            </div>
          </div>
          {issue.rerunSuggestion === "" ? null : (
            <div>
              <h4 className="text-xs font-semibold text-muted-foreground">建议</h4>
              <p className="mt-1 text-sm">{issue.rerunSuggestion}</p>
            </div>
          )}
        </section>

        <IssueRefs
          versionId={versionId}
          issue={issue}
          onOpenEvidence={onOpenEvidence}
          onOpenCase={onOpenCase}
          onOpenCodeRef={onOpenCodeRef}
        />
      </div>

      {issue.status === "open" ? null : (
        <p className="mt-3 rounded-xl border border-border bg-muted/40 px-3 py-2 text-xs text-muted-foreground">
          {issue.status === "confirmed"
            ? `已确认 · 操作人 ${issue.confirmedBy ?? "—"} · ${issue.confirmedAt ?? "—"}`
            : `已驳回 · 操作人 ${issue.dismissedBy ?? "—"} · ${issue.dismissedAt ?? "—"}`}
          {issue.status === "dismissed" && issue.dismissedReason !== ""
            ? ` · 驳回原因：${issue.dismissedReason}`
            : ""}
        </p>
      )}

      {actionError === "" ? null : (
        <p
          role="alert"
          data-testid="issue-action-error"
          className="mt-3 rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
        >
          {actionError}
        </p>
      )}
      {notice === "" ? null : (
        <p
          role="status"
          className="mt-3 rounded-xl border border-emerald-500/30 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-700 dark:text-emerald-300"
        >
          {notice}
        </p>
      )}
      {copyError === "" ? null : (
        <p role="alert" className="mt-3 text-xs text-destructive">
          {copyError}
        </p>
      )}

      <footer className="mt-4 flex flex-wrap gap-2 border-t border-border pt-3">
        <button
          type="button"
          data-testid="issue-confirm-btn"
          onClick={onConfirm}
          disabled={terminal || busy}
          className={BUTTON_CLASS}
        >
          确认
        </button>
        <button
          type="button"
          data-testid="issue-dismiss-btn"
          onClick={onDismiss}
          disabled={terminal || busy}
          className={BUTTON_CLASS}
        >
          驳回
        </button>
        <button type="button" onClick={() => void handleCopy()} className={BUTTON_CLASS}>
          <Copy className="h-4 w-4" aria-hidden="true" />
          {copied ? "已复制" : "复制 Markdown"}
        </button>
      </footer>
    </aside>
  )
}
