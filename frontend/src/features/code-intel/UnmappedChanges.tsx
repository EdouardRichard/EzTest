import { fileStatusLabel } from "./ChangedFiles"
import type { UnmappedChange } from "./types"

/** Backend cap for one analysis' unmapped list (spec §6.6 step 7). */
export const MAX_UNMAPPED_ENTRIES = 200

/** Human labels for the closed unmapped reason set (spec §6.6 step 7). */
export const UNMAPPED_REASON_LABELS: Record<string, string> = {
  deleted_file: "文件已删除",
  unsupported_content: "内容不支持分析",
  no_fact_or_anchor: "无事实或锚点覆盖",
  ai_unavailable: "AI 不可用",
  unmatched_contract: "契约未匹配",
}

const statusBadgeClass =
  "inline-flex items-center rounded-full border border-border bg-muted px-2 py-0.5 text-[11px] font-medium text-muted-foreground"

const reasonBadgeClass =
  "inline-flex items-center rounded-full border border-amber-500/25 bg-amber-500/10 px-2 py-0.5 text-[11px] font-medium text-amber-700 dark:text-amber-300"

export type UnmappedChangesProps = {
  entries: UnmappedChange[]
  /**
   * True when the backend truncated the unmapped list (stats.unmappedTruncated).
   * The flag is authoritative because a truncated list is still capped at
   * `MAX_UNMAPPED_ENTRIES`; a longer list also warns defensively.
   */
  truncated?: boolean
}

/**
 * Unmapped change list (spec §9.2/§6.6 step 7): path, git status, reason and
 * the backend suggestion. It only renders persisted analysis data and says so
 * when the list was truncated.
 */
export function UnmappedChanges({ entries, truncated }: UnmappedChangesProps) {
  // Exactly `MAX_UNMAPPED_ENTRIES` is a complete list unless the backend says it
  // truncated; only a longer list (or the explicit backend flag) means the
  // response was truncated.
  const capped = truncated || entries.length > MAX_UNMAPPED_ENTRIES

  return (
    <section
      aria-label="未映射变更"
      className="space-y-3 rounded-2xl border border-border bg-card p-4"
    >
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h2 className="text-base font-semibold">未映射变更</h2>
        <p className="text-xs text-muted-foreground">共 {entries.length} 个文件</p>
      </div>

      {capped ? (
        <p
          role="status"
          className="rounded-xl border border-amber-500/25 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300"
        >
          未映射变更超过上限，仅展示前 {entries.length} 条。
        </p>
      ) : null}

      {entries.length === 0 ? (
        <p className="text-sm text-muted-foreground">没有未映射变更</p>
      ) : (
        <ul className="divide-y divide-border">
          {entries.map((entry) => (
            <li
              key={`${entry.reason}:${entry.status}:${entry.path}`}
              data-unmapped-reason={entry.reason}
              className="space-y-1.5 py-2.5"
            >
              <div className="flex flex-wrap items-center gap-1.5">
                <span className="min-w-0 break-all font-mono text-xs text-foreground">
                  {entry.path}
                </span>
                <span className={statusBadgeClass}>{fileStatusLabel(entry.status)}</span>
                <span className={reasonBadgeClass}>
                  {UNMAPPED_REASON_LABELS[entry.reason] ??
                    (entry.reason === "" ? "未知原因" : entry.reason)}
                </span>
              </div>
              {entry.suggestion === "" ? null : (
                <p className="break-all text-xs text-muted-foreground">建议：{entry.suggestion}</p>
              )}
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}
