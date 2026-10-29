"use client"

import { useState } from "react"

import { StatusBadge, type StatusBadgeVariant } from "@/components/ui/StatusBadge"
import { ApiError } from "@/lib/api-client"
import {
  cancelTakeover,
  confirmSession,
  invalidateSession,
  revokeSession,
  validateSession,
} from "./api"
import { TakeoverDialog } from "./TakeoverDialog"
import type { SessionDTO, SnapshotDTO } from "./types"

const STATE_VARIANTS: Record<string, StatusBadgeVariant> = {
  valid: "success",
  needs_manual: "warning",
  pending: "info",
  expired: "danger",
  invalid: "danger",
  revoked: "neutral",
  failed: "danger",
}

const TAKEOVER_LABELS: Record<string, string> = {
  waiting: "等待人工处理",
  completed: "已完成",
  cancelled: "已取消",
  expired: "已过期",
}

const TAKEOVER_VARIANTS: Record<string, StatusBadgeVariant> = {
  waiting: "warning",
  completed: "success",
  cancelled: "neutral",
  expired: "danger",
}

const DEFAULT_INVALIDATE_REASON = "管理员手动失效"
const DEFAULT_REVOKE_REASON = "管理员手动撤销"

const ERROR_REASON_LABELS: Record<string, string> = {
  session_state_conflict: "当前状态不允许该操作",
  takeover_not_waiting: "没有等待中的接管",
  invalid_storage_state: "storage_state 结构不合法",
  storage_state_expired: "storage_state 已过期",
  invalid_argument: "参数不合法",
}

function describeError(error: unknown): string {
  if (!(error instanceof ApiError)) {
    return error instanceof Error ? error.message : "未知错误"
  }
  if (error.status === 0) return "无法连接 API 服务，请确认后端已启动。"
  if (error.status === 403) return "当前账户没有管理员权限。"
  const reason = typeof error.details.reason === "string" ? error.details.reason : ""
  const field = typeof error.details.field === "string" ? error.details.field : ""
  const location = reason === "" ? "" : field === "" ? `（${reason}）` : `（${reason} / ${field}）`
  const label = reason === "" ? "" : ERROR_REASON_LABELS[reason]
  if (label) return `${label}${location}：${error.message}`
  if (reason !== "") return `${error.message}${location}`
  return error.message
}

function formatDateTime(value: string | null): string {
  if (value === null || value === "") return "—"
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString("zh-CN", { hour12: false })
}

type SnapshotSummary = {
  total: number | null
  allowed: number | null
  denied: number | null
  error: number | null
  degraded: boolean
}

/** `summary` is `unknown` in the DTO; read the counters spec §6.3 defines. */
function readSummary(summary: unknown): SnapshotSummary {
  const result: SnapshotSummary = {
    total: null,
    allowed: null,
    denied: null,
    error: null,
    degraded: false,
  }
  if (typeof summary !== "object" || summary === null) return result
  const record = summary as Record<string, unknown>
  const read = (key: string): number | null =>
    typeof record[key] === "number" ? (record[key] as number) : null
  result.total = read("total")
  result.allowed = read("allowed")
  result.denied = read("denied")
  result.error = read("error")
  result.degraded = record.degraded === true
  return result
}

function summaryText(summary: unknown): string {
  const parsed = readSummary(summary)
  if (
    parsed.total === null &&
    parsed.allowed === null &&
    parsed.denied === null &&
    parsed.error === null
  ) {
    return "—"
  }
  return `允许 ${parsed.allowed ?? 0} / 拒绝 ${parsed.denied ?? 0} / 错误 ${parsed.error ?? 0}`
}

function isDegraded(snapshot: SnapshotDTO): boolean {
  return snapshot.degraded || readSummary(snapshot.summary).degraded
}

type LoginObservation = { observed: string; status: number | null }

/** Snapshot `items` is `unknown`; read the `login.observed` value of spec §6.3. */
function readLoginObservation(items: unknown): LoginObservation | null {
  if (typeof items !== "object" || items === null) return null
  const login = (items as Record<string, unknown>).login
  if (typeof login !== "object" || login === null) return null
  const record = login as Record<string, unknown>
  const observed = typeof record.observed === "string" ? record.observed : ""
  if (observed === "") return null
  const status = typeof record.status === "number" ? record.status : null
  return { observed, status }
}

const labelClass = "text-[11px] text-muted-foreground"
const valueClass = "text-sm"
const buttonClass =
  "inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-2.5 py-1.5 text-xs font-medium transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
const dangerButtonClass =
  "inline-flex items-center gap-1.5 rounded-lg border border-destructive/40 bg-card px-2.5 py-1.5 text-xs font-medium text-destructive transition-colors hover:bg-destructive/10 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
const inputClass =
  "mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"

export type SessionDetailProps = {
  session: SessionDTO
  accountName: string
  systemName: string
  environmentName: string
  canWrite: boolean
  onChanged: () => void | Promise<void>
}

/**
 * Detail of one session: metadata, latest snapshot summary, active lease,
 * latest takeover and the admin actions of spec §7.5/§7.6. Buttons stay in the
 * DOM but disable themselves when the session state machine (spec §6.1) does
 * not allow the action; actions re-read the parent via `onChanged`.
 */
export function SessionDetail({
  session,
  accountName,
  systemName,
  environmentName,
  canWrite,
  onChanged,
}: SessionDetailProps) {
  const [busy, setBusy] = useState("")
  const [error, setError] = useState("")
  const [notice, setNotice] = useState("")
  const [reason, setReason] = useState("")
  const [dialog, setDialog] = useState<"request" | "complete" | null>(null)

  const state = session.state
  const snapshot = session.latestSnapshot ?? null
  const loginCheck = snapshot === null ? null : readLoginObservation(snapshot.items)
  const takeover = session.latestTakeover ?? null
  const waitingTakeover = takeover !== null && takeover.status === "waiting"

  const canValidate = ["pending", "needs_manual", "valid", "expired", "invalid"].includes(state)
  const canConfirm = state === "pending" || state === "needs_manual"
  const canInvalidate = ["valid", "needs_manual", "expired"].includes(state)
  const canRevoke = state !== "revoked"
  const canRequestTakeover = state !== "revoked" && !waitingTakeover

  async function runAction(
    name: string,
    task: () => Promise<SessionDTO>,
    describeResult: (updated: SessionDTO) => string,
  ) {
    setBusy(name)
    setError("")
    setNotice("")
    try {
      const updated = await task()
      setNotice(describeResult(updated))
      await onChanged()
    } catch (caught) {
      setError(describeError(caught))
    } finally {
      setBusy("")
    }
  }

  function handleValidate() {
    void runAction(
      "validate",
      () => validateSession(session.id),
      (updated) => `校验完成：状态 ${updated.state}。`,
    )
  }

  function handleConfirm() {
    void runAction(
      "confirm",
      () => confirmSession(session.id, "人工确认"),
      (updated) => `已确认会话：状态 ${updated.state}。`,
    )
  }

  function handleInvalidate() {
    const cause = reason.trim() === "" ? DEFAULT_INVALIDATE_REASON : reason.trim()
    void runAction(
      "invalidate",
      () => invalidateSession(session.id, cause),
      (updated) => `已失效会话：状态 ${updated.state}。`,
    )
  }

  function handleRevoke() {
    const cause = reason.trim() === "" ? DEFAULT_REVOKE_REASON : reason.trim()
    void runAction(
      "revoke",
      () => revokeSession(session.id, cause),
      (updated) => `已撤销会话：状态 ${updated.state}。`,
    )
  }

  async function handleCancelTakeover() {
    if (takeover === null) return
    setBusy("cancel-takeover")
    setError("")
    setNotice("")
    try {
      await cancelTakeover(takeover.id)
      setNotice("已取消接管。")
      await onChanged()
    } catch (caught) {
      setError(describeError(caught))
    } finally {
      setBusy("")
    }
  }

  async function handleRequested() {
    setDialog(null)
    setNotice("已发起接管，等待人工完成。")
    await onChanged()
  }

  async function handleCompleted() {
    setDialog(null)
    setNotice("已完成接管，系统已重新校验会话。")
    await onChanged()
  }

  return (
    <section
      aria-label="会话详情"
      className="space-y-3 rounded-2xl border border-border bg-card p-3 shadow-sm"
    >
      <header className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <h3 className="text-base font-semibold">会话 #{session.id}</h3>
          <p className="mt-1 text-xs text-muted-foreground">
            {accountName} · {systemName} · {environmentName}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-1.5">
          <StatusBadge variant={STATE_VARIANTS[state] ?? "neutral"}>{state}</StatusBadge>
          {session.expiring ? <StatusBadge variant="warning">即将过期</StatusBadge> : null}
          {snapshot !== null && isDegraded(snapshot) ? (
            <StatusBadge variant="warning">能力未验证</StatusBadge>
          ) : null}
        </div>
      </header>

      {error ? (
        <p
          role="alert"
          className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
        >
          {error}
        </p>
      ) : null}
      {notice ? (
        <p
          role="status"
          className="rounded-xl border border-emerald-500/25 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-700 dark:text-emerald-300"
        >
          {notice}
        </p>
      ) : null}

      {session.failureReason !== "" ? (
        <p className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
          失败原因：{session.failureReason}
        </p>
      ) : null}
      {session.invalidatedReason !== "" ? (
        <p className="rounded-xl border border-amber-500/25 bg-amber-500/10 px-3 py-2 text-sm text-amber-700 dark:text-amber-300">
          失效/撤销原因：{session.invalidatedReason}
        </p>
      ) : null}

      <dl className="grid gap-2 rounded-xl border border-border bg-muted/20 p-2.5 text-xs sm:grid-cols-3">
        <div>
          <dt className={labelClass}>账号</dt>
          <dd className={valueClass}>{accountName}</dd>
        </div>
        <div>
          <dt className={labelClass}>系统</dt>
          <dd className={valueClass}>{systemName}</dd>
        </div>
        <div>
          <dt className={labelClass}>环境</dt>
          <dd className={valueClass}>{environmentName}</dd>
        </div>
        <div>
          <dt className={labelClass}>获取方式</dt>
          <dd className={valueClass}>{session.acquisition}</dd>
        </div>
        <div>
          <dt className={labelClass}>存储类型</dt>
          <dd className={valueClass}>{session.storageStateKind}</dd>
        </div>
        <div>
          <dt className={labelClass}>Cookie / Origin</dt>
          <dd className={valueClass}>
            {session.cookieCount} / {session.originCount}
          </dd>
        </div>
        <div>
          <dt className={labelClass}>有效期</dt>
          <dd className={valueClass}>{formatDateTime(session.expiresAt)}</dd>
        </div>
        <div>
          <dt className={labelClass}>最后校验</dt>
          <dd className={valueClass}>{formatDateTime(session.lastValidatedAt)}</dd>
        </div>
        <div>
          <dt className={labelClass}>状态哈希</dt>
          <dd className={`${valueClass} break-all font-mono text-[11px] text-muted-foreground`}>
            {session.storageStateHash === "" ? "—" : session.storageStateHash}
          </dd>
        </div>
      </dl>

      <div className="grid gap-2 lg:grid-cols-2">
        <section
          aria-labelledby="session-snapshot-title"
          className="rounded-xl border border-border bg-muted/20 p-2.5"
        >
          <div className="flex flex-wrap items-center justify-between gap-1.5">
            <h4 id="session-snapshot-title" className="text-sm font-semibold">
              最新快照
            </h4>
            {snapshot !== null ? (
              <span className="text-[11px] text-muted-foreground">
                {formatDateTime(snapshot.capturedAt)}
              </span>
            ) : null}
          </div>
          {snapshot === null ? (
            <p className="mt-1 text-xs text-muted-foreground">暂无快照。</p>
          ) : (
            <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs">
              <span className="text-muted-foreground">类型</span>
              <span>{snapshot.kind}</span>
              <span className="text-muted-foreground">摘要</span>
              <span>{summaryText(snapshot.summary)}</span>
              {loginCheck !== null ? (
                <>
                  <span className="text-muted-foreground">登录检查</span>
                  <span>{loginCheck.observed}</span>
                  {loginCheck.status !== null ? (
                    <span className="font-mono">{loginCheck.status}</span>
                  ) : null}
                </>
              ) : null}
              <span className="break-all text-muted-foreground">
                权限哈希 {snapshot.permissionHash === "" ? "—" : snapshot.permissionHash}
              </span>
            </div>
          )}
        </section>

        <section
          aria-labelledby="session-lease-title"
          className="rounded-xl border border-border bg-muted/20 p-2.5"
        >
          <h4 id="session-lease-title" className="text-sm font-semibold">
            活跃租约
          </h4>
          {session.activeLease === null || session.activeLease === undefined ? (
            <p className="mt-1 text-xs text-muted-foreground">无活跃租约。</p>
          ) : (
            <p className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs">
              <span className="text-muted-foreground">持有者</span>
              <span className="font-mono">{session.activeLease.holder}</span>
              <span className="text-muted-foreground">到期</span>
              <span>{formatDateTime(session.activeLease.expiresAt)}</span>
            </p>
          )}
        </section>
      </div>

      <section
        aria-labelledby="session-takeover-title"
        className="space-y-2 rounded-xl border border-border bg-muted/20 p-2.5"
      >
        <h4 id="session-takeover-title" className="text-sm font-semibold">
          人工接管
        </h4>
        {takeover === null ? (
          <p className="text-xs text-muted-foreground">暂无接管记录。</p>
        ) : (
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs">
            <StatusBadge variant={TAKEOVER_VARIANTS[takeover.status] ?? "neutral"}>
              {takeover.status}
            </StatusBadge>
            <span>{TAKEOVER_LABELS[takeover.status] ?? takeover.status}</span>
            {takeover.reason !== "" ? (
              <>
                <span className="text-muted-foreground">原因</span>
                <span>{takeover.reason}</span>
              </>
            ) : null}
          </div>
        )}
        {canWrite && waitingTakeover ? (
          <div className="flex flex-wrap gap-1.5">
            <button
              type="button"
              onClick={() => setDialog("complete")}
              disabled={busy !== ""}
              className={buttonClass}
            >
              完成接管
            </button>
            <button
              type="button"
              onClick={() => void handleCancelTakeover()}
              disabled={busy !== ""}
              className={dangerButtonClass}
            >
              取消接管
            </button>
          </div>
        ) : null}
      </section>

      {canWrite ? (
        <section
          aria-labelledby="session-actions-title"
          className="space-y-2 rounded-xl border border-border p-2.5"
        >
          <h4 id="session-actions-title" className="text-sm font-semibold">
            操作
          </h4>
          <div>
            <label htmlFor="session-action-reason" className={labelClass}>
              操作原因
            </label>
            <input
              id="session-action-reason"
              value={reason}
              onChange={(event) => setReason(event.target.value)}
              autoComplete="off"
              placeholder="失效/撤销时使用；留空则使用默认原因"
              className={inputClass}
            />
          </div>
          <div className="flex flex-wrap gap-1.5">
            <button
              type="button"
              onClick={handleValidate}
              disabled={!canValidate || busy !== ""}
              className={buttonClass}
            >
              {busy === "validate" ? "校验中…" : "校验"}
            </button>
            <button
              type="button"
              onClick={handleConfirm}
              disabled={!canConfirm || busy !== ""}
              className={buttonClass}
            >
              {busy === "confirm" ? "确认中…" : "确认"}
            </button>
            <button
              type="button"
              onClick={handleInvalidate}
              disabled={!canInvalidate || busy !== ""}
              className={dangerButtonClass}
            >
              {busy === "invalidate" ? "失效中…" : "失效"}
            </button>
            <button
              type="button"
              onClick={handleRevoke}
              disabled={!canRevoke || busy !== ""}
              className={dangerButtonClass}
            >
              {busy === "revoke" ? "撤销中…" : "撤销"}
            </button>
            <button
              type="button"
              onClick={() => setDialog("request")}
              disabled={!canRequestTakeover || busy !== ""}
              className={buttonClass}
            >
              发起接管
            </button>
          </div>
        </section>
      ) : null}

      {dialog === "request" ? (
        <TakeoverDialog
          mode="request"
          sessionId={session.id}
          onClose={() => setDialog(null)}
          onDone={handleRequested}
        />
      ) : null}
      {dialog === "complete" && takeover !== null ? (
        <TakeoverDialog
          mode="complete"
          takeoverId={takeover.id}
          defaultKind={session.storageStateKind === "token" ? "token" : "storage_state"}
          onClose={() => setDialog(null)}
          onDone={handleCompleted}
        />
      ) : null}
    </section>
  )
}
