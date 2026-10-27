"use client"

import { Globe, Pencil, Plus, RefreshCw, Trash2 } from "lucide-react"
import { type FormEvent, useCallback, useEffect, useState } from "react"

import { EmptyState } from "@/components/ui/EmptyState"
import { StatusBadge } from "@/components/ui/StatusBadge"
import { ApiError } from "@/lib/api-client"

import { createEnvironment, deleteEnvironment, listEnvironments, updateEnvironment } from "./api"
import type { EnvironmentDTO } from "./types"

const KEY_PATTERN = /^[a-z0-9][a-z0-9_-]{1,63}$/
const DEFAULT_KIND = "staging"
const MAX_ALLOWED_HOSTS = 50
const MAX_EXPECT_STATUS = 10
const MAX_LOGIN_CHECK_URL_LENGTH = 2048

const KIND_OPTIONS = [
  { value: "dev", label: "开发" },
  { value: "staging", label: "预发" },
  { value: "prod", label: "生产" },
  { value: "other", label: "其他" },
]

function kindLabel(kind: string): string {
  return KIND_OPTIONS.find((option) => option.value === kind)?.label ?? kind
}

function describeError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 0) return "无法连接 API 服务，请确认后端已启动。"
    if (error.status === 403) return "当前账户没有管理员权限。"
    if (error.details.reason === "duplicate_key") return "环境标识已存在，请更换 key。"
    if (error.details.reason === "has_dependencies") {
      return "存在依赖，无法删除（has_dependencies）。"
    }
    return error.message
  }
  return error instanceof Error ? error.message : "未知错误"
}

type EnvironmentFormState = {
  key: string
  name: string
  kind: string
  baseUrl: string
  isProd: boolean
  allowedHosts: string
  loginCheckUrl: string
  expectStatus: string
  bodyContains: string
  allowAutoLogin: boolean
  requireApproval: boolean
}

const EMPTY_FORM: EnvironmentFormState = {
  key: "",
  name: "",
  kind: DEFAULT_KIND,
  baseUrl: "",
  isProd: false,
  allowedHosts: "",
  loginCheckUrl: "",
  expectStatus: "200",
  bodyContains: "",
  allowAutoLogin: false,
  requireApproval: false,
}

function toFormState(environment: EnvironmentDTO): EnvironmentFormState {
  return {
    key: environment.key,
    name: environment.name,
    kind: environment.kind,
    baseUrl: environment.baseUrl,
    isProd: environment.isProd,
    allowedHosts: environment.allowedHosts.join(", "),
    loginCheckUrl: environment.loginCheck?.url ?? "",
    expectStatus: environment.loginCheck?.expectStatus.join(", ") ?? "200",
    bodyContains: environment.loginCheck?.bodyContains ?? "",
    allowAutoLogin: environment.allowAutoLogin,
    requireApproval: environment.requireApproval,
  }
}

function splitList(raw: string): string[] {
  return raw
    .split(",")
    .map((part) => part.trim())
    .filter((part) => part !== "")
}

function parseStatuses(raw: string): number[] | null {
  const parts = splitList(raw)
  if (parts.length > MAX_EXPECT_STATUS) return null
  const statuses: number[] = []
  for (const part of parts) {
    const value = Number(part)
    if (!Number.isInteger(value) || value < 100 || value > 599) return null
    statuses.push(value)
  }
  return statuses
}

function validateForm(form: EnvironmentFormState, editing: boolean): string {
  if (!editing && !KEY_PATTERN.test(form.key.trim())) {
    return "环境标识需匹配 ^[a-z0-9][a-z0-9_-]{1,63}$。"
  }
  if (form.name.trim() === "") return "请输入环境名称。"
  if (!/^https?:\/\//i.test(form.baseUrl.trim())) {
    return "Base URL 必须以 http:// 或 https:// 开头。"
  }
  if (splitList(form.allowedHosts).length > MAX_ALLOWED_HOSTS) {
    return `允许主机最多 ${MAX_ALLOWED_HOSTS} 个。`
  }
  if (form.loginCheckUrl.trim() !== "") {
    const loginCheckUrl = form.loginCheckUrl.trim()
    if (loginCheckUrl.length > MAX_LOGIN_CHECK_URL_LENGTH) {
      return `登录检查 URL 不能超过 ${MAX_LOGIN_CHECK_URL_LENGTH} 个字符。`
    }
    // Spec §6.3: an absolute http(s) URL is used as-is; anything else is joined
    // to the environment base URL, so it must be a rooted path such as /account.
    if (!/^https?:\/\//i.test(loginCheckUrl) && !loginCheckUrl.startsWith("/")) {
      return "登录检查 URL 需为 http(s):// 绝对地址，或以 / 开头的相对路径。"
    }
    if (parseStatuses(form.expectStatus) === null) {
      return "期望状态码需为 100–599 的整数，最多 10 个。"
    }
    if (form.bodyContains.length > 512) return "响应体包含内容不能超过 512 个字符。"
  }
  return ""
}

const inputClass =
  "mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
const labelClass = "text-xs font-medium text-muted-foreground"
const primaryButtonClass =
  "inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
const secondaryButtonClass =
  "inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
const rowActionClass =
  "inline-flex items-center gap-1 rounded-lg border border-border px-2 py-1 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
const rowDangerActionClass =
  "inline-flex items-center gap-1 rounded-lg border border-destructive/40 bg-destructive/10 px-2 py-1 text-xs font-medium text-destructive transition-colors hover:bg-destructive/20 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"

export type EnvironmentsPanelProps = {
  systemId: number
  canWrite: boolean
}

/**
 * Environment table for the selected system. Admins get the inline create/edit
 * form including the three `login_check` fields; readers only see the table.
 */
export function EnvironmentsPanel({ systemId, canWrite }: EnvironmentsPanelProps) {
  const [environments, setEnvironments] = useState<EnvironmentDTO[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState("")
  const [notice, setNotice] = useState("")
  const [formOpen, setFormOpen] = useState(false)
  const [editingId, setEditingId] = useState<number | null>(null)
  const [form, setForm] = useState<EnvironmentFormState>(EMPTY_FORM)
  const [saving, setSaving] = useState(false)
  const [deletingId, setDeletingId] = useState<number | null>(null)
  const [confirmingDeleteId, setConfirmingDeleteId] = useState<number | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError("")
    try {
      setEnvironments(await listEnvironments(systemId))
    } catch (caught) {
      setError(describeError(caught))
    } finally {
      setLoading(false)
    }
  }, [systemId])

  useEffect(() => {
    setEnvironments([])
    void load()
  }, [load])

  function updateForm<K extends keyof EnvironmentFormState>(
    field: K,
    value: EnvironmentFormState[K],
  ) {
    setForm((previous) => ({ ...previous, [field]: value }))
  }

  function startCreate() {
    setEditingId(null)
    setForm(EMPTY_FORM)
    setFormOpen(true)
    setError("")
    setNotice("")
  }

  function startEdit(environment: EnvironmentDTO) {
    setEditingId(environment.id)
    setForm(toFormState(environment))
    setFormOpen(true)
    setError("")
    setNotice("")
  }

  function cancelForm() {
    setFormOpen(false)
    setEditingId(null)
    setError("")
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const editing = editingId !== null
    const validationError = validateForm(form, editing)
    if (validationError !== "") {
      setError(validationError)
      return
    }

    const statuses = parseStatuses(form.expectStatus) ?? []
    const loginCheckUrl = form.loginCheckUrl.trim()
    const payload = {
      name: form.name.trim(),
      kind: form.kind,
      baseUrl: form.baseUrl.trim(),
      isProd: form.isProd,
      allowedHosts: splitList(form.allowedHosts),
      loginCheck:
        loginCheckUrl === ""
          ? null
          : {
              url: loginCheckUrl,
              expectStatus: statuses.length === 0 ? [200] : statuses,
              bodyContains: form.bodyContains.trim(),
            },
      allowAutoLogin: form.allowAutoLogin,
      requireApproval: form.requireApproval,
    }

    setSaving(true)
    setError("")
    setNotice("")
    try {
      if (editingId !== null) {
        const updated = await updateEnvironment(editingId, payload)
        setNotice(`已更新环境 ${updated.name}。`)
      } else {
        const created = await createEnvironment(systemId, { key: form.key.trim(), ...payload })
        setNotice(`已创建环境 ${created.name}。`)
      }
      setFormOpen(false)
      setEditingId(null)
      await load()
    } catch (caught) {
      setError(describeError(caught))
    } finally {
      setSaving(false)
    }
  }

  async function handleDelete(environment: EnvironmentDTO) {
    setConfirmingDeleteId(null)
    setDeletingId(environment.id)
    setError("")
    setNotice("")
    try {
      await deleteEnvironment(environment.id)
      setEnvironments((previous) => previous.filter((item) => item.id !== environment.id))
      if (editingId === environment.id) {
        setFormOpen(false)
        setEditingId(null)
      }
      setNotice(`已删除环境 ${environment.name}。`)
    } catch (caught) {
      setError(describeError(caught))
    } finally {
      setDeletingId(null)
    }
  }

  return (
    <section
      aria-labelledby="environments-panel-title"
      className="space-y-3 rounded-2xl border border-border bg-card p-3 shadow-sm"
    >
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <h2
            id="environments-panel-title"
            className="flex items-center gap-2 text-base font-semibold"
          >
            <Globe className="h-4 w-4 text-primary" aria-hidden="true" />
            环境
          </h2>
          <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
            系统 #{systemId} 的环境；登录检查用于判断 Session 是否仍然有效。
          </p>
        </div>
        <div className="flex items-center gap-1.5">
          <button
            type="button"
            onClick={() => void load()}
            disabled={loading}
            aria-label="刷新环境"
            className={secondaryButtonClass}
          >
            <RefreshCw className={`h-4 w-4 ${loading ? "animate-spin" : ""}`} aria-hidden="true" />
          </button>
          {canWrite ? (
            <button type="button" onClick={startCreate} className={primaryButtonClass}>
              <Plus className="h-4 w-4" aria-hidden="true" />
              新建环境
            </button>
          ) : null}
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
      {notice ? (
        <p
          role="status"
          className="rounded-xl border border-emerald-500/25 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-700 dark:text-emerald-300"
        >
          {notice}
        </p>
      ) : null}

      {formOpen && canWrite ? (
        <form
          onSubmit={(event) => void handleSubmit(event)}
          className="space-y-2 rounded-xl border border-border bg-muted/30 p-3"
        >
          <h3 className="text-sm font-semibold">{editingId === null ? "新建环境" : "编辑环境"}</h3>
          <div className="grid gap-2 sm:grid-cols-2">
            <div>
              <label htmlFor="environment-key" className={labelClass}>
                环境标识 key
              </label>
              <input
                id="environment-key"
                value={form.key}
                onChange={(event) => updateForm("key", event.target.value)}
                disabled={editingId !== null}
                autoComplete="off"
                placeholder="例如 staging"
                className={inputClass}
              />
            </div>
            <div>
              <label htmlFor="environment-name" className={labelClass}>
                名称
              </label>
              <input
                id="environment-name"
                value={form.name}
                onChange={(event) => updateForm("name", event.target.value)}
                autoComplete="off"
                placeholder="例如 预发环境"
                className={inputClass}
              />
            </div>
            <div>
              <label htmlFor="environment-kind" className={labelClass}>
                类型
              </label>
              <select
                id="environment-kind"
                value={form.kind}
                onChange={(event) => updateForm("kind", event.target.value)}
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
              <label htmlFor="environment-base-url" className={labelClass}>
                Base URL
              </label>
              <input
                id="environment-base-url"
                value={form.baseUrl}
                onChange={(event) => updateForm("baseUrl", event.target.value)}
                autoComplete="off"
                placeholder="http://localhost:13335"
                className={inputClass}
              />
            </div>
            <div>
              <label htmlFor="environment-allowed-hosts" className={labelClass}>
                允许主机（逗号分隔）
              </label>
              <input
                id="environment-allowed-hosts"
                value={form.allowedHosts}
                onChange={(event) => updateForm("allowedHosts", event.target.value)}
                autoComplete="off"
                placeholder="localhost, 127.0.0.1"
                className={inputClass}
              />
            </div>
            <div className="flex flex-wrap items-center gap-4 pt-5">
              <label htmlFor="environment-is-prod" className="flex items-center gap-1.5 text-xs">
                <input
                  id="environment-is-prod"
                  type="checkbox"
                  checked={form.isProd}
                  onChange={(event) => updateForm("isProd", event.target.checked)}
                />
                生产环境
              </label>
              <label
                htmlFor="environment-allow-auto-login"
                className="flex items-center gap-1.5 text-xs"
              >
                <input
                  id="environment-allow-auto-login"
                  type="checkbox"
                  checked={form.allowAutoLogin}
                  onChange={(event) => updateForm("allowAutoLogin", event.target.checked)}
                />
                允许自动登录
              </label>
              <label
                htmlFor="environment-require-approval"
                className="flex items-center gap-1.5 text-xs"
              >
                <input
                  id="environment-require-approval"
                  type="checkbox"
                  checked={form.requireApproval}
                  onChange={(event) => updateForm("requireApproval", event.target.checked)}
                />
                需要审批
              </label>
            </div>
          </div>

          <fieldset className="space-y-2 rounded-xl border border-border/70 p-2">
            <legend className="px-1 text-xs font-semibold text-muted-foreground">
              登录检查 login_check（可选）
            </legend>
            <div>
              <label htmlFor="environment-login-check-url" className={labelClass}>
                登录检查 URL
              </label>
              <input
                id="environment-login-check-url"
                value={form.loginCheckUrl}
                onChange={(event) => updateForm("loginCheckUrl", event.target.value)}
                autoComplete="off"
                placeholder="/account"
                className={inputClass}
              />
              <p className="mt-1 text-[11px] leading-relaxed text-muted-foreground">
                可填写 http(s):// 绝对地址，或以 / 开头的相对路径（与 Base URL 拼接）。
              </p>
            </div>
            <div className="grid gap-2 sm:grid-cols-2">
              <div>
                <label htmlFor="environment-expect-status" className={labelClass}>
                  期望状态码（逗号分隔）
                </label>
                <input
                  id="environment-expect-status"
                  value={form.expectStatus}
                  onChange={(event) => updateForm("expectStatus", event.target.value)}
                  autoComplete="off"
                  placeholder="200, 302"
                  className={inputClass}
                />
              </div>
              <div>
                <label htmlFor="environment-body-contains" className={labelClass}>
                  响应体包含
                </label>
                <input
                  id="environment-body-contains"
                  value={form.bodyContains}
                  onChange={(event) => updateForm("bodyContains", event.target.value)}
                  autoComplete="off"
                  placeholder="例如 Account"
                  className={inputClass}
                />
              </div>
            </div>
          </fieldset>

          <div className="flex justify-end gap-1.5 pt-1">
            <button
              type="button"
              onClick={cancelForm}
              disabled={saving}
              className={secondaryButtonClass}
            >
              取消
            </button>
            <button type="submit" disabled={saving} className={primaryButtonClass}>
              {saving ? "保存中…" : "保存环境"}
            </button>
          </div>
        </form>
      ) : null}

      {loading && environments.length === 0 ? (
        <p className="rounded-xl border border-dashed border-border px-4 py-6 text-center text-sm text-muted-foreground">
          正在加载环境…
        </p>
      ) : environments.length === 0 ? (
        <EmptyState
          icon={Globe}
          title="暂无环境"
          description={
            canWrite ? "使用“新建环境”为该系统添加第一个环境。" : "该系统尚未配置任何环境。"
          }
        />
      ) : (
        <div className="overflow-x-auto rounded-xl border border-border">
          <table className="min-w-full divide-y divide-border text-sm">
            <caption className="sr-only">环境列表</caption>
            <thead className="bg-muted/50 text-left text-[11px] uppercase tracking-wide text-muted-foreground">
              <tr>
                <th scope="col" className="px-2.5 py-2 font-medium">
                  标识
                </th>
                <th scope="col" className="px-2.5 py-2 font-medium">
                  名称
                </th>
                <th scope="col" className="px-2.5 py-2 font-medium">
                  Base URL
                </th>
                <th scope="col" className="px-2.5 py-2 font-medium">
                  类型
                </th>
                <th scope="col" className="px-2.5 py-2 font-medium">
                  生产
                </th>
                <th scope="col" className="px-2.5 py-2 font-medium">
                  登录检查
                </th>
                <th scope="col" className="px-2.5 py-2 font-medium">
                  自动登录 / 审批
                </th>
                {canWrite ? (
                  <th scope="col" className="px-2.5 py-2 text-right font-medium">
                    操作
                  </th>
                ) : null}
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {environments.map((environment) => (
                <tr key={environment.id}>
                  <td className="px-2.5 py-2 font-mono text-[11px] text-muted-foreground">
                    {environment.key}
                  </td>
                  <td className="px-2.5 py-2 font-medium">{environment.name}</td>
                  <td className="break-all px-2.5 py-2 text-muted-foreground">
                    {environment.baseUrl}
                  </td>
                  <td className="px-2.5 py-2 text-muted-foreground">
                    {kindLabel(environment.kind)}
                  </td>
                  <td className="px-2.5 py-2">
                    {environment.isProd ? (
                      <StatusBadge variant="warning">生产</StatusBadge>
                    ) : (
                      <span className="text-xs text-muted-foreground">非生产</span>
                    )}
                  </td>
                  <td className="px-2.5 py-2">
                    {environment.loginCheck ? (
                      <span className="text-xs" title={environment.loginCheck.url}>
                        已配置
                      </span>
                    ) : (
                      <span className="text-xs text-muted-foreground">未配置</span>
                    )}
                  </td>
                  <td className="px-2.5 py-2 text-xs text-muted-foreground">
                    {environment.allowAutoLogin ? "允许" : "禁止"} /{" "}
                    {environment.requireApproval ? "需审批" : "免审批"}
                  </td>
                  {canWrite ? (
                    <td className="px-2.5 py-2 text-right">
                      <div className="flex flex-wrap items-center justify-end gap-1.5">
                        <button
                          type="button"
                          onClick={() => startEdit(environment)}
                          aria-label={`编辑 ${environment.name}`}
                          className={rowActionClass}
                        >
                          <Pencil className="h-3 w-3" aria-hidden="true" />
                          编辑
                        </button>
                        {confirmingDeleteId === environment.id ? (
                          <>
                            <button
                              type="button"
                              onClick={() => void handleDelete(environment)}
                              disabled={deletingId === environment.id}
                              aria-label={`确认删除环境 ${environment.name}`}
                              className={rowDangerActionClass}
                            >
                              <Trash2 className="h-3 w-3" aria-hidden="true" />
                              确认删除
                            </button>
                            <button
                              type="button"
                              onClick={() => setConfirmingDeleteId(null)}
                              disabled={deletingId === environment.id}
                              aria-label={`取消删除环境 ${environment.name}`}
                              className={rowActionClass}
                            >
                              取消
                            </button>
                          </>
                        ) : (
                          <button
                            type="button"
                            onClick={() => {
                              setConfirmingDeleteId(environment.id)
                              setError("")
                              setNotice("")
                            }}
                            disabled={deletingId === environment.id}
                            aria-label={`删除环境 ${environment.name}`}
                            className={rowActionClass}
                          >
                            <Trash2 className="h-3 w-3" aria-hidden="true" />
                            删除
                          </button>
                        )}
                      </div>
                    </td>
                  ) : null}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  )
}
