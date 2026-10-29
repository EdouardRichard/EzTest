"use client"

import { Bug, Loader2, RefreshCw } from "lucide-react"
import { useMemo, useState } from "react"

import { EmptyState } from "@/components/ui/EmptyState"
import { EvidenceViewer } from "@/features/execution/components/EvidenceViewer"

import { describeDeliveryError, isIssueConflict, patchIssue } from "../api"
import { useIssue, useIssues } from "../hooks/useIssues"
import { useReportBatches } from "../hooks/useReports"
import type { DeliveryStream, IssueCodeRef, IssueListFilters } from "../types"
import { IssueDetail } from "./IssueDetail"
import { IssueDismissDialog } from "./IssueDismissDialog"
import { EMPTY_ISSUE_FILTERS, IssueFilters, type IssueFilterValues } from "./IssueFilters"
import { IssueRow } from "./IssueRow"

export type IssuesTabProps = {
  /** P3-owned version id; never re-parsed (spec §7.1). */
  versionId: string
  /**
   * Version stream already owned by the page. When omitted the tab subscribes
   * itself (a stable empty id is passed to keep hook order, spec §10.8).
   */
  stream?: DeliveryStream
  /** Shows the AI-degraded hint on the empty state (spec §10.3). */
  degraded?: boolean
  /** Controlled open issue for the `?issue=` deep link. */
  issueId?: string
  /** Notified when the open issue changes; required to use `issueId`. */
  onIssueChange?: (issueId: string) => void
  /** Open evidence in the page-owned viewer; otherwise the tab opens its own. */
  onOpenEvidence?: (evidenceId: string) => void
  onOpenCase?: (caseNodeId: string) => void
  onOpenCodeRef?: (ref: IssueCodeRef) => void
}

/**
 * Version-scoped issue tab (spec §10.3): filterable cursor list, detail drawer
 * with refs/evidence/code links, and confirm/dismiss under the revision CAS.
 * Live issue.* events refresh both the list and the open detail.
 */
export function IssuesTab({
  versionId,
  stream,
  degraded = false,
  issueId,
  onIssueChange,
  onOpenEvidence,
  onOpenCase,
  onOpenCodeRef,
}: IssuesTabProps) {
  const [filterValues, setFilterValues] = useState<IssueFilterValues>(EMPTY_ISSUE_FILTERS)
  const [localIssueId, setLocalIssueId] = useState("")
  const [dismissOpen, setDismissOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  const [actionError, setActionError] = useState("")
  const [dialogError, setDialogError] = useState("")
  const [notice, setNotice] = useState("")
  const [localEvidenceId, setLocalEvidenceId] = useState("")

  const openIssueId = issueId ?? localIssueId

  const batchPage = useReportBatches(versionId, stream)
  const filters = useMemo<IssueListFilters>(
    () => ({
      status: filterValues.status as IssueListFilters["status"],
      severity: filterValues.severity as IssueListFilters["severity"],
      category: filterValues.category as IssueListFilters["category"],
      batchId: filterValues.batchId,
      q: filterValues.q,
    }),
    [filterValues],
  )
  const page = useIssues(versionId, filters, stream)
  const detail = useIssue(versionId, openIssueId, stream)

  const batchNoById = useMemo(() => {
    const map = new Map<string, number>()
    for (const batch of batchPage.batches) map.set(batch.id, batch.batchNo)
    return map
  }, [batchPage.batches])

  function openIssue(nextIssueId: string): void {
    if (issueId === undefined) setLocalIssueId(nextIssueId)
    onIssueChange?.(nextIssueId)
    setActionError("")
    setDialogError("")
    setNotice("")
  }

  function closeIssue(): void {
    if (issueId === undefined) setLocalIssueId("")
    onIssueChange?.("")
  }

  function openEvidence(evidenceId: string): void {
    if (onOpenEvidence === undefined) setLocalEvidenceId(evidenceId)
    else onOpenEvidence(evidenceId)
  }

  async function confirmIssue(): Promise<void> {
    const issue = detail.issue
    if (issue === null || issue.status !== "open" || busy) return
    setBusy(true)
    setActionError("")
    setNotice("")
    try {
      await patchIssue(versionId, issue.id, {
        status: "confirmed",
        expectedRevision: issue.revision,
      })
      setNotice("已确认")
      await detail.refresh()
    } catch (caught) {
      setActionError(describeDeliveryError(caught))
      if (isIssueConflict(caught)) await detail.refresh()
    } finally {
      setBusy(false)
    }
  }

  async function dismissIssue(reason: string): Promise<void> {
    const issue = detail.issue
    if (issue === null || issue.status !== "open" || busy) return
    if (reason.trim() === "") {
      setDialogError("驳回原因必填。")
      return
    }
    setBusy(true)
    setDialogError("")
    setActionError("")
    setNotice("")
    try {
      await patchIssue(versionId, issue.id, {
        status: "dismissed",
        expectedRevision: issue.revision,
        comment: reason.trim(),
      })
      setDismissOpen(false)
      setNotice("已驳回")
      await detail.refresh()
    } catch (caught) {
      const message = describeDeliveryError(caught)
      setDialogError(message)
      if (isIssueConflict(caught)) await detail.refresh()
    } finally {
      setBusy(false)
    }
  }

  let listBody = null
  if (page.loading && page.issues.length === 0) {
    listBody = (
      <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
        <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
        正在加载问题…
      </p>
    )
  } else if (page.error !== "") {
    listBody = (
      <div
        role="alert"
        data-testid="issues-error"
        className="flex flex-wrap items-center gap-2 rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
      >
        <span>加载问题失败：{page.error}</span>
        <button
          type="button"
          onClick={() => void page.refresh()}
          className="rounded-lg border border-border bg-card px-2.5 py-1 text-xs font-medium text-foreground hover:bg-muted"
        >
          重试
        </button>
      </div>
    )
  } else if (page.issues.length === 0) {
    listBody = (
      <div data-testid="issues-empty" className="space-y-2">
        <EmptyState
          icon={Bug}
          title="当前批次暂无问题"
          description="问题由失败分析与问题整理生成，也可由人工创建；可切换批次或筛选条件查看其它问题。"
        />
        {degraded ? (
          <p
            data-testid="issues-degraded"
            className="rounded-xl border border-amber-500/25 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300"
          >
            AI 分析不可用，问题可能不完整
          </p>
        ) : null}
      </div>
    )
  } else {
    listBody = (
      <ul className="space-y-2">
        {page.issues.map((issue) => (
          <IssueRow
            key={issue.id}
            issue={issue}
            batchNo={issue.batchId === null ? undefined : batchNoById.get(issue.batchId)}
            onOpen={() => openIssue(issue.id)}
            onOpenEvidence={openEvidence}
          />
        ))}
      </ul>
    )
  }

  return (
    <section aria-label="问题" data-testid="issues-tab" className="space-y-4">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 className="text-base font-semibold">问题</h2>
          <p className="mt-0.5 text-xs text-muted-foreground">
            AI 失败分析与问题整理生成的问题；确认或驳回是终态操作，驳回必须填写原因。
          </p>
        </div>
        {page.streamStatus === "reconnecting" ? (
          <span
            data-testid="issues-stream-status"
            className="inline-flex items-center gap-1 text-xs text-amber-700 dark:text-amber-300"
          >
            <RefreshCw className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
            实时连接重连中
          </span>
        ) : null}
      </header>

      <IssueFilters value={filterValues} batches={batchPage.batches} onChange={setFilterValues} />

      {listBody}

      {page.hasMore ? (
        <button
          type="button"
          data-testid="issues-load-more"
          onClick={() => void page.loadMore()}
          disabled={page.loadingMore}
          className="inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50"
        >
          {page.loadingMore ? (
            <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
          ) : null}
          {page.loadingMore ? "加载中…" : "加载更多"}
        </button>
      ) : null}

      {openIssueId === "" ? null : (
        <div className="mt-4">
          {detail.loading && detail.issue === null ? (
            <p role="status" className="text-sm text-muted-foreground">
              正在加载问题详情…
            </p>
          ) : detail.error !== "" && detail.issue === null ? (
            <p
              role="alert"
              className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
            >
              加载问题详情失败：{detail.error}
            </p>
          ) : detail.issue === null ? null : (
            <IssueDetail
              versionId={versionId}
              issue={detail.issue}
              batchNo={
                detail.issue.batchId === null ? undefined : batchNoById.get(detail.issue.batchId)
              }
              busy={busy}
              actionError={actionError}
              notice={notice}
              onConfirm={() => void confirmIssue()}
              onDismiss={() => {
                setDialogError("")
                setDismissOpen(true)
              }}
              onClose={closeIssue}
              onOpenEvidence={openEvidence}
              onOpenCase={onOpenCase}
              onOpenCodeRef={onOpenCodeRef}
            />
          )}
        </div>
      )}

      {dismissOpen ? (
        <IssueDismissDialog
          busy={busy}
          error={dialogError}
          onCancel={() => setDismissOpen(false)}
          onSubmit={(reason) => void dismissIssue(reason)}
        />
      ) : null}

      {localEvidenceId === "" ? null : (
        <EvidenceViewer
          versionId={versionId}
          evidenceId={localEvidenceId}
          onClose={() => setLocalEvidenceId("")}
        />
      )}
    </section>
  )
}
