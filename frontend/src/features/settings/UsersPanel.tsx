"use client"

import type { FormEvent } from "react"
import { useEffect, useState } from "react"

import { Alert } from "@/components/ui/Alert"
import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { Dialog } from "@/components/ui/Dialog"
import { ErrorState, InlineError } from "@/components/ui/ErrorState"
import { Field } from "@/components/ui/Field"
import { Input } from "@/components/ui/Input"
import { LoadingCard } from "@/components/ui/Spinner"
import { Table, type TableColumn } from "@/components/ui/Table"
import { ApiError } from "@/lib/api-client"
import { formatDateTime } from "@/lib/format"

import {
  createUser,
  fetchUsers,
  resetUserPassword,
  type User,
  type UserRole,
  type UserStatus,
  updateUser,
} from "./api"

const PAGE_SIZE = 20

const ROLE_LABELS: Record<UserRole, string> = {
  admin: "管理员",
  user: "普通用户",
}

const STATUS_LABELS: Record<UserStatus, string> = {
  active: "启用",
  disabled: "已禁用",
}

function errorText(caught: unknown, fallback: string): string {
  return caught instanceof ApiError ? caught.message : fallback
}

type CreateUserDialogProps = {
  onClose: () => void
  onSaved: (user: User) => void
}

function CreateUserDialog({ onClose, onSaved }: CreateUserDialogProps) {
  const [username, setUsername] = useState("")
  const [displayName, setDisplayName] = useState("")
  const [password, setPassword] = useState("")
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState(false)

  async function handleSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault()
    setError(null)

    const trimmedUsername = username.trim()
    if (trimmedUsername.length === 0) {
      setError("请输入用户名")
      return
    }
    if (!/^[a-zA-Z0-9_.-]{3,64}$/.test(trimmedUsername)) {
      setError("用户名需为 3–64 位字母、数字或 _ . -")
      return
    }
    const trimmedDisplayName = displayName.trim()
    if (trimmedDisplayName.length === 0) {
      setError("请输入显示名")
      return
    }
    if (password.length < 8) {
      setError("密码至少 8 个字符")
      return
    }
    if (password.length > 128) {
      setError("密码不能超过 128 个字符")
      return
    }

    setPending(true)
    try {
      const created = await createUser({
        username: trimmedUsername,
        display_name: trimmedDisplayName,
        password,
      })
      onSaved(created)
    } catch (caught) {
      setError(errorText(caught, "创建用户失败，请稍后重试"))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog
      open
      onClose={onClose}
      title="创建用户"
      description="只能创建普通用户（role=user）；用户名创建后不可修改。"
    >
      <form onSubmit={handleSubmit} className="flex flex-col gap-4" noValidate>
        <Field id="create-username" label="用户名" required hint="3–64 位字母、数字或 _ . -">
          <Input
            id="create-username"
            autoComplete="off"
            value={username}
            onChange={(event) => setUsername(event.target.value)}
          />
        </Field>
        <Field id="create-display-name" label="显示名" required>
          <Input
            id="create-display-name"
            autoComplete="off"
            value={displayName}
            onChange={(event) => setDisplayName(event.target.value)}
          />
        </Field>
        <Field id="create-password" label="密码" required hint="8–128 个字符">
          <Input
            id="create-password"
            type="password"
            autoComplete="new-password"
            value={password}
            onChange={(event) => setPassword(event.target.value)}
          />
        </Field>

        {error ? <Alert tone="danger">{error}</Alert> : null}

        <div className="flex items-center justify-end gap-2 pt-1">
          <Button variant="secondary" onClick={onClose} disabled={pending}>
            取消
          </Button>
          <Button type="submit" disabled={pending}>
            {pending ? "创建中…" : "创建"}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}

type ResetPasswordDialogProps = {
  target: User
  onClose: () => void
  onDone: (message: string) => void
}

function ResetPasswordDialog({ target, onClose, onDone }: ResetPasswordDialogProps) {
  const [password, setPassword] = useState("")
  const [confirm, setConfirm] = useState("")
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState(false)

  async function handleSubmit(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault()
    setError(null)

    if (password.length === 0) {
      setError("请输入新密码")
      return
    }
    if (password.length < 8) {
      setError("密码至少 8 个字符")
      return
    }
    if (password.length > 128) {
      setError("密码不能超过 128 个字符")
      return
    }
    if (password !== confirm) {
      setError("两次输入的密码不一致")
      return
    }

    setPending(true)
    try {
      await resetUserPassword(target.id, password)
      onDone(`已重置 ${target.username} 的密码，该用户需使用新密码重新登录。`)
    } catch (caught) {
      setError(errorText(caught, "重置密码失败，请稍后重试"))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog
      open
      onClose={onClose}
      title="重置密码"
      description={`为「${target.username}」设置新密码；该用户的所有令牌会立即失效。`}
    >
      <form onSubmit={handleSubmit} className="flex flex-col gap-4" noValidate>
        <Field id="reset-password" label="新密码" required hint="8–128 个字符">
          <Input
            id="reset-password"
            type="password"
            autoComplete="new-password"
            value={password}
            onChange={(event) => setPassword(event.target.value)}
          />
        </Field>
        <Field id="reset-password-confirm" label="确认新密码" required>
          <Input
            id="reset-password-confirm"
            type="password"
            autoComplete="new-password"
            value={confirm}
            onChange={(event) => setConfirm(event.target.value)}
          />
        </Field>

        {error ? <Alert tone="danger">{error}</Alert> : null}

        <div className="flex items-center justify-end gap-2 pt-1">
          <Button variant="secondary" onClick={onClose} disabled={pending}>
            取消
          </Button>
          <Button type="submit" variant="danger" disabled={pending}>
            {pending ? "重置中…" : "确认重置"}
          </Button>
        </div>
      </form>
    </Dialog>
  )
}

/** Admin user directory: create, enable/disable and reset passwords. */
export function UsersPanel() {
  const [users, setUsers] = useState<User[] | null>(null)
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState<unknown>(null)
  const [reloadKey, setReloadKey] = useState(0)
  const [nextCursor, setNextCursor] = useState("")
  const [hasMore, setHasMore] = useState(false)
  const [loadingMore, setLoadingMore] = useState(false)
  const [createOpen, setCreateOpen] = useState(false)
  const [resetTarget, setResetTarget] = useState<User | null>(null)
  const [busyId, setBusyId] = useState<string | null>(null)
  const [panelError, setPanelError] = useState<unknown>(null)
  const [notice, setNotice] = useState<string | null>(null)

  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadKey retriggers reload
  useEffect(() => {
    let active = true
    setLoading(true)
    setLoadError(null)
    fetchUsers({ limit: PAGE_SIZE })
      .then((page) => {
        if (!active) return
        setUsers(page.items)
        setNextCursor(page.nextCursor)
        setHasMore(page.hasMore)
      })
      .catch((caught: unknown) => {
        if (active) setLoadError(caught)
      })
      .finally(() => {
        if (active) setLoading(false)
      })
    return () => {
      active = false
    }
  }, [reloadKey])

  async function loadMore(): Promise<void> {
    if (nextCursor.length === 0 || loadingMore) return
    setLoadingMore(true)
    setPanelError(null)
    try {
      const page = await fetchUsers({ cursor: nextCursor, limit: PAGE_SIZE })
      setUsers((current) => [...(current ?? []), ...page.items])
      setNextCursor(page.nextCursor)
      setHasMore(page.hasMore)
    } catch (caught) {
      setPanelError(caught)
    } finally {
      setLoadingMore(false)
    }
  }

  async function toggleStatus(user: User): Promise<void> {
    const nextStatus: UserStatus = user.status === "active" ? "disabled" : "active"
    setBusyId(user.id)
    setPanelError(null)
    setNotice(null)
    try {
      const updated = await updateUser(user.id, { status: nextStatus })
      setUsers((current) =>
        current ? current.map((item) => (item.id === updated.id ? updated : item)) : current,
      )
      setNotice(
        nextStatus === "disabled" ? `已禁用 ${updated.username}` : `已启用 ${updated.username}`,
      )
    } catch (caught) {
      setPanelError(caught)
    } finally {
      setBusyId(null)
    }
  }

  function handleCreated(created: User): void {
    setUsers((current) => (current ? [created, ...current] : [created]))
    setCreateOpen(false)
    setNotice(`已创建用户 ${created.username}`)
  }

  function handleResetDone(message: string): void {
    setResetTarget(null)
    setNotice(message)
  }

  const columns: TableColumn<User>[] = [
    {
      key: "username",
      header: "用户名",
      render: (item) => (
        <div>
          <span className="font-medium text-foreground">{item.username}</span>
          <span className="ml-2 font-mono text-[10px] text-muted-foreground">#{item.id}</span>
        </div>
      ),
    },
    {
      key: "display_name",
      header: "显示名",
      render: (item) => item.display_name,
    },
    {
      key: "role",
      header: "角色",
      render: (item) => (
        <Badge tone={item.role === "admin" ? "info" : "neutral"}>{ROLE_LABELS[item.role]}</Badge>
      ),
    },
    {
      key: "status",
      header: "状态",
      render: (item) => (
        <Badge tone={item.status === "active" ? "success" : "danger"}>
          {STATUS_LABELS[item.status]}
        </Badge>
      ),
    },
    {
      key: "created_at",
      header: "创建时间",
      render: (item) => (
        <span className="text-xs text-muted-foreground">{formatDateTime(item.created_at)}</span>
      ),
    },
    {
      key: "actions",
      header: "操作",
      render: (item) => (
        <div className="flex flex-wrap items-center gap-2">
          {item.role === "user" ? (
            <Button
              variant="secondary"
              size="sm"
              disabled={busyId === item.id}
              onClick={() => void toggleStatus(item)}
            >
              {item.status === "active" ? "禁用" : "启用"}
            </Button>
          ) : null}
          <Button variant="ghost" size="sm" onClick={() => setResetTarget(item)}>
            重置密码
          </Button>
        </div>
      ),
    },
  ]

  if (loading && users === null) return <LoadingCard label="正在加载用户…" />
  if (loadError) {
    return (
      <ErrorState
        error={loadError}
        title="用户列表加载失败"
        onRetry={() => setReloadKey((n) => n + 1)}
      />
    )
  }

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 className="text-sm font-semibold text-foreground">用户管理</h2>
          <p className="mt-1 text-xs text-muted-foreground">
            创建普通用户、启用/禁用账号或重置密码；禁用与重置会撤销该用户的全部令牌。
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="secondary" size="sm" onClick={() => setReloadKey((n) => n + 1)}>
            刷新
          </Button>
          <Button size="sm" onClick={() => setCreateOpen(true)}>
            创建用户
          </Button>
        </div>
      </div>

      {notice ? (
        <p role="status" className="text-xs text-success">
          {notice}
        </p>
      ) : null}
      {panelError ? <InlineError error={panelError} /> : null}

      <Table
        ariaLabel="用户列表"
        columns={columns}
        rows={users ?? []}
        rowKey={(item) => item.id}
        empty="暂无用户"
      />

      {hasMore ? (
        <div className="flex justify-center">
          <Button
            variant="secondary"
            size="sm"
            onClick={() => void loadMore()}
            disabled={loadingMore}
          >
            {loadingMore ? "加载中…" : "加载更多"}
          </Button>
        </div>
      ) : null}

      {createOpen ? (
        <CreateUserDialog onClose={() => setCreateOpen(false)} onSaved={handleCreated} />
      ) : null}
      {resetTarget ? (
        <ResetPasswordDialog
          key={resetTarget.id}
          target={resetTarget}
          onClose={() => setResetTarget(null)}
          onDone={handleResetDone}
        />
      ) : null}
    </div>
  )
}
