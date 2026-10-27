"use client"

import {
  type ChangeEvent,
  type FormEvent,
  type KeyboardEvent,
  useEffect,
  useRef,
  useState,
} from "react"

import { ApiError } from "@/lib/api-client"

import { importSession, listAccesses, listAccounts, listEnvironments, listSystems } from "./api"
import type {
  AccessDTO,
  AccountDTO,
  EnvironmentDTO,
  SessionDTO,
  SnapshotDTO,
  SystemDTO,
} from "./types"

/** Import payload cap: 5 MiB of JSON (spec §6.2). */
const MAX_IMPORT_BYTES = 5 * 1024 * 1024
const PAGE_LIMIT = 100

const KIND_OPTIONS = [
  { value: "storage_state", label: "storage_state（浏览器状态）" },
  { value: "token", label: "token（请求头）" },
] as const

type ImportKind = (typeof KIND_OPTIONS)[number]["value"]

type SessionStatePayload =
  | { kind: "storage_state"; storageState: { cookies: unknown[]; origins: unknown[] } }
  | { kind: "token"; headers: Record<string, string> }

const ERROR_REASON_LABELS: Record<string, string> = {
  invalid_storage_state: "storage_state 结构不合法",
  storage_state_expired: "storage_state 已过期",
  invalid_argument: "参数不合法",
  session_state_conflict: "当前状态不允许该操作",
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
  if (label) return `导入失败：${label}${location}：${error.message}`
  if (reason !== "") return `导入失败${location}：${error.message}`
  return error.message
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

function byteLength(text: string): number {
  return new TextEncoder().encode(text).byteLength
}

type ParsedState = { payload: SessionStatePayload | null; error: string }

/** Parses a storage_state or token document into the import request shape. */
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

type SnapshotSummary = {
  total: number | null
  allowed: number | null
  denied: number | null
  error: number | null
  degraded: boolean
}

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

/** Reads `items.login.observed` from a login-check snapshot, when present. */
function observedLogin(items: unknown): string {
  if (typeof items !== "object" || items === null) return ""
  const login = (items as Record<string, unknown>).login
  if (typeof login !== "object" || login === null) return ""
  const observed = (login as Record<string, unknown>).observed
  return typeof observed === "string" ? observed : ""
}

/**
 * Import outcome line (spec §6.3). A non-degraded snapshot is only a pass when
 * the session is valid and nothing was denied or errored: a denied/errored
 * login_check lands in needs_manual, so it must not be reported as a pass.
 */
function describeCheckOutcome(session: SessionDTO, snapshot: SnapshotDTO): string {
  const summary = readSummary(snapshot.summary)
  if (snapshot.degraded || summary.degraded) {
    return "能力未验证（degraded），请人工确认或重新校验。"
  }
  const observed = observedLogin(snapshot.items)
  if ((summary.denied ?? 0) > 0 || observed === "denied") {
    return "能力校验未通过：登录检查被拒绝，请人工确认或重新校验。"
  }
  if ((summary.error ?? 0) > 0 || observed === "error") {
    return "能力校验未通过：登录检查执行出错，请人工确认或重新校验。"
  }
  if (session.state === "valid" && ((summary.allowed ?? 0) > 0 || observed === "allowed")) {
    return "能力校验通过。"
  }
  return "能力校验未通过，需人工确认。"
}

/** Import result line: state, snapshot counters and the degraded flag (spec §6.3). */
function describeResult(session: SessionDTO): string {
  const parts = [`已导入会话 #${session.id}：状态 ${session.state}。`]
  const snapshot = session.latestSnapshot ?? null
  if (snapshot === null) {
    parts.push("暂无快照。")
    return parts.join("")
  }
  parts.push(describeCheckOutcome(session, snapshot))
  parts.push(`快照：${summaryText(snapshot.summary)}。`)
  return parts.join("")
}

const inputClass =
  "mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-60"
const labelClass = "text-xs font-medium text-muted-foreground"
const primaryButtonClass =
  "inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
const secondaryButtonClass =
  "inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"

/** Tab order inside the modal dialog; disabled controls are skipped. */
const FOCUSABLE_SELECTOR = [
  "a[href]",
  "button:not([disabled])",
  "input:not([disabled])",
  "select:not([disabled])",
  "textarea:not([disabled])",
  "[tabindex]:not([tabindex='-1'])",
].join(", ")

export type ImportSessionDialogProps = {
  onClose: () => void
  onImported: (session: SessionDTO) => void | Promise<void>
}

/**
 * Session import dialog: account → Access → environment → storage_state/token
 * document. The environment list follows the selected Access's system, file and
 * pasted input share the 5 MiB cap, and the result line shows the new state plus
 * the snapshot's degraded flag (spec §6.2/§7.5).
 */
export function ImportSessionDialog({ onClose, onImported }: ImportSessionDialogProps) {
  const [accounts, setAccounts] = useState<AccountDTO[]>([])
  const [systems, setSystems] = useState<SystemDTO[]>([])
  const [accesses, setAccesses] = useState<AccessDTO[]>([])
  const [environments, setEnvironments] = useState<EnvironmentDTO[]>([])
  const [accountId, setAccountId] = useState("")
  const [accessId, setAccessId] = useState("")
  const [environmentId, setEnvironmentId] = useState("")
  const [kind, setKind] = useState<ImportKind>("storage_state")
  const [json, setJson] = useState("")
  const [error, setError] = useState("")
  const [result, setResult] = useState("")
  const [importing, setImporting] = useState(false)
  const [optionsLoading, setOptionsLoading] = useState(true)
  const [accessLoading, setAccessLoading] = useState(false)
  const [environmentLoading, setEnvironmentLoading] = useState(false)
  const accessRequestRef = useRef(0)
  const environmentRequestRef = useRef(0)
  const dialogRef = useRef<HTMLDivElement>(null)
  const firstFieldRef = useRef<HTMLSelectElement>(null)
  const triggerRef = useRef<HTMLElement | null>(null)

  // Move focus into the modal on open (so `aria-modal` and Escape work) and
  // hand it back to the trigger when the dialog unmounts.
  useEffect(() => {
    const trigger = document.activeElement
    triggerRef.current = trigger instanceof HTMLElement ? trigger : null
    firstFieldRef.current?.focus()
    return () => {
      const previous = triggerRef.current
      if (previous?.isConnected) previous.focus()
    }
  }, [])

  function handleDialogKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    if (event.key === "Escape") {
      onClose()
      return
    }
    if (event.key !== "Tab") return
    const dialog = dialogRef.current
    if (dialog === null) return
    const focusable = Array.from(dialog.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR))
    if (focusable.length === 0) {
      event.preventDefault()
      return
    }
    const first = focusable[0]
    const last = focusable[focusable.length - 1]
    const active = document.activeElement
    const inside = active instanceof HTMLElement && dialog.contains(active)
    if (event.shiftKey) {
      if (!inside || active === first) {
        event.preventDefault()
        last.focus()
      }
    } else if (!inside || active === last) {
      event.preventDefault()
      first.focus()
    }
  }

  useEffect(() => {
    let active = true
    async function loadOptions() {
      try {
        const [accountPage, systemPage] = await Promise.all([
          listAccounts({ limit: PAGE_LIMIT }),
          listSystems({ limit: PAGE_LIMIT }),
        ])
        if (!active) return
        setAccounts(accountPage.items)
        setSystems(systemPage.items)
      } catch (caught) {
        if (active) setError(describeError(caught))
      } finally {
        if (active) setOptionsLoading(false)
      }
    }
    void loadOptions()
    return () => {
      active = false
    }
  }, [])

  function systemName(id: number): string {
    return systems.find((system) => system.id === id)?.name ?? `系统 #${id}`
  }

  async function loadAccesses(selectedAccountId: number) {
    const requestId = accessRequestRef.current + 1
    accessRequestRef.current = requestId
    setAccessLoading(true)
    try {
      const loaded = await listAccesses(selectedAccountId, { limit: PAGE_LIMIT })
      if (accessRequestRef.current !== requestId) return
      setAccesses(loaded)
    } catch (caught) {
      if (accessRequestRef.current !== requestId) return
      setAccesses([])
      setError(describeError(caught))
    } finally {
      if (accessRequestRef.current === requestId) setAccessLoading(false)
    }
  }

  async function loadEnvironments(systemId: number) {
    const requestId = environmentRequestRef.current + 1
    environmentRequestRef.current = requestId
    setEnvironmentLoading(true)
    try {
      const loaded = await listEnvironments(systemId)
      if (environmentRequestRef.current !== requestId) return
      setEnvironments(loaded)
    } catch (caught) {
      if (environmentRequestRef.current !== requestId) return
      setEnvironments([])
      setError(describeError(caught))
    } finally {
      if (environmentRequestRef.current === requestId) setEnvironmentLoading(false)
    }
  }

  function handleAccountChange(raw: string) {
    setAccountId(raw)
    setAccessId("")
    setEnvironmentId("")
    setAccesses([])
    setEnvironments([])
    setError("")
    setResult("")
    if (raw === "") return
    void loadAccesses(Number(raw))
  }

  function handleAccessChange(raw: string) {
    setAccessId(raw)
    setEnvironmentId("")
    setEnvironments([])
    setError("")
    setResult("")
    const access = accesses.find((item) => String(item.id) === raw)
    if (access !== undefined) void loadEnvironments(access.systemId)
  }

  async function handleFileChange(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0]
    if (!file) return
    setError("")
    setResult("")
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

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setError("")
    setResult("")

    if (accounts.length === 0) {
      setError("暂无可选账号，请先在「账号与访问」中创建账号与 Access。")
      return
    }
    if (accountId === "") {
      setError("请选择账号。")
      return
    }
    const access = accesses.find((item) => String(item.id) === accessId)
    if (access === undefined) {
      setError("请选择 Access。")
      return
    }
    if (environmentId === "") {
      setError("请选择环境。")
      return
    }
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

    setImporting(true)
    try {
      const session = await importSession(access.id, {
        environmentId: Number(environmentId),
        ...parsed.payload,
      })
      setResult(describeResult(session))
      await onImported(session)
    } catch (caught) {
      setError(describeError(caught))
    } finally {
      setImporting(false)
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-black/40 p-4 sm:items-center">
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby="session-import-title"
        onKeyDown={handleDialogKeyDown}
        className="w-full max-w-2xl space-y-3 rounded-2xl border border-border bg-card p-4 shadow-xl"
      >
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div>
            <h2 id="session-import-title" className="text-base font-semibold">
              导入会话
            </h2>
            <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
              选择账号、Access 与环境，再导入 storage_state（cookies/origins）或 token
              请求头；导入后立即执行 login_check。单次 5 MiB。
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
        {result ? (
          <p
            role="status"
            className="rounded-xl border border-emerald-500/25 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-700 dark:text-emerald-300"
          >
            {result}
          </p>
        ) : null}

        <form onSubmit={(event) => void handleSubmit(event)} noValidate className="space-y-3">
          <div className="grid gap-2 sm:grid-cols-3">
            <div>
              <label htmlFor="session-import-account" className={labelClass}>
                账号
              </label>
              <select
                id="session-import-account"
                ref={firstFieldRef}
                value={accountId}
                onChange={(event) => handleAccountChange(event.target.value)}
                className={inputClass}
              >
                <option value="">{optionsLoading ? "（加载中…）" : "请选择账号"}</option>
                {accounts.map((account) => (
                  <option key={account.id} value={account.id}>
                    {account.label}（{account.username}）
                  </option>
                ))}
              </select>
            </div>
            <div>
              <label htmlFor="session-import-access" className={labelClass}>
                Access
              </label>
              <select
                id="session-import-access"
                value={accessId}
                onChange={(event) => handleAccessChange(event.target.value)}
                className={inputClass}
              >
                <option value="">
                  {accessLoading
                    ? "（加载中…）"
                    : accounts.length === 0
                      ? "（暂无账号）"
                      : "请选择 Access"}
                </option>
                {accesses.map((access) => (
                  <option key={access.id} value={access.id}>
                    {systemName(access.systemId)} · {access.authType}
                    {access.enabled ? "" : "（停用）"}
                  </option>
                ))}
              </select>
            </div>
            <div>
              <label htmlFor="session-import-environment" className={labelClass}>
                环境
              </label>
              <select
                id="session-import-environment"
                value={environmentId}
                onChange={(event) => setEnvironmentId(event.target.value)}
                className={inputClass}
              >
                <option value="">{environmentLoading ? "（加载中…）" : "请选择环境"}</option>
                {environments.map((environment) => (
                  <option key={environment.id} value={environment.id}>
                    {environment.name}（{environment.baseUrl}）
                  </option>
                ))}
              </select>
            </div>
          </div>

          <div>
            <label htmlFor="session-import-kind" className={labelClass}>
              导入类型
            </label>
            <select
              id="session-import-kind"
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
            <label htmlFor="session-import-json" className={labelClass}>
              JSON 内容
            </label>
            <textarea
              id="session-import-json"
              value={json}
              onChange={(event) => setJson(event.target.value)}
              rows={10}
              spellCheck={false}
              placeholder={
                kind === "token"
                  ? '{"headers":{"Authorization":"Bearer ..."}}'
                  : '{"cookies":[{"name":"sid","value":"..."}],"origins":[]}'
              }
              className={inputClass}
            />
          </div>
          <div>
            <label htmlFor="session-import-file" className={labelClass}>
              JSON 文件
            </label>
            <input
              id="session-import-file"
              type="file"
              accept=".json,application/json"
              onChange={(event) => void handleFileChange(event)}
              className="mt-1 block w-full text-xs text-muted-foreground file:mr-2 file:rounded-lg file:border file:border-border file:bg-card file:px-2.5 file:py-1.5 file:text-xs file:font-medium file:text-foreground"
            />
          </div>

          <div className="flex justify-end gap-1.5">
            <button
              type="button"
              onClick={onClose}
              disabled={importing}
              className={secondaryButtonClass}
            >
              取消
            </button>
            <button type="submit" disabled={importing} className={primaryButtonClass}>
              {importing ? "导入中…" : "导入"}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
