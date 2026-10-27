"use client"

import { KeyRound, Pencil, Plus, RefreshCw, Users } from "lucide-react"
import { type FormEvent, useCallback, useEffect, useState } from "react"

import { EmptyState } from "@/components/ui/EmptyState"
import { StatusBadge } from "@/components/ui/StatusBadge"
import { ApiError } from "@/lib/api-client"

import { AccessForm, type AccessWriteInput } from "./AccessForm"
import {
  createAccess,
  createAccount,
  listAccesses,
  listAccounts,
  listSystems,
  updateAccess,
  updateAccount,
} from "./api"
import type { AccessDTO, AccountDTO, SystemDTO } from "./types"

const ACCOUNT_PAGE_LIMIT = 100
const ACCESS_PAGE_LIMIT = 100
const SYSTEM_PAGE_LIMIT = 100
const KEY_PATTERN = /^[a-z0-9][a-z0-9_-]{1,63}$/

const ACCOUNT_KIND_OPTIONS = [
  { value: "human", label: "人工" },
  { value: "service", label: "服务" },
]

const AUTH_TYPE_LABELS: Record<string, string> = {
  password: "密码",
  sso: "SSO",
  token: "Token",
  apikey: "API Key",
  basic: "Basic",
  manual: "手工",
}

function authTypeLabel(authType: string): string {
  return AUTH_TYPE_LABELS[authType] ?? authType
}

function accountKindLabel(kind: string): string {
  return ACCOUNT_KIND_OPTIONS.find((option) => option.value === kind)?.label ?? kind
}

/** Human labels for the closed `details.reason` set of spec §7.1/§7.4. */
const ERROR_REASON_LABELS: Record<string, string> = {
  duplicate_key: "标识已存在",
  has_dependencies: "存在依赖，无法删除",
}

function describeError(error: unknown): string {
  if (!(error instanceof ApiError)) {
    return error instanceof Error ? error.message : "未知错误"
  }
  if (error.status === 0) return "无法连接 API 服务，请确认后端已启动。"
  if (error.status === 403) return "当前账户没有管理员权限。"
  const reason = typeof error.details.reason === "string" ? error.details.reason : ""
  const label = reason === "" ? "" : ERROR_REASON_LABELS[reason]
  if (label) return `${label}（${reason}）`
  if (error.status === 422 && reason !== "") return `保存失败（${reason}）：${error.message}`
  if (reason !== "") return `${error.message}（${reason}）`
  return error.message
}

type AccountFormState = {
  key: string
  label: string
  username: string
  kind: string
  notes: string
}

const EMPTY_ACCOUNT_FORM: AccountFormState = {
  key: "",
  label: "",
  username: "",
  kind: "human",
  notes: "",
}

function toFormState(account: AccountDTO): AccountFormState {
  return {
    key: account.key,
    label: account.label,
    username: account.username,
    kind: account.kind,
    notes: account.notes,
  }
}

function validateForm(form: AccountFormState, editing: boolean): string {
  if (!editing && !KEY_PATTERN.test(form.key.trim())) {
    return "账号标识需匹配 ^[a-z0-9][a-z0-9_-]{1,63}$。"
  }
  if (form.label.trim() === "") return "请输入账号标签。"
  return ""
}

const inputClass =
  "mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-60"
const labelClass = "text-xs font-medium text-muted-foreground"
const primaryButtonClass =
  "inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
const secondaryButtonClass =
  "inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"

export type AccountsPanelProps = {
  canWrite: boolean
}

/**
 * Accounts and their per-system Access rows. The account list selects which
 * accesses are shown; admins can create/edit accounts and accesses. Access
 * credentials are write-only (spec §7.4): the table only shows whether one is
 * stored and the shared AccessForm never echoes it.
 */
export function AccountsPanel({ canWrite }: AccountsPanelProps) {
  const [accounts, setAccounts] = useState<AccountDTO[]>([])
  const [systems, setSystems] = useState<SystemDTO[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState("")
  const [notice, setNotice] = useState("")
  const [selectedAccountId, setSelectedAccountId] = useState<number | null>(null)

  const [formOpen, setFormOpen] = useState(false)
  const [editingId, setEditingId] = useState<number | null>(null)
  const [form, setForm] = useState<AccountFormState>(EMPTY_ACCOUNT_FORM)
  const [saving, setSaving] = useState(false)

  const [accesses, setAccesses] = useState<AccessDTO[]>([])
  const [accessLoading, setAccessLoading] = useState(false)
  const [accessError, setAccessError] = useState("")
  const [accessNotice, setAccessNotice] = useState("")
  const [accessFormOpen, setAccessFormOpen] = useState(false)
  const [editingAccess, setEditingAccess] = useState<AccessDTO | null>(null)

  const loadAccounts = useCallback(async () => {
    setLoading(true)
    setError("")
    try {
      const page = await listAccounts({ limit: ACCOUNT_PAGE_LIMIT })
      setAccounts(page.items)
    } catch (caught) {
      setError(describeError(caught))
    } finally {
      setLoading(false)
    }
  }, [])

  const loadSystems = useCallback(async () => {
    try {
      const page = await listSystems({ limit: SYSTEM_PAGE_LIMIT })
      setSystems(page.items)
    } catch (caught) {
      setError(describeError(caught))
    }
  }, [])

  const loadAccesses = useCallback(async (accountId: number) => {
    setAccessLoading(true)
    setAccessError("")
    try {
      setAccesses(await listAccesses(accountId, { limit: ACCESS_PAGE_LIMIT }))
    } catch (caught) {
      setAccessError(describeError(caught))
    } finally {
      setAccessLoading(false)
    }
  }, [])

  useEffect(() => {
    void loadAccounts()
    void loadSystems()
  }, [loadAccounts, loadSystems])

  useEffect(() => {
    if (selectedAccountId === null) {
      setAccesses([])
      return
    }
    void loadAccesses(selectedAccountId)
  }, [selectedAccountId, loadAccesses])

  const selectedAccount =
    selectedAccountId === null
      ? null
      : (accounts.find((account) => account.id === selectedAccountId) ?? null)

  function systemName(systemId: number): string {
    return systems.find((system) => system.id === systemId)?.name ?? `#${systemId}`
  }

  function selectAccount(account: AccountDTO) {
    setSelectedAccountId(account.id)
    setAccessFormOpen(false)
    setEditingAccess(null)
    setAccessError("")
    setAccessNotice("")
  }

  function updateForm<K extends keyof AccountFormState>(field: K, value: AccountFormState[K]) {
    setForm((previous) => ({ ...previous, [field]: value }))
  }

  function startCreate() {
    setEditingId(null)
    setForm(EMPTY_ACCOUNT_FORM)
    setFormOpen(true)
    setError("")
    setNotice("")
  }

  function startEdit(account: AccountDTO) {
    setEditingId(account.id)
    setForm(toFormState(account))
    setFormOpen(true)
    setError("")
    setNotice("")
  }

  function cancelForm() {
    setFormOpen(false)
    setEditingId(null)
    setError("")
  }

  async function handleAccountSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const editing = editingId !== null
    const validationError = validateForm(form, editing)
    if (validationError !== "") {
      setError(validationError)
      return
    }

    const payload = {
      label: form.label.trim(),
      username: form.username.trim(),
      kind: form.kind,
      notes: form.notes.trim(),
    }

    setSaving(true)
    setError("")
    setNotice("")
    try {
      if (editingId !== null) {
        const updated = await updateAccount(editingId, payload)
        setNotice(`已更新账号 ${updated.label}。`)
      } else {
        const created = await createAccount({ key: form.key.trim(), ...payload })
        setNotice(`已创建账号 ${created.label}。`)
        setSelectedAccountId(created.id)
      }
      setFormOpen(false)
      setEditingId(null)
      await loadAccounts()
    } catch (caught) {
      setError(describeError(caught))
    } finally {
      setSaving(false)
    }
  }

  function startCreateAccess() {
    setEditingAccess(null)
    setAccessFormOpen(true)
    setAccessError("")
    setAccessNotice("")
  }

  function startEditAccess(access: AccessDTO) {
    setEditingAccess(access)
    setAccessFormOpen(true)
    setAccessError("")
    setAccessNotice("")
  }

  function cancelAccessForm() {
    setAccessFormOpen(false)
    setEditingAccess(null)
  }

  async function handleAccessSubmit(input: unknown) {
    if (selectedAccount === null) return
    const payload = input as AccessWriteInput
    setAccessError("")
    setAccessNotice("")
    if (editingAccess !== null) {
      await updateAccess(editingAccess.id, payload)
      setAccessNotice(`已更新 ${systemName(payload.systemId)} 的 Access。`)
    } else {
      await createAccess(selectedAccount.id, payload)
      setAccessNotice(`已为 ${systemName(payload.systemId)} 创建 Access。`)
    }
    setAccessFormOpen(false)
    setEditingAccess(null)
    await loadAccesses(selectedAccount.id)
  }

  return (
    <section
      aria-labelledby="accounts-panel-title"
      className="space-y-3 rounded-2xl border border-border bg-card p-3 shadow-sm"
    >
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <h2 id="accounts-panel-title" className="flex items-center gap-2 text-base font-semibold">
            <Users className="h-4 w-4 text-primary" aria-hidden="true" />
            账号与访问
          </h2>
          <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
            账号是目标系统的身份；每个 Access 配置账号在某个系统上的认证方式与凭据。
          </p>
        </div>
        <div className="flex items-center gap-1.5">
          <button
            type="button"
            onClick={() => void loadAccounts()}
            disabled={loading}
            aria-label="刷新账号"
            className={secondaryButtonClass}
          >
            <RefreshCw className={`h-4 w-4 ${loading ? "animate-spin" : ""}`} aria-hidden="true" />
          </button>
          {canWrite ? (
            <button type="button" onClick={startCreate} className={primaryButtonClass}>
              <Plus className="h-4 w-4" aria-hidden="true" />
              新建账号
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
          onSubmit={(event) => void handleAccountSubmit(event)}
          className="space-y-2 rounded-xl border border-border bg-muted/30 p-3"
        >
          <h3 className="text-sm font-semibold">{editingId === null ? "新建账号" : "编辑账号"}</h3>
          <div className="grid gap-2 sm:grid-cols-2">
            <div>
              <label htmlFor="account-key" className={labelClass}>
                账号标识 key
              </label>
              <input
                id="account-key"
                value={form.key}
                onChange={(event) => updateForm("key", event.target.value)}
                disabled={editingId !== null}
                autoComplete="off"
                placeholder="例如 qa01"
                className={inputClass}
              />
            </div>
            <div>
              <label htmlFor="account-label" className={labelClass}>
                标签
              </label>
              <input
                id="account-label"
                value={form.label}
                onChange={(event) => updateForm("label", event.target.value)}
                autoComplete="off"
                placeholder="例如 QA 01"
                className={inputClass}
              />
            </div>
            <div>
              <label htmlFor="account-username" className={labelClass}>
                用户名
              </label>
              <input
                id="account-username"
                value={form.username}
                onChange={(event) => updateForm("username", event.target.value)}
                autoComplete="off"
                placeholder="例如 qa01"
                className={inputClass}
              />
            </div>
            <div>
              <label htmlFor="account-kind" className={labelClass}>
                类型
              </label>
              <select
                id="account-kind"
                value={form.kind}
                onChange={(event) => updateForm("kind", event.target.value)}
                className={inputClass}
              >
                {ACCOUNT_KIND_OPTIONS.map((option) => (
                  <option key={option.value} value={option.value}>
                    {option.label}
                  </option>
                ))}
              </select>
            </div>
            <div className="sm:col-span-2">
              <label htmlFor="account-notes" className={labelClass}>
                备注
              </label>
              <input
                id="account-notes"
                value={form.notes}
                onChange={(event) => updateForm("notes", event.target.value)}
                autoComplete="off"
                className={inputClass}
              />
            </div>
          </div>
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
              {saving ? "保存中…" : "保存账号"}
            </button>
          </div>
        </form>
      ) : null}

      {loading && accounts.length === 0 ? (
        <p className="rounded-xl border border-dashed border-border px-4 py-6 text-center text-sm text-muted-foreground">
          正在加载账号…
        </p>
      ) : accounts.length === 0 ? (
        <EmptyState
          icon={Users}
          title="暂无账号"
          description={canWrite ? "使用“新建账号”创建第一个账号。" : "管理员尚未创建任何账号。"}
        />
      ) : (
        <div className="overflow-x-auto rounded-xl border border-border">
          <table className="min-w-full divide-y divide-border text-sm">
            <caption className="sr-only">账号列表</caption>
            <thead className="bg-muted/50 text-left text-[11px] uppercase tracking-wide text-muted-foreground">
              <tr>
                <th scope="col" className="px-2.5 py-2 font-medium">
                  账号
                </th>
                <th scope="col" className="px-2.5 py-2 font-medium">
                  用户名
                </th>
                <th scope="col" className="px-2.5 py-2 font-medium">
                  类型
                </th>
                <th scope="col" className="px-2.5 py-2 font-medium">
                  状态
                </th>
                {canWrite ? (
                  <th scope="col" className="px-2.5 py-2 text-right font-medium">
                    操作
                  </th>
                ) : null}
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {accounts.map((account) => {
                const active = account.id === selectedAccountId
                return (
                  <tr key={account.id} className={active ? "bg-primary/5" : undefined}>
                    <td className="px-2.5 py-2">
                      <button
                        type="button"
                        onClick={() => selectAccount(account)}
                        aria-current={active ? "true" : undefined}
                        className="text-left font-medium focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                      >
                        {account.label}
                      </button>
                      <span className="block font-mono text-[11px] text-muted-foreground">
                        {account.key}
                      </span>
                    </td>
                    <td className="px-2.5 py-2 text-muted-foreground">{account.username}</td>
                    <td className="px-2.5 py-2 text-muted-foreground">
                      {accountKindLabel(account.kind)}
                    </td>
                    <td className="px-2.5 py-2">
                      <StatusBadge variant={account.status === "active" ? "success" : "neutral"}>
                        {account.status === "active" ? "活跃" : "已停用"}
                      </StatusBadge>
                    </td>
                    {canWrite ? (
                      <td className="px-2.5 py-2 text-right">
                        <button
                          type="button"
                          onClick={() => startEdit(account)}
                          aria-label={`编辑账号 ${account.label}`}
                          className="inline-flex items-center gap-1 rounded-lg border border-border px-2 py-1 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                        >
                          <Pencil className="h-3 w-3" aria-hidden="true" />
                          编辑
                        </button>
                      </td>
                    ) : null}
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}

      {selectedAccount === null ? (
        accounts.length === 0 ? null : (
          <p className="rounded-xl border border-dashed border-border px-4 py-4 text-center text-sm text-muted-foreground">
            选择一个账号查看其 Access 配置。
          </p>
        )
      ) : (
        <section
          aria-labelledby="accesses-panel-title"
          className="space-y-3 rounded-2xl border border-border bg-muted/20 p-3"
        >
          <div className="flex flex-wrap items-start justify-between gap-2">
            <h3 id="accesses-panel-title" className="flex items-center gap-2 text-sm font-semibold">
              <KeyRound className="h-4 w-4 text-primary" aria-hidden="true" />
              Access：{selectedAccount.label}
            </h3>
            <div className="flex items-center gap-1.5">
              <button
                type="button"
                onClick={() => void loadAccesses(selectedAccount.id)}
                disabled={accessLoading}
                aria-label="刷新 Access"
                className={secondaryButtonClass}
              >
                <RefreshCw
                  className={`h-4 w-4 ${accessLoading ? "animate-spin" : ""}`}
                  aria-hidden="true"
                />
              </button>
              {canWrite ? (
                <button type="button" onClick={startCreateAccess} className={primaryButtonClass}>
                  <Plus className="h-4 w-4" aria-hidden="true" />
                  新建 Access
                </button>
              ) : null}
            </div>
          </div>

          {accessError ? (
            <p
              role="alert"
              className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
            >
              {accessError}
            </p>
          ) : null}
          {accessNotice ? (
            <p
              role="status"
              className="rounded-xl border border-emerald-500/25 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-700 dark:text-emerald-300"
            >
              {accessNotice}
            </p>
          ) : null}

          {accessFormOpen && canWrite ? (
            <AccessForm
              key={editingAccess === null ? "new" : editingAccess.id}
              accountId={selectedAccount.id}
              systems={systems}
              access={editingAccess ?? undefined}
              canWrite={canWrite}
              onSubmit={handleAccessSubmit}
              onCancel={cancelAccessForm}
            />
          ) : null}

          {accessLoading && accesses.length === 0 ? (
            <p className="rounded-xl border border-dashed border-border px-4 py-6 text-center text-sm text-muted-foreground">
              正在加载 Access…
            </p>
          ) : accesses.length === 0 ? (
            <EmptyState
              icon={KeyRound}
              title="暂无 Access"
              description={
                canWrite ? "使用“新建 Access”为该账号配置系统访问。" : "该账号尚未配置 Access。"
              }
            />
          ) : (
            <div className="overflow-x-auto rounded-xl border border-border">
              <table className="min-w-full divide-y divide-border text-sm">
                <caption className="sr-only">Access 列表</caption>
                <thead className="bg-muted/50 text-left text-[11px] uppercase tracking-wide text-muted-foreground">
                  <tr>
                    <th scope="col" className="px-2.5 py-2 font-medium">
                      系统
                    </th>
                    <th scope="col" className="px-2.5 py-2 font-medium">
                      认证
                    </th>
                    <th scope="col" className="px-2.5 py-2 font-medium">
                      状态
                    </th>
                    <th scope="col" className="px-2.5 py-2 font-medium">
                      并发
                    </th>
                    <th scope="col" className="px-2.5 py-2 font-medium">
                      TTL
                    </th>
                    <th scope="col" className="px-2.5 py-2 font-medium">
                      凭据
                    </th>
                    {canWrite ? (
                      <th scope="col" className="px-2.5 py-2 text-right font-medium">
                        操作
                      </th>
                    ) : null}
                  </tr>
                </thead>
                <tbody className="divide-y divide-border">
                  {accesses.map((access) => (
                    <tr key={access.id}>
                      <td className="px-2.5 py-2">{systemName(access.systemId)}</td>
                      <td className="px-2.5 py-2 text-muted-foreground">
                        {authTypeLabel(access.authType)}
                      </td>
                      <td className="px-2.5 py-2">
                        <StatusBadge variant={access.enabled ? "success" : "neutral"}>
                          {access.enabled ? "启用" : "停用"}
                        </StatusBadge>
                      </td>
                      <td className="px-2.5 py-2 text-muted-foreground">
                        {access.concurrencyLimit}
                      </td>
                      <td className="px-2.5 py-2 text-muted-foreground">
                        {access.sessionTtlSeconds === null
                          ? "继承系统"
                          : `${access.sessionTtlSeconds} 秒`}
                      </td>
                      <td className="px-2.5 py-2">
                        {access.credentialSet ? (
                          <StatusBadge variant="info">已配置</StatusBadge>
                        ) : (
                          <span className="text-xs text-muted-foreground">未配置</span>
                        )}
                      </td>
                      {canWrite ? (
                        <td className="px-2.5 py-2 text-right">
                          <button
                            type="button"
                            onClick={() => startEditAccess(access)}
                            aria-label={`编辑 Access ${access.id}`}
                            className="inline-flex items-center gap-1 rounded-lg border border-border px-2 py-1 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                          >
                            <Pencil className="h-3 w-3" aria-hidden="true" />
                            编辑
                          </button>
                        </td>
                      ) : null}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </section>
      )}
    </section>
  )
}
