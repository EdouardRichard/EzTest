"use client"

import { useEffect, useState } from "react"

import { Alert } from "@/components/ui/Alert"
import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { InlineError } from "@/components/ui/ErrorState"
import { Field } from "@/components/ui/Field"
import { Input } from "@/components/ui/Input"
import { Textarea } from "@/components/ui/Textarea"
import { formatDateTime, formatDuration } from "@/lib/format"

import { HUMAN_REQUEST_KIND_LABEL } from "./labels"
import type {
  HumanDecision,
  HumanRequest,
  HumanRequestKind,
  HumanRequestResponseInput,
} from "./types"

/** The three v0.22 version-level decision kinds (spec R-W2-1). */
const DECISION_KINDS: readonly HumanRequestKind[] = ["approval", "blocked_recovery", "conflict"]

function isDecisionRequest(request: HumanRequest): boolean {
  return DECISION_KINDS.includes(request.kind)
}

export type HumanRequestBannerProps = {
  /**
   * Pending human requests of every kind. `login_takeover` renders the 2FA
   * form; approval/blocked_recovery/conflict render decision cards. Structured
   * questions are version todos and are ignored here.
   */
  requests: HumanRequest[]
  onResolve: (id: string, response: HumanRequestResponseInput) => Promise<void>
  onCancel: (id: string) => Promise<void>
  /** Read-only projection (non-admin/observers): hides every mutating action. */
  readOnly?: boolean
  /** Fixed clock for tests/countdown determinism; defaults to the wall clock. */
  now?: number
}

/**
 * Human-work banner of the execution workspace.
 *
 * - `login_takeover` keeps the P3 2FA flow: a code/note resolves it and the
 *   server resumes the paused wave.
 * - `approval`/`blocked_recovery`/`conflict` are v0.22 version decision
 *   requests (R-W2-1). They render as decision cards with 批准/拒绝 and a
 *   mandatory rejection reason, and resolve through the same
 *   `POST /api/human-requests/{id}/resolve` route with `{decision, reason}`.
 *
 * Structured clarification questions are version todos and are never rendered
 * here (they would otherwise reach the code/2FA form).
 */
export function HumanRequestBanner({
  requests,
  onResolve,
  onCancel,
  readOnly = false,
  now,
}: HumanRequestBannerProps) {
  const pending = requests.filter((request) => request.status === "pending")
  const decisions = pending.filter(isDecisionRequest)
  const takeovers = pending.filter((request) => request.kind === "login_takeover")

  if (decisions.length === 0 && takeovers.length === 0) return null

  return (
    <div className="space-y-2">
      {decisions.length > 0 ? (
        <HumanDecisionCards requests={decisions} onResolve={onResolve} readOnly={readOnly} />
      ) : null}
      {takeovers.length > 0 ? (
        <LoginTakeoverBanner
          takeovers={takeovers}
          onResolve={onResolve}
          onCancel={onCancel}
          now={now}
        />
      ) : null}
    </div>
  )
}

export type HumanDecisionCardsProps = {
  requests: HumanRequest[]
  onResolve: (id: string, response: HumanRequestResponseInput) => Promise<void>
  readOnly?: boolean
}

/**
 * The version-level decision cards. Exported separately so the version overview
 * (where the todo lives) can render the same approval/blocked/conflict surface.
 */
export function HumanDecisionCards({
  requests,
  onResolve,
  readOnly = false,
}: HumanDecisionCardsProps) {
  const pending = requests.filter(
    (request) => request.status === "pending" && isDecisionRequest(request),
  )
  if (pending.length === 0) return null
  return (
    <div className="space-y-2" data-testid="human-decision-list">
      {pending.map((request) => (
        <HumanDecisionCard
          key={request.id}
          request={request}
          onResolve={onResolve}
          readOnly={readOnly}
        />
      ))}
    </div>
  )
}

function HumanDecisionCard({
  request,
  onResolve,
  readOnly,
}: {
  request: HumanRequest
  onResolve: (id: string, response: HumanRequestResponseInput) => Promise<void>
  readOnly: boolean
}) {
  const [reason, setReason] = useState("")
  const [busy, setBusy] = useState<HumanDecision | null>(null)
  const [error, setError] = useState<unknown>(null)
  const [reasonError, setReasonError] = useState<string | null>(null)
  const reasonId = `human-decision-reason-${request.id}`
  const disabled = readOnly || busy !== null

  async function decide(decision: HumanDecision): Promise<void> {
    if (disabled) return
    const trimmed = reason.trim()
    if (decision === "rejected" && trimmed.length === 0) {
      setReasonError("拒绝时必须填写原因")
      return
    }
    setBusy(decision)
    setError(null)
    setReasonError(null)
    try {
      const response: HumanRequestResponseInput = { decision }
      if (trimmed.length > 0) response.reason = trimmed
      await onResolve(request.id, response)
    } catch (cause) {
      setError(cause)
    } finally {
      setBusy(null)
    }
  }

  return (
    <Alert
      tone="warning"
      title={`需要人工决策：${HUMAN_REQUEST_KIND_LABEL[request.kind]}`}
      className="space-y-2"
    >
      <div data-testid={`human-decision-card-${request.id}`} className="space-y-2">
        <div className="flex flex-wrap items-center gap-2">
          <Badge tone="warning">{HUMAN_REQUEST_KIND_LABEL[request.kind]}</Badge>
          {request.expires_at !== "" ? (
            <span className="text-muted-foreground">
              过期时间 {formatDateTime(request.expires_at)}
            </span>
          ) : null}
          {readOnly ? <Badge tone="neutral">只读</Badge> : null}
        </div>

        <p data-testid={`human-decision-reason-${request.id}`} className="text-xs text-foreground">
          {request.payload.reason}
        </p>

        <Field
          id={reasonId}
          label="原因"
          hint="可填写决策说明（拒绝必填）"
          error={reasonError ?? undefined}
        >
          <Input
            id={reasonId}
            value={reason}
            disabled={disabled}
            placeholder={readOnly ? "当前为只读视图" : "输入决策原因"}
            onChange={(event) => {
              setReasonError(null)
              setReason(event.target.value)
            }}
          />
        </Field>

        <div className="flex flex-wrap items-center gap-2">
          <Button
            size="sm"
            disabled={disabled}
            onClick={() => {
              void decide("approved")
            }}
          >
            {busy === "approved" ? "批准中…" : "批准"}
          </Button>
          <Button
            size="sm"
            variant="secondary"
            disabled={disabled}
            onClick={() => {
              void decide("rejected")
            }}
          >
            {busy === "rejected" ? "拒绝中…" : "拒绝"}
          </Button>
        </div>

        {error !== null ? <InlineError error={error} /> : null}
      </div>
    </Alert>
  )
}

function LoginTakeoverBanner({
  takeovers,
  onResolve,
  onCancel,
  now,
}: {
  takeovers: HumanRequest[]
  onResolve: (id: string, response: HumanRequestResponseInput) => Promise<void>
  onCancel: (id: string) => Promise<void>
  now?: number
}) {
  const current = takeovers[0]
  const [clock, setClock] = useState(() => now ?? Date.now())
  const [code, setCode] = useState("")
  const [note, setNote] = useState("")
  const [busy, setBusy] = useState<"resolve" | "cancel" | null>(null)
  const [error, setError] = useState<unknown>(null)

  useEffect(() => {
    if (now !== undefined) {
      setClock(now)
      return undefined
    }
    setClock(Date.now())
    const timer = setInterval(() => setClock(Date.now()), 1000)
    return () => clearInterval(timer)
  }, [now])

  // biome-ignore lint/correctness/useExhaustiveDependencies: a new request resets the draft fields
  useEffect(() => {
    setCode("")
    setNote("")
    setBusy(null)
    setError(null)
  }, [current?.id])

  if (current === undefined) return null

  const expiresAt = Date.parse(current.expires_at)
  const remaining = Number.isNaN(expiresAt) ? 0 : expiresAt - clock
  const expired = remaining <= 0
  const disabled = expired || busy !== null

  async function resolve(): Promise<void> {
    if (current === undefined || disabled) return
    setBusy("resolve")
    setError(null)
    try {
      const response: HumanRequestResponseInput = {}
      if (code.trim().length > 0) response.code = code.trim()
      if (note.trim().length > 0) response.note = note.trim()
      await onResolve(current.id, response)
    } catch (cause) {
      setError(cause)
    } finally {
      setBusy(null)
    }
  }

  async function cancel(): Promise<void> {
    if (current === undefined || disabled) return
    setBusy("cancel")
    setError(null)
    try {
      await onCancel(current.id)
    } catch (cause) {
      setError(cause)
    } finally {
      setBusy(null)
    }
  }

  return (
    <Alert tone="warning" title="需要人工处理：执行已暂停" className="space-y-2">
      <div data-testid="human-request-banner" className="space-y-2">
        <div className="flex flex-wrap items-center gap-2">
          <Badge tone="warning">{HUMAN_REQUEST_KIND_LABEL[current.kind]}</Badge>
          {current.payload.version_sut_id !== undefined &&
          current.payload.version_sut_id !== null ? (
            <span className="text-muted-foreground">系统 {current.payload.version_sut_id}</span>
          ) : null}
          {current.payload.account_role !== undefined && current.payload.account_role !== "" ? (
            <span className="text-muted-foreground">账号角色 {current.payload.account_role}</span>
          ) : null}
          {current.payload.challenge_kind !== undefined && current.payload.challenge_kind !== "" ? (
            <span className="text-muted-foreground">挑战 {current.payload.challenge_kind}</span>
          ) : null}
          {expired ? (
            <Badge tone="danger">已过期</Badge>
          ) : (
            <span className="text-muted-foreground">剩余 {formatDuration(remaining)}</span>
          )}
        </div>

        <p className="text-xs text-foreground">{current.payload.reason}</p>

        {current.payload.page_url !== undefined && current.payload.page_url !== "" ? (
          <a
            href={current.payload.page_url}
            target="_blank"
            rel="noreferrer"
            className="break-all text-[11px] text-primary underline"
          >
            {current.payload.page_url}
          </a>
        ) : null}

        <div className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,1.5fr)]">
          <Field id="human-request-code" label="一次性验证码">
            <Input
              id="human-request-code"
              value={code}
              disabled={disabled}
              autoComplete="one-time-code"
              placeholder={expired ? "请求已过期" : "例如：246810"}
              onChange={(event) => setCode(event.target.value)}
            />
          </Field>
          <Field id="human-request-note" label="备注（可选）">
            <Textarea
              id="human-request-note"
              value={note}
              disabled={disabled}
              rows={2}
              placeholder="输入处理结果或说明"
              onChange={(event) => setNote(event.target.value)}
            />
          </Field>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <Button
            size="sm"
            disabled={disabled}
            onClick={() => {
              void resolve()
            }}
          >
            {busy === "resolve" ? "提交中…" : "完成"}
          </Button>
          <Button
            size="sm"
            variant="secondary"
            disabled={disabled}
            onClick={() => {
              void cancel()
            }}
          >
            {busy === "cancel" ? "取消中…" : "取消请求"}
          </Button>
          {takeovers.length > 1 ? (
            <span className="text-[11px] text-muted-foreground">
              另有 {takeovers.length - 1} 条待处理
            </span>
          ) : null}
        </div>

        {error !== null ? <InlineError error={error} /> : null}
      </div>
    </Alert>
  )
}
