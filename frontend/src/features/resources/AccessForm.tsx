"use client"

import { type FormEvent, useEffect, useState } from "react"

import { ApiError } from "@/lib/api-client"

import type { AccessDTO, SystemDTO } from "./types"

const MIN_CONCURRENCY = 1
const MAX_CONCURRENCY = 100
const MIN_TTL_SECONDS = 60
const MAX_TTL_SECONDS = 604800
const DEFAULT_CONCURRENCY = 1

const AUTH_TYPE_OPTIONS = [
  { value: "password", label: "密码" },
  { value: "sso", label: "SSO" },
  { value: "token", label: "Token" },
  { value: "apikey", label: "API Key" },
  { value: "basic", label: "Basic" },
  { value: "manual", label: "手工" },
]

/** Credential is never part of a response DTO; the UI only tracks `credentialSet`. */
export type AccessWriteInput = {
  systemId: number
  authType: string
  usernameOverride: string
  loginUrl: string
  enabled: boolean
  concurrencyLimit: number
  sessionTtlSeconds: number | null
  permissionNote: string
  credential?: string
}

type AccessFormState = {
  systemId: string
  authType: string
  usernameOverride: string
  loginUrl: string
  enabled: boolean
  concurrencyLimit: string
  sessionTtlSeconds: string
  permissionNote: string
  credential: string
  clearCredential: boolean
}

function toFormState(access: AccessDTO | undefined, systems: SystemDTO[]): AccessFormState {
  if (!access) {
    return {
      systemId: systems[0] === undefined ? "" : String(systems[0].id),
      authType: "password",
      usernameOverride: "",
      loginUrl: "",
      enabled: true,
      concurrencyLimit: String(DEFAULT_CONCURRENCY),
      sessionTtlSeconds: "",
      permissionNote: "",
      credential: "",
      clearCredential: false,
    }
  }
  return {
    systemId: String(access.systemId),
    authType: access.authType,
    usernameOverride: access.usernameOverride,
    loginUrl: access.loginUrl,
    enabled: access.enabled,
    concurrencyLimit: String(access.concurrencyLimit),
    sessionTtlSeconds: access.sessionTtlSeconds === null ? "" : String(access.sessionTtlSeconds),
    permissionNote: access.permissionNote,
    credential: "",
    clearCredential: false,
  }
}

function describeError(error: unknown): string {
  if (!(error instanceof ApiError)) {
    return error instanceof Error ? error.message : "未知错误"
  }
  if (error.status === 0) return "无法连接 API 服务，请确认后端已启动。"
  if (error.status === 403) return "当前账户没有管理员权限。"
  const reason = typeof error.details.reason === "string" ? error.details.reason : ""
  if (error.status === 422 && reason !== "") return `保存失败（${reason}）：${error.message}`
  if (error.status === 422) return `保存失败：${error.message}`
  return error.message
}

function parseTtl(raw: string): number | null | undefined {
  const trimmed = raw.trim()
  if (trimmed === "") return null
  const value = Number(trimmed)
  if (!Number.isInteger(value) || value < MIN_TTL_SECONDS || value > MAX_TTL_SECONDS) {
    return undefined
  }
  return value
}

const inputClass =
  "mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-60"
const labelClass = "text-xs font-medium text-muted-foreground"
const primaryButtonClass =
  "inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
const secondaryButtonClass =
  "inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"

export type AccessFormProps = {
  accountId: number
  systems: SystemDTO[]
  access?: AccessDTO
  canWrite: boolean
  onSubmit: (input: unknown) => Promise<void>
  onCancel?: () => void
}

/**
 * Access editor shared by the account/access panel. Credential semantics follow
 * spec §7.4: an empty field omits `credential` (keep), the clear box sends an
 * empty string (clear) and a typed value replaces it. The stored credential is
 * never returned by the API, so the input always starts blank and is never
 * echoed back.
 */
export function AccessForm({
  accountId,
  systems,
  access,
  canWrite,
  onSubmit,
  onCancel,
}: AccessFormProps) {
  const [form, setForm] = useState<AccessFormState>(() => toFormState(access, systems))
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState("")

  const editing = access !== undefined

  // The account panel loads systems in parallel with accounts; when a create
  // form is opened before they arrive, adopt the first system once they do.
  useEffect(() => {
    if (editing) return
    setForm((previous) => {
      if (previous.systemId !== "" || systems.length === 0) return previous
      return { ...previous, systemId: String(systems[0].id) }
    })
  }, [editing, systems])

  if (!canWrite) return null

  function updateForm<K extends keyof AccessFormState>(field: K, value: AccessFormState[K]) {
    setForm((previous) => ({ ...previous, [field]: value }))
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setError("")

    if (systems.length === 0) {
      setError("暂无系统可选，请先在「系统与环境」中创建系统。")
      return
    }
    const systemId = Number(form.systemId)
    if (!Number.isInteger(systemId) || !systems.some((system) => system.id === systemId)) {
      setError("请选择 Access 关联的系统。")
      return
    }
    const concurrencyLimit = Number(form.concurrencyLimit)
    if (
      !Number.isInteger(concurrencyLimit) ||
      concurrencyLimit < MIN_CONCURRENCY ||
      concurrencyLimit > MAX_CONCURRENCY
    ) {
      setError(`并发上限需为 ${MIN_CONCURRENCY}–${MAX_CONCURRENCY} 之间的整数。`)
      return
    }
    const sessionTtlSeconds = parseTtl(form.sessionTtlSeconds)
    if (sessionTtlSeconds === undefined) {
      setError(
        `会话 TTL 需为 ${MIN_TTL_SECONDS}–${MAX_TTL_SECONDS} 秒之间的整数，或留空继承系统默认值。`,
      )
      return
    }
    const loginUrl = form.loginUrl.trim()
    if (loginUrl !== "" && !/^https?:\/\//i.test(loginUrl)) {
      setError("登录 URL 必须以 http:// 或 https:// 开头。")
      return
    }
    const usernameOverride = form.usernameOverride.trim()
    if (form.authType === "basic" && usernameOverride === "") {
      setError("Basic 认证需要填写用户名覆盖。")
      return
    }

    const payload: AccessWriteInput = {
      systemId,
      authType: form.authType,
      usernameOverride,
      loginUrl,
      enabled: form.enabled,
      concurrencyLimit,
      sessionTtlSeconds,
      permissionNote: form.permissionNote.trim(),
    }
    if (form.clearCredential) {
      payload.credential = ""
    } else if (form.credential !== "") {
      payload.credential = form.credential
    }

    setSaving(true)
    try {
      await onSubmit(payload)
      setForm((previous) => ({ ...previous, credential: "", clearCredential: false }))
    } catch (caught) {
      setError(describeError(caught))
    } finally {
      setSaving(false)
    }
  }

  return (
    <form
      onSubmit={(event) => void handleSubmit(event)}
      noValidate
      aria-labelledby="access-form-title"
      className="space-y-3 rounded-xl border border-border bg-muted/30 p-3"
    >
      <h3 id="access-form-title" className="text-sm font-semibold">
        {editing ? "编辑 Access" : "新建 Access"}
      </h3>
      <p className="text-[11px] text-muted-foreground">账号 #{accountId}</p>

      {error ? (
        <p
          role="alert"
          className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
        >
          {error}
        </p>
      ) : null}

      <div className="grid gap-2 sm:grid-cols-2">
        <div>
          <label htmlFor="access-system" className={labelClass}>
            系统
          </label>
          <select
            id="access-system"
            value={form.systemId}
            onChange={(event) => updateForm("systemId", event.target.value)}
            className={inputClass}
          >
            {systems.length === 0 ? <option value="">（暂无系统）</option> : null}
            {systems.map((system) => (
              <option key={system.id} value={system.id}>
                {system.name}（{system.key}）
              </option>
            ))}
          </select>
        </div>
        <div>
          <label htmlFor="access-auth-type" className={labelClass}>
            认证类型
          </label>
          <select
            id="access-auth-type"
            value={form.authType}
            onChange={(event) => updateForm("authType", event.target.value)}
            className={inputClass}
          >
            {AUTH_TYPE_OPTIONS.map((option) => (
              <option key={option.value} value={option.value}>
                {option.label}
              </option>
            ))}
          </select>
        </div>
        <div>
          <label htmlFor="access-username-override" className={labelClass}>
            用户名覆盖
          </label>
          <input
            id="access-username-override"
            value={form.usernameOverride}
            onChange={(event) => updateForm("usernameOverride", event.target.value)}
            autoComplete="off"
            placeholder="Basic 认证的用户名"
            className={inputClass}
          />
        </div>
        <div>
          <label htmlFor="access-login-url" className={labelClass}>
            登录 URL
          </label>
          <input
            id="access-login-url"
            value={form.loginUrl}
            onChange={(event) => updateForm("loginUrl", event.target.value)}
            autoComplete="off"
            placeholder="https://example.test/login"
            className={inputClass}
          />
        </div>
        <div>
          <label htmlFor="access-concurrency-limit" className={labelClass}>
            并发上限
          </label>
          <input
            id="access-concurrency-limit"
            type="number"
            min={MIN_CONCURRENCY}
            max={MAX_CONCURRENCY}
            value={form.concurrencyLimit}
            onChange={(event) => updateForm("concurrencyLimit", event.target.value)}
            className={inputClass}
          />
        </div>
        <div>
          <label htmlFor="access-ttl" className={labelClass}>
            会话 TTL（秒）
          </label>
          <input
            id="access-ttl"
            type="number"
            min={MIN_TTL_SECONDS}
            max={MAX_TTL_SECONDS}
            value={form.sessionTtlSeconds}
            onChange={(event) => updateForm("sessionTtlSeconds", event.target.value)}
            placeholder="留空继承系统默认值"
            className={inputClass}
          />
        </div>
        <div className="sm:col-span-2">
          <label htmlFor="access-permission-note" className={labelClass}>
            权限备注
          </label>
          <input
            id="access-permission-note"
            value={form.permissionNote}
            onChange={(event) => updateForm("permissionNote", event.target.value)}
            autoComplete="off"
            placeholder="例如 只读账号"
            className={inputClass}
          />
        </div>
      </div>

      <div>
        <label htmlFor="access-credential" className={labelClass}>
          凭据 credential
        </label>
        <input
          id="access-credential"
          type="password"
          value={form.credential}
          onChange={(event) => updateForm("credential", event.target.value)}
          disabled={form.clearCredential}
          autoComplete="new-password"
          placeholder={editing ? "留空保持不变" : "留空不设置凭据"}
          className={inputClass}
        />
        <p className="mt-1 text-[11px] text-muted-foreground">
          已保存的凭据不会回显；留空表示保持不变，输入新值表示替换。
        </p>
        {editing && access.credentialSet ? (
          <label
            htmlFor="access-clear-credential"
            className="mt-1.5 flex items-center gap-1.5 text-xs"
          >
            <input
              id="access-clear-credential"
              type="checkbox"
              checked={form.clearCredential}
              onChange={(event) =>
                setForm((previous) => ({
                  ...previous,
                  clearCredential: event.target.checked,
                  credential: event.target.checked ? "" : previous.credential,
                }))
              }
            />
            清除已保存的凭据
          </label>
        ) : null}
      </div>

      <label htmlFor="access-enabled" className="flex items-center gap-1.5 text-xs">
        <input
          id="access-enabled"
          type="checkbox"
          checked={form.enabled}
          onChange={(event) => updateForm("enabled", event.target.checked)}
        />
        启用
      </label>

      <div className="flex justify-end gap-1.5 pt-1">
        {onCancel ? (
          <button
            type="button"
            onClick={onCancel}
            disabled={saving}
            className={secondaryButtonClass}
          >
            取消
          </button>
        ) : null}
        <button type="submit" disabled={saving} className={primaryButtonClass}>
          {saving ? "保存中…" : "保存"}
        </button>
      </div>
    </form>
  )
}
