"use client"

import { Loader2, Save, Trash2 } from "lucide-react"
import { type FormEvent, useId, useState } from "react"

import { ApiError } from "@/lib/api-client"

import { mindmapApi } from "./api"
import { describeMindmapError } from "./error-messages"
import type { CaseNodeView, CasePriority } from "./types"

const PRIORITY_OPTIONS: readonly CasePriority[] = ["P0", "P1", "P2", "P3"]

const FIELD_CLASS =
  "mt-1 w-full rounded-lg border border-border bg-input px-2.5 py-1.5 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-70"

const PRIMARY_BUTTON_CLASS =
  "inline-flex items-center gap-1.5 rounded-lg bg-primary px-3.5 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"

const SECONDARY_BUTTON_CLASS =
  "inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium transition-colors hover:bg-muted disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"

const DANGER_BUTTON_CLASS =
  "inline-flex items-center gap-1.5 rounded-lg border border-destructive/40 bg-card px-3 py-2 text-sm font-medium text-destructive transition-colors hover:bg-destructive/10 disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"

type EditorErrorState = {
  message: string
  stale: boolean
}

function toEditorError(caught: unknown): EditorErrorState {
  const stale =
    caught instanceof ApiError &&
    caught.status === 409 &&
    caught.details.reason === "case_revision_conflict"
  return {
    message: stale ? "内容已变化，请刷新" : describeMindmapError(caught),
    stale,
  }
}

export type ScenarioEditorProps = {
  versionId: string
  /** The selected scenario node; case nodes render an empty-state guide only. */
  node: CaseNodeView
  /** Server-owned editability flag; the editor never infers it. */
  editable: boolean
  /** Number of descendant nodes removed together with this scenario. */
  childCount: number
  /**
   * Called after a successful write. A node response is passed on save;
   * deletion only calls `onDeleted`.
   */
  onSaved: (node?: CaseNodeView) => void
  onDeleted: (nodeId: string) => void
  /** Optional reload hook rendered in the 409 stale-draft banner. */
  onRefresh?: () => void
}

/**
 * Scenario editor (spec §9.2): only the structural node attributes are
 * editable — scenarios never carry steps, assertions or sources. Deleting the
 * scenario asks for confirmation first and states how many descendants will
 * be removed with it.
 */
export function ScenarioEditor(props: ScenarioEditorProps) {
  if (props.node.kind !== "scenario") {
    return (
      <section
        data-testid="scenario-editor"
        aria-label="场景编辑器"
        className="min-w-0 rounded-2xl border border-border bg-card p-4"
      >
        <h2 className="text-base font-semibold">编辑场景</h2>
        <p className="mt-2 text-sm text-muted-foreground">
          用例节点不包含子场景，请选择场景节点进行编辑，或使用用例编辑器。
        </p>
      </section>
    )
  }

  return <ScenarioEditorForm key={`${props.node.id}:${props.node.revision}`} {...props} />
}

function ScenarioEditorForm({
  versionId,
  node,
  editable,
  childCount,
  onSaved,
  onDeleted,
  onRefresh,
}: ScenarioEditorProps) {
  const uid = useId()
  const [title, setTitle] = useState(node.title)
  const [description, setDescription] = useState(node.description)
  const [priority, setPriority] = useState<CasePriority>(node.priority)
  const [revision, setRevision] = useState(node.revision)
  const [busy, setBusy] = useState<"save" | "delete" | null>(null)
  const [error, setError] = useState<EditorErrorState | null>(null)
  const [confirmDelete, setConfirmDelete] = useState(false)

  async function save(): Promise<void> {
    const trimmed = title.trim()
    if (trimmed === "") {
      setError({ message: "请填写场景标题。", stale: false })
      return
    }

    setBusy("save")
    setError(null)
    try {
      const updated = await mindmapApi.updateNode(versionId, node.id, {
        title: trimmed,
        description: description.trim(),
        priority,
        revision,
      })
      setRevision(updated.revision)
      setTitle(updated.title)
      setDescription(updated.description)
      setPriority(updated.priority)
      onSaved(updated)
    } catch (caught) {
      setError(toEditorError(caught))
    } finally {
      setBusy(null)
    }
  }

  async function remove(): Promise<void> {
    setBusy("delete")
    setError(null)
    try {
      await mindmapApi.deleteNode(versionId, node.id, revision)
      onDeleted(node.id)
    } catch (caught) {
      setError(toEditorError(caught))
    } finally {
      setBusy(null)
    }
  }

  return (
    <section
      data-testid="scenario-editor"
      aria-label="场景编辑器"
      className="min-w-0 space-y-4 rounded-2xl border border-border bg-card p-4"
    >
      <form
        onSubmit={(event: FormEvent<HTMLFormElement>) => {
          event.preventDefault()
          void save()
        }}
        className="space-y-3"
      >
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div>
            <h2 className="text-base font-semibold">编辑场景</h2>
            <p className="mt-1 text-[11px] text-muted-foreground">
              {`修订 ${revision} · 子节点 ${childCount}`}
            </p>
          </div>
          {editable && !confirmDelete ? (
            <button
              type="button"
              disabled={busy !== null}
              onClick={() => setConfirmDelete(true)}
              className={DANGER_BUTTON_CLASS}
            >
              <Trash2 className="h-4 w-4" aria-hidden="true" />
              删除场景
            </button>
          ) : null}
        </div>

        {editable ? null : (
          <p className="rounded-xl border border-amber-500/25 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300">
            场景已冻结，当前为只读。
          </p>
        )}

        <div>
          <label htmlFor={`${uid}-title`} className="text-xs font-medium text-muted-foreground">
            场景标题
          </label>
          <input
            id={`${uid}-title`}
            value={title}
            disabled={!editable || busy !== null}
            autoComplete="off"
            onChange={(event) => setTitle(event.target.value)}
            className={FIELD_CLASS}
          />
        </div>

        <div>
          <label
            htmlFor={`${uid}-description`}
            className="text-xs font-medium text-muted-foreground"
          >
            描述
          </label>
          <textarea
            id={`${uid}-description`}
            value={description}
            disabled={!editable || busy !== null}
            rows={4}
            onChange={(event) => setDescription(event.target.value)}
            className={`${FIELD_CLASS} resize-y`}
          />
        </div>

        <div>
          <label htmlFor={`${uid}-priority`} className="text-xs font-medium text-muted-foreground">
            优先级
          </label>
          <select
            id={`${uid}-priority`}
            value={priority}
            disabled={!editable || busy !== null}
            onChange={(event) => setPriority(event.target.value as CasePriority)}
            className={FIELD_CLASS}
          >
            {PRIORITY_OPTIONS.map((option) => (
              <option key={option} value={option}>
                {option}
              </option>
            ))}
          </select>
        </div>

        {error?.stale ? (
          <div
            data-testid="scenario-revision-conflict"
            role="alert"
            className="flex flex-wrap items-center justify-between gap-2 rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
          >
            <div>
              <p className="font-medium">内容已变化，请刷新</p>
              <p className="mt-0.5">已保留你的本地草稿；刷新后可重新保存。</p>
            </div>
            {onRefresh === undefined ? null : (
              <button type="button" onClick={onRefresh} className={SECONDARY_BUTTON_CLASS}>
                刷新
              </button>
            )}
          </div>
        ) : error !== null ? (
          <p
            role="alert"
            className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
          >
            {error.message}
          </p>
        ) : null}

        {editable ? (
          <div className="flex justify-end">
            <button type="submit" disabled={busy !== null} className={PRIMARY_BUTTON_CLASS}>
              {busy === "save" ? (
                <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
              ) : (
                <Save className="h-4 w-4" aria-hidden="true" />
              )}
              保存场景
            </button>
          </div>
        ) : null}
      </form>

      {confirmDelete ? (
        <div
          role="alertdialog"
          aria-modal="true"
          aria-label="确认删除场景"
          className="rounded-xl border border-destructive/30 bg-destructive/10 p-3"
        >
          <h3 className="text-sm font-semibold">确认删除场景</h3>
          <p className="mt-1 text-xs text-muted-foreground">
            {`确认删除场景「${node.title}」？其下 ${childCount} 个子节点将被一并删除，且不可恢复。`}
          </p>
          <div className="mt-3 flex flex-wrap gap-1.5">
            <button
              type="button"
              disabled={busy !== null}
              onClick={() => void remove()}
              className="inline-flex items-center gap-1.5 rounded-lg bg-destructive px-3 py-2 text-sm font-medium text-white transition-opacity hover:opacity-90 disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              {busy === "delete" ? (
                <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
              ) : (
                <Trash2 className="h-4 w-4" aria-hidden="true" />
              )}
              确认删除
            </button>
            <button
              type="button"
              disabled={busy !== null}
              onClick={() => setConfirmDelete(false)}
              className={SECONDARY_BUTTON_CLASS}
            >
              取消删除
            </button>
          </div>
        </div>
      ) : null}
    </section>
  )
}
