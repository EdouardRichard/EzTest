"use client"

import { MessageSquare } from "lucide-react"
import { useEffect, useMemo, useRef, useState } from "react"

import { EmptyState } from "@/components/ui/EmptyState"
import { StatusBadge } from "@/components/ui/StatusBadge"
import { Transcript } from "@/features/agent-console/Transcript"
import type { VersionEvent } from "@/features/versions/types"
import { useVersionStream } from "@/features/versions/useVersionStream"
import type { SSEStatus } from "@/lib/sse-client"

import { listExecutionSessions } from "../api"
import { describeExecutionError } from "../execution-errors"
import { useCaseRuns, useExecution } from "../hooks/useExecution"
import type { CaseRun, ExecutionSession, PendingTakeover } from "../types"

import { HumanGateCard } from "./HumanGateCard"
import {
  ACTIVITY_TYPE_FILTERS,
  type ActivityTypeFilter,
  activitiesForSession,
  activitySearchText,
  activityTypeKey,
  collectEvidenceIds,
  eventSessionId,
  groupSessions,
  isErrorActivity,
  maxEventSeq,
  type SessionGroupBy,
  sessionAgentKey,
  sessionCaseKey,
  sessionSearchText,
  sessionStatusMeta,
} from "./session-adapter"

export type SessionsTabProps = {
  /** P3-owned version ID; every session read is version scoped. */
  versionId: string
  /** Current user role; forwarded to the human-gate card (spec §5.11). */
  role?: string
  /** Opens T22's evidence viewer through the parent container. */
  onOpenEvidence?: (evidenceId: string) => void
  /** Opens the existing resources takeover dialog through the parent. */
  onCompleteTakeover?: (takeover: PendingTakeover) => void
}

function isAbortError(error: unknown): boolean {
  return error instanceof Error && error.name === "AbortError"
}

function uniqueSorted(values: string[]): string[] {
  return [...new Set(values)].sort((left, right) => left.localeCompare(right, "zh-CN"))
}

function streamStatusMeta(status: SSEStatus): {
  label: string
  variant: "success" | "info" | "warning" | "danger"
} {
  switch (status) {
    case "live":
      return { label: "实时", variant: "success" }
    case "connecting":
      return { label: "连接中", variant: "info" }
    case "reconnecting":
      return { label: "重连中", variant: "warning" }
    case "closed":
      return { label: "连接已断开", variant: "danger" }
  }
}

/** Status-aware copy used before any session exists / while the stream is down. */
function isStreamPending(status: SSEStatus): boolean {
  return status === "connecting" || status === "reconnecting"
}

function idlePanelMessage(status: SSEStatus, hasSessions: boolean): string {
  if (isStreamPending(status)) return "正在连接执行会话…"
  if (status === "closed") return "连接已断开"
  return hasSessions ? "没有匹配的会话" : "执行会话将在批次开始后出现。"
}

type FilteredActivityOptions = {
  activities: ReturnType<typeof activitiesForSession>
  typeFilter: ActivityTypeFilter
  errorsOnly: boolean
  searchTerm: string
  metadataMatchesSearch: boolean
}

function filterActivities({
  activities,
  typeFilter,
  errorsOnly,
  searchTerm,
  metadataMatchesSearch,
}: FilteredActivityOptions): ReturnType<typeof activitiesForSession> {
  return activities.filter((activity) => {
    if (typeFilter !== "" && activityTypeKey(activity) !== typeFilter) return false
    if (errorsOnly && !isErrorActivity(activity)) return false
    if (searchTerm !== "" && !metadataMatchesSearch) {
      if (!activitySearchText(activity).includes(searchTerm)) return false
    }
    return true
  })
}

export type SessionsTabContentProps = {
  sessions: ExecutionSession[]
  caseRuns?: CaseRun[]
  /** The version stream snapshot/live events (spec §5.10 `agent.activity`). */
  events: VersionEvent[]
  streamStatus: SSEStatus
  loading?: boolean
  error?: string
  role?: string
  onOpenEvidence?: (evidenceId: string) => void
  onCompleteTakeover?: (takeover: PendingTakeover) => void
}

/**
 * Pure Sessions-tab rendering: grouped session list on the left, the reused
 * agent-console transcript on the right, filter/search controls, the live
 * stream status and human-takeover cards. Exported so tests can drive it with
 * a fixed stream snapshot; `SessionsTab` wires the hooks and the version API.
 */
export function SessionsTabContent({
  sessions,
  caseRuns = [],
  events,
  streamStatus,
  loading = false,
  error = "",
  role,
  onOpenEvidence,
  onCompleteTakeover,
}: SessionsTabContentProps) {
  const [groupBy, setGroupBy] = useState<SessionGroupBy>("agent")
  const [agentFilter, setAgentFilter] = useState("")
  const [caseFilter, setCaseFilter] = useState("")
  const [typeFilter, setTypeFilter] = useState<ActivityTypeFilter>("")
  const [errorsOnly, setErrorsOnly] = useState(false)
  const [search, setSearch] = useState("")
  const [selectedId, setSelectedId] = useState("")
  const [unread, setUnread] = useState<Record<string, boolean>>({})

  const searchTerm = search.trim().toLowerCase()
  const agentOptions = useMemo(
    () => uniqueSorted(sessions.map((session) => sessionAgentKey(session))),
    [sessions],
  )
  const caseOptions = useMemo(
    () => uniqueSorted(sessions.map((session) => sessionCaseKey(session))),
    [sessions],
  )

  const filteredSessions = useMemo(() => {
    return sessions.filter((session) => {
      if (agentFilter !== "" && sessionAgentKey(session) !== agentFilter) return false
      if (caseFilter !== "" && sessionCaseKey(session) !== caseFilter) return false
      if (searchTerm === "") return true
      if (sessionSearchText(session).includes(searchTerm)) return true
      return activitiesForSession(events, session.id).some((activity) =>
        activitySearchText(activity).includes(searchTerm),
      )
    })
  }, [sessions, events, agentFilter, caseFilter, searchTerm])

  const groups = useMemo(
    () => groupSessions(filteredSessions, groupBy),
    [filteredSessions, groupBy],
  )

  // The selection stays a user choice; a filter/session change falls back to
  // the first visible session without briefly rendering a stale transcript.
  const selectedSession = useMemo(
    () =>
      filteredSessions.find((session) => session.id === selectedId) ?? filteredSessions[0] ?? null,
    [filteredSessions, selectedId],
  )
  const selectedSessionId = selectedSession?.id ?? ""

  useEffect(() => {
    if (selectedSession !== null && selectedSession.id !== selectedId) {
      setSelectedId(selectedSession.id)
    }
  }, [selectedSession, selectedId])

  // Mark sessions that receive activities while another one is selected, so
  // the list can pulse an unread indicator (spec §9.3). The first snapshot is
  // the baseline and never marks everything unread.
  const streamHeadRef = useRef(0)
  useEffect(() => {
    const head = maxEventSeq(events)
    const previous = streamHeadRef.current
    if (previous === 0) {
      streamHeadRef.current = head
      return
    }
    if (head <= previous) return
    streamHeadRef.current = head

    const nextUnread: string[] = []
    for (const event of events) {
      if (event.seq <= previous) continue
      const sessionId = eventSessionId(event)
      if (sessionId === null || sessionId === selectedSessionId) continue
      nextUnread.push(sessionId)
    }
    if (nextUnread.length === 0) return
    setUnread((current) => {
      const next = { ...current }
      for (const sessionId of nextUnread) next[sessionId] = true
      return next
    })
  }, [events, selectedSessionId])

  const selectedActivities = useMemo(
    () => activitiesForSession(events, selectedSessionId),
    [events, selectedSessionId],
  )
  const metadataMatchesSearch =
    searchTerm === "" ||
    (selectedSession !== null && sessionSearchText(selectedSession).includes(searchTerm))
  const visibleActivities = useMemo(
    () =>
      filterActivities({
        activities: selectedActivities,
        typeFilter,
        errorsOnly,
        searchTerm,
        metadataMatchesSearch,
      }),
    [selectedActivities, typeFilter, errorsOnly, searchTerm, metadataMatchesSearch],
  )

  const evidenceIds = useMemo(
    () => collectEvidenceIds(events, selectedSessionId),
    [events, selectedSessionId],
  )
  const pendingTakeovers = useMemo(
    () =>
      caseRuns.filter(
        (caseRun): caseRun is CaseRun & { pending_takeover: PendingTakeover } =>
          caseRun.pending_takeover !== null,
      ),
    [caseRuns],
  )

  const stream = streamStatusMeta(streamStatus)
  const selectedStatus = selectedSession === null ? null : sessionStatusMeta(selectedSession)

  function selectSession(sessionId: string): void {
    setSelectedId(sessionId)
    setUnread((current) => {
      if (!current[sessionId]) return current
      const next = { ...current }
      delete next[sessionId]
      return next
    })
  }

  function clearFilters(): void {
    setAgentFilter("")
    setCaseFilter("")
    setTypeFilter("")
    setErrorsOnly(false)
    setSearch("")
  }

  const hasActiveFilter =
    agentFilter !== "" || caseFilter !== "" || typeFilter !== "" || errorsOnly || searchTerm !== ""

  return (
    <div data-testid="sessions-tab" className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <span data-testid="stream-status" role="status" aria-live="polite">
          <StatusBadge variant={stream.variant}>{stream.label}</StatusBadge>
        </span>
        <span className="text-xs text-muted-foreground">
          共 {sessions.length} 个执行会话
          {filteredSessions.length === sessions.length
            ? ""
            : ` · 已筛选 ${filteredSessions.length} 个`}
        </span>
        {hasActiveFilter ? (
          <button
            type="button"
            onClick={clearFilters}
            className="ml-auto rounded-lg border border-border bg-card px-2 py-1 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            清除筛选
          </button>
        ) : null}
      </div>

      {error !== "" ? (
        <p
          role="alert"
          className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
        >
          加载执行会话失败：{error}
        </p>
      ) : null}

      <div className="grid gap-4 lg:grid-cols-[minmax(15rem,21rem)_minmax(0,1fr)]">
        <aside aria-label="执行会话列表" className="space-y-3">
          <div className="space-y-2 rounded-xl border border-border bg-card/60 p-2">
            <div className="grid grid-cols-2 gap-2">
              <select
                data-testid="session-filter-agent"
                aria-label="按 Agent 过滤"
                value={agentFilter}
                onChange={(event) => setAgentFilter(event.target.value)}
                className="w-full rounded-lg border border-border bg-input px-2 py-1.5 text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                <option value="">全部 Agent</option>
                {agentOptions.map((agent) => (
                  <option key={agent} value={agent}>
                    {agent}
                  </option>
                ))}
              </select>
              <select
                data-testid="session-filter-case"
                aria-label="按用例过滤"
                value={caseFilter}
                onChange={(event) => setCaseFilter(event.target.value)}
                className="w-full rounded-lg border border-border bg-input px-2 py-1.5 text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                <option value="">全部用例</option>
                {caseOptions.map((currentCase) => (
                  <option key={currentCase} value={currentCase}>
                    {currentCase}
                  </option>
                ))}
              </select>
              <select
                data-testid="session-filter-kind"
                aria-label="按活动类型过滤"
                value={typeFilter}
                onChange={(event) => setTypeFilter(event.target.value as ActivityTypeFilter)}
                className="w-full rounded-lg border border-border bg-input px-2 py-1.5 text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                {ACTIVITY_TYPE_FILTERS.map((option) => (
                  <option key={option.value} value={option.value}>
                    {option.label}
                  </option>
                ))}
              </select>
              <select
                data-testid="session-group-by"
                aria-label="会话分组方式"
                value={groupBy}
                onChange={(event) => setGroupBy(event.target.value as SessionGroupBy)}
                className="w-full rounded-lg border border-border bg-input px-2 py-1.5 text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                <option value="agent">按 Agent 分组</option>
                <option value="case">按用例分组</option>
              </select>
            </div>
            <div className="flex items-center gap-2">
              <input
                data-testid="session-search"
                aria-label="搜索会话"
                type="search"
                value={search}
                onChange={(event) => setSearch(event.target.value)}
                placeholder="搜索会话或活动"
                className="min-w-0 flex-1 rounded-lg border border-border bg-input px-2 py-1.5 text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring"
              />
              <label className="flex shrink-0 items-center gap-1 text-xs text-muted-foreground">
                <input
                  data-testid="session-filter-errors"
                  type="checkbox"
                  checked={errorsOnly}
                  onChange={(event) => setErrorsOnly(event.target.checked)}
                />
                仅异常
              </label>
            </div>
          </div>

          {loading ? (
            <p className="rounded-xl border border-dashed border-border px-3 py-6 text-center text-xs text-muted-foreground">
              正在加载执行会话…
            </p>
          ) : sessions.length === 0 ? (
            isStreamPending(streamStatus) ? (
              <p className="rounded-xl border border-dashed border-border px-3 py-6 text-center text-xs text-muted-foreground">
                正在连接执行会话…
              </p>
            ) : streamStatus === "closed" ? (
              <p className="rounded-xl border border-dashed border-border px-3 py-6 text-center text-xs text-muted-foreground">
                连接已断开，暂时无法确认执行会话。
              </p>
            ) : (
              <EmptyState
                icon={MessageSquare}
                title="暂无执行会话"
                description="批次开始后，执行 Agent 的会话会出现在这里。"
              />
            )
          ) : filteredSessions.length === 0 ? (
            <p className="rounded-xl border border-dashed border-border px-3 py-6 text-center text-xs text-muted-foreground">
              没有匹配的会话
            </p>
          ) : (
            <div className="space-y-3">
              {groups.map((group) => (
                <section key={group.key} aria-label={`会话分组 ${group.label}`}>
                  <h3
                    aria-label={group.label}
                    className="flex items-center justify-between gap-2 px-1 pb-1 text-[11px] font-semibold uppercase tracking-wide text-muted-foreground"
                  >
                    <span className="truncate">{group.label}</span>
                    <span>{group.sessions.length}</span>
                  </h3>
                  <div className="space-y-1.5">
                    {group.sessions.map((session) => {
                      const status = sessionStatusMeta(session)
                      const selected = session.id === selectedSessionId
                      return (
                        <button
                          key={session.id}
                          type="button"
                          data-testid={`session-row-${session.id}`}
                          aria-pressed={selected}
                          onClick={() => selectSession(session.id)}
                          className={
                            selected
                              ? "w-full rounded-xl border border-primary/40 bg-primary/10 px-3 py-2 text-left transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                              : "w-full rounded-xl border border-border bg-card px-3 py-2 text-left transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                          }
                        >
                          <span className="flex items-center gap-2">
                            <span className="min-w-0 flex-1 truncate text-sm font-medium">
                              {session.title || "执行会话"}
                            </span>
                            {unread[session.id] ? (
                              <span
                                data-testid={`session-unread-${session.id}`}
                                role="img"
                                aria-label="有新活动"
                                className="h-1.5 w-1.5 shrink-0 rounded-full bg-sky-500"
                              />
                            ) : null}
                            <StatusBadge variant={status.variant}>{status.label}</StatusBadge>
                          </span>
                          <span className="mt-1 block truncate text-xs text-muted-foreground">
                            当前用例：{sessionCaseKey(session)}
                          </span>
                        </button>
                      )
                    })}
                  </div>
                </section>
              ))}
            </div>
          )}
        </aside>

        <section
          aria-label="执行会话转录"
          className="min-w-0 space-y-3 rounded-xl border border-border bg-card/40 p-3"
        >
          {pendingTakeovers.map((caseRun) => (
            <HumanGateCard
              key={caseRun.pending_takeover.id}
              takeover={caseRun.pending_takeover}
              caseTitle={caseRun.title}
              role={role}
              onComplete={onCompleteTakeover}
            />
          ))}

          {selectedSession === null ? (
            <div data-testid="execution-transcript">
              <p className="rounded-xl border border-dashed border-border bg-card/60 px-4 py-10 text-center text-sm text-muted-foreground">
                {idlePanelMessage(streamStatus, sessions.length > 0)}
              </p>
            </div>
          ) : (
            <>
              <div className="flex flex-wrap items-center gap-2">
                <h2 className="min-w-0 flex-1 truncate text-sm font-semibold">
                  {selectedSession.title || "执行会话"}
                </h2>
                {selectedStatus === null ? null : (
                  <StatusBadge variant={selectedStatus.variant}>{selectedStatus.label}</StatusBadge>
                )}
                <span className="text-xs text-muted-foreground">
                  当前用例：{sessionCaseKey(selectedSession)}
                </span>
              </div>

              {evidenceIds.length > 0 ? (
                <div className="flex flex-wrap items-center gap-1.5">
                  <span className="text-[11px] font-medium text-muted-foreground">证据</span>
                  {evidenceIds.map((evidenceId) => (
                    <button
                      key={evidenceId}
                      type="button"
                      data-testid={`session-evidence-${evidenceId}`}
                      onClick={() => onOpenEvidence?.(evidenceId)}
                      className="rounded-lg border border-border bg-card px-2 py-1 font-mono text-[11px] text-muted-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                    >
                      证据 {evidenceId}
                    </button>
                  ))}
                </div>
              ) : null}

              <div data-testid="execution-transcript">
                <Transcript activities={visibleActivities} status={streamStatus} />
              </div>
            </>
          )}
        </section>
      </div>
    </div>
  )
}

/**
 * Connected Sessions tab: loads this version's execution sessions, shares the
 * version stream, reads the active batch's case runs for pending takeovers and
 * forwards evidence/takeover actions to the container.
 */
export function SessionsTab({
  versionId,
  role,
  onOpenEvidence,
  onCompleteTakeover,
}: SessionsTabProps) {
  const execution = useExecution(versionId)
  const stream = useVersionStream(versionId)
  const activeBatchId = execution.activeBatch?.id ?? ""
  const caseRuns = useCaseRuns(versionId, activeBatchId, {}, stream)

  const [sessions, setSessions] = useState<ExecutionSession[]>([])
  const [sessionsLoading, setSessionsLoading] = useState(versionId !== "")
  const [sessionsError, setSessionsError] = useState("")
  const requestRef = useRef(0)

  // A new active batch can create new execution sessions; the ID is a reload
  // trigger, not a fetch input.
  // biome-ignore lint/correctness/useExhaustiveDependencies: activeBatchId restarts the read
  useEffect(() => {
    requestRef.current += 1
    const request = requestRef.current

    if (versionId === "") {
      setSessions([])
      setSessionsLoading(false)
      setSessionsError("")
      return
    }

    const controller = new AbortController()
    setSessionsLoading(true)
    listExecutionSessions(versionId, {}, { signal: controller.signal })
      .then((page) => {
        if (requestRef.current !== request) return
        setSessions(page.items)
        setSessionsError("")
      })
      .catch((caught: unknown) => {
        if (requestRef.current !== request || isAbortError(caught)) return
        setSessionsError(describeExecutionError(caught))
      })
      .finally(() => {
        if (requestRef.current === request) setSessionsLoading(false)
      })

    return () => {
      requestRef.current += 1
      controller.abort()
    }
  }, [versionId, activeBatchId])

  return (
    <SessionsTabContent
      sessions={sessions}
      caseRuns={caseRuns.caseRuns}
      events={stream.events}
      streamStatus={stream.status}
      loading={sessionsLoading}
      error={sessionsError || caseRuns.error}
      role={role}
      onOpenEvidence={onOpenEvidence}
      onCompleteTakeover={onCompleteTakeover}
    />
  )
}
