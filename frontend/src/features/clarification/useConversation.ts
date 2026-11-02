"use client"

import { useCallback, useEffect, useState } from "react"

import { listMessages, postMessage } from "./api"
import type { ConversationMessage, NewMessagePart } from "./types"

function compareMessageIds(left: ConversationMessage, right: ConversationMessage): number {
  try {
    const leftId = BigInt(left.id)
    const rightId = BigInt(right.id)
    if (leftId === rightId) return 0
    return leftId < rightId ? -1 : 1
  } catch {
    return left.id.localeCompare(right.id)
  }
}

function sortMessages(messages: ConversationMessage[]): ConversationMessage[] {
  return [...messages].sort(compareMessageIds)
}

function createClientMessageId(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID()
  }
  return `client_${Date.now()}_${Math.random().toString(36).slice(2, 10)}`
}

export type UseConversationOptions = {
  versionId: string
  conversationId: string | null
  /** Messages pushed by the version SSE channel; merged by id. */
  liveMessages?: ConversationMessage[]
}

export type UseConversationResult = {
  messages: ConversationMessage[]
  loading: boolean
  error: unknown
  sending: boolean
  sendError: unknown
  reload: () => void
  /** Returns true when the API accepted the message (draft can be cleared). */
  send: (parts: NewMessagePart[]) => Promise<boolean>
}

/**
 * Owns one conversation thread: initial fetch, live SSE merge, send with a
 * client idempotency key, and reload for retries. The SSE connection itself is
 * owned by the workspace (`useVersionEvents`), so this hook never reconnects.
 */
export function useConversation({
  versionId,
  conversationId,
  liveMessages = [],
}: UseConversationOptions): UseConversationResult {
  const [messages, setMessages] = useState<ConversationMessage[]>([])
  const [loading, setLoading] = useState(conversationId !== null)
  const [error, setError] = useState<unknown>(null)
  const [reloadToken, setReloadToken] = useState(0)
  const [sending, setSending] = useState(false)
  const [sendError, setSendError] = useState<unknown>(null)

  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadToken is a manual refresh trigger
  useEffect(() => {
    let cancelled = false
    if (conversationId === null) {
      setMessages([])
      setLoading(false)
      setError(null)
      return () => {
        cancelled = true
      }
    }

    setLoading(true)
    setError(null)
    void listMessages(versionId, conversationId).then(
      (next) => {
        if (cancelled) return
        setMessages(sortMessages(next))
        setLoading(false)
      },
      (cause: unknown) => {
        if (cancelled) return
        setError(cause)
        setLoading(false)
      },
    )

    return () => {
      cancelled = true
    }
  }, [versionId, conversationId, reloadToken])

  useEffect(() => {
    if (conversationId === null || liveMessages.length === 0) return
    setMessages((current) => {
      const known = new Set(current.map((message) => message.id))
      const additions = liveMessages.filter(
        (message) => message.conversation_id === conversationId && !known.has(message.id),
      )
      return additions.length === 0 ? current : sortMessages([...current, ...additions])
    })
  }, [liveMessages, conversationId])

  const reload = useCallback(() => {
    setReloadToken((token) => token + 1)
  }, [])

  const send = useCallback(
    async (parts: NewMessagePart[]): Promise<boolean> => {
      if (conversationId === null) return false
      setSending(true)
      setSendError(null)
      try {
        const message = await postMessage(versionId, conversationId, {
          client_message_id: createClientMessageId(),
          parts,
        })
        setMessages((current) =>
          current.some((existing) => existing.id === message.id)
            ? current
            : sortMessages([...current, message]),
        )
        return true
      } catch (cause) {
        setSendError(cause)
        return false
      } finally {
        setSending(false)
      }
    },
    [versionId, conversationId],
  )

  return { messages, loading, error, sending, sendError, reload, send }
}
