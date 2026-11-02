"use client"

import { useEffect, useState } from "react"

import { Alert } from "@/components/ui/Alert"
import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { InlineError } from "@/components/ui/ErrorState"
import { Field } from "@/components/ui/Field"
import { Input } from "@/components/ui/Input"
import { Textarea } from "@/components/ui/Textarea"
import { formatDuration } from "@/lib/format"

import { HUMAN_REQUEST_KIND_LABEL } from "./labels"
import type { HumanRequest, HumanRequestResponseInput } from "./types"

export type HumanRequestBannerProps = {
  /** Open human requests for the version; only the oldest is actionable here. */
  requests: HumanRequest[]
  onResolve: (id: string, response: HumanRequestResponseInput) => Promise<void>
  onCancel: (id: string) => Promise<void>
  /** Fixed clock for tests/countdown determinism; defaults to the wall clock. */
  now?: number
}

/**
 * Login-takeover banner (spec §8.5-7, §12.1): when an Agent needs a 2FA code it
 * pauses the wave and the user resolves the request here. The banner is
 * controlled by the parent list, so a successful resolve removes it as soon as
 * the execution snapshot refreshes.
 */
export function HumanRequestBanner({
  requests,
  onResolve,
  onCancel,
  now,
}: HumanRequestBannerProps) {
  const open = requests.filter((request) => request.status === "open")
  const current = open[0]
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
          <span className="text-muted-foreground">账号角色 {current.payload.account_role}</span>
          <span className="text-muted-foreground">挑战 {current.payload.challenge_kind}</span>
          {expired ? (
            <Badge tone="danger">已过期</Badge>
          ) : (
            <span className="text-muted-foreground">剩余 {formatDuration(remaining)}</span>
          )}
        </div>

        <p className="text-xs text-foreground">{current.payload.reason}</p>

        <a
          href={current.payload.page_url}
          target="_blank"
          rel="noreferrer"
          className="break-all text-[11px] text-primary underline"
        >
          {current.payload.page_url}
        </a>

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
          {open.length > 1 ? (
            <span className="text-[11px] text-muted-foreground">
              另有 {open.length - 1} 条待处理
            </span>
          ) : null}
        </div>

        {error !== null ? <InlineError error={error} /> : null}
      </div>
    </Alert>
  )
}
