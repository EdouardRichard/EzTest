"use client"

import { MessageSquarePlus } from "lucide-react"
import { useState } from "react"

import { Button } from "@/components/ui/Button"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import { Composer } from "@/features/clarification/Composer"
import { SSE_STATUS_LABEL } from "@/features/clarification/labels"
import { MessageList } from "@/features/clarification/MessageList"
import { TeamRoomPanel } from "@/features/clarification/TeamRoomPanel"
import type {
  ConversationMessage,
  ConversationSummary,
  NewMessagePart,
} from "@/features/clarification/types"
import { useConversation } from "@/features/clarification/useConversation"
import { formatRelativeTime } from "@/lib/format"
import type { SSEStatus } from "@/lib/sse-client"

export type DesignChatProps = {
  versionId: string
  conversations: ConversationSummary[]
  activeConversationId: string | null
  /** Latest design conversation is always the active round (no close action). */
  onSelectConversation: (conversationId: string) => void
  onCreateConversation: () => void
  creatingConversation?: boolean
  /** Messages pushed by the `version:{id}` SSE channel. */
  liveMessages?: ConversationMessage[]
  /** Current `agent.activity` text. */
  activity?: string | null
  sseStatus?: SSEStatus
  /** `design.case_version_stale` system hint. */
  staleNotice?: string | null
  /** External draft filled by "请求修改"; never sent automatically. */
  draftText?: string
  onDraftTextChange?: (value: string) => void
  /** Bumped by `team_room.*` events for the read-only design team room. */
  refreshToken?: number
}

/**
 * Design conversation panel: history/new conversation, Markdown messages,
 * attachments, the read-only design team room and the composer. There is no
 * "结束本轮"/close action — the latest design conversation is always current
 * (user decision 2026-10-31).
 */
export function DesignChat({
  versionId,
  conversations,
  activeConversationId,
  onSelectConversation,
  onCreateConversation,
  creatingConversation = false,
  liveMessages = [],
  activity = null,
  sseStatus = "connecting",
  staleNotice = null,
  draftText,
  onDraftTextChange,
  refreshToken = 0,
}: DesignChatProps) {
  const [historyOpen, setHistoryOpen] = useState(false)
  const { messages, loading, error, reload, send, sendError } = useConversation({
    versionId,
    conversationId: activeConversationId,
    liveMessages,
  })
  const active = conversations.find((conversation) => conversation.id === activeConversationId)

  async function handleSend(parts: NewMessagePart[]): Promise<boolean> {
    const sent = await send(parts)
    if (sent) onDraftTextChange?.("")
    return sent
  }

  return (
    <section
      data-testid="design-chat"
      aria-label="设计对话"
      className="surface-card flex min-h-[560px] flex-col overflow-hidden lg:max-h-[720px]"
    >
      <header className="flex flex-wrap items-center gap-2 border-b border-border px-3 py-2.5">
        <div className="min-w-0">
          <h2 className="text-sm font-semibold text-foreground">设计对话</h2>
          <p className="mt-0.5 truncate text-[11px] text-muted-foreground">
            {active?.title ?? "创建对话后，设计团队会扫描待设计需求并生成用例"}
          </p>
        </div>
        <div className="ml-auto flex items-center gap-1.5">
          <span data-testid="design-sse-status" className="text-[10px] text-muted-foreground">
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
                data-testid="design-chat-history-list"
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
                      className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-xs hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
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
            data-testid="design-chat-new-conversation"
            disabled={creatingConversation}
            onClick={onCreateConversation}
            className="gap-1"
          >
            <MessageSquarePlus className="size-3.5" aria-hidden="true" />
            {creatingConversation ? "创建中…" : "＋ 新建对话"}
          </Button>
        </div>
      </header>

      {staleNotice !== null ? (
        <p
          data-testid="design-stale-notice"
          role="status"
          className="border-b border-warning/30 bg-warning/10 px-3 py-2 text-[11px] text-warning"
        >
          {staleNotice}
        </p>
      ) : null}

      {activeConversationId === null ? (
        <div className="flex flex-1 items-center justify-center p-6">
          <EmptyState
            title="暂无设计对话"
            description="新建对话后，设计团队会扫描已澄清需求并实时生成用例脑图。"
            action={
              <Button
                size="sm"
                data-testid="design-chat-empty-create"
                disabled={creatingConversation}
                onClick={onCreateConversation}
              >
                ＋ 新建对话
              </Button>
            }
            className="border-none bg-transparent"
          />
        </div>
      ) : loading ? (
        <LoadingCard label="正在加载设计对话…" />
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
            onRequirementSelect={() => {}}
          />
          <Composer
            onSend={handleSend}
            error={sendError ?? undefined}
            placeholder="描述设计修改，或上传参考文档…"
            text={draftText}
            onTextChange={onDraftTextChange}
          />
        </>
      )}

      <TeamRoomPanel versionId={versionId} team="design" refreshToken={refreshToken} />
    </section>
  )
}
