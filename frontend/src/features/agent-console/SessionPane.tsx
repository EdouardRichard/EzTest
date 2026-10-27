import type { AgentSession } from "./agent-api"

export type SessionPaneProps = {
  sessionId: number | null
  session: AgentSession | null
}

/**
 * Placeholder for the transcript + composer pane. Task 12 replaces this
 * component while keeping the same props contract.
 */
export function SessionPane({ sessionId, session }: SessionPaneProps) {
  return (
    <section
      aria-label="会话内容"
      className="rounded-2xl border border-border bg-card p-5 shadow-sm"
    >
      {sessionId === null ? (
        <p className="text-sm text-muted-foreground">选择或新建会话</p>
      ) : (
        <div className="space-y-1">
          <p className="text-sm font-medium">当前会话：{session?.title ?? `#${sessionId}`}</p>
          <p className="text-sm text-muted-foreground">转录与输入框将在后续任务接入。</p>
        </div>
      )}
    </section>
  )
}
