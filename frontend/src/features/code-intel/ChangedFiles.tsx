import { useId } from "react"

import type { ChangedFile, ChangeList } from "./types"

/** Labels for the closed git status families (spec §7.2). */
const FILE_STATUS_LABELS: Record<string, string> = {
  A: "新增",
  M: "修改",
  D: "删除",
  R: "重命名",
  C: "复制",
  T: "类型变更",
}

/** Git returns `M`, `R100`, `C75`…: the first letter identifies the family. */
export function fileStatusKey(status: string): string {
  return status.trim().charAt(0).toUpperCase()
}

/** Human label for a change-set status; unknown values fall back verbatim. */
export function fileStatusLabel(status: string): string {
  const key = fileStatusKey(status)
  return FILE_STATUS_LABELS[key] ?? (status === "" ? "未知" : status)
}

const statusBadgeClass =
  "inline-flex items-center rounded-full border border-border bg-muted px-2 py-0.5 text-[11px] font-medium text-muted-foreground"

const binaryBadgeClass =
  "inline-flex items-center rounded-full border border-amber-500/25 bg-amber-500/10 px-2 py-0.5 text-[11px] font-medium text-amber-700 dark:text-amber-300"

export type ChangedFilesProps = {
  changes: ChangeList
  onOpenFile: (file: ChangedFile) => void
}

/**
 * Change-set file list (spec §9.2): status/additions/deletions and the binary
 * marker, one button per file opening the unified diff drawer. It never
 * fabricates rows; an empty list is rendered by the container's empty state.
 */
export function ChangedFiles({ changes, onOpenFile }: ChangedFilesProps) {
  const headingId = useId()
  const { files, stats } = changes

  return (
    <section
      aria-labelledby={headingId}
      className="space-y-3 rounded-2xl border border-border bg-card p-4"
    >
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h2 id={headingId} className="text-base font-semibold">
          变更文件
        </h2>
        <p className="text-xs text-muted-foreground">
          共 {stats.files} 个文件 · +{stats.additions} -{stats.deletions}
        </p>
      </div>

      {stats.truncated ? (
        <p
          role="status"
          className="rounded-xl border border-amber-500/25 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300"
        >
          变更文件超过存储上限，仅展示前 {files.length} 个。
        </p>
      ) : null}

      <ul className="divide-y divide-border">
        {files.map((file) => (
          <li key={`${file.status}:${file.path}`}>
            <button
              type="button"
              aria-label={`查看 ${file.path} 的 diff`}
              onClick={() => onOpenFile(file)}
              className="grid w-full grid-cols-[minmax(0,1fr)_auto] items-center gap-3 rounded-lg px-1 py-2.5 text-left transition-colors hover:bg-muted/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <span className="min-w-0">
                <span className="block truncate font-mono text-xs text-foreground">
                  {file.oldPath === null ? file.path : `${file.oldPath} → ${file.path}`}
                </span>
                <span className="mt-1 flex flex-wrap items-center gap-1.5">
                  <span className={statusBadgeClass}>{fileStatusLabel(file.status)}</span>
                  {file.binary ? <span className={binaryBadgeClass}>二进制</span> : null}
                </span>
              </span>
              <span className="shrink-0 font-mono text-xs tabular-nums">
                <span className="text-emerald-700 dark:text-emerald-300">+{file.additions}</span>{" "}
                <span className="text-red-700 dark:text-red-300">-{file.deletions}</span>
              </span>
            </button>
          </li>
        ))}
      </ul>
    </section>
  )
}
