import { Badge, type BadgeTone } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { Spinner } from "@/components/ui/Spinner"
import { formatDateTime } from "@/lib/format"

import type { CodeIndexRunView, CodeIndexStatus } from "../types"
import { CODE_INDEX_STATUS_LABELS } from "../types"

export type IndexRunStatusProps = {
  index: CodeIndexRunView | null
  baselineCommit: string | null
  indexInProgress: boolean
  busy: boolean
  onTriggerIndex: () => void
}

const INDEX_STATUS_TONE: Record<CodeIndexStatus, BadgeTone> = {
  queued: "warning",
  running: "info",
  done: "success",
  failed: "danger",
}

function firstErrorMessage(index: CodeIndexRunView): string {
  const first = index.errors[0]
  if (first === undefined) return ""
  const message = first.message
  return typeof message === "string" ? message : ""
}

/** Latest T0 index run summary plus the trigger (spec §4.2, §9.2). */
export function IndexRunStatus({
  index,
  baselineCommit,
  indexInProgress,
  busy,
  onTriggerIndex,
}: IndexRunStatusProps) {
  const disabled = busy || indexInProgress
  const errorMessage = index !== null && index.status === "failed" ? firstErrorMessage(index) : ""

  return (
    <section aria-label="索引状态" className="surface-card space-y-3 p-4">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <h2 className="text-sm font-semibold text-foreground">索引状态</h2>
          <p className="mt-0.5 text-[11px] text-muted-foreground">
            {index !== null ? `更新于 ${formatDateTime(index.updated_at)}` : "尚未触发代码索引"}
          </p>
        </div>
        <div className="flex items-center gap-2">
          {index !== null ? (
            <Badge tone={INDEX_STATUS_TONE[index.status]}>
              {CODE_INDEX_STATUS_LABELS[index.status]}
            </Badge>
          ) : (
            <Badge tone="neutral">尚未索引</Badge>
          )}
          {index?.truncated ? <Badge tone="warning">已截断</Badge> : null}
          <Button size="sm" onClick={onTriggerIndex} disabled={disabled}>
            {indexInProgress || busy ? (
              <span aria-hidden="true">
                <Spinner size="sm" label="索引中" />
              </span>
            ) : null}
            触发索引
          </Button>
        </div>
      </div>

      {index !== null ? (
        <dl className="grid grid-cols-2 gap-2 text-[11px]">
          <div>
            <dt className="text-muted-foreground">文件</dt>
            <dd className="font-medium text-foreground">
              文件 {index.files_indexed}/{index.files_total}
            </dd>
          </div>
          <div>
            <dt className="text-muted-foreground">commit</dt>
            <dd className="font-mono text-foreground">{index.commit_sha.slice(0, 7)}</dd>
          </div>
        </dl>
      ) : null}

      {baselineCommit === null ? (
        <p className="text-[11px] leading-relaxed text-warning">
          无代码基线：首次索引成功后会回填基线 commit，之后才能触发变更分析。
        </p>
      ) : null}

      {errorMessage.length > 0 ? (
        <p role="alert" className="text-[11px] text-destructive">
          {errorMessage}
        </p>
      ) : null}
    </section>
  )
}
