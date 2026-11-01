import { Alert } from "@/components/ui/Alert"
import { Badge, type BadgeTone } from "@/components/ui/Badge"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"

import { shortCommit } from "../format"
import type { CodeChangedFileStatus, CodeChangedFileView } from "../types"
import { CODE_CHANGED_STATUS_LABELS, CODE_CHANGED_STATUS_LETTERS } from "../types"

export type ChangedFilesPanelProps = {
  files: CodeChangedFileView[]
  truncated: boolean
  loading: boolean
  error: unknown
  baseCommit: string | null
  headCommit: string | null
  selectedPath: string | null
  onSelectFile: (file: CodeChangedFileView) => void
  onRetry: () => void
}

const STATUS_TONE: Record<CodeChangedFileStatus, BadgeTone> = {
  added: "success",
  modified: "info",
  deleted: "danger",
  renamed: "warning",
}

/** Left column of the code tab: changed files and their on-demand diff entry. */
export function ChangedFilesPanel({
  files,
  truncated,
  loading,
  error,
  baseCommit,
  headCommit,
  selectedPath,
  onSelectFile,
  onRetry,
}: ChangedFilesPanelProps) {
  const range =
    baseCommit !== null && headCommit !== null
      ? `${shortCommit(baseCommit)} → ${shortCommit(headCommit)}`
      : "基线/head 未确定"

  return (
    <section aria-label="变更文件" className="surface-card space-y-3 p-4">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <h2 className="text-sm font-semibold text-foreground">变更文件</h2>
          <p className="mt-0.5 font-mono text-[11px] text-muted-foreground">{range}</p>
        </div>
        <Badge tone="neutral">{files.length} 个文件</Badge>
      </div>

      {truncated ? (
        <Alert tone="warning" title="变更集已截断">
          变更文件超过 2000 个，列表只展示上限内的文件。
        </Alert>
      ) : null}

      {loading && files.length === 0 ? (
        <LoadingCard label="正在加载变更文件…" />
      ) : error !== null && files.length === 0 ? (
        <ErrorState error={error} title="变更文件加载失败" onRetry={onRetry} />
      ) : files.length === 0 ? (
        <EmptyState
          title="暂无变更文件"
          description="基线 commit 与 head 之间没有差异，或尚未完成变更分析。"
        />
      ) : (
        <ul className="divide-y divide-border">
          {files.map((file) => {
            const selected = file.path === selectedPath
            return (
              <li key={file.path}>
                <button
                  type="button"
                  aria-current={selected ? "true" : undefined}
                  onClick={() => onSelectFile(file)}
                  className="flex w-full items-start gap-2 px-1 py-2 text-left transition-colors hover:bg-muted/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  <Badge tone={STATUS_TONE[file.status]}>
                    {CODE_CHANGED_STATUS_LETTERS[file.status]}
                    <span className="sr-only">{CODE_CHANGED_STATUS_LABELS[file.status]}</span>
                  </Badge>
                  <span className="min-w-0 flex-1">
                    <span className="block truncate font-mono text-xs text-foreground">
                      {file.path}
                    </span>
                    <span className="mt-0.5 flex flex-wrap items-center gap-1.5 text-[11px] text-muted-foreground">
                      <span className="text-success">+{file.additions}</span>
                      <span className="text-destructive">-{file.deletions}</span>
                      {file.binary ? <Badge tone="warning">二进制</Badge> : null}
                      {file.status === "renamed" && file.old_path ? (
                        <span className="truncate">来自 {file.old_path}</span>
                      ) : null}
                    </span>
                  </span>
                </button>
              </li>
            )
          })}
        </ul>
      )}
    </section>
  )
}
