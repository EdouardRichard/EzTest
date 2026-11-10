"use client"

import { ArrowLeft, Database, FileCode2, Server } from "lucide-react"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { type ReactNode, useCallback, useEffect, useMemo, useRef, useState } from "react"
import { Alert } from "@/components/ui/Alert"
import { Badge } from "@/components/ui/Badge"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState, InlineError } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import { Tabs } from "@/components/ui/Tabs"
import { useIsAdmin } from "@/features/auth/useIsAdmin"
import { ChainTab } from "@/features/chain/ChainTab"
import {
  isClarificationQuestionBatch,
  isHumanDecisionKind,
  listQuestions,
  listVersionAttachments,
  type QuestionBatch,
} from "@/features/clarification/api"
import { QuestionCard } from "@/features/clarification/QuestionCard"
import { RequirementsTab } from "@/features/clarification/RequirementsTab"
import type { VersionAttachment } from "@/features/clarification/types"
import { useVersionEvents } from "@/features/clarification/useVersionEvents"
import { CodeTab } from "@/features/code-intel/CodeTab"
import { ContextPacketCard } from "@/features/conversation/ContextPacketCard"
import { listIssues, listReports } from "@/features/delivery/api"
import { IssuesTab } from "@/features/delivery/IssuesTab"
import { ReportsTab } from "@/features/delivery/ReportsTab"
import { rollbackCaseVersion } from "@/features/design/api"
import { DesignTab } from "@/features/design/DesignTab"
import {
  DiscoveryRevisionNotice,
  discoveryRevisionFromEventPayload,
} from "@/features/design/DiscoveryRevisionNotice"
import type {
  CaseRevisionRollbackView,
  DiscoveryRevisionData,
  StageView,
} from "@/features/design/types"
import { listHumanRequests, resolveHumanRequest } from "@/features/execution/api"
import { ExecutionTab } from "@/features/execution/ExecutionTab"
import { HumanDecisionCards } from "@/features/execution/HumanRequestBanner"
import type { HumanRequest, HumanRequestResponseInput } from "@/features/execution/types"
import { TodoDrawer } from "@/features/shell/TodoDrawer"
import { apiDownload, apiGetPage, apiPost } from "@/lib/api-client"
import { formatDateTime } from "@/lib/format"

import { getVersion, getVersionStage, parseStageBlocker } from "./api"
import { BindingOverview } from "./BindingOverview"
import { BlockerPanel } from "./BlockerPanel"
import { CompletionBlockersPanel } from "./CompletionBlockersPanel"
import { CoveragePanel } from "./CoveragePanel"
import { DiscoveryPanel } from "./DiscoveryPanel"
import { FactsPanel } from "./FactsPanel"
import { PlanPanel } from "./PlanPanel"
import { StageBar } from "./StageBar"
import { parseVersionTab, VERSION_TABS } from "./tabs"
import type { Version, VersionBinding, VersionTab } from "./types"
import { useShortViewport } from "./useShortViewport"
import { VersionHeader } from "./VersionHeader"
import { WorkItemsPanel } from "./WorkItemsPanel"

/** SSE bursts (issue.created + report.published + version.closed) collapse into one refresh. */
const SSE_REFRESH_DEBOUNCE_MS = 350

type CountProps = {
  icon: ReactNode
  label: string
  value: string
}

function OverviewStat({ icon, label, value }: CountProps) {
  return (
    <div className="rounded-lg border border-border bg-card px-3 py-2.5">
      <dt className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
        <span className="text-muted-foreground" aria-hidden="true">
          {icon}
        </span>
        {label}
      </dt>
      <dd className="mt-0.5 text-sm font-semibold text-foreground">{value}</dd>
    </div>
  )
}

function BindingCard({ binding, index }: { binding: VersionBinding; index: number }) {
  const repositoryName = binding.repository?.name ?? "未绑定代码仓"
  return (
    <article className="surface-card p-4">
      <header className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <h2 className="text-sm font-semibold text-foreground">
            绑定 {index + 1} · {binding.sut.name}
          </h2>
          <p className="mt-0.5 text-[11px] text-muted-foreground">
            {binding.environment.name} · {binding.environment.base_url}
          </p>
        </div>
        <Badge tone={binding.environment.status === "active" ? "success" : "neutral"}>
          {binding.environment.status === "active" ? "环境启用" : "环境停用"}
        </Badge>
      </header>

      <dl className="mt-3 grid grid-cols-2 gap-2 text-[11px] sm:grid-cols-4">
        <div>
          <dt className="text-muted-foreground">页面</dt>
          <dd className="font-medium text-foreground">{binding.pages.length} 个页面</dd>
        </div>
        <div>
          <dt className="text-muted-foreground">API</dt>
          <dd className="font-medium text-foreground">{binding.apis.length} 个 API</dd>
        </div>
        <div>
          <dt className="text-muted-foreground">目标库</dt>
          <dd className="font-medium text-foreground">{binding.databases.length} 个目标库</dd>
        </div>
        <div>
          <dt className="text-muted-foreground">测试账号</dt>
          <dd className="font-medium text-foreground">{binding.accounts.length} 个测试账号</dd>
        </div>
      </dl>

      <div className="mt-3 flex flex-wrap items-center gap-2 border-t border-border pt-3 text-[11px]">
        <span className="text-muted-foreground">代码仓</span>
        <span className="font-medium text-foreground">{repositoryName}</span>
        {binding.pages.length > 0 ? (
          <span className="text-muted-foreground">
            页面范围：{binding.pages.map((page) => page.name).join("、")}
          </span>
        ) : null}
      </div>
    </article>
  )
}

function VersionOverview({
  version,
  showBindings = true,
}: {
  version: Version
  showBindings?: boolean
}) {
  const totals = version.bindings.reduce(
    (sum, binding) => ({
      pages: sum.pages + binding.pages.length,
      apis: sum.apis + binding.apis.length,
      databases: sum.databases + binding.databases.length,
      accounts: sum.accounts + binding.accounts.length,
      repositories: sum.repositories + (binding.repository ? 1 : 0),
    }),
    { pages: 0, apis: 0, databases: 0, accounts: 0, repositories: 0 },
  )

  return (
    <div className="space-y-4">
      <dl className="grid grid-cols-2 gap-2 sm:grid-cols-5">
        <OverviewStat
          icon={<FileCode2 className="size-3.5" />}
          label="页面"
          value={`${totals.pages} 个页面`}
        />
        <OverviewStat
          icon={<Server className="size-3.5" />}
          label="API"
          value={`${totals.apis} 个 API`}
        />
        <OverviewStat
          icon={<Database className="size-3.5" />}
          label="目标库"
          value={`${totals.databases} 个目标库`}
        />
        <OverviewStat
          icon={<Database className="size-3.5" />}
          label="测试账号"
          value={`${totals.accounts} 个测试账号`}
        />
        <OverviewStat
          icon={<FileCode2 className="size-3.5" />}
          label="代码仓"
          value={`${totals.repositories} 个代码仓`}
        />
      </dl>

      {totals.pages === 0 ? (
        <Alert tone="warning" title="该版本未绑定页面">
          未绑定页面时，AI 设计拿不到页面清单与 locator，执行也无法导航 SUT。请先在「资源」
          创建页面/API（或等待探索发现后刷新创建向导），再新建版本并勾选页面/API。
          <div className="mt-1">
            <Link href="/resources?tab=pages" className="font-medium underline underline-offset-2">
              前往资源页创建或确认发现的页面 →
            </Link>
          </div>
        </Alert>
      ) : null}

      {showBindings ? (
        <section aria-label="绑定概览" className="space-y-3">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <h2 className="text-sm font-semibold text-foreground">SUT 绑定概览</h2>
            <span className="text-[11px] text-muted-foreground">
              共 {version.bindings.length} 个 SUT · 更新于 {formatDateTime(version.updated_at)}
            </span>
          </div>

          {version.bindings.length === 0 ? (
            <EmptyState
              title="该版本暂无 SUT 绑定"
              description="P0 创建接口要求至少一个绑定，请检查数据或重新创建版本。"
            />
          ) : (
            <div className="grid gap-3 lg:grid-cols-2">
              {version.bindings.map((binding, index) => (
                <BindingCard key={binding.id} binding={binding} index={index} />
              ))}
            </div>
          )}
        </section>
      ) : null}

      <section className="rounded-xl border border-dashed border-border bg-card/60 px-4 py-3 text-xs leading-relaxed text-muted-foreground">
        需求与澄清（P1）、用例设计（P2）、执行（P3）、探索链路/问题/报告（P4）与代码与变更（P5）
        已在对应 Tab 提供。
      </section>
    </div>
  )
}

const ATTACHMENT_STATUS_LABEL: Record<string, string> = {
  pending: "待解析",
  processing: "解析中",
  done: "已解析",
  failed: "解析失败",
  skipped: "已跳过",
}

function attachmentStatusTone(
  status: string,
): "neutral" | "info" | "success" | "warning" | "danger" {
  if (status === "done") return "success"
  if (status === "failed") return "danger"
  if (status === "processing") return "info"
  if (status === "skipped") return "warning"
  return "neutral"
}

const IMAGE_STATUS_LABEL: Record<string, string> = {
  pending: "待分析",
  processing: "分析中",
  done: "成功",
  failed: "失败",
  skipped: "跳过",
}

function imageStatusTone(status: string): "neutral" | "info" | "success" | "warning" | "danger" {
  if (status === "done") return "success"
  if (status === "failed") return "danger"
  if (status === "skipped") return "warning"
  if (status === "processing") return "info"
  return "neutral"
}

/**
 * C-05 version attachment panel entry: a read-only list of the version's
 * attachments (status + image count + extraction reason). Every row links into
 * the existing `?tab=overview&attachment={id}` image panel, so the user can see
 * captions without guessing an attachment ID.
 */
function VersionAttachmentsSection({
  versionId,
  refreshKey,
  onSelect,
}: {
  versionId: string
  refreshKey: number
  onSelect: (attachmentId: string) => void
}) {
  const [attachments, setAttachments] = useState<VersionAttachment[] | null>(null)
  const [error, setError] = useState<unknown>(null)

  // biome-ignore lint/correctness/useExhaustiveDependencies: refreshKey is the SSE nonce.
  useEffect(() => {
    let cancelled = false
    setError(null)
    void listVersionAttachments(versionId).then(
      (rows) => {
        if (!cancelled) setAttachments(rows)
      },
      (cause) => {
        if (cancelled) return
        setAttachments(null)
        setError(cause)
      },
    )
    return () => {
      cancelled = true
    }
  }, [versionId, refreshKey])

  return (
    <section
      data-testid="version-attachments"
      aria-label="版本附件"
      className="surface-card space-y-2 px-3 py-2.5"
    >
      <header className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h2 className="text-sm font-semibold text-foreground">附件</h2>
          <p className="text-[11px] text-muted-foreground">
            版本上传的文档与截图；点击查看图片分析结果与 caption（只读）
          </p>
        </div>
        <span className="text-[11px] text-muted-foreground">
          {attachments === null ? "—" : `${attachments.length} 个附件`}
        </span>
      </header>
      {error !== null ? <InlineError error={error} /> : null}
      {attachments === null && error === null ? (
        <LoadingCard label="正在加载版本附件…" />
      ) : attachments !== null && attachments.length === 0 ? (
        <p data-testid="version-attachments-empty" className="text-[11px] text-muted-foreground">
          该版本暂无附件
        </p>
      ) : (
        <ul className="divide-y divide-border">
          {(attachments ?? []).map((attachment) => (
            <li key={attachment.id} data-testid={`version-attachment-${attachment.id}`}>
              <a
                href={`/versions/${encodeURIComponent(versionId)}?tab=overview&attachment=${encodeURIComponent(attachment.id)}`}
                onClick={(event) => {
                  event.preventDefault()
                  onSelect(attachment.id)
                }}
                className="flex flex-wrap items-center gap-x-2 gap-y-1 py-2 text-[11px] text-foreground hover:text-primary"
              >
                <span className="font-medium">{attachment.name}</span>
                <span className="text-muted-foreground">#{attachment.id}</span>
                <Badge tone={attachmentStatusTone(attachment.extraction_status)}>
                  {ATTACHMENT_STATUS_LABEL[attachment.extraction_status] ??
                    attachment.extraction_status}
                </Badge>
                <span className="text-muted-foreground">{attachment.image_count} 张图片</span>
                {attachment.error_text !== "" ? (
                  <span className="text-destructive">{attachment.error_text}</span>
                ) : null}
                {attachment.image_count > 0 ? (
                  <span className="ml-auto text-muted-foreground">查看图片 →</span>
                ) : null}
              </a>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

type AttachmentImageRow = {
  id: string
  attachment_id: string
  seq: number
  sha256: string
  url?: string
  mime: string
  width?: number
  height?: number
  status: string
  caption: string
  error: string
  created_at?: string
  updated_at?: string
}

/**
 * Authenticated "查看原图" link. The content endpoint needs the bearer token, so
 * a plain `<a href="/api/content/...">` would 401; this fetches the blob and
 * exposes a revocable object URL instead (same pattern as MessageImage).
 */
function AttachmentImageLink({ sha256 }: { sha256: string }) {
  const [url, setUrl] = useState<string | null>(null)
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    let cancelled = false
    let objectUrl: string | null = null
    setUrl(null)
    setFailed(false)
    void apiDownload(`/api/content/${encodeURIComponent(sha256)}`).then(
      (download) => {
        if (cancelled) return
        objectUrl = URL.createObjectURL(download.blob)
        setUrl(objectUrl)
      },
      () => {
        if (!cancelled) setFailed(true)
      },
    )
    return () => {
      cancelled = true
      if (objectUrl !== null) URL.revokeObjectURL(objectUrl)
    }
  }, [sha256])

  if (failed) {
    return <span className="mt-1 inline-block text-muted-foreground">原图加载失败</span>
  }
  if (url === null) {
    return <span className="mt-1 inline-block text-muted-foreground">原图加载中…</span>
  }
  return (
    <a
      className="mt-1 inline-block underline underline-offset-2"
      data-testid="attachment-image-link"
      href={url}
      rel="noreferrer"
      target="_blank"
    >
      查看原图
    </a>
  )
}

/**
 * H7 attachment-image panel: the page only reads images for the attachment
 * selected by `?attachment=` and always through the version-scoped route, so
 * another version's attachment can never be fetched by guessing an ID.
 */
function AttachmentImagesPanel({
  versionId,
  attachmentId,
  refreshKey = 0,
}: {
  versionId: string
  attachmentId: string
  refreshKey?: number
}) {
  const [images, setImages] = useState<AttachmentImageRow[] | null>(null)
  const [error, setError] = useState<unknown>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    setError(null)
    try {
      const page = await apiGetPage<AttachmentImageRow>(
        `/api/versions/${encodeURIComponent(versionId)}/attachments/${encodeURIComponent(attachmentId)}/images?limit=100`,
      )
      setImages(page.items)
    } catch (cause) {
      setImages(null)
      setError(cause)
    }
  }, [versionId, attachmentId])

  // biome-ignore lint/correctness/useExhaustiveDependencies: refreshKey is the SSE nonce.
  useEffect(() => {
    void load()
  }, [load, refreshKey])

  async function reanalyze(): Promise<void> {
    setBusy(true)
    setError(null)
    setNotice(null)
    try {
      const rows = await apiPost<AttachmentImageRow[]>(
        `/api/versions/${encodeURIComponent(versionId)}/attachments/${encodeURIComponent(attachmentId)}/reanalyze`,
      )
      if (!Array.isArray(rows)) {
        await load()
        return
      }
      setImages(rows)
      // C-08: the POST response is the refreshed image list, so the summary and
      // the per-row statuses/time update without a second read.
      const done = rows.filter((image) => image.status === "done").length
      const failed = rows.filter((image) => image.status === "failed").length
      const skipped = rows.filter((image) => image.status === "skipped").length
      const latest = rows.reduce<string | undefined>((newest, image) => {
        if (image.updated_at === undefined || image.updated_at === "") return newest
        if (newest === undefined) return image.updated_at
        return Date.parse(image.updated_at) > Date.parse(newest) ? image.updated_at : newest
      }, undefined)
      setNotice(
        `已重新分析：${done} 成功 / ${failed} 失败 / ${skipped} 跳过（最近更新 ${formatDateTime(latest)}）`,
      )
    } catch (cause) {
      setError(cause)
    } finally {
      setBusy(false)
    }
  }

  return (
    <section
      data-testid="attachment-images"
      aria-label="附件图片"
      className="surface-card space-y-2 px-3 py-2.5"
    >
      <header className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h2 className="text-sm font-semibold text-foreground">附件图片</h2>
          <p className="text-[11px] text-muted-foreground">
            附件 #{attachmentId} · 图片分析结果与 caption（按版本授权读取）
          </p>
        </div>
        <button
          type="button"
          className="inline-flex h-8 items-center rounded-md border border-border bg-card px-3 text-xs font-medium text-foreground transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50"
          disabled={busy}
          onClick={() => {
            void reanalyze()
          }}
        >
          {busy ? "分析中…" : "重新分析图片"}
        </button>
      </header>
      {notice !== null ? (
        <p
          data-testid="attachment-reanalyze-notice"
          role="status"
          className="text-[11px] text-success"
        >
          {notice}
        </p>
      ) : null}
      {error !== null ? <InlineError error={error} /> : null}
      {images === null && error === null ? (
        <LoadingCard label="正在加载附件图片…" />
      ) : images !== null && images.length === 0 ? (
        <p className="text-[11px] text-muted-foreground">该附件没有提取到图片。</p>
      ) : (
        <ul className="grid gap-2 sm:grid-cols-2">
          {(images ?? []).map((image) => (
            <li
              key={image.id}
              data-testid={`attachment-image-${image.id}`}
              className="rounded-lg border border-border bg-card p-2 text-[11px]"
            >
              <div className="flex flex-wrap items-center gap-1.5">
                <span className="font-medium text-foreground">#{image.seq}</span>
                <Badge tone={imageStatusTone(image.status)}>
                  {IMAGE_STATUS_LABEL[image.status] ?? image.status}
                </Badge>
                <span className="text-muted-foreground">{image.mime}</span>
                {image.width !== undefined && image.height !== undefined ? (
                  <span className="text-muted-foreground">
                    {image.width}×{image.height}
                  </span>
                ) : null}
                <span className="ml-auto text-muted-foreground">
                  更新于 {formatDateTime(image.updated_at)}
                </span>
              </div>
              <p className="mt-1 text-foreground">
                {image.caption !== "" ? image.caption : "暂无 caption"}
              </p>
              {image.error !== "" ? (
                <p className="mt-1 text-destructive">
                  {image.status === "skipped" ? "跳过原因" : "失败原因"}：{image.error}
                </p>
              ) : null}
              {image.sha256 !== "" ? <AttachmentImageLink sha256={image.sha256} /> : null}
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

export type VersionDetailPageProps = {
  versionId: string
  initialTab?: VersionTab
  /** Deep link `?conversation=` forwarded to the clarification workspace. */
  initialConversationId?: string
  /** Deep link `?requirement=` forwarded to the clarification workspace. */
  initialRequirementId?: string
}

/** `/versions/{id}` shell: header, eight URL-backed tabs and the overview. */
export function VersionDetailPage({
  versionId,
  initialTab,
  initialConversationId,
  initialRequirementId,
}: VersionDetailPageProps) {
  const router = useRouter()
  const searchParams = useSearchParams()
  // The context packet contains prompt content and the API is admin-only; a
  // normal user must not mount the block or fire the guaranteed-403 request.
  const isAdmin = useIsAdmin()
  const [version, setVersion] = useState<Version | null>(null)
  const [stage, setStage] = useState<StageView | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<unknown>(null)
  const [activeTab, setActiveTab] = useState<VersionTab>(() => parseVersionTab(initialTab))
  // R2-D-01: on a short viewport the design tab is the only tab that needs the
  // version chrome compressed; every other tab keeps the approved full header.
  const shortViewport = useShortViewport()
  const compactDesignChrome = activeTab === "design" && shortViewport
  const [issueCount, setIssueCount] = useState<number | undefined>(undefined)
  const [reportRevision, setReportRevision] = useState<number | undefined>(undefined)
  const [todoOpen, setTodoOpen] = useState(false)
  // v0.22 W0 SSE-driven refresh nonces and the pending question cards.
  const [planRefreshKey, setPlanRefreshKey] = useState(0)
  const [bindingRefreshKey, setBindingRefreshKey] = useState(0)
  const [attachmentRefreshKey, setAttachmentRefreshKey] = useState(0)
  // Work-item rows carry the job/run projection, so both `work_item.*` and
  // `tool.retry` refresh them; the admin-only context packet has its own nonce.
  const [workItemsRefreshKey, setWorkItemsRefreshKey] = useState(0)
  const [contextRefreshKey, setContextRefreshKey] = useState(0)
  // W1-14B: execution-time discovery observations and the discovery-side
  // coverage matrix share the `resource.delta` / frozen-scope events.
  const [discoveryRefreshKey, setDiscoveryRefreshKey] = useState(0)
  const [coverageRefreshKey, setCoverageRefreshKey] = useState(0)
  // W2-12 FE-19: the latest discovery revision auto-confirm/rollback notice.
  const [discoveryRevision, setDiscoveryRevision] = useState<DiscoveryRevisionData | null>(null)
  const [pendingQuestions, setPendingQuestions] = useState<QuestionBatch[]>([])
  // v0.22 decision requests are version todos too, but they are human_request
  // decision cards, not QuestionCards (approval/blocked_recovery/conflict).
  const [pendingDecisions, setPendingDecisions] = useState<HumanRequest[]>([])

  // `?case=` / `?conversation=` deep links for the design workspace. The route
  // already forwards `conversation` for clarification; read the URL here so the
  // case deep link works without changing `src/app` routing.
  const designConversationId =
    initialConversationId ?? searchParams.get("conversation") ?? undefined
  const designCaseId = searchParams.get("case") ?? undefined
  // `?export=` opens one export artifact's detail inside the design workspace.
  const designExportId = searchParams.get("export") ?? undefined
  // `?wave=` / `?case_run=` select a wave/case run inside the execution tab.
  const executionWaveId = searchParams.get("wave") ?? undefined
  const executionCaseRunId = searchParams.get("case_run") ?? undefined
  // Task 35: forward the evidence deep link to the execution tab.
  const executionEvidenceId = searchParams.get("evidence") ?? undefined
  // H7: attachment images are only loaded for an explicitly selected attachment.
  const attachmentId = searchParams.get("attachment") ?? undefined
  // C-05: selecting a row opens the same in-page panel without a full navigation;
  // the URL is kept in sync so the deep link still works after a reload.
  const [selectedAttachmentId, setSelectedAttachmentId] = useState<string | undefined>(attachmentId)
  useEffect(() => {
    setSelectedAttachmentId(attachmentId)
  }, [attachmentId])
  // W1-4A: work-item and fact deep links inside the overview data blocks.
  const versionWorkItemId = searchParams.get("work_item") ?? undefined
  const versionFactId =
    searchParams.get("panel") === "facts" ? (searchParams.get("fact") ?? undefined) : undefined

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      setVersion(await getVersion(versionId))
    } catch (cause) {
      setVersion(null)
      setError(cause)
    } finally {
      setLoading(false)
    }
  }, [versionId])

  /** Silent projection refresh used by SSE events (no full-page loading flash). */
  const refreshVersion = useCallback(async () => {
    try {
      setVersion(await getVersion(versionId))
    } catch {
      // Keep the last projection; the next event or retry refreshes it.
    }
  }, [versionId])

  /**
   * Deterministic stage projection refresh (Task 78). It is independent of the
   * version projection: a stage event may arrive before the version row, and a
   * failed read clears the badge instead of showing a stale fake stage.
   */
  const refreshStage = useCallback(async () => {
    try {
      setStage(await getVersionStage(versionId))
    } catch {
      setStage(null)
    }
  }, [versionId])

  // Tab counts come from the real list projections; a failed/missing query
  // leaves the count absent instead of rendering a misleading zero (spec §9.1).
  const refreshCounts = useCallback(async () => {
    try {
      const items = await listIssues(versionId, { status: "open", limit: 100 })
      setIssueCount(items.filter((issue) => issue.status === "open").length)
    } catch {
      setIssueCount(undefined)
    }
    try {
      const items = await listReports(versionId, { kind: "version", limit: 20 })
      const versionReport = items.find((report) => report.kind === "version")
      setReportRevision(versionReport?.current_revision?.revision)
    } catch {
      setReportRevision(undefined)
    }
  }, [versionId])

  useEffect(() => {
    void load()
  }, [load])

  useEffect(() => {
    void refreshStage()
  }, [refreshStage])

  // Pending structured questions are a version todo: only load them once the
  // v0.22 stage projection is available. A failure renders no cards instead of
  // blocking the page (questions never block stage progression, R-B3-4).
  //
  // `GET /questions` also returns the approval/blocked_recovery/conflict
  // decision rows (W2-2); those are split out and never become QuestionCards.
  const refreshQuestions = useCallback(async () => {
    try {
      const questions = await listQuestions(versionId, { status: "pending" })
      setPendingQuestions(questions.filter(isClarificationQuestionBatch))
    } catch {
      setPendingQuestions([])
    }
    try {
      const humanRequests = await listHumanRequests(versionId, { status: "pending" })
      setPendingDecisions(
        humanRequests.filter(
          (request) => request.status === "pending" && isHumanDecisionKind(request.kind),
        ),
      )
    } catch {
      setPendingDecisions([])
    }
  }, [versionId])

  /**
   * Resolve one approval/blocked_recovery/conflict request through the existing
   * `POST /api/human-requests/{id}/resolve` route, then refetch the pending
   * projections so an answered card disappears without a page reload.
   */
  const handleResolveDecision = useCallback(
    async (id: string, response: HumanRequestResponseInput): Promise<void> => {
      await resolveHumanRequest(id, response)
      await refreshQuestions()
    },
    [refreshQuestions],
  )

  useEffect(() => {
    if (stage === null) return
    void refreshQuestions()
  }, [stage, refreshQuestions])

  useEffect(() => {
    setActiveTab(parseVersionTab(initialTab))
  }, [initialTab])

  useEffect(() => {
    void refreshCounts()
  }, [refreshCounts])

  const refreshTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const stageRefreshTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const pendingRefresh = useRef({ version: false, counts: false })

  const runPendingRefresh = useCallback(() => {
    const pending = pendingRefresh.current
    pendingRefresh.current = { version: false, counts: false }
    if (pending.version) void refreshVersion()
    if (pending.counts) void refreshCounts()
  }, [refreshVersion, refreshCounts])

  const scheduleRefresh = useCallback(
    (includeVersion: boolean) => {
      pendingRefresh.current = {
        version: pendingRefresh.current.version || includeVersion,
        counts: true,
      }
      if (refreshTimer.current !== null) clearTimeout(refreshTimer.current)
      refreshTimer.current = setTimeout(() => {
        refreshTimer.current = null
        runPendingRefresh()
      }, SSE_REFRESH_DEBOUNCE_MS)
    },
    [runPendingRefresh],
  )

  const refreshVersionAndCounts = useCallback(() => scheduleRefresh(true), [scheduleRefresh])
  const refreshCountsOnly = useCallback(() => scheduleRefresh(false), [scheduleRefresh])

  // A version burst often carries the version event and the stage event back to
  // back; both branches schedule the stage read, so debounce it to one GET.
  const scheduleStageRefresh = useCallback(() => {
    if (stageRefreshTimer.current !== null) clearTimeout(stageRefreshTimer.current)
    stageRefreshTimer.current = setTimeout(() => {
      stageRefreshTimer.current = null
      void refreshStage()
    }, SSE_REFRESH_DEBOUNCE_MS)
  }, [refreshStage])

  const refreshVersionAndStage = useCallback(() => {
    refreshVersionAndCounts()
    scheduleStageRefresh()
  }, [refreshVersionAndCounts, scheduleStageRefresh])

  // A successful inline binding save is the same snapshot change as the
  // `version.binding.changed` SSE event: re-read the binding and the version
  // projection instead of trusting only the response the child applied.
  const refreshBindingAfterSave = useCallback(() => {
    setBindingRefreshKey((current) => current + 1)
    refreshVersionAndCounts()
  }, [refreshVersionAndCounts])

  // `resource.delta` and a frozen scope snapshot both change the discovered
  // resource set, so the observations and the coverage matrix refetch together.
  const refreshDiscovery = useCallback(() => {
    setDiscoveryRefreshKey((current) => current + 1)
    setCoverageRefreshKey((current) => current + 1)
  }, [])

  const rollbackDiscoveryRevision = useCallback(
    (caseVersionId: string, reason?: string) =>
      reason === undefined
        ? rollbackCaseVersion(versionId, caseVersionId)
        : rollbackCaseVersion(versionId, caseVersionId, reason),
    [versionId],
  )

  const handleDiscoveryRolledBack = useCallback(
    (result?: CaseRevisionRollbackView) => {
      setDiscoveryRevision((current) => {
        if (current === null || result === undefined) return current
        return {
          ...current,
          caseId: result.case_id,
          caseVersionId: result.case_version_id,
          revision: result.revision,
          status: "confirmed",
          trigger: "rollback",
          rollbackOf: result.rolled_back_from,
          inferred: false,
        }
      })
      refreshDiscovery()
      refreshVersionAndCounts()
    },
    [refreshDiscovery, refreshVersionAndCounts],
  )

  const refreshSnapshotNow = useCallback(() => {
    if (refreshTimer.current !== null) {
      clearTimeout(refreshTimer.current)
      refreshTimer.current = null
    }
    pendingRefresh.current = { version: true, counts: true }
    runPendingRefresh()
  }, [runPendingRefresh])

  useEffect(
    () => () => {
      if (refreshTimer.current !== null) clearTimeout(refreshTimer.current)
      if (stageRefreshTimer.current !== null) clearTimeout(stageRefreshTimer.current)
    },
    [],
  )

  // One page-level subscription keeps the header projection and the tab counts
  // live while the mounted tab owns its own workspace subscription. Every v0.22
  // event has exactly one branch in useVersionEvents (no duplicate handlers).
  useVersionEvents(versionId, {
    onVersionChanged: refreshVersionAndStage,
    onStageChanged: refreshVersionAndStage,
    onPlanChanged: () => setPlanRefreshKey((current) => current + 1),
    onFactChanged: () => {
      setPlanRefreshKey((current) => current + 1)
      setBindingRefreshKey((current) => current + 1)
    },
    onQuestionChanged: () => {
      void refreshQuestions()
    },
    // approval.requested/decided share the human-request refresh with the
    // execution banner: both the pending decision cards and the question todo
    // projection may have changed (W2-2 R-W2-1).
    onHumanRequestChanged: () => {
      void refreshQuestions()
    },
    onHandoffChanged: () => {
      // The handoff is rendered by the clarification workspace; a version-level
      // change only needs a projection refresh.
      refreshCountsOnly()
    },
    onBindingChanged: () => {
      setBindingRefreshKey((current) => current + 1)
      refreshVersionAndCounts()
    },
    onAttachmentImageChanged: () => setAttachmentRefreshKey((current) => current + 1),
    onWorkItemChanged: () => setWorkItemsRefreshKey((current) => current + 1),
    onContextChanged: () => setContextRefreshKey((current) => current + 1),
    // W1-14B: a delta pass settles/proposes discovered resources; the frozen
    // scope snapshot changes which binding resources a wave may use.
    onDiscoveryChanged: refreshDiscovery,
    // W2-12 FE-19/EV-12: a discovery revision was auto-confirmed or rolled back;
    // show the replacement/auto-confirm notice and refetch the discovery panels.
    onDesignCaseChanged: (event) => {
      if (event.type !== "case.revision.auto_confirmed") return
      const next = discoveryRevisionFromEventPayload(event.payload)
      if (next === null) return
      setDiscoveryRevision(next)
      refreshDiscovery()
    },
    onScopeSnapshotChanged: () => {
      refreshDiscovery()
      refreshVersionAndCounts()
    },
    // A tool retry re-runs one tool inside an agent run; the work-item rows show
    // the run/activity projection, so they refetch on the same nonce.
    onToolRetry: () => setWorkItemsRefreshKey((current) => current + 1),
    onRequirementChanged: refreshVersionAndStage,
    onIssueChanged: refreshCountsOnly,
    onReportChanged: refreshCountsOnly,
    onStreamReset: () => {
      refreshSnapshotNow()
      void refreshStage()
      void refreshQuestions()
      setPlanRefreshKey((current) => current + 1)
      setBindingRefreshKey((current) => current + 1)
      setAttachmentRefreshKey((current) => current + 1)
      setWorkItemsRefreshKey((current) => current + 1)
      setContextRefreshKey((current) => current + 1)
      setDiscoveryRefreshKey((current) => current + 1)
      setCoverageRefreshKey((current) => current + 1)
    },
  })

  const tabItems = useMemo(
    () =>
      VERSION_TABS.map((tab) => {
        if (tab.value === "issues" && issueCount !== undefined) return { ...tab, count: issueCount }
        if (tab.value === "reports" && reportRevision !== undefined) {
          return { ...tab, count: reportRevision }
        }
        return tab
      }),
    [issueCount, reportRevision],
  )

  function handleTabChange(value: string): void {
    const next = parseVersionTab(value)
    setActiveTab(next)
    const params = new URLSearchParams({ tab: next })
    if (next === "overview") {
      // Keep the H7 attachment-image deep link alive across tab switches.
      const attachment = searchParams.get("attachment")
      if (attachment) params.set("attachment", attachment)
    }
    if (next === "requirements") {
      if (initialConversationId) params.set("conversation", initialConversationId)
      if (initialRequirementId) params.set("requirement", initialRequirementId)
    }
    if (next === "design") {
      const conversationId = searchParams.get("conversation") ?? initialConversationId
      const caseId = searchParams.get("case")
      const exportId = searchParams.get("export")
      if (conversationId) params.set("conversation", conversationId)
      if (caseId) params.set("case", caseId)
      if (exportId) params.set("export", exportId)
    }
    if (next === "execution") {
      const waveId = searchParams.get("wave")
      const caseRunId = searchParams.get("case_run")
      const evidenceId = searchParams.get("evidence")
      if (waveId) params.set("wave", waveId)
      if (caseRunId) params.set("case_run", caseRunId)
      if (evidenceId) params.set("evidence", evidenceId)
    }
    if (next === "issues") {
      const issueId = searchParams.get("issue")
      if (issueId) params.set("issue", issueId)
    }
    if (next === "reports") {
      const reportId = searchParams.get("report")
      const revision = searchParams.get("revision")
      if (reportId) params.set("report", reportId)
      if (revision) params.set("revision", revision)
    }
    if (next === "chain") {
      for (const key of ["node", "issue", "report", "wave"]) {
        const value = searchParams.get(key)
        if (value) params.set(key, value)
      }
    }
    if (next === "code") {
      for (const key of ["repo", "analysis", "candidate", "anchor", "fact"]) {
        const value = searchParams.get(key)
        if (value) params.set(key, value)
      }
    }
    router.replace(`/versions/${encodeURIComponent(versionId)}?${params.toString()}`)
  }

  /** C-05: open one attachment's image panel from the overview list. */
  function selectAttachment(id: string): void {
    setSelectedAttachmentId(id)
    // Same style as `handleTabChange`: start from the tab URL and keep every
    // unrelated workspace param (conversation/requirement/…) instead of
    // dropping it when the image panel opens.
    const params = new URLSearchParams({ tab: "overview" })
    for (const [key, value] of searchParams.entries()) {
      if (key === "tab" || key === "attachment") continue
      params.set(key, value)
    }
    params.set("attachment", id)
    router.replace(`/versions/${encodeURIComponent(versionId)}?${params.toString()}`)
  }

  if (loading) {
    return (
      <div className="space-y-4">
        <Link
          href="/versions"
          className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
        >
          <ArrowLeft className="size-3.5" aria-hidden="true" />
          返回版本列表
        </Link>
        <LoadingCard label="正在加载版本详情…" />
      </div>
    )
  }

  if (error !== null || version === null) {
    return (
      <div className="space-y-4">
        <Link
          href="/versions"
          className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
        >
          <ArrowLeft className="size-3.5" aria-hidden="true" />
          返回版本列表
        </Link>
        <ErrorState error={error} title="版本详情加载失败" onRetry={() => void load()} />
      </div>
    )
  }

  return (
    <div className="space-y-5">
      <Link
        href="/versions"
        className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
      >
        <ArrowLeft className="size-3.5" aria-hidden="true" />
        返回版本列表
      </Link>

      <VersionHeader
        version={version}
        stage={stage}
        compact={compactDesignChrome}
        onOpenTodo={() => setTodoOpen(true)}
        onReopened={(next) => setVersion(next)}
        onStageChanged={(view) => {
          setStage(view)
          refreshVersionAndCounts()
        }}
      />

      {stage !== null ? (
        <div className="space-y-3">
          <StageBar
            versionId={versionId}
            stage={stage.stage}
            stageEnteredAt={stage.stage_entered_at}
            compact={compactDesignChrome}
            blocker={parseStageBlocker(stage.blocker)}
            pausedReason={stage.stage === "paused" ? stage.blocker : null}
            disabled={loading}
            onStageChanged={(view) => {
              setStage(view)
              refreshVersionAndCounts()
            }}
          />
          <BlockerPanel
            versionId={versionId}
            stage={stage.stage}
            blocker={parseStageBlocker(stage.blocker)}
            blockedSince={stage.stage === "blocked" ? stage.stage_entered_at : null}
            onBlocked={(view) => {
              setStage(view)
              refreshVersionAndCounts()
            }}
            onUnblocked={(view) => {
              setStage(view)
              refreshVersionAndCounts()
            }}
          />
          {/* R3-E-01: name the open completion conditions while reporting. */}
          <CompletionBlockersPanel
            version={version}
            stage={stage.stage}
            pendingQuestions={pendingQuestions.length}
            pendingDecisions={pendingDecisions.length}
            refreshKey={`${version.updated_at}|${reportRevision ?? ""}`}
          />
        </div>
      ) : null}

      <Tabs
        items={tabItems}
        value={activeTab}
        onValueChange={handleTabChange}
        ariaLabel="版本工作区"
      />

      {activeTab === "overview" ? (
        <div className="space-y-4">
          {stage !== null ? (
            <>
              <section data-testid="plan-overview" className="space-y-2">
                <h2 className="text-sm font-semibold text-foreground">版本计划</h2>
                <PlanPanel versionId={versionId} refreshKey={planRefreshKey} />
              </section>
              <BindingOverview
                versionId={versionId}
                refreshKey={bindingRefreshKey}
                onUpdated={refreshBindingAfterSave}
              />
              <section data-testid="version-data" aria-label="版本数据" className="space-y-3">
                <div className="flex flex-wrap items-end justify-between gap-2">
                  <h2 className="text-sm font-semibold text-foreground">版本数据</h2>
                  <span className="text-[11px] text-muted-foreground">
                    事实 · 作业与任务单 · 上下文包（只读投影）
                  </span>
                </div>
                <div
                  data-testid="version-data-grid"
                  className="grid items-start gap-3 lg:grid-cols-2"
                >
                  <FactsPanel
                    versionId={versionId}
                    initialFactId={versionFactId}
                    refreshKey={planRefreshKey}
                  />
                  <WorkItemsPanel
                    versionId={versionId}
                    initialWorkItemId={versionWorkItemId}
                    refreshKey={workItemsRefreshKey}
                  />
                </div>
                {isAdmin ? (
                  <ContextPacketCard versionId={versionId} refreshKey={contextRefreshKey} />
                ) : null}
                <section
                  data-testid="discovery-overview"
                  aria-label="探索发现"
                  className="space-y-3 border-t border-border pt-3"
                >
                  <div className="flex flex-wrap items-end justify-between gap-2">
                    <h2 className="text-sm font-semibold text-foreground">探索发现</h2>
                    <span className="text-[11px] text-muted-foreground">
                      执行观测 · 待处理资源 · 发现侧覆盖（审批复用资源目录）
                    </span>
                  </div>
                  {discoveryRevision !== null ? (
                    <DiscoveryRevisionNotice
                      revision={discoveryRevision}
                      readOnly={!isAdmin}
                      onRollback={rollbackDiscoveryRevision}
                      onRolledBack={handleDiscoveryRolledBack}
                    />
                  ) : null}
                  <DiscoveryPanel
                    versionId={versionId}
                    refreshKey={discoveryRefreshKey}
                    onChanged={() => setCoverageRefreshKey((current) => current + 1)}
                  />
                  <CoveragePanel versionId={versionId} refreshKey={coverageRefreshKey} />
                </section>
              </section>
              {pendingDecisions.length > 0 ? (
                <section
                  data-testid="pending-human-decisions"
                  aria-label="待处理人工决策"
                  className="space-y-2"
                >
                  <div className="flex flex-wrap items-center gap-2">
                    <h2 className="text-sm font-semibold text-foreground">待处理人工决策</h2>
                    <Badge tone="warning">{pendingDecisions.length}</Badge>
                  </div>
                  <HumanDecisionCards
                    requests={pendingDecisions}
                    onResolve={handleResolveDecision}
                  />
                </section>
              ) : null}
              {pendingQuestions.length > 0 ? (
                <section
                  data-testid="pending-questions"
                  aria-label="待回答问题"
                  className="space-y-2"
                >
                  <div className="flex flex-wrap items-center gap-2">
                    <h2 className="text-sm font-semibold text-foreground">待回答问题</h2>
                    <Badge tone="warning">{pendingQuestions.length}</Badge>
                  </div>
                  {pendingQuestions.map((question) => (
                    <QuestionCard
                      key={question.id}
                      versionId={versionId}
                      question={question}
                      onUpdated={(updated) => {
                        setPendingQuestions((current) =>
                          current
                            .map((item) => (item.id === updated.id ? updated : item))
                            .filter((item) => item.status === "pending"),
                        )
                      }}
                    />
                  ))}
                </section>
              ) : null}
            </>
          ) : null}
          <VersionOverview version={version} showBindings={stage === null} />
          <VersionAttachmentsSection
            versionId={versionId}
            refreshKey={attachmentRefreshKey}
            onSelect={selectAttachment}
          />
          {selectedAttachmentId !== undefined ? (
            <AttachmentImagesPanel
              versionId={versionId}
              attachmentId={selectedAttachmentId}
              refreshKey={attachmentRefreshKey}
            />
          ) : null}
        </div>
      ) : activeTab === "requirements" ? (
        <RequirementsTab
          versionId={versionId}
          stage={stage?.stage ?? null}
          onStageChanged={() => {
            void refreshStage()
          }}
          initialConversationId={initialConversationId}
          initialRequirementId={initialRequirementId}
        />
      ) : activeTab === "design" ? (
        <DesignTab
          versionId={versionId}
          stage={stage?.stage ?? null}
          onStageChanged={() => {
            void refreshStage()
          }}
          initialCaseId={designCaseId}
          initialConversationId={designConversationId}
          initialExportId={designExportId}
        />
      ) : activeTab === "execution" ? (
        <ExecutionTab
          versionId={versionId}
          initialWaveId={executionWaveId}
          initialCaseRunId={executionCaseRunId}
          initialEvidenceId={executionEvidenceId}
        />
      ) : activeTab === "chain" ? (
        <ChainTab versionId={versionId} onVersionChanged={refreshVersionAndCounts} />
      ) : activeTab === "issues" ? (
        <IssuesTab versionId={versionId} />
      ) : activeTab === "reports" ? (
        <ReportsTab versionId={versionId} />
      ) : (
        <CodeTab versionId={versionId} />
      )}

      <TodoDrawer versionId={versionId} open={todoOpen} onClose={() => setTodoOpen(false)} />
    </div>
  )
}
