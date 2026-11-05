"use client"

import { useId, useState } from "react"

import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { InlineError } from "@/components/ui/ErrorState"
import { Field } from "@/components/ui/Field"
import { Textarea } from "@/components/ui/Textarea"

import { arbitrateFact, type FactArbitrationResult, type VersionFact } from "./api"

export type FactArbitrationCardProps = {
  versionId: string
  /** The fact this card would keep or drop. */
  fact: VersionFact
  /** Number of competing current facts of the same `kind` + `key`. */
  conflictCount?: number
  onArbitrated?: (result: FactArbitrationResult) => void
}

/**
 * Fact conflict arbitration card (API-13). Two contradictory current facts share
 * a `kind` + `key`; the host either **keeps** the selected row (the competitors
 * are superseded) or **drops** it (the competitors stay current). The reason is
 * mandatory, audited and published as `fact.arbitrated`.
 */
export function FactArbitrationCard({
  versionId,
  fact,
  conflictCount,
  onArbitrated,
}: FactArbitrationCardProps) {
  const reasonId = useId()
  const [reason, setReason] = useState("")
  const [busy, setBusy] = useState<"keep" | "drop" | null>(null)
  const [error, setError] = useState<unknown>(null)
  const [notice, setNotice] = useState<string | null>(null)

  const supersededBy = fact.superseded_by
  const trimmed = reason.trim()
  const disabled = busy !== null || trimmed.length === 0

  async function submit(decision: "keep" | "drop"): Promise<void> {
    if (disabled) return
    setBusy(decision)
    setError(null)
    setNotice(null)
    try {
      const result = await arbitrateFact(versionId, fact.id, { decision, reason: trimmed })
      setNotice(
        result.decision === "keep"
          ? `已保留事实 #${result.keep_id}，替代 ${result.dropped_ids.length} 条并存事实`
          : `已丢弃事实 #${result.fact_id}，保留 #${result.keep_id}`,
      )
      setReason("")
      onArbitrated?.(result)
    } catch (cause) {
      setError(cause)
    } finally {
      setBusy(null)
    }
  }

  if (supersededBy !== null) {
    return (
      <section
        data-testid="fact-arbitration"
        aria-label="事实仲裁"
        className="rounded-lg border border-border bg-muted/40 px-3 py-2 text-[11px] text-muted-foreground"
      >
        该事实已被 #{supersededBy} 替代，无需仲裁。
      </section>
    )
  }

  return (
    <section
      data-testid="fact-arbitration"
      aria-label="冲突事实仲裁"
      className="space-y-2 rounded-lg border border-warning/40 bg-warning/5 px-3 py-2"
    >
      <header className="flex flex-wrap items-center gap-2">
        <h3 className="text-xs font-semibold text-foreground">冲突事实仲裁</h3>
        <Badge tone="warning">{conflictCount ?? 2} 条并存事实</Badge>
        <code className="min-w-0 flex-1 truncate text-[11px] text-muted-foreground">
          {fact.key}
        </code>
      </header>
      <p className="text-[11px] text-muted-foreground">
        选择保留本条（替代其他并存事实）或丢弃本条（保留其他事实）；决定会写入审计并发布
        fact.arbitrated 事件。
      </p>

      <Field id={reasonId} label="仲裁原因" required hint="必填（1–2000 字），写入审计。">
        <Textarea
          id={reasonId}
          value={reason}
          maxLength={2000}
          disabled={busy !== null}
          placeholder="例如：以最新一次探索结果为准"
          onChange={(event) => setReason(event.target.value)}
        />
      </Field>

      <div className="flex flex-wrap items-center gap-2">
        <Button
          size="sm"
          disabled={disabled}
          onClick={() => {
            void submit("keep")
          }}
        >
          {busy === "keep" ? "提交中…" : "保留此条"}
        </Button>
        <Button
          size="sm"
          variant="secondary"
          disabled={disabled}
          onClick={() => {
            void submit("drop")
          }}
        >
          {busy === "drop" ? "提交中…" : "丢弃此条"}
        </Button>
      </div>

      {notice !== null ? (
        <p
          data-testid="fact-arbitration-notice"
          role="status"
          className="rounded-md border border-success/30 bg-success/10 px-2 py-1 text-[11px] text-success"
        >
          {notice}
        </p>
      ) : null}
      {error !== null ? <InlineError error={error} /> : null}
    </section>
  )
}
