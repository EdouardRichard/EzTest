/**
 * Wire types of the P6 global search (`GET /api/search`).
 *
 * Field names and enums are pinned by the frozen P6 OpenAPI contract
 * (`SearchResultView`/`SearchGroup`/`SearchItem`): IDs are decimal strings,
 * group types are the five frozen values and `url` is the server-built 0.2
 * workspace deep link.
 */

/** Result types rendered by the command palette (canonical group order). */
export const SEARCH_TYPES = ["version", "requirement", "case", "issue", "report"] as const

export type SearchType = (typeof SEARCH_TYPES)[number]

/** One `SearchItem`. `subtitle` may be empty; status is the raw entity status. */
export type SearchItem = {
  id: string
  title: string
  subtitle: string
  version_id: string
  version_title: string
  status: string
  url: string
}

/** One `SearchGroup`. */
export type SearchGroup = {
  type: SearchType
  items: SearchItem[]
}

/** `SearchResultView` response body (`{"data": {...}}` once unwrapped). */
export type SearchSnapshot = {
  groups: SearchGroup[]
  total: number
  truncated: boolean
  degraded: boolean
}

/** Empty snapshot used before the first request and for a cleared query. */
export const EMPTY_SEARCH_SNAPSHOT: SearchSnapshot = {
  groups: [],
  total: 0,
  truncated: false,
  degraded: false,
}

/** Chinese label of each result group. */
export const GROUP_LABELS: Record<SearchType, string> = {
  version: "版本",
  requirement: "需求",
  case: "用例",
  issue: "问题",
  report: "报告",
}

/** Short hint telling where the group's deep link opens. */
export const GROUP_HINTS: Record<SearchType, string> = {
  version: "打开版本总览",
  requirement: "打开需求",
  case: "打开用例设计",
  issue: "查看问题",
  report: "查看报告",
}
