/** Wire types for P5 code anchors (frozen `CodeAnchorView`). */

import type { BadgeTone } from "@/components/ui/Badge"

export type CodeSubjectKind =
  | "version"
  | "requirement"
  | "requirement_version"
  | "case_version"
  | "issue"

export type CodeAnchorStatus = "valid" | "stale" | "broken"

export type CodeConfidence = "low" | "medium" | "high"

export type CodeAnchorCreatedByKind = "user" | "ai" | "system"

/** `CodeAnchorView`: a path:line reference owned by one design/issue subject. */
export type CodeAnchor = {
  id: string
  version_id: string
  repo_id: string
  subject_kind: CodeSubjectKind
  subject_id: string
  file_path: string
  start_line: number | null
  end_line: number | null
  entity_id?: string | null
  commit_sha: string | null
  excerpt_sha?: string | null
  status: CodeAnchorStatus
  confidence: CodeConfidence
  created_by_kind: CodeAnchorCreatedByKind
  created_by?: string | null
  last_validated_at: string | null
  created_at: string
  updated_at: string
}

/** `CreateCodeAnchorRequest` (camelCase body, spec §4.2 #19). */
export type CreateCodeAnchorInput = {
  subjectKind: CodeSubjectKind
  subjectId: string
  repoId: string
  filePath: string
  startLine?: number
  endLine?: number
  excerptSha?: string | null
}

export type CodeAnchorListOptions = {
  subjectKind: CodeSubjectKind
  subjectId: string
}

/** One selectable version-bound repository for the "add anchor" form. */
export type CodeRepoOption = {
  id: string
  name: string
}

export const CODE_ANCHOR_STATUS_LABEL: Record<CodeAnchorStatus, string> = {
  valid: "正常",
  stale: "内容已变",
  broken: "文件/行失效",
}

export const CODE_ANCHOR_STATUS_TONE: Record<CodeAnchorStatus, BadgeTone> = {
  valid: "success",
  stale: "warning",
  broken: "danger",
}

export const CODE_SUBJECT_KIND_LABEL: Record<CodeSubjectKind, string> = {
  version: "版本",
  requirement: "需求",
  requirement_version: "需求版本",
  case_version: "用例版本",
  issue: "问题",
}
