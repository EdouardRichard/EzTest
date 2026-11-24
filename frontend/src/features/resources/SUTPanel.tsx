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
  rejectSut,
  updateEnvironment,
  updateSut,
} from "./api"
import { ConfirmDialog, EnvironmentDialog, SutDialog } from "./dialogs"
import type {
  ResourceScope,
  Sut,
  SutEnvironment,
  SutFormValues,
  SutRegistrationStatus,
} from "./types"

/** Registration-status badge tone/label (v0.2.4-F R-B4: admin audits AI SUTs). */
const SUT_STATUS_BADGE: Record<
  SutRegistrationStatus,
  { tone: "success" | "warning" | "danger"; label: string }
> = {
  approved: { tone: "success", label: "已批准" },
  proposed: { tone: "warning", label: "待审计" },
  rejected: { tone: "danger", label: "已驳回" },
}

export type SUTPanelProps = {
  scope: ResourceScope
  onSelectSut: (id: string) => void
  onRefresh: (preferredSutId?: string) => Promise<void>
  onRetry: () => void
}

/**
 * SUT tab (v0.2.4-B): lists SUTs and creates a SUT under a freshly created
 * top-level environment. Environments are managed top-level (they own SUTs), not
 * per-SUT; base_url lives on each SUT.
 */
export function SUTPanel({ scope, onSelectSut, onRefresh, onRetry }: SUTPanelProps) {
  const [createSutOpen, setCreateSutOpen] = useState(false)
  const [editingSut, setEditingSut] = useState<Sut | null>(null)
  const [deletingSut, setDeletingSut] = useState<Sut | null>(null)
  const [rejectingSut, setRejectingSut] = useState<Sut | null>(null)
  const [createEnvironmentOpen, setCreateEnvironmentOpen] = useState(false)
  const [editingEnvironment, setEditingEnvironment] = useState<SutEnvironment | null>(null)
  const [deletingEnvironment, setDeletingEnvironment] = useState<SutEnvironment | null>(null)

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
      // The environment exists even when the SUT create failed; refresh so the
      // user can add the SUT from the SUT list.
      if (caught instanceof DefaultEnvironmentError) await onRefresh()
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
            <Badge tone={SUT_STATUS_BADGE[sut.status].tone}>
              {SUT_STATUS_BADGE[sut.status].label}
            </Badge>
            {sut.source === "ai" ? <Badge tone="neutral">AI 上报</Badge> : null}
            {sut.family !== null && sut.family !== undefined && sut.family.length > 0 ? (
              <Badge tone="neutral" className="font-mono">
                {sut.family}
              </Badge>
            ) : null}
          </div>
          {sut.intro.length > 0 ? (
            <p className="max-w-md truncate text-[11px] text-muted-foreground">{sut.intro}</p>
          ) : null}
          {sut.base_url.length > 0 ? (
            <p className="max-w-md truncate font-mono text-[11px] text-muted-foreground">
              {sut.base_url}
            </p>
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
          {sut.status !== "rejected" ? (
            <Button
              variant="ghost"
              size="sm"
              aria-label={`驳回 SUT ${sut.name}`}
              onClick={() => setRejectingSut(sut)}
            >
              驳回
            </Button>
          ) : null}
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
          {environment.status === "disabled" ? <Badge tone="warning">停用</Badge> : null}
        </div>
      ),
    },
    {
      key: "description",
      header: "描述",
      render: (environment) => (
        <span className="block max-w-md truncate text-[11px] text-muted-foreground">
          {environment.description || "—"}
        </span>
      ),
    },
    {
      key: "suts",
      header: "SUT",
      render: (environment) => (
        <span className="text-[11px] text-muted-foreground">{environment.suts.length} 个</span>
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
            创建 SUT 时会同时创建一个顶层环境并把 SUT 挂在它下面；base_url 属于 SUT。
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
          description="创建第一个 SUT 和它的环境，然后在页面/API、数据与代码仓 Tab 继续配置资源。"
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

      <section aria-label="环境管理" className="surface-card space-y-3 p-4">
        <header className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <h2 className="text-sm font-semibold text-foreground">环境</h2>
            <p className="mt-0.5 text-xs text-muted-foreground">
              环境是顶层资源，拥有若干 SUT；版本直接绑定环境。
            </p>
          </div>
          <Button variant="secondary" onClick={() => setCreateEnvironmentOpen(true)}>
            <Plus className="size-4" aria-hidden="true" />
            新建环境
          </Button>
        </header>

        {scope.environments.length === 0 ? (
          <EmptyState
            title="还没有环境"
            description="环境拥有 SUT；先创建环境，再在它下面创建 SUT 与页面/API、目标库。"
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
            rows={scope.environments}
            rowKey={(environment) => environment.id}
          />
        )}
      </section>

      {createSutOpen ? (
        <SutDialog onClose={() => setCreateSutOpen(false)} onSubmit={handleCreateSut} />
      ) : null}

      {editingSut ? (
        <SutDialog
          initial={editingSut}
          onClose={() => setEditingSut(null)}
          onSubmit={async (values) => {
            // The edit dialog always carries the current family (pre-filled, cleared
            // to ""). Pass it through unconditionally so clearing reaches the API as
            // family:"" (the backend clears on the empty string). Stripping the key
            // here when blank made "clear family" a silent no-op (H-C2).
            await updateSut(editingSut.id, {
              name: values.name,
              intro: values.intro,
              family: values.family?.trim() ?? "",
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

      {rejectingSut ? (
        <ConfirmDialog
          title={`驳回 SUT「${rejectingSut.name}」？`}
          description="驳回后该 SUT 退出有效范围（含其代码仓关联），不再参与执行/指纹/代码基线；Agent 上报的新 SUT 经证据门自动转正后仍可事后驳回。"
          confirmLabel="驳回 SUT"
          onClose={() => setRejectingSut(null)}
          onConfirm={async () => {
            await rejectSut(rejectingSut.id)
            await onRefresh()
          }}
        />
      ) : null}

      {createEnvironmentOpen ? (
        <EnvironmentDialog
          onClose={() => setCreateEnvironmentOpen(false)}
          onSubmit={async (values) => {
            await createEnvironment(values)
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
