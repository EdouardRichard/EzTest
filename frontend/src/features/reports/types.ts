/**
 * Wire types and fixed vocabulary for the human-useful report views (v0.2.4-G
 * SC-G1/G2/G4/G11, T12).
 *
 * The authoritative report is the server-rendered, byte-frozen template v3
 * document (T9/T10): five-element defect cards, AC verification matrix, judge
 * display, failure explanations and waived content all live in that HTML. The
 * frontend renders the document verbatim (see ReportContentView) — there is no
 * second React truth for the report body, so the AC verification state
 * vocabulary was deleted as unwired code (H-G4 #31). The closure-kind
 * vocabulary below is pinned against the Go authority by
 * `report-view.authority.test.ts` (宪法五.1/五.2).
 *
 * Waiver wire shapes mirror `version/interfaces/httpapi/waiver_handler.go` (T6):
 * IDs are decimal strings, the reason body is `{reason}`, responses are
 * `{data: ...}` unwrapped by api-client.
 */

/**
 * `version.closure_kind` (T2/T6): normal close vs admin waived close. This is
 * the single frontend authority — `versions/types.ts` imports this type for the
 * `Version.closure_kind` wire field (H-G4 #32), and `report-view.authority.test`
 * pins it against the Go delivery display authority.
 */
export const CLOSURE_KINDS = ["normal", "waived"] as const
export type VersionClosureKind = (typeof CLOSURE_KINDS)[number]

export const CLOSURE_KIND_LABELS: Record<VersionClosureKind, string> = {
  normal: "正常闭环",
  waived: "豁免闭环",
}

/** Reason body shared by both admin waiver endpoints (1..500 server-side). */
export type WaiveInput = {
  reason: string
}

/** `POST /api/versions/{id}/waive-close` response (waiveCloseResponseDTO). */
export type WaiveCloseResult = {
  version_id: string
  status: string
  closure_kind: VersionClosureKind
  unverified: boolean
  close_reason: string
  report_id: string
  report_revision: number
}

/** `POST /api/versions/{id}/requirements/{rid}/waive` response. */
export type WaiveRequirementResult = {
  requirement_id: string
  waived: boolean
}
