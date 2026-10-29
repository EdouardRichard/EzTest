"use client"

import { ChevronDown, ChevronRight, Loader2, Plus, RefreshCw } from "lucide-react"
import Link from "next/link"
import {
  type FormEvent,
  type ReactNode,
  useCallback,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
} from "react"

import { StatusBadge, type StatusBadgeVariant } from "@/components/ui/StatusBadge"
import { useAuth } from "@/features/auth/AuthProvider"
import { getVersion } from "@/features/versions/api"
import type { Version, VersionStatus } from "@/features/versions/types"
import type { VersionStreamState } from "@/features/versions/useVersionStream"
import { versionStatusLabel, versionStatusVariant } from "@/features/versions/VersionHeader"
import type { SSEStatus } from "@/lib/sse-client"

import { mindmapApi } from "./api"
import { CaseEditor } from "./CaseEditor"
import { ExportMenu } from "./ExportMenu"
import { describeMindmapError } from "./error-messages"
import { MindmapCanvas } from "./MindmapCanvas"
import { ReviewPanel } from "./ReviewPanel"
import { RunPanel } from "./RunPanel"
import { ScenarioEditor } from "./ScenarioEditor"
import { SourcePanel } from "./SourcePanel"
import type { CaseNodeKind, CaseNodeView } from "./types"
import { useMindmap } from "./useMindmap"

/** Toolbar copy for the four channel states (spec §9.2 asks for 实时/重连中/断开). */
const STREAM_STATUS_META: Record<SSEStatus, { label: string; variant: StatusBadgeVariant }> = {
  connecting: { label: "连接中", variant: "info" },
  live: { label: "实时", variant: "success" },
  reconnecting: { label: "重连中", variant: "warning" },
  closed: { label: "断开", variant: "danger" },
}

/** Spec §6.3: review runs and exports are only open in these version states. */
const REVIEW_EXPORT_STATUSES: ReadonlySet<VersionStatus> = new Set([
  "designing",
  "reviewing",
  "executing",
])

/**
 * Copy for a read-only graph without a frozen baseline. The "baseline frozen"
 * banner is reserved for a real `graph.baseline`; version status alone decides
 * whether editing is simply not open yet (draft/clarifying) or already over.
 */
function statusBannerCopy(status: VersionStatus): string {
  switch (status) {
    case "draft":
    case "clarifying":
      return "需求确认并进入设计后可编辑脑图。"
    case "completed":
    case "archived":
      return "当前版本已结束，脑图为只读。"
    case "executing":
    case "continuing":
      return "当前版本处于执行阶段，脑图不可编辑。"
    default:
      return "当前版本暂不可编辑脑图。"
  }
}

const FIELD_CLASS =
  "mt-1 w-full rounded-lg border border-border bg-input px-2.5 py-1.5 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-70"

const PRIMARY_BUTTON_CLASS =
  "inline-flex items-center gap-1.5 rounded-lg bg-primary px-3.5 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"

const SECONDARY_BUTTON_CLASS =
  "inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"

/**
 * Height of the embedded canvas column. The version-detail tab sits below the
 * page chrome (topbar, version header, tab bar), so the canvas follows the
 * viewport instead of always occupying the full-screen 70vh viewport.
 */
const EMBEDDED_CANVAS_HEIGHT_CLASS = "h-[calc(100vh-20rem)] min-h-[420px]"

type EmbeddedStatusBarProps = {
  streamStatus: SSEStatus
  loading: boolean
  onRefresh: () => void
}

/**
 * Compact status strip of the embedded tab (spec §7.1): the tab drops the
 * full-screen header, but the stream state and a manual refresh must stay
 * visible. It intentionally carries no title, back link or baseline badge.
 */
function EmbeddedStatusBar({ streamStatus, loading, onRefresh }: EmbeddedStatusBarProps) {
  const meta = STREAM_STATUS_META[streamStatus]
  return (
    <div
      data-testid="mindmap-embedded-status"
      className="flex flex-wrap items-center justify-end gap-1.5 rounded-xl border border-border bg-card px-3 py-2"
    >
      <span
        data-testid="stream-status"
        data-status={streamStatus}
        className="inline-flex items-center gap-1.5 rounded-full border border-border bg-card px-2.5 py-1 text-[11px] font-medium"
      >
        <StatusBadge variant={meta.variant}>{meta.label}</StatusBadge>
      </span>
      <button
        type="button"
        data-testid="btn-refresh-mindmap"
        onClick={onRefresh}
        disabled={loading}
        className={SECONDARY_BUTTON_CLASS}
      >
        {loading ? (
          <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
        ) : (
          <RefreshCw className="h-4 w-4" aria-hidden="true" />
        )}
        刷新
      </button>
    </div>
  )
}

/** Number of nodes removed together with a scenario (delete confirmation copy). */
function countDescendants(nodes: readonly CaseNodeView[], rootId: string): number {
  const childrenByParent = new Map<string, string[]>()
  for (const node of nodes) {
    if (node.parentId === null) continue
    const siblings = childrenByParent.get(node.parentId)
    if (siblings === undefined) childrenByParent.set(node.parentId, [node.id])
    else siblings.push(node.id)
  }

  let count = 0
  const stack = [...(childrenByParent.get(rootId) ?? [])]
  while (stack.length > 0) {
    const nodeId = stack.pop()
    if (nodeId === undefined) continue
    count += 1
    const children = childrenByParent.get(nodeId)
    if (children !== undefined) stack.push(...children)
  }
  return count
}

type CollapsibleSectionProps = {
  testId: string
  title: string
  description: string
  children: ReactNode
}

/**
 * One collapsible panel below the workspace (spec §9.2). Children stay unmounted
 * while collapsed, so a collapsed review/run panel costs no extra API reads.
 */
function CollapsibleSection({ testId, title, description, children }: CollapsibleSectionProps) {
  const [open, setOpen] = useState(true)
  const contentId = useId()

  return (
    <section
      data-testid={testId}
      className="min-w-0 rounded-2xl border border-dashed border-border bg-card/40 p-2"
    >
      <div className="flex flex-wrap items-center justify-between gap-2 px-1">
        <button
          type="button"
          data-testid={`${testId}-toggle`}
          aria-expanded={open}
          aria-controls={contentId}
          onClick={() => setOpen((current) => !current)}
          className="inline-flex items-center gap-1.5 rounded-lg px-1.5 py-1 text-sm font-semibold transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          {open ? (
            <ChevronDown className="h-4 w-4" aria-hidden="true" />
          ) : (
            <ChevronRight className="h-4 w-4" aria-hidden="true" />
          )}
          {title}
        </button>
        <span className="text-[11px] text-muted-foreground">{description}</span>
      </div>
      {open ? (
        <div id={contentId} className="mt-2">
          {children}
        </div>
      ) : null}
    </section>
  )
}

export type MindmapPageProps = {
  /** P3-owned version id from the dynamic route segment; never re-parsed. */
  versionId: string
  /**
   * Embedded version-detail tab mode (spec §7.1): drops the full-screen header
   * and the back link, keeps the canvas/review/run/export surfaces and lets the
   * height follow the tab container instead of the viewport.
   */
  embedded?: boolean
  /** `?case=` deep link: the node selected on first render (may be unknown). */
  initialCaseId?: string
  /** Canvas selection changes, so the container can write `?case=` back. */
  onCaseChange?: (caseId: string) => void
  /**
   * Version stream owned by the container (the embedded tab reuses the version
   * detail's single SSE connection instead of opening another one).
   */
  stream?: VersionStreamState
  /**
   * Already-loaded version from the container. The embedded tab must not issue
   * a second `GET /versions/{id}`; the full-screen route still loads its own.
   */
  version?: Version
}

/**
 * Case-mindmap page assembly (spec §9.1/§9.2, P8 §7.1): toolbar, canvas,
 * selected-node editor and the collapsible review/run/export panels. The page
 * is a pure assembly — the whole graph state comes from `useMindmap`, every
 * write is server-authoritative and re-reads through `refresh()`, editability is
 * never inferred from the version status, and a frozen baseline only turns the
 * editing surface read-only (review runs and exports stay available).
 *
 * The same component backs the full-screen route and the version-detail tab:
 * `embedded` removes only the page chrome, and `initialCaseId`/`onCaseChange`
 * carry the `?case=` deep link without the page owning the URL.
 */
export function MindmapPage({
  versionId,
  embedded = false,
  initialCaseId = "",
  onCaseChange,
  stream,
  version: versionProp,
}: MindmapPageProps) {
  const { graph, loading, error, streamStatus, refresh } = useMindmap(versionId, stream)
  const { user } = useAuth()
  const isAdmin = user?.role === "admin"

  const [version, setVersion] = useState<Version | null>(versionProp ?? null)
  const [selectedId, setSelectedId] = useState<string | null>(
    initialCaseId === "" ? null : initialCaseId,
  )
  /** Deep-link id kept after selection so an unknown one can show a notice. */
  const [requestedCaseId, setRequestedCaseId] = useState(initialCaseId)
  const [collapsed, setCollapsed] = useState<ReadonlySet<string>>(new Set<string>())
  const [notice, setNotice] = useState("")
  const [actionError, setActionError] = useState("")
  const [generating, setGenerating] = useState(false)
  const [manualOpen, setManualOpen] = useState(false)
  const [manualKind, setManualKind] = useState<CaseNodeKind>("case")
  const [manualTitle, setManualTitle] = useState("")
  const [creating, setCreating] = useState(false)

  const svgRef = useRef<SVGSVGElement | null>(null)
  const manualKindId = useId()
  const manualTitleId = useId()

  // The version detail feeds the toolbar title, the PNG filename and the
  // system/environment bindings of the explore run. It is supplementary: a
  // failed lookup falls back to the version id and disables explore. The
  // embedded tab receives the container's already-loaded version instead of
  // paying for a second read of the same resource.
  useEffect(() => {
    if (versionProp !== undefined) {
      setVersion(versionProp)
      return
    }
    let cancelled = false
    getVersion(versionId)
      .then((loaded) => {
        if (!cancelled) setVersion(loaded)
      })
      .catch(() => {
        // Non-fatal: the graph read below is the authoritative load state.
      })
    return () => {
      cancelled = true
    }
  }, [versionId, versionProp])

  const selectNode = useCallback(
    (nodeId: string) => {
      setSelectedId(nodeId)
      // Any explicit selection supersedes (and dismisses) a stale deep link.
      setRequestedCaseId("")
      onCaseChange?.(nodeId)
    },
    [onCaseChange],
  )

  const toggleCollapsed = useCallback((nodeId: string) => {
    setCollapsed((current) => {
      const next = new Set(current)
      if (next.has(nodeId)) next.delete(nodeId)
      else next.add(nodeId)
      return next
    })
  }, [])

  const selectedNode = useMemo(() => {
    if (graph === null || selectedId === null) return null
    return graph.nodes.find((node) => node.id === selectedId) ?? null
  }, [graph, selectedId])

  const reload = useCallback(() => {
    void refresh()
  }, [refresh])

  async function handleGenerate(): Promise<void> {
    if (generating) return
    setGenerating(true)
    setActionError("")
    setNotice("")
    try {
      const result = await mindmapApi.startRun(versionId, { kind: "design", mode: "append" })
      setNotice(`已提交 AI 生成运行（${result.runId}），完成后脑图会自动刷新。`)
      await refresh()
    } catch (caught) {
      setActionError(describeMindmapError(caught))
    } finally {
      setGenerating(false)
    }
  }

  async function handleCreate(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault()
    const title = manualTitle.trim()
    if (title === "") {
      setActionError("请填写节点标题。")
      return
    }

    setCreating(true)
    setActionError("")
    setNotice("")
    const firstRequirementId = graph?.coverage.requirementIds[0]
    try {
      const created = await mindmapApi.createNode(versionId, {
        kind: manualKind,
        parentId: null,
        title,
        ...(manualKind === "case" && firstRequirementId !== undefined
          ? { requirementIds: [firstRequirementId] }
          : {}),
      })
      setManualTitle("")
      setManualOpen(false)
      setSelectedId(created.id)
      setRequestedCaseId("")
      onCaseChange?.(created.id)
      setNotice(`已创建${manualKind === "case" ? "用例" : "场景"}「${created.title}」。`)
      await refresh()
    } catch (caught) {
      setActionError(describeMindmapError(caught))
    } finally {
      setCreating(false)
    }
  }

  const streamBadge = STREAM_STATUS_META[streamStatus]
  const versionLabel = version?.name ?? versionId
  // The embedded tab follows the tab content instead of owning the full-screen
  // page; its canvas column carries the explicit height (spec §7.1).
  const rootClass = embedded ? "flex min-h-0 flex-col space-y-3" : "space-y-3"

  if (graph === null) {
    return (
      <div
        data-testid="mindmap-page"
        data-embedded={embedded ? "true" : undefined}
        className={rootClass}
      >
        {embedded ? null : (
          <header
            data-testid="mindmap-toolbar"
            className="flex flex-wrap items-start justify-between gap-3 rounded-2xl border border-border bg-card p-4"
          >
            <div className="min-w-0">
              <p className="text-xs font-medium text-muted-foreground">用例脑图</p>
              <h1 className="text-xl font-semibold tracking-tight">{versionLabel}</h1>
            </div>
            <span
              data-testid="stream-status"
              data-status={streamStatus}
              className="inline-flex items-center gap-1.5 rounded-full border border-border bg-card px-2.5 py-1 text-[11px] font-medium text-muted-foreground"
            >
              <StatusBadge variant={streamBadge.variant}>{streamBadge.label}</StatusBadge>
            </span>
          </header>
        )}

        {embedded ? (
          <EmbeddedStatusBar streamStatus={streamStatus} loading={loading} onRefresh={reload} />
        ) : null}

        {error === "" ? (
          <p
            data-testid="mindmap-loading"
            role="status"
            className="flex items-center gap-2 rounded-xl border border-dashed border-border px-4 py-10 text-sm text-muted-foreground"
          >
            <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
            正在加载用例脑图…
          </p>
        ) : (
          <div
            data-testid="mindmap-error"
            role="alert"
            className="space-y-3 rounded-xl border border-destructive/30 bg-destructive/10 px-4 py-6 text-sm text-destructive"
          >
            <p>{`加载用例脑图失败：${error}`}</p>
            <button type="button" onClick={reload} className={SECONDARY_BUTTON_CLASS}>
              重试
            </button>
          </div>
        )}
      </div>
    )
  }

  const caseCount = graph.nodes.filter((node) => node.kind === "case").length
  const scenarioCount = graph.nodes.length - caseCount
  const requirementCount = graph.coverage.requirementIds.length
  const uncoveredCount = graph.coverage.uncoveredRequirementIds.length
  const noNodes = graph.nodes.length === 0
  const noRequirements = requirementCount === 0
  const reviewExportAllowed = REVIEW_EXPORT_STATUSES.has(graph.versionStatus)
  // A `?case=` node that is not in the graph (deleted, renamed, other version)
  // is a dead link: tell the user instead of rendering an editor placeholder
  // that silently disagrees with the address bar.
  const caseNotFound =
    requestedCaseId !== "" && !graph.nodes.some((node) => node.id === requestedCaseId)

  return (
    <div
      data-testid="mindmap-page"
      data-embedded={embedded ? "true" : undefined}
      className={rootClass}
    >
      {embedded ? null : (
        <header
          data-testid="mindmap-toolbar"
          className="flex flex-wrap items-start justify-between gap-3 rounded-2xl border border-border bg-card p-4"
        >
          <div className="min-w-0 space-y-1">
            <Link
              href={`/versions/${encodeURIComponent(versionId)}`}
              data-testid="back-to-version"
              className="inline-flex items-center gap-1 text-xs font-medium text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              ← 返回版本详情
            </Link>

            <div className="flex flex-wrap items-center gap-2">
              <h1 className="text-xl font-semibold tracking-tight">用例脑图</h1>
              <StatusBadge variant={versionStatusVariant(graph.versionStatus)}>
                {versionStatusLabel(graph.versionStatus)}
              </StatusBadge>
              {graph.baseline === null ? null : (
                <span
                  data-testid="toolbar-baseline"
                  className="rounded-full border border-sky-500/30 bg-sky-500/10 px-2 py-0.5 text-[11px] font-medium text-sky-700 dark:text-sky-300"
                >
                  {`已绑定基线 · ${graph.baseline.caseCount} 个用例`}
                </span>
              )}
            </div>

            <p
              data-testid="mindmap-version-name"
              className="truncate text-sm text-muted-foreground"
            >
              {versionLabel}
            </p>

            <p className="flex flex-wrap gap-x-3 gap-y-1 text-xs text-muted-foreground">
              <span>{`用例 ${caseCount}`}</span>
              <span>{`场景 ${scenarioCount}`}</span>
              <span>{`已确认需求 ${requirementCount}`}</span>
              {uncoveredCount === 0 ? null : (
                <span className="text-amber-700 dark:text-amber-300">
                  {`未覆盖需求 ${uncoveredCount}`}
                </span>
              )}
            </p>
          </div>

          <div className="flex flex-wrap items-center gap-1.5">
            <span
              data-testid="stream-status"
              data-status={streamStatus}
              className="inline-flex items-center gap-1.5 rounded-full border border-border bg-card px-2.5 py-1 text-[11px] font-medium"
            >
              <StatusBadge variant={streamBadge.variant}>{streamBadge.label}</StatusBadge>
            </span>
            <button
              type="button"
              data-testid="btn-refresh-mindmap"
              onClick={reload}
              disabled={loading}
              className={SECONDARY_BUTTON_CLASS}
            >
              {loading ? (
                <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
              ) : (
                <RefreshCw className="h-4 w-4" aria-hidden="true" />
              )}
              刷新
            </button>
          </div>
        </header>
      )}

      {embedded ? (
        <EmbeddedStatusBar streamStatus={streamStatus} loading={loading} onRefresh={reload} />
      ) : null}

      {graph.baseline !== null ? (
        <p
          data-testid="readonly-banner"
          role="status"
          className="rounded-xl border border-amber-500/25 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300"
        >
          基线已冻结：脑图与用例编辑为只读，仍可发起评审运行与导出。
        </p>
      ) : graph.editable ? null : (
        <p
          data-testid="status-banner"
          role="status"
          className="rounded-xl border border-amber-500/25 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300"
        >
          {statusBannerCopy(graph.versionStatus)}
        </p>
      )}

      {error === "" ? null : (
        <div
          data-testid="mindmap-refresh-error"
          role="alert"
          className="flex flex-wrap items-center justify-between gap-2 rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
        >
          <span>{`刷新失败：${error}（正在展示上次数据）`}</span>
          <button type="button" onClick={reload} className={SECONDARY_BUTTON_CLASS}>
            重试
          </button>
        </div>
      )}

      {actionError === "" ? null : (
        <p
          role="alert"
          className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
        >
          {actionError}
        </p>
      )}
      {notice === "" ? null : (
        <p
          role="status"
          className="rounded-xl border border-emerald-500/30 bg-emerald-500/10 px-3 py-2 text-xs text-emerald-700 dark:text-emerald-300"
        >
          {notice}
        </p>
      )}

      {caseNotFound ? (
        <p
          data-testid="mindmap-case-not-found"
          role="status"
          className="rounded-xl border border-amber-500/25 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300"
        >
          {`未找到链接中指定的脑图节点（${requestedCaseId}），可能已被删除；请从画布重新选择。`}
        </p>
      ) : null}

      {noNodes || !noRequirements ? null : (
        <p
          data-testid="requirements-hint"
          className="rounded-xl border border-amber-500/25 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300"
        >
          当前版本没有已确认需求，用例来源与覆盖无法维护。请先在
          <Link
            href={`/versions/${encodeURIComponent(versionId)}?tab=requirements`}
            className="mx-0.5 font-medium underline"
          >
            需求与澄清
          </Link>
          确认需求。
        </p>
      )}

      <div className="grid min-w-0 gap-3 xl:grid-cols-[minmax(0,1fr)_24rem]">
        <div
          data-testid="mindmap-canvas-column"
          className={
            embedded ? `flex min-w-0 flex-col ${EMBEDDED_CANVAS_HEIGHT_CLASS}` : "min-w-0 space-y-3"
          }
        >
          {noNodes ? (
            <section
              data-testid="mindmap-empty"
              className="rounded-2xl border border-dashed border-border bg-card/60 p-4 sm:p-6"
            >
              {noRequirements ? (
                <>
                  <h2 className="text-base font-semibold">还没有已确认需求</h2>
                  <p className="mt-2 max-w-2xl text-sm text-muted-foreground">
                    用例脑图基于已确认需求生成或手工编写；请先在「需求与澄清」中录入并确认需求。
                  </p>
                  <Link
                    href={`/versions/${encodeURIComponent(versionId)}?tab=requirements`}
                    data-testid="goto-requirements"
                    className={`${PRIMARY_BUTTON_CLASS} mt-4`}
                  >
                    去需求与澄清
                  </Link>
                </>
              ) : (
                <>
                  <h2 className="text-base font-semibold">还没有用例脑图</h2>
                  <p className="mt-2 max-w-2xl text-sm text-muted-foreground">
                    可以用 AI 从已确认需求生成整版脑图，也可以手工新建第一个场景或用例。
                  </p>
                  <div className="mt-4 flex flex-wrap gap-2">
                    <button
                      type="button"
                      data-testid="btn-empty-generate"
                      onClick={() => void handleGenerate()}
                      disabled={generating || !graph.editable}
                      className={PRIMARY_BUTTON_CLASS}
                    >
                      {generating ? (
                        <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
                      ) : null}
                      {generating ? "正在提交…" : "AI 生成"}
                    </button>
                    <button
                      type="button"
                      data-testid="btn-empty-manual"
                      aria-expanded={manualOpen}
                      onClick={() => setManualOpen((current) => !current)}
                      disabled={!graph.editable}
                      className={SECONDARY_BUTTON_CLASS}
                    >
                      <Plus className="h-4 w-4" aria-hidden="true" />
                      手工新建
                    </button>
                  </div>

                  {manualOpen ? (
                    <form
                      data-testid="manual-create-form"
                      onSubmit={(event) => void handleCreate(event)}
                      className="mt-4 grid gap-3 rounded-xl border border-border bg-card p-3 sm:grid-cols-[8rem_minmax(0,1fr)_auto] sm:items-end"
                    >
                      <div>
                        <label
                          htmlFor={manualKindId}
                          className="text-xs font-medium text-muted-foreground"
                        >
                          节点类型
                        </label>
                        <select
                          id={manualKindId}
                          value={manualKind}
                          disabled={creating}
                          onChange={(event) => setManualKind(event.target.value as CaseNodeKind)}
                          className={FIELD_CLASS}
                        >
                          <option value="scenario">场景</option>
                          <option value="case">用例</option>
                        </select>
                      </div>
                      <div>
                        <label
                          htmlFor={manualTitleId}
                          className="text-xs font-medium text-muted-foreground"
                        >
                          节点标题
                        </label>
                        <input
                          id={manualTitleId}
                          value={manualTitle}
                          autoComplete="off"
                          disabled={creating}
                          onChange={(event) => setManualTitle(event.target.value)}
                          className={FIELD_CLASS}
                        />
                      </div>
                      <button
                        type="submit"
                        data-testid="btn-manual-create"
                        disabled={creating || manualTitle.trim() === ""}
                        className={PRIMARY_BUTTON_CLASS}
                      >
                        {creating ? "创建中…" : "创建"}
                      </button>
                    </form>
                  ) : null}
                </>
              )}
            </section>
          ) : (
            <MindmapCanvas
              graph={graph}
              selectedId={selectedId}
              onSelect={selectNode}
              collapsed={collapsed}
              onToggle={toggleCollapsed}
              svgRef={svgRef}
              embedded={embedded}
            />
          )}
        </div>

        <aside aria-label="节点编辑" className="min-w-0 space-y-3">
          {selectedNode === null ? (
            <section
              data-testid="editor-placeholder"
              className="rounded-2xl border border-dashed border-border bg-card/60 p-4"
            >
              <h2 className="text-sm font-semibold">节点编辑</h2>
              <p className="mt-2 text-xs text-muted-foreground">
                从画布中选择一个场景或用例进行编辑；来源与代码锚点在用例下维护。
              </p>
            </section>
          ) : selectedNode.kind === "case" ? (
            <CaseEditor
              versionId={versionId}
              node={selectedNode}
              editable={graph.editable}
              onSaved={(node) => {
                if (node !== undefined) setSelectedId(node.id)
                reload()
              }}
              onDeleted={() => {
                setSelectedId(null)
                setRequestedCaseId("")
                onCaseChange?.("")
                reload()
              }}
              onRefresh={reload}
              isAdmin={isAdmin}
            />
          ) : (
            <ScenarioEditor
              versionId={versionId}
              node={selectedNode}
              editable={graph.editable}
              childCount={countDescendants(graph.nodes, selectedNode.id)}
              onSaved={(node) => {
                if (node !== undefined) setSelectedId(node.id)
                reload()
              }}
              onDeleted={() => {
                setSelectedId(null)
                setRequestedCaseId("")
                onCaseChange?.("")
                reload()
              }}
              onRefresh={reload}
            />
          )}

          {selectedNode === null ? null : (
            <SourcePanel
              versionId={versionId}
              node={selectedNode}
              editable={graph.editable}
              systemId={version?.systemId ?? null}
              onChanged={reload}
            />
          )}
        </aside>
      </div>

      <CollapsibleSection
        testId="mindmap-section-review"
        title="设计评审"
        description="评审发现项、批准与驳回"
      >
        <ReviewPanel
          versionId={versionId}
          graph={graph}
          disabled={!reviewExportAllowed}
          onChanged={reload}
        />
      </CollapsibleSection>

      <CollapsibleSection
        testId="mindmap-section-runs"
        title="AI 运行"
        description="生成、评审与数据规划"
      >
        <fieldset disabled={!graph.editable} className="m-0 min-w-0 border-0 p-0">
          <RunPanel
            versionId={versionId}
            graph={graph}
            systemId={version?.systemId ?? null}
            environmentId={version?.environmentId ?? null}
            onChanged={reload}
          />
        </fieldset>
      </CollapsibleSection>

      <CollapsibleSection
        testId="mindmap-section-export"
        title="导出"
        description="Markdown / Mermaid / OPML / PNG"
      >
        <ExportMenu
          versionId={versionId}
          svgRef={svgRef}
          versionName={versionLabel}
          baseline={graph.baseline}
          disabled={noNodes || !reviewExportAllowed}
          disabledReason={
            reviewExportAllowed
              ? undefined
              : "当前版本状态不开放导出（仅设计、评审、执行阶段开放）。"
          }
        />
      </CollapsibleSection>
    </div>
  )
}
