const DASH = "—"
const MINUTE_MS = 60_000
const HOUR_MS = 60 * MINUTE_MS
const DAY_MS = 24 * HOUR_MS

function toDate(value: string | Date | null | undefined): Date | null {
  if (value === null || value === undefined || value === "") return null
  const date = value instanceof Date ? value : new Date(value)
  return Number.isNaN(date.getTime()) ? null : date
}

const pad = (value: number) => String(value).padStart(2, "0")

/** Local `YYYY-MM-DD` for a calendar date. */
export function formatDate(value: string | Date | null | undefined): string {
  const date = toDate(value)
  if (!date) return DASH
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`
}

/** Local `YYYY-MM-DD HH:mm` for a timestamp. */
export function formatDateTime(value: string | Date | null | undefined): string {
  const date = toDate(value)
  if (!date) return DASH
  return `${formatDate(date)} ${pad(date.getHours())}:${pad(date.getMinutes())}`
}

/**
 * Human-readable age for audit/event timestamps. Anything older than 30 days
 * falls back to an absolute date so lists never say "203 天前".
 */
export function formatRelativeTime(
  value: string | Date | null | undefined,
  now: Date = new Date(),
): string {
  const date = toDate(value)
  if (!date) return DASH
  const diff = now.getTime() - date.getTime()
  if (diff < MINUTE_MS) return "刚刚"
  if (diff < HOUR_MS) return `${Math.floor(diff / MINUTE_MS)} 分钟前`
  if (diff < DAY_MS) return `${Math.floor(diff / HOUR_MS)} 小时前`
  if (diff < 30 * DAY_MS) return `${Math.floor(diff / DAY_MS)} 天前`
  return formatDate(date)
}

const BYTE_UNITS = ["B", "KB", "MB", "GB", "TB"] as const

/** Human-readable byte size (binary units, one decimal above bytes). */
export function formatBytes(bytes: number | null | undefined): string {
  if (typeof bytes !== "number" || !Number.isFinite(bytes) || bytes < 0) return DASH
  if (bytes < 1024) return `${Math.round(bytes)} B`
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < BYTE_UNITS.length - 1) {
    value /= 1024
    unit += 1
  }
  return `${value.toFixed(1)} ${BYTE_UNITS[unit]}`
}
