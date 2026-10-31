"use client"

import { ExternalLink, RotateCcw } from "lucide-react"
import Link from "next/link"
import { useEffect, useState } from "react"

import { Badge, type BadgeTone } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { Dialog } from "@/components/ui/Dialog"
import { InlineError } from "@/components/ui/ErrorState"
import { Field } from "@/components/ui/Field"
import { Textarea } from "@/components/ui/Textarea"
import { listHumanRequests } from "@/features/execution/api"
import { formatDateTime } from "@/lib/format"

import { reopenVersion } from "./api"
import type { Version, VersionStatus } from "./types"

export type VersionHeaderProps = {
  version: Version
  /** Called with the server projection after a successful manual reopen. */
  onReopened?: (version: Version) => void
  /** Opens the shared topbar/page todo drawer. */
  onOpenTodo?: () => void
}

const STATUS_META: Record<VersionStatus, { label: string; tone: BadgeTone }> = {
  initial: { label: "初始", tone: "info" },
  running: { label: "运行中", tone: "success" },
  completed: { label: "已完成", tone: "neutral" },
}

/**
 * Version header (spec §9.1): status badge, SUT chips, closed metadata, the
 * exploration-chain and todo entries, the pending-design CTA and the manual
 * `completed → running` reopen dialog (reason required). Reopening calls the
 * frozen version API and hands the refreshed projection back to the page.
 */
export function VersionHeader({ version, onReopened, onOpenTodo }: VersionHeaderProps) {
  const [openTodoCount, setOpenTodoCount] = useState<number | null>(null)
  const [reopenOpen, setReopenOpen] = useState(false)
  const [reason, setReason] = useState("")
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<unknown>(null)

  useEffect(() => {
    let cancelled = false
    void listHumanRequests(version.id, { status: "open" })
      .then((requests) => {
        if (!cancelled) {
          setOpenTodoCount(requests.filter((request) => request.status === "open").length)
        }
      })
      .catch(() => {
        // A missing count renders no number instead of a misleading zero.
        if (!cancelled) setOpenTodoCount(null)
      })
    return () => {
      cancelled = true
    }
  }, [version.id])

  const statusMeta = STATUS_META[version.status]
  const pendingDesignCount = version.pending_design_count ?? 0
  const completed = version.status === "completed"
  const trimmedReason = reason.trim()
  const submitDisabled = submitting || trimmedReason.length === 0

  async function submitReopen(): Promise<void> {
    if (submitDisabled) return
    setSubmitting(true)
    setError(null)
    try {
      const updated = await reopenVersion(version.id, { reason: trimmedReason })
      setReopenOpen(false)
      setReason("")
      onReopened?.(updated)
    } catch (cause) {
      setError(cause)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <header data-testid="version-header" className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <h1 className="text-xl font-semibold text-foreground">{version.title}</h1>
        <Badge tone={statusMeta.tone}>{statusMeta.label}</Badge>
        {version.auto_design_new_requirements ? <Badge tone="info">新需求自动设计</Badge> : null}
      </div>

      <fieldset className="flex flex-wrap items-center gap-2">
        <legend className="sr-only">版本 SUT</legend>
        {version.bindings.map((binding) => (
          <Badge key={binding.id} tone="info">
            {binding.sut.name}
          </Badge>
        ))}
        <span className="text-[11px] text-muted-foreground">
          创建于 {formatDateTime(version.created_at)}
        </span>
      </fieldset>

      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" variant="secondary" data-testid="version-todo-entry" onClick={onOpenTodo}>
          待办{openTodoCount === null ? "" : ` ${openTodoCount}`}
        </Button>

        <Link
          href={`/versions/${encodeURIComponent(version.id)}?tab=chain`}
          className="inline-flex h-8 items-center gap-1.5 rounded-md border border-border bg-card px-3 text-xs font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          查看探索链路
          <ExternalLink className="size-3.5" aria-hidden="true" />
        </Link>

        {pendingDesignCount > 0 ? (
          <Link
            href={`/versions/${encodeURIComponent(version.id)}?tab=design`}
            data-testid="version-pending-design"
            className="inline-flex h-8 items-center rounded-md border border-warning/40 bg-warning/10 px-3 text-xs font-medium text-warning transition-colors hover:bg-warning/20 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            {pendingDesignCount} 条待设计
          </Link>
        ) : null}

        {completed ? (
          <Button
            size="sm"
            variant="secondary"
            data-testid="version-reopen"
            onClick={() => {
              setReason("")
              setError(null)
              setReopenOpen(true)
            }}
          >
            <RotateCcw className="size-3.5" aria-hidden="true" />
            重新打开
          </Button>
        ) : null}
      </div>

      {completed ? (
        <p
          data-testid="version-closed-info"
          className="rounded-lg border border-border bg-muted/40 px-3 py-2 text-[11px] text-muted-foreground"
        >
          收尾于 {formatDateTime(version.closed_at)}
          {" · "}
          原因：
          {version.close_reason !== null &&
          version.close_reason !== undefined &&
          version.close_reason.length > 0
            ? version.close_reason
            : "未填写"}
        </p>
      ) : null}

      <Dialog
        open={reopenOpen}
        onClose={() => {
          if (!submitting) setReopenOpen(false)
        }}
        title="重新打开版本"
        description="版本将从已完成回到运行中，清空收尾信息并把最新版本报告置为过期。"
        footer={
          <>
            <Button
              variant="secondary"
              size="sm"
              disabled={submitting}
              onClick={() => setReopenOpen(false)}
            >
              取消
            </Button>
            <Button
              size="sm"
              disabled={submitDisabled}
              onClick={() => {
                void submitReopen()
              }}
            >
              {submitting ? "提交中…" : "重新打开"}
            </Button>
          </>
        }
      >
        <div className="space-y-3">
          <Field
            id="version-reopen-reason"
            label="重开原因"
            required
            hint="必填（1–2000 字）；会写入审计与 version.reopened 事件。"
          >
            <Textarea
              id="version-reopen-reason"
              value={reason}
              maxLength={2000}
              disabled={submitting}
              placeholder="例如：需要补充一个回归用例"
              onChange={(event) => setReason(event.target.value)}
            />
          </Field>
          {error !== null ? <InlineError error={error} /> : null}
        </div>
      </Dialog>
    </header>
  )
}
