"use client"

import { Paperclip } from "lucide-react"

import { cn } from "@/components/ui/cn"
import { EmptyState } from "@/components/ui/EmptyState"
import { Spinner } from "@/components/ui/Spinner"
import { formatDateTime } from "@/lib/format"

import { RequirementCard } from "./RequirementCard"
import { RichText } from "./RichText"
import { RollbackCard } from "./RollbackCard"
import type { ConversationMessage, MessagePart, RequirementDetail } from "./types"

export type MessageListProps = {
  versionId: string
  messages: ConversationMessage[]
  /** Current `agent.activity` text; null when the AI is idle. */
  activity: string | null
  onRequirementSelect: (requirementId: string) => void
  onRequirementChanged?: (requirement: RequirementDetail) => void
}

function MessagePartView({
  versionId,
  messageId,
  part,
  onRequirementSelect,
  onRequirementChanged,
}: {
  versionId: string
  messageId: string
  part: MessagePart
  onRequirementSelect: (requirementId: string) => void
  onRequirementChanged?: (requirement: RequirementDetail) => void
}) {
  switch (part.type) {
    case "text":
      return <RichText>{part.text}</RichText>
    case "attachment":
      return (
        <span
          data-testid="message-attachment"
          className="inline-flex items-center gap-1 rounded-full border border-border bg-muted px-2 py-0.5 text-[11px] text-muted-foreground"
        >
          <Paperclip className="size-3" aria-hidden="true" />
          {part.name}
        </span>
      )
    case "requirement_card":
      return <RequirementCard part={part} onSelect={onRequirementSelect} />
    case "rollback_proposal":
      return (
        <RollbackCard
          versionId={versionId}
          part={part}
          proposalMessageId={messageId}
          onRolledBack={onRequirementChanged}
        />
      )
    default:
      return null
  }
}

function partKey(part: MessagePart): string {
  switch (part.type) {
    case "text":
      return `text:${part.text.slice(0, 32)}`
    case "attachment":
      return `attachment:${part.sha256}`
    case "requirement_card":
      return `requirement:${part.requirement_id}`
    case "rollback_proposal":
      return `rollback:${part.requirement_id}`
  }
}

function MessageBubble({
  versionId,
  message,
  onRequirementSelect,
  onRequirementChanged,
}: {
  versionId: string
  message: ConversationMessage
  onRequirementSelect: (requirementId: string) => void
  onRequirementChanged?: (requirement: RequirementDetail) => void
}) {
  const isUser = message.role === "user"
  const seenKeys = new Map<string, number>()
  const parts = message.parts.map((part) => {
    const base = `${message.id}:${partKey(part)}`
    const occurrence = seenKeys.get(base) ?? 0
    seenKeys.set(base, occurrence + 1)
    return { key: `${base}:${occurrence}`, part }
  })
  return (
    <article
      data-testid={`message-${message.role}`}
      className={cn("flex flex-col gap-1", isUser ? "items-end" : "items-start")}
    >
      <span className="text-[10px] font-medium text-muted-foreground">{isUser ? "你" : "AI"}</span>
      <div
        className={cn(
          "max-w-[85%] space-y-2 rounded-xl border px-3 py-2",
          isUser ? "border-primary/20 bg-primary/5" : "border-border bg-card",
        )}
      >
        {parts.map(({ key, part }) => (
          <MessagePartView
            key={key}
            versionId={versionId}
            messageId={message.id}
            part={part}
            onRequirementSelect={onRequirementSelect}
            onRequirementChanged={onRequirementChanged}
          />
        ))}
      </div>
      <span className="text-[10px] text-muted-foreground">
        {formatDateTime(message.created_at)}
      </span>
    </article>
  )
}

/** Scrollable thread plus the AI activity bubble. */
export function MessageList({
  versionId,
  messages,
  activity,
  onRequirementSelect,
  onRequirementChanged,
}: MessageListProps) {
  return (
    <div
      data-testid="message-list"
      className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4 py-4"
    >
      {messages.length === 0 && activity === null ? (
        <EmptyState
          title="暂无消息"
          description="描述需求，AI 会先读取版本上下文，再产出需求草案。"
          className="border-none bg-transparent py-8"
        />
      ) : null}

      {messages.map((message) => (
        <MessageBubble
          key={message.id}
          versionId={versionId}
          message={message}
          onRequirementSelect={onRequirementSelect}
          onRequirementChanged={onRequirementChanged}
        />
      ))}

      {activity !== null ? (
        <div
          data-testid="agent-activity"
          className="flex items-center gap-2 rounded-lg border border-border bg-card px-3 py-2 text-[11px] text-muted-foreground"
        >
          <Spinner size="sm" label="AI 正在思考" />
          <span>{activity}</span>
        </div>
      ) : null}
    </div>
  )
}
