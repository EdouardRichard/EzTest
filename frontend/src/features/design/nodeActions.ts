import type { CaseVersionStatus, DesignStatus } from "./types"

/** Callbacks shared by every custom mindmap node (passed through node data). */
export type DesignNodeActions = {
  onSelectCase: (caseId: string) => void
  onConfirmCase: (caseId: string) => void
  onConfirmScenario: (scenarioId: string) => void
  onConfirmRequirement: (requirementId: string) => void
  /** `${scope}:${targetId}` while a confirmation request is in flight. */
  confirmingKey: string | null
}

export const DESIGN_STATUS_LABEL: Record<DesignStatus, string> = {
  pending: "待设计",
  in_design: "设计中",
  draft: "草稿",
  confirmed: "已确认",
  stale: "已过期",
}

export const CASE_VERSION_STATUS_LABEL: Record<CaseVersionStatus, string> = {
  draft: "draft",
  confirmed: "confirmed",
  stale: "stale",
  superseded: "superseded",
}
