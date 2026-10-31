"use client"

import { MessageSquarePlus } from "lucide-react"
import { useState } from "react"

import { Button } from "@/components/ui/Button"
import { cn } from "@/components/ui/cn"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import { formatRelativeTime } from "@/lib/format"
import type { SSEStatus } from "@/lib/sse-client"

import { Composer } from "./Composer"
import { SSE_STATUS_LABEL } from "./labels"
import { MessageList } from "./MessageList"
import type { ConversationMessage, ConversationSummary, RequirementDetail } from "./types"
import { useConversation } from "./useConversation"

export type ChatPanelProps = {
  versionId: string
  conversationId: string | null
  conversations: ConversationSummary[]
  onSelectConversation: (conversationId: string) => void
  onCreateConversation: () => void
  creatingConversation?: boolean
  onRequirementSelect: (requirementId: string) => void
  /** Fired when a rollback card succeeds; the sidebar refreshes. */
  onRequirementChanged?: (requirement: RequirementDetail) => void
  /** Conversation messages pushed by the `version:{id}` SSE channel. */
  liveMessages?: ConversationMessage[]
  /** Current `agent.activity` text. */
  activity?: string | null
  sseStatus?: SSEStatus
}

/**
 * Chat is the primary clarification UI: conversation header with history/new,
 * message thread (text, attachments, requirement cards, rollback cards), the AI
 * activity indicator and the composer.
 */
export function ChatPanel({
  versionId,
  conversationId,
  conversations,
  onSelectConversation,
  onCreateConversation,
  creatingConversation = false,
  onRequirementSelect,
  onRequirementChanged,
  liveMessages = [],
  activity = null,
  sseStatus = "connecting",
}: ChatPanelProps) {
  const [historyOpen, setHistoryOpen] = useState(false)
  const { messages, loading, error, reload, send, sendError } = useConversation({
    versionId,
    conversationId,
    liveMessages,
  })

  const active = conversations.find((conversation) => conversation.id === conversationId)

  return (
    <section
      data-testid="chat-panel"
      aria-label="需求澄清对话"
      className="surface-card flex min-h-[520px] flex-col overflow-hidden lg:max-h-[calc(100vh-260px)]"
    >
      <header className="flex flex-wrap items-center gap-2 border-b border-border px-4 py-3">
        <div className="min-w-0">
          <h2 className="text-sm font-semibold text-foreground">需求澄清 · 与澄清团队对话</h2>
          <p className="mt-0.5 truncate text-[11px] text-muted-foreground">
            {active?.title ?? "对话是主入口；AI 自动读取版本上下文、页面清单与知识"}
          </p>
        </div>
        <div className="ml-auto flex items-center gap-1.5">
          <span data-testid="sse-status" className="text-[10px] text-muted-foreground">
            {SSE_STATUS_LABEL[sseStatus]}
          </span>
          <div className="relative">
            <Button
              variant="secondary"
              size="sm"
              aria-expanded={historyOpen}
              onClick={() => setHistoryOpen((open) => !open)}
            >
              历史对话
            </Button>
            {historyOpen ? (
              <div
                data-testid="conversation-history-list"
                className="absolute right-0 z-20 mt-1 w-64 rounded-lg border border-border bg-card p-1 shadow-lg"
              >
                {conversations.length === 0 ? (
                  <p className="px-2 py-1.5 text-[11px] text-muted-foreground">暂无历史对话</p>
                ) : (
                  conversations.map((conversation) => (
                    <button
                      key={conversation.id}
                      type="button"
                      onClick={() => {
                        onSelectConversation(conversation.id)
                        setHistoryOpen(false)
                      }}
                      className={cn(
                        "flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-xs",
                        "hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                        conversation.id === conversationId && "bg-accent/60",
                      )}
                    >
                      <span className="min-w-0 flex-1 truncate">{conversation.title}</span>
                      <span className="text-[10px] text-muted-foreground">
                        {conversation.message_count} 条
                      </span>
                      <span className="text-[10px] text-muted-foreground">
                        {formatRelativeTime(conversation.last_message_at)}
                      </span>
                    </button>
                  ))
                )}
              </div>
            ) : null}
          </div>
          <Button
            size="sm"
            disabled={creatingConversation}
            onClick={onCreateConversation}
            className="gap-1"
          >
            <MessageSquarePlus className="size-3.5" aria-hidden="true" />
            {creatingConversation ? "创建中…" : "＋ 新增对话"}
          </Button>
        </div>
      </header>

      {conversationId === null ? (
        <div className="flex flex-1 items-center justify-center p-6">
          <EmptyState
            title="暂无澄清对话"
            description="新建对话后，澄清团队会读取版本上下文并产出需求草案。"
            action={
              <Button size="sm" disabled={creatingConversation} onClick={onCreateConversation}>
                ＋ 新增对话
              </Button>
            }
            className="border-none bg-transparent"
          />
        </div>
      ) : loading ? (
        <LoadingCard label="正在加载对话…" />
      ) : error !== null ? (
        <div className="p-4">
          <ErrorState error={error} title="对话加载失败" onRetry={reload} />
        </div>
      ) : (
        <>
          <MessageList
            versionId={versionId}
            messages={messages}
            activity={activity}
            onRequirementSelect={onRequirementSelect}
            onRequirementChanged={onRequirementChanged}
          />
          <Composer onSend={send} error={sendError ?? undefined} />
        </>
      )}
    </section>
  )
}
