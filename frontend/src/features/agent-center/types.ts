import type { BadgeTone } from "@/components/ui/Badge"

/**
 * Wire types for the P6 Agent center.
 *
 * Names/fields are pinned by the frozen P6 OpenAPI contract
 * (`docs/version-V0.2/contracts/openapi.yaml`): IDs are decimal strings,
 * timestamps RFC3339 UTC. The read models never carry a transcript, and only
 * the admin profile/prompt endpoints return the prompt body.
 */

/* --------------------------------- catalog -------------------------------- */

export type AgentTeam = {
  team: string
  label: string
  profile_keys: string[]
  note?: string
}

export type RunStats = {
  run_count: number
  done_count: number
  failed_count: number
  aborted_count: number
  interrupted_count: number
  success_rate: number
  last_run_at: string | null
}

export type ProfileBudgets = {
  max_turns: number
  soft_seconds: number
  hard_seconds: number
}

export type CatalogProfile = {
  profile_key: string
  name: string
  team: string
  order: number
  mode: "agent"
  model: string | null
  enabled: boolean
  budgets: ProfileBudgets
  allowed_tools_count: number
  prompt_version: number
  prompt_updated_at: string | null
  updated_at: string
  stats: RunStats
}

export type AgentCatalog = {
  profiles: CatalogProfile[]
  teams: AgentTeam[]
}

/* ---------------------------------- runs ---------------------------------- */

export type AgentRunStatus = "running" | "done" | "failed" | "aborted" | "interrupted"

export type AgentRunView = {
  id: string
  profile_key: string
  work_item_id: string | null
  work_type: string | null
  version_id: string | null
  version_title?: string
  conversation_id: string | null
  status: AgentRunStatus
  terminal_reason: string | null
  turns: number
  usage: Record<string, number>
  started_at: string | null
  finished_at: string | null
}

/* --------------------------------- quality -------------------------------- */

export type EvalSummary = {
  id: string
  mode: "real_llm"
  status: "running" | "passed" | "failed" | "error"
  created_at: string
  metrics_count: number
  metrics_passed: number
}

export type AgentCenterQuality = {
  latest: EvalSummary | null
  run_stats: RunStats
}

/* --------------------------------- profile -------------------------------- */

export type ProfileView = {
  id: string
  key: string
  name: string
  system_prompt: string
  allowed_tools: string[]
  model: string | null
  budgets: ProfileBudgets
  enabled: boolean
  prompt_version: number
  created_at: string
  updated_at: string
}

export type PromptVariable = {
  name: string
  description: string
  example: string
  source: "runtime"
}

export type ProfileDetail = {
  profile: ProfileView
  default_system_prompt: string
  variables: PromptVariable[]
  prompt_version: number
}

export type PromptMutation = {
  profile: ProfileView
  version: number
  changed: boolean
}

export type PromptRevision = {
  version: number
  system_prompt: string
  note: string | null
  source: "seed" | "admin" | "reset"
  actor_id: string | null
  created_at: string
}

/* ---------------------------------- tools --------------------------------- */

export type AgentTool = {
  name: string
  description: string
  parameters: Record<string, unknown>
}

export type LLMModel = {
  id: string
  model: string
  display_name: string
  status: string
}

/* --------------------------------- UI meta -------------------------------- */

export const PROMPT_MAX_RUNES = 32768

export const AGENT_RUN_STATUS_LABEL: Record<AgentRunStatus, string> = {
  running: "运行中",
  done: "完成",
  failed: "失败",
  aborted: "中止",
  interrupted: "中断",
}

export const AGENT_RUN_STATUS_TONE: Record<AgentRunStatus, BadgeTone> = {
  running: "info",
  done: "success",
  failed: "danger",
  aborted: "warning",
  interrupted: "warning",
}

export const EVAL_STATUS_LABEL: Record<EvalSummary["status"], string> = {
  running: "运行中",
  passed: "通过",
  failed: "失败",
  error: "错误",
}

export const EVAL_STATUS_TONE: Record<EvalSummary["status"], BadgeTone> = {
  running: "info",
  passed: "success",
  failed: "danger",
  error: "warning",
}

/** Percent formatting for a 0–1 ratio; `0` stays `0%` and null is a dash. */
export function formatRate(value: number | null | undefined): string {
  if (value === null || value === undefined || Number.isNaN(value)) return "—"
  return `${(value * 100).toFixed(2).replace(/\.?0+$/, "")}%`
}

/** Counts the prompt limit in Unicode code points (not UTF-16 units). */
export function promptRuneCount(value: string): number {
  return Array.from(value).length
}

/** Renders a run usage object as compact tokens text. */
export function formatUsage(usage: Record<string, number>): string {
  const entries = Object.entries(usage).filter(([, value]) => Number.isFinite(value))
  if (entries.length === 0) return "—"
  return entries.map(([key, value]) => `${key}: ${value}`).join(" · ")
}

export const PROMPT_SOURCE_LABEL: Record<PromptRevision["source"], string> = {
  seed: "seed",
  admin: "admin",
  reset: "reset",
}

/** One line of a line diff. `same` lines have no prefix. */
export type DiffLine = {
  kind: "same" | "add" | "remove"
  text: string
}

/**
 * Line diff between a historical revision and the current prompt. The
 * LCS-based implementation is bounded for the 32 KB prompt editor: very large
 * inputs fall back to a section replacement instead of a quadratic table.
 */
export function diffLines(previous: string, current: string): DiffLine[] {
  const before = previous.split("\n")
  const after = current.split("\n")
  if (before.length * after.length > 40_000) {
    const lines: DiffLine[] = []
    for (const text of before) lines.push({ kind: "remove", text })
    for (const text of after) lines.push({ kind: "add", text })
    return lines
  }

  const table: number[][] = Array.from({ length: before.length + 1 }, () =>
    new Array<number>(after.length + 1).fill(0),
  )
  for (let i = before.length - 1; i >= 0; i -= 1) {
    for (let j = after.length - 1; j >= 0; j -= 1) {
      table[i][j] =
        before[i] === after[j]
          ? table[i + 1][j + 1] + 1
          : Math.max(table[i + 1][j], table[i][j + 1])
    }
  }

  const lines: DiffLine[] = []
  let i = 0
  let j = 0
  while (i < before.length && j < after.length) {
    if (before[i] === after[j]) {
      lines.push({ kind: "same", text: before[i] })
      i += 1
      j += 1
    } else if (table[i + 1][j] >= table[i][j + 1]) {
      lines.push({ kind: "remove", text: before[i] })
      i += 1
    } else {
      lines.push({ kind: "add", text: after[j] })
      j += 1
    }
  }
  while (i < before.length) {
    lines.push({ kind: "remove", text: before[i] })
    i += 1
  }
  while (j < after.length) {
    lines.push({ kind: "add", text: after[j] })
    j += 1
  }
  return lines
}
