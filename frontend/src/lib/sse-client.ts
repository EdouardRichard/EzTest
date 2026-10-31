import { clearToken, getToken } from "./auth-storage"

/** Connection states surfaced to the UI status dot. */
export type SSEStatus = "connecting" | "live" | "reconnecting" | "closed"

/** One dispatched SSE frame (`id` is the channel seq when the server sent one). */
export type SSEEvent = {
  id?: string
  event: string
  data: string
}

export type ConnectSSEOptions = {
  /** Absolute stream URL, usually `${getApiBaseUrl()}/api/stream?channel=system`. */
  url: string
  token: string
  /** Initial channel cursor (same meaning as the `Last-Event-ID` header). */
  lastEventId?: number
  onEvent: (event: SSEEvent) => void
  onStatus: (status: SSEStatus) => void
  /** Called for the synthetic `stream.reset` frame. It is never an onEvent. */
  onReset?: (data: unknown) => void
}

/** The server sends a heartbeat comment every 20s; 45s without bytes reconnects. */
export const SSE_SILENCE_TIMEOUT_MS = 45_000
const BASE_RETRY_MS = 1_000
const MAX_RETRY_MS = 30_000

type ParsedFrame = {
  id?: string
  event: string
  data: string
}

function parseFrame(block: string): ParsedFrame | null {
  let id: string | undefined
  let event = "message"
  const dataLines: string[] = []

  for (const line of block.split("\n")) {
    if (line.length === 0 || line.startsWith(":")) continue
    const separator = line.indexOf(":")
    const field = separator === -1 ? line : line.slice(0, separator)
    let value = separator === -1 ? "" : line.slice(separator + 1)
    if (value.startsWith(" ")) value = value.slice(1)

    if (field === "id") id = value
    else if (field === "event") event = value.length > 0 ? value : "message"
    else if (field === "data") dataLines.push(value)
  }

  if (dataLines.length === 0) return null
  return { id, event, data: dataLines.join("\n") }
}

function parseResetData(data: string): unknown {
  try {
    return JSON.parse(data)
  } catch {
    return data
  }
}

/**
 * Opens an authorized SSE stream over `fetch` (the browser EventSource cannot
 * send a Bearer header). It parses frames incrementally, dedupes by channel
 * seq, reconnects with a 1s→30s exponential backoff and gives up on a 401.
 *
 * Returns an idempotent cleanup function.
 */
export function connectSSE(options: ConnectSSEOptions): () => void {
  let finished = false
  let reconnectScheduled = false
  let attempt = 0
  let lastSeq = typeof options.lastEventId === "number" ? options.lastEventId : -1
  let controller: AbortController | null = null
  let retryTimer: ReturnType<typeof setTimeout> | null = null
  let silenceTimer: ReturnType<typeof setTimeout> | null = null

  function clearSilenceTimer(): void {
    if (silenceTimer !== null) {
      clearTimeout(silenceTimer)
      silenceTimer = null
    }
  }

  function clearRetryTimer(): void {
    if (retryTimer !== null) {
      clearTimeout(retryTimer)
      retryTimer = null
    }
  }

  function scheduleReconnect(): void {
    if (finished || reconnectScheduled) return
    reconnectScheduled = true
    clearSilenceTimer()
    controller?.abort()
    controller = null

    const delay = Math.min(BASE_RETRY_MS * 2 ** attempt, MAX_RETRY_MS)
    attempt += 1
    options.onStatus("reconnecting")
    retryTimer = setTimeout(() => {
      retryTimer = null
      reconnectScheduled = false
      void open()
    }, delay)
  }

  function armSilenceTimer(): void {
    clearSilenceTimer()
    silenceTimer = setTimeout(() => {
      silenceTimer = null
      if (finished) return
      scheduleReconnect()
    }, SSE_SILENCE_TIMEOUT_MS)
  }

  function dispatch(block: string): void {
    const frame = parseFrame(block)
    if (frame === null) return

    if (frame.event === "stream.reset") {
      options.onReset?.(parseResetData(frame.data))
      return
    }

    if (frame.id !== undefined && /^\d+$/.test(frame.id)) {
      const seq = Number(frame.id)
      if (seq <= lastSeq) return
      lastSeq = seq
    }

    options.onEvent({ id: frame.id, event: frame.event, data: frame.data })
  }

  async function readStream(body: ReadableStream<Uint8Array>): Promise<void> {
    const reader = body.getReader()
    const decoder = new TextDecoder()
    let buffer = ""

    try {
      for (;;) {
        const { done, value } = await reader.read()
        if (finished) return
        if (done) return
        if (value === undefined) continue

        armSilenceTimer()
        buffer += decoder.decode(value, { stream: true })
        buffer = buffer.replace(/\r\n/g, "\n")

        let boundary = buffer.indexOf("\n\n")
        while (boundary !== -1) {
          dispatch(buffer.slice(0, boundary))
          buffer = buffer.slice(boundary + 2)
          if (finished) return
          boundary = buffer.indexOf("\n\n")
        }
      }
    } catch {
      // A read error is handled by the caller as a normal disconnect: it either
      // reconnects with backoff or stops when the stream was torn down.
    }
  }

  async function open(): Promise<void> {
    if (finished) return
    options.onStatus("connecting")

    const current = new AbortController()
    controller = current

    const headers = new Headers({ Accept: "text/event-stream" })
    if (options.token.length > 0) headers.set("Authorization", `Bearer ${options.token}`)
    if (lastSeq >= 0) headers.set("Last-Event-ID", String(lastSeq))

    let response: Response
    try {
      response = await fetch(options.url, {
        method: "GET",
        headers,
        cache: "no-store",
        signal: current.signal,
      })
    } catch {
      if (finished) return
      scheduleReconnect()
      return
    }

    if (finished) return

    if (response.status === 401) {
      if (getToken() !== null) clearToken()
      finished = true
      controller = null
      options.onStatus("closed")
      return
    }

    if (!response.ok || response.body === null) {
      scheduleReconnect()
      return
    }

    options.onStatus("live")
    armSilenceTimer()
    await readStream(response.body)

    if (finished) return
    scheduleReconnect()
  }

  void open()

  return () => {
    if (finished) return
    finished = true
    clearSilenceTimer()
    clearRetryTimer()
    controller?.abort()
    controller = null
    options.onStatus("closed")
  }
}
