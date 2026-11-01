"use client"

import { useCallback, useEffect, useState } from "react"

import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { Dialog } from "@/components/ui/Dialog"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState, InlineError } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import { formatDateTime } from "@/lib/format"

import { listPromptRevisions, restoreAgentPrompt } from "./api"
import { diffLines, PROMPT_SOURCE_LABEL, type PromptRevision } from "./types"

export type PromptVersionListProps = {
  profileKey: string
  currentPrompt: string
  /** Loads a revision body into the editor without saving it. */
  onLoad: (revision: PromptRevision) => void
  /** Called after a successful restore so the parent refreshes the detail. */
  onRestored: () => void
}

const VERSION_LIMIT = 20

/**
 * Append-only prompt version history: view the full body, diff against the
 * current prompt, load it into the editor, or restore it as a new version.
 * History rows are never modified.
 */
export function PromptVersionList({
  profileKey,
  currentPrompt,
  onLoad,
  onRestored,
}: PromptVersionListProps) {
  const [revisions, setRevisions] = useState<PromptRevision[] | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<unknown>(null)
  const [reloadToken, setReloadToken] = useState(0)

  const [viewing, setViewing] = useState<PromptRevision | null>(null)
  const [diffing, setDiffing] = useState<PromptRevision | null>(null)
  const [restoring, setRestoring] = useState<number | null>(null)
  const [restoreError, setRestoreError] = useState<unknown>(null)

  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadToken re-runs the explicit refetch
  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)
    void listPromptRevisions(profileKey, { limit: VERSION_LIMIT })
      .then((page) => {
        if (!cancelled) setRevisions(page.items)
      })
      .catch((cause: unknown) => {
        if (!cancelled) {
          setRevisions(null)
          setError(cause)
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [profileKey, reloadToken])

  const reload = useCallback(() => setReloadToken((token) => token + 1), [])

  async function restore(revision: PromptRevision): Promise<void> {
    setRestoring(revision.version)
    setRestoreError(null)
    try {
      await restoreAgentPrompt(profileKey, revision.version)
      onRestored()
      reload()
    } catch (cause) {
      setRestoreError(cause)
    } finally {
      setRestoring(null)
    }
  }

  return (
    <section data-testid="agent-prompt-versions" aria-label="提示词版本历史" className="space-y-2">
      <header className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h3 className="text-xs font-semibold text-foreground">版本历史</h3>
          <p className="mt-0.5 text-[11px] text-muted-foreground">
            追加写历史；恢复旧版本会生成一个新版本，不改写历史行。
          </p>
        </div>
        <Button variant="ghost" size="sm" onClick={reload}>
          刷新历史
        </Button>
      </header>

      {restoreError !== null ? <InlineError error={restoreError} /> : null}

      {loading && revisions === null ? (
        <LoadingCard label="正在加载版本历史…" />
      ) : error !== null ? (
        <ErrorState error={error} title="版本历史加载失败" onRetry={reload} />
      ) : revisions === null || revisions.length === 0 ? (
        <div data-testid="agent-prompt-versions-empty">
          <EmptyState title="尚无历史版本" description="保存或恢复提示词后会在这里出现版本记录。" />
        </div>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full min-w-[720px] border-separate border-spacing-0 text-left text-xs">
            <thead>
              <tr className="text-[11px] uppercase tracking-wide text-muted-foreground">
                <th className="border-b border-border px-2 py-1.5 font-medium">版本</th>
                <th className="border-b border-border px-2 py-1.5 font-medium">来源</th>
                <th className="border-b border-border px-2 py-1.5 font-medium">操作者</th>
                <th className="border-b border-border px-2 py-1.5 font-medium">时间</th>
                <th className="border-b border-border px-2 py-1.5 font-medium">备注</th>
                <th className="border-b border-border px-2 py-1.5 font-medium">操作</th>
              </tr>
            </thead>
            <tbody>
              {revisions.map((revision) => (
                <tr key={revision.version} data-testid={`agent-prompt-version-${revision.version}`}>
                  <td className="border-b border-border px-2 py-1.5 font-mono text-foreground">
                    v{revision.version}
                  </td>
                  <td className="border-b border-border px-2 py-1.5">
                    <Badge tone={revision.source === "admin" ? "info" : "neutral"}>
                      {PROMPT_SOURCE_LABEL[revision.source] ?? revision.source}
                    </Badge>
                  </td>
                  <td className="border-b border-border px-2 py-1.5 text-muted-foreground">
                    {revision.actor_id === null ? "—" : `#${revision.actor_id}`}
                  </td>
                  <td className="border-b border-border px-2 py-1.5 text-muted-foreground">
                    {formatDateTime(revision.created_at)}
                  </td>
                  <td className="border-b border-border px-2 py-1.5 text-muted-foreground">
                    {revision.note ?? "—"}
                  </td>
                  <td className="border-b border-border px-2 py-1.5">
                    <div className="flex flex-wrap gap-1">
                      <Button
                        data-testid={`agent-prompt-version-view-${revision.version}`}
                        variant="ghost"
                        size="sm"
                        onClick={() => setViewing(revision)}
                      >
                        查看全文
                      </Button>
                      <Button
                        data-testid={`agent-prompt-version-diff-${revision.version}`}
                        variant="ghost"
                        size="sm"
                        onClick={() => setDiffing(revision)}
                      >
                        与当前 diff
                      </Button>
                      <Button
                        data-testid={`agent-prompt-version-load-${revision.version}`}
                        variant="secondary"
                        size="sm"
                        onClick={() => onLoad(revision)}
                      >
                        载入编辑器
                      </Button>
                      <Button
                        data-testid={`agent-prompt-version-restore-${revision.version}`}
                        variant="secondary"
                        size="sm"
                        disabled={restoring !== null}
                        onClick={() => void restore(revision)}
                      >
                        {restoring === revision.version ? "恢复中…" : "恢复"}
                      </Button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {diffing !== null ? (
        <div
          data-testid={`agent-prompt-version-diff-panel-${diffing.version}`}
          className="rounded-lg border border-border bg-muted/20 p-3"
        >
          <div className="flex items-center justify-between gap-2">
            <h4 className="text-xs font-semibold text-foreground">
              与当前正文的差异（v{diffing.version} → 当前 v{currentVersionOf(revisions)}）
            </h4>
            <Button variant="ghost" size="sm" onClick={() => setDiffing(null)}>
              收起
            </Button>
          </div>
          <pre className="mt-2 max-h-72 overflow-auto whitespace-pre-wrap break-words font-mono text-[11px] leading-relaxed">
            {diffLines(diffing.system_prompt, currentPrompt).map((line, index) => (
              <div
                // biome-ignore lint/suspicious/noArrayIndexKey: diff lines may repeat
                key={`${line.kind}-${index}`}
                className={
                  line.kind === "add"
                    ? "text-success"
                    : line.kind === "remove"
                      ? "text-destructive"
                      : "text-muted-foreground"
                }
              >
                {line.kind === "add" ? "+" : line.kind === "remove" ? "-" : " "} {line.text}
              </div>
            ))}
          </pre>
        </div>
      ) : null}

      <Dialog
        open={viewing !== null}
        onClose={() => setViewing(null)}
        title={viewing === null ? "版本全文" : `版本 v${viewing.version} 全文`}
        description={
          viewing === null
            ? undefined
            : `来源 ${viewing.source} · ${formatDateTime(viewing.created_at)}`
        }
        className="max-w-2xl"
      >
        <pre
          data-testid="agent-prompt-version-view-text"
          className="max-h-[60vh] overflow-auto whitespace-pre-wrap break-words rounded-md border border-border bg-muted/20 p-3 font-mono text-xs"
        >
          {viewing?.system_prompt ?? ""}
        </pre>
      </Dialog>
    </section>
  )
}

function currentVersionOf(revisions: PromptRevision[] | null): string {
  if (revisions === null || revisions.length === 0) return "?"
  return String(revisions[0].version)
}
