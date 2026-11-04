"use client"

import { useState } from "react"

import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { InlineError } from "@/components/ui/ErrorState"
import { Input } from "@/components/ui/Input"

import {
  answerQuestion,
  type QuestionAnswer,
  type QuestionAnswerInput,
  type QuestionBatch,
  type QuestionItem,
  type QuestionStatus,
  skipQuestion,
} from "./api"
import { questionBatchStatus } from "./useConversation"

export type QuestionCardProps = {
  /** Version scope of the default answer/skip transport. */
  versionId?: string
  /** One `QuestionView` batch (1–5 questions) carried by a conversation message. */
  question: QuestionBatch
  /** Transport override for tests/embeds; defaults to the questions API. */
  onSubmitAnswer?: (
    questionId: string,
    answers: QuestionAnswerInput[],
  ) => Promise<QuestionBatch> | QuestionBatch
  onSubmitSkip?: (questionId: string) => Promise<QuestionBatch> | QuestionBatch
  /** Echo callback: one entry per question after a successful submit. */
  onAnswer?: (answer: QuestionAnswerInput) => void
  onSkip?: (questionId: string) => void
  /** The updated batch after answer/skip; the thread refreshes its pending count. */
  onUpdated?: (question: QuestionBatch) => void
}

type FieldErrors = Record<string, string>

function questionSignature(question: QuestionBatch): string {
  return JSON.stringify([
    question.id,
    question.status,
    question.expires_at,
    question.answers,
    question.answered_at,
    question.skipped,
  ])
}

function toWireAnswer(answer: QuestionAnswerInput): QuestionAnswer {
  return { question_id: answer.questionId, selected: [...answer.selected], text: answer.text }
}

function statusLabel(status: QuestionStatus, skipped: boolean): string {
  switch (status) {
    case "pending":
      return "待回答"
    case "answered":
      return "已回答"
    case "expired":
      return "已过期"
    case "cancelled":
      return skipped ? "已跳过" : "已取消"
  }
}

/**
 * Structured question card (v0.22 R-B3-5/FE-4). The card renders the whole
 * batch, validates required/multiple/free-text answers client-side, submits one
 * structured `answers[]` payload and echoes the answered/skipped/expired state.
 *
 * `QuestionView` has no batch title field, so the card synthesizes "澄清提问"
 * plus the question count. The backend remains the validation authority; this
 * client validation only blocks an obviously incomplete submit (e.g. 请选择).
 */
export function QuestionCard({
  versionId,
  question,
  onSubmitAnswer,
  onSubmitSkip,
  onAnswer,
  onSkip,
  onUpdated,
}: QuestionCardProps) {
  const [selections, setSelections] = useState<Record<string, string[]>>({})
  const [texts, setTexts] = useState<Record<string, string>>({})
  const [fieldErrors, setFieldErrors] = useState<FieldErrors>({})
  const [submitError, setSubmitError] = useState<unknown>(null)
  const [submitting, setSubmitting] = useState(false)
  // Local echo of the batch this card submitted. It is keyed by the prop
  // signature at submit time, so an unchanged parent snapshot cannot overwrite
  // the answered state, while a newer server snapshot (status/answers changed)
  // always wins.
  const [localAnswer, setLocalAnswer] = useState<{
    signature: string
    question: QuestionBatch
  } | null>(null)

  const signature = questionSignature(question)
  const current =
    localAnswer !== null && localAnswer.signature === signature ? localAnswer.question : question
  const status = questionBatchStatus(current)
  const readOnly = status !== "pending"
  const busy = submitting || readOnly
  const label = statusLabel(status, current.skipped === true)

  function clearFieldError(questionId: string): void {
    setFieldErrors((previous) => {
      if (!(questionId in previous)) return previous
      const next = { ...previous }
      delete next[questionId]
      return next
    })
  }

  function selectedFor(item: QuestionItem): string[] {
    const chosen = selections[item.id]
    if (chosen !== undefined) return chosen
    return current.answers.find((answer) => answer.question_id === item.id)?.selected ?? []
  }

  function textFor(item: QuestionItem): string {
    const typed = texts[item.id]
    if (typed !== undefined) return typed
    return current.answers.find((answer) => answer.question_id === item.id)?.text ?? ""
  }

  function chooseOption(item: QuestionItem, optionId: string): void {
    if (busy) return
    clearFieldError(item.id)
    setSelections((previous) => {
      const selected = previous[item.id] ?? []
      const next = item.multiple
        ? selected.includes(optionId)
          ? selected.filter((id) => id !== optionId)
          : [...selected, optionId]
        : [optionId]
      return { ...previous, [item.id]: next }
    })
  }

  function updateText(item: QuestionItem, value: string): void {
    if (busy) return
    clearFieldError(item.id)
    setTexts((previous) => ({ ...previous, [item.id]: value }))
  }

  function validate(): FieldErrors {
    const errors: FieldErrors = {}
    for (const item of current.questions) {
      const selected = selections[item.id] ?? []
      const text = (texts[item.id] ?? "").trim()
      if (text.length > 0 && !item.allow_free_text) {
        errors[item.id] = "该问题不支持补充说明"
        continue
      }
      if (item.required && selected.length === 0 && text.length === 0) {
        errors[item.id] = item.options.length > 0 ? "请选择" : "请填写"
        continue
      }
      if (!item.multiple && selected.length > 1) {
        errors[item.id] = "只能选择一项"
      }
    }
    return errors
  }

  async function resolveAnswer(answers: QuestionAnswerInput[]): Promise<QuestionBatch> {
    if (onSubmitAnswer !== undefined) return onSubmitAnswer(current.id, answers)
    if (versionId === undefined || versionId === "") {
      throw new Error("问题提交接口未配置")
    }
    return answerQuestion(versionId, current.id, answers.map(toWireAnswer))
  }

  async function resolveSkip(): Promise<QuestionBatch> {
    if (onSubmitSkip !== undefined) return onSubmitSkip(current.id)
    if (versionId === undefined || versionId === "") {
      throw new Error("问题提交接口未配置")
    }
    return skipQuestion(versionId, current.id)
  }

  async function submit(): Promise<void> {
    if (busy) return
    const errors = validate()
    setFieldErrors(errors)
    if (Object.keys(errors).length > 0) return

    const answers: QuestionAnswerInput[] = current.questions.map((item) => ({
      questionId: item.id,
      selected: [...(selections[item.id] ?? [])],
      text: (texts[item.id] ?? "").trim(),
    }))
    setSubmitting(true)
    setSubmitError(null)
    try {
      const updated = await resolveAnswer(answers)
      setLocalAnswer({ signature, question: updated })
      for (const answer of answers) onAnswer?.(answer)
      onUpdated?.(updated)
    } catch (cause) {
      // Keep the draft selections: a retryable API error must not discard them.
      setSubmitError(cause)
    } finally {
      setSubmitting(false)
    }
  }

  async function skip(): Promise<void> {
    if (busy) return
    setSubmitting(true)
    setSubmitError(null)
    try {
      const updated = await resolveSkip()
      setLocalAnswer({ signature, question: updated })
      onSkip?.(current.id)
      onUpdated?.(updated)
    } catch (cause) {
      setSubmitError(cause)
    } finally {
      setSubmitting(false)
    }
  }

  const tone = status === "answered" ? "success" : status === "pending" ? "warning" : "neutral"

  return (
    <section
      data-testid="question-card"
      data-question-id={current.id}
      aria-label="结构化提问"
      className="rounded-xl border border-primary/30 bg-background/70 p-3 text-xs"
    >
      <header className="flex flex-wrap items-center gap-1.5">
        <h3 className="text-sm font-semibold text-foreground">澄清提问</h3>
        <Badge tone="neutral">共 {current.questions.length} 题</Badge>
        <Badge tone={tone}>{label}</Badge>
      </header>
      {current.batch_id !== "" ? (
        <p className="mt-1 text-[10px] text-muted-foreground">批次 {current.batch_id}</p>
      ) : null}

      <ol className="mt-2 space-y-3">
        {current.questions.map((item) => {
          const selected = selectedFor(item)
          const error = fieldErrors[item.id]
          return (
            <li key={item.id} data-testid={`question-item-${item.id}`} className="space-y-1.5">
              <div className="flex flex-wrap items-center gap-1.5">
                <p className="font-medium text-foreground">{item.text}</p>
                {item.required ? <Badge tone="warning">必填</Badge> : null}
                {item.multiple ? <Badge tone="neutral">多选</Badge> : null}
              </div>

              {item.options.length > 0 ? (
                <fieldset
                  aria-label={item.text}
                  disabled={readOnly || submitting}
                  className="space-y-1 border-0 p-0"
                >
                  {item.options.map((option) => (
                    <label
                      key={option.id}
                      className="flex cursor-pointer items-start gap-2 text-xs text-foreground"
                    >
                      <input
                        type={item.multiple ? "checkbox" : "radio"}
                        name={`question-${current.id}-${item.id}`}
                        value={option.id}
                        checked={selected.includes(option.id)}
                        disabled={readOnly || submitting}
                        onChange={() => chooseOption(item, option.id)}
                        aria-label={option.label}
                        className="mt-0.5 accent-primary"
                      />
                      <span>{option.label}</span>
                    </label>
                  ))}
                </fieldset>
              ) : null}

              {item.allow_free_text ? (
                <Input
                  aria-label={`${item.text} 补充说明`}
                  placeholder="补充说明（选填）"
                  value={textFor(item)}
                  disabled={readOnly || submitting}
                  maxLength={2000}
                  onChange={(event) => updateText(item, event.target.value)}
                  className="h-8 text-xs"
                />
              ) : null}

              {error !== undefined ? (
                <p role="alert" className="text-[11px] text-destructive">
                  {error}
                </p>
              ) : null}

              {item.source_refs.length > 0 ? (
                <p
                  data-testid={`question-source-refs-${item.id}`}
                  className="text-[10px] text-muted-foreground"
                >
                  来源：{item.source_refs.join("、")}
                </p>
              ) : null}
            </li>
          )
        })}
      </ol>

      {status === "answered" ? (
        <ul data-testid="question-answer-echo" className="mt-2 space-y-1.5">
          {current.questions.map((item) => {
            const answer = current.answers.find((entry) => entry.question_id === item.id)
            if (answer === undefined) return null
            const labels = answer.selected.map(
              (id) => item.options.find((option) => option.id === id)?.label ?? id,
            )
            const summary = [labels.join("、"), answer.text]
              .filter((value) => value !== "")
              .join("；")
            return (
              <li
                key={item.id}
                data-testid={`question-answer-${item.id}`}
                className="rounded-md bg-success/10 px-2 py-1 text-[11px] text-foreground"
              >
                <span className="font-medium">你的回答：</span>
                {summary === "" ? "（未填写）" : summary}
              </li>
            )
          })}
        </ul>
      ) : null}

      {status === "cancelled" ? (
        <p className="mt-2 text-[11px] text-muted-foreground">
          {current.skipped ? "问题已跳过，可继续对话。" : "问题已取消。"}
        </p>
      ) : null}

      {status === "expired" ? (
        <p data-testid="question-expired-note" className="mt-2 text-[11px] text-muted-foreground">
          该问题已过期，可继续对话或由 AI 重新提问。
        </p>
      ) : null}

      {status !== "expired" ? (
        <div className="mt-2 flex items-center gap-2">
          <Button
            size="sm"
            disabled={busy}
            onClick={() => {
              void submit()
            }}
          >
            {submitting ? "提交中…" : "提交"}
          </Button>
          <Button
            size="sm"
            variant="secondary"
            disabled={busy}
            onClick={() => {
              void skip()
            }}
          >
            {submitting ? "处理中…" : "跳过"}
          </Button>
        </div>
      ) : null}

      {submitError !== null ? (
        <div className="mt-1.5">
          <InlineError error={submitError} />
        </div>
      ) : null}
    </section>
  )
}
