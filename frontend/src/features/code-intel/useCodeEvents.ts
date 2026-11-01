"use client"

import { useEffect, useRef, useState } from "react"

import { getApiBaseUrl } from "@/lib/api-client"
import { getToken } from "@/lib/auth-storage"
import { connectSSE, type SSEStatus } from "@/lib/sse-client"

export type CodeEventHandlers = {
  /** Any `code.*` event: refresh the version/RUN snapshots after the burst. */
  onCodeChanged?: () => void
  /** Synthetic `stream.reset`: refetch now instead of reconnecting. */
  onStreamReset?: (data: unknown) => void
  /** Connection state for the status indicator. */
  onStatus?: (status: SSEStatus) => void
}

/** One SSE burst (index + analysis + impact) collapses into one refresh. */
export const CODE_EVENT_DEBOUNCE_MS = 350

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

/**
 * Subscribes the code tab to the version channel through the P0 SSE client
 * (spec §9.2, §10). Only `code.*` events schedule the debounced snapshot
 * refresh; the stream reconnection/cursor dedupe stays inside `connectSSE`.
 * Handlers live in a ref so a re-render never reconnects the stream.
 */
export function useCodeEvents(versionId: string, handlers: CodeEventHandlers): SSEStatus {
  const [status, setStatus] = useState<SSEStatus>("connecting")
  const handlersRef = useRef(handlers)
  handlersRef.current = handlers

  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | null = null

    function clearTimer(): void {
      if (timer !== null) {
        clearTimeout(timer)
        timer = null
      }
    }

    function onEvent(frame: { event: string; data: string }): void {
      let parsed: unknown
      try {
        parsed = JSON.parse(frame.data)
      } catch {
        return
      }
      if (!isRecord(parsed) || typeof parsed.type !== "string") return
      if (!parsed.type.startsWith("code.")) return
      clearTimer()
      timer = setTimeout(() => {
        timer = null
        handlersRef.current.onCodeChanged?.()
      }, CODE_EVENT_DEBOUNCE_MS)
    }

    const disconnect = connectSSE({
      url: `${getApiBaseUrl()}/api/stream?channel=version:${versionId}`,
      token: getToken() ?? "",
      onEvent,
      onReset: (data) => {
        handlersRef.current.onStreamReset?.(data)
      },
      onStatus: (next) => {
        setStatus(next)
        handlersRef.current.onStatus?.(next)
      },
    })

    return () => {
      clearTimer()
      disconnect()
    }
  }, [versionId])

  return status
}
