"use client"

import { Plus } from "lucide-react"
import { useCallback, useEffect, useState } from "react"

import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import { Table, type TableColumn } from "@/components/ui/Table"
import { formatDateTime } from "@/lib/format"

import { createRepository, deleteRepository, listRepositories, updateRepository } from "./api"
import { ConfirmDialog, RepositoryDialog } from "./dialogs"
import type { CodeRepository } from "./types"

/** Repositories tab: global (not SUT-scoped) code repositories. */
export function CodeRepoPanel() {
  const [repositories, setRepositories] = useState<CodeRepository[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<unknown>(null)
  const [createOpen, setCreateOpen] = useState(false)
  const [editingRepository, setEditingRepository] = useState<CodeRepository | null>(null)
  const [deletingRepository, setDeletingRepository] = useState<CodeRepository | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      setRepositories(await listRepositories())
    } catch (caught) {
      setError(caught)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const columns: TableColumn<CodeRepository>[] = [
    {
      key: "name",
      header: "代码仓",
      render: (repository) => (
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-sm font-medium text-foreground">{repository.name}</span>
            {repository.status === "disabled" ? <Badge tone="warning">停用</Badge> : null}
          </div>
          <p className="max-w-sm truncate font-mono text-[11px] text-muted-foreground">
            {repository.path}
          </p>
        </div>
      ),
    },
    {
      key: "branch",
      header: "分支",
      render: (repository) => (
        <span className="font-mono text-xs text-muted-foreground">{repository.branch || "—"}</span>
      ),
    },
    {
      key: "rules",
      header: "规则",
      render: (repository) => (
        <span className="text-[11px] text-muted-foreground">
          包含 {repository.include.length} 条 · 排除 {repository.exclude.length} 条
        </span>
      ),
    },
    {
      key: "updated_at",
      header: "更新时间",
      render: (repository) => (
        <span className="text-xs text-muted-foreground">
          {formatDateTime(repository.updated_at)}
        </span>
      ),
    },
    {
      key: "actions",
      header: "操作",
      className: "text-right",
      render: (repository) => (
        <div className="flex items-center justify-end gap-1">
          <Button
            variant="ghost"
            size="sm"
            aria-label={`编辑代码仓 ${repository.name}`}
            onClick={() => setEditingRepository(repository)}
          >
            编辑
          </Button>
          <Button
            variant="ghost"
            size="sm"
            aria-label={`删除代码仓 ${repository.name}`}
            onClick={() => setDeletingRepository(repository)}
          >
            删除
          </Button>
        </div>
      ),
    },
  ]

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 className="text-sm font-semibold text-foreground">代码仓库</h2>
          <p className="mt-0.5 text-xs text-muted-foreground">
            代码仓不绑定 SUT；任意登录用户都可以创建、编辑和删除。被版本绑定引用时 API 会拒绝删除。
          </p>
        </div>
        <Button onClick={() => setCreateOpen(true)}>
          <Plus className="size-4" aria-hidden="true" />
          新建代码仓
        </Button>
      </div>

      {loading ? (
        <LoadingCard label="正在加载代码仓…" />
      ) : error ? (
        <ErrorState error={error} onRetry={() => void load()} />
      ) : repositories.length === 0 ? (
        <EmptyState
          title="还没有代码仓"
          description="创建代码仓后，在版本创建向导中可以把仓库绑定到某个 SUT。"
          action={
            <Button onClick={() => setCreateOpen(true)}>
              <Plus className="size-4" aria-hidden="true" />
              新建代码仓
            </Button>
          }
        />
      ) : (
        <Table
          ariaLabel="代码仓列表"
          columns={columns}
          rows={repositories}
          rowKey={(repository) => repository.id}
        />
      )}

      {createOpen ? (
        <RepositoryDialog
          onClose={() => setCreateOpen(false)}
          onSubmit={async (values) => {
            await createRepository(values)
            await load()
          }}
        />
      ) : null}

      {editingRepository ? (
        <RepositoryDialog
          initial={editingRepository}
          onClose={() => setEditingRepository(null)}
          onSubmit={async (values) => {
            await updateRepository(editingRepository.id, values)
            await load()
          }}
        />
      ) : null}

      {deletingRepository ? (
        <ConfirmDialog
          title={`删除代码仓「${deletingRepository.name}」？`}
          description="被版本绑定引用时 API 会返回 409 并拒绝删除。"
          confirmLabel="删除代码仓"
          onClose={() => setDeletingRepository(null)}
          onConfirm={async () => {
            await deleteRepository(deletingRepository.id)
            await load()
          }}
        />
      ) : null}
    </div>
  )
}
