/**
 * Shared security and resource limits for the executor protocol (spec §5.6/§8).
 *
 * The executor is an internal service, but it is still the process that talks
 * to arbitrary (agent-chosen) URLs, so the allow list and the size caps are
 * enforced here rather than in the callers.
 */

import fsp from "node:fs/promises"

export const MAX_REQUEST_BYTES = 1024 * 1024
// Visual comparisons carry a baseline copy of up to 5 MiB (design spec §9.3),
// which is ~6.7 MiB once base64 encoded. Only the authenticated action route
// accepts this larger body; every other route keeps MAX_REQUEST_BYTES.
export const MAX_ACTION_REQUEST_BYTES = 16 * 1024 * 1024
export const MAX_ARTIFACT_BYTES = 10 * 1024 * 1024
export const MAX_SNAPSHOT_CHARS = 12_000
export const DEFAULT_ACTION_TIMEOUT_MS = 15_000
export const GOTO_TIMEOUT_MS = 30_000
export const MAX_ACTION_TIMEOUT_MS = 60_000
export const RING_CAPACITY = 500
export const DEFAULT_MAX_CONTEXTS = 8
export const MAX_SECRET_ENTRIES = 32
export const MAX_SECRET_LENGTH = 1024
// P3 extension (execution design §6.1): context metadata is a small object the
// API uses to correlate a browser context with its case_run/version_sut/role.
export const MAX_CONTEXT_METADATA_BYTES = 2048

/** Executor error with a protocol error code (see spec §5.6 for the list). */
export class ExecutorError extends Error {
  constructor(code, message, details) {
    super(message)
    this.name = "ExecutorError"
    this.code = code
    if (details !== undefined) {
      this.details = details
    }
  }
}

export function invalidArguments(message) {
  return new ExecutorError("invalid_arguments", message)
}

function normalizeHostPattern(input) {
  let value = String(input).trim().toLowerCase()
  if (value.length === 0) {
    return ""
  }
  if (value.includes("://")) {
    let parsed
    try {
      parsed = new URL(value)
    } catch {
      throw invalidArguments(`invalid host entry: ${input}`)
    }
    value = parsed.hostname
  }
  if (value.startsWith("[")) {
    // IPv6 literal: keep the brackets off, URL hostnames keep them.
    const end = value.indexOf("]")
    value = end >= 0 ? value.slice(1, end) : value.slice(1)
  } else {
    // Drop a trailing :port so callers may pass "127.0.0.1:13335".
    const colon = value.indexOf(":")
    if (colon >= 0 && value.indexOf(":", colon + 1) < 0) {
      value = value.slice(0, colon)
    }
  }
  return value.replace(/\.$/, "")
}

export function normalizeAllowedHosts(input) {
  if (input === undefined || input === null) {
    return []
  }
  if (!Array.isArray(input)) {
    throw invalidArguments("allowedHosts must be an array of host strings")
  }
  const hosts = []
  for (const entry of input) {
    if (typeof entry !== "string" || entry.trim().length === 0) {
      throw invalidArguments("allowedHosts entries must be non-empty strings")
    }
    const normalized = normalizeHostPattern(entry)
    if (normalized.length > 0) {
      hosts.push(normalized)
    }
  }
  return hosts
}

/** Exact host match, or `*.suffix` / `.suffix` matching subdomains only. */
export function isAllowedHost(host, allowedHosts = []) {
  if (typeof host !== "string" || host.length === 0) {
    return false
  }
  const target = normalizeHostPattern(host)
  if (!target) {
    return false
  }
  for (const entry of allowedHosts) {
    let pattern
    try {
      pattern = normalizeHostPattern(entry)
    } catch {
      continue
    }
    if (!pattern) {
      continue
    }
    if (pattern.startsWith("*.")) {
      const suffix = pattern.slice(2)
      // A wildcard covers subdomains only; list the apex explicitly to allow it.
      if (target.endsWith(`.${suffix}`)) {
        return true
      }
      continue
    }
    if (pattern.startsWith(".")) {
      const suffix = pattern.slice(1)
      if (target.endsWith(`.${suffix}`)) {
        return true
      }
      continue
    }
    if (target === pattern) {
      return true
    }
  }
  return false
}

/**
 * Registers a `fill(secret=true)` value for later scrubbing. Both bounds reject
 * instead of evicting: every value that was typed is either fully tracked or
 * the fill fails, so a previously registered secret can never silently fall out
 * of the scrub set and leak. Oversized values are rejected for the same reason
 * (a truncated secret would leak its tail).
 */
export function registerSecret(
  secrets,
  value,
  { maxEntries = MAX_SECRET_ENTRIES, maxLength = MAX_SECRET_LENGTH } = {},
) {
  if (typeof value !== "string" || value.length === 0) {
    return false
  }
  if (value.length > maxLength) {
    throw invalidArguments(`secret value exceeds the ${maxLength} character limit`)
  }
  if (secrets.has(value)) {
    return true
  }
  if (secrets.size >= maxEntries) {
    throw invalidArguments(`at most ${maxEntries} secret values are tracked per context`)
  }
  secrets.add(value)
  return true
}

export function hostFromUrl(rawUrl) {
  try {
    return new URL(String(rawUrl)).hostname.toLowerCase()
  } catch {
    return null
  }
}

/**
 * True when the URL may be requested by a context: http(s) and host allowed,
 * or a data:text/html page explicitly enabled for executor self-tests.
 */
export function isAllowedUrl(rawUrl, { allowedHosts = [], baseUrl, allowData = false } = {}) {
  let parsed
  try {
    parsed = new URL(String(rawUrl))
  } catch {
    throw invalidArguments("url must be an absolute URL")
  }
  if (parsed.protocol === "data:") {
    return allowData === true && /^data:text\/html/i.test(String(rawUrl))
  }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
    return false
  }
  const hosts = [...allowedHosts]
  if (typeof baseUrl === "string" && baseUrl.length > 0) {
    const baseHost = hostFromUrl(baseUrl)
    if (baseHost) {
      hosts.push(baseHost)
    }
  }
  return isAllowedHost(parsed.hostname, hosts)
}

/** Normalizes `timeout_ms` from a request, clamped to [1, 60s]. */
export function resolveTimeout(value, fallback) {
  const numeric = typeof value === "number" ? value : Number(value)
  if (!Number.isFinite(numeric)) {
    return Math.min(Math.max(Math.trunc(fallback), 1), MAX_ACTION_TIMEOUT_MS)
  }
  return Math.min(Math.max(Math.trunc(numeric), 1), MAX_ACTION_TIMEOUT_MS)
}

export function ensureArtifactSize(buffer, maxBytes = MAX_ARTIFACT_BYTES) {
  if (!Buffer.isBuffer(buffer)) {
    throw invalidArguments("artifact payload must be a Buffer")
  }
  if (buffer.length > maxBytes) {
    throw new ExecutorError(
      "too_large",
      `artifact is ${buffer.length} bytes, exceeding the ${maxBytes} byte limit`,
    )
  }
  return buffer
}

/**
 * Checks a downloaded file's size with `stat` before it is read into memory.
 * Downloads are written to a temp file by Playwright, so this rejects oversized
 * artifacts without buffering them. (Screenshots are already in memory when the
 * browser returns them, so those use `ensureArtifactSize` after capture.)
 */
export async function ensureArtifactFileSize(filePath, maxBytes = MAX_ARTIFACT_BYTES) {
  const stat = await fsp.stat(filePath)
  if (!stat.isFile()) {
    throw invalidArguments("artifact path must be a regular file")
  }
  if (stat.size > maxBytes) {
    throw new ExecutorError(
      "too_large",
      `artifact is ${stat.size} bytes, exceeding the ${maxBytes} byte limit`,
    )
  }
  return stat.size
}

/** Adds an item to a fixed-size ring buffer (oldest entries dropped). */
export function pushRing(ring, item, capacity = RING_CAPACITY) {
  ring.push(item)
  if (ring.length > capacity) {
    ring.splice(0, ring.length - capacity)
  }
  return item
}
