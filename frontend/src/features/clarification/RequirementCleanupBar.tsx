"use client"

import { useEffect, useState } from "react"

import { Button } from "@/components/ui/Button"
import { cn } from "@/components/ui/cn"
import { InlineError } from "@/components/ui/ErrorState"

/** One selectable draft: the ID is sent to the API, the title is user-facing. */
export type CleanupDraft = {
  id: string
  title: string
}

export type RequirementCleanupBarProps = {
  /** Number of draft requirements in the version (confirmed ones excluded). */
  draftCount: number
  /** Drafts of the current selection; clarified selections never appear here. */
  selectedDrafts: CleanupDraft[]
  /** True while a delete/merge/dedupe request (or its refresh) is in flight. */
  busy?: boolean
  /** Result line shown only after a successful request/refresh. */
  notice?: string | null
  error?: unknown
  onDelete: (requirementIds: string[]) => void
  onMerge: (targetRequirementId: string, sourceRequirementIds: string[]) => void
  onDedupe: () => void
}

type PendingAction = "delete" | "merge" | "dedupe" | null

/**
 * Draft-only cleanup toolbar for the clarification workspace (Task 83). Drafts
 * can be deleted, merged into a canonical draft, or deduped across the version.
 * Destructive actions are confirm-gated; confirmed requirements never reach
 * these actions because only draft selections are passed in.
 */
export function RequirementCleanupBar({
  draftCount,
  selectedDrafts,
  busy = false,
  notice = null,
  error = null,
  onDelete,
  onMerge,
  onDedupe,
}: RequirementCleanupBarProps) {
  const [pending, setPending] = useState<PendingAction>(null)
  const [mergeTarget, setMergeTarget] = useState("")

  const hasSelection = selectedDrafts.length > 0
  const canMerge = selectedDrafts.length >= 2
  const canDedupe = draftCount >= 2
  // Keep the bar mounted when only the feedback remains (e.g. after deleting
  // the last duplicate draft) so the user always sees the outcome.
  const visible = hasSelection || canDedupe || notice !== null || error !== null

  useEffect(() => {
    if (pending === "merge" && !selectedDrafts.some((draft) => draft.id === mergeTarget)) {
      setMergeTarget(selectedDrafts[0]?.id ?? "")
    }
  }, [pending, selectedDrafts, mergeTarget])

  if (!visible) return null

  function start(action: Exclude<PendingAction, null>): void {
    if (action === "merge") setMergeTarget(selectedDrafts[0]?.id ?? "")
    setPending(action)
  }

  function cancel(): void {
    setPending(null)
  }

  function confirm(): void {
    if (pending === "delete") {
      const ids = selectedDrafts.map((draft) => draft.id)
      setPending(null)
      onDelete(ids)
      return
    }
    if (pending === "merge" && mergeTarget !== "" && canMerge) {
      const sources = selectedDrafts
        .filter((draft) => draft.id !== mergeTarget)
        .map((draft) => draft.id)
      setPending(null)
      onMerge(mergeTarget, sources)
      return
    }
    if (pending === "dedupe") {
      setPending(null)
      onDedupe()
    }
  }

  return (
    <section
      data-testid="requirement-cleanup-bar"
      aria-label="草案清理"
      className="mx-3 mt-2.5 rounded-lg border border-dashed border-border bg-muted/40 px-2.5 py-2 text-[11px] text-muted-foreground"
    >
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium text-foreground">草案清理</span>
        {hasSelection ? <span>已选 {selectedDrafts.length} 条草案</span> : null}
        <div className="ml-auto flex flex-wrap items-center gap-1.5">
          {canDedupe ? (
            <Button
              data-testid="cleanup-dedupe"
              size="sm"
              variant="secondary"
              disabled={busy}
              onClick={() => start("dedupe")}
            >
              去重重复草案
            </Button>
          ) : null}
          {hasSelection ? (
            <Button
              data-testid="cleanup-delete"
              size="sm"
              variant="secondary"
              disabled={busy}
              onClick={() => start("delete")}
            >
              删除所选草案
            </Button>
          ) : null}
          {hasSelection ? (
            <Button
              data-testid="cleanup-merge"
              size="sm"
              variant="secondary"
              disabled={busy || !canMerge}
              onClick={() => start("merge")}
            >
              合并所选草案
            </Button>
          ) : null}
        </div>
      </div>

      {!canMerge && hasSelection ? (
        <p className="mt-1 text-[10.5px]">至少选择 2 条草案才能合并</p>
      ) : null}

      {pending !== null ? (
        <div
          data-testid="cleanup-confirm-panel"
          className={cn("mt-1.5 rounded-md border border-border bg-background/70 px-2 py-1.5")}
        >
          {pending === "delete" ? (
            <p>将 {selectedDrafts.length} 条草案标记为 obsolete（不删除历史与已确认需求）。</p>
          ) : null}
          {pending === "merge" ? (
            <div className="flex flex-wrap items-center gap-1.5">
              <label htmlFor="cleanup-merge-target" className="text-foreground">
                合并到
              </label>
              <select
                id="cleanup-merge-target"
                data-testid="cleanup-merge-target"
                aria-label="合并目标"
                value={mergeTarget}
                disabled={busy}
                onChange={(event) => setMergeTarget(event.target.value)}
                className="h-7 rounded-md border border-border bg-card px-1.5 text-[11px] text-foreground"
              >
                {selectedDrafts.map((draft) => (
                  <option key={draft.id} value={draft.id}>
                    {draft.title}
                  </option>
                ))}
              </select>
              <span>，其余 {selectedDrafts.length - 1} 条将标记为 obsolete。</span>
            </div>
          ) : null}
          {pending === "dedupe" ? (
            <p>将保留每组重复草案中最早的一条，其余标记为 obsolete（已确认需求不受影响）。</p>
          ) : null}
          <div className="mt-1.5 flex flex-wrap gap-1.5">
            <Button
              data-testid="cleanup-confirm"
              size="sm"
              variant={pending === "delete" ? "danger" : "primary"}
              disabled={busy || (pending === "merge" && !canMerge)}
              onClick={confirm}
            >
              {busy
                ? "处理中…"
                : pending === "delete"
                  ? "确认删除"
                  : pending === "merge"
                    ? "确认合并"
                    : "确认去重"}
            </Button>
            <Button
              data-testid="cleanup-cancel"
              size="sm"
              variant="ghost"
              disabled={busy}
              onClick={cancel}
            >
              取消
            </Button>
          </div>
        </div>
      ) : null}

      {notice !== null ? (
        <p data-testid="cleanup-notice" role="status" className="mt-1 text-[11px] text-success">
          {notice}
        </p>
      ) : null}
      {error !== null ? (
        <div className="mt-1">
          <InlineError error={error} />
        </div>
      ) : null}
    </section>
  )
}
