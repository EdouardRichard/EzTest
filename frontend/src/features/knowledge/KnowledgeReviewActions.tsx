"use client"

import { useEffect, useState } from "react"

import { Button } from "@/components/ui/Button"
import { Dialog } from "@/components/ui/Dialog"
import { InlineError } from "@/components/ui/ErrorState"
import { Field } from "@/components/ui/Field"
import { Textarea } from "@/components/ui/Textarea"

import { approveKnowledge, rejectKnowledge, revokeKnowledge } from "./api"
import type { KnowledgeItemView } from "./types"

type ReasonAction = "reject" | "revoke"

const REASON_META: Record<ReasonAction, { title: string; description: string; label: string }> = {
  reject: {
    title: "驳回知识",
    description: "驳回后条目保留为已驳回，作者可编辑后重新提交审核。",
    label: "驳回原因",
  },
  revoke: {
    title: "撤回知识",
    description: "撤回后条目立即从后续注入中排除；管理员可再次批准恢复。",
    label: "撤回原因",
  },
}

export type KnowledgeReviewActionsProps = {
  item: KnowledgeItemView
  /** The server also enforces admin-only review transitions. */
  isAdmin: boolean
  /** Receives the updated item after a successful transition. */
  onUpdated: (item: KnowledgeItemView) => void
}

/**
 * Admin review toolbar of the knowledge detail (spec §6.1/P7-D-06).
 *
 * draft → approve | reject(reason); approved → revoke(reason);
 * revoked → approve (re-approve). rejected has no review transition: the
 * author edits it back to draft. Normal users never see this toolbar.
 */
export function KnowledgeReviewActions({ item, isAdmin, onUpdated }: KnowledgeReviewActionsProps) {
  const [reasonAction, setReasonAction] = useState<ReasonAction | null>(null)
  const [reason, setReason] = useState("")
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<unknown>(null)

  // A review transition changes the status; close any dialog and clear stale errors.
  // biome-ignore lint/correctness/useExhaustiveDependencies: status is the transition signal
  useEffect(() => {
    setReasonAction(null)
    setReason("")
    setSubmitting(false)
    setError(null)
  }, [item.status])

  if (!isAdmin) return null
  if (item.status === "rejected") return null

  const meta = reasonAction === null ? null : REASON_META[reasonAction]
  const trimmedReason = reason.trim()
  const reasonInvalid = reasonAction !== null && trimmedReason.length === 0

  async function run(action: "approve" | ReasonAction, submittedReason = ""): Promise<void> {
    if (submitting) return
    setSubmitting(true)
    setError(null)
    try {
      const updated =
        action === "approve"
          ? await approveKnowledge(item.id)
          : action === "reject"
            ? await rejectKnowledge(item.id, submittedReason)
            : await revokeKnowledge(item.id, submittedReason)
      setReasonAction(null)
      setReason("")
      onUpdated(updated)
    } catch (cause) {
      setError(cause)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <section data-testid="knowledge-review-actions" aria-label="知识审核" className="space-y-2">
      <h3 className="text-xs font-semibold text-foreground">审核操作（仅管理员）</h3>
      <div className="flex flex-wrap items-center gap-2">
        {item.status === "draft" ? (
          <>
            <Button
              size="sm"
              data-testid="knowledge-review-approve"
              disabled={submitting}
              onClick={() => {
                void run("approve")
              }}
            >
              批准
            </Button>
            <Button
              size="sm"
              variant="danger"
              data-testid="knowledge-review-reject"
              disabled={submitting}
              onClick={() => setReasonAction("reject")}
            >
              驳回
            </Button>
          </>
        ) : null}
        {item.status === "approved" ? (
          <Button
            size="sm"
            variant="danger"
            data-testid="knowledge-review-revoke"
            disabled={submitting}
            onClick={() => setReasonAction("revoke")}
          >
            撤回
          </Button>
        ) : null}
        {item.status === "revoked" ? (
          <Button
            size="sm"
            data-testid="knowledge-review-approve"
            disabled={submitting}
            onClick={() => {
              void run("approve")
            }}
          >
            重新批准
          </Button>
        ) : null}
      </div>
      {error !== null && reasonAction === null ? <InlineError error={error} /> : null}

      {meta !== null ? (
        <Dialog
          open
          title={meta.title}
          description={meta.description}
          onClose={() => setReasonAction(null)}
          footer={
            <>
              <Button
                variant="secondary"
                size="sm"
                data-testid="knowledge-review-cancel"
                disabled={submitting}
                onClick={() => setReasonAction(null)}
              >
                取消
              </Button>
              <Button
                size="sm"
                variant="danger"
                data-testid="knowledge-review-confirm"
                disabled={submitting || reasonInvalid}
                onClick={() => {
                  if (reasonAction !== null) void run(reasonAction, trimmedReason)
                }}
              >
                {submitting ? "提交中…" : meta.title}
              </Button>
            </>
          }
        >
          <div data-testid="knowledge-review-dialog" className="space-y-3">
            <Field
              id="knowledge-review-reason"
              label={meta.label}
              required
              hint="必填（1–2000 字）；会写入审核信息与审计。"
            >
              <Textarea
                id="knowledge-review-reason"
                data-testid="knowledge-review-reason"
                value={reason}
                maxLength={2000}
                disabled={submitting}
                placeholder="说明原因（最多 2000 字）"
                onChange={(event) => setReason(event.target.value)}
              />
            </Field>
            {error !== null ? <InlineError error={error} /> : null}
          </div>
        </Dialog>
      ) : null}
    </section>
  )
}
