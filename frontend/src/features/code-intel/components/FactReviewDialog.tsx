"use client"

import { useEffect, useState } from "react"

import { Badge, type BadgeTone } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { Dialog } from "@/components/ui/Dialog"
import { EmptyState } from "@/components/ui/EmptyState"
import { InlineError } from "@/components/ui/ErrorState"
import { LoadingCard, Spinner } from "@/components/ui/Spinner"
import { Textarea } from "@/components/ui/Textarea"
import { formatDateTime } from "@/lib/format"

import type {
  CodeFactReviewVerdict,
  CodeFactReviewView,
  CodeFactView,
  CreateCodeFactReviewInput,
} from "../types"
import {
  CODE_CONFIDENCE_LABELS,
  CODE_CONFIDENCE_ORDER,
  CODE_FACT_KIND_LABELS,
  CODE_REVIEW_VERDICT_LABELS,
} from "../types"
import { useFocusTrap } from "./useFocusTrap"

const REASON_ID = "fact-review-reason"

export type FactReviewDialogProps = {
  fact: CodeFactView | null
  reviews: CodeFactReviewView[]
  reviewsLoading: boolean
  reviewsError: unknown
  submitting: boolean
  error: unknown
  onClose: () => void
  onSubmit: (input: CreateCodeFactReviewInput) => void
  onRetryReviews: () => void
}

const VERDICT_TONE: Record<CodeFactReviewVerdict, BadgeTone> = {
  correct: "success",
  incorrect: "danger",
}

/** Append-only fact review dialog (spec §4.2 #22–#23, §6.2). */
export function FactReviewDialog({
  fact,
  reviews,
  reviewsLoading,
  reviewsError,
  submitting,
  error,
  onClose,
  onSubmit,
  onRetryReviews,
}: FactReviewDialogProps) {
  const [reason, setReason] = useState("")
  const open = fact !== null
  useFocusTrap(open)

  // A new fact always starts with an empty reason box.
  const factId = fact?.id ?? null
  // biome-ignore lint/correctness/useExhaustiveDependencies: factId identifies the reset boundary
  useEffect(() => {
    setReason("")
  }, [factId])

  function submit(verdict: CodeFactReviewVerdict): void {
    if (submitting) return
    const trimmed = reason.trim()
    onSubmit(trimmed.length > 0 ? { verdict, reason: trimmed } : { verdict })
  }

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title="事实复核"
      description={fact !== null ? `${fact.fact_key} · v${fact.version}` : undefined}
      footer={
        <>
          <Button variant="ghost" size="sm" onClick={onClose} disabled={submitting}>
            取消
          </Button>
          <Button
            variant="danger"
            size="sm"
            onClick={() => submit("incorrect")}
            disabled={submitting}
          >
            标记错误
          </Button>
          <Button size="sm" onClick={() => submit("correct")} disabled={submitting}>
            {submitting ? (
              <span aria-hidden="true">
                <Spinner size="sm" label="提交中" />
              </span>
            ) : null}
            标记正确
          </Button>
        </>
      }
    >
      {fact !== null ? (
        <div className="space-y-3">
          <div className="flex flex-wrap items-center gap-2 text-[11px] text-muted-foreground">
            <Badge tone="neutral">{CODE_FACT_KIND_LABELS[fact.kind]}</Badge>
            <Badge tone={CODE_CONFIDENCE_ORDER[fact.confidence] === 2 ? "neutral" : "info"}>
              {CODE_CONFIDENCE_LABELS[fact.confidence]}
            </Badge>
            <span className="font-mono">
              {fact.path}
              {typeof fact.start_line === "number" ? `:${fact.start_line}` : ""}
            </span>
          </div>

          <div>
            <p className="mb-1 text-xs font-medium text-foreground">复核历史</p>
            {reviewsLoading ? (
              <LoadingCard label="正在加载复核历史…" />
            ) : reviewsError !== null ? (
              <div className="space-y-2">
                <InlineError error={reviewsError} />
                <Button variant="secondary" size="sm" onClick={onRetryReviews}>
                  重试
                </Button>
              </div>
            ) : reviews.length === 0 ? (
              <EmptyState title="暂无复核记录" description="提交后记录 append-only 复核历史。" />
            ) : (
              <ul className="space-y-1.5">
                {reviews.map((review) => (
                  <li
                    key={review.id}
                    className="flex flex-wrap items-center gap-2 text-[11px] text-foreground"
                  >
                    <Badge tone={VERDICT_TONE[review.verdict]}>
                      {CODE_REVIEW_VERDICT_LABELS[review.verdict]}
                    </Badge>
                    <span className="text-muted-foreground">
                      v{review.fact_version} · {formatDateTime(review.created_at)}
                    </span>
                    {review.reason.length > 0 ? <span>“{review.reason}”</span> : null}
                  </li>
                ))}
              </ul>
            )}
          </div>

          <div className="flex flex-col gap-1.5">
            <label htmlFor={REASON_ID} className="text-xs font-medium text-foreground">
              复核说明（可选）
            </label>
            <Textarea
              id={REASON_ID}
              value={reason}
              maxLength={2000}
              placeholder="说明判定理由，例如行号/证据是否准确"
              onChange={(event) => setReason(event.target.value)}
              disabled={submitting}
            />
          </div>

          {error !== null ? <InlineError error={error} /> : null}
        </div>
      ) : null}
    </Dialog>
  )
}
