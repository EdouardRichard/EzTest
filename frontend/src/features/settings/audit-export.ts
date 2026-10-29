import type { AuditEntry } from "./audit-types"

const CSV_HEADER = [
  "id",
  "created_at",
  "action",
  "actor_user_id",
  "target_type",
  "target_id",
  "request_id",
  "ip",
  "user_agent",
  "metadata",
]

/**
 * Renders one CSV cell with RFC 4180 quoting. A cell containing a comma, a
 * double quote or a line break is quoted and its quotes are doubled.
 */
function csvCell(value: unknown): string {
  const text =
    value === null || value === undefined
      ? ""
      : typeof value === "string"
        ? value
        : JSON.stringify(value)
  if (/[",\r\n]/.test(text)) return `"${text.replaceAll('"', '""')}"`
  return text
}

/**
 * Builds the CSV export of the currently loaded audit page. It is generated in
 * the browser on purpose: the API stays read-only (spec §8.2).
 */
export function buildAuditCSV(entries: AuditEntry[]): string {
  const lines = [CSV_HEADER.join(",")]
  for (const entry of entries) {
    lines.push(
      [
        entry.id,
        entry.created_at,
        entry.action,
        entry.actor_user_id,
        entry.target_type,
        entry.target_id,
        entry.request_id,
        entry.ip,
        entry.user_agent,
        entry.metadata,
      ]
        .map(csvCell)
        .join(","),
    )
  }
  return `${lines.join("\r\n")}\r\n`
}

/** Escapes a Markdown table cell so pipes and newlines cannot break the table. */
function markdownCell(value: unknown): string {
  const text =
    value === null || value === undefined
      ? ""
      : typeof value === "string"
        ? value
        : JSON.stringify(value)
  return text.replaceAll("\\", "\\\\").replaceAll("|", "\\|").replaceAll(/\r?\n/g, " ")
}

/**
 * Builds the Markdown export of the currently loaded audit page. Only ids,
 * timestamps, targets and request correlation are rendered.
 */
export function buildAuditMarkdown(entries: AuditEntry[]): string {
  const lines = [
    "| ID | 时间 | 操作 | 操作者 | 目标 | 请求 ID |",
    "| --- | --- | --- | --- | --- | --- |",
  ]
  for (const entry of entries) {
    const actor = entry.actor_user_id === null ? "" : String(entry.actor_user_id)
    const target =
      entry.target_type === ""
        ? entry.target_id
        : `${entry.target_type}${entry.target_id === "" ? "" : ` #${entry.target_id}`}`
    lines.push(
      `| ${markdownCell(entry.id)} | ${markdownCell(entry.created_at)} | ${markdownCell(entry.action)} | ${markdownCell(actor)} | ${markdownCell(target)} | ${markdownCell(entry.request_id)} |`,
    )
  }
  return `${lines.join("\n")}\n`
}

/** Returns a stable, timestamped export filename. */
export function auditExportFilename(format: "csv" | "md", now = new Date()): string {
  const stamp = now.toISOString().slice(0, 19).replaceAll(/[-:T]/g, "")
  return `audit-log-${stamp}.${format}`
}

/**
 * Triggers a browser download of generated text. It never sends the content to
 * the API; jsdom tests stub `URL.createObjectURL`.
 */
export function downloadTextFile(filename: string, content: string, mime: string): void {
  const blob = new Blob([content], { type: `${mime};charset=utf-8` })
  const url = URL.createObjectURL(blob)
  try {
    const anchor = document.createElement("a")
    anchor.href = url
    anchor.download = filename
    anchor.rel = "noopener"
    anchor.click()
  } finally {
    URL.revokeObjectURL(url)
  }
}
