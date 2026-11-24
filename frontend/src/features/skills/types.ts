/** One active skill of the runtime library (admin SkillView contract). */
export type SkillView = {
  id: string
  name: string
  description: string
  body: string
  metadata: Record<string, unknown>
  version: number
  enabled: boolean
  created_at: string
  updated_at: string
}

/** One append-only skill revision (admin SkillVersionView contract). */
export type SkillVersionView = {
  version: number
  name: string
  description: string
  body: string
  metadata: Record<string, unknown>
  enabled: boolean
  source: "seed" | "admin"
  actor_id: string | null
  note: string
  created_at: string
}

export const SKILL_SOURCE_LABEL: Record<SkillVersionView["source"], string> = {
  seed: "文件播种",
  admin: "管理员编辑",
}

/** Skill package name shape (mirrors the backend domain pattern). */
export const SKILL_NAME_PATTERN = /^[a-z][a-z0-9_-]{0,63}$/
