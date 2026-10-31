"use client"

import { useEffect, useRef, useState } from "react"

import { getApiBaseUrl } from "@/lib/api-client"
import { getToken } from "@/lib/auth-storage"
import { connectSSE, type SSEStatus } from "@/lib/sse-client"

import type { ConversationMessage, VersionEvent } from "./types"

export type VersionEventHandlers = {
  /** `conversation.message` payload (the full message object, spec §9). */
  onConversationMessage?: (message: ConversationMessage) => void
  /** `agent.activity` payload; shape is not pinned by the contract yet. */
  onAgentActivity?: (payload: Record<string, unknown>) => void
  /** Any `requirement.*` event; the workspace refetches the list. */
  onRequirementChanged?: (event: VersionEvent) => void
  /** `team_room.post` / `team_room.digest_updated`. */
  onTeamRoomChanged?: (event: VersionEvent) => void
  /** `version.started` / `version.status_changed`. */
  onVersionChanged?: (event: VersionEvent) => void
  /** Connection state for the status indicator. */
  onStatus?: (status: SSEStatus) => void
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

function toMessage(payload: Record<string, unknown>): ConversationMessage | null {
  const candidate = isRecord(payload.message) ? payload.message : payload
  if (typeof candidate.id !== "string" || typeof candidate.conversation_id !== "string") {
    return null
  }
  if (candidate.role !== "user" && candidate.role !== "assistant") return null
  if (!Array.isArray(candidate.parts)) return null
  return candidate as unknown as ConversationMessage
}

/**
 * Subscribes to the `version:{id}` channel through the P0 SSE client and routes
 * the P1 events the clarification workspace cares about. Reconnects/seq dedupe
 * stay inside `connectSSE`; handlers are read from a ref so a re-render never
 * reconnects the stream.
 */
export function useVersionEvents(versionId: string, handlers: VersionEventHandlers): SSEStatus {
  const [status, setStatus] = useState<SSEStatus>("connecting")
  const handlersRef = useRef(handlers)
  handlersRef.current = handlers

  useEffect(() => {
    function onEvent(frame: { event: string; data: string }): void {
      let event: VersionEvent
      try {
        const parsed: unknown = JSON.parse(frame.data)
        if (!isRecord(parsed) || typeof parsed.type !== "string") return
        event = parsed as unknown as VersionEvent
      } catch {
        return
      }

      const payload = isRecord(event.payload) ? event.payload : {}
      switch (event.type) {
        case "conversation.message": {
          const message = toMessage(payload)
          if (message !== null) handlersRef.current.onConversationMessage?.(message)
          break
        }
        case "agent.activity":
          handlersRef.current.onAgentActivity?.(payload)
          break
        case "conversation.created":
        case "conversation.summary_updated":
          break
        case "version.started":
        case "version.status_changed":
          handlersRef.current.onVersionChanged?.(event)
          break
        default:
          if (event.type.startsWith("requirement.")) {
            handlersRef.current.onRequirementChanged?.(event)
          } else if (event.type.startsWith("team_room.")) {
            handlersRef.current.onTeamRoomChanged?.(event)
          }
      }
    }

    const disconnect = connectSSE({
      url: `${getApiBaseUrl()}/api/stream?channel=version:${versionId}`,
      token: getToken() ?? "",
      onEvent,
      onStatus: (next) => {
        setStatus(next)
        handlersRef.current.onStatus?.(next)
      },
    })

    return disconnect
  }, [versionId])

  return status
}
