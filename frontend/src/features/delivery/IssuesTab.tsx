"use client"

import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { useCallback, useEffect, useMemo, useRef, useState } from "react"

import { Badge, type BadgeTone } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState, InlineError } from "@/components/ui/ErrorState"
import { Field } from "@/components/ui/Field"
import { Select } from "@/components/ui/Select"
import { LoadingCard } from "@/components/ui/Spinner"
import { Table, type TableColumn } from "@/components/ui/Table"
import { useVersionEvents } from "@/features/clarification/useVersionEvents"
import {
  closedRequirementId,
  requirementClosedMessage,
} from "@/features/execution/closed-requirement"
import { formatDateTime } from "@/lib/format"

import { confirmIssue, dismissIssue, getIssue, listIssues, reopenIssue, retestIssue } from "./api"
import { IssueDetailSheet, type IssueTransitionAction } from "./IssueDetailSheet"
import { MergeIssueDialog } from "./MergeIssueDialog"
import {
  ISSUE_CATEGORY_LABELS,
  ISSUE_RETEST_STATUS_LABELS,
  ISSUE_RETEST_STATUS_TONES,
  ISSUE_SEVERITY_LABELS,
  ISSUE_STATUS_LABELS,
  ISSUE_STATUS_ORDER,
  type IssueCategory,
  type IssueDetailView,
  type IssueListOptions,
  type IssueRetestStatus,
  type IssueSeverity,
  type IssueStatus,
  type IssueView,
  type RetestIssueResult,
} from "./types"

export type IssuesTabProps = {
  versionId: string
}

type IssueFilters = {
  status: IssueStatus | "all"
  category: IssueCategory | "all"
  severity: IssueSeverity | "all"
  versionSutId: string
}

const DEFAULT_FILTERS: IssueFilters = {
  status: "all",
  category: "all",
  severity: "all",
  versionSutId: "all",
}

const STATUS_TONE: Record<IssueStatus, BadgeTone> = {
  open: "warning",
  confirmed: "success",
  dismissed: "neutral",
  merged: "info",
}

/** SSE bursts (issue.reopened + issue.updated) collapse into one snapshot refetch. */
const SSE_REFRESH_DEBOUNCE_MS = 350

function retestStatusOf(issue: IssueView): IssueRetestStatus {
  return issue.retest_status ?? "not_retested"
}

/** Dismissed/merged issues are terminal; a running retest blocks another one. */
function canRetest(issue: IssueView): boolean {
  return (
    (issue.status === "open" || issue.status === "confirmed") &&
    retestStatusOf(issue) !== "retesting"
  )
}

/** Deep link to one retest case run; the wave comes from the retest response. */
function retestRunHref(versionId: string, waveId: string, caseRunId: string): string {
  const params = new URLSearchParams({ tab: "execution", wave: waveId, case_run: caseRunId })
  return `/versions/${encodeURIComponent(versionId)}?${params.toString()}`
}

function buildListOptions(filters: IssueFilters): IssueListOptions {
  const options: IssueListOptions = { limit: 20 }
  if (filters.status !== "all") options.status = filters.status
  if (filters.category !== "all") options.category = filters.category
  if (filters.severity !== "all") options.severity = filters.severity
  if (filters.versionSutId !== "all") options.versionSutId = filters.versionSutId
  return options
}

function hasActiveFilters(filters: IssueFilters): boolean {
  return (
    filters.status !== "all" ||
    filters.category !== "all" ||
    filters.severity !== "all" ||
    filters.versionSutId !== "all"
  )
}

function compareIssues(left: IssueView, right: IssueView): number {
  const statusOrder = ISSUE_STATUS_ORDER[left.status] - ISSUE_STATUS_ORDER[right.status]
  if (statusOrder !== 0) return statusOrder
  try {
    const leftId = BigInt(left.id)
    const rightId = BigInt(right.id)
    if (leftId === rightId) return 0
    return leftId < rightId ? 1 : -1
  } catch {
    return right.id.localeCompare(left.id)
  }
}

function sutLabel(issue: IssueView): string {
  const ids = Array.from(
    new Set(
      issue.refs
        .map((ref) => ref.version_sut_id)
        .filter((value): value is string => typeof value === "string" && value.length > 0),
    ),
  ).sort()
  return ids.length === 0 ? "—" : ids.map((id) => `SUT ${id}`).join("、")
}

/**
 * P4 issue tab (spec §9.2): status/category/severity/SUT filters over a table,
 * plus a URL-backed detail side sheet. List ordering is client-side because the
 * API page is id DESC while the product wants open issues first; `?issue=` is
 * the deep-link source of truth. Requests only go through `./api` (api-client).
 */
export function IssuesTab({ versionId }: IssuesTabProps) {
  const router = useRouter()
  const searchParams = useSearchParams()
  const urlIssueId = searchParams.get("issue")

  const [filters, setFilters] = useState<IssueFilters>(DEFAULT_FILTERS)
  const [issues, setIssues] = useState<IssueView[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<unknown>(null)
  const [reloadToken, setReloadToken] = useState(0)

  const [selectedIssueId, setSelectedIssueId] = useState<string | null>(urlIssueId)
  const [detail, setDetail] = useState<IssueDetailView | null>(null)
  const [detailLoading, setDetailLoading] = useState(false)
  const [detailError, setDetailError] = useState<unknown>(null)
  const [detailToken, setDetailToken] = useState(0)
  const [merging, setMerging] = useState(false)
  const [retestingIds, setRetestingIds] = useState<Set<string>>(() => new Set())
  const [retestError, setRetestError] = useState<unknown>(null)
  const [retestNotice, setRetestNotice] = useState<RetestIssueResult | null>(null)

  const listOptions = useMemo(() => buildListOptions(filters), [filters])

  const fetchIssues = useCallback(
    () => listIssues(versionId, listOptions),
    [versionId, listOptions],
  )

  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadToken forces a retry
  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)
    void fetchIssues()
      .then((items) => {
        if (!cancelled) setIssues(items)
      })
      .catch((cause: unknown) => {
        if (!cancelled) setError(cause)
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [fetchIssues, reloadToken])

  // The URL is the deep-link truth; follow back/forward navigation changes.
  useEffect(() => {
    setSelectedIssueId(urlIssueId)
  }, [urlIssueId])

  // biome-ignore lint/correctness/useExhaustiveDependencies: detailToken forces a retry
  useEffect(() => {
    if (selectedIssueId === null) {
      setDetail(null)
      setDetailLoading(false)
      setDetailError(null)
      return undefined
    }
    let cancelled = false
    setDetail(null)
    setDetailLoading(true)
    setDetailError(null)
    void getIssue(versionId, selectedIssueId)
      .then((next) => {
        if (!cancelled) setDetail(next)
      })
      .catch((cause: unknown) => {
        if (!cancelled) setDetailError(cause)
      })
      .finally(() => {
        if (!cancelled) setDetailLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [versionId, selectedIssueId, detailToken])

  const writeIssueUrl = useCallback(
    (issueId: string | null) => {
      const params = new URLSearchParams({ tab: "issues" })
      if (issueId !== null) params.set("issue", issueId)
      router.replace(`/versions/${encodeURIComponent(versionId)}?${params.toString()}`, {
        scroll: false,
      })
    },
    [router, versionId],
  )

  const openIssue = useCallback(
    (issueId: string) => {
      setSelectedIssueId(issueId)
      writeIssueUrl(issueId)
    },
    [writeIssueUrl],
  )

  const closeDetail = useCallback(() => {
    setSelectedIssueId(null)
    setMerging(false)
    writeIssueUrl(null)
  }, [writeIssueUrl])

  /** Deep link to the requirements tab on the closed requirement (reopen + retry). */
  const openRequirement = useCallback(
    (requirementId: string) => {
      router.push(
        `/versions/${encodeURIComponent(versionId)}?tab=requirements&requirement=${encodeURIComponent(requirementId)}`,
      )
    },
    [router, versionId],
  )

  /** Refetches the list and (when open) the selected detail after a write. */
  const refresh = useCallback(async () => {
    const items = await fetchIssues()
    setIssues(items)
    if (selectedIssueId !== null) {
      try {
        setDetail(await getIssue(versionId, selectedIssueId))
      } catch {
        // Keep the last detail snapshot; the next refresh retries.
      }
    }
  }, [fetchIssues, selectedIssueId, versionId])

  const selectedIssueIdRef = useRef(selectedIssueId)
  selectedIssueIdRef.current = selectedIssueId

  /** Silent snapshot refetch for SSE events: keeps the current view while loading. */
  const refetchFromEvents = useCallback(async () => {
    try {
      setIssues(await fetchIssues())
    } catch {
      // Keep the last list snapshot; the next event or manual retry refreshes.
    }
    const issueId = selectedIssueIdRef.current
    if (issueId === null) return
    try {
      setDetail(await getIssue(versionId, issueId))
    } catch {
      // Keep the last detail snapshot.
    }
  }, [fetchIssues, versionId])

  const eventRefreshTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const eventRefreshRef = useRef(refetchFromEvents)
  eventRefreshRef.current = refetchFromEvents

  const scheduleEventRefresh = useCallback(() => {
    if (eventRefreshTimer.current !== null) clearTimeout(eventRefreshTimer.current)
    eventRefreshTimer.current = setTimeout(() => {
      eventRefreshTimer.current = null
      void eventRefreshRef.current()
    }, SSE_REFRESH_DEBOUNCE_MS)
  }, [])

  const refreshNow = useCallback(() => {
    if (eventRefreshTimer.current !== null) {
      clearTimeout(eventRefreshTimer.current)
      eventRefreshTimer.current = null
    }
    void eventRefreshRef.current()
  }, [])

  useEffect(
    () => () => {
      if (eventRefreshTimer.current !== null) clearTimeout(eventRefreshTimer.current)
    },
    [],
  )

  // One subscription per rendered tab; events only schedule a snapshot refetch.
  useVersionEvents(versionId, {
    onIssueChanged: scheduleEventRefresh,
    onStreamReset: refreshNow,
  })

  const handleTransition = useCallback(
    async (action: IssueTransitionAction, reason: string): Promise<void> => {
      if (selectedIssueId === null || detail === null) return
      const expectedRevision = detail.expected_revision
      const input = { reason, expected_revision: expectedRevision }
      if (action === "confirm") {
        await confirmIssue(versionId, selectedIssueId, input)
      } else if (action === "dismiss") {
        await dismissIssue(versionId, selectedIssueId, input)
      } else {
        await reopenIssue(versionId, selectedIssueId, input)
      }
      await refresh()
    },
    [detail, refresh, selectedIssueId, versionId],
  )

  /**
   * Starts a retest with an empty selection (the server retests every linked
   * source run). The response `case_run_ids` are the new retest runs and are
   * the only IDs used for the deep links below.
   */
  const startRetest = useCallback(
    async (issueId: string): Promise<void> => {
      setRetestError(null)
      setRetestingIds((current) => new Set(current).add(issueId))
      try {
        const result = await retestIssue(versionId, issueId)
        setRetestNotice(result)
        await refresh()
      } catch (cause) {
        setRetestError(cause)
        throw cause
      } finally {
        setRetestingIds((current) => {
          const next = new Set(current)
          next.delete(issueId)
          return next
        })
      }
    },
    [refresh, versionId],
  )

  const handleRetest = useCallback(async (): Promise<void> => {
    if (selectedIssueId === null) return
    await startRetest(selectedIssueId)
  }, [selectedIssueId, startRetest])

  const sortedIssues = useMemo(() => [...issues].sort(compareIssues), [issues])
  const closedRetestRequirement = closedRequirementId(retestError)

  const sutOptions = useMemo(() => {
    const values = new Set<string>()
    for (const issue of issues) {
      for (const ref of issue.refs) {
        if (typeof ref.version_sut_id === "string" && ref.version_sut_id.length > 0) {
          values.add(ref.version_sut_id)
        }
      }
    }
    if (filters.versionSutId !== "all") values.add(filters.versionSutId)
    return Array.from(values).sort()
  }, [issues, filters.versionSutId])

  const filtered = hasActiveFilters(filters)

  const columns: TableColumn<IssueView>[] = [
    {
      key: "title",
      header: "标题",
      render: (issue) => (
        <button
          type="button"
          data-testid={`issue-${issue.id}`}
          onClick={() => openIssue(issue.id)}
          className="max-w-md text-left text-xs font-medium text-foreground underline-offset-2 hover:underline"
        >
          {issue.title}
        </button>
      ),
    },
    {
      key: "status",
      header: "状态",
      render: (issue) => (
        <Badge tone={STATUS_TONE[issue.status]}>{ISSUE_STATUS_LABELS[issue.status]}</Badge>
      ),
    },
    {
      key: "retest",
      header: "复测",
      render: (issue) => {
        const retestStatus = retestStatusOf(issue)
        return (
          <span
            data-testid={`issue-retest-${issue.id}`}
            className="inline-flex items-center gap-1"
            title={
              issue.last_retest_wave_id != null
                ? `最近复测波次 #${issue.last_retest_wave_id}`
                : "尚未复测"
            }
          >
            <Badge tone={ISSUE_RETEST_STATUS_TONES[retestStatus]}>
              {ISSUE_RETEST_STATUS_LABELS[retestStatus]}
            </Badge>
            <span className="text-[10px] text-muted-foreground">{retestStatus}</span>
          </span>
        )
      },
    },
    {
      key: "category",
      header: "类别",
      render: (issue) => (
        <span className="text-xs text-foreground">{ISSUE_CATEGORY_LABELS[issue.category]}</span>
      ),
    },
    {
      key: "severity",
      header: "严重度",
      render: (issue) => (
        <span className="text-xs text-foreground">{ISSUE_SEVERITY_LABELS[issue.severity]}</span>
      ),
    },
    {
      key: "sut",
      header: "SUT",
      render: (issue) => <span className="text-xs text-muted-foreground">{sutLabel(issue)}</span>,
    },
    {
      key: "refs",
      header: "引用",
      render: (issue) => (
        <span className="text-xs text-muted-foreground">{issue.refs.length} 个引用</span>
      ),
    },
    {
      key: "reopen",
      header: "重开",
      render: (issue) => (
        <span className="text-xs text-muted-foreground">{issue.reopen_count}</span>
      ),
    },
    {
      key: "recurrence",
      header: "复发",
      render: (issue) => (
        <span
          className="text-xs text-muted-foreground"
          title={
            issue.last_recurred_at != null
              ? `最近复发 ${formatDateTime(issue.last_recurred_at)}`
              : undefined
          }
        >
          {issue.recurrence_count ?? 0}
          {issue.last_recurred_at != null ? (
            <span className="ml-1 text-[10px]">{formatDateTime(issue.last_recurred_at)}</span>
          ) : null}
        </span>
      ),
    },
    {
      key: "updated",
      header: "更新时间",
      render: (issue) => (
        <span className="text-xs text-muted-foreground">{formatDateTime(issue.updated_at)}</span>
      ),
    },
    {
      key: "actions",
      header: "操作",
      render: (issue) =>
        canRetest(issue) ? (
          <Button
            variant="secondary"
            size="sm"
            disabled={retestingIds.has(issue.id)}
            onClick={() => {
              void startRetest(issue.id).catch(() => {})
            }}
          >
            复测
          </Button>
        ) : (
          <span className="text-[11px] text-muted-foreground">—</span>
        ),
    },
  ]

  function updateFilter<Key extends keyof IssueFilters>(key: Key, value: IssueFilters[Key]): void {
    setFilters((current) => ({ ...current, [key]: value }))
  }

  return (
    <div data-testid="issues-tab" className="space-y-3">
      <section aria-label="问题筛选" className="flex flex-wrap items-end gap-3">
        <Field id="issue-filter-status" label="状态" className="w-36">
          <Select
            id="issue-filter-status"
            value={filters.status}
            onChange={(event) =>
              updateFilter("status", event.target.value as IssueFilters["status"])
            }
          >
            <option value="all">全部状态</option>
            {(Object.keys(ISSUE_STATUS_LABELS) as IssueStatus[]).map((status) => (
              <option key={status} value={status}>
                {ISSUE_STATUS_LABELS[status]}
              </option>
            ))}
          </Select>
        </Field>
        <Field id="issue-filter-category" label="类别" className="w-36">
          <Select
            id="issue-filter-category"
            value={filters.category}
            onChange={(event) =>
              updateFilter("category", event.target.value as IssueFilters["category"])
            }
          >
            <option value="all">全部类别</option>
            {(Object.keys(ISSUE_CATEGORY_LABELS) as IssueCategory[]).map((category) => (
              <option key={category} value={category}>
                {ISSUE_CATEGORY_LABELS[category]}
              </option>
            ))}
          </Select>
        </Field>
        <Field id="issue-filter-severity" label="严重度" className="w-36">
          <Select
            id="issue-filter-severity"
            value={filters.severity}
            onChange={(event) =>
              updateFilter("severity", event.target.value as IssueFilters["severity"])
            }
          >
            <option value="all">全部严重度</option>
            {(Object.keys(ISSUE_SEVERITY_LABELS) as IssueSeverity[]).map((severity) => (
              <option key={severity} value={severity}>
                {ISSUE_SEVERITY_LABELS[severity]}
              </option>
            ))}
          </Select>
        </Field>
        <Field id="issue-filter-sut" label="SUT" className="w-40">
          <Select
            id="issue-filter-sut"
            value={filters.versionSutId}
            onChange={(event) => updateFilter("versionSutId", event.target.value)}
          >
            <option value="all">全部 SUT</option>
            {sutOptions.map((sutId) => (
              <option key={sutId} value={sutId}>
                SUT {sutId}
              </option>
            ))}
          </Select>
        </Field>
      </section>

      {retestError !== null ? (
        <div className="space-y-1">
          <InlineError
            error={retestError}
            message={
              closedRetestRequirement !== null ? requirementClosedMessage("retest") : undefined
            }
          >
            <span className="ml-2 text-muted-foreground">（复测启动失败）</span>
          </InlineError>
          {closedRetestRequirement !== null ? (
            <Button
              size="sm"
              variant="secondary"
              onClick={() => openRequirement(closedRetestRequirement)}
            >
              前往重新打开需求
            </Button>
          ) : null}
        </div>
      ) : null}

      {retestNotice !== null ? (
        <div
          data-testid="issue-retest-notice"
          role="status"
          className="rounded-lg border border-info/30 bg-info/10 px-3 py-2 text-xs text-foreground"
        >
          <p className="font-medium">
            问题 #{retestNotice.issue_id} 已启动复测：波次 #{retestNotice.wave_id}（
            {ISSUE_RETEST_STATUS_LABELS[retestNotice.retest_status]}）
          </p>
          <p className="mt-1 flex flex-wrap items-center gap-2 text-[11px]">
            <span className="text-muted-foreground">新复测运行：</span>
            {retestNotice.case_run_ids.length === 0 ? (
              <span className="text-muted-foreground">等待执行器创建运行</span>
            ) : (
              retestNotice.case_run_ids.map((caseRunId) => (
                <Link
                  key={caseRunId}
                  href={retestRunHref(versionId, retestNotice.wave_id, caseRunId)}
                  className="text-primary underline-offset-2 hover:underline"
                >
                  运行 #{caseRunId}
                </Link>
              ))
            )}
          </p>
        </div>
      ) : null}

      {loading ? (
        <LoadingCard label="正在加载问题…" />
      ) : error !== null ? (
        <ErrorState
          error={error}
          title="问题列表加载失败"
          onRetry={() => setReloadToken((token) => token + 1)}
        />
      ) : sortedIssues.length === 0 ? (
        <EmptyState
          title={filtered ? "没有符合筛选条件的问题" : "暂无问题"}
          description={filtered ? "调整筛选条件后重试。" : "执行失败后会自动生成问题候选。"}
        />
      ) : (
        <Table
          ariaLabel="问题列表"
          columns={columns}
          rows={sortedIssues}
          rowKey={(issue) => issue.id}
        />
      )}

      {selectedIssueId !== null ? (
        <IssueDetailSheet
          versionId={versionId}
          issue={detail}
          loading={detailLoading}
          error={detailError}
          onClose={closeDetail}
          onRetry={() => setDetailToken((token) => token + 1)}
          onTransition={handleTransition}
          onMerge={() => setMerging(true)}
          onRetest={handleRetest}
          onOpenRequirement={openRequirement}
        />
      ) : null}

      <MergeIssueDialog
        open={merging && detail !== null}
        versionId={versionId}
        source={detail}
        onClose={() => setMerging(false)}
        onMerged={() => {
          void refresh()
        }}
      />
    </div>
  )
}
