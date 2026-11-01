"use client"

import { useEffect, useState } from "react"

import { Alert } from "@/components/ui/Alert"
import { Button } from "@/components/ui/Button"
import { Dialog } from "@/components/ui/Dialog"
import { InlineError } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import { getEvidence } from "@/features/execution/api"
import type { Evidence } from "@/features/execution/types"
import { formatBytes, formatDateTime } from "@/lib/format"

import { approveVisualBaseline } from "../api"
import type { VisualAssertion, VisualBaseline } from "../types"

export type BaselineApprovalDialogProps = {
  versionId: string
  assertion: VisualAssertion
  /** Candidate `visual_actual` evidence ids, newest first. */
  evidenceIds: string[]
  open: boolean
  onClose: () => void
  onApproved: (baseline: VisualBaseline) => void
}

/**
 * Admin-only baseline approval (spec §7.2/§9.3). The dialog only offers
 * `visual_actual` evidence; the server re-validates ownership, size, config
 * revision and the environment fingerprint, so a stale candidate is rejected
 * here with the frozen 422 envelope.
 */
export function BaselineApprovalDialog({
  versionId,
  assertion,
  evidenceIds,
  open,
  onClose,
  onApproved,
}: BaselineApprovalDialogProps) {
  const [candidates, setCandidates] = useState<Evidence[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [selectedId, setSelectedId] = useState("")
  const [submitting, setSubmitting] = useState(false)

  useEffect(() => {
    if (!open) return undefined
    let cancelled = false
    setSelectedId(evidenceIds[0] ?? "")
    setError(null)

    if (evidenceIds.length === 0) {
      setCandidates([])
      setLoading(false)
      return () => {
        cancelled = true
      }
    }

    setLoading(true)
    void Promise.all(evidenceIds.map((id) => getEvidence(versionId, id)))
      .then((items) => {
        if (cancelled) return
        setCandidates(items)
        setSelectedId((current) =>
          items.some((item) => item.id === current) ? current : (items[0]?.id ?? ""),
        )
      })
      .catch((cause: unknown) => {
        if (!cancelled) setError(cause)
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })

    return () => {
      cancelled = true
    }
  }, [open, evidenceIds, versionId])

  async function approve(): Promise<void> {
    if (selectedId.length === 0 || submitting) return
    setSubmitting(true)
    setError(null)
    try {
      const baseline = await approveVisualBaseline(versionId, assertion.id, selectedId)
      onApproved(baseline)
      onClose()
    } catch (cause) {
      setError(cause)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title="批准视觉基线"
      description={`断言 ${assertion.step_seq}.${assertion.assertion_seq} · 配置 revision ${assertion.config_revision}`}
      footer={
        <>
          <Button variant="secondary" size="sm" onClick={onClose} disabled={submitting}>
            取消
          </Button>
          <Button
            size="sm"
            disabled={submitting || selectedId.length === 0}
            onClick={() => {
              void approve()
            }}
          >
            {submitting ? "批准中…" : "确认批准"}
          </Button>
        </>
      }
    >
      {loading ? <LoadingCard label="正在加载 actual 证据…" /> : null}

      {!loading && error !== null ? <InlineError error={error} /> : null}

      {!loading && error === null && candidates.length === 0 ? (
        <Alert tone="warning">暂无可用于批准的 visual_actual 证据。</Alert>
      ) : null}

      {!loading && candidates.length > 0 ? (
        <fieldset className="space-y-2">
          <legend className="text-xs font-medium text-foreground">选择 actual 证据</legend>
          {candidates.map((evidence) => (
            <label
              key={evidence.id}
              className="flex cursor-pointer items-start gap-2 rounded-md border border-border px-3 py-2 text-[11px] hover:bg-muted/40"
            >
              <input
                type="radio"
                name="visual-baseline-evidence"
                aria-label={`证据 #${evidence.id}`}
                checked={selectedId === evidence.id}
                disabled={submitting}
                onChange={() => setSelectedId(evidence.id)}
                className="mt-0.5 size-3.5 accent-primary"
              />
              <span className="min-w-0">
                <span className="block font-medium text-foreground">证据 #{evidence.id}</span>
                <span className="block text-muted-foreground">
                  {evidence.filename || "visual_actual"} · {formatBytes(evidence.size_bytes)} ·{" "}
                  {evidence.mime}
                </span>
                <span className="block truncate font-mono text-[10px] text-muted-foreground">
                  sha256: {evidence.sha256}
                </span>
                <span className="block text-[10px] text-muted-foreground">
                  创建于 {formatDateTime(evidence.created_at)}
                </span>
              </span>
            </label>
          ))}
        </fieldset>
      ) : null}
    </Dialog>
  )
}
