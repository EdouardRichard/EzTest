"use client"

import { Loader2, Plus, RefreshCw } from "lucide-react"
import { type FormEvent, useCallback, useEffect, useId, useRef, useState } from "react"

import { StatusBadge, type StatusBadgeVariant } from "@/components/ui/StatusBadge"
import { ApiError } from "@/lib/api-client"

import {
  createClarificationQuestion,
  listClarificationQuestions,
  updateClarificationQuestion,
} from "./api"
import type {
  ClarificationQuestion,
  ClarificationQuestionStatus,
  Requirement,
  UpdateClarificationQuestionBody,
} from "./types"

/** Question/answer bounds from spec §6.6. */
const QUESTION_MAX = 2000
const ANSWER_MAX = 5000
/** The workbench loads every question so the status counters are complete. */
const PAGE_SIZE = 100

export const QUESTION_STATUS_META: Record<
  ClarificationQuestionStatus,
  { label: string; variant: StatusBadgeVariant }
> = {
  open: { label: "待回答", variant: "warning" },
  answered: { label: "已回答", variant: "success" },
  dismissed: { label: "已驳回", variant: "neutral" },
}

/** Human labels for the closed question error reasons (spec §7.1). */
const REASON_LABELS: Record<string, string> = {
  clarification_question_state_conflict: "问题状态已变化，请刷新后重试。",
  clarification_session_not_active: "澄清会话未处于进行中状态，请刷新后重试。",
}

function describeQuestionError(error: unknown): string {
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

type StatusFilter = "all" | ClarificationQuestionStatus

const FILTERS: readonly { value: StatusFilter; label: string }[] = [
  { value: "all", label: "全部" },
  { value: "open", label: "待回答" },
  { value: "answered", label: "已回答" },
  { value: "dismissed", label: "已驳回" },
]

type QuestionCounts = Record<ClarificationQuestionStatus, number>

function countQuestions(items: ClarificationQuestion[]): QuestionCounts {
  const counts: QuestionCounts = { open: 0, answered: 0, dismissed: 0 }
  for (const item of items) counts[item.status] += 1
  return counts
}

type QuestionGroup = {
  key: string
  label: string
  items: ClarificationQuestion[]
}

export type QuestionPanelProps = {
  /** P3-owned version ID; never re-parsed. */
  versionId: string
  /** Requirements used for grouping and the manual-create selector. */
  requirements: Requirement[]
  /** Bumped by the workbench when another clarification panel writes. */
  refreshToken?: number
  /** Notifies the workbench after a successful write. */
  onChanged?: () => void
}

/**
 * Structured clarification questions (spec §9.2): grouped by requirement, with
 * open/answered/dismissed filters and counts, inline answering, dismiss/reopen,
 * and manual question creation. Counts come from the loaded DTOs; nothing is
 * fabricated. A failed write keeps the list on screen and surfaces the reason.
 */
export function QuestionPanel({
  versionId,
  requirements,
  refreshToken = 0,
  onChanged,
}: QuestionPanelProps) {
  const uid = useId()
  const [questions, setQuestions] = useState<ClarificationQuestion[]>([])
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState("")
  const [filter, setFilter] = useState<StatusFilter>("all")
  const [answers, setAnswers] = useState<Record<string, string>>({})
  const [busyId, setBusyId] = useState<string | null>(null)
  const [actionError, setActionError] = useState("")
  const [creating, setCreating] = useState(false)
  const [saving, setSaving] = useState(false)
  const [draft, setDraft] = useState({ question: "", requirementId: "" })
  const [createError, setCreateError] = useState("")
  const requestRef = useRef(0)

  const load = useCallback(async (): Promise<void> => {
    const requestId = ++requestRef.current
    setLoading(true)
    setLoadError("")
    try {
      const page = await listClarificationQuestions(versionId, { limit: PAGE_SIZE })
      if (requestRef.current === requestId) setQuestions(page.items)
    } catch (caught) {
      if (requestRef.current === requestId) setLoadError(describeQuestionError(caught))
    } finally {
      if (requestRef.current === requestId) setLoading(false)
    }
  }, [versionId])

  // `refreshToken` is a workbench-provided reload trigger for writes made in
  // another clarification panel; it is intentionally not read inside the effect.
  // biome-ignore lint/correctness/useExhaustiveDependencies: refreshToken restarts the load
  useEffect(() => {
    void load()
    return () => {
      requestRef.current += 1
    }
  }, [load, refreshToken])

  const counts = countQuestions(questions)
  const visible = filter === "all" ? questions : questions.filter((item) => item.status === filter)

  const groups: QuestionGroup[] = []
  for (const requirement of requirements) {
    const items = visible.filter((item) => item.requirementId === requirement.id)
    if (items.length > 0) {
      groups.push({ key: requirement.id, label: requirement.title, items })
    }
  }
  const knownRequirementIds = new Set(requirements.map((requirement) => requirement.id))
  const versionLevel = visible.filter((item) => item.requirementId === null)
  if (versionLevel.length > 0) {
    groups.push({ key: "version", label: "版本级问题", items: versionLevel })
  }
  const ungrouped = visible.filter(
    (item) => item.requirementId !== null && !knownRequirementIds.has(item.requirementId),
  )
  if (ungrouped.length > 0) {
    groups.push({ key: "unknown", label: "其他需求", items: ungrouped })
  }

  async function mutate(questionId: string, body: UpdateClarificationQuestionBody): Promise<void> {
    setBusyId(questionId)
    setActionError("")
    try {
      await updateClarificationQuestion(versionId, questionId, body)
      await load()
      onChanged?.()
    } catch (caught) {
      setActionError(describeQuestionError(caught))
    } finally {
      setBusyId(null)
    }
  }

  function submitAnswer(item: ClarificationQuestion): void {
    const answer = (answers[item.id] ?? "").trim()
    if (answer === "") {
      setActionError("请输入回答内容。")
      return
    }
    void mutate(item.id, { status: "answered", answer })
  }

  async function handleCreate(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault()
    const question = draft.question.trim()
    if (question === "") {
      setCreateError("请输入问题内容。")
      return
    }
    if (question.length > QUESTION_MAX) {
      setCreateError(`问题不能超过 ${QUESTION_MAX} 个字符。`)
      return
    }
    setSaving(true)
    setCreateError("")
    try {
      await createClarificationQuestion(
        versionId,
        draft.requirementId === ""
          ? { question }
          : { question, requirementId: draft.requirementId },
      )
      setCreating(false)
      setDraft({ question: "", requirementId: "" })
      await load()
      onChanged?.()
    } catch (caught) {
      setCreateError(describeQuestionError(caught))
    } finally {
      setSaving(false)
    }
  }

  return (
    <section aria-label="澄清问题" className="min-w-0 rounded-2xl border border-border bg-card p-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h2 className="text-base font-semibold">澄清问题</h2>
          <p className="mt-0.5 text-xs text-muted-foreground">
            按需求分组，可行内回答、驳回或重开。
          </p>
        </div>
        <div className="flex items-center gap-1.5">
          {loading ? (
            <Loader2
              className="h-3.5 w-3.5 animate-spin text-muted-foreground"
              aria-hidden="true"
            />
          ) : null}
          <button
            type="button"
            onClick={() => {
              setCreating((previous) => !previous)
              setCreateError("")
            }}
            className="inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-2.5 py-1.5 text-xs font-medium transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <Plus className="h-3.5 w-3.5" aria-hidden="true" />
            新增问题
          </button>
        </div>
      </div>

      <fieldset className="mt-2 flex flex-wrap gap-1 border-0 p-0">
        <legend className="sr-only">问题状态筛选</legend>
        {FILTERS.map((option) => {
          const count = option.value === "all" ? questions.length : counts[option.value]
          return (
            <button
              key={option.value}
              type="button"
              aria-pressed={filter === option.value}
              onClick={() => setFilter(option.value)}
              className="rounded-lg border border-border px-2.5 py-1 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted aria-pressed:border-primary/40 aria-pressed:bg-primary/10 aria-pressed:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              {`${option.label} ${count}`}
            </button>
          )
        })}
      </fieldset>

      {loadError === "" ? null : (
        <div
          role="alert"
          className="mt-2 rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
        >
          <p>加载问题失败：{loadError}</p>
          <button
            type="button"
            onClick={() => void load()}
            className="mt-1.5 inline-flex items-center gap-1 rounded-lg border border-border bg-card px-2.5 py-1 text-[11px] font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <RefreshCw className="h-3 w-3" aria-hidden="true" />
            重试
          </button>
        </div>
      )}

      {actionError === "" ? null : (
        <p
          role="alert"
          className="mt-2 rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
        >
          {actionError}
        </p>
      )}

      {creating ? (
        <form
          onSubmit={(event) => void handleCreate(event)}
          className="mt-3 space-y-2 rounded-xl border border-border p-3"
        >
          <div>
            <label
              htmlFor={`${uid}-question`}
              className="text-xs font-medium text-muted-foreground"
            >
              问题内容
            </label>
            <textarea
              id={`${uid}-question`}
              value={draft.question}
              rows={2}
              maxLength={QUESTION_MAX}
              placeholder="输入需要澄清的问题…"
              onChange={(event) =>
                setDraft((previous) => ({ ...previous, question: event.target.value }))
              }
              className="mt-1 w-full resize-y rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
            />
          </div>
          <div>
            <label
              htmlFor={`${uid}-requirement`}
              className="text-xs font-medium text-muted-foreground"
            >
              关联需求
            </label>
            <select
              id={`${uid}-requirement`}
              value={draft.requirementId}
              onChange={(event) =>
                setDraft((previous) => ({ ...previous, requirementId: event.target.value }))
              }
              className="mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <option value="">版本级问题（不绑定需求）</option>
              {requirements.map((requirement) => (
                <option key={requirement.id} value={requirement.id}>
                  {requirement.title}
                </option>
              ))}
            </select>
          </div>
          {createError === "" ? null : (
            <p
              role="alert"
              className="rounded-lg border border-destructive/30 bg-destructive/10 px-2.5 py-1.5 text-xs text-destructive"
            >
              {createError}
            </p>
          )}
          <div className="flex flex-wrap justify-end gap-1.5">
            <button
              type="button"
              onClick={() => {
                setCreating(false)
                setCreateError("")
              }}
              disabled={saving}
              className="rounded-lg border border-border bg-card px-2.5 py-1.5 text-xs font-medium transition-colors hover:bg-muted disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              取消
            </button>
            <button
              type="submit"
              disabled={saving}
              className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              {saving ? <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" /> : null}
              创建问题
            </button>
          </div>
        </form>
      ) : null}

      {loading && questions.length === 0 ? (
        <p className="mt-3 flex items-center gap-1.5 text-xs text-muted-foreground">
          <Loader2 className="h-3 w-3 animate-spin" aria-hidden="true" />
          正在加载问题…
        </p>
      ) : null}

      {!loading && loadError === "" && questions.length === 0 ? (
        <div className="mt-3 rounded-xl border border-dashed border-border px-3 py-4 text-center">
          <p className="text-sm font-medium">暂无澄清问题</p>
          <p className="mt-1 text-xs text-muted-foreground">
            AI 追问或人工录入的问题会显示在这里。
          </p>
        </div>
      ) : null}

      {!loading && questions.length > 0 && visible.length === 0 ? (
        <p className="mt-3 text-xs text-muted-foreground">当前筛选下没有问题。</p>
      ) : null}

      {groups.map((group) => (
        <div key={group.key} className="mt-3">
          <h3 className="text-xs font-semibold text-muted-foreground">{group.label}</h3>
          <ul aria-label={`${group.label}的澄清问题`} className="mt-1.5 space-y-2">
            {group.items.map((item) => {
              const meta = QUESTION_STATUS_META[item.status]
              const answerFieldId = `${uid}-answer-${item.id}`
              const busy = busyId === item.id
              return (
                <li
                  key={item.id}
                  data-testid={`question-row-${item.id}`}
                  className="rounded-xl border border-border px-3 py-2"
                >
                  <div className="flex flex-wrap items-center gap-1.5">
                    <StatusBadge variant={meta.variant}>{meta.label}</StatusBadge>
                    <span className="text-[11px] text-muted-foreground">
                      {item.source === "ai" ? "AI 提出" : "人工录入"}
                    </span>
                  </div>
                  <p className="mt-1 break-words text-sm">{item.question}</p>
                  {item.status === "answered" ? (
                    <p className="mt-1 break-words text-xs text-muted-foreground">
                      {`回答：${item.answer}`}
                    </p>
                  ) : null}

                  {item.status === "open" ? (
                    <div className="mt-2 space-y-1.5">
                      <label
                        htmlFor={answerFieldId}
                        className="text-[11px] font-medium text-muted-foreground"
                      >
                        回答内容
                      </label>
                      <textarea
                        id={answerFieldId}
                        value={answers[item.id] ?? ""}
                        rows={2}
                        maxLength={ANSWER_MAX}
                        placeholder="输入回答…"
                        onChange={(event) =>
                          setAnswers((previous) => ({
                            ...previous,
                            [item.id]: event.target.value,
                          }))
                        }
                        className="w-full resize-y rounded-lg border border-border bg-input px-2.5 py-1.5 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
                      />
                      <div className="flex flex-wrap gap-1.5">
                        <button
                          type="button"
                          onClick={() => submitAnswer(item)}
                          disabled={busy}
                          className="inline-flex items-center gap-1 rounded-lg bg-primary px-2.5 py-1 text-xs font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                        >
                          {busy ? (
                            <Loader2 className="h-3 w-3 animate-spin" aria-hidden="true" />
                          ) : null}
                          保存回答
                        </button>
                        <button
                          type="button"
                          onClick={() => void mutate(item.id, { status: "dismissed" })}
                          disabled={busy}
                          className="rounded-lg border border-border bg-card px-2.5 py-1 text-xs font-medium transition-colors hover:bg-muted disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                        >
                          驳回
                        </button>
                      </div>
                    </div>
                  ) : (
                    <div className="mt-2">
                      <button
                        type="button"
                        onClick={() => void mutate(item.id, { status: "open" })}
                        disabled={busy}
                        className="inline-flex items-center gap-1 rounded-lg border border-border bg-card px-2.5 py-1 text-xs font-medium transition-colors hover:bg-muted disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                      >
                        {busy ? (
                          <Loader2 className="h-3 w-3 animate-spin" aria-hidden="true" />
                        ) : null}
                        重开
                      </button>
                    </div>
                  )}
                </li>
              )
            })}
          </ul>
        </div>
      ))}
    </section>
  )
}
