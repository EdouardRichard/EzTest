"use client"

import { Plus } from "lucide-react"
import { useCallback, useState } from "react"

import { Badge, type BadgeTone } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState, InlineError } from "@/components/ui/ErrorState"
import { Select } from "@/components/ui/Select"
import { LoadingCard } from "@/components/ui/Spinner"
import { Table, type TableColumn } from "@/components/ui/Table"
import { useIsAdmin } from "@/features/auth/useIsAdmin"
import { listVersions } from "@/features/versions/api"
import type { VersionSummary } from "@/features/versions/types"
import { formatDateTime } from "@/lib/format"

import {
  confirmApiProposal,
  confirmPageProposal,
  createApi,
  createPage,
  deleteApi,
  deletePage,
  generatePageApiProposals,
  importApis,
  importPages,
  listPageApiProposals,
  rejectApiProposal,
  rejectPageProposal,
  updateApi,
  updatePage,
} from "./api"
import { ApiDialog, ConfirmDialog, ImportDialog, PageDialog } from "./dialogs"
import { ScopeBar } from "./ScopeBar"
import type { CodeProposal, PageStatus, ResourceScope, SutApi, SutPage } from "./types"
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

type StatusFilter = "all" | PageStatus

const STATUS_LABEL: Record<PageStatus, string> = {
  approved: "已确认",
  proposed: "待确认",
  rejected: "已驳回",
}

const STATUS_TONE: Record<PageStatus, BadgeTone> = {
  approved: "success",
  proposed: "warning",
  rejected: "neutral",
}

function proposalEvidenceLabels(proposal: CodeProposal): string[] {
  return (proposal.evidence ?? []).flatMap((item) => {
    if (typeof item.path === "string" && item.path.length > 0) return [item.path]
    if (typeof item.ref === "string" && item.ref.length > 0) return [item.ref]
    return []
  })
}

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

  const isAdmin = useIsAdmin()
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all")
  const [proposalsOpen, setProposalsOpen] = useState(false)
  const [versions, setVersions] = useState<VersionSummary[]>([])
  const [proposalVersionId, setProposalVersionId] = useState("")
  const [proposals, setProposals] = useState<CodeProposal[]>([])
  const [proposalsLoading, setProposalsLoading] = useState(false)
  const [proposalsError, setProposalsError] = useState<unknown>(null)
  const [proposalNotice, setProposalNotice] = useState<string | null>(null)
  const [proposalActionError, setProposalActionError] = useState<unknown>(null)
  const [generating, setGenerating] = useState(false)
  const [decisionBusy, setDecisionBusy] = useState<string | null>(null)

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

  const loadProposals = useCallback(async (versionId: string) => {
    if (versionId.length === 0) {
      setProposals([])
      return
    }
    setProposalsLoading(true)
    setProposalsError(null)
    try {
      setProposals(await listPageApiProposals(versionId))
    } catch (cause) {
      setProposals([])
      setProposalsError(cause)
    } finally {
      setProposalsLoading(false)
    }
  }, [])

  const openProposals = useCallback(async () => {
    setProposalsOpen(true)
    setProposalNotice(null)
    setProposalsError(null)
    setProposalActionError(null)
    try {
      let nextVersions = versions
      if (nextVersions.length === 0) {
        nextVersions = await listVersions()
        setVersions(nextVersions)
      }
      const versionId =
        proposalVersionId.length > 0 &&
        nextVersions.some((version) => version.id === proposalVersionId)
          ? proposalVersionId
          : (nextVersions[0]?.id ?? "")
      setProposalVersionId(versionId)
      await loadProposals(versionId)
    } catch (cause) {
      setProposalsError(cause)
    }
  }, [loadProposals, proposalVersionId, versions])

  const generate = useCallback(async () => {
    if (proposalVersionId.length === 0 || generating) return
    setGenerating(true)
    setProposalNotice(null)
    setProposalActionError(null)
    try {
      const result = await generatePageApiProposals(proposalVersionId)
      setProposalNotice(
        `已创建 ${result.pages.created} 个页面、${result.apis.created} 个 API，跳过 ${
          result.pages.skipped + result.apis.skipped
        } 个。`,
      )
      await onRefresh()
      await loadProposals(proposalVersionId)
    } catch (cause) {
      setProposalActionError(cause)
    } finally {
      setGenerating(false)
    }
  }, [generating, loadProposals, onRefresh, proposalVersionId])

  const decide = useCallback(
    async (kind: "page" | "api", id: string, action: "confirm" | "reject") => {
      const key = `${kind}-${id}-${action}`
      setDecisionBusy(key)
      setProposalActionError(null)
      try {
        if (kind === "page") {
          await (action === "confirm" ? confirmPageProposal(id) : rejectPageProposal(id))
        } else {
          await (action === "confirm" ? confirmApiProposal(id) : rejectApiProposal(id))
        }
        await onRefresh()
        if (proposalsOpen && proposalVersionId.length > 0) {
          await loadProposals(proposalVersionId)
        }
      } catch (cause) {
        setProposalActionError(cause)
      } finally {
        setDecisionBusy(null)
      }
    },
    [loadProposals, onRefresh, proposalVersionId, proposalsOpen],
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
      key: "status",
      header: "状态 / 来源",
      render: (page) => (
        <div className="flex flex-wrap items-center gap-1">
          <Badge tone={STATUS_TONE[page.status]}>{STATUS_LABEL[page.status]}</Badge>
          <Badge tone="neutral">{page.source}</Badge>
        </div>
      ),
    },
    {
      key: "actions",
      header: "操作",
      className: "text-right",
      render: (page) => (
        <div className="flex items-center justify-end gap-1">
          {isAdmin && page.status === "proposed" ? (
            <>
              <Button
                size="sm"
                disabled={decisionBusy === `page-${page.id}-confirm`}
                aria-label={`确认页面 ${page.name}`}
                onClick={() => {
                  void decide("page", page.id, "confirm")
                }}
              >
                确认
              </Button>
              <Button
                variant="secondary"
                size="sm"
                disabled={decisionBusy === `page-${page.id}-reject`}
                aria-label={`驳回页面 ${page.name}`}
                onClick={() => {
                  void decide("page", page.id, "reject")
                }}
              >
                驳回
              </Button>
            </>
          ) : null}
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
      key: "status",
      header: "状态 / 来源",
      render: (api) => (
        <div className="flex flex-wrap items-center gap-1">
          <Badge tone={STATUS_TONE[api.status]}>{STATUS_LABEL[api.status]}</Badge>
          <Badge tone="neutral">{api.source}</Badge>
        </div>
      ),
    },
    {
      key: "actions",
      header: "操作",
      className: "text-right",
      render: (api) => (
        <div className="flex items-center justify-end gap-1">
          {isAdmin && api.status === "proposed" ? (
            <>
              <Button
                size="sm"
                disabled={decisionBusy === `api-${api.id}-confirm`}
                aria-label={`确认 API ${api.name}`}
                onClick={() => {
                  void decide("api", api.id, "confirm")
                }}
              >
                确认
              </Button>
              <Button
                variant="secondary"
                size="sm"
                disabled={decisionBusy === `api-${api.id}-reject`}
                aria-label={`驳回 API ${api.name}`}
                onClick={() => {
                  void decide("api", api.id, "reject")
                }}
              >
                驳回
              </Button>
            </>
          ) : null}
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

  const visiblePages =
    environment === null
      ? []
      : statusFilter === "all"
        ? environment.pages
        : environment.pages.filter((page) => page.status === statusFilter)
  const visibleApis =
    environment === null
      ? []
      : statusFilter === "all"
        ? environment.apis
        : environment.apis.filter((api) => api.status === statusFilter)

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

      <div className="flex flex-wrap items-center gap-1.5 text-[11px] text-muted-foreground">
        <span>状态</span>
        <Select
          aria-label="状态"
          value={statusFilter}
          onChange={(event) => setStatusFilter(event.target.value as StatusFilter)}
          className="h-8 w-32 text-xs"
        >
          <option value="all">全部</option>
          <option value="approved">已确认</option>
          <option value="proposed">待确认</option>
          <option value="rejected">已驳回</option>
        </Select>
      </div>

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
              rows={visiblePages}
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
              rows={visibleApis}
              rowKey={(api) => api.id}
              empty="还没有 API。点击「新建 API」或「导入 API」添加。"
            />
          </section>
        </>
      )}

      <section aria-label="代码提议" className="surface-card space-y-3 p-4">
        <header className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <h3 className="text-sm font-semibold text-foreground">代码提议</h3>
            <p className="mt-0.5 text-[11px] text-muted-foreground">
              从索引代码事实与执行网络证据生成 proposed 页面/API；只写
              proposed，确认或驳回由管理员完成。
            </p>
          </div>
          <Button
            variant="secondary"
            onClick={() => {
              if (proposalsOpen) {
                setProposalsOpen(false)
              } else {
                void openProposals()
              }
            }}
          >
            {proposalsOpen ? "收起代码提议" : "展开代码提议"}
          </Button>
        </header>

        {proposalsOpen ? (
          <div className="space-y-2">
            <div className="flex flex-wrap items-end gap-2">
              <div className="flex flex-col gap-1 text-[11px] text-muted-foreground">
                <span>提议版本</span>
                <Select
                  aria-label="提议版本"
                  value={proposalVersionId}
                  onChange={(event) => {
                    const next = event.target.value
                    setProposalVersionId(next)
                    setProposalNotice(null)
                    void loadProposals(next)
                  }}
                  className="h-8 w-56 text-xs"
                >
                  {versions.length === 0 ? <option value="">（暂无版本）</option> : null}
                  {versions.map((version) => (
                    <option key={version.id} value={version.id}>
                      #{version.id} · {version.title}
                    </option>
                  ))}
                </Select>
              </div>
              <Button
                disabled={proposalVersionId.length === 0 || generating}
                onClick={() => {
                  void generate()
                }}
              >
                {generating ? "生成中…" : "从代码生成提议"}
              </Button>
            </div>

            {proposalNotice !== null ? (
              <p role="status" className="text-[11px] text-success">
                {proposalNotice}
              </p>
            ) : null}

            {proposalActionError !== null ? <InlineError error={proposalActionError} /> : null}

            {proposalsLoading ? <LoadingCard label="正在加载代码提议…" /> : null}

            {!proposalsLoading && proposalsError !== null ? (
              <div className="space-y-1.5">
                <InlineError error={proposalsError} />
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() => {
                    void openProposals()
                  }}
                >
                  重试
                </Button>
              </div>
            ) : null}

            {!proposalsLoading && proposalsError === null && proposals.length === 0 ? (
              <p className="text-[11px] text-muted-foreground">
                暂无 proposed 页面/API。先生成提议，或在页面/API 列表中确认已有资源。
              </p>
            ) : null}

            {!proposalsLoading && proposalsError === null && proposals.length > 0 ? (
              <ul className="space-y-1.5">
                {proposals.map((proposal) => {
                  const evidence = proposalEvidenceLabels(proposal)
                  return (
                    <li
                      key={`${proposal.kind}-${proposal.id}`}
                      className="rounded-lg border border-border bg-muted/20 px-3 py-2 text-[11px]"
                    >
                      <div className="flex flex-wrap items-center gap-1.5">
                        <Badge tone={proposal.kind === "page" ? "info" : "neutral"}>
                          {proposal.kind === "page" ? "页面" : "API"}
                        </Badge>
                        <span className="font-medium text-foreground">{proposal.name}</span>
                        <Badge tone="warning">待确认</Badge>
                        <Badge tone="neutral">{proposal.source}</Badge>
                        <span className="font-mono text-muted-foreground">
                          {proposal.kind === "page"
                            ? (proposal.route_template ?? proposal.url ?? "")
                            : `${proposal.method ?? ""} ${proposal.path_template ?? ""}`.trim()}
                        </span>
                        {isAdmin ? (
                          <div className="ml-auto flex items-center gap-1">
                            <Button
                              size="sm"
                              disabled={decisionBusy !== null}
                              aria-label={`确认提议 ${proposal.name}`}
                              onClick={() => {
                                void decide(proposal.kind, proposal.id, "confirm")
                              }}
                            >
                              确认
                            </Button>
                            <Button
                              variant="secondary"
                              size="sm"
                              disabled={decisionBusy !== null}
                              aria-label={`驳回提议 ${proposal.name}`}
                              onClick={() => {
                                void decide(proposal.kind, proposal.id, "reject")
                              }}
                            >
                              驳回
                            </Button>
                          </div>
                        ) : null}
                      </div>
                      {evidence.length > 0 ? (
                        <div className="mt-1">
                          <p className="text-[10px] text-muted-foreground">代码事实来源</p>
                          <ul className="mt-0.5 space-y-0.5">
                            {evidence.map((item) => (
                              <li key={item} className="truncate font-mono text-[10px]">
                                {item}
                              </li>
                            ))}
                          </ul>
                        </div>
                      ) : null}
                    </li>
                  )
                })}
              </ul>
            ) : null}
          </div>
        ) : null}
      </section>

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
