"use client"

import { ArrowDown, ArrowUp, Loader2, Plus, Save, Trash2 } from "lucide-react"
import { type FormEvent, useId, useState } from "react"

import { StatusBadge } from "@/components/ui/StatusBadge"
import { ApiError } from "@/lib/api-client"

import { mindmapApi } from "./api"
import { describeMindmapError } from "./error-messages"
import type {
  AssertionKind,
  CaseAssertionView,
  CaseNodeStatus,
  CaseNodeView,
  CasePriority,
  CaseSourceKind,
  CaseStepView,
} from "./types"

const PRIORITY_OPTIONS: readonly CasePriority[] = ["P0", "P1", "P2", "P3"]

const ASSERTION_KIND_OPTIONS: readonly { value: AssertionKind; label: string }[] = [
  { value: "ui", label: "界面" },
  { value: "api", label: "接口" },
  { value: "db", label: "数据库" },
  { value: "text", label: "文本" },
]

const STATUS_LABEL: Record<CaseNodeStatus, string> = {
  draft: "草稿",
  reviewed: "已评审",
  executed: "已执行",
  supplemented: "已补充",
  merged: "已合并",
  rejected: "已驳回",
  needs_revalidation: "需重验",
}

const SOURCE_LABEL: Record<CaseSourceKind, string> = {
  user_input: "人工",
  ai_generated: "AI",
  ai_supplement: "AI 补充",
}

const FIELD_CLASS =
  "mt-1 w-full rounded-lg border border-border bg-input px-2.5 py-1.5 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-70 read-only:cursor-not-allowed read-only:opacity-70"

const SMALL_BUTTON_CLASS =
  "inline-flex items-center gap-1 rounded-lg border border-border bg-card px-2 py-1 text-[11px] font-medium text-foreground transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"

const ICON_BUTTON_CLASS =
  "inline-flex h-6 w-6 items-center justify-center rounded-lg border border-border bg-card text-foreground transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"

const PRIMARY_BUTTON_CLASS =
  "inline-flex items-center gap-1.5 rounded-lg bg-primary px-3.5 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"

const DANGER_BUTTON_CLASS =
  "inline-flex items-center gap-1.5 rounded-lg border border-destructive/40 bg-card px-3 py-2 text-sm font-medium text-destructive transition-colors hover:bg-destructive/10 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"

type EditorFormState = {
  title: string
  description: string
  priority: CasePriority
  preconditions: string
  testDataText: string
}

function toFormState(node: CaseNodeView): EditorFormState {
  return {
    title: node.title,
    description: node.description,
    priority: node.priority,
    preconditions: node.preconditions,
    testDataText: node.testData === null ? "" : JSON.stringify(node.testData, null, 2),
  }
}

/** Keeps the visible `seq` continuous even before the server reload arrives. */
function orderSteps(steps: readonly CaseStepView[]): CaseStepView[] {
  return [...steps]
    .sort((left, right) => left.seq - right.seq)
    .map((step, index) => ({ ...step, seq: index + 1 }))
}

type EditorErrorState = {
  message: string
  /** A 409 `case_revision_conflict`: the local draft is intentionally kept. */
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

export type CaseEditorProps = {
  versionId: string
  /** The selected case node; scenario nodes render an empty-state guide only. */
  node: CaseNodeView
  /** Server-owned editability flag; the editor never infers it. */
  editable: boolean
  /**
   * Called after a successful write. A node response is passed for node saves;
   * sub-resource writes call it without an argument so the page can refetch.
   */
  onSaved: (node?: CaseNodeView) => void
  onDeleted: (nodeId: string) => void
  /** Optional reload hook rendered in the 409 stale-draft banner. */
  onRefresh?: () => void
}

/**
 * Case editor (spec §9.2): node attributes plus the ordered step/assertion
 * collections. Every write sends the current node revision (OCC); a 409
 * `case_revision_conflict` keeps the local draft and asks for a refresh.
 * Read-only mode (frozen baseline) disables every field and action.
 */
export function CaseEditor(props: CaseEditorProps) {
  if (props.node.kind !== "case") {
    return (
      <section
        data-testid="case-editor"
        aria-label="用例编辑器"
        className="min-w-0 rounded-2xl border border-border bg-card p-4"
      >
        <h2 className="text-base font-semibold">编辑用例</h2>
        <p className="mt-2 text-sm text-muted-foreground">
          场景节点不包含步骤与断言，请选择用例节点进行编辑，或使用场景编辑器。
        </p>
      </section>
    )
  }

  return <CaseEditorForm key={`${props.node.id}:${props.node.revision}`} {...props} />
}

function CaseEditorForm({
  versionId,
  node,
  editable,
  onSaved,
  onDeleted,
  onRefresh,
}: CaseEditorProps) {
  const uid = useId()
  const [form, setForm] = useState<EditorFormState>(() => toFormState(node))
  const [steps, setSteps] = useState<CaseStepView[]>(() => orderSteps(node.steps))
  const [assertions, setAssertions] = useState<CaseAssertionView[]>(() => node.assertions)
  const [revision, setRevision] = useState(node.revision)
  const [busy, setBusy] = useState<string | null>(null)
  const [error, setError] = useState<EditorErrorState | null>(null)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [newStep, setNewStep] = useState({ action: "", expected: "", data: "" })
  const [newAssertion, setNewAssertion] = useState({
    kind: "ui" as AssertionKind,
    target: "",
    expression: "",
    expected: "",
  })

  function updateForm(patch: Partial<EditorFormState>): void {
    setForm((previous) => ({ ...previous, ...patch }))
  }

  function updateStepField(
    stepId: string,
    patch: Partial<Pick<CaseStepView, "action" | "expected" | "data">>,
  ): void {
    setSteps((current) =>
      current.map((step) => (step.id === stepId ? { ...step, ...patch } : step)),
    )
  }

  function updateAssertionField(
    assertionId: string,
    patch: Partial<Pick<CaseAssertionView, "kind" | "target" | "expression" | "expected">>,
  ): void {
    setAssertions((current) =>
      current.map((item) => (item.id === assertionId ? { ...item, ...patch } : item)),
    )
  }

  /** Shared sub-resource write: bumps the local revision and asks for a reload. */
  async function perform(subject: string, request: () => Promise<void>): Promise<void> {
    setBusy(subject)
    setError(null)
    try {
      await request()
      setRevision((current) => current + 1)
      onSaved()
    } catch (caught) {
      setError(toEditorError(caught))
    } finally {
      setBusy(null)
    }
  }

  function parseTestData(): { ok: true; value: unknown } | { ok: false } {
    const text = form.testDataText.trim()
    if (text === "") return { ok: true, value: null }
    try {
      return { ok: true, value: JSON.parse(text) as unknown }
    } catch {
      return { ok: false }
    }
  }

  async function saveNode(): Promise<void> {
    const title = form.title.trim()
    if (title === "") {
      setError({ message: "请填写用例标题。", stale: false })
      return
    }
    const parsed = parseTestData()
    if (!parsed.ok) {
      setError({ message: "测试数据必须是合法的 JSON。", stale: false })
      return
    }

    setBusy("save")
    setError(null)
    try {
      const updated = await mindmapApi.updateNode(versionId, node.id, {
        title,
        description: form.description.trim(),
        priority: form.priority,
        preconditions: form.preconditions.trim(),
        testData: parsed.value,
        revision,
      })
      setRevision(updated.revision)
      setForm(toFormState(updated))
      setSteps(orderSteps(updated.steps))
      setAssertions(updated.assertions)
      onSaved(updated)
    } catch (caught) {
      setError(toEditorError(caught))
    } finally {
      setBusy(null)
    }
  }

  function handleAddStep(): void {
    const action = newStep.action.trim()
    if (action === "") {
      setError({ message: "请填写步骤动作。", stale: false })
      return
    }
    void perform("add-step", async () => {
      const created = await mindmapApi.createStep(versionId, node.id, {
        action,
        expected: newStep.expected.trim(),
        data: newStep.data.trim(),
      })
      setSteps((current) => orderSteps([...current, created]))
      setNewStep({ action: "", expected: "", data: "" })
    })
  }

  function handleSaveStep(step: CaseStepView): void {
    const action = step.action.trim()
    if (action === "") {
      setError({ message: "步骤动作不能为空。", stale: false })
      return
    }
    void perform(`step-${step.id}`, async () => {
      const updated = await mindmapApi.updateStep(versionId, step.id, {
        action,
        expected: step.expected.trim(),
        data: step.data.trim(),
        nodeRevision: revision,
      })
      setSteps((current) =>
        orderSteps(current.map((item) => (item.id === step.id ? updated : item))),
      )
    })
  }

  function handleDeleteStep(step: CaseStepView): void {
    if (steps.length <= 1) return
    void perform(`delete-step-${step.id}`, async () => {
      await mindmapApi.deleteStep(versionId, step.id, revision)
      setSteps((current) => orderSteps(current.filter((item) => item.id !== step.id)))
    })
  }

  function moveStep(index: number, offset: -1 | 1): void {
    const target = index + offset
    if (target < 0 || target >= steps.length) return
    const next = [...steps]
    const [moved] = next.splice(index, 1)
    if (moved === undefined) return
    next.splice(target, 0, moved)
    void perform(`move-step-${moved.id}`, async () => {
      await mindmapApi.reorderSteps(versionId, node.id, {
        stepIds: next.map((step) => step.id),
        nodeRevision: revision,
      })
      // `next` is already the desired order; renumber without re-sorting by the
      // previous `seq` values (which would undo the move).
      setSteps(next.map((step, position) => ({ ...step, seq: position + 1 })))
    })
  }

  function handleAddAssertion(): void {
    const expression = newAssertion.expression.trim()
    if (expression === "") {
      setError({ message: "请填写断言表达式。", stale: false })
      return
    }
    void perform("add-assertion", async () => {
      const created = await mindmapApi.createAssertion(versionId, node.id, {
        kind: newAssertion.kind,
        target: newAssertion.target.trim(),
        expression,
        expected: newAssertion.expected.trim(),
      })
      setAssertions((current) => [...current, created])
      setNewAssertion({ kind: "ui", target: "", expression: "", expected: "" })
    })
  }

  function handleSaveAssertion(assertion: CaseAssertionView): void {
    const expression = assertion.expression.trim()
    if (expression === "") {
      setError({ message: "断言表达式不能为空。", stale: false })
      return
    }
    void perform(`assertion-${assertion.id}`, async () => {
      const updated = await mindmapApi.updateAssertion(versionId, assertion.id, {
        kind: assertion.kind,
        target: assertion.target.trim(),
        expression,
        expected: assertion.expected.trim(),
        nodeRevision: revision,
      })
      setAssertions((current) => current.map((item) => (item.id === assertion.id ? updated : item)))
    })
  }

  function handleDeleteAssertion(assertion: CaseAssertionView): void {
    if (assertions.length <= 1) return
    void perform(`delete-assertion-${assertion.id}`, async () => {
      await mindmapApi.deleteAssertion(versionId, assertion.id, revision)
      setAssertions((current) => current.filter((item) => item.id !== assertion.id))
    })
  }

  async function handleDeleteNode(): Promise<void> {
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

  const controlsDisabled = busy !== null

  return (
    <section
      data-testid="case-editor"
      aria-label="用例编辑器"
      className="min-w-0 space-y-4 rounded-2xl border border-border bg-card p-4"
    >
      <form
        onSubmit={(event: FormEvent<HTMLFormElement>) => {
          event.preventDefault()
          void saveNode()
        }}
        className="space-y-3"
      >
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div className="min-w-0">
            <h2 className="text-base font-semibold">编辑用例</h2>
            <p className="mt-1 flex flex-wrap items-center gap-1.5 text-[11px] text-muted-foreground">
              <StatusBadge variant="neutral">{SOURCE_LABEL[node.source]}</StatusBadge>
              <StatusBadge variant="info">{STATUS_LABEL[node.status]}</StatusBadge>
              <span>{`修订 ${revision}`}</span>
            </p>
          </div>
          {editable && !confirmDelete ? (
            <button
              type="button"
              disabled={controlsDisabled}
              onClick={() => setConfirmDelete(true)}
              className={DANGER_BUTTON_CLASS}
            >
              <Trash2 className="h-4 w-4" aria-hidden="true" />
              删除用例
            </button>
          ) : null}
        </div>

        {editable ? null : (
          <p
            data-testid="case-editor-readonly"
            className="rounded-xl border border-amber-500/25 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300"
          >
            用例已冻结，当前为只读。
          </p>
        )}

        <div>
          <label htmlFor={`${uid}-title`} className="text-xs font-medium text-muted-foreground">
            用例标题
          </label>
          <input
            id={`${uid}-title`}
            value={form.title}
            disabled={!editable || controlsDisabled}
            autoComplete="off"
            onChange={(event) => updateForm({ title: event.target.value })}
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
            value={form.description}
            disabled={!editable || controlsDisabled}
            rows={3}
            onChange={(event) => updateForm({ description: event.target.value })}
            className={`${FIELD_CLASS} resize-y`}
          />
        </div>

        <div className="grid gap-3 sm:grid-cols-2">
          <div>
            <label
              htmlFor={`${uid}-priority`}
              className="text-xs font-medium text-muted-foreground"
            >
              优先级
            </label>
            <select
              id={`${uid}-priority`}
              value={form.priority}
              disabled={!editable || controlsDisabled}
              onChange={(event) => updateForm({ priority: event.target.value as CasePriority })}
              className={FIELD_CLASS}
            >
              {PRIORITY_OPTIONS.map((priority) => (
                <option key={priority} value={priority}>
                  {priority}
                </option>
              ))}
            </select>
          </div>

          <div>
            <label
              htmlFor={`${uid}-preconditions`}
              className="text-xs font-medium text-muted-foreground"
            >
              前置条件
            </label>
            <textarea
              id={`${uid}-preconditions`}
              value={form.preconditions}
              disabled={!editable || controlsDisabled}
              rows={1}
              onChange={(event) => updateForm({ preconditions: event.target.value })}
              className={`${FIELD_CLASS} resize-y`}
            />
          </div>
        </div>

        <div>
          <label htmlFor={`${uid}-test-data`} className="text-xs font-medium text-muted-foreground">
            测试数据（JSON）
          </label>
          <textarea
            id={`${uid}-test-data`}
            value={form.testDataText}
            disabled={!editable || controlsDisabled}
            rows={5}
            spellCheck={false}
            onChange={(event) => updateForm({ testDataText: event.target.value })}
            className={`${FIELD_CLASS} resize-y font-mono`}
          />
        </div>

        {error?.stale ? (
          <div
            role="alert"
            data-testid="case-revision-conflict"
            className="flex flex-wrap items-center justify-between gap-2 rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
          >
            <div>
              <p className="font-medium">内容已变化，请刷新</p>
              <p className="mt-0.5">已保留你的本地草稿；刷新后可重新保存。</p>
            </div>
            {onRefresh === undefined ? null : (
              <button type="button" onClick={onRefresh} className={SMALL_BUTTON_CLASS}>
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
            <button type="submit" disabled={controlsDisabled} className={PRIMARY_BUTTON_CLASS}>
              {busy === "save" ? (
                <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
              ) : (
                <Save className="h-4 w-4" aria-hidden="true" />
              )}
              保存用例
            </button>
          </div>
        ) : null}
      </form>

      {confirmDelete ? (
        <div
          role="alertdialog"
          aria-modal="true"
          aria-label="确认删除用例"
          className="rounded-xl border border-destructive/30 bg-destructive/10 p-3"
        >
          <h3 className="text-sm font-semibold">确认删除用例</h3>
          <p className="mt-1 text-xs text-muted-foreground">
            {`确认删除用例「${node.title}」？步骤、断言、来源与锚点将一并移除，且不可恢复。`}
          </p>
          <div className="mt-3 flex flex-wrap gap-1.5">
            <button
              type="button"
              disabled={controlsDisabled}
              onClick={() => void handleDeleteNode()}
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
              disabled={controlsDisabled}
              onClick={() => setConfirmDelete(false)}
              className={SMALL_BUTTON_CLASS}
            >
              取消删除
            </button>
          </div>
        </div>
      ) : null}

      <section aria-label="用例步骤" className="space-y-2 border-t border-border pt-3">
        <div className="flex items-baseline justify-between gap-2">
          <h3 className="text-sm font-semibold">步骤</h3>
          <span className="text-xs text-muted-foreground">{`${steps.length} 条`}</span>
        </div>

        {steps.length === 0 ? (
          <p className="text-xs text-muted-foreground">该用例还没有步骤，请新增至少一条步骤。</p>
        ) : (
          <ol className="space-y-2">
            {steps.map((step, index) => (
              <li
                key={step.id}
                data-testid={`step-row-${step.seq}`}
                className="rounded-xl border border-border bg-muted/20 p-2"
              >
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <span className="text-xs font-medium text-muted-foreground">
                    {`步骤 ${step.seq}`}
                  </span>
                  <div className="flex items-center gap-1">
                    <button
                      type="button"
                      aria-label={`上移步骤 ${step.seq}`}
                      disabled={!editable || controlsDisabled || index === 0}
                      onClick={() => moveStep(index, -1)}
                      className={ICON_BUTTON_CLASS}
                    >
                      <ArrowUp className="h-3.5 w-3.5" aria-hidden="true" />
                    </button>
                    <button
                      type="button"
                      aria-label={`下移步骤 ${step.seq}`}
                      disabled={!editable || controlsDisabled || index === steps.length - 1}
                      onClick={() => moveStep(index, 1)}
                      className={ICON_BUTTON_CLASS}
                    >
                      <ArrowDown className="h-3.5 w-3.5" aria-hidden="true" />
                    </button>
                    <button
                      type="button"
                      aria-label={`保存步骤 ${step.seq}`}
                      disabled={!editable || controlsDisabled}
                      onClick={() => handleSaveStep(step)}
                      className={SMALL_BUTTON_CLASS}
                    >
                      保存
                    </button>
                    <button
                      type="button"
                      aria-label={`删除步骤 ${step.seq}`}
                      disabled={!editable || controlsDisabled || steps.length <= 1}
                      aria-describedby={steps.length <= 1 ? `${uid}-last-step` : undefined}
                      onClick={() => handleDeleteStep(step)}
                      className={SMALL_BUTTON_CLASS}
                    >
                      删除
                    </button>
                  </div>
                </div>
                <div className="mt-2 grid gap-2 sm:grid-cols-3">
                  <label className="block text-[11px] text-muted-foreground">
                    动作
                    <input
                      aria-label={`步骤 ${step.seq} 动作`}
                      value={step.action}
                      disabled={!editable || controlsDisabled}
                      onChange={(event) => updateStepField(step.id, { action: event.target.value })}
                      className={FIELD_CLASS}
                    />
                  </label>
                  <label className="block text-[11px] text-muted-foreground">
                    预期
                    <input
                      aria-label={`步骤 ${step.seq} 预期`}
                      value={step.expected}
                      disabled={!editable || controlsDisabled}
                      onChange={(event) =>
                        updateStepField(step.id, { expected: event.target.value })
                      }
                      className={FIELD_CLASS}
                    />
                  </label>
                  <label className="block text-[11px] text-muted-foreground">
                    数据
                    <input
                      aria-label={`步骤 ${step.seq} 数据`}
                      value={step.data}
                      disabled={!editable || controlsDisabled}
                      onChange={(event) => updateStepField(step.id, { data: event.target.value })}
                      className={FIELD_CLASS}
                    />
                  </label>
                </div>
              </li>
            ))}
          </ol>
        )}

        {steps.length <= 1 ? (
          <p id={`${uid}-last-step`} className="text-[11px] text-muted-foreground">
            每个用例至少保留一条步骤，最后一条不可删除。
          </p>
        ) : null}

        {editable ? (
          <div className="rounded-xl border border-dashed border-border p-2">
            <p className="text-[11px] font-medium text-muted-foreground">新增步骤</p>
            <div className="mt-1 grid gap-2 sm:grid-cols-3">
              <input
                aria-label="新步骤动作"
                placeholder="动作"
                value={newStep.action}
                disabled={controlsDisabled}
                onChange={(event) =>
                  setNewStep((current) => ({ ...current, action: event.target.value }))
                }
                className={FIELD_CLASS}
              />
              <input
                aria-label="新步骤预期"
                placeholder="预期"
                value={newStep.expected}
                disabled={controlsDisabled}
                onChange={(event) =>
                  setNewStep((current) => ({ ...current, expected: event.target.value }))
                }
                className={FIELD_CLASS}
              />
              <input
                aria-label="新步骤数据"
                placeholder="数据"
                value={newStep.data}
                disabled={controlsDisabled}
                onChange={(event) =>
                  setNewStep((current) => ({ ...current, data: event.target.value }))
                }
                className={FIELD_CLASS}
              />
            </div>
            <div className="mt-2 flex justify-end">
              <button
                type="button"
                disabled={controlsDisabled}
                onClick={handleAddStep}
                className={SMALL_BUTTON_CLASS}
              >
                <Plus className="h-3.5 w-3.5" aria-hidden="true" />
                新增步骤
              </button>
            </div>
          </div>
        ) : null}
      </section>

      <section aria-label="用例断言" className="space-y-2 border-t border-border pt-3">
        <div className="flex items-baseline justify-between gap-2">
          <h3 className="text-sm font-semibold">断言</h3>
          <span className="text-xs text-muted-foreground">{`${assertions.length} 条`}</span>
        </div>

        {assertions.length === 0 ? (
          <p className="text-xs text-muted-foreground">该用例还没有断言，请新增至少一条断言。</p>
        ) : (
          <ol className="space-y-2">
            {assertions.map((assertion, index) => (
              <li
                key={assertion.id}
                data-testid={`assertion-row-${assertion.id}`}
                className="rounded-xl border border-border bg-muted/20 p-2"
              >
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <span className="text-xs font-medium text-muted-foreground">
                    {`断言 ${index + 1}`}
                  </span>
                  <div className="flex items-center gap-1">
                    <button
                      type="button"
                      aria-label={`保存断言 ${index + 1}`}
                      disabled={!editable || controlsDisabled}
                      onClick={() => handleSaveAssertion(assertion)}
                      className={SMALL_BUTTON_CLASS}
                    >
                      保存
                    </button>
                    <button
                      type="button"
                      aria-label={`删除断言 ${index + 1}`}
                      disabled={!editable || controlsDisabled || assertions.length <= 1}
                      aria-describedby={
                        assertions.length <= 1 ? `${uid}-last-assertion` : undefined
                      }
                      onClick={() => handleDeleteAssertion(assertion)}
                      className={SMALL_BUTTON_CLASS}
                    >
                      删除
                    </button>
                  </div>
                </div>
                <div className="mt-2 grid gap-2 sm:grid-cols-2">
                  <label className="block text-[11px] text-muted-foreground">
                    类型
                    <select
                      aria-label={`断言 ${index + 1} 类型`}
                      value={assertion.kind}
                      disabled={!editable || controlsDisabled}
                      onChange={(event) =>
                        updateAssertionField(assertion.id, {
                          kind: event.target.value as AssertionKind,
                        })
                      }
                      className={FIELD_CLASS}
                    >
                      {ASSERTION_KIND_OPTIONS.map((option) => (
                        <option key={option.value} value={option.value}>
                          {option.label}
                        </option>
                      ))}
                    </select>
                  </label>
                  <label className="block text-[11px] text-muted-foreground">
                    目标
                    <input
                      aria-label={`断言 ${index + 1} 目标`}
                      value={assertion.target}
                      disabled={!editable || controlsDisabled}
                      onChange={(event) =>
                        updateAssertionField(assertion.id, { target: event.target.value })
                      }
                      className={FIELD_CLASS}
                    />
                  </label>
                  <label className="block text-[11px] text-muted-foreground">
                    表达式
                    <input
                      aria-label={`断言 ${index + 1} 表达式`}
                      value={assertion.expression}
                      disabled={!editable || controlsDisabled}
                      onChange={(event) =>
                        updateAssertionField(assertion.id, { expression: event.target.value })
                      }
                      className={FIELD_CLASS}
                    />
                  </label>
                  <label className="block text-[11px] text-muted-foreground">
                    预期
                    <input
                      aria-label={`断言 ${index + 1} 预期`}
                      value={assertion.expected}
                      disabled={!editable || controlsDisabled}
                      onChange={(event) =>
                        updateAssertionField(assertion.id, { expected: event.target.value })
                      }
                      className={FIELD_CLASS}
                    />
                  </label>
                </div>
              </li>
            ))}
          </ol>
        )}

        {assertions.length <= 1 ? (
          <p id={`${uid}-last-assertion`} className="text-[11px] text-muted-foreground">
            每个用例至少保留一条断言，最后一条不可删除。
          </p>
        ) : null}

        {editable ? (
          <div className="rounded-xl border border-dashed border-border p-2">
            <p className="text-[11px] font-medium text-muted-foreground">新增断言</p>
            <div className="mt-1 grid gap-2 sm:grid-cols-2">
              <label className="block text-[11px] text-muted-foreground">
                类型
                <select
                  aria-label="新断言类型"
                  value={newAssertion.kind}
                  disabled={controlsDisabled}
                  onChange={(event) =>
                    setNewAssertion((current) => ({
                      ...current,
                      kind: event.target.value as AssertionKind,
                    }))
                  }
                  className={FIELD_CLASS}
                >
                  {ASSERTION_KIND_OPTIONS.map((option) => (
                    <option key={option.value} value={option.value}>
                      {option.label}
                    </option>
                  ))}
                </select>
              </label>
              <input
                aria-label="新断言目标"
                placeholder="目标"
                value={newAssertion.target}
                disabled={controlsDisabled}
                onChange={(event) =>
                  setNewAssertion((current) => ({ ...current, target: event.target.value }))
                }
                className={FIELD_CLASS}
              />
              <input
                aria-label="新断言表达式"
                placeholder="表达式"
                value={newAssertion.expression}
                disabled={controlsDisabled}
                onChange={(event) =>
                  setNewAssertion((current) => ({ ...current, expression: event.target.value }))
                }
                className={FIELD_CLASS}
              />
              <input
                aria-label="新断言预期"
                placeholder="预期"
                value={newAssertion.expected}
                disabled={controlsDisabled}
                onChange={(event) =>
                  setNewAssertion((current) => ({ ...current, expected: event.target.value }))
                }
                className={FIELD_CLASS}
              />
            </div>
            <div className="mt-2 flex justify-end">
              <button
                type="button"
                disabled={controlsDisabled}
                onClick={handleAddAssertion}
                className={SMALL_BUTTON_CLASS}
              >
                <Plus className="h-3.5 w-3.5" aria-hidden="true" />
                新增断言
              </button>
            </div>
          </div>
        ) : null}
      </section>
    </section>
  )
}
