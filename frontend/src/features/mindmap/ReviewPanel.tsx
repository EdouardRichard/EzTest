"use client"

import { Loader2, RefreshCw } from "lucide-react"
import { useCallback, useEffect, useId, useMemo, useRef, useState } from "react"

import { StatusBadge, type StatusBadgeVariant } from "@/components/ui/StatusBadge"

import { mindmapApi } from "./api"
import { describeMindmapError } from "./error-messages"
import type {
  BaselineView,
  CaseFindingView,
  FindingCategory,
  FindingSeverity,
  FindingStatus,
  MindmapGraph,
  ReviewDecision,
} from "./types"

/** Severity groups in display order (spec §9.2). */
const SEVERITY_ORDER: readonly FindingSeverity[] = ["blocker", "major", "minor", "info"]

const SEVERITY_META: Record<FindingSeverity, { label: string; variant: StatusBadgeVariant }> = {
  blocker: { label: "阻塞 blocker", variant: "danger" },
  major: { label: "严重 major", variant: "warning" },
  minor: { label: "一般 minor", variant: "info" },
  info: { label: "提示 info", variant: "neutral" },
}

const FINDING_STATUS_META: Record<FindingStatus, { label: string; variant: StatusBadgeVariant }> = {
  open: { label: "未处理", variant: "warning" },
  fixed: { label: "已修复", variant: "success" },
  dismissed: { label: "已忽略", variant: "neutral" },
}

const CATEGORY_LABEL: Record<FindingCategory, string> = {
  coverage: "覆盖",
  assertion: "断言",
  executability: "可执行性",
  duplicate: "重复",
  code_uncovered: "代码未覆盖",
  other: "其他",
}

const BUTTON_CLASS =
  "inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-2.5 py-1.5 text-xs font-medium text-foreground transition-colors hover:bg-muted disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"

const PRIMARY_BUTTON_CLASS =
  "inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"

export type ReviewPanelProps = {
  /** P3-owned version id; never re-parsed (spec §7.1). */
  versionId: string
  /** Server-authoritative graph snapshot used for status, baseline and node titles. */
  graph: MindmapGraph
  /** Spec §6.3 status gate: review actions are only open designing/reviewing/executing. */
  disabled?: boolean
  /** Notifies the page after a successful write so it can refetch the graph. */
  onChanged?: () => void
}

/**
 * Review panel (spec §9.2): findings grouped by severity with fixed/dismissed
 * updates, the review-run entry point, and the approve/reject gate. Approving
 * while open blocker findings exist requires the explicit
 * "已知悉 blocker 并继续" acknowledgement (D35); rejecting always requires a
 * non-empty comment. Failures surface the mapped Chinese `details.reason` copy
 * and never raw server text.
 */
export function ReviewPanel({ versionId, graph, disabled = false, onChanged }: ReviewPanelProps) {
  const uid = useId()
  const [findings, setFindings] = useState<CaseFindingView[]>(() => graph.findings ?? [])
  const [loading, setLoading] = useState(true)
  const [findingsLoaded, setFindingsLoaded] = useState(false)
  const [error, setError] = useState("")
  const [notice, setNotice] = useState("")
  const [busyFindingId, setBusyFindingId] = useState<string | null>(null)
  const [startingRun, setStartingRun] = useState(false)
  const [deciding, setDeciding] = useState(false)
  const [acknowledged, setAcknowledged] = useState(false)
  const [comment, setComment] = useState("")
  const [baseline, setBaseline] = useState<BaselineView | null>(graph.baseline ?? null)
  const requestRef = useRef(0)

  const loadFindings = useCallback(async (): Promise<void> => {
    const request = requestRef.current + 1
    requestRef.current = request
    setLoading(true)
    try {
      const items = await mindmapApi.listFindings(versionId)
      if (requestRef.current !== request) return
      setFindings(items)
      setFindingsLoaded(true)
      setError("")
    } catch (caught) {
      if (requestRef.current !== request) return
      setError(describeMindmapError(caught))
    } finally {
      if (requestRef.current === request) setLoading(false)
    }
  }, [versionId])

  // Initial read and version switch; a late response from the old version is
  // dropped by the request counter.
  useEffect(() => {
    void loadFindings()
    return () => {
      requestRef.current += 1
    }
  }, [loadFindings])

  // A finished review run replaces the finding set; reload when the graph's
  // review summary reaches a terminal state (covers another tab / SSE refresh).
  const reviewRunSignature = `${graph.latestRuns.review?.id ?? ""}:${
    graph.latestRuns.review?.status ?? ""
  }`
  const previousReviewSignatureRef = useRef(reviewRunSignature)
  useEffect(() => {
    if (previousReviewSignatureRef.current === reviewRunSignature) return
    previousReviewSignatureRef.current = reviewRunSignature
    const status = graph.latestRuns.review?.status
    if (status === "done" || status === "degraded" || status === "failed") {
      void loadFindings()
    }
  }, [reviewRunSignature, graph.latestRuns.review?.status, loadFindings])

  const openFindings = useMemo(
    () => findings.filter((finding) => finding.status === "open"),
    [findings],
  )
  const openBlockers = useMemo(
    () => openFindings.filter((finding) => finding.severity === "blocker"),
    [openFindings],
  )
  /** Before the first authoritative read, trust the per-node counters. */
  const graphBlockerCount = useMemo(
    () => graph.nodes.reduce((total, node) => total + node.openBlockerCount, 0),
    [graph.nodes],
  )
  const blockerCount = findingsLoaded ? openBlockers.length : graphBlockerCount

  const grouped = useMemo(() => {
    const bySeverity: Record<FindingSeverity, CaseFindingView[]> = {
      blocker: [],
      major: [],
      minor: [],
      info: [],
    }
    for (const finding of findings) bySeverity[finding.severity].push(finding)
    return SEVERITY_ORDER.map((severity) => ({ severity, items: bySeverity[severity] })).filter(
      (group) => group.items.length > 0,
    )
  }, [findings])

  /** The freshest baseline: the approve response, else the server snapshot. */
  const baselineView = baseline ?? graph.baseline
  const frozen = baselineView !== null
  const canDecide = graph.versionStatus === "reviewing" && !frozen && !deciding
  const blockerGate = blockerCount > 0 && !acknowledged
  const approveDisabled = !canDecide || blockerGate
  const rejectDisabled = !canDecide || comment.trim() === ""

  async function startReview(): Promise<void> {
    setStartingRun(true)
    setError("")
    setNotice("")
    try {
      const result = await mindmapApi.startRun(versionId, { kind: "review" })
      setNotice(`评审已启动（运行 ${result.runId}），完成后会自动刷新发现项。`)
      onChanged?.()
    } catch (caught) {
      setError(describeMindmapError(caught))
    } finally {
      setStartingRun(false)
    }
  }

  async function submitReview(): Promise<void> {
    setDeciding(true)
    setError("")
    setNotice("")
    try {
      await mindmapApi.submitReview(versionId)
      setNotice("已提交评审，版本进入评审阶段。")
      onChanged?.()
    } catch (caught) {
      setError(describeMindmapError(caught))
    } finally {
      setDeciding(false)
    }
  }

  async function markFinding(findingId: string, status: "fixed" | "dismissed"): Promise<void> {
    setBusyFindingId(findingId)
    setError("")
    try {
      const updated = await mindmapApi.updateFinding(versionId, findingId, { status })
      setFindings((previous) =>
        previous.map((finding) => (finding.id === findingId ? updated : finding)),
      )
      onChanged?.()
    } catch (caught) {
      setError(describeMindmapError(caught))
    } finally {
      setBusyFindingId(null)
    }
  }

  async function decide(decision: ReviewDecision): Promise<void> {
    if (decision === "reject" && comment.trim() === "") {
      setError("驳回必须填写评论。")
      return
    }
    if (decision === "approve" && blockerGate) {
      setError("存在未处理的 blocker 评审意见，请先勾选“已知悉 blocker 并继续”。")
      return
    }

    setDeciding(true)
    setError("")
    setNotice("")
    try {
      const body =
        decision === "reject"
          ? { decision, comment: comment.trim() }
          : { decision, ...(blockerCount > 0 ? { acknowledgeFindings: true } : {}) }
      const result = await mindmapApi.decideReview(versionId, body)
      if (decision === "approve") {
        if (result.baseline !== undefined && result.baseline !== null) {
          setBaseline(result.baseline)
        }
        setNotice("已批准：版本进入执行阶段并冻结基线。")
      } else {
        setNotice("已驳回：版本回到设计阶段。")
        setComment("")
        setAcknowledged(false)
      }
      onChanged?.()
    } catch (caught) {
      setError(describeMindmapError(caught))
    } finally {
      setDeciding(false)
    }
  }

  function nodeTitle(nodeId: string | null): string | null {
    if (nodeId === null) return null
    return graph.nodes.find((node) => node.id === nodeId)?.title ?? nodeId
  }

  return (
    <section
      aria-label="评审"
      data-testid="review-panel"
      className="min-w-0 rounded-2xl border border-border bg-card p-4"
    >
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <h2 className="text-base font-semibold">评审</h2>
          <p className="mt-0.5 text-xs text-muted-foreground">
            AI 评审发现项按严重级分组；处置后可批准（冻结基线）或驳回。
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-1.5">
          {loading ? (
            <Loader2
              className="h-3.5 w-3.5 animate-spin text-muted-foreground"
              aria-hidden="true"
            />
          ) : null}
          <button
            type="button"
            data-testid="btn-refresh-findings"
            onClick={() => void loadFindings()}
            disabled={loading}
            className={BUTTON_CLASS}
          >
            <RefreshCw className="h-3.5 w-3.5" aria-hidden="true" />
            刷新发现项
          </button>
          <button
            type="button"
            data-testid="btn-run-review"
            onClick={() => void startReview()}
            disabled={disabled || startingRun}
            className={BUTTON_CLASS}
          >
            {startingRun ? (
              <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
            ) : null}
            {startingRun ? "正在启动…" : "发起评审运行"}
          </button>
          {graph.versionStatus === "designing" && !frozen ? (
            <button
              type="button"
              data-testid="btn-submit-review"
              onClick={() => void submitReview()}
              disabled={disabled || deciding}
              className={PRIMARY_BUTTON_CLASS}
            >
              提交评审
            </button>
          ) : null}
        </div>
      </div>

      {error === "" ? null : (
        <p
          role="alert"
          className="mt-2 rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
        >
          {error}
        </p>
      )}
      {notice === "" ? null : (
        <p
          role="status"
          className="mt-2 rounded-xl border border-emerald-500/30 bg-emerald-500/10 px-3 py-2 text-xs text-emerald-700 dark:text-emerald-300"
        >
          {notice}
        </p>
      )}

      {disabled ? (
        <p data-testid="review-status-gate" className="mt-2 text-xs text-muted-foreground">
          当前版本状态不开放评审操作（仅设计、评审、执行阶段开放）。
        </p>
      ) : null}

      {baselineView === null ? null : (
        <p
          data-testid="baseline-badge"
          className="mt-2 rounded-xl border border-sky-500/30 bg-sky-500/10 px-3 py-2 text-xs text-sky-700 dark:text-sky-300"
        >
          {`已绑定基线：${baselineView.caseCount} 个用例 · ${baselineView.nodeCount} 个节点（${baselineView.id}）`}
        </p>
      )}

      <p className="mt-2 text-xs text-muted-foreground">
        {`未处理 blocker：${blockerCount} 个 · 发现项合计 ${findings.length} 条`}
      </p>

      {loading && !findingsLoaded ? (
        <p className="mt-3 flex items-center gap-1.5 text-xs text-muted-foreground">
          <Loader2 className="h-3 w-3 animate-spin" aria-hidden="true" />
          正在加载评审发现项…
        </p>
      ) : null}

      {!loading && findings.length === 0 ? (
        <div className="mt-3 rounded-xl border border-dashed border-border px-3 py-4 text-center">
          <p className="text-sm font-medium">暂无评审发现项</p>
          <p className="mt-1 text-xs text-muted-foreground">
            可发起 AI 评审运行，或直接批准 / 驳回当前用例。
          </p>
        </div>
      ) : null}

      {grouped.length === 0 ? null : (
        <div className="mt-3 space-y-3">
          {grouped.map(({ severity, items }) => {
            const meta = SEVERITY_META[severity]
            return (
              <section
                key={severity}
                data-testid={`findings-group-${severity}`}
                aria-label={meta.label}
                className="rounded-xl border border-border px-3 py-2"
              >
                <h3 className="flex items-center gap-1.5 text-xs font-semibold">
                  <StatusBadge variant={meta.variant}>{meta.label}</StatusBadge>
                  <span className="text-muted-foreground">{`${items.length} 条`}</span>
                </h3>
                <ul className="mt-2 space-y-2">
                  {items.map((item) => {
                    const statusMeta = FINDING_STATUS_META[item.status]
                    const title = nodeTitle(item.nodeId)
                    return (
                      <li
                        key={item.id}
                        data-testid={`finding-row-${item.id}`}
                        className="rounded-lg border border-border/70 bg-muted/20 px-2.5 py-2"
                      >
                        <div className="flex flex-wrap items-center gap-1.5 text-[11px] text-muted-foreground">
                          <StatusBadge variant={statusMeta.variant}>{statusMeta.label}</StatusBadge>
                          <span>{CATEGORY_LABEL[item.category]}</span>
                          <span>{item.source === "ai" ? "AI 发现" : "人工记录"}</span>
                        </div>
                        <p className="mt-1 break-words text-sm">{item.detail}</p>
                        {title === null ? null : (
                          <p className="mt-0.5 break-words text-[11px] text-muted-foreground">
                            {`关联用例：${title}`}
                          </p>
                        )}
                        {item.status === "open" ? (
                          <div className="mt-1.5 flex flex-wrap gap-1.5">
                            <button
                              type="button"
                              onClick={() => void markFinding(item.id, "fixed")}
                              disabled={disabled || busyFindingId === item.id}
                              className={BUTTON_CLASS}
                            >
                              {busyFindingId === item.id ? (
                                <Loader2 className="h-3 w-3 animate-spin" aria-hidden="true" />
                              ) : null}
                              标记已修复
                            </button>
                            <button
                              type="button"
                              onClick={() => void markFinding(item.id, "dismissed")}
                              disabled={disabled || busyFindingId === item.id}
                              className={BUTTON_CLASS}
                            >
                              忽略
                            </button>
                          </div>
                        ) : null}
                      </li>
                    )
                  })}
                </ul>
              </section>
            )
          })}
        </div>
      )}

      <div className="mt-4 space-y-2 rounded-xl border border-border p-3">
        <h3 className="text-sm font-semibold">评审结论</h3>
        <div>
          <label htmlFor={`${uid}-comment`} className="text-xs font-medium text-muted-foreground">
            评审评论
          </label>
          <textarea
            id={`${uid}-comment`}
            value={comment}
            rows={2}
            maxLength={5000}
            placeholder="驳回时必填；批准时可留空。"
            onChange={(event) => setComment(event.target.value)}
            className="mt-1 w-full resize-y rounded-lg border border-border bg-input px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
          />
        </div>

        {blockerCount > 0 && !frozen ? (
          <div className="space-y-1">
            <label className="flex items-start gap-2 text-xs text-muted-foreground">
              <input
                type="checkbox"
                checked={acknowledged}
                onChange={(event) => setAcknowledged(event.target.checked)}
                className="mt-0.5 h-3.5 w-3.5 rounded border-border accent-primary"
              />
              <span>已知悉 blocker 并继续</span>
            </label>
            <p className="text-[11px] text-muted-foreground">
              {`仍有 ${blockerCount} 个未处理 blocker，批准后不会自动关闭。`}
            </p>
          </div>
        ) : null}

        {frozen ? (
          <p className="text-xs text-muted-foreground">
            版本已批准并冻结基线，不可重复批准或驳回。
          </p>
        ) : graph.versionStatus !== "reviewing" ? (
          <p className="text-xs text-muted-foreground">只有评审阶段的版本才能批准或驳回。</p>
        ) : null}

        <div className="flex flex-wrap justify-end gap-1.5">
          <button
            type="button"
            data-testid="btn-reject"
            onClick={() => void decide("reject")}
            disabled={disabled || rejectDisabled}
            className="inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-2.5 py-1.5 text-xs font-medium text-destructive transition-colors hover:bg-destructive/10 disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            {deciding ? <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" /> : null}
            驳回
          </button>
          <button
            type="button"
            data-testid="btn-approve"
            onClick={() => void decide("approve")}
            disabled={disabled || approveDisabled}
            className={PRIMARY_BUTTON_CLASS}
          >
            {deciding ? <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" /> : null}
            批准并冻结基线
          </button>
        </div>
      </div>
    </section>
  )
}
