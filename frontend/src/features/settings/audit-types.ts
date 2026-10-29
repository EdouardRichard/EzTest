/** One audit-log entry returned by `GET /api/admin/audit` (read-only). */
export type AuditEntry = {
  id: number
  actor_user_id: number | null
  action: string
  target_type: string
  target_id: string
  request_id: string
  ip: string
  user_agent: string
  metadata: Record<string, unknown>
  created_at: string
}

/** The viewer's filter form state. Times are `datetime-local` values. */
export type AuditFilters = {
  actor: string
  action: string
  from: string
  to: string
}

/** The default page size requested from the admin audit endpoint. */
export const AUDIT_PAGE_SIZE = 20

/** The empty filter set (no narrowing). */
export const EMPTY_AUDIT_FILTERS: AuditFilters = {
  actor: "",
  action: "",
  from: "",
  to: "",
}
