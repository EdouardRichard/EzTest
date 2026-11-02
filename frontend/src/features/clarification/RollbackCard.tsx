"use client"

import { useState } from "react"

import { Button } from "@/components/ui/Button"
import { InlineError } from "@/components/ui/ErrorState"

import { rollbackRequirement } from "./api"
import type { RequirementDetail, RollbackProposalPart } from "./types"

export type RollbackCardProps = {
  versionId: string
  part: RollbackProposalPart
  /** Message id of the proposal, sent as `proposal_message_id` for traceability. */
  proposalMessageId?: string
  /** Notifies the workspace so the sidebar can refresh the requirement. */
  onRolledBack?: (requirement: RequirementDetail) => void
}

/**
 * AI `rollback_proposal` card. The AI can never roll back on its own (spec
 * §6.1): the user presses "确认回退" and the rollback API creates the draft.
 */
export function RollbackCard({
  versionId,
  part,
  proposalMessageId,
  onRolledBack,
}: RollbackCardProps) {
  const [dismissed, setDismissed] = useState(false)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [done, setDone] = useState(false)

  if (dismissed) return null

  async function rollback(): Promise<void> {
    if (pending || done) return
    setPending(true)
    setError(null)
    try {
      const requirement = await rollbackRequirement(versionId, part.requirement_id, {
        reason: part.reason,
        ...(proposalMessageId !== undefined ? { proposal_message_id: proposalMessageId } : {}),
      })
      setDone(true)
      onRolledBack?.(requirement)
    } catch (cause) {
      setError(cause)
    } finally {
      setPending(false)
    }
  }

  return (
    <div
      data-testid="rollback-card"
      className="rounded-lg border border-warning/40 bg-warning/10 px-3 py-2 text-xs leading-relaxed text-warning"
    >
      <p className="font-semibold">⚠ 需求回退建议</p>
      <p className="mt-1 text-foreground">{part.reason}</p>
      {done ? (
        <p className="mt-2 font-medium">已回退为新的草案版本</p>
      ) : (
        <div className="mt-2 flex items-center gap-2">
          <Button
            size="sm"
            variant="secondary"
            disabled={pending}
            onClick={() => {
              void rollback()
            }}
          >
            {pending ? "回退中…" : "确认回退"}
          </Button>
          <Button size="sm" variant="ghost" disabled={pending} onClick={() => setDismissed(true)}>
            暂不处理
          </Button>
        </div>
      )}
      {error !== null ? (
        <div className="mt-1.5">
          <InlineError error={error} />
        </div>
      ) : null}
    </div>
  )
}
