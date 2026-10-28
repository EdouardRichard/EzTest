"use client"

import { type FormEvent, type KeyboardEvent, useEffect, useRef, useState } from "react"

import { ApiError } from "@/lib/api-client"

import { createAuthFlow, updateAuthFlow } from "./api"
import type { AuthFlowDefinitionDTO, AuthFlowDTO, SystemDTO } from "./types"

/** Timeout bounds of spec §3.4. */
const MIN_TIMEOUT_MS = 5000
const MAX_TIMEOUT_MS = 60000
const DEFAULT_TIMEOUT_MS = 20000
const KIND_FORM = "form"

type SuccessKind = "url_contains" | "selector"

type AuthFlowFormState = {
  systemId: string
  name: string
  loginUrl: string
  usernameSelector: string
  passwordSelector: string
  submitSelector: string
  successKind: SuccessKind
  successValue: string
  mfaSelector: string
  captchaSelector: string
  invalidSelector: string
  timeoutMs: string
  enabled: boolean
}

function toFormState(flow: AuthFlowDTO | undefined, systems: SystemDTO[]): AuthFlowFormState {
  if (!flow) {
    return {
      systemId: systems[0] === undefined ? "" : String(systems[0].id),
      name: "",
      loginUrl: "",
      usernameSelector: "",
      passwordSelector: "",
      submitSelector: "",
      successKind: "url_contains",
      successValue: "",
      mfaSelector: "",
      captchaSelector: "",
      invalidSelector: "",
      timeoutMs: String(DEFAULT_TIMEOUT_MS),
      enabled: true,
    }
  }
  const definition = flow.definition
  const successKind: SuccessKind =
    definition.success.selector !== undefined && definition.success.selector !== ""
      ? "selector"
      : "url_contains"
  return {
    systemId: flow.system_id,
    name: flow.name,
    loginUrl: definition.login_url,
    usernameSelector: definition.username_selector,
    passwordSelector: definition.password_selector,
    submitSelector: definition.submit_selector,
    successKind,
    successValue:
      successKind === "selector"
        ? (definition.success.selector ?? "")
        : (definition.success.url_contains ?? ""),
    mfaSelector: definition.failure.mfa_selector ?? "",
    captchaSelector: definition.failure.captcha_selector ?? "",
    invalidSelector: definition.failure.invalid_selector ?? "",
    timeoutMs: String(definition.timeout_ms),
    enabled: flow.enabled,
  }
}

function isHttpUrl(raw: string): boolean {
  try {
    const parsed = new URL(raw)
    return (parsed.protocol === "http:" || parsed.protocol === "https:") && parsed.host !== ""
  } catch {
    return false
  }
}

/** Enforces spec §3.4 required fields, http(s) URL, one success rule and the timeout range. */
function validateForm(form: AuthFlowFormState, systems: SystemDTO[]): string {
  if (systems.length === 0) {
    return "暂无系统可选，请先在「系统与环境」中创建系统。"
  }
  const systemId = Number(form.systemId)
  if (!Number.isInteger(systemId) || !systems.some((system) => system.id === systemId)) {
    return "请选择配方所属的系统。"
  }
  if (form.name.trim() === "") return "请输入配方名称。"
  if (!isHttpUrl(form.loginUrl.trim())) {
    return "登录 URL 必须是 http:// 或 https:// 开头的绝对地址。"
  }
  if (form.usernameSelector.trim() === "") return "请输入用户名选择器。"
  if (form.passwordSelector.trim() === "") return "请输入密码选择器。"
  if (form.submitSelector.trim() === "") return "请输入提交按钮选择器。"
  if (form.successValue.trim() === "") {
    return "请填写成功判定（URL 包含或选择器，二选一）。"
  }
  const timeoutMs = Number(form.timeoutMs)
  if (!Number.isInteger(timeoutMs) || timeoutMs < MIN_TIMEOUT_MS || timeoutMs > MAX_TIMEOUT_MS) {
    return `超时需为 ${MIN_TIMEOUT_MS}–${MAX_TIMEOUT_MS} 之间的整数毫秒。`
  }
  return ""
}

function buildDefinition(form: AuthFlowFormState): AuthFlowDefinitionDTO {
  const failure: AuthFlowDefinitionDTO["failure"] = {}
  if (form.mfaSelector.trim() !== "") failure.mfa_selector = form.mfaSelector.trim()
  if (form.captchaSelector.trim() !== "") failure.captcha_selector = form.captchaSelector.trim()
  if (form.invalidSelector.trim() !== "") failure.invalid_selector = form.invalidSelector.trim()
  const value = form.successValue.trim()
  return {
    login_url: form.loginUrl.trim(),
    username_selector: form.usernameSelector.trim(),
    password_selector: form.passwordSelector.trim(),
    submit_selector: form.submitSelector.trim(),
    success: form.successKind === "url_contains" ? { url_contains: value } : { selector: value },
    failure,
    timeout_ms: Number(form.timeoutMs),
  }
}

const ERROR_REASON_LABELS: Record<string, string> = {
  auth_flow_invalid: "登录配方无效",
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
  if (label) return `保存失败：${label}${location}：${error.message}`
  if (reason !== "") return `保存失败${location}：${error.message}`
  return error.message
}

/** Tab order inside the modal dialog; disabled controls are skipped. */
const FOCUSABLE_SELECTOR = [
  "a[href]",
  "button:not([disabled])",
  "input:not([disabled])",
  "select:not([disabled])",
  "textarea:not([disabled])",
  "[tabindex]:not([tabindex='-1'])",
].join(", ")

const inputClass =
  "mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-60"
const labelClass = "text-xs font-medium text-muted-foreground"
const primaryButtonClass =
  "inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
const secondaryButtonClass =
  "inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"

export type AuthFlowDialogProps = {
  systems: SystemDTO[]
  /** Present when editing; system is immutable after creation. */
  flow?: AuthFlowDTO
  onClose: () => void
  onSaved: (flow: AuthFlowDTO) => void | Promise<void>
}

/**
 * Admin-only create/edit dialog for one form login recipe (spec §3.4): login URL,
 * the three submit selectors, exactly one success rule, optional failure
 * selectors and the 5000-60000 ms timeout. Writes go through the auth-flow API;
 * the parent reloads the list from `onSaved`.
 */
export function AuthFlowDialog({ systems, flow, onClose, onSaved }: AuthFlowDialogProps) {
  const editing = flow !== undefined
  const [form, setForm] = useState<AuthFlowFormState>(() => toFormState(flow, systems))
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState("")
  const dialogRef = useRef<HTMLDivElement>(null)
  const systemRef = useRef<HTMLSelectElement>(null)
  const nameRef = useRef<HTMLInputElement>(null)
  const triggerRef = useRef<HTMLElement | null>(null)

  // Move focus into the modal on open and hand it back to the trigger on close.
  useEffect(() => {
    const trigger = document.activeElement
    triggerRef.current = trigger instanceof HTMLElement ? trigger : null
    if (editing) nameRef.current?.focus()
    else systemRef.current?.focus()
    return () => {
      const previous = triggerRef.current
      if (previous?.isConnected) previous.focus()
    }
  }, [editing])

  function updateForm<K extends keyof AuthFlowFormState>(field: K, value: AuthFlowFormState[K]) {
    setForm((previous) => ({ ...previous, [field]: value }))
  }

  function handleDialogKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    if (event.key === "Escape") {
      event.stopPropagation()
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

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setError("")
    const validationError = validateForm(form, systems)
    if (validationError !== "") {
      setError(validationError)
      return
    }
    const payload = {
      name: form.name.trim(),
      kind: KIND_FORM,
      definition: buildDefinition(form),
      enabled: form.enabled,
    }
    setSaving(true)
    try {
      const saved =
        flow === undefined
          ? await createAuthFlow({ system_id: String(Number(form.systemId)), ...payload })
          : await updateAuthFlow(flow.id, payload)
      await onSaved(saved)
    } catch (caught) {
      setError(describeError(caught))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-black/40 p-4 sm:items-center">
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby="auth-flow-dialog-title"
        data-testid="auth-flow-dialog"
        onKeyDown={handleDialogKeyDown}
        className="w-full max-w-3xl space-y-3 rounded-2xl border border-border bg-card p-4 shadow-xl"
      >
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div>
            <h2 id="auth-flow-dialog-title" className="text-base font-semibold">
              {editing ? "编辑登录配方" : "新建登录配方"}
            </h2>
            <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
              表单登录步骤：打开登录页、填写账号密码、提交并等待成功（URL 包含或选择器二选一）。
              失败选择器用于识别 MFA／验证码／凭据无效并转人工接管。
            </p>
          </div>
          <button
            type="button"
            onClick={onClose}
            disabled={saving}
            className={secondaryButtonClass}
          >
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

        <form onSubmit={(event) => void handleSubmit(event)} noValidate className="space-y-3">
          <div className="grid gap-2 sm:grid-cols-3">
            <div>
              <label htmlFor="auth-flow-system" className={labelClass}>
                系统
              </label>
              <select
                id="auth-flow-system"
                ref={systemRef}
                value={form.systemId}
                onChange={(event) => updateForm("systemId", event.target.value)}
                disabled={editing}
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
              <label htmlFor="auth-flow-name" className={labelClass}>
                名称
              </label>
              <input
                id="auth-flow-name"
                ref={nameRef}
                value={form.name}
                onChange={(event) => updateForm("name", event.target.value)}
                autoComplete="off"
                placeholder="例如 Shop 表单登录"
                className={inputClass}
              />
            </div>
            <div>
              <label htmlFor="auth-flow-kind" className={labelClass}>
                类型
              </label>
              <select id="auth-flow-kind" value={KIND_FORM} disabled className={inputClass}>
                <option value={KIND_FORM}>form（表单）</option>
              </select>
            </div>
          </div>

          <div>
            <label htmlFor="auth-flow-login-url" className={labelClass}>
              登录 URL
            </label>
            <input
              id="auth-flow-login-url"
              value={form.loginUrl}
              onChange={(event) => updateForm("loginUrl", event.target.value)}
              autoComplete="off"
              placeholder="http://127.0.0.1:13335/p6/login"
              className={inputClass}
            />
          </div>

          <div className="grid gap-2 sm:grid-cols-3">
            <div>
              <label htmlFor="auth-flow-username-selector" className={labelClass}>
                用户名选择器
              </label>
              <input
                id="auth-flow-username-selector"
                value={form.usernameSelector}
                onChange={(event) => updateForm("usernameSelector", event.target.value)}
                autoComplete="off"
                placeholder="#username"
                className={inputClass}
              />
            </div>
            <div>
              <label htmlFor="auth-flow-password-selector" className={labelClass}>
                密码选择器
              </label>
              <input
                id="auth-flow-password-selector"
                value={form.passwordSelector}
                onChange={(event) => updateForm("passwordSelector", event.target.value)}
                autoComplete="off"
                placeholder="#password"
                className={inputClass}
              />
            </div>
            <div>
              <label htmlFor="auth-flow-submit-selector" className={labelClass}>
                提交选择器
              </label>
              <input
                id="auth-flow-submit-selector"
                value={form.submitSelector}
                onChange={(event) => updateForm("submitSelector", event.target.value)}
                autoComplete="off"
                placeholder="button[type=submit]"
                className={inputClass}
              />
            </div>
          </div>

          <fieldset className="space-y-1.5">
            <legend className={labelClass}>成功判定</legend>
            <div className="flex flex-wrap items-center gap-3 text-xs">
              <label htmlFor="auth-flow-success-url-contains" className="flex items-center gap-1.5">
                <input
                  id="auth-flow-success-url-contains"
                  type="radio"
                  name="auth-flow-success-kind"
                  checked={form.successKind === "url_contains"}
                  onChange={() => updateForm("successKind", "url_contains")}
                />
                URL 包含
              </label>
              <label
                htmlFor="auth-flow-success-selector-rule"
                className="flex items-center gap-1.5"
              >
                <input
                  id="auth-flow-success-selector-rule"
                  type="radio"
                  name="auth-flow-success-kind"
                  checked={form.successKind === "selector"}
                  onChange={() => updateForm("successKind", "selector")}
                />
                选择器
              </label>
            </div>
            <div>
              <label htmlFor="auth-flow-success-value" className={labelClass}>
                {form.successKind === "url_contains" ? "成功 URL 包含" : "成功选择器"}
              </label>
              <input
                id="auth-flow-success-value"
                value={form.successValue}
                onChange={(event) => updateForm("successValue", event.target.value)}
                autoComplete="off"
                placeholder={form.successKind === "url_contains" ? "/p6/home" : "#home"}
                className={inputClass}
              />
            </div>
          </fieldset>

          <div className="grid gap-2 sm:grid-cols-3">
            <div>
              <label htmlFor="auth-flow-mfa-selector" className={labelClass}>
                MFA 选择器
              </label>
              <input
                id="auth-flow-mfa-selector"
                value={form.mfaSelector}
                onChange={(event) => updateForm("mfaSelector", event.target.value)}
                autoComplete="off"
                placeholder="可选，例如 #mfa"
                className={inputClass}
              />
            </div>
            <div>
              <label htmlFor="auth-flow-captcha-selector" className={labelClass}>
                验证码选择器
              </label>
              <input
                id="auth-flow-captcha-selector"
                value={form.captchaSelector}
                onChange={(event) => updateForm("captchaSelector", event.target.value)}
                autoComplete="off"
                placeholder="可选，例如 #captcha"
                className={inputClass}
              />
            </div>
            <div>
              <label htmlFor="auth-flow-invalid-selector" className={labelClass}>
                无效凭据选择器
              </label>
              <input
                id="auth-flow-invalid-selector"
                value={form.invalidSelector}
                onChange={(event) => updateForm("invalidSelector", event.target.value)}
                autoComplete="off"
                placeholder="可选，例如 .login-error"
                className={inputClass}
              />
            </div>
          </div>

          <div className="flex flex-wrap items-end justify-between gap-2">
            <div className="w-40">
              <label htmlFor="auth-flow-timeout" className={labelClass}>
                超时（毫秒）
              </label>
              <input
                id="auth-flow-timeout"
                type="number"
                min={MIN_TIMEOUT_MS}
                max={MAX_TIMEOUT_MS}
                value={form.timeoutMs}
                onChange={(event) => updateForm("timeoutMs", event.target.value)}
                className={inputClass}
              />
            </div>
            <label htmlFor="auth-flow-enabled" className="flex items-center gap-1.5 text-xs">
              <input
                id="auth-flow-enabled"
                type="checkbox"
                checked={form.enabled}
                onChange={(event) => updateForm("enabled", event.target.checked)}
              />
              启用
            </label>
          </div>

          <div className="flex justify-end gap-1.5">
            <button
              type="button"
              onClick={onClose}
              disabled={saving}
              className={secondaryButtonClass}
            >
              取消
            </button>
            <button
              type="submit"
              data-testid="auth-flow-save"
              disabled={saving}
              className={primaryButtonClass}
            >
              {saving ? "保存中…" : "保存配方"}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
