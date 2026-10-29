"use client"

import { GitBranch, Loader2, RefreshCw } from "lucide-react"
import { useCallback, useEffect, useMemo, useRef, useState } from "react"

import { EmptyState } from "@/components/ui/EmptyState"
import { StatusBadge, type StatusBadgeVariant } from "@/components/ui/StatusBadge"
import type { VersionStreamState } from "@/features/versions/useVersionStream"
import type { SSEStatus } from "@/lib/sse-client"
import type { ChainSnapshot, ChainSnapshotNode } from "../chain/chain-types"
import { chainSnapshotTotal } from "../chain/chain-types"
import { useExplorationChain } from "../chain/use-exploration-chain"
import {
  autoCollapsedRequirements,
  type ChainCaseInput,
  type ChainIssueInput,
  type ChainLayoutInput,
  type ChainNode,
  type ChainRequirementInput,
  type ChainResultFilter,
  type ChainResultInput,
  layoutChain,
} from "../chain-layout"
import { useCaseRun } from "../hooks/useExecution"
import type { CaseRunStatus, FailureClass } from "../types"
import { ChainGraph } from "./ChainGraph"
import { FailureClassBadge } from "./FailureClassBadge"

const BUTTON_CLASS =
  "inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-1.5 text-xs font-medium text-foreground transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"

const FIELD_CLASS =
  "mt-1 w-full rounded-lg border border-border bg-input px-2.5 py-1.5 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"

const RESULT_STATUS_LABELS: Record<CaseRunStatus, string> = {
  pending: "待执行",
  running: "执行中",
  passed: "通过",
  failed: "失败",
  blocked: "阻塞",
  inconclusive: "不确定",
  skipped: "跳过",
  interrupted: "中断",
}

const RESULT_BADGE_VARIANTS: Record<CaseRunStatus, StatusBadgeVariant> = {
  pending: "info",
  running: "info",
  passed: "success",
  failed: "danger",
  blocked: "neutral",
  inconclusive: "warning",
  skipped: "neutral",
  interrupted: "danger",
}

const CASE_STATUS_LABELS: Record<string, string> = {
  draft: "草稿",
  reviewed: "已评审",
  executed: "已执行",
  supplemented: "已补充",
  merged: "已合并",
  rejected: "已驳回",
  needs_revalidation: "需重验",
}

const REQUIREMENT_STATUS_LABELS: Record<string, string> = {
  draft: "草稿",
  clarified: "已澄清",
  designed: "已设计",
  verified: "已验证",
  obsolete: "已废弃",
}

const ISSUE_STATUS_LABELS: Record<string, string> = {
  open: "待确认",
  confirmed: "已确认",
  dismissed: "已驳回",
}

const ISSUE_SEVERITY_LABELS: Record<string, string> = {
  blocker: "阻断",
  major: "严重",
  minor: "一般",
  info: "提示",
}

const ISSUE_CONFIDENCE_LABELS: Record<string, string> = {
  high: "高",
  medium: "中",
  low: "低",
}

const ISSUE_CATEGORY_LABELS: Record<string, string> = {
  defect: "应用缺陷",
  requirement_gap: "需求缺口",
  other: "其他",
}

const ISSUE_SEVERITY_BADGE_VARIANTS: Record<string, StatusBadgeVariant> = {
  blocker: "danger",
  major: "warning",
  minor: "info",
  info: "neutral",
}

/** Result statuses accepted by the server-side filter (pending/running excluded). */
const STATUS_FILTER_OPTIONS: readonly { value: ChainResultFilter; label: string }[] = [
  { value: "", label: "全部状态" },
  { value: "passed", label: "通过" },
  { value: "failed", label: "失败" },
  { value: "blocked", label: "阻塞" },
  { value: "inconclusive", label: "不确定" },
  { value: "skipped", label: "跳过" },
  { value: "interrupted", label: "中断" },
]

const ISSUE_STATUS_FILTER_OPTIONS = [
  { value: "", label: "全部问题状态" },
  { value: "open", label: "待确认" },
  { value: "confirmed", label: "已确认" },
  { value: "dismissed", label: "已驳回" },
] as const

const ISSUE_SEVERITY_FILTER_OPTIONS = [
  { value: "", label: "全部严重级" },
  { value: "blocker", label: "阻断" },
  { value: "major", label: "严重" },
  { value: "minor", label: "一般" },
  { value: "info", label: "提示" },
] as const

/** Debounce of the `q` server filter (spec §5.5). */
const SEARCH_DEBOUNCE_MS = 300

/** URL-owned filter subset; the page persists these in the query string. */
export type ExplorationFilters = {
  requirementId: string
  resultStatus: string
  abnormalOnly: boolean
  q: string
}

/** Deep-link target of the node drawer (spec §5.7). */
export type ExplorationDeepLink =
  | { kind: "requirement"; id: string }
  | { kind: "case"; id: string }
  | { kind: "result"; id: string }
  | { kind: "issue"; id: string }

export type ExplorationTabProps = {
  /** P3 version id; never re-parsed (spec §7.1). */
  versionId: string
  /** Selected batch; `undefined`/empty lets the read model use its latest batch. */
  batchId?: string
  /** Version stream owned by the tab container; the tab never opens its own. */
  stream?: VersionStreamState
  /** Opens one evidence item in the results tab's viewer. */
  onOpenEvidence?: (evidenceId: string) => void
  /** Results-tab deep link callback for requirement/case/result nodes. */
  onNavigate?: (target: ExplorationDeepLink) => void
  /** Filter values read from the URL deep link; used to seed the local filters. */
  initialFilters?: Partial<ExplorationFilters>
  /** Called whenever the URL-owned filters change so the page can sync the URL. */
  onFiltersChange?: (filters: ExplorationFilters) => void
  /** Deep-linked node (`<kind>:<id>`); when set this Sheet is open. */
  selectedNodeId?: string
  /** Called when the user opens/closes a node Sheet so the page can sync the URL. */
  onSelectedNodeChange?: (nodeId: string) => void
}

function formatDuration(durationMs: number | undefined): string {
  if (durationMs === undefined || durationMs <= 0) return ""
  if (durationMs < 1000) return `${durationMs} ms`
  return `${(durationMs / 1000).toFixed(1)} s`
}

function caseStatusLabel(status: string): string {
  return CASE_STATUS_LABELS[status] ?? (status === "" ? "—" : status)
}

function requirementStatusLabel(status: string): string {
  return REQUIREMENT_STATUS_LABELS[status] ?? (status === "" ? "—" : status)
}

function issueStatusLabel(status: string): string {
  return ISSUE_STATUS_LABELS[status] ?? (status === "" ? "—" : status)
}

function issueSeverityLabel(severity: string): string {
  return ISSUE_SEVERITY_LABELS[severity] ?? (severity === "" ? "—" : severity)
}

function issueConfidenceLabel(confidence: string): string {
  return ISSUE_CONFIDENCE_LABELS[confidence] ?? (confidence === "" ? "—" : confidence)
}

function issueCategoryLabel(category: string): string {
  return ISSUE_CATEGORY_LABELS[category] ?? (category === "" ? "—" : category)
}

function normalizeStatusFilter(value: string | undefined): ChainResultFilter {
  if (value === undefined) return ""
  return STATUS_FILTER_OPTIONS.some((option) => option.value === value)
    ? (value as ChainResultFilter)
    : ""
}

const FAILURE_CLASSES = new Set<FailureClass>([
  "application",
  "environment",
  "session",
  "model",
  "case",
])

function toFailureClass(value: string | undefined): FailureClass | null {
  if (value === undefined || value === "") return null
  return FAILURE_CLASSES.has(value as FailureClass) ? (value as FailureClass) : null
}

const CHAIN_NODE_KINDS = new Set<ChainNode["kind"]>(["requirement", "case", "result", "issue"])

function chainNodeLayer(kind: ChainNode["kind"]): ChainNode["layer"] {
  if (kind === "requirement") return 0
  if (kind === "case") return 1
  if (kind === "result") return 2
  return 3
}

/** Parses the backend node/deep-link id `<kind>:<decimal id>`. */
function parseChainNodeId(
  raw: string | null | undefined,
): { kind: ChainNode["kind"]; id: string } | null {
  if (raw === null || raw === undefined) return null
  const separator = raw.indexOf(":")
  if (separator <= 0 || separator === raw.length - 1) return null
  const kind = raw.slice(0, separator)
  const id = raw.slice(separator + 1)
  if (!CHAIN_NODE_KINDS.has(kind as ChainNode["kind"]) || id === "") return null
  return { kind: kind as ChainNode["kind"], id }
}

function domainIdOf(node: ChainSnapshotNode): string {
  return parseChainNodeId(node.id)?.id ?? node.id
}

type ChainView = {
  input: ChainLayoutInput
  dtoByKey: Map<string, ChainSnapshotNode>
  /** Batch of each result/case-run, so evidence can be lazily fetched in place. */
  resultBatchById: Map<string, string>
}

const EMPTY_CHAIN_VIEW: ChainView = {
  input: { requirements: [], cases: [], results: [], issues: [] },
  dtoByKey: new Map(),
  resultBatchById: new Map(),
}

/**
 * Adapts the read-model snapshot onto the existing four-layer layout input.
 *
 * Requirement → case links come from the snapshot's `requirement_case` /
 * `impact_case` edges. `case.requirementIds` is display-only (it may mention
 * requirements filtered out of the response), so it is never used to create an
 * edge. Issue attachment is likewise resolved from `result_issue` /
 * `case_issue` edges; an issue without an edge is passed as unlinked.
 */
function toChainView(snapshot: ChainSnapshot | null): ChainView {
  if (snapshot === null) return EMPTY_CHAIN_VIEW

  const requirementNodes: ChainSnapshotNode[] = []
  const caseNodes: ChainSnapshotNode[] = []
  const resultNodes: ChainSnapshotNode[] = []
  const issueNodes: ChainSnapshotNode[] = []
  const dtoByKey = new Map<string, ChainSnapshotNode>()
  for (const node of snapshot.nodes) {
    dtoByKey.set(node.id, node)
    if (node.kind === "requirement") requirementNodes.push(node)
    else if (node.kind === "case") caseNodes.push(node)
    else if (node.kind === "result") resultNodes.push(node)
    else issueNodes.push(node)
  }

  // First pass: case → result links let a result_issue edge resolve its case.
  const caseByResult = new Map<string, string>()
  for (const edge of snapshot.edges) {
    if (edge.kind !== "case_result") continue
    const source = parseChainNodeId(edge.source)
    const target = parseChainNodeId(edge.target)
    if (source?.kind !== "case" || target?.kind !== "result") continue
    caseByResult.set(target.id, source.id)
  }

  // Second pass: requirement → case links and issue attachments.
  const requirementIdsByCase = new Map<string, string[]>()
  const caseByIssue = new Map<string, string>()
  for (const edge of snapshot.edges) {
    const source = parseChainNodeId(edge.source)
    const target = parseChainNodeId(edge.target)
    if (source === null || target === null) continue

    if (
      (edge.kind === "requirement_case" || edge.kind === "impact_case") &&
      source.kind === "requirement" &&
      target.kind === "case"
    ) {
      const ids = requirementIdsByCase.get(target.id)
      if (ids === undefined) requirementIdsByCase.set(target.id, [source.id])
      else if (!ids.includes(source.id)) ids.push(source.id)
      continue
    }

    if (edge.kind === "case_issue" && source.kind === "case" && target.kind === "issue") {
      caseByIssue.set(target.id, source.id)
      continue
    }

    if (edge.kind === "result_issue" && source.kind === "result" && target.kind === "issue") {
      const caseId = caseByResult.get(source.id)
      if (caseId !== undefined) caseByIssue.set(target.id, caseId)
    }
  }

  const requirementInputs: ChainRequirementInput[] = requirementNodes.map((node, index) => ({
    id: domainIdOf(node),
    title: node.title,
    status: node.status,
    sortOrder: index,
  }))

  const caseInputs: ChainCaseInput[] = caseNodes.map((node, index) => {
    const id = domainIdOf(node)
    return {
      id,
      title: node.title,
      requirementIds: requirementIdsByCase.get(id) ?? [],
      status: node.status,
      sortOrder: index,
      aiSupplemented: node.source === "ai_supplement",
    }
  })

  const resultBatchById = new Map<string, string>()
  const resultInputs: ChainResultInput[] = resultNodes.map((node, index) => {
    const nodeId = domainIdOf(node)
    const runId = node.caseRunId ?? nodeId
    if (node.batchId !== undefined && node.batchId !== "") resultBatchById.set(runId, node.batchId)
    return {
      id: runId,
      caseNodeId: caseByResult.get(runId) ?? node.caseNodeId ?? "",
      status: node.status as CaseRunStatus,
      title: node.title,
      summary: node.summary,
      durationMs: node.durationMs,
      failureClass: toFailureClass(node.failureClass),
      sortOrder: index,
    }
  })

  const issueInputs: ChainIssueInput[] = issueNodes.map((node, index) => {
    const id = domainIdOf(node)
    // Edges are authoritative: an issue without an incoming issue edge stays
    // unlinked even when its DTO still carries a stale caseRunId/caseNodeId.
    const caseNodeId = caseByIssue.get(id) ?? null
    return {
      id,
      title: node.title,
      status: node.status,
      severity: node.severity,
      confidence: node.confidence,
      category: node.category,
      caseNodeId,
      caseRunId: caseNodeId === null ? null : (node.caseRunId ?? null),
      sortOrder: index,
    }
  })

  return {
    input: {
      requirements: requirementInputs,
      cases: caseInputs,
      results: resultInputs,
      issues: issueInputs,
    },
    dtoByKey,
    resultBatchById,
  }
}

/** A positioned node for a deep-linked node hidden by a collapsed group. */
function toFallbackChainNode(view: ChainView, dto: ChainSnapshotNode): ChainNode | null {
  const parsed = parseChainNodeId(dto.id)
  if (parsed === null) return null
  const result =
    parsed.kind === "result"
      ? view.input.results?.find((row) => row.id === (dto.caseRunId ?? parsed.id))
      : undefined
  const issue =
    parsed.kind === "issue" ? view.input.issues?.find((row) => row.id === parsed.id) : undefined
  return {
    id: parsed.id,
    kind: parsed.kind,
    layer: chainNodeLayer(parsed.kind),
    title: dto.title,
    status: dto.status,
    x: 0,
    y: 0,
    w: 0,
    h: 0,
    requirementIds: [],
    caseNodeId: dto.caseNodeId ?? null,
    result,
    issue,
    hasChildren: false,
    descendantCount: 0,
    collapsed: false,
  }
}

function mergeRequirementOptions(
  previous: readonly ChainRequirementInput[],
  incoming: readonly ChainRequirementInput[],
): ChainRequirementInput[] {
  const byId = new Map<string, ChainRequirementInput>()
  for (const row of previous) byId.set(row.id, row)
  for (const row of incoming) byId.set(row.id, row)
  return [...byId.values()].sort((left, right) => {
    const orderDiff = (left.sortOrder ?? 0) - (right.sortOrder ?? 0)
    if (orderDiff !== 0) return orderDiff
    return left.id < right.id ? -1 : left.id > right.id ? 1 : 0
  })
}

type ChainDrawerProps = {
  node: ChainNode
  snapshotNode: ChainSnapshotNode | undefined
  view: ChainView
  versionId: string
  batchId: string
  stream?: VersionStreamState
  onClose: () => void
  onOpenEvidence?: (evidenceId: string) => void
  onNavigate?: (target: ExplorationDeepLink) => void
}

/** Node Sheet: summary, attempt/batch, lazy evidence and deep links (spec §5.5). */
function ChainDrawer({
  node,
  snapshotNode,
  view,
  versionId,
  batchId,
  stream,
  onClose,
  onOpenEvidence,
  onNavigate,
}: ChainDrawerProps) {
  const [evidenceRequested, setEvidenceRequested] = useState(false)

  const kindLabel =
    node.kind === "requirement"
      ? "需求节点"
      : node.kind === "case"
        ? "用例节点"
        : node.kind === "issue"
          ? "问题节点"
          : "执行结果"
  const statusLabel =
    node.kind === "result"
      ? (RESULT_STATUS_LABELS[node.status as CaseRunStatus] ?? node.status)
      : node.kind === "case"
        ? caseStatusLabel(node.status)
        : node.kind === "issue"
          ? issueStatusLabel(node.status)
          : requirementStatusLabel(node.status)
  const badgeVariant =
    node.kind === "result"
      ? (RESULT_BADGE_VARIANTS[node.status as CaseRunStatus] ?? "neutral")
      : node.kind === "issue"
        ? (ISSUE_SEVERITY_BADGE_VARIANTS[snapshotNode?.severity ?? ""] ?? "neutral")
        : "neutral"

  const caseRunId =
    node.kind === "result"
      ? (snapshotNode?.caseRunId ?? node.result?.id ?? "")
      : node.kind === "issue"
        ? (snapshotNode?.caseRunId ?? "")
        : ""
  const runBatchId = (caseRunId === "" ? "" : view.resultBatchById.get(caseRunId)) ?? batchId
  const evidenceCount = snapshotNode?.evidenceCount ?? 0
  // Evidence is read by caseRunId, so an issue without one cannot ever load it.
  // Do not offer a button that can only resolve to "暂无证据"; point at the
  // issue page instead (spec §5.5).
  const evidenceUnavailable = node.kind === "issue" && evidenceCount > 0 && caseRunId === ""
  const detail = useCaseRun(
    versionId,
    runBatchId,
    evidenceRequested && caseRunId !== "" ? caseRunId : "",
    stream,
  )
  const evidenceItems = detail.caseRun?.evidence ?? []

  const resultCaseId = node.kind === "result" ? node.caseNodeId : null
  const summary =
    node.kind === "result" && (snapshotNode?.summary ?? "") !== ""
      ? (snapshotNode?.summary ?? "")
      : node.kind === "requirement"
        ? "该需求派生出的用例与执行结果如下。"
        : node.kind === "case"
          ? "该用例的执行结果与证据如下。"
          : node.kind === "issue"
            ? "该问题关联的用例执行结果可追溯证据；打开问题页查看复现步骤与处理记录。"
            : "该执行结果暂无摘要。"

  return (
    <aside
      data-testid="chain-drawer"
      data-kind={node.kind}
      role="dialog"
      aria-label={`${kindLabel}详情`}
      className="rounded-2xl border border-border bg-card p-4"
    >
      <header className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <p className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
            {kindLabel}
          </p>
          <h3 className="mt-1 break-words text-sm font-semibold">{node.title}</h3>
        </div>
        <button
          type="button"
          data-testid="chain-drawer-close"
          aria-label="关闭节点详情"
          onClick={onClose}
          className={BUTTON_CLASS}
        >
          关闭
        </button>
      </header>

      <div className="mt-3 flex flex-wrap items-center gap-2">
        <StatusBadge variant={badgeVariant}>{statusLabel}</StatusBadge>
        {node.kind === "result" && node.result?.failureClass != null ? (
          <FailureClassBadge failureClass={node.result.failureClass} />
        ) : null}
        {node.kind === "result" ? (
          <span data-testid="chain-drawer-attempt" className="text-xs text-muted-foreground">
            {`第 ${snapshotNode?.attempt ?? node.result?.sortOrder ?? 1} 次尝试`}
          </span>
        ) : null}
        {node.kind === "result" && (snapshotNode?.batchId ?? "") !== "" ? (
          <span className="text-xs text-muted-foreground">
            {`批次 ${snapshotNode?.batchId ?? ""}`}
          </span>
        ) : null}
        {node.kind === "result" && formatDuration(snapshotNode?.durationMs) !== "" ? (
          <span className="text-xs text-muted-foreground">
            {`耗时 ${formatDuration(snapshotNode?.durationMs)}`}
          </span>
        ) : null}
        {node.kind === "result" || node.kind === "issue" ? (
          <span className="text-xs text-muted-foreground">{`证据 ${evidenceCount}`}</span>
        ) : null}
        {node.kind === "issue" ? (
          <>
            <span className="text-xs text-muted-foreground">
              {`严重级：${issueSeverityLabel(snapshotNode?.severity ?? "")}`}
            </span>
            <span className="text-xs text-muted-foreground">
              {`置信度：${issueConfidenceLabel(snapshotNode?.confidence ?? "")}`}
            </span>
            <span className="text-xs text-muted-foreground">
              {`分类：${issueCategoryLabel(snapshotNode?.category ?? "")}`}
            </span>
          </>
        ) : null}
      </div>

      <p className="mt-3 text-sm leading-relaxed text-muted-foreground">{summary}</p>

      {node.kind === "requirement" ? (
        <p className="mt-2 text-xs text-muted-foreground">
          {`需求状态：${requirementStatusLabel(node.status)}`}
        </p>
      ) : null}
      {node.kind === "case" ? (
        <>
          <p className="mt-2 text-xs text-muted-foreground">
            {`用例状态：${caseStatusLabel(node.status)}`}
          </p>
          {(snapshotNode?.requirementIds?.length ?? 0) === 0 ? null : (
            <p
              data-testid="chain-drawer-case-requirements"
              className="mt-1 text-xs text-muted-foreground"
            >
              {`关联需求：${(snapshotNode?.requirementIds ?? []).join("、")}`}
            </p>
          )}
        </>
      ) : null}

      <div className="mt-3 flex flex-wrap items-center gap-2">
        {node.kind === "requirement" ? (
          <button
            type="button"
            data-testid="chain-drawer-open-requirement"
            onClick={() => onNavigate?.({ kind: "requirement", id: node.id })}
            className={BUTTON_CLASS}
          >
            查看需求
          </button>
        ) : null}
        {node.kind === "case" ? (
          <button
            type="button"
            data-testid="chain-drawer-open-case"
            onClick={() => onNavigate?.({ kind: "case", id: node.id })}
            className={BUTTON_CLASS}
          >
            查看用例
          </button>
        ) : null}
        {node.kind === "issue" ? (
          <>
            <button
              type="button"
              data-testid="chain-drawer-open-issue"
              onClick={() => onNavigate?.({ kind: "issue", id: node.id })}
              className={BUTTON_CLASS}
            >
              查看问题
            </button>
            {node.caseNodeId === null ? null : (
              <button
                type="button"
                data-testid="chain-drawer-open-case"
                onClick={() => onNavigate?.({ kind: "case", id: node.caseNodeId ?? "" })}
                className={BUTTON_CLASS}
              >
                查看用例
              </button>
            )}
          </>
        ) : null}
        {node.kind === "result" && resultCaseId !== null ? (
          <>
            <button
              type="button"
              data-testid="chain-drawer-open-case"
              onClick={() => onNavigate?.({ kind: "case", id: resultCaseId })}
              className={BUTTON_CLASS}
            >
              查看用例
            </button>
            <button
              type="button"
              data-testid="chain-drawer-open-result"
              onClick={() => onNavigate?.({ kind: "result", id: node.id })}
              className={BUTTON_CLASS}
            >
              打开执行结果
            </button>
          </>
        ) : null}
      </div>

      <section data-testid="chain-drawer-evidence" className="mt-4">
        <h4 className="text-xs font-semibold">证据</h4>
        {evidenceUnavailable ? (
          <p
            data-testid="chain-drawer-evidence-unavailable"
            className="mt-1 text-xs text-muted-foreground"
          >
            该问题未关联执行结果，无法加载证据；请点击“查看问题”查看复现步骤与处理记录。
          </p>
        ) : evidenceCount === 0 ? (
          <p className="mt-1 text-xs text-muted-foreground">该节点暂无可查看证据。</p>
        ) : evidenceRequested ? (
          detail.loading ? (
            <p
              data-testid="chain-drawer-evidence-loading"
              role="status"
              className="mt-1 flex items-center gap-2 text-xs text-muted-foreground"
            >
              <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
              证据加载中…
            </p>
          ) : detail.error !== "" ? (
            <p
              data-testid="chain-drawer-evidence-error"
              role="alert"
              className="mt-1 text-xs text-destructive"
            >
              {`证据加载失败：${detail.error}`}
            </p>
          ) : evidenceItems.length === 0 ? (
            <p className="mt-1 text-xs text-muted-foreground">该节点暂无可查看证据。</p>
          ) : (
            <ul className="mt-1.5 flex flex-wrap gap-1.5">
              {evidenceItems.map((evidence) => (
                <li key={evidence.id}>
                  <button
                    type="button"
                    data-testid={`chain-drawer-evidence-${evidence.id}`}
                    onClick={() => onOpenEvidence?.(evidence.id)}
                    className={BUTTON_CLASS}
                  >
                    {`证据 ${evidence.id}`}
                  </button>
                </li>
              ))}
            </ul>
          )
        ) : (
          <button
            type="button"
            data-testid="chain-drawer-load-evidence"
            onClick={() => setEvidenceRequested(true)}
            className={`mt-1.5 ${BUTTON_CLASS}`}
          >
            {`加载证据（${evidenceCount}）`}
          </button>
        )}
      </section>

      <section
        data-testid="chain-drawer-issues"
        className="mt-4 rounded-xl border border-dashed border-border px-3 py-2"
      >
        <h4 className="text-xs font-semibold">问题</h4>
        <p className="mt-1 text-xs text-muted-foreground">
          {node.kind === "issue"
            ? "问题状态与处理结果以问题页为准。"
            : "该节点关联的问题节点会以红色菱形显示在链路图中。"}
        </p>
      </section>
    </aside>
  )
}

function streamStatusLabel(status: SSEStatus): string {
  if (status === "reconnecting" || status === "connecting") return "重连中"
  if (status === "live") return "实时"
  return "未连接"
}

/**
 * Exploration chain tab (spec §5.1/§5.5/§5.7): one read-model snapshot drives
 * the requirement → case → result → issue layers. The four former REST reads
 * (requirements/mindmap/case-runs/issues) are gone; every filter is applied by
 * the server, the version SSE stream coalesces into 500ms refreshes, truncation
 * and per-layer failures stay explicit, and evidence is lazily loaded by
 * `caseRunId` from the node Sheet.
 */
export function ExplorationTab({
  versionId,
  batchId,
  stream,
  onOpenEvidence,
  onNavigate,
  initialFilters,
  onFiltersChange,
  selectedNodeId,
  onSelectedNodeChange,
}: ExplorationTabProps) {
  const initialRequirementId = initialFilters?.requirementId ?? ""
  const initialStatus = normalizeStatusFilter(initialFilters?.resultStatus)
  const initialAbnormal = initialFilters?.abnormalOnly === true
  const initialSearch = initialFilters?.q ?? ""

  const [requirementFilter, setRequirementFilter] = useState(initialRequirementId)
  const [statusFilter, setStatusFilter] = useState<ChainResultFilter>(initialStatus)
  const [abnormalOnly, setAbnormalOnly] = useState(initialAbnormal)
  const [searchInput, setSearchInput] = useState(initialSearch)
  const [searchQuery, setSearchQuery] = useState(initialSearch)
  const [issueStatusFilter, setIssueStatusFilter] = useState("")
  const [issueSeverityFilter, setIssueSeverityFilter] = useState("")
  const [includeSupplement, setIncludeSupplement] = useState(true)
  const [revalidationOnly, setRevalidationOnly] = useState(false)
  const [batchScope, setBatchScope] = useState<"current" | "all">("current")
  const [requirementOptions, setRequirementOptions] = useState<ChainRequirementInput[]>([])
  const [selectedNodeLocal, setSelectedNodeLocal] = useState(selectedNodeId ?? "")
  const [collapsed, setCollapsed] = useState<ReadonlySet<string> | null>(null)

  const onFiltersChangeRef = useRef(onFiltersChange)
  useEffect(() => {
    onFiltersChangeRef.current = onFiltersChange
  }, [onFiltersChange])

  const effectiveBatchId = batchScope === "all" ? "all" : (batchId ?? "")
  const chain = useExplorationChain(
    versionId,
    {
      batchId: effectiveBatchId,
      requirementId: requirementFilter,
      resultStatus: statusFilter,
      abnormalOnly,
      issueStatus: issueStatusFilter,
      issueSeverity: issueSeverityFilter,
      q: searchQuery,
      includeSupplement,
      revalidationOnly,
    },
    stream,
  )

  // Persist the URL-owned filters after every change (and on first mount, which
  // normalizes a shared link into the canonical form).
  useEffect(() => {
    onFiltersChangeRef.current?.({
      requirementId: requirementFilter,
      resultStatus: statusFilter,
      abnormalOnly,
      q: searchQuery,
    })
  }, [requirementFilter, statusFilter, abnormalOnly, searchQuery])

  // The title search hits the server; debounce it for 300ms (spec §5.5).
  useEffect(() => {
    if (searchInput === searchQuery) return
    const timer = setTimeout(() => {
      setSearchQuery(searchInput.trim())
    }, SEARCH_DEBOUNCE_MS)
    return () => clearTimeout(timer)
  }, [searchInput, searchQuery])

  // Keep the requirement dropdown populated across filtered responses: a
  // single-requirement response only carries that requirement node.
  const view = useMemo(() => toChainView(chain.data), [chain.data])
  const optionsVersionRef = useRef(versionId)
  useEffect(() => {
    if (optionsVersionRef.current === versionId) return
    optionsVersionRef.current = versionId
    setRequirementOptions([])
  }, [versionId])
  useEffect(() => {
    if (chain.data === null || chain.data.versionId !== versionId) return
    const incoming = view.input.requirements
    if (incoming.length === 0) return
    setRequirementOptions((previous) => mergeRequirementOptions(previous, incoming))
  }, [view, versionId, chain.data])

  useEffect(() => {
    setSelectedNodeLocal(selectedNodeId ?? "")
  }, [selectedNodeId])

  const selectedNodeParam = selectedNodeId !== undefined ? selectedNodeId : selectedNodeLocal

  const handleSelectNode = useCallback(
    (nextNodeId: string): void => {
      setSelectedNodeLocal(nextNodeId)
      onSelectedNodeChange?.(nextNodeId)
    },
    [onSelectedNodeChange],
  )

  const autoCollapsed = useMemo(() => autoCollapsedRequirements(view.input), [view.input])
  const collapsedSet = useMemo(
    () => collapsed ?? new Set(autoCollapsed),
    [collapsed, autoCollapsed],
  )
  const layout = useMemo(
    () => layoutChain(view.input, { collapsed: collapsedSet }),
    [view.input, collapsedSet],
  )

  const parsedSelection = parseChainNodeId(selectedNodeParam)
  const selectedNode = useMemo(() => {
    if (parsedSelection === null) return null
    const visible = layout.nodes.find(
      (node) => node.kind === parsedSelection.kind && node.id === parsedSelection.id,
    )
    if (visible !== undefined) return visible
    const dto = view.dtoByKey.get(`${parsedSelection.kind}:${parsedSelection.id}`)
    return dto === undefined ? null : toFallbackChainNode(view, dto)
  }, [layout, view, parsedSelection])
  const selectedSnapshotNode =
    parsedSelection === null
      ? undefined
      : view.dtoByKey.get(`${parsedSelection.kind}:${parsedSelection.id}`)
  const nodeMissing =
    selectedNodeParam !== "" &&
    chain.data !== null &&
    !chain.loading &&
    chain.error === "" &&
    selectedNode === null

  const toggleRequirement = useCallback(
    (requirementId: string): void => {
      setCollapsed((previous) => {
        const next = new Set(previous ?? autoCollapsed)
        if (next.has(requirementId)) next.delete(requirementId)
        else next.add(requirementId)
        return next
      })
    },
    [autoCollapsed],
  )

  const handleSelect = useCallback(
    (node: ChainNode): void => {
      handleSelectNode(`${node.kind}:${node.id}`)
    },
    [handleSelectNode],
  )

  const requirementSelectOptions = useMemo(() => {
    if (
      requirementFilter === "" ||
      requirementOptions.some((option) => option.id === requirementFilter)
    ) {
      return requirementOptions
    }
    return [...requirementOptions, { id: requirementFilter, title: `需求 ${requirementFilter}` }]
  }, [requirementOptions, requirementFilter])

  const counts = chain.data?.counts
  const totalAvailable = counts === undefined ? 0 : chainSnapshotTotal(counts)
  const truncatedFlags = chain.data?.truncated
  const truncatedLayers =
    truncatedFlags === undefined
      ? []
      : (
          [
            ["requirements", "需求"],
            ["cases", "用例"],
            ["results", "结果"],
            ["issues", "问题"],
          ] as const
        )
          .filter(([key]) => truncatedFlags[key])
          .map(([, label]) => label)
  const isTruncated = truncatedLayers.length > 0

  const filtersActive =
    requirementFilter !== "" ||
    statusFilter !== "" ||
    abnormalOnly ||
    searchQuery !== "" ||
    issueStatusFilter !== "" ||
    issueSeverityFilter !== "" ||
    !includeSupplement ||
    revalidationOnly

  const hasData = layout.nodes.length > 0
  const hasSnapshot = chain.data !== null
  const issuesUnavailable = chain.error !== ""
  const issueNodeCount = chain.data?.nodes.filter((node) => node.kind === "issue").length ?? 0
  const issueCountLabel = issuesUnavailable
    ? "问题数据不可用"
    : counts === undefined
      ? "问题加载中…"
      : chain.data?.truncated.issues === true
        ? `${issueNodeCount} 个问题（共 ${counts.issues} 个）`
        : `${counts.issues} 个问题`

  function retry(): void {
    void chain.refresh()
  }

  return (
    <section data-testid="exploration-tab" className="space-y-3">
      <header className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <h2 className="text-base font-semibold tracking-tight">探索链路</h2>
          <p className="mt-0.5 text-xs text-muted-foreground">
            需求 → 用例 → 结果 → 问题的血缘图；红色菱形为问题，虚线 AI
            角标为补全用例，黄色描边为需重验用例。筛选与搜索由服务端读模型完成，拖拽平移、滚轮缩放、按需求折叠。
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <p
            data-testid="chain-stream-status"
            data-state={chain.streamStatus}
            className={
              chain.streamStatus === "live"
                ? "text-xs text-muted-foreground"
                : "text-xs font-medium text-amber-700 dark:text-amber-300"
            }
          >
            {streamStatusLabel(chain.streamStatus)}
          </p>
          <p data-testid="exploration-visible-count" className="text-xs text-muted-foreground">
            {`可见 ${layout.visibleIds.length} 个节点${
              layout.hiddenIds.length > 0 ? `，折叠 ${layout.hiddenIds.length} 个` : ""
            }`}
          </p>
          <button
            type="button"
            data-testid="exploration-refresh"
            onClick={retry}
            disabled={chain.loading}
            className={BUTTON_CLASS}
          >
            <RefreshCw className="h-3.5 w-3.5" aria-hidden="true" />
            刷新
          </button>
        </div>
      </header>

      <div className="grid gap-2 rounded-2xl border border-border bg-card p-3 sm:grid-cols-2 lg:grid-cols-4">
        <label className="text-xs font-medium text-muted-foreground">
          需求
          <select
            data-testid="exploration-filter-requirement"
            value={requirementFilter}
            onChange={(event) => setRequirementFilter(event.target.value)}
            className={FIELD_CLASS}
          >
            <option value="">全部需求</option>
            {requirementSelectOptions.map((row) => (
              <option key={row.id} value={row.id}>
                {row.title}
              </option>
            ))}
          </select>
        </label>

        <label className="text-xs font-medium text-muted-foreground">
          结果状态
          <select
            data-testid="exploration-filter-status"
            value={statusFilter}
            onChange={(event) => setStatusFilter(event.target.value as ChainResultFilter)}
            className={FIELD_CLASS}
          >
            {STATUS_FILTER_OPTIONS.map((option) => (
              <option key={option.value} value={option.value}>
                {option.label}
              </option>
            ))}
          </select>
        </label>

        <label className="text-xs font-medium text-muted-foreground">
          批次
          <select
            data-testid="chain-batch-filter"
            value={batchScope}
            onChange={(event) => setBatchScope(event.target.value === "all" ? "all" : "current")}
            className={FIELD_CLASS}
          >
            <option value="current" disabled={batchId === undefined || batchId === ""}>
              当前批次
            </option>
            <option value="all">全部批次</option>
          </select>
        </label>

        <label className="text-xs font-medium text-muted-foreground">
          问题严重级
          <select
            data-testid="exploration-filter-issue-severity"
            value={issueSeverityFilter}
            onChange={(event) => setIssueSeverityFilter(event.target.value)}
            className={FIELD_CLASS}
          >
            {ISSUE_SEVERITY_FILTER_OPTIONS.map((option) => (
              <option key={option.value} value={option.value}>
                {option.label}
              </option>
            ))}
          </select>
        </label>

        <label className="text-xs font-medium text-muted-foreground">
          问题状态
          <select
            data-testid="exploration-filter-issue-status"
            value={issueStatusFilter}
            onChange={(event) => setIssueStatusFilter(event.target.value)}
            className={FIELD_CLASS}
          >
            {ISSUE_STATUS_FILTER_OPTIONS.map((option) => (
              <option key={option.value} value={option.value}>
                {option.label}
              </option>
            ))}
          </select>
        </label>

        <label className="text-xs font-medium text-muted-foreground">
          搜索需求/用例
          <input
            type="search"
            data-testid="exploration-filter-search"
            value={searchInput}
            onChange={(event) => setSearchInput(event.target.value)}
            placeholder="标题关键字"
            className={FIELD_CLASS}
          />
        </label>

        <label className="flex items-end gap-2 pb-1.5 text-xs font-medium text-muted-foreground">
          <input
            type="checkbox"
            data-testid="exploration-filter-abnormal"
            checked={abnormalOnly}
            onChange={(event) => setAbnormalOnly(event.target.checked)}
            className="h-4 w-4 rounded border-border"
          />
          仅异常
        </label>

        <div className="flex flex-col justify-end gap-1 text-xs font-medium text-muted-foreground">
          <label className="flex items-center gap-2">
            <input
              type="checkbox"
              data-testid="exploration-filter-supplement"
              checked={includeSupplement}
              onChange={(event) => setIncludeSupplement(event.target.checked)}
              className="h-4 w-4 rounded border-border"
            />
            包含 AI 补充
          </label>
          <label className="flex items-center gap-2">
            <input
              type="checkbox"
              data-testid="exploration-filter-revalidation"
              checked={revalidationOnly}
              onChange={(event) => setRevalidationOnly(event.target.checked)}
              className="h-4 w-4 rounded border-border"
            />
            仅需重验
          </label>
        </div>
      </div>

      {isTruncated && chain.data !== null && counts !== undefined ? (
        <div
          data-testid="chain-truncated"
          role="status"
          className="rounded-xl border border-amber-500/25 bg-amber-500/10 px-4 py-3 text-xs text-amber-700 dark:text-amber-300"
        >
          {`已显示前 ${chain.data.nodes.length} 条（共 ${totalAvailable} 条），请收窄筛选（${truncatedLayers.join("、")}层已截断）。`}
        </div>
      ) : null}

      {chain.error !== "" && hasSnapshot ? (
        <div
          data-testid="exploration-degraded"
          role="alert"
          className="flex flex-wrap items-center justify-between gap-2 rounded-xl border border-amber-500/25 bg-amber-500/10 px-4 py-3 text-sm text-amber-700 dark:text-amber-300"
        >
          <p>{`链路刷新失败（保留上次数据）：${chain.error}`}</p>
          <button type="button" onClick={retry} className={BUTTON_CLASS}>
            重试
          </button>
        </div>
      ) : null}

      {chain.error !== "" && !hasSnapshot && !chain.loading ? (
        <div
          data-testid="exploration-error"
          role="alert"
          className="flex flex-wrap items-center justify-between gap-2 rounded-xl border border-destructive/30 bg-destructive/10 px-4 py-3 text-sm text-destructive"
        >
          <p>{`加载探索链路失败：${chain.error}`}</p>
          <button type="button" onClick={retry} className={BUTTON_CLASS}>
            重试
          </button>
        </div>
      ) : null}

      {chain.loading && !hasSnapshot ? (
        <p
          data-testid="exploration-loading"
          role="status"
          className="flex items-center gap-2 rounded-xl border border-dashed border-border px-4 py-6 text-sm text-muted-foreground"
        >
          <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
          探索链路加载中…
        </p>
      ) : null}

      {hasSnapshot && !hasData && totalAvailable === 0 && !filtersActive ? (
        <div data-testid="exploration-empty">
          <EmptyState
            icon={GitBranch}
            title="暂无探索链路"
            description="版本的需求、用例与执行结果会在这里形成需求 → 用例 → 结果链路。"
          />
        </div>
      ) : null}

      {hasSnapshot && !hasData && totalAvailable === 0 && filtersActive ? (
        <p
          data-testid="exploration-filtered-empty"
          className="rounded-xl border border-dashed border-border px-4 py-10 text-center text-sm text-muted-foreground"
        >
          没有符合筛选条件的链路节点
        </p>
      ) : null}

      {nodeMissing ? (
        <p
          data-testid="chain-node-missing"
          role="alert"
          className="flex flex-wrap items-center justify-between gap-2 rounded-xl border border-dashed border-border px-4 py-3 text-sm text-muted-foreground"
        >
          <span>节点不存在或已被刷新，请重新选择链路节点。</span>
          <button type="button" onClick={() => handleSelectNode("")} className={BUTTON_CLASS}>
            关闭
          </button>
        </p>
      ) : null}

      {hasData ? (
        <div className="grid gap-3 xl:grid-cols-[minmax(0,1fr)_320px]">
          <ChainGraph
            layout={layout}
            selectedId={selectedNode?.id ?? null}
            onSelect={handleSelect}
            onToggleRequirement={toggleRequirement}
          />

          <div className="space-y-3">
            {selectedNode === null ? (
              <aside
                data-testid="chain-drawer-hint"
                className="rounded-2xl border border-dashed border-border bg-card/60 px-4 py-6 text-sm text-muted-foreground"
              >
                点击节点查看摘要、证据与深链。
              </aside>
            ) : (
              <ChainDrawer
                key={`${selectedNode.kind}:${selectedNode.id}`}
                node={selectedNode}
                snapshotNode={selectedSnapshotNode}
                view={view}
                versionId={versionId}
                batchId={batchId ?? ""}
                stream={stream}
                onClose={() => handleSelectNode("")}
                onOpenEvidence={onOpenEvidence}
                onNavigate={onNavigate}
              />
            )}
          </div>
        </div>
      ) : null}

      <aside
        data-testid="chain-issue-panel"
        className="rounded-2xl border border-border bg-card px-4 py-3"
      >
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h3 className="text-sm font-semibold">问题节点</h3>
          <p
            data-testid="chain-issue-count"
            data-state={issuesUnavailable ? "degraded" : "ready"}
            className="text-xs text-muted-foreground"
          >
            {issueCountLabel}
          </p>
        </div>

        {issuesUnavailable ? (
          <p
            data-testid="chain-issues-degraded"
            role="alert"
            className="mt-2 rounded-lg border border-amber-500/25 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300"
          >
            {`问题数据加载失败：${chain.error} 问题节点可能不完整（AI 分析降级时同样可能不完整）。`}
          </p>
        ) : chain.loading && !hasSnapshot ? (
          <p
            data-testid="chain-issues-loading"
            role="status"
            className="mt-2 flex items-center gap-2 text-xs text-muted-foreground"
          >
            <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
            问题加载中…
          </p>
        ) : counts !== undefined && counts.issues === 0 && chain.data?.truncated.issues !== true ? (
          <p data-testid="chain-issues-empty" className="mt-2 text-xs text-muted-foreground">
            本批次未发现问题。
          </p>
        ) : (
          <p className="mt-2 text-xs text-muted-foreground">
            {`红色菱形为问题节点（${
              batchScope === "all" ? "全部批次" : "当前批次"
            }）；点击节点查看摘要、证据与深链。`}
          </p>
        )}

        {layout.unlinkedIssues.length === 0 ? null : (
          <div data-testid="chain-unlinked-issues" className="mt-3">
            <h4 className="text-xs font-semibold">未关联用例的问题</h4>
            <ul className="mt-1.5 flex flex-wrap gap-1.5">
              {layout.unlinkedIssues.map((issue) => (
                <li key={issue.id}>
                  <button
                    type="button"
                    data-testid={`chain-unlinked-issue-${issue.id}`}
                    onClick={() => onNavigate?.({ kind: "issue", id: issue.id })}
                    className={BUTTON_CLASS}
                  >
                    {`${issueSeverityLabel(issue.severity ?? "")} · ${issue.title}`}
                  </button>
                </li>
              ))}
            </ul>
          </div>
        )}
      </aside>
    </section>
  )
}
