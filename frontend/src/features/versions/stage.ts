import type { VersionStage } from "@/features/design/types"

/** Human labels for the deterministic version stage machine (spec §5.3). */
export const VERSION_STAGE_LABEL: Record<VersionStage, string> = {
  created: "已创建",
  clarifying: "澄清中",
  requirement_review: "需求待确认",
  designing: "设计中",
  case_review: "用例待确认",
  executing: "执行中",
  reporting: "报告生成中",
  completed: "已完成",
  blocked: "已阻塞",
  paused: "已暂停",
}

/**
 * Stages where the design workspace may create design conversations or mutate
 * the design graph. Unknown (`null`/`undefined`) stays permissive: the backend
 * stage guards are authoritative and the UI must not flash a blocking notice
 * before the stage projection has loaded.
 */
const DESIGN_STAGES: readonly VersionStage[] = [
  "designing",
  "case_review",
  "executing",
  "reporting",
  "completed",
]

export function stageLabel(stage: VersionStage): string {
  return VERSION_STAGE_LABEL[stage] ?? stage
}

export function designActionsAllowed(stage: VersionStage | null | undefined): boolean {
  if (stage === null || stage === undefined) return true
  return DESIGN_STAGES.includes(stage)
}

export function caseReviewStage(stage: VersionStage | null | undefined): boolean {
  if (stage === null || stage === undefined) return true
  return stage === "case_review"
}

/** Guidance shown when a design/case action is not available at the stage. */
export function designStageGuidance(stage: VersionStage | null | undefined): string {
  if (stage === "created" || stage === "clarifying") {
    return "当前版本还在澄清阶段。请先在「需求与澄清」确认需求，再点击「开始设计」通过需求门。"
  }
  if (stage === "requirement_review") {
    return "当前版本在需求确认门。请先在「需求与澄清」点击「开始设计」通过需求门后再设计用例。"
  }
  if (stage === "blocked" || stage === "paused") {
    return `当前版本${stageLabel(stage)}，恢复后才能继续用例设计。`
  }
  return `当前版本阶段为「${stageLabel(stage as VersionStage)}」，暂不能执行该用例操作。`
}

/** Guidance shown when case confirmation/execution is not at the case gate. */
export function caseGateGuidance(stage: VersionStage | null | undefined): string {
  if (stage === null || stage === undefined) return "当前不在用例评审阶段，暂不能确认或执行用例。"
  return `当前版本阶段为「${stageLabel(stage)}」。用例确认与批量执行只在「用例待确认」阶段可用。`
}
