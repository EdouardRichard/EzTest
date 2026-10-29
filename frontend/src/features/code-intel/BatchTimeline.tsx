import { useId } from "react"

import { shortCommit } from "./empty"
import type { VersionChangeBatch, VersionChangeItem } from "./types"

/** Human labels for the persisted snapshot change kinds (spec §4.2). */
const CHANGE_KIND_LABELS: Record<string, string> = {
  added: "新增",
  modified: "修改",
  deleted: "删除",
  renamed: "重命名",
  copied: "复制",
  type_changed: "类型变更",
}

/** Human labels for run_batch statuses on the timeline. */
const BATCH_STATUS_LABELS: Record<string, string> = {
  planned: "待执行",
  running: "运行中",
  done: "完成",
  failed: "失败",
  cancelled: "已取消",
}

const badgeClass =
  "inline-flex items-center rounded-full border border-border bg-muted px-2 py-0.5 text-[11px] font-medium text-muted-foreground"

const rewriteBadgeClass =
  "inline-flex items-center rounded-full border border-amber-500/25 bg-amber-500/10 px-2 py-0.5 text-[11px] font-medium text-amber-700 dark:text-amber-300"

const unmappedBadgeClass =
  "inline-flex items-center rounded-full border border-destructive/30 bg-destructive/10 px-2 py-0.5 text-[11px] font-medium text-destructive"

const caseBadgeClass =
  "inline-flex items-center rounded-full border border-sky-500/25 bg-sky-500/10 px-2 py-0.5 text-[11px] font-medium text-sky-700 dark:text-sky-300"

function batchStatusLabel(status: string): string {
  return BATCH_STATUS_LABELS[status] ?? (status === "" ? "未知" : status)
}

function changeKindLabel(kind: string): string {
  return CHANGE_KIND_LABELS[kind] ?? (kind === "" ? "变更" : kind)
}

/** One file → entities → affected cases row of a batch snapshot. */
function BatchItem({ item }: { item: VersionChangeItem }) {
  return (
    <li data-testid="batch-change-item" className="rounded-lg border border-border/70 px-2.5 py-2">
      <div className="flex flex-wrap items-center gap-1.5">
        <span className="min-w-0 break-all font-mono text-xs text-foreground">{item.file}</span>
        <span className={badgeClass}>{changeKindLabel(item.changeKind)}</span>
        {item.unmapped ? <span className={unmappedBadgeClass}>未映射</span> : null}
      </div>
      {item.subjects.length === 0 ? null : (
        <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
          {item.subjects.map((subject) => (
            <span key={`${subject.kind}:${subject.name}`} className={badgeClass}>
              {subject.kind} · {subject.name}
            </span>
          ))}
        </div>
      )}
      {item.caseIds.length === 0 ? null : (
        <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
          {item.caseIds.map((caseId) => (
            <span key={caseId} className={caseBadgeClass}>
              用例 {caseId}
            </span>
          ))}
        </div>
      )}
    </li>
  )
}

export type BatchTimelineProps = {
  /** Batch 1→N snapshots; an older backend omits the field (empty array). */
  batches: VersionChangeBatch[]
}

/**
 * 「批次时间线」of the code tab (spec §7.2): one entry per batch with its commit
 * range, status, D49 history-rewrite marker, changed-file/affected-case/unmapped
 * counters, the file → entity → case summary chain and the per-batch
 * 「本批无代码变更」empty state. It renders real snapshot data only and never
 * fabricates batches; a missing field renders nothing so old clients keep the
 * P7 change list unchanged.
 */
export function BatchTimeline({ batches }: BatchTimelineProps) {
  const headingId = useId()
  if (batches.length === 0) return null

  return (
    <section
      aria-labelledby={headingId}
      data-testid="code-batch-timeline"
      className="space-y-3 rounded-2xl border border-border bg-card p-4"
    >
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h2 id={headingId} className="text-base font-semibold">
          批次时间线
        </h2>
        <p className="text-xs text-muted-foreground">共 {batches.length} 个批次</p>
      </div>

      <ol className="space-y-3">
        {batches.map((batch) => {
          const unmapped = batch.items.filter((item) => item.unmapped)
          return (
            <li
              key={batch.batchId}
              data-testid={`batch-timeline-${batch.batchId}`}
              className="space-y-2 rounded-xl border border-border bg-background/40 p-3"
            >
              <div className="flex flex-wrap items-center gap-1.5">
                <h3 className="text-sm font-semibold">Batch {batch.batchNo}</h3>
                <span className={badgeClass}>{batchStatusLabel(batch.batchStatus)}</span>
                {batch.historyRewritten ? (
                  <span className={rewriteBadgeClass} title="父批 head 不可用，基线回退到版本基线">
                    历史重写
                  </span>
                ) : null}
              </div>

              <p className="font-mono text-xs text-muted-foreground">
                {shortCommit(batch.baseCommit)} → {shortCommit(batch.headCommit)}
              </p>
              <p className="text-xs text-muted-foreground">
                {`变更文件 ${batch.changedFiles} · 影响用例 ${batch.impactedCases} · 未映射 ${batch.unmappedCount}`}
              </p>

              {batch.items.length === 0 ? (
                <p className="rounded-lg border border-dashed border-border px-3 py-2 text-sm text-muted-foreground">
                  本批无代码变更
                </p>
              ) : (
                <ul className="space-y-1.5">
                  {batch.items.map((item) => (
                    <BatchItem key={`${item.changeKind}:${item.file}`} item={item} />
                  ))}
                </ul>
              )}

              {unmapped.length === 0 ? null : (
                <div
                  data-testid={`batch-unmapped-${batch.batchId}`}
                  className="rounded-lg border border-destructive/25 bg-destructive/5 px-2.5 py-2"
                >
                  <h4 className="text-xs font-medium text-destructive">未映射变更</h4>
                  <ul className="mt-1 space-y-0.5">
                    {unmapped.map((item) => (
                      <li
                        key={item.file}
                        data-unmapped-file={item.file}
                        className="break-all font-mono text-xs text-muted-foreground"
                      >
                        {item.file}
                      </li>
                    ))}
                  </ul>
                </div>
              )}
            </li>
          )
        })}
      </ol>
    </section>
  )
}
