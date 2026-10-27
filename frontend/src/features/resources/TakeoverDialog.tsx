"use client"

import { type ChangeEvent, type FormEvent, useEffect, useRef, useState } from "react"

import { ApiError } from "@/lib/api-client"

import { completeTakeover, requestTakeover } from "./api"

/** Takeover upload cap mirrors the import cap of spec §6.2/§6.4. */
const MAX_IMPORT_BYTES = 5 * 1024 * 1024
const DEFAULT_EXPIRES_SECONDS = 1800

const KIND_OPTIONS = [
  { value: "storage_state", label: "storage_state（浏览器状态）" },
  { value: "token", label: "token（请求头）" },
] as const

type ImportKind = (typeof KIND_OPTIONS)[number]["value"]

/** Payload accepted by both `importSession` and `completeTakeover`. */
type SessionStatePayload =
  | { kind: "storage_state"; storageState: { cookies: unknown[]; origins: unknown[] } }
  | { kind: "token"; headers: Record<string, string> }

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

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

function byteLength(text: string): number {
  return new TextEncoder().encode(text).byteLength
}

type ParsedState = { payload: SessionStatePayload | null; error: string }

/** Parses a storage_state or token document, matching the import request shape. */
function parseState(raw: string, kind: ImportKind): ParsedState {
  let parsed: unknown
  try {
    parsed = JSON.parse(raw)
  } catch {
    return { payload: null, error: "JSON 解析失败，请检查语法。" }
  }
  if (!isRecord(parsed)) return { payload: null, error: "JSON 必须是对象。" }

  if (kind === "token") {
    const headers = parsed.headers
    if (!isRecord(headers)) return { payload: null, error: "token 导入需要 headers 对象。" }
    const result: Record<string, string> = {}
    for (const [name, value] of Object.entries(headers)) {
      if (typeof value !== "string") {
        return { payload: null, error: `请求头 ${name} 的值必须是字符串。` }
      }
      result[name] = value
    }
    if (Object.keys(result).length === 0) {
      return { payload: null, error: "headers 至少需要一个请求头。" }
    }
    return { payload: { kind: "token", headers: result }, error: "" }
  }

  const cookies = parsed.cookies
  const origins = parsed.origins
  if (!Array.isArray(cookies) || !Array.isArray(origins)) {
    return { payload: null, error: "storage_state 必须包含 cookies 与 origins 数组。" }
  }
  return { payload: { kind: "storage_state", storageState: { cookies, origins } }, error: "" }
}

const inputClass =
  "mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-60"
const labelClass = "text-xs font-medium text-muted-foreground"
const primaryButtonClass =
  "inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
const secondaryButtonClass =
  "inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"

type TakeoverDialogBase = {
  onClose: () => void
  onDone: () => void | Promise<void>
}

export type TakeoverDialogProps =
  | (TakeoverDialogBase & { mode: "request"; sessionId: number })
  | (TakeoverDialogBase & {
      mode: "complete"
      takeoverId: number
      defaultKind?: ImportKind
    })

/**
 * Human-takeover dialog for one session. `request` mode creates a waiting
 * takeover (reason/instructions/expiry); `complete` mode uploads the new
 * storage state or token headers that replace the stale session state
 * (spec §6.4/§7.6). Both modes keep the backend's 422/409 `details.reason`
 * visible instead of hiding the failure.
 */
export function TakeoverDialog(props: TakeoverDialogProps) {
  const { mode, onClose, onDone } = props
  const [reason, setReason] = useState("")
  const [instructions, setInstructions] = useState("")
  const [expiresSeconds, setExpiresSeconds] = useState(String(DEFAULT_EXPIRES_SECONDS))
  const [kind, setKind] = useState<ImportKind>(
    mode === "complete" ? (props.defaultKind ?? "storage_state") : "storage_state",
  )
  const [json, setJson] = useState("")
  const [error, setError] = useState("")
  const [saving, setSaving] = useState(false)
  const reasonRef = useRef<HTMLTextAreaElement>(null)
  const jsonRef = useRef<HTMLTextAreaElement>(null)

  useEffect(() => {
    if (mode === "request") reasonRef.current?.focus()
    else jsonRef.current?.focus()
  }, [mode])

  async function handleFileChange(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0]
    if (!file) return
    setError("")
    if (file.size > MAX_IMPORT_BYTES) {
      setError("JSON 文件不能超过 5 MiB。")
      return
    }
    try {
      setJson(await file.text())
    } catch {
      setError("读取文件失败，请重试或直接粘贴 JSON。")
    }
  }

  async function submitRequest(sessionId: number) {
    const trimmedReason = reason.trim()
    if (trimmedReason === "") {
      setError("请输入接管原因。")
      return
    }
    let expiresInSeconds: number | undefined
    const rawExpires = expiresSeconds.trim()
    if (rawExpires !== "") {
      const parsed = Number(rawExpires)
      if (!Number.isInteger(parsed) || parsed < 1) {
        setError("有效期需为大于 0 的整数秒。")
        return
      }
      expiresInSeconds = parsed
    }

    const payload: {
      reason: string
      instructions?: { text: string }
      expiresInSeconds?: number
    } = { reason: trimmedReason }
    const trimmedInstructions = instructions.trim()
    if (trimmedInstructions !== "") payload.instructions = { text: trimmedInstructions }
    if (expiresInSeconds !== undefined) payload.expiresInSeconds = expiresInSeconds

    setSaving(true)
    setError("")
    try {
      await requestTakeover(sessionId, payload)
      await onDone()
    } catch (caught) {
      setError(describeError(caught))
    } finally {
      setSaving(false)
    }
  }

  async function submitComplete(takeoverId: number) {
    const raw = json.trim()
    if (raw === "") {
      setError("请粘贴 JSON 或选择 JSON 文件。")
      return
    }
    if (byteLength(raw) > MAX_IMPORT_BYTES) {
      setError("JSON 内容不能超过 5 MiB。")
      return
    }
    const parsed = parseState(raw, kind)
    if (parsed.payload === null) {
      setError(parsed.error)
      return
    }

    setSaving(true)
    setError("")
    try {
      await completeTakeover(takeoverId, parsed.payload)
      await onDone()
    } catch (caught) {
      setError(describeError(caught))
    } finally {
      setSaving(false)
    }
  }

  function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (props.mode === "request") void submitRequest(props.sessionId)
    else void submitComplete(props.takeoverId)
  }

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-black/40 p-4 sm:items-center">
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="takeover-dialog-title"
        onKeyDown={(event) => {
          if (event.key === "Escape") onClose()
        }}
        className="w-full max-w-2xl space-y-3 rounded-2xl border border-border bg-card p-4 shadow-xl"
      >
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div>
            <h2 id="takeover-dialog-title" className="text-base font-semibold">
              {mode === "request" ? "发起人工接管" : "完成人工接管"}
            </h2>
            <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
              {mode === "request"
                ? "创建一条等待人工处理的接管请求；会话将进入 needs_manual。"
                : "上传人工获取的新状态；系统会替换当前状态并重新执行 login_check。"}
            </p>
          </div>
          <button type="button" onClick={onClose} className={secondaryButtonClass}>
            关闭
          </button>
        </div>

        {error ? (
          <p
            role="alert"
            className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
          >
            {error}
          </p>
        ) : null}

        <form onSubmit={handleSubmit} noValidate className="space-y-3">
          {mode === "request" ? (
            <>
              <div>
                <label htmlFor="takeover-reason" className={labelClass}>
                  接管原因
                </label>
                <textarea
                  id="takeover-reason"
                  ref={reasonRef}
                  value={reason}
                  onChange={(event) => setReason(event.target.value)}
                  rows={2}
                  placeholder="例如 需要完成短信验证码"
                  className={inputClass}
                />
              </div>
              <div>
                <label htmlFor="takeover-instructions" className={labelClass}>
                  操作指引
                </label>
                <textarea
                  id="takeover-instructions"
                  value={instructions}
                  onChange={(event) => setInstructions(event.target.value)}
                  rows={2}
                  placeholder="例如 请在 5 分钟内完成验证码"
                  className={inputClass}
                />
              </div>
              <div>
                <label htmlFor="takeover-expires" className={labelClass}>
                  有效期（秒）
                </label>
                <input
                  id="takeover-expires"
                  type="number"
                  min={1}
                  value={expiresSeconds}
                  onChange={(event) => setExpiresSeconds(event.target.value)}
                  className={inputClass}
                />
              </div>
            </>
          ) : (
            <>
              <div>
                <label htmlFor="takeover-kind" className={labelClass}>
                  导入类型
                </label>
                <select
                  id="takeover-kind"
                  value={kind}
                  onChange={(event) => setKind(event.target.value as ImportKind)}
                  className={inputClass}
                >
                  {KIND_OPTIONS.map((option) => (
                    <option key={option.value} value={option.value}>
                      {option.label}
                    </option>
                  ))}
                </select>
              </div>
              <div>
                <label htmlFor="takeover-json" className={labelClass}>
                  JSON 内容
                </label>
                <textarea
                  id="takeover-json"
                  ref={jsonRef}
                  value={json}
                  onChange={(event) => setJson(event.target.value)}
                  rows={10}
                  spellCheck={false}
                  placeholder={
                    kind === "token"
                      ? '{"headers":{"Authorization":"Bearer ..."}}'
                      : '{"cookies":[...],"origins":[...]}'
                  }
                  className={inputClass}
                />
              </div>
              <div>
                <label htmlFor="takeover-file" className={labelClass}>
                  JSON 文件
                </label>
                <input
                  id="takeover-file"
                  type="file"
                  accept=".json,application/json"
                  onChange={(event) => void handleFileChange(event)}
                  className="mt-1 block w-full text-xs text-muted-foreground file:mr-2 file:rounded-lg file:border file:border-border file:bg-card file:px-2.5 file:py-1.5 file:text-xs file:font-medium file:text-foreground"
                />
              </div>
            </>
          )}

          <div className="flex justify-end gap-1.5">
            <button
              type="button"
              onClick={onClose}
              disabled={saving}
              className={secondaryButtonClass}
            >
              取消
            </button>
            <button type="submit" disabled={saving} className={primaryButtonClass}>
              {saving ? "提交中…" : mode === "request" ? "确认发起" : "上传并完成"}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
