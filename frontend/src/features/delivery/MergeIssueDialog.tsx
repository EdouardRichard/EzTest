"use client"

import { useEffect, useState } from "react"

import { Alert } from "@/components/ui/Alert"
import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { Dialog } from "@/components/ui/Dialog"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState, InlineError } from "@/components/ui/ErrorState"
import { Field } from "@/components/ui/Field"
import { Input } from "@/components/ui/Input"
import { LoadingCard } from "@/components/ui/Spinner"
import { Textarea } from "@/components/ui/Textarea"
import { ApiError } from "@/lib/api-client"

import { listIssues, mergeIssue } from "./api"
import {
  ISSUE_CATEGORY_LABELS,
  ISSUE_SEVERITY_LABELS,
  ISSUE_STATUS_LABELS,
  type IssueView,
} from "./types"

export type MergeIssueDialogProps = {
  open: boolean
  versionId: string
  /** The issue that will be marked `merged`; never offered as a target. */
  source: IssueView | null
  onClose: () => void
  /** Called after a successful merge so the tab can refetch list and detail. */
  onMerged: () => void
}

/** Stable server sub-codes the merge endpoint returns inside 409 `state_conflict`. */
function mergeErrorHint(error: unknown): string | null {
  if (!(error instanceof ApiError) || error.status !== 409) return null
  const reason = typeof error.details.reason === "string" ? error.details.reason : ""
  if (reason === "merge_cycle") return "合并会形成环，已阻止。请选择其他目标问题。"
  if (reason === "merge_target_invalid") {
    return "目标问题不可用（可能已合并、不存在或属于其他版本）。"
  }
  return null
}

/**
 * Merge dialog (spec §9.2): lists same-version, non-merged issues and filters
 * them locally by ID or title. Cycle/target violations are shown inline and
 * keep the dialog open so another target can be selected.
 */
export function MergeIssueDialog({
  open,
  versionId,
  source,
  onClose,
  onMerged,
}: MergeIssueDialogProps) {
  const [candidates, setCandidates] = useState<IssueView[]>([])
  const [loading, setLoading] = useState(false)
  const [loadError, setLoadError] = useState<unknown>(null)
  const [reloadToken, setReloadToken] = useState(0)
  const [search, setSearch] = useState("")
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [reason, setReason] = useState("")
  const [submitting, setSubmitting] = useState(false)
  const [mergeError, setMergeError] = useState<unknown>(null)

  const sourceId = source?.id ?? null

  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadToken forces a retry
  useEffect(() => {
    if (!open || sourceId === null) return undefined
    let cancelled = false
    setSearch("")
    setSelectedId(null)
    setReason("")
    setSubmitting(false)
    setMergeError(null)
    setLoading(true)
    setLoadError(null)
    void listIssues(versionId, { limit: 100 })
      .then((items) => {
        if (cancelled) return
        setCandidates(items.filter((issue) => issue.id !== sourceId && issue.status !== "merged"))
      })
      .catch((cause: unknown) => {
        if (!cancelled) setLoadError(cause)
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [open, versionId, sourceId, reloadToken])

  if (!open || source === null) return null
  const currentSource = source

  const query = search.trim().toLowerCase()
  const visible =
    query.length === 0
      ? candidates
      : candidates.filter(
          (issue) => issue.id.includes(query) || issue.title.toLowerCase().includes(query),
        )
  const selected = candidates.find((issue) => issue.id === selectedId) ?? null
  const errorHint = mergeErrorHint(mergeError)

  async function submit(): Promise<void> {
    if (selectedId === null || submitting) return
    setSubmitting(true)
    setMergeError(null)
    try {
      const input = {
        target_issue_id: selectedId,
        expected_revision: currentSource.expected_revision,
      }
      const trimmed = reason.trim()
      await mergeIssue(versionId, currentSource.id, {
        ...input,
        ...(trimmed.length > 0 ? { reason: trimmed } : {}),
      })
      onMerged()
      onClose()
    } catch (cause) {
      setMergeError(cause)
      setSubmitting(false)
    }
  }

  return (
    <Dialog
      open
      onClose={onClose}
      title="合并问题"
      description="选择同一版本内未合并的目标问题；源问题将标记为已合并。"
      footer={
        <>
          <Button variant="secondary" size="sm" disabled={submitting} onClick={onClose}>
            取消
          </Button>
          <Button
            size="sm"
            disabled={selectedId === null || submitting}
            onClick={() => {
              void submit()
            }}
          >
            {submitting ? "合并中…" : "合并"}
          </Button>
        </>
      }
    >
      <div className="space-y-3">
        <p className="text-[11px] text-muted-foreground">
          源问题 #{source.id} · {source.title}
        </p>

        <Field id="merge-target-search" label="搜索目标问题">
          <Input
            id="merge-target-search"
            value={search}
            placeholder="输入问题 ID 或标题"
            disabled={loading || submitting}
            onChange={(event) => setSearch(event.target.value)}
          />
        </Field>

        {loading ? (
          <LoadingCard label="正在加载可合并问题…" />
        ) : loadError !== null ? (
          <ErrorState
            error={loadError}
            title="可合并问题加载失败"
            onRetry={() => setReloadToken((token) => token + 1)}
          />
        ) : visible.length === 0 ? (
          <EmptyState
            title="没有可合并的问题"
            description={
              query.length > 0 ? "没有匹配的问题，请调整搜索条件。" : "该版本暂无其他未合并问题。"
            }
          />
        ) : (
          <ul aria-label="合并目标列表" className="max-h-64 space-y-1.5 overflow-y-auto">
            {visible.map((issue) => {
              const active = issue.id === selectedId
              return (
                <li key={issue.id}>
                  <button
                    type="button"
                    data-testid={`merge-target-${issue.id}`}
                    aria-pressed={active}
                    disabled={submitting}
                    onClick={() => setSelectedId(issue.id)}
                    className={
                      active
                        ? "w-full rounded-lg border border-primary bg-primary/5 px-3 py-2 text-left"
                        : "w-full rounded-lg border border-border bg-card px-3 py-2 text-left hover:bg-muted/40"
                    }
                  >
                    <span className="block text-xs font-medium text-foreground">{issue.title}</span>
                    <span className="mt-1 flex flex-wrap items-center gap-1.5 text-[11px] text-muted-foreground">
                      #{issue.id}
                      <Badge tone="neutral">{ISSUE_STATUS_LABELS[issue.status]}</Badge>
                      <span>{ISSUE_CATEGORY_LABELS[issue.category]}</span>
                      <span>{ISSUE_SEVERITY_LABELS[issue.severity]}</span>
                    </span>
                  </button>
                </li>
              )
            })}
          </ul>
        )}

        <Field id="merge-reason" label="合并原因">
          <Textarea
            id="merge-reason"
            value={reason}
            maxLength={2000}
            disabled={submitting}
            placeholder="可选；会写入状态历史与审计"
            onChange={(event) => setReason(event.target.value)}
          />
        </Field>

        {selected !== null ? (
          <p className="text-[11px] text-muted-foreground">
            将合并到 #{selected.id}「{selected.title}」
          </p>
        ) : null}

        {mergeError !== null ? (
          <div className="space-y-1.5">
            {errorHint !== null ? <Alert tone="danger">{errorHint}</Alert> : null}
            <InlineError error={mergeError} />
          </div>
        ) : null}
      </div>
    </Dialog>
  )
}
