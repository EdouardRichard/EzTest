"use client"

import { Database, RefreshCw, Trash2, Zap } from "lucide-react"
import { type FormEvent, useEffect, useState } from "react"

import { useDBTarget } from "../hooks/useDBTarget"
import type { DBTargetDTO } from "../types"

const SSL_MODE_OPTIONS = [
  { value: "disable", label: "disable（不加密）" },
  { value: "require", label: "require（要求 TLS）" },
  { value: "verify-ca", label: "verify-ca（校验 CA）" },
  { value: "verify-full", label: "verify-full（校验主机名）" },
]

const CONNECT_TIMEOUT_RANGE = [500, 30000] as const
const STATEMENT_TIMEOUT_RANGE = [500, 60000] as const
const MAX_ROWS_RANGE = [1, 1000] as const

type FormState = {
  host: string
  port: string
  database: string
  username: string
  password: string
  sslmode: string
  connectTimeoutMs: string
  statementTimeoutMs: string
  maxRows: string
  enabled: boolean
}

const EMPTY_FORM: FormState = {
  host: "",
  port: "5432",
  database: "",
  username: "",
  password: "",
  sslmode: "disable",
  connectTimeoutMs: "3000",
  statementTimeoutMs: "5000",
  maxRows: "100",
  enabled: true,
}

function toFormState(target: DBTargetDTO | null): FormState {
  if (target === null) return EMPTY_FORM
  return {
    host: target.host,
    port: String(target.port),
    database: target.database,
    username: target.username,
    password: "",
    sslmode: target.sslmode,
    connectTimeoutMs: String(target.connectTimeoutMs),
    statementTimeoutMs: String(target.statementTimeoutMs),
    maxRows: String(target.maxRows),
    enabled: target.enabled,
  }
}

function parseBoundedInteger(raw: string, range: readonly [number, number]): number | null {
  const value = Number(raw)
  if (!Number.isInteger(value) || value < range[0] || value > range[1]) return null
  return value
}

/** A password is required on first configuration and whenever none is stored. */
function needsPassword(target: DBTargetDTO | null): boolean {
  return target === null || !target.credentialSet
}

/**
 * Compares the editable form against the loaded target. An unchanged form is
 * probed as the stored configuration (which persists last_tested); a dirty form
 * must be sent as an unsaved override.
 */
function isFormDirty(form: FormState, target: DBTargetDTO | null): boolean {
  if (target === null) return true
  return (
    form.host !== target.host ||
    form.port !== String(target.port) ||
    form.database !== target.database ||
    form.username !== target.username ||
    form.password !== "" ||
    form.sslmode !== target.sslmode ||
    form.connectTimeoutMs !== String(target.connectTimeoutMs) ||
    form.statementTimeoutMs !== String(target.statementTimeoutMs) ||
    form.maxRows !== String(target.maxRows) ||
    form.enabled !== target.enabled
  )
}

type PayloadResult = { payload?: FormPayload; error: string }

type FormPayload = {
  name: string
  host: string
  port: number
  database: string
  username: string
  sslmode: string
  connectTimeoutMs: number
  statementTimeoutMs: number
  maxRows: number
  enabled: boolean
  password?: string
}

/**
 * Builds the PUT body allowed by spec §7.8. A blank password is omitted so the
 * backend keeps the stored credential; the name is not editable here and is
 * echoed back so an unrelated save does not clear it.
 */
function buildPayload(form: FormState, target: DBTargetDTO | null): PayloadResult {
  if (form.host.trim() === "") return { error: "请输入主机 host。" }
  const port = parseBoundedInteger(form.port, [1, 65535])
  if (port === null) return { error: "端口需为 1–65535 的整数。" }
  if (form.database.trim() === "") return { error: "请输入数据库名。" }
  if (form.username.trim() === "") return { error: "请输入用户名。" }
  if (needsPassword(target) && form.password === "") {
    return { error: "请填写密码：首次配置或尚无已存储密码时必须填写。" }
  }
  const connectTimeoutMs = parseBoundedInteger(form.connectTimeoutMs, CONNECT_TIMEOUT_RANGE)
  if (connectTimeoutMs === null) return { error: "连接超时需为 500–30000 毫秒。" }
  const statementTimeoutMs = parseBoundedInteger(form.statementTimeoutMs, STATEMENT_TIMEOUT_RANGE)
  if (statementTimeoutMs === null) return { error: "语句超时需为 500–60000 毫秒。" }
  const maxRows = parseBoundedInteger(form.maxRows, MAX_ROWS_RANGE)
  if (maxRows === null) return { error: "最大行数需为 1–1000 的整数。" }

  return {
    payload: {
      name: target?.name ?? "",
      host: form.host.trim(),
      port,
      database: form.database.trim(),
      username: form.username.trim(),
      sslmode: form.sslmode,
      connectTimeoutMs,
      statementTimeoutMs,
      maxRows,
      enabled: form.enabled,
      ...(form.password === "" ? {} : { password: form.password }),
    },
    error: "",
  }
}

const inputClass =
  "mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
const labelClass = "text-xs font-medium text-muted-foreground"
const hintClass = "mt-1 text-[11px] leading-relaxed text-muted-foreground"
const primaryButtonClass =
  "inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
const secondaryButtonClass =
  "inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
const dangerButtonClass =
  "inline-flex items-center gap-1.5 rounded-lg border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm font-medium text-destructive transition-colors hover:bg-destructive/20 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
const warningClass =
  "rounded-xl border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-sm text-amber-800 dark:text-amber-200"

export type DBTargetSectionProps = {
  environmentId: number
  environmentName: string
  canWrite: boolean
}

/**
 * Environment detail "目标数据库" section (spec §10.7). The whole section is
 * admin-only because the db target API answers 403 to ordinary users; the
 * password field is write-only and shows only whether a credential is stored.
 */
export function DBTargetSection({
  environmentId,
  environmentName,
  canWrite,
}: DBTargetSectionProps) {
  const {
    target,
    loading,
    loaded,
    error,
    notice,
    saving,
    deleting,
    testing,
    testResult,
    reload,
    save,
    remove,
    testConnection,
    clearTestResult,
  } = useDBTarget(environmentId, canWrite)
  const [form, setForm] = useState<FormState>(EMPTY_FORM)
  const [formError, setFormError] = useState("")
  const [confirmingDelete, setConfirmingDelete] = useState(false)

  useEffect(() => {
    if (!loading) setForm(toFormState(target))
  }, [loading, target])

  if (!canWrite) return null

  function updateForm<K extends keyof FormState>(field: K, value: FormState[K]) {
    setForm((previous) => ({ ...previous, [field]: value }))
    setFormError("")
    // A previous probe described different values; keep the panel honest.
    clearTestResult()
  }

  async function handleSave(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const { payload, error: validationError } = buildPayload(form, target)
    if (payload === undefined) {
      setFormError(validationError)
      return
    }
    setFormError("")
    const saved = await save(payload)
    if (saved !== null) {
      setConfirmingDelete(false)
      setForm((previous) => ({ ...previous, password: "" }))
    }
  }

  async function handleTest() {
    const { payload, error: validationError } = buildPayload(form, target)
    if (payload === undefined) {
      setFormError(validationError)
      return
    }
    setFormError("")
    // Testing the stored configuration (T5) updates last_tested; only a dirty
    // form is sent as a non-persisted override.
    const storedUnchanged = target !== null && !needsPassword(target) && !isFormDirty(form, target)
    await testConnection(storedUnchanged ? undefined : payload)
  }

  async function handleDelete() {
    setConfirmingDelete(false)
    await remove()
  }

  const requiresPassword = needsPassword(target)
  const roleWarning = testResult?.warnings.includes("role_has_write_privileges") ?? false
  const notReadOnly = testResult?.ok === true && testResult?.transactionReadOnly === false

  return (
    <section
      data-testid="db-target-section"
      aria-labelledby={`db-target-title-${environmentId}`}
      className="space-y-3 rounded-2xl border border-border bg-card p-3 shadow-sm"
    >
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <h3
            id={`db-target-title-${environmentId}`}
            className="flex items-center gap-2 text-base font-semibold"
          >
            <Database className="h-4 w-4 text-primary" aria-hidden="true" />
            目标数据库
          </h3>
          <p className={hintClass}>
            {environmentName} 的只读 PostgreSQL 目标；仅管理员可维护，密码只写不回显。
          </p>
        </div>
        <div className="flex items-center gap-1.5">
          {loaded ? (
            <span
              data-testid="db-target-password-state"
              className="rounded-lg border border-border px-2 py-1 text-[11px] text-muted-foreground"
            >
              {target?.credentialSet ? "密码已设置" : "密码未设置"}
            </span>
          ) : null}
          <button
            type="button"
            onClick={() => void reload()}
            disabled={loading}
            aria-label="刷新目标数据库"
            className={secondaryButtonClass}
          >
            <RefreshCw className={`h-4 w-4 ${loading ? "animate-spin" : ""}`} aria-hidden="true" />
          </button>
        </div>
      </div>

      {error ? (
        <p
          role="alert"
          className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
        >
          {error}
        </p>
      ) : null}
      {formError ? (
        <p
          role="alert"
          className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
        >
          {formError}
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

      {loading ? (
        <p className="rounded-xl border border-dashed border-border px-4 py-6 text-center text-sm text-muted-foreground">
          正在加载目标数据库…
        </p>
      ) : !loaded ? (
        <div className="flex flex-wrap items-center justify-between gap-2 rounded-xl border border-dashed border-border px-4 py-3">
          <p className="text-sm text-muted-foreground">
            目标数据库加载失败，为避免覆盖已有配置，暂不显示表单。
          </p>
          <button
            type="button"
            data-testid="db-target-retry"
            onClick={() => void reload()}
            className={secondaryButtonClass}
          >
            重试
          </button>
        </div>
      ) : (
        <>
          {target === null ? (
            <p className="rounded-xl border border-dashed border-border px-4 py-3 text-sm text-muted-foreground">
              尚未配置目标数据库
            </p>
          ) : (
            <p className={hintClass}>
              最近测试：
              {target.lastTestStatus === "ok"
                ? "通过"
                : target.lastTestStatus === "failed"
                  ? `失败${target.lastTestMessage === "" ? "" : `（${target.lastTestMessage}）`}`
                  : "从未测试"}
            </p>
          )}

          <form
            data-testid="db-target-form"
            onSubmit={(event) => void handleSave(event)}
            className="space-y-2 rounded-xl border border-border bg-muted/30 p-3"
          >
            <div className="grid gap-2 sm:grid-cols-2">
              <div>
                <label htmlFor="db-target-host" className={labelClass}>
                  主机
                </label>
                <input
                  id="db-target-host"
                  value={form.host}
                  onChange={(event) => updateForm("host", event.target.value)}
                  autoComplete="off"
                  placeholder="127.0.0.1"
                  className={inputClass}
                />
              </div>
              <div>
                <label htmlFor="db-target-port" className={labelClass}>
                  端口
                </label>
                <input
                  id="db-target-port"
                  value={form.port}
                  onChange={(event) => updateForm("port", event.target.value)}
                  inputMode="numeric"
                  autoComplete="off"
                  className={inputClass}
                />
              </div>
              <div>
                <label htmlFor="db-target-database" className={labelClass}>
                  数据库
                </label>
                <input
                  id="db-target-database"
                  value={form.database}
                  onChange={(event) => updateForm("database", event.target.value)}
                  autoComplete="off"
                  placeholder="eztest_target"
                  className={inputClass}
                />
              </div>
              <div>
                <label htmlFor="db-target-username" className={labelClass}>
                  用户名
                </label>
                <input
                  id="db-target-username"
                  value={form.username}
                  onChange={(event) => updateForm("username", event.target.value)}
                  autoComplete="off"
                  placeholder="eztest_ro"
                  className={inputClass}
                />
              </div>
              <div>
                <label htmlFor="db-target-password" className={labelClass}>
                  密码
                </label>
                <input
                  id="db-target-password"
                  type="password"
                  value={form.password}
                  onChange={(event) => updateForm("password", event.target.value)}
                  autoComplete="new-password"
                  placeholder={requiresPassword ? "首次配置必填" : "已设置（留空保持不变）"}
                  className={inputClass}
                />
                <p className={hintClass}>密码只写不回显；留空表示保留已存储的密码。</p>
              </div>
              <div>
                <label htmlFor="db-target-sslmode" className={labelClass}>
                  SSL 模式
                </label>
                <select
                  id="db-target-sslmode"
                  value={form.sslmode}
                  onChange={(event) => updateForm("sslmode", event.target.value)}
                  className={inputClass}
                >
                  {SSL_MODE_OPTIONS.map((option) => (
                    <option key={option.value} value={option.value}>
                      {option.label}
                    </option>
                  ))}
                </select>
              </div>
              <div>
                <label htmlFor="db-target-connect-timeout" className={labelClass}>
                  连接超时（毫秒）
                </label>
                <input
                  id="db-target-connect-timeout"
                  value={form.connectTimeoutMs}
                  onChange={(event) => updateForm("connectTimeoutMs", event.target.value)}
                  inputMode="numeric"
                  autoComplete="off"
                  className={inputClass}
                />
              </div>
              <div>
                <label htmlFor="db-target-statement-timeout" className={labelClass}>
                  语句超时（毫秒）
                </label>
                <input
                  id="db-target-statement-timeout"
                  value={form.statementTimeoutMs}
                  onChange={(event) => updateForm("statementTimeoutMs", event.target.value)}
                  inputMode="numeric"
                  autoComplete="off"
                  className={inputClass}
                />
              </div>
              <div>
                <label htmlFor="db-target-max-rows" className={labelClass}>
                  最大行数
                </label>
                <input
                  id="db-target-max-rows"
                  value={form.maxRows}
                  onChange={(event) => updateForm("maxRows", event.target.value)}
                  inputMode="numeric"
                  autoComplete="off"
                  className={inputClass}
                />
              </div>
              <div className="flex items-center pt-5">
                <label htmlFor="db-target-enabled" className="flex items-center gap-1.5 text-xs">
                  <input
                    id="db-target-enabled"
                    type="checkbox"
                    checked={form.enabled}
                    onChange={(event) => updateForm("enabled", event.target.checked)}
                  />
                  启用目标数据库
                </label>
              </div>
            </div>

            <div className="flex flex-wrap justify-end gap-1.5 pt-1">
              {target === null ? null : confirmingDelete ? (
                <>
                  <button
                    type="button"
                    onClick={() => void handleDelete()}
                    disabled={deleting}
                    className={dangerButtonClass}
                  >
                    <Trash2 className="h-4 w-4" aria-hidden="true" />
                    确认删除目标数据库
                  </button>
                  <button
                    type="button"
                    onClick={() => setConfirmingDelete(false)}
                    disabled={deleting}
                    className={secondaryButtonClass}
                  >
                    取消
                  </button>
                </>
              ) : (
                <button
                  type="button"
                  onClick={() => setConfirmingDelete(true)}
                  disabled={deleting}
                  className={dangerButtonClass}
                >
                  <Trash2 className="h-4 w-4" aria-hidden="true" />
                  删除目标数据库
                </button>
              )}
              <button
                type="button"
                data-testid="db-target-test-btn"
                onClick={() => void handleTest()}
                disabled={testing || saving}
                className={secondaryButtonClass}
              >
                <Zap className="h-4 w-4" aria-hidden="true" />
                {testing ? "测试中…" : "测试连接"}
              </button>
              <button type="submit" disabled={saving || testing} className={primaryButtonClass}>
                {saving ? "保存中…" : "保存目标数据库"}
              </button>
            </div>
          </form>

          {testResult ? (
            <div
              data-testid="db-target-result"
              role="status"
              className="space-y-1 rounded-xl border border-border bg-muted/30 px-3 py-2 text-sm"
            >
              <p className="font-medium">
                {testResult.ok ? "连接成功" : "连接失败"}（{testResult.latencyMs} ms）
              </p>
              {testResult.ok ? (
                <p className="text-xs text-muted-foreground">
                  延迟 {testResult.latencyMs} ms ·{" "}
                  {testResult.transactionReadOnly ? "只读事务" : "非只读事务"}
                  {testResult.serverVersion ? ` · ${testResult.serverVersion}` : ""}
                  {testResult.currentUser ? ` · ${testResult.currentUser}` : ""}
                </p>
              ) : (
                <p className="text-xs text-destructive">
                  {testResult.error?.reason ?? "unknown"}
                  {testResult.error?.message ? `：${testResult.error.message}` : ""}
                </p>
              )}
            </div>
          ) : null}

          {testResult?.ok && (roleWarning || notReadOnly) ? (
            <p data-testid="db-target-readonly-warning" className={warningClass}>
              {roleWarning
                ? "目标角色具备写权限，建议改用只读账号（role_has_write_privileges）。"
                : ""}
              {roleWarning && notReadOnly ? " " : ""}
              {notReadOnly ? "连接未处于只读事务，写语句可能真正执行，请检查目标角色。" : ""}
            </p>
          ) : null}
        </>
      )}
    </section>
  )
}
