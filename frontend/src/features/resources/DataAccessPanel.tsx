"use client"

import { Plus } from "lucide-react"
import { useState } from "react"

import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import { Table, type TableColumn } from "@/components/ui/Table"

import {
  type AccountPatch,
  createAccount,
  createDatabase,
  type DatabasePatch,
  deleteAccount,
  deleteDatabase,
  updateAccount,
  updateDatabase,
} from "./api"
import { AccountDialog, ConfirmDialog, DatabaseDialog } from "./dialogs"
import { ScopeBar } from "./ScopeBar"
import type { ResourceScope, TargetDatabase, TestAccount } from "./types"
import { splitList } from "./validation"

export type DataAccessPanelProps = {
  scope: ResourceScope
  onSelectSut: (id: string) => void
  onSelectEnvironment: (id: string) => void
  onRefresh: () => Promise<void>
  onRetry: () => void
}

function PasswordBadge({ isSet }: { isSet: boolean }) {
  return <Badge tone={isSet ? "success" : "neutral"}>{isSet ? "已设置" : "未设置"}</Badge>
}

/** Data tab: target databases + test accounts for the selected environment. */
export function DataAccessPanel({
  scope,
  onSelectSut,
  onSelectEnvironment,
  onRefresh,
  onRetry,
}: DataAccessPanelProps) {
  const [createDatabaseOpen, setCreateDatabaseOpen] = useState(false)
  const [editingDatabase, setEditingDatabase] = useState<TargetDatabase | null>(null)
  const [deletingDatabase, setDeletingDatabase] = useState<TargetDatabase | null>(null)
  const [createAccountOpen, setCreateAccountOpen] = useState(false)
  const [editingAccount, setEditingAccount] = useState<TestAccount | null>(null)
  const [deletingAccount, setDeletingAccount] = useState<TestAccount | null>(null)

  const environments = scope.detail?.environments ?? []
  const environment =
    environments.find((item) => item.id === scope.environmentId) ?? environments[0] ?? null

  if (scope.loading) return <LoadingCard label="正在加载 SUT 列表…" />
  if (scope.error) return <ErrorState error={scope.error} onRetry={onRetry} />
  if (scope.suts.length === 0) {
    return (
      <EmptyState
        title="还没有 SUT"
        description="先在「SUT 与环境」Tab 创建 SUT 和环境，再回到这里维护目标库与测试账号。"
      />
    )
  }
  if (scope.detailLoading) return <LoadingCard label="正在加载数据资源…" />
  if (scope.detailError) return <ErrorState error={scope.detailError} onRetry={onRetry} />

  const databaseColumns: TableColumn<TargetDatabase>[] = [
    {
      key: "name",
      header: "目标库",
      render: (database) => (
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-sm font-medium text-foreground">{database.name}</span>
          {database.read_only ? <Badge tone="neutral">只读</Badge> : null}
          {database.status === "disabled" ? <Badge tone="warning">停用</Badge> : null}
        </div>
      ),
    },
    {
      key: "dsn",
      header: "DSN",
      render: (database) => (
        <span className="block max-w-sm truncate font-mono text-[11px] text-muted-foreground">
          {database.dsn}
        </span>
      ),
    },
    {
      key: "username",
      header: "用户名",
      render: (database) => (
        <span className="font-mono text-xs text-muted-foreground">{database.username}</span>
      ),
    },
    {
      key: "password",
      header: "密码",
      render: (database) => <PasswordBadge isSet={database.password_set} />,
    },
    {
      key: "actions",
      header: "操作",
      className: "text-right",
      render: (database) => (
        <div className="flex items-center justify-end gap-1">
          <Button
            variant="ghost"
            size="sm"
            aria-label={`编辑数据库 ${database.name}`}
            onClick={() => setEditingDatabase(database)}
          >
            编辑
          </Button>
          <Button
            variant="ghost"
            size="sm"
            aria-label={`删除数据库 ${database.name}`}
            onClick={() => setDeletingDatabase(database)}
          >
            删除
          </Button>
        </div>
      ),
    },
  ]

  const accountColumns: TableColumn<TestAccount>[] = [
    {
      key: "username",
      header: "测试账号",
      render: (account) => (
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-1.5">
            <span className="text-sm font-medium text-foreground">{account.username}</span>
            {account.roles.map((role) => (
              <Badge key={role} tone="info">
                {role}
              </Badge>
            ))}
            {account.status === "disabled" ? <Badge tone="warning">停用</Badge> : null}
          </div>
          {account.notes.length > 0 ? (
            <p className="max-w-sm truncate text-[11px] text-muted-foreground">{account.notes}</p>
          ) : null}
        </div>
      ),
    },
    {
      key: "password",
      header: "密码",
      render: (account) => <PasswordBadge isSet={account.password_set} />,
    },
    {
      key: "actions",
      header: "操作",
      className: "text-right",
      render: (account) => (
        <div className="flex items-center justify-end gap-1">
          <Button
            variant="ghost"
            size="sm"
            aria-label={`编辑账号 ${account.username}`}
            onClick={() => setEditingAccount(account)}
          >
            编辑
          </Button>
          <Button
            variant="ghost"
            size="sm"
            aria-label={`删除账号 ${account.username}`}
            onClick={() => setDeletingAccount(account)}
          >
            删除
          </Button>
        </div>
      ),
    },
  ]

  return (
    <div className="space-y-5">
      <div>
        <h2 className="text-sm font-semibold text-foreground">
          数据与账号{environment ? ` · ${environment.name}` : ""}
        </h2>
        <p className="mt-0.5 text-xs text-muted-foreground">
          目标库与测试账号归属当前环境；密码只显示是否已设置，可设置或清除。
        </p>
      </div>

      <ScopeBar
        suts={scope.suts}
        sutId={scope.sutId}
        environments={environments}
        environmentId={environment?.id ?? ""}
        onSelectSut={onSelectSut}
        onSelectEnvironment={onSelectEnvironment}
      />

      {!environment ? (
        <EmptyState
          title="还没有环境"
          description="目标库与测试账号必须归属于环境。先在「SUT 与环境」Tab 为当前 SUT 创建环境，再回到这里。"
        />
      ) : (
        <>
          <section aria-label="目标库管理" className="surface-card space-y-3 p-4">
            <header className="flex flex-wrap items-start justify-between gap-3">
              <div>
                <h3 className="text-sm font-semibold text-foreground">
                  目标库
                  <span className="ml-2 text-[11px] font-normal text-muted-foreground">
                    {environment.databases.length} 个
                  </span>
                </h3>
                <p className="mt-0.5 text-[11px] text-muted-foreground">
                  DSN 仅支持 postgres:// 或 postgresql://；密码永不回显。
                </p>
              </div>
              <Button onClick={() => setCreateDatabaseOpen(true)}>
                <Plus className="size-4" aria-hidden="true" />
                新建目标库
              </Button>
            </header>
            <Table
              ariaLabel="目标库列表"
              columns={databaseColumns}
              rows={environment.databases}
              rowKey={(database) => database.id}
              empty="还没有目标库。点击「新建目标库」添加。"
            />
          </section>

          <section aria-label="测试账号管理" className="surface-card space-y-3 p-4">
            <header className="flex flex-wrap items-start justify-between gap-3">
              <div>
                <h3 className="text-sm font-semibold text-foreground">
                  测试账号
                  <span className="ml-2 text-[11px] font-normal text-muted-foreground">
                    {environment.accounts.length} 个
                  </span>
                </h3>
                <p className="mt-0.5 text-[11px] text-muted-foreground">
                  账号角色用于版本绑定标注；密码只显示是否已设置。
                </p>
              </div>
              <Button onClick={() => setCreateAccountOpen(true)}>
                <Plus className="size-4" aria-hidden="true" />
                新建测试账号
              </Button>
            </header>
            <Table
              ariaLabel="测试账号列表"
              columns={accountColumns}
              rows={environment.accounts}
              rowKey={(account) => account.id}
              empty="还没有测试账号。点击「新建测试账号」添加。"
            />
          </section>
        </>
      )}

      {createDatabaseOpen && environment ? (
        <DatabaseDialog
          environmentId={environment.id}
          onClose={() => setCreateDatabaseOpen(false)}
          onSubmit={async (values) => {
            await createDatabase(scope.sutId, {
              environmentId: values.environmentId,
              name: values.name,
              dsn: values.dsn,
              username: values.username,
              password: values.password,
              readOnly: values.readOnly,
            })
            await onRefresh()
          }}
        />
      ) : null}

      {editingDatabase ? (
        <DatabaseDialog
          environmentId={editingDatabase.environment_id}
          initial={editingDatabase}
          onClose={() => setEditingDatabase(null)}
          onSubmit={async (values) => {
            const patch: DatabasePatch = {
              name: values.name,
              dsn: values.dsn,
              username: values.username,
              readOnly: values.readOnly,
            }
            if (values.clearPassword) patch.password = ""
            else if (values.password.length > 0) patch.password = values.password
            await updateDatabase(editingDatabase.id, patch)
            await onRefresh()
          }}
        />
      ) : null}

      {deletingDatabase ? (
        <ConfirmDialog
          title={`删除目标库「${deletingDatabase.name}」？`}
          description="被版本绑定引用时 API 会返回 409 并拒绝删除。"
          confirmLabel="删除目标库"
          onClose={() => setDeletingDatabase(null)}
          onConfirm={async () => {
            await deleteDatabase(deletingDatabase.id)
            await onRefresh()
          }}
        />
      ) : null}

      {createAccountOpen && environment ? (
        <AccountDialog
          environmentId={environment.id}
          onClose={() => setCreateAccountOpen(false)}
          onSubmit={async (values) => {
            await createAccount(scope.sutId, {
              environmentId: values.environmentId,
              username: values.username,
              password: values.password,
              roles: values.roles,
              notes: values.notes,
            })
            await onRefresh()
          }}
        />
      ) : null}

      {editingAccount ? (
        <AccountDialog
          environmentId={editingAccount.environment_id}
          initial={editingAccount}
          onClose={() => setEditingAccount(null)}
          onSubmit={async (values) => {
            const patch: AccountPatch = {
              username: values.username,
              roles: splitList(values.roles),
              notes: values.notes,
            }
            if (values.clearPassword) patch.password = ""
            else if (values.password.length > 0) patch.password = values.password
            await updateAccount(editingAccount.id, patch)
            await onRefresh()
          }}
        />
      ) : null}

      {deletingAccount ? (
        <ConfirmDialog
          title={`删除测试账号「${deletingAccount.username}」？`}
          description="被版本绑定引用时 API 会返回 409 并拒绝删除。"
          confirmLabel="删除账号"
          onClose={() => setDeletingAccount(null)}
          onConfirm={async () => {
            await deleteAccount(deletingAccount.id)
            await onRefresh()
          }}
        />
      ) : null}
    </div>
  )
}
