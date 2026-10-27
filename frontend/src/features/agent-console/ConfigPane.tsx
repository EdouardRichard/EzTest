import type { AgentProfile } from "./agent-api"

export type ConfigPaneProps = {
  profiles: AgentProfile[]
  onSaved: (profile: AgentProfile) => void
}

/**
 * Placeholder for the profile editor. Task 13 replaces this component while
 * keeping the same props contract.
 */
export function ConfigPane({ profiles }: ConfigPaneProps) {
  return (
    <section
      aria-label="Agent 配置"
      className="rounded-2xl border border-border bg-card p-5 shadow-sm"
    >
      <p className="text-sm text-muted-foreground">配置在后续任务接入</p>
      <p className="mt-1 text-xs text-muted-foreground">已加载 {profiles.length} 个 Agent 配置。</p>
    </section>
  )
}
