"use client"

import { Plus } from "lucide-react"
import { useCallback, useState } from "react"

import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import { Table, type TableColumn } from "@/components/ui/Table"
import { formatDateTime } from "@/lib/format"

import {
  createApi,
  createPage,
  deleteApi,
  deletePage,
  importApis,
  importPages,
  updateApi,
  updatePage,
} from "./api"
import { ApiDialog, ConfirmDialog, ImportDialog, PageDialog } from "./dialogs"
import { ScopeBar } from "./ScopeBar"
import type { ResourceScope, SutApi, SutPage } from "./types"
import { parseApisImport, parsePagesImport } from "./validation"

export type PageAPIPanelProps = {
  scope: ResourceScope
  onSelectSut: (id: string) => void
  onSelectEnvironment: (id: string) => void
  onRefresh: () => Promise<void>
  onRetry: () => void
}

const PAGE_IMPORT_EXAMPLE =
  '[{"name":"订单列表","url":"https://order.test/orders","route_template":"/orders"}]'
const API_IMPORT_EXAMPLE = '[{"name":"查询订单","method":"GET","path_template":"/api/orders"}]'

/** Pages + APIs tab for the selected environment, including JSON imports. */
export function PageAPIPanel({
  scope,
  onSelectSut,
  onSelectEnvironment,
  onRefresh,
  onRetry,
}: PageAPIPanelProps) {
  const [createPageOpen, setCreatePageOpen] = useState(false)
  const [editingPage, setEditingPage] = useState<SutPage | null>(null)
  const [deletingPage, setDeletingPage] = useState<SutPage | null>(null)
  const [importPagesOpen, setImportPagesOpen] = useState(false)
  const [createApiOpen, setCreateApiOpen] = useState(false)
  const [editingApi, setEditingApi] = useState<SutApi | null>(null)
  const [deletingApi, setDeletingApi] = useState<SutApi | null>(null)
  const [importApisOpen, setImportApisOpen] = useState(false)

  const environments = scope.detail?.environments ?? []
  const environment =
    environments.find((item) => item.id === scope.environmentId) ?? environments[0] ?? null

  const submitPageImport = useCallback(
    async (items: Parameters<typeof importPages>[1]) => {
      if (!environment) throw new Error("请先选择环境")
      return importPages(environment.id, items)
    },
    [environment],
  )

  const submitApiImport = useCallback(
    async (items: Parameters<typeof importApis>[1]) => {
      if (!environment) throw new Error("请先选择环境")
      return importApis(environment.id, items)
    },
    [environment],
  )

  if (scope.loading) return <LoadingCard label="正在加载 SUT 列表…" />
  if (scope.error) return <ErrorState error={scope.error} onRetry={onRetry} />
  if (scope.suts.length === 0) {
    return (
      <EmptyState
        title="还没有 SUT"
        description="先在「SUT 与环境」Tab 创建 SUT 和环境，再回到这里维护页面与 API。"
      />
    )
  }
  if (scope.detailLoading) return <LoadingCard label="正在加载环境与资源…" />
  if (scope.detailError) return <ErrorState error={scope.detailError} onRetry={onRetry} />

  const pageColumns: TableColumn<SutPage>[] = [
    {
      key: "name",
      header: "页面",
      render: (page) => (
        <div className="min-w-0">
          <p className="text-sm font-medium text-foreground">{page.name}</p>
          <p className="font-mono text-[11px] text-muted-foreground">
            {page.route_template || `${page.source} / ${page.page_type}`}
          </p>
        </div>
      ),
    },
    {
      key: "url",
      header: "URL",
      render: (page) => (
        <span className="block max-w-md truncate font-mono text-[11px] text-muted-foreground">
          {page.url}
        </span>
      ),
    },
    {
      key: "updated_at",
      header: "更新时间",
      render: (page) => (
        <span className="text-xs text-muted-foreground">{formatDateTime(page.updated_at)}</span>
      ),
    },
    {
      key: "actions",
      header: "操作",
      className: "text-right",
      render: (page) => (
        <div className="flex items-center justify-end gap-1">
          <Button
            variant="ghost"
            size="sm"
            aria-label={`编辑页面 ${page.name}`}
            onClick={() => setEditingPage(page)}
          >
            编辑
          </Button>
          <Button
            variant="ghost"
            size="sm"
            aria-label={`删除页面 ${page.name}`}
            onClick={() => setDeletingPage(page)}
          >
            删除
          </Button>
        </div>
      ),
    },
  ]

  const apiColumns: TableColumn<SutApi>[] = [
    {
      key: "name",
      header: "API",
      render: (api) => (
        <div className="flex flex-wrap items-center gap-2">
          <Badge tone="info">{api.method}</Badge>
          <span className="text-sm font-medium text-foreground">{api.name}</span>
        </div>
      ),
    },
    {
      key: "path",
      header: "路径模板",
      render: (api) => (
        <span className="block max-w-md truncate font-mono text-[11px] text-muted-foreground">
          {api.path_template}
        </span>
      ),
    },
    {
      key: "auth_role",
      header: "所需角色",
      render: (api) => (
        <span className="text-[11px] text-muted-foreground">{api.auth_role || "—"}</span>
      ),
    },
    {
      key: "actions",
      header: "操作",
      className: "text-right",
      render: (api) => (
        <div className="flex items-center justify-end gap-1">
          <Button
            variant="ghost"
            size="sm"
            aria-label={`编辑 API ${api.name}`}
            onClick={() => setEditingApi(api)}
          >
            编辑
          </Button>
          <Button
            variant="ghost"
            size="sm"
            aria-label={`删除 API ${api.name}`}
            onClick={() => setDeletingApi(api)}
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
          页面与 API{environment ? ` · ${environment.name}` : ""}
        </h2>
        <p className="mt-0.5 text-xs text-muted-foreground">
          维护当前环境的可测页面与 API；导入时逐项校验，重复键由 API 跳过。
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
          description="页面与 API 必须归属于环境。先在「SUT 与环境」Tab 为当前 SUT 创建环境，再回到这里。"
        />
      ) : (
        <>
          <section aria-label="页面管理" className="surface-card space-y-3 p-4">
            <header className="flex flex-wrap items-start justify-between gap-3">
              <div>
                <h3 className="text-sm font-semibold text-foreground">
                  页面
                  <span className="ml-2 text-[11px] font-normal text-muted-foreground">
                    {environment.pages.length} 个
                  </span>
                </h3>
                <p className="mt-0.5 text-[11px] text-muted-foreground">
                  页面 URL 必填；路由模板可选，API 会从 URL 路径派生。
                </p>
              </div>
              <div className="flex flex-wrap items-center gap-2">
                <Button variant="secondary" onClick={() => setImportPagesOpen(true)}>
                  导入页面
                </Button>
                <Button onClick={() => setCreatePageOpen(true)}>
                  <Plus className="size-4" aria-hidden="true" />
                  新建页面
                </Button>
              </div>
            </header>
            <Table
              ariaLabel="页面列表"
              columns={pageColumns}
              rows={environment.pages}
              rowKey={(page) => page.id}
              empty="还没有页面。点击「新建页面」或「导入页面」添加。"
            />
          </section>

          <section aria-label="API 管理" className="surface-card space-y-3 p-4">
            <header className="flex flex-wrap items-start justify-between gap-3">
              <div>
                <h3 className="text-sm font-semibold text-foreground">
                  API
                  <span className="ml-2 text-[11px] font-normal text-muted-foreground">
                    {environment.apis.length} 个
                  </span>
                </h3>
                <p className="mt-0.5 text-[11px] text-muted-foreground">
                  HTTP 方法固定枚举；路径模板必须以 / 开头。
                </p>
              </div>
              <div className="flex flex-wrap items-center gap-2">
                <Button variant="secondary" onClick={() => setImportApisOpen(true)}>
                  导入 API
                </Button>
                <Button onClick={() => setCreateApiOpen(true)}>
                  <Plus className="size-4" aria-hidden="true" />
                  新建 API
                </Button>
              </div>
            </header>
            <Table
              ariaLabel="API 列表"
              columns={apiColumns}
              rows={environment.apis}
              rowKey={(api) => api.id}
              empty="还没有 API。点击「新建 API」或「导入 API」添加。"
            />
          </section>
        </>
      )}

      {createPageOpen && environment ? (
        <PageDialog
          onClose={() => setCreatePageOpen(false)}
          onSubmit={async (values) => {
            await createPage(environment.id, values)
            await onRefresh()
          }}
        />
      ) : null}

      {editingPage ? (
        <PageDialog
          initial={editingPage}
          onClose={() => setEditingPage(null)}
          onSubmit={async (values) => {
            await updatePage(editingPage.id, values)
            await onRefresh()
          }}
        />
      ) : null}

      {deletingPage ? (
        <ConfirmDialog
          title={`删除页面「${deletingPage.name}」？`}
          description="被版本 scope 引用时 API 会返回 409 并拒绝删除。"
          confirmLabel="删除页面"
          onClose={() => setDeletingPage(null)}
          onConfirm={async () => {
            await deletePage(deletingPage.id)
            await onRefresh()
          }}
        />
      ) : null}

      {importPagesOpen && environment ? (
        <ImportDialog
          title="导入页面"
          itemLabel="页面"
          example={PAGE_IMPORT_EXAMPLE}
          parse={parsePagesImport}
          onClose={() => setImportPagesOpen(false)}
          onSubmit={async (items) => {
            const result = await submitPageImport(items)
            await onRefresh()
            return result
          }}
        />
      ) : null}

      {createApiOpen && environment ? (
        <ApiDialog
          onClose={() => setCreateApiOpen(false)}
          onSubmit={async (values) => {
            await createApi(environment.id, values)
            await onRefresh()
          }}
        />
      ) : null}

      {editingApi ? (
        <ApiDialog
          initial={editingApi}
          onClose={() => setEditingApi(null)}
          onSubmit={async (values) => {
            await updateApi(editingApi.id, values)
            await onRefresh()
          }}
        />
      ) : null}

      {deletingApi ? (
        <ConfirmDialog
          title={`删除 API「${deletingApi.name}」？`}
          description="被版本 scope 引用时 API 会返回 409 并拒绝删除。"
          confirmLabel="删除 API"
          onClose={() => setDeletingApi(null)}
          onConfirm={async () => {
            await deleteApi(deletingApi.id)
            await onRefresh()
          }}
        />
      ) : null}

      {importApisOpen && environment ? (
        <ImportDialog
          title="导入 API"
          itemLabel="API"
          example={API_IMPORT_EXAMPLE}
          parse={parseApisImport}
          onClose={() => setImportApisOpen(false)}
          onSubmit={async (items) => {
            const result = await submitApiImport(items)
            await onRefresh()
            return result
          }}
        />
      ) : null}
    </div>
  )
}
