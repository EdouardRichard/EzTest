import { Badge, type BadgeTone } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"

import { anchorLocation } from "../format"
import type { CodeAnchorStatus, CodeAnchorView } from "../types"
import {
  CODE_ANCHOR_STATUS_LABELS,
  CODE_ANCHOR_SUBJECT_LABELS,
  CODE_CONFIDENCE_LABELS,
} from "../types"

export type AnchorsPanelProps = {
  anchors: CodeAnchorView[]
  loading: boolean
  error: unknown
  busyAnchorId: string | null
  focusedAnchorId?: string | null
  onValidate: (anchorId: string) => void
  onDelete: (anchorId: string) => void
  onFocusAnchor?: (anchorId: string) => void
  onRetry: () => void
}

const STATUS_TONE: Record<CodeAnchorStatus, BadgeTone> = {
  valid: "success",
  stale: "warning",
  broken: "danger",
}

/** Version-level code anchors with validate/delete/focus actions (spec §6.1). */
export function AnchorsPanel({
  anchors,
  loading,
  error,
  busyAnchorId,
  focusedAnchorId = null,
  onValidate,
  onDelete,
  onFocusAnchor,
  onRetry,
}: AnchorsPanelProps) {
  return (
    <section aria-label="代码锚点" className="surface-card space-y-3 p-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h2 className="text-sm font-semibold text-foreground">代码锚点</h2>
          <p className="mt-0.5 text-[11px] text-muted-foreground">
            需求/用例/问题锚点的有效性与审计。
          </p>
        </div>
        <Badge tone="neutral">{anchors.length} 条</Badge>
      </div>

      {loading && anchors.length === 0 ? (
        <LoadingCard label="正在加载代码锚点…" />
      ) : error !== null && anchors.length === 0 ? (
        <ErrorState error={error} title="代码锚点加载失败" onRetry={onRetry} />
      ) : anchors.length === 0 ? (
        <EmptyState title="暂无代码锚点" description="可在需求、用例或问题详情中添加代码锚点。" />
      ) : (
        <ul className="space-y-2">
          {anchors.map((anchor) => {
            const busy = busyAnchorId === anchor.id
            const focused = focusedAnchorId === anchor.id
            return (
              <li
                key={anchor.id}
                aria-label={`锚点 ${anchor.id}`}
                aria-current={focused ? "true" : undefined}
                className={
                  focused
                    ? "rounded-lg border border-primary/40 bg-primary/5 p-2"
                    : "rounded-lg border border-border p-2"
                }
              >
                <div className="flex flex-wrap items-center gap-1.5">
                  <Badge tone={STATUS_TONE[anchor.status]}>
                    {CODE_ANCHOR_STATUS_LABELS[anchor.status]}
                  </Badge>
                  <span className="text-xs font-medium text-foreground">
                    {CODE_ANCHOR_SUBJECT_LABELS[anchor.subject_kind]} #{anchor.subject_id}
                  </span>
                  <Badge tone="neutral">{CODE_CONFIDENCE_LABELS[anchor.confidence]}</Badge>
                  {busy ? <span className="text-[11px] text-muted-foreground">处理中…</span> : null}
                </div>
                <p className="mt-1 truncate font-mono text-[11px] text-muted-foreground">
                  {anchorLocation(anchor.file_path, anchor.start_line, anchor.end_line)}
                </p>
                <div className="mt-1 flex flex-wrap items-center gap-1">
                  <Button
                    variant="ghost"
                    size="sm"
                    className="h-6 px-2"
                    aria-label={`校验锚点 ${anchor.id}`}
                    disabled={busy}
                    onClick={() => onValidate(anchor.id)}
                  >
                    校验
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="h-6 px-2 text-destructive"
                    aria-label={`删除锚点 ${anchor.id}`}
                    disabled={busy}
                    onClick={() => onDelete(anchor.id)}
                  >
                    删除
                  </Button>
                  {onFocusAnchor ? (
                    <Button
                      variant="ghost"
                      size="sm"
                      className="h-6 px-2"
                      aria-label={`定位锚点 ${anchor.id}`}
                      onClick={() => onFocusAnchor(anchor.id)}
                    >
                      定位
                    </Button>
                  ) : null}
                </div>
              </li>
            )
          })}
        </ul>
      )}
    </section>
  )
}
