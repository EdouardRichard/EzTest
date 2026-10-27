"use client"

import { Check, ClipboardList, Loader2, RefreshCw, RotateCcw, Save, Trash2, X } from "lucide-react"
import { type FormEvent, useEffect, useId, useState } from "react"

import { EmptyState } from "@/components/ui/EmptyState"
import { StatusBadge } from "@/components/ui/StatusBadge"
import { ApiError } from "@/lib/api-client"

import { AttachmentPanel } from "./AttachmentPanel"
import {
  confirmRequirement,
  createRequirement,
  deleteRequirement,
  reopenRequirement,
  updateRequirement,
} from "./api"
import { REQUIREMENT_STATUS_META } from "./RequirementList"
import type { Requirement, RequirementPriority } from "./types"

const TITLE_MAX = 300
const DESCRIPTION_MAX = 20000
const ACCEPTANCE_MAX = 10000

const PRIORITY_OPTIONS: readonly { value: RequirementPriority; label: string }[] = [
  { value: "must", label: "必须" },
  { value: "should", label: "应该" },
  { value: "could", label: "可以" },
]

/** Human labels for the closed requirement error reasons (spec §7.1). */
const REASON_LABELS: Record<string, string> = {
  revision_conflict: "该需求已被更新，请刷新后重试。",
  requirement_state_conflict: "当前需求状态不允许该操作，请刷新后重试。",
  open_questions_remaining: "仍有待澄清问题，无法确认。请刷新后重试。",
}

function describeRequirementError(error: unknown): string {
  if (!(error instanceof ApiError)) {
    return error instanceof Error ? error.message : "未知错误"
  }
  if (error.status === 0) return "无法连接 API 服务，请确认后端已启动。"
  const reason = typeof error.details.reason === "string" ? error.details.reason : ""
  const label = REASON_LABELS[reason]
  if (label !== undefined) return label
  if (reason !== "") return `${error.message}（${reason}）`
  return error.message
}

/** A 409 means the client state is stale; offer the reload explicitly. */
function isStaleConflict(error: unknown): boolean {
  return error instanceof ApiError && error.status === 409
}

/** `stale` marks a conflict the user can resolve by reloading the requirement. */
type EditorErrorState = {
  message: string
  stale: boolean
  /** The requirement revision the error belongs to; a reload changes it. */
  revision: number
}

type RequirementFormState = {
  title: string
  description: string
  acceptanceCriteria: string
  priority: RequirementPriority
}

function toFormState(requirement: Requirement): RequirementFormState {
  return {
    title: requirement.title,
    description: requirement.description,
    acceptanceCriteria: requirement.acceptanceCriteria,
    priority: requirement.priority,
  }
}

const EMPTY_FORM: RequirementFormState = {
  title: "",
  description: "",
  acceptanceCriteria: "",
  priority: "should",
}

function validate(form: RequirementFormState): string {
  const title = form.title.trim()
  if (title === "") return "请输入需求标题。"
  if (title.length > TITLE_MAX) return `需求标题不能超过 ${TITLE_MAX} 个字符。`
  if (form.description.trim().length > DESCRIPTION_MAX) {
    return `需求描述不能超过 ${DESCRIPTION_MAX} 个字符。`
  }
  if (form.acceptanceCriteria.trim().length > ACCEPTANCE_MAX) {
    return `验收标准不能超过 ${ACCEPTANCE_MAX} 个字符。`
  }
  return ""
}

type RequirementFieldsProps = {
  form: RequirementFormState
  readOnly: boolean
  onChange: (patch: Partial<RequirementFormState>) => void
}

/** Shared title/description/acceptance/priority fields for create and edit. */
function RequirementFields({ form, readOnly, onChange }: RequirementFieldsProps) {
  const uid = useId()

  return (
    <div className="space-y-3">
      <div>
        <label htmlFor={`${uid}-title`} className="text-xs font-medium text-muted-foreground">
          标题
        </label>
        <input
          id={`${uid}-title`}
          value={form.title}
          readOnly={readOnly}
          maxLength={TITLE_MAX}
          autoComplete="off"
          onChange={(event) => onChange({ title: event.target.value })}
          className="mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring read-only:cursor-not-allowed read-only:opacity-80"
        />
      </div>

      <div>
        <label htmlFor={`${uid}-description`} className="text-xs font-medium text-muted-foreground">
          描述
        </label>
        <textarea
          id={`${uid}-description`}
          value={form.description}
          readOnly={readOnly}
          maxLength={DESCRIPTION_MAX}
          rows={4}
          onChange={(event) => onChange({ description: event.target.value })}
          className="mt-1 w-full resize-y rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring read-only:cursor-not-allowed read-only:opacity-80"
        />
      </div>

      <div>
        <label htmlFor={`${uid}-acceptance`} className="text-xs font-medium text-muted-foreground">
          验收标准
        </label>
        <textarea
          id={`${uid}-acceptance`}
          value={form.acceptanceCriteria}
          readOnly={readOnly}
          maxLength={ACCEPTANCE_MAX}
          rows={3}
          onChange={(event) => onChange({ acceptanceCriteria: event.target.value })}
          className="mt-1 w-full resize-y rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring read-only:cursor-not-allowed read-only:opacity-80"
        />
      </div>

      <div>
        <label htmlFor={`${uid}-priority`} className="text-xs font-medium text-muted-foreground">
          优先级
        </label>
        <select
          id={`${uid}-priority`}
          value={form.priority}
          disabled={readOnly}
          onChange={(event) => onChange({ priority: event.target.value as RequirementPriority })}
          className="mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-80"
        >
          {PRIORITY_OPTIONS.map((option) => (
            <option key={option.value} value={option.value}>
              {option.label}
            </option>
          ))}
        </select>
      </div>
    </div>
  )
}

type ErrorBannerProps = {
  message: string
  onRefresh: () => void
}

function EditorError({ message, onRefresh }: ErrorBannerProps) {
  return (
    <div
      role="alert"
      className="flex flex-wrap items-center justify-between gap-2 rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
    >
      <span>{message}</span>
      <button
        type="button"
        onClick={onRefresh}
        className="inline-flex items-center gap-1 rounded-lg border border-border bg-card px-2.5 py-1 text-xs font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <RefreshCw className="h-3 w-3" aria-hidden="true" />
        刷新
      </button>
    </div>
  )
}

type CreateRequirementFormProps = {
  versionId: string
  onCreated: (requirement: Requirement) => void
  onCancel: () => void
}

function CreateRequirementForm({ versionId, onCreated, onCancel }: CreateRequirementFormProps) {
  const [form, setForm] = useState<RequirementFormState>(EMPTY_FORM)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState("")

  function updateForm(patch: Partial<RequirementFormState>): void {
    setForm((previous) => ({ ...previous, ...patch }))
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault()
    const validation = validate(form)
    if (validation !== "") {
      setError(validation)
      return
    }

    setSaving(true)
    setError("")
    try {
      const created = await createRequirement(versionId, {
        title: form.title.trim(),
        description: form.description.trim(),
        acceptanceCriteria: form.acceptanceCriteria.trim(),
        priority: form.priority,
      })
      onCreated(created)
    } catch (caught) {
      setError(describeRequirementError(caught))
    } finally {
      setSaving(false)
    }
  }

  return (
    <form onSubmit={(event) => void handleSubmit(event)} className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-base font-semibold">新增需求</h2>
        <StatusBadge variant="info">创建后为草稿</StatusBadge>
      </div>

      <RequirementFields form={form} readOnly={false} onChange={updateForm} />

      {error === "" ? null : (
        <p
          role="alert"
          className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
        >
          {error}
        </p>
      )}

      <div className="flex flex-wrap justify-end gap-1.5">
        <button
          type="button"
          onClick={onCancel}
          disabled={saving}
          className="rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium transition-colors hover:bg-muted disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          取消
        </button>
        <button
          type="submit"
          disabled={saving}
          className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3.5 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          {saving ? (
            <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
          ) : (
            <Save className="h-4 w-4" aria-hidden="true" />
          )}
          {saving ? "创建中…" : "创建需求"}
        </button>
      </div>
    </form>
  )
}

type EditRequirementFormProps = {
  versionId: string
  requirement: Requirement
  onUpdated: (requirement: Requirement) => void
  onDeleted: (requirementId: string) => void
  onReload: () => void
  onAttachmentsChanged: () => void
}

function EditRequirementForm({
  versionId,
  requirement,
  onUpdated,
  onDeleted,
  onReload,
  onAttachmentsChanged,
}: EditRequirementFormProps) {
  const uid = useId()
  const [form, setForm] = useState<RequirementFormState>(() => toFormState(requirement))
  const [busy, setBusy] = useState<"save" | "confirm" | "reopen" | "delete" | null>(null)
  const [error, setError] = useState<EditorErrorState | null>(null)
  const [confirmDelete, setConfirmDelete] = useState(false)

  const readOnly = requirement.status !== "draft"
  const status = REQUIREMENT_STATUS_META[requirement.status]
  const blockedByQuestions = requirement.openQuestionCount > 0

  useEffect(() => {
    // A requirement that stopped being a draft can no longer be deleted.
    if (requirement.status !== "draft") setConfirmDelete(false)
  }, [requirement.status])

  // A stale 409 banner belongs to one revision; a reloaded requirement hides it.
  const visibleError =
    error !== null && (!error.stale || error.revision === requirement.revision) ? error : null

  function updateForm(patch: Partial<RequirementFormState>): void {
    setForm((previous) => ({ ...previous, ...patch }))
  }

  async function run(
    action: "save" | "confirm" | "reopen" | "delete",
    request: () => Promise<Requirement | undefined>,
  ): Promise<void> {
    setBusy(action)
    setError(null)
    try {
      const result = await request()
      if (action === "delete") {
        onDeleted(requirement.id)
        return
      }
      if (result !== undefined) onUpdated(result)
    } catch (caught) {
      setError({
        message: describeRequirementError(caught),
        stale: isStaleConflict(caught),
        revision: requirement.revision,
      })
    } finally {
      setBusy(null)
    }
  }

  function handleSave(event: FormEvent<HTMLFormElement>): void {
    event.preventDefault()
    const validation = validate(form)
    if (validation !== "") {
      setError({ message: validation, stale: false, revision: requirement.revision })
      return
    }
    void run("save", () =>
      updateRequirement(versionId, requirement.id, {
        revision: requirement.revision,
        title: form.title.trim(),
        description: form.description.trim(),
        acceptanceCriteria: form.acceptanceCriteria.trim(),
        priority: form.priority,
      }),
    )
  }

  return (
    <form onSubmit={handleSave} className="space-y-3">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <h2 className="text-base font-semibold">编辑需求</h2>
          <p className="mt-1 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
            <StatusBadge variant={status.variant}>{status.label}</StatusBadge>
            <span>{`待澄清问题 ${requirement.openQuestionCount}`}</span>
            <span>{`修订 ${requirement.revision}`}</span>
          </p>
        </div>
        {readOnly ? (
          <button
            type="button"
            onClick={() => void run("reopen", () => reopenRequirement(versionId, requirement.id))}
            disabled={busy !== null}
            className="inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium transition-colors hover:bg-muted disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            {busy === "reopen" ? (
              <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
            ) : (
              <RotateCcw className="h-4 w-4" aria-hidden="true" />
            )}
            重开需求
          </button>
        ) : (
          <button
            type="button"
            onClick={() => setConfirmDelete(true)}
            disabled={busy !== null}
            className="inline-flex items-center gap-1.5 rounded-lg border border-destructive/40 bg-card px-3 py-2 text-sm font-medium text-destructive transition-colors hover:bg-destructive/10 disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <Trash2 className="h-4 w-4" aria-hidden="true" />
            删除需求
          </button>
        )}
      </div>

      {readOnly ? (
        <p className="rounded-xl border border-amber-500/25 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300">
          已确认需求为只读，可重开后继续编辑。
        </p>
      ) : null}

      <RequirementFields form={form} readOnly={readOnly} onChange={updateForm} />

      {visibleError === null ? null : visibleError.stale ? (
        <EditorError message={visibleError.message} onRefresh={onReload} />
      ) : (
        <p
          role="alert"
          className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
        >
          {visibleError.message}
        </p>
      )}

      {readOnly ? null : (
        <div className="flex flex-wrap items-center justify-end gap-1.5">
          {blockedByQuestions ? (
            <p
              id={`${uid}-confirm-reason`}
              className="mr-auto text-[11px] text-amber-700 dark:text-amber-300"
            >
              {`仍有 ${requirement.openQuestionCount} 个待澄清问题，确认前需先解决。`}
            </p>
          ) : null}
          <button
            type="button"
            onClick={() => void run("confirm", () => confirmRequirement(versionId, requirement.id))}
            disabled={busy !== null || blockedByQuestions}
            aria-describedby={blockedByQuestions ? `${uid}-confirm-reason` : undefined}
            className="inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            {busy === "confirm" ? (
              <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
            ) : (
              <Check className="h-4 w-4" aria-hidden="true" />
            )}
            确认需求
          </button>
          <button
            type="submit"
            disabled={busy !== null}
            className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3.5 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            {busy === "save" ? (
              <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
            ) : (
              <Save className="h-4 w-4" aria-hidden="true" />
            )}
            保存需求
          </button>
        </div>
      )}

      {confirmDelete ? (
        <div
          role="alertdialog"
          aria-modal="true"
          aria-labelledby={`${uid}-delete-title`}
          className="rounded-xl border border-destructive/30 bg-destructive/10 p-3"
        >
          <h3 id={`${uid}-delete-title`} className="text-sm font-semibold">
            确认删除需求
          </h3>
          <p className="mt-1 text-xs text-muted-foreground">
            {`确认删除需求「${requirement.title}」？删除后关系、附件与问题将一并移除，且不可恢复。`}
          </p>
          <div className="mt-3 flex flex-wrap gap-1.5">
            <button
              type="button"
              onClick={() =>
                void run("delete", async () => {
                  await deleteRequirement(versionId, requirement.id)
                  return undefined
                })
              }
              disabled={busy !== null}
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
              onClick={() => setConfirmDelete(false)}
              disabled={busy !== null}
              className="inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium transition-colors hover:bg-muted disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <X className="h-4 w-4" aria-hidden="true" />
              取消删除
            </button>
          </div>
        </div>
      ) : null}

      <AttachmentPanel
        versionId={versionId}
        requirement={requirement}
        onChanged={onAttachmentsChanged}
      />
    </form>
  )
}

export type RequirementEditorProps = {
  versionId: string
  /** The selected requirement, or null while nothing is selected. */
  requirement: Requirement | null
  /** True while the workbench shows the create form instead of an editor. */
  creating: boolean
  onCreated: (requirement: Requirement) => void
  onUpdated: (requirement: Requirement) => void
  onDeleted: (requirementId: string) => void
  onCancelCreate: () => void
  /** Reloads the workbench data after a stale 409 conflict. */
  onReload: () => void
  /** Lets the workbench refresh requirement counts after attachment writes. */
  onAttachmentsChanged: () => void
}

/**
 * Requirement editor (spec §9.2): create form, draft editing with the
 * optimistic-lock revision, the confirmation gate (disabled while open
 * questions remain, with the reason shown), clarified read-only plus reopen,
 * draft-only delete with confirmation, and the attachment panel.
 */
export function RequirementEditor({
  versionId,
  requirement,
  creating,
  onCreated,
  onUpdated,
  onDeleted,
  onCancelCreate,
  onReload,
  onAttachmentsChanged,
}: RequirementEditorProps) {
  if (creating) {
    return (
      <section
        aria-label="需求编辑器"
        className="min-w-0 rounded-2xl border border-border bg-card p-4"
      >
        <CreateRequirementForm
          versionId={versionId}
          onCreated={onCreated}
          onCancel={onCancelCreate}
        />
      </section>
    )
  }

  if (requirement === null) {
    return (
      <section
        aria-label="需求编辑器"
        className="min-w-0 rounded-2xl border border-border bg-card p-4"
      >
        <EmptyState
          icon={ClipboardList}
          title="未选择需求"
          description="从左侧需求列表选择一条需求进行编辑，或点击「新增需求」。"
        />
      </section>
    )
  }

  return (
    <section
      aria-label="需求编辑器"
      className="min-w-0 rounded-2xl border border-border bg-card p-4"
    >
      <EditRequirementForm
        key={requirement.id}
        versionId={versionId}
        requirement={requirement}
        onUpdated={onUpdated}
        onDeleted={onDeleted}
        onReload={onReload}
        onAttachmentsChanged={onAttachmentsChanged}
      />
    </section>
  )
}
