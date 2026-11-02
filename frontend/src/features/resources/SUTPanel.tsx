"use client"

import { Plus } from "lucide-react"
import { useState } from "react"

import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import { Table, type TableColumn } from "@/components/ui/Table"
import { formatDateTime } from "@/lib/format"

import {
  createEnvironment,
  createSutWithDefaultEnvironment,
  DefaultEnvironmentError,
  deleteEnvironment,
  deleteSut,
  updateEnvironment,
  updateSut,
} from "./api"
import { ConfirmDialog, EnvironmentDialog, SutDialog } from "./dialogs"
import type { ResourceScope, Sut, SutEnvironment, SutFormValues } from "./types"

export type SUTPanelProps = {
  scope: ResourceScope
  onSelectSut: (id: string) => void
  onRefresh: (preferredSutId?: string) => Promise<void>
  onRetry: () => void
}

/**
 * SUT tab: lists SUTs, creates a SUT with its default environment, and manages
 * the environments of the selected SUT.
 */
export function SUTPanel({ scope, onSelectSut, onRefresh, onRetry }: SUTPanelProps) {
  const [createSutOpen, setCreateSutOpen] = useState(false)
  const [editingSut, setEditingSut] = useState<Sut | null>(null)
  const [deletingSut, setDeletingSut] = useState<Sut | null>(null)
  const [createEnvironmentOpen, setCreateEnvironmentOpen] = useState(false)
  const [editingEnvironment, setEditingEnvironment] = useState<SutEnvironment | null>(null)
  const [deletingEnvironment, setDeletingEnvironment] = useState<SutEnvironment | null>(null)

  const selectedSut = scope.detail ?? scope.suts.find((sut) => sut.id === scope.sutId) ?? null
  const environments = selectedSut?.environments ?? []

  async function handleCreateSut(values: SutFormValues): Promise<void> {
    try {
      const family = values.family?.trim() ?? ""
      const { sut } = await createSutWithDefaultEnvironment({
        sut: {
          name: values.name,
          intro: values.intro,
          ...(family.length > 0 ? { family } : {}),
        },
        environment: { name: values.environmentName, base_url: values.baseUrl },
      })
      await onRefresh(sut.id)
    } catch (caught) {
      // The SUT exists even when its default environment failed; refresh so the
      // user can add the environment from the environment section.
      if (caught instanceof DefaultEnvironmentError) await onRefresh(caught.sut.id)
      throw caught
    }
  }

  const sutColumns: TableColumn<Sut>[] = [
    {
      key: "name",
      header: "SUT",
      render: (sut) => (
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <button
              type="button"
              onClick={() => onSelectSut(sut.id)}
              className="max-w-md truncate text-left text-sm font-medium text-primary hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              {sut.name}
            </button>
            {sut.id === scope.sutId ? <Badge tone="info">已选择</Badge> : null}
            {sut.family !== null && sut.family !== undefined && sut.family.length > 0 ? (
              <Badge tone="neutral" className="font-mono">
                {sut.family}
              </Badge>
            ) : null}
          </div>
          {sut.intro.length > 0 ? (
            <p className="max-w-md truncate text-[11px] text-muted-foreground">{sut.intro}</p>
          ) : null}
        </div>
      ),
    },
    {
      key: "updated_at",
      header: "更新时间",
      render: (sut) => (
        <span className="text-xs text-muted-foreground">{formatDateTime(sut.updated_at)}</span>
      ),
    },
    {
      key: "actions",
      header: "操作",
      className: "text-right",
      render: (sut) => (
        <div className="flex items-center justify-end gap-1">
          <Button
            variant="ghost"
            size="sm"
            aria-label={`编辑 SUT ${sut.name}`}
            onClick={() => setEditingSut(sut)}
          >
            编辑
          </Button>
          <Button
            variant="ghost"
            size="sm"
            aria-label={`删除 SUT ${sut.name}`}
            onClick={() => setDeletingSut(sut)}
          >
            删除
          </Button>
        </div>
      ),
    },
  ]

  const environmentColumns: TableColumn<SutEnvironment>[] = [
    {
      key: "name",
      header: "环境",
      render: (environment) => (
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-sm font-medium text-foreground">{environment.name}</span>
          {environment.is_default ? <Badge tone="success">默认</Badge> : null}
          {environment.status === "disabled" ? <Badge tone="warning">停用</Badge> : null}
        </div>
      ),
    },
    {
      key: "base_url",
      header: "地址",
      render: (environment) => (
        <span className="block max-w-md truncate font-mono text-[11px] text-muted-foreground">
          {environment.base_url}
        </span>
      ),
    },
    {
      key: "resources",
      header: "资源",
      render: (environment) => (
        <span className="text-[11px] text-muted-foreground">
          {environment.pages.length} 页面 · {environment.apis.length} API ·{" "}
          {environment.databases.length} 库 · {environment.accounts.length} 账号
        </span>
      ),
    },
    {
      key: "actions",
      header: "操作",
      className: "text-right",
      render: (environment) => (
        <div className="flex items-center justify-end gap-1">
          <Button
            variant="ghost"
            size="sm"
            aria-label={`编辑环境 ${environment.name}`}
            onClick={() => setEditingEnvironment(environment)}
          >
            编辑
          </Button>
          <Button
            variant="ghost"
            size="sm"
            aria-label={`删除环境 ${environment.name}`}
            onClick={() => setDeletingEnvironment(environment)}
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
          <h2 className="text-sm font-semibold text-foreground">SUT 列表</h2>
          <p className="mt-0.5 text-xs text-muted-foreground">
            创建 SUT 时会同时创建默认环境；选择 SUT 后可维护它的环境。
          </p>
        </div>
        <Button onClick={() => setCreateSutOpen(true)}>
          <Plus className="size-4" aria-hidden="true" />
          新建 SUT
        </Button>
      </div>

      {scope.loading ? (
        <LoadingCard label="正在加载 SUT 列表…" />
      ) : scope.error ? (
        <ErrorState error={scope.error} onRetry={onRetry} />
      ) : scope.suts.length === 0 ? (
        <EmptyState
          title="还没有 SUT"
          description="创建第一个 SUT 和它的默认环境，然后在页面/API、数据与代码仓 Tab 继续配置资源。"
          action={
            <Button onClick={() => setCreateSutOpen(true)}>
              <Plus className="size-4" aria-hidden="true" />
              新建 SUT
            </Button>
          }
        />
      ) : (
        <Table
          ariaLabel="SUT 列表"
          columns={sutColumns}
          rows={scope.suts}
          rowKey={(sut) => sut.id}
        />
      )}

      {scope.sutId.length > 0 ? (
        <section aria-label="环境管理" className="surface-card space-y-3 p-4">
          <header className="flex flex-wrap items-start justify-between gap-3">
            <div>
              <h2 className="text-sm font-semibold text-foreground">
                环境{selectedSut ? ` · ${selectedSut.name}` : ""}
              </h2>
              <p className="mt-0.5 text-xs text-muted-foreground">
                页面/API 与数据资源都挂在一个环境下；默认环境会被版本向导优先选中。
              </p>
            </div>
            <Button
              variant="secondary"
              onClick={() => setCreateEnvironmentOpen(true)}
              disabled={scope.detailLoading}
            >
              <Plus className="size-4" aria-hidden="true" />
              新建环境
            </Button>
          </header>

          {scope.detailLoading ? (
            <LoadingCard label="正在加载环境…" />
          ) : scope.detailError ? (
            <ErrorState error={scope.detailError} onRetry={onRetry} />
          ) : environments.length === 0 ? (
            <EmptyState
              title="还没有环境"
              description="SUT 至少需要一个环境，页面/API、目标库与测试账号都归属于环境。"
              action={
                <Button variant="secondary" onClick={() => setCreateEnvironmentOpen(true)}>
                  新建环境
                </Button>
              }
            />
          ) : (
            <Table
              ariaLabel="环境列表"
              columns={environmentColumns}
              rows={environments}
              rowKey={(environment) => environment.id}
            />
          )}
        </section>
      ) : null}

      {createSutOpen ? (
        <SutDialog onClose={() => setCreateSutOpen(false)} onSubmit={handleCreateSut} />
      ) : null}

      {editingSut ? (
        <SutDialog
          initial={editingSut}
          onClose={() => setEditingSut(null)}
          onSubmit={async (values) => {
            const family = values.family?.trim() ?? ""
            await updateSut(editingSut.id, {
              name: values.name,
              intro: values.intro,
              ...(family.length > 0 ? { family } : {}),
            })
            await onRefresh()
          }}
        />
      ) : null}

      {deletingSut ? (
        <ConfirmDialog
          title={`删除 SUT「${deletingSut.name}」？`}
          description="删除后不可恢复；如果该 SUT 已被版本绑定，API 会拒绝删除。"
          confirmLabel="删除 SUT"
          onClose={() => setDeletingSut(null)}
          onConfirm={async () => {
            await deleteSut(deletingSut.id)
            await onRefresh()
          }}
        />
      ) : null}

      {createEnvironmentOpen ? (
        <EnvironmentDialog
          onClose={() => setCreateEnvironmentOpen(false)}
          onSubmit={async (values) => {
            await createEnvironment(scope.sutId, values)
            await onRefresh()
          }}
        />
      ) : null}

      {editingEnvironment ? (
        <EnvironmentDialog
          initial={editingEnvironment}
          onClose={() => setEditingEnvironment(null)}
          onSubmit={async (values) => {
            await updateEnvironment(editingEnvironment.id, values)
            await onRefresh()
          }}
        />
      ) : null}

      {deletingEnvironment ? (
        <ConfirmDialog
          title={`删除环境「${deletingEnvironment.name}」？`}
          description="只有未被版本绑定引用的环境才能删除。"
          confirmLabel="删除环境"
          onClose={() => setDeletingEnvironment(null)}
          onConfirm={async () => {
            await deleteEnvironment(deletingEnvironment.id)
            await onRefresh()
          }}
        />
      ) : null}
    </div>
  )
}
