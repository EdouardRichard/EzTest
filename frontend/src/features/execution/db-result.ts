/**
 * Decoding helpers for the read-only DB assertion artifacts of P7 (spec §6.6,
 * §9.5, §10.6).
 *
 * Two shapes exist:
 * - the `db_result` evidence document written by `execution.db_assert`
 *   (`assertion_id/sql/columns/rows/row_count/truncated/elapsed_ms`);
 * - the ≤4 KiB `actual` preview stored on the `assertion_result`, which carries
 *   `columns/rows/rowCount/truncated` (and, defensively, an elapsed time).
 *
 * Parsing is defensive: a malformed artifact must degrade to an explanatory
 * message instead of crashing the results tab or the evidence viewer.
 */

export type DbResultDocument = {
  /** Decimal assertion id; empty when the artifact omits it. */
  assertionId: string
  sql: string
  columns: string[]
  rows: string[][]
  /** Total row count before truncation; null when unknown. */
  rowCount: number | null
  truncated: boolean
  /** Execution time in milliseconds; null when the artifact omits it. */
  elapsedMs: number | null
}

export type DbAssertionMeta = {
  rowCount: number | null
  truncated: boolean
  elapsedMs: number | null
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

function parseJsonObject(text: string): Record<string, unknown> | null {
  if (text.trim() === "") return null
  try {
    const parsed: unknown = JSON.parse(text)
    return isRecord(parsed) ? parsed : null
  } catch {
    return null
  }
}

function readString(value: unknown): string {
  return typeof value === "string" ? value : ""
}

function readNumber(value: unknown): number | null {
  return typeof value === "number" && Number.isFinite(value) ? value : null
}

function readBoolean(value: unknown): boolean {
  return value === true
}

/** Cells are strings in the artifact; any scalar JSON is rendered as text. */
function readCell(value: unknown): string {
  if (typeof value === "string") return value
  if (value === null || value === undefined) return ""
  if (typeof value === "object") return JSON.stringify(value) ?? ""
  return String(value)
}

function readColumns(value: unknown): string[] {
  if (!Array.isArray(value)) return []
  return value.map((column) => readCell(column))
}

function readRows(value: unknown): string[][] {
  if (!Array.isArray(value)) return []
  return value.filter(Array.isArray).map((row) => row.map((cell) => readCell(cell)))
}

/** True when the assertion kind is the read-only DB assertion of P7. */
export function isDbAssertionKind(kind: string): boolean {
  return kind === "db"
}

/** Parses the `db_result` evidence document; null when it is not a JSON object. */
export function parseDbResultEvidence(text: string): DbResultDocument | null {
  const record = parseJsonObject(text)
  if (record === null) return null

  return {
    assertionId: readString(record.assertion_id),
    sql: readString(record.sql),
    columns: readColumns(record.columns),
    rows: readRows(record.rows),
    rowCount: readNumber(record.row_count) ?? readNumber(record.rowCount),
    truncated: readBoolean(record.truncated),
    elapsedMs: readNumber(record.elapsed_ms) ?? readNumber(record.elapsedMs),
  }
}

/**
 * Parses the recorded `actual` preview of a db assertion. Returns null when the
 * preview is empty or not a JSON object (older rows stored free text).
 */
export function parseDbAssertionActual(actual: string): DbAssertionMeta | null {
  const record = parseJsonObject(actual)
  if (record === null) return null

  return {
    rowCount: readNumber(record.rowCount) ?? readNumber(record.row_count),
    truncated: readBoolean(record.truncated),
    elapsedMs: readNumber(record.elapsedMs) ?? readNumber(record.elapsed_ms),
  }
}

/** Stable db assertion failure reasons of spec §5.7/§7.11. */
export const DB_FAILURE_LABELS: Record<string, string> = {
  db_connection_failed: "数据库连接失败",
  db_target_missing: "未配置目标数据库",
  db_query_failed: "查询失败",
  db_readonly_violation: "只读违规",
  db_result_truncated: "结果已截断",
  db_expected_invalid: "期望值非法",
  assertion_mismatch: "断言不匹配",
}

/**
 * Readable copy of a db failure reason. Unknown reasons stay visible instead of
 * being hidden; an empty reason becomes an empty string.
 */
export function dbFailureLabel(reason: string | undefined): string {
  if (reason === undefined || reason === "") return ""
  return DB_FAILURE_LABELS[reason] ?? reason
}
