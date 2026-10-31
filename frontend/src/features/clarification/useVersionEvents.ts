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
  /** `design.graph_delta`: refetch the design graph snapshot. */
  onDesignGraphChanged?: (event: VersionEvent) => void
  /** `design.case_version_*` / `design.case_confirmed`: refresh graph and case detail. */
  onDesignCaseChanged?: (event: VersionEvent) => void
  /** `design.round_started` / `design.scope_activated`: refresh overview and conversations. */
  onDesignRoundChanged?: (event: VersionEvent) => void
  /** `design.export_ready`: refresh export history. */
  onDesignExportReady?: (event: VersionEvent) => void
  /** P3 `wave.*`: refresh the execution snapshot (the tab debounces). */
  onWaveChanged?: (event: VersionEvent) => void
  /** P3 `case_run.*` including step/assertion completion: refresh the trace. */
  onCaseRunChanged?: (event: VersionEvent) => void
  /** P3 `evidence.created`: refresh the evidence snapshot. */
  onEvidenceCreated?: (event: VersionEvent) => void
  /** P3 `human_request.*`: refresh open takeover requests and pause state. */
  onHumanRequestChanged?: (event: VersionEvent) => void
  /** P3 `execution.*` intent/failure/coverage events. */
  onExecutionChanged?: (event: VersionEvent) => void
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
        case "design.graph_delta":
          handlersRef.current.onDesignGraphChanged?.(event)
          break
        case "design.case_version_created":
        case "design.case_version_stale":
        case "design.case_version_superseded":
        case "design.case_confirmed":
          handlersRef.current.onDesignCaseChanged?.(event)
          break
        case "design.round_started":
        case "design.scope_activated":
          handlersRef.current.onDesignRoundChanged?.(event)
          break
        case "design.export_ready":
          handlersRef.current.onDesignExportReady?.(event)
          break
        case "wave.created":
        case "wave.started":
        case "wave.paused":
        case "wave.resumed":
        case "wave.finished":
          handlersRef.current.onWaveChanged?.(event)
          break
        case "case_run.started":
        case "case_run.paused":
        case "case_run.resumed":
        case "case_run.finished":
        case "case_run.step_finished":
        case "case_run.assertion_finished":
          handlersRef.current.onCaseRunChanged?.(event)
          break
        case "evidence.created":
          handlersRef.current.onEvidenceCreated?.(event)
          break
        case "human_request.created":
        case "human_request.resolved":
        case "human_request.expired":
          handlersRef.current.onHumanRequestChanged?.(event)
          break
        case "execution.intent_created":
        case "execution.intent_cancelled":
        case "execution.intent_superseded":
        case "execution.failure_candidate":
        case "execution.coverage_gap":
          handlersRef.current.onExecutionChanged?.(event)
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
