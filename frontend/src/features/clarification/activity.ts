/**
 * Statuses that mean an agent run is doing something the user can watch. The
 * backend only publishes `agent.activity` frames for a live run, but the frame
 * shape is not pinned by the contract and older/unknown statuses exist; a
 * status outside this list must stay idle instead of showing the generic
 * "AI 正在处理…" placeholder forever (D-06).
 */
const ACTIVE_STATUSES: ReadonlySet<string> = new Set(["thinking", "tool", "started", "running"])

const DEFAULT_ACTIVITY_TEXT = "AI 正在处理…"

function nonBlankString(value: unknown): string | null {
  if (typeof value !== "string") return null
  const trimmed = value.trim()
  return trimmed === "" ? null : value
}

/**
 * Chinese activity line of one `agent.activity` frame, or null when the frame
 * carries no live activity.
 *
 * Only a known active status is rendered: the previous implementation fell back
 * to "AI 正在处理…" for every non-terminal status, so an unknown status (or a
 * run whose only frames predate the terminal event) left a permanent fake
 * activity bubble. An explicit `activity`/`message` is passed through so the
 * host keeps ownership of the wording; a known active status without text gets
 * the generic placeholder.
 */
export function activityText(payload: Record<string, unknown>): string | null {
  const status = typeof payload.status === "string" ? payload.status.trim().toLowerCase() : ""
  if (!ACTIVE_STATUSES.has(status)) return null
  return (
    nonBlankString(payload.activity) ?? nonBlankString(payload.message) ?? DEFAULT_ACTIVITY_TEXT
  )
}
