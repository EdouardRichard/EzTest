import type { StatusBadgeVariant } from "@/components/ui/StatusBadge"
import type { AgentEvent } from "@/features/agent-console/agent-api"
import type { VersionEvent } from "@/features/versions/types"

import type { ExecutionSession } from "../types"

/** The P1 activity discriminator carried on the version stream (spec §5.10). */
const ACTIVITY_EVENT_TYPE = "agent.activity"

/** Filters offered by the Sessions tab for the activity type column. */
export const ACTIVITY_TYPE_FILTERS = [
  { value: "", label: "全部类型" },
  { value: "text", label: "文本" },
  { value: "thinking", label: "思考" },
  { value: "tool", label: "工具" },
  { value: "decision", label: "决策" },
  { value: "run", label: "运行" },
  { value: "notice", label: "通知" },
] as const

export type ActivityTypeFilter = (typeof ACTIVITY_TYPE_FILTERS)[number]["value"]

/** How the left session list is grouped (spec §9.3). */
export type SessionGroupBy = "agent" | "case"

export type SessionGroup = {
  key: string
  label: string
  sessions: ExecutionSession[]
}

export type SessionStatusMeta = {
  key: "running" | "waiting" | "failed" | "done" | "unknown"
  label: string
  variant: StatusBadgeVariant
}

const RUNNING_STATUSES = new Set(["running", "active", "in_progress", "in-progress"])
const WAITING_STATUSES = new Set([
  "waiting",
  "needs_manual",
  "needs-manual",
  "pending",
  "blocked",
  "paused",
  "takeover",
  "requested",
])
const FAILED_STATUSES = new Set(["failed", "error", "invalid", "expired", "aborted"])
const DONE_STATUSES = new Set(["done", "completed", "closed", "idle", "finished"])

function trimmed(value: string | null | undefined): string {
  return typeof value === "string" ? value.trim() : ""
}

/**
 * The P1 session id of one version event. The execution projection normally
 * puts it in the envelope, but a raw `agent.activity` detail may repeat it.
 */
export function eventSessionId(event: VersionEvent): string | null {
  const envelope = trimmed(event.session_id)
  if (envelope !== "") return envelope
  const detail = event.detail.session_id
  if (typeof detail === "string" && detail !== "") return detail
  if (typeof detail === "number" && Number.isFinite(detail)) return String(detail)
  return null
}

/**
 * Maps a P1 `agent.activity` version event onto the transcript's `AgentEvent`.
 * Execution IDs stay strings; only the P1 message/part ordinals are normalized
 * from a decimal string to a safe integer so lazy detail loading keeps working.
 */
export function toAgentEvent(event: VersionEvent): AgentEvent | null {
  if (event.type !== ACTIVITY_EVENT_TYPE) return null
  const detail: Record<string, unknown> = { ...event.detail }
  for (const key of ["message_id", "part"]) {
    const value = detail[key]
    if (typeof value !== "string" || value.trim() === "") continue
    const numeric = Number(value)
    if (Number.isSafeInteger(numeric)) detail[key] = numeric
  }
  return {
    seq: event.seq,
    channel: event.channel,
    type: ACTIVITY_EVENT_TYPE,
    ts: event.ts,
    summary: event.summary,
    agent_role: event.agent_role,
    session_id: eventSessionId(event),
    detail,
  }
}

/** One session's activities in seq order, mapped for the transcript. */
export function activitiesForSession(events: VersionEvent[], sessionId: string): AgentEvent[] {
  if (sessionId === "") return []
  const activities: AgentEvent[] = []
  for (const event of events) {
    if (eventSessionId(event) !== sessionId) continue
    const activity = toAgentEvent(event)
    if (activity) activities.push(activity)
  }
  return activities.sort((left, right) => left.seq - right.seq)
}

/** Highest envelope seq of the channel; used to detect newly arrived activities. */
export function maxEventSeq(events: VersionEvent[]): number {
  let max = 0
  for (const event of events) {
    if (event.seq > max) max = event.seq
  }
  return max
}

function pushEvidence(target: string[], value: unknown): void {
  if (typeof value === "string" && value !== "") {
    target.push(value)
    return
  }
  if (!Array.isArray(value)) return
  for (const item of value) {
    if (typeof item === "string" && item !== "") target.push(item)
  }
}

/**
 * Evidence IDs referenced by one session's activities, in first-seen order.
 * The Sessions tab hands them to the evidence callback instead of importing
 * T22's viewer (see the W6 integration ruling).
 */
export function collectEvidenceIds(events: VersionEvent[], sessionId: string): string[] {
  if (sessionId === "") return []
  const seen = new Set<string>()
  const ids: string[] = []
  for (const event of events) {
    if (eventSessionId(event) !== sessionId) continue
    const found: string[] = []
    for (const evidence of event.refs.evidence) found.push(evidence)
    pushEvidence(found, event.detail.evidence_ids)
    pushEvidence(found, event.detail.evidence_id)
    const detailRefs = event.detail.refs
    if (typeof detailRefs === "object" && detailRefs !== null && !Array.isArray(detailRefs)) {
      pushEvidence(found, (detailRefs as Record<string, unknown>).evidence)
    }
    for (const id of found) {
      if (seen.has(id)) continue
      seen.add(id)
      ids.push(id)
    }
  }
  return ids
}

/** Agent label of one execution session; never empty. */
export function sessionAgentKey(session: ExecutionSession): string {
  return trimmed(session.agent_role) || trimmed(session.profile_key) || "执行 Agent"
}

/** Current-case label of one execution session; never empty. */
export function sessionCaseKey(session: ExecutionSession): string {
  return trimmed(session.case_title) || trimmed(session.case_node_id) || "未关联用例"
}

/** Status pill text/variant, including the running and waiting-human states. */
export function sessionStatusMeta(session: ExecutionSession): SessionStatusMeta {
  const status = trimmed(session.status).toLowerCase()
  if (session.running || RUNNING_STATUSES.has(status)) {
    return { key: "running", label: "进行中", variant: "info" }
  }
  if (WAITING_STATUSES.has(status)) {
    return { key: "waiting", label: "等待人工", variant: "warning" }
  }
  if (FAILED_STATUSES.has(status)) {
    return { key: "failed", label: "异常", variant: "danger" }
  }
  if (DONE_STATUSES.has(status)) {
    return { key: "done", label: status === "idle" ? "空闲" : "已结束", variant: "success" }
  }
  return { key: "unknown", label: status === "" ? "空闲" : session.status, variant: "neutral" }
}

/** Groups sessions by agent role or by current case, preserving input order. */
export function groupSessions(
  sessions: ExecutionSession[],
  groupBy: SessionGroupBy,
): SessionGroup[] {
  const groups = new Map<string, SessionGroup>()
  for (const session of sessions) {
    const label = groupBy === "case" ? sessionCaseKey(session) : sessionAgentKey(session)
    const key = `${groupBy}:${label}`
    const existing = groups.get(key)
    if (existing) existing.sessions.push(session)
    else groups.set(key, { key, label, sessions: [session] })
  }
  return [...groups.values()].sort((left, right) => left.label.localeCompare(right.label, "zh-CN"))
}

/** Reads `detail.kind`, the frozen V1 activity discriminator. */
export function activityKindOf(event: AgentEvent): string {
  const kind = event.detail.kind
  return typeof kind === "string" ? kind : ""
}

/** The activity-type filter bucket of one transcript row. */
export function activityTypeKey(event: AgentEvent): Exclude<ActivityTypeFilter, ""> {
  switch (activityKindOf(event)) {
    case "thinking":
      return "thinking"
    case "text":
    case "user_message":
      return "text"
    case "tool_use":
    case "tool_result":
      return "tool"
    case "decision":
      return "decision"
    case "run_started":
    case "run_finished":
    case "usage":
      return "run"
    default:
      return "notice"
  }
}

const ERROR_STATUSES = new Set([
  "error",
  "failed",
  "failure",
  "timeout",
  "aborted",
  "interrupted",
  "cancelled",
  "denied",
])

/** Whether a transcript row represents an exception worth surfacing. */
export function isErrorActivity(event: AgentEvent): boolean {
  if (activityKindOf(event) === "decision") return true
  const status = typeof event.detail.status === "string" ? event.detail.status.toLowerCase() : ""
  if (ERROR_STATUSES.has(status)) return true
  const errorCode = event.detail.error_code
  return typeof errorCode === "string" && errorCode !== ""
}

/** Case-insensitive haystack of one activity, for the search box. */
export function activitySearchText(event: AgentEvent): string {
  let detail = ""
  try {
    detail = JSON.stringify(event.detail)
  } catch {
    detail = ""
  }
  return `${event.summary} ${detail}`.toLowerCase()
}

/** Case-insensitive haystack of one session, for the search box. */
export function sessionSearchText(session: ExecutionSession): string {
  return [
    session.title,
    session.status,
    sessionAgentKey(session),
    sessionCaseKey(session),
    trimmed(session.case_node_id),
  ]
    .join(" ")
    .toLowerCase()
}
