"use client"

import { Ban, KeyRound, RefreshCw, UserPlus, Users } from "lucide-react"
import { type FormEvent, useCallback, useEffect, useState } from "react"

import { useAuth } from "@/features/auth/AuthProvider"
import { EmptyState } from "@/features/shell/EmptyState"
import { StatusBadge } from "@/features/shell/StatusBadge"
import { ApiError, apiGet, apiPatch, apiPost } from "@/lib/api-client"
import type { AuthUser } from "@/lib/auth-storage"

const MIN_PASSWORD_LENGTH = 10
const MAX_PASSWORD_LENGTH = 128

function describeError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 0) return "无法连接 API 服务，请确认后端已启动。"
    if (error.status === 403) return "当前账户没有管理员权限。"
    return error.message
  }
  return error instanceof Error ? error.message : "未知错误"
}

function validatePassword(password: string): string | null {
  if (password.length < MIN_PASSWORD_LENGTH) {
    return `密码至少需要 ${MIN_PASSWORD_LENGTH} 个字符。`
  }
  if (password.length > MAX_PASSWORD_LENGTH) {
    return `密码不能超过 ${MAX_PASSWORD_LENGTH} 个字符。`
  }
  return null
}

/**
 * Admin user management: list, create, disable and reset password. The panel
 * renders only the public account projection (id/username/role/status); no
 * password, hash or token is ever displayed, stored in component state after a
 * submit, or written to a log.
 */
export function UsersPanel() {
  const { user: currentUser } = useAuth()
  const [users, setUsers] = useState<AuthUser[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState("")
  const [notice, setNotice] = useState("")

  const [newUsername, setNewUsername] = useState("")
  const [newPassword, setNewPassword] = useState("")
  const [creating, setCreating] = useState(false)

  const [busyUserId, setBusyUserId] = useState<number | null>(null)
  const [confirmingDisableId, setConfirmingDisableId] = useState<number | null>(null)

  const [resettingUserId, setResettingUserId] = useState<number | null>(null)
  const [resetPassword, setResetPassword] = useState("")
  const [resetting, setResetting] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    setError("")
    try {
      // Ask for the backend's maximum page size. The default is 20 and this
      // panel has no pagination, so an unqualified request silently hides
      // every user after the first page.
      const list = await apiGet<AuthUser[]>("/api/users?limit=100")
      setUsers(Array.isArray(list) ? list : [])
    } catch (caught) {
      setError(describeError(caught))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  async function handleCreate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setError("")
    setNotice("")

    const username = newUsername.trim()
    if (username === "") {
      setError("请输入用户名。")
      return
    }
    const passwordError = validatePassword(newPassword)
    if (passwordError) {
      setError(passwordError)
      return
    }

    setCreating(true)
    try {
      const created = await apiPost<AuthUser>("/api/users", { username, password: newPassword })
      setUsers((previous) =>
        [...previous.filter((item) => item.id !== created.id), created].sort(
          (left, right) => left.id - right.id,
        ),
      )
      setNewUsername("")
      setNewPassword("")
      setNotice(`已创建用户 ${created.username}。`)
    } catch (caught) {
      setError(describeError(caught))
    } finally {
      setCreating(false)
    }
  }

  async function handleDisable(user: AuthUser) {
    setConfirmingDisableId(null)
    setError("")
    setNotice("")
    setBusyUserId(user.id)
    try {
      const updated = await apiPatch<AuthUser>(`/api/users/${user.id}`, { status: "disabled" })
      setUsers((previous) => previous.map((item) => (item.id === updated.id ? updated : item)))
      setNotice(`已禁用用户 ${updated.username}。`)
    } catch (caught) {
      setError(describeError(caught))
    } finally {
      setBusyUserId(null)
    }
  }

  function startResetPassword(user: AuthUser) {
    setResettingUserId(user.id)
    setResetPassword("")
    setError("")
    setNotice("")
  }

  function cancelResetPassword() {
    setResettingUserId(null)
    setResetPassword("")
  }

  async function handleResetPassword(event: FormEvent<HTMLFormElement>, userId: number) {
    event.preventDefault()
    setError("")
    setNotice("")
    const passwordError = validatePassword(resetPassword)
    if (passwordError) {
      setError(passwordError)
      return
    }
    setResetting(true)
    try {
      await apiPost<void>(`/api/users/${userId}/reset-password`, {
        new_password: resetPassword,
      })
      setResettingUserId(null)
      setResetPassword("")
      setNotice("密码已重置，该用户需要使用新密码重新登录。")
    } catch (caught) {
      setError(describeError(caught))
    } finally {
      setResetting(false)
    }
  }

  return (
    <section className="space-y-5" aria-labelledby="users-panel-title">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 id="users-panel-title" className="flex items-center gap-2 text-base font-semibold">
            <Users className="h-4 w-4 text-primary" aria-hidden="true" />
            用户管理
          </h2>
          <p className="mt-1 max-w-2xl text-sm leading-relaxed text-muted-foreground">
            创建普通用户、禁用账户、重置密码。接口不会返回密码哈希，界面也不会展示密码。
          </p>
        </div>
        <button
          type="button"
          onClick={() => void load()}
          disabled={loading}
          className="inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <RefreshCw className={`h-4 w-4 ${loading ? "animate-spin" : ""}`} aria-hidden="true" />
          刷新
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
      {notice ? (
        <p
          role="status"
          className="rounded-xl border border-emerald-500/25 bg-emerald-500/10 px-3 py-2 text-sm text-emerald-700 dark:text-emerald-300"
        >
          {notice}
        </p>
      ) : null}

      <form
        onSubmit={(event) => void handleCreate(event)}
        className="grid gap-3 rounded-xl border border-border bg-muted/30 p-4 sm:grid-cols-[1fr_1fr_auto] sm:items-end"
      >
        <div>
          <label htmlFor="new-user-username" className="text-xs font-medium text-muted-foreground">
            用户名
          </label>
          <input
            id="new-user-username"
            value={newUsername}
            onChange={(event) => setNewUsername(event.target.value)}
            autoComplete="off"
            placeholder="例如 qa01"
            className="mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
          />
        </div>
        <div>
          <label htmlFor="new-user-password" className="text-xs font-medium text-muted-foreground">
            初始密码
          </label>
          <input
            id="new-user-password"
            type="password"
            value={newPassword}
            onChange={(event) => setNewPassword(event.target.value)}
            autoComplete="new-password"
            placeholder="至少 10 个字符"
            className="mt-1 w-full rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
          />
        </div>
        <button
          type="submit"
          disabled={creating}
          className="inline-flex items-center justify-center gap-1.5 rounded-lg bg-primary px-3.5 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <UserPlus className="h-4 w-4" aria-hidden="true" />
          {creating ? "创建中…" : "创建用户"}
        </button>
      </form>

      {loading && users.length === 0 ? (
        <p className="rounded-xl border border-dashed border-border px-4 py-8 text-center text-sm text-muted-foreground">
          正在加载用户…
        </p>
      ) : users.length === 0 ? (
        <EmptyState
          icon={Users}
          title="暂无用户"
          description="使用上方的表单创建第一个普通用户。"
        />
      ) : (
        <div className="overflow-x-auto rounded-xl border border-border">
          <table className="min-w-full divide-y divide-border text-sm">
            <caption className="sr-only">用户账户列表</caption>
            <thead className="bg-muted/50 text-left text-[11px] uppercase tracking-wide text-muted-foreground">
              <tr>
                <th scope="col" className="px-3 py-2 font-medium">
                  用户名
                </th>
                <th scope="col" className="px-3 py-2 font-medium">
                  角色
                </th>
                <th scope="col" className="px-3 py-2 font-medium">
                  状态
                </th>
                <th scope="col" className="px-3 py-2 text-right font-medium">
                  操作
                </th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {users.map((user) => {
                const isSelf = user.id === currentUser?.id
                const isResetting = resettingUserId === user.id
                return (
                  <tr key={user.id}>
                    <td className="px-3 py-2">
                      <span className="font-medium">{user.username}</span>
                      {isSelf ? (
                        <span className="ml-2 text-[11px] text-muted-foreground">当前账户</span>
                      ) : null}
                    </td>
                    <td className="px-3 py-2 text-muted-foreground">
                      {user.role === "admin" ? "管理员" : "普通用户"}
                    </td>
                    <td className="px-3 py-2">
                      <StatusBadge variant={user.status === "active" ? "success" : "danger"}>
                        {user.status === "active" ? "活跃" : "已禁用"}
                      </StatusBadge>
                    </td>
                    <td className="px-3 py-2">
                      <div className="flex flex-wrap items-center justify-end gap-1.5">
                        {user.status === "active" && !isSelf ? (
                          confirmingDisableId === user.id ? (
                            <>
                              <button
                                type="button"
                                onClick={() => void handleDisable(user)}
                                disabled={busyUserId === user.id}
                                className="rounded-lg border border-destructive/40 bg-destructive/10 px-2.5 py-1.5 text-xs font-medium text-destructive transition-colors hover:bg-destructive/20 disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                              >
                                确认禁用
                              </button>
                              <button
                                type="button"
                                onClick={() => setConfirmingDisableId(null)}
                                className="rounded-lg border border-border px-2.5 py-1.5 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                              >
                                取消
                              </button>
                            </>
                          ) : (
                            <button
                              type="button"
                              onClick={() => setConfirmingDisableId(user.id)}
                              disabled={busyUserId === user.id}
                              className="inline-flex items-center gap-1 rounded-lg border border-border px-2.5 py-1.5 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                            >
                              <Ban className="h-3.5 w-3.5" aria-hidden="true" />
                              禁用
                            </button>
                          )
                        ) : null}
                        <button
                          type="button"
                          onClick={() => startResetPassword(user)}
                          className="inline-flex items-center gap-1 rounded-lg border border-border px-2.5 py-1.5 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                        >
                          <KeyRound className="h-3.5 w-3.5" aria-hidden="true" />
                          重置密码
                        </button>
                      </div>
                      {isResetting ? (
                        <form
                          onSubmit={(event) => void handleResetPassword(event, user.id)}
                          className="mt-2 flex flex-wrap items-center justify-end gap-1.5"
                        >
                          <label htmlFor={`reset-password-${user.id}`} className="sr-only">
                            新密码
                          </label>
                          <input
                            id={`reset-password-${user.id}`}
                            type="password"
                            value={resetPassword}
                            onChange={(event) => setResetPassword(event.target.value)}
                            autoComplete="new-password"
                            placeholder="新密码（至少 10 个字符）"
                            className="w-52 rounded-lg border border-border bg-input px-2.5 py-1.5 text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring"
                          />
                          <button
                            type="submit"
                            disabled={resetting}
                            className="rounded-lg bg-primary px-2.5 py-1.5 text-xs font-medium text-primary-foreground disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                          >
                            {resetting ? "重置中…" : "确认重置"}
                          </button>
                          <button
                            type="button"
                            onClick={cancelResetPassword}
                            disabled={resetting}
                            className="rounded-lg border border-border px-2.5 py-1.5 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                          >
                            取消
                          </button>
                        </form>
                      ) : null}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
    </section>
  )
}
