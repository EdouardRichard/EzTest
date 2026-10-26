import { clearSession } from "./auth-storage"

/** One dispatched Server-Sent Event frame. */
export type SSEEvent = {
  /** The frame's `id` field (the channel seq for persisted events). */
  id?: string
  /** The frame's `event` field; `message` when the server omitted it. */
  event: string
  /** The frame's `data` field, with multiple data lines joined by `\n`. */
  data: string
}

/** Connection state exposed to the UI. */
export type SSEStatus = "connecting" | "live" | "reconnecting" | "closed"

export type ConnectSSEOptions = {
  /** Absolute stream URL, normally resolved with `resolveApiUrl`. */
  url: string
  /** Bearer token. SSE uses fetch, never EventSource/Cookie auth. */
  token: string
  /** Optional initial cursor; sent as Last-Event-ID on the first request. */
  lastEventId?: number
  onEvent: (event: SSEEvent) => void
  onStatus: (status: SSEStatus) => void
  /**
   * Called for the synthetic `stream.reset` control frame. The frame is not a
   * persisted event: it carries no id, is not delivered to onEvent, and never
   * advances lastSeq. The caller should refetch its snapshot.
   */
  onReset?: (data: unknown) => void
}

/** The stream is silent for this long, so the connection is considered dead. */
const NO_BYTE_TIMEOUT_MS = 45_000
/** First reconnect delay; doubles per consecutive failure. */
const BASE_RETRY_DELAY_MS = 1_000
/** Reconnect delay ceiling from the design. */
const MAX_RETRY_DELAY_MS = 30_000
/** Extra randomized delay (0-999ms) added to every reconnect attempt. */
const RETRY_JITTER_MS = 1_000

type ParsedFrame = {
  id?: string
  event: string
  data: string
}

type FrameParser = {
  push: (chunk: string) => void
}

/**
 * Incremental SSE line parser. It follows the text/event-stream framing rules
 * the API uses: colon-prefixed lines are comments, blank lines dispatch the
 * pending frame, and repeated `data:` lines are joined with newlines.
 */
function createFrameParser(dispatch: (frame: ParsedFrame) => void): FrameParser {
  let buffer = ""
  let id: string | undefined
  let event: string | undefined
  const data: string[] = []

  const reset = () => {
    id = undefined
    event = undefined
    data.length = 0
  }

  const flush = () => {
    if (id === undefined && event === undefined && data.length === 0) return
    const frame: ParsedFrame = {
      id,
      event: event ?? "message",
      data: data.join("\n"),
    }
    reset()
    dispatch(frame)
  }

  const processLine = (rawLine: string) => {
    const line = rawLine.endsWith("\r") ? rawLine.slice(0, -1) : rawLine
    if (line === "") {
      flush()
      return
    }
    if (line.startsWith(":")) return
    const colon = line.indexOf(":")
    if (colon < 0) return
    const field = line.slice(0, colon)
    let value = line.slice(colon + 1)
    if (value.startsWith(" ")) value = value.slice(1)
    if (field === "id") id = value === "" ? undefined : value
    else if (field === "event") event = value
    else if (field === "data") data.push(value)
  }

  return {
    push(chunk) {
      buffer += chunk
      let newline = buffer.indexOf("\n")
      while (newline >= 0) {
        const line = buffer.slice(0, newline)
        buffer = buffer.slice(newline + 1)
        processLine(line)
        newline = buffer.indexOf("\n")
      }
    },
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}

/** Reads the seq from the event payload, falling back to the numeric id. */
function readSeq(frame: ParsedFrame): number | undefined {
  if (frame.data.length > 0) {
    try {
      const payload: unknown = JSON.parse(frame.data)
      if (isRecord(payload) && typeof payload.seq === "number" && Number.isFinite(payload.seq)) {
        return payload.seq
      }
    } catch {
      // A non-JSON payload is still delivered; only the numeric id can dedupe it.
    }
  }
  if (frame.id !== undefined) {
    const seq = Number(frame.id)
    if (Number.isFinite(seq)) return seq
  }
  return undefined
}

/**
 * Opens the SSE stream with `fetch` (never EventSource, which cannot attach the
 * Authorization header), parses frames incrementally, dedupes persisted events
 * by seq, and reconnects with Last-Event-ID using 1s→30s exponential backoff
 * plus jitter. A 45s byte-silence timeout and a 401 both end the connection;
 * 401 also clears the local session and never retries.
 *
 * Returns a cleanup function that aborts the request, cancels reconnects, and
 * reports `closed`.
 */
export function connectSSE(options: ConnectSSEOptions): () => void {
  let stopped = false
  let status: SSEStatus | null = null
  let lastSeq = options.lastEventId
  let attempt = 0
  let retryTimer: ReturnType<typeof setTimeout> | null = null
  let controller: AbortController | null = null
  let reader: ReadableStreamDefaultReader<Uint8Array> | null = null

  const emitStatus = (next: SSEStatus) => {
    if (status === next) return
    status = next
    options.onStatus(next)
  }

  const scheduleReconnect = () => {
    if (stopped) return
    emitStatus("reconnecting")
    const delay = Math.min(MAX_RETRY_DELAY_MS, BASE_RETRY_DELAY_MS * 2 ** attempt)
    attempt += 1
    const jitter = Math.floor(Math.random() * RETRY_JITTER_MS)
    retryTimer = setTimeout(() => {
      retryTimer = null
      void open()
    }, delay + jitter)
  }

  const handleFrame = (frame: ParsedFrame) => {
    if (stopped) return
    if (frame.event === "stream.reset") {
      let data: unknown = frame.data
      if (frame.data.length > 0) {
        try {
          data = JSON.parse(frame.data)
        } catch {
          data = frame.data
        }
      }
      options.onReset?.(data)
      return
    }

    const seq = readSeq(frame)
    if (seq !== undefined) {
      if (lastSeq !== undefined && seq <= lastSeq) return
      lastSeq = seq
    }
    options.onEvent({ id: frame.id, event: frame.event, data: frame.data })
  }

  const open = async (): Promise<void> => {
    if (stopped) return
    const requestController = new AbortController()
    controller = requestController

    try {
      const headers: Record<string, string> = {
        Accept: "text/event-stream",
        "Cache-Control": "no-cache",
        Authorization: `Bearer ${options.token}`,
      }
      if (lastSeq !== undefined) headers["Last-Event-ID"] = String(lastSeq)

      const response = await fetch(options.url, {
        method: "GET",
        headers,
        signal: requestController.signal,
        cache: "no-store",
      })

      if (stopped) {
        void response.body?.cancel()
        return
      }
      if (response.status === 401) {
        // The reconnect would present the same invalid token: stop for good and
        // drop the local session so the protected layout redirects to /login.
        stopped = true
        emitStatus("closed")
        clearSession()
        void response.body?.cancel()
        return
      }
      if (!response.ok || !response.body) {
        void response.body?.cancel()
        scheduleReconnect()
        return
      }

      attempt = 0
      emitStatus("live")
      const streamReader = response.body.getReader()
      reader = streamReader
      const decoder = new TextDecoder()
      const parser = createFrameParser(handleFrame)
      let silenceTimer: ReturnType<typeof setTimeout> | null = null

      const armSilenceTimer = () => {
        if (silenceTimer !== null) clearTimeout(silenceTimer)
        silenceTimer = setTimeout(() => {
          // No bytes for 45s: cancel the body and abort the request. Either one
          // ends reader.read(); the loop below then schedules a reconnect.
          void streamReader.cancel().catch(() => {})
          requestController.abort()
        }, NO_BYTE_TIMEOUT_MS)
      }

      armSilenceTimer()
      try {
        while (!stopped) {
          const { done, value } = await streamReader.read()
          if (done) break
          armSilenceTimer()
          parser.push(decoder.decode(value, { stream: true }))
        }
      } finally {
        if (silenceTimer !== null) clearTimeout(silenceTimer)
        if (reader === streamReader) reader = null
      }

      if (stopped) return
      scheduleReconnect()
    } catch {
      if (!stopped) scheduleReconnect()
    } finally {
      if (controller === requestController) controller = null
    }
  }

  emitStatus("connecting")
  void open()

  return () => {
    if (stopped) return
    stopped = true
    if (retryTimer !== null) {
      clearTimeout(retryTimer)
      retryTimer = null
    }
    if (reader !== null) {
      void reader.cancel().catch(() => {})
      reader = null
    }
    controller?.abort()
    controller = null
    emitStatus("closed")
  }
}

export type ConsumeSSEOptions = {
  onEvent: (event: SSEEvent) => void
  signal?: AbortSignal
}

/**
 * Reads an already-open SSE response once (used by the LLM streaming test,
 * whose normalized frames are llm.delta / llm.done / llm.error). It shares the
 * same framing parser as connectSSE but does not dedupe or reconnect.
 */
export async function consumeSSEStream(
  response: Response,
  options: ConsumeSSEOptions,
): Promise<void> {
  if (!response.body) throw new Error("SSE response has no body")
  const streamReader = response.body.getReader()
  const decoder = new TextDecoder()
  const parser = createFrameParser((frame) => {
    options.onEvent({ id: frame.id, event: frame.event, data: frame.data })
  })
  const cancel = () => {
    void streamReader.cancel().catch(() => {})
  }
  options.signal?.addEventListener("abort", cancel)
  try {
    while (true) {
      const { done, value } = await streamReader.read()
      if (done) break
      parser.push(decoder.decode(value, { stream: true }))
    }
  } catch (error) {
    cancel()
    throw error
  } finally {
    options.signal?.removeEventListener("abort", cancel)
  }
}
