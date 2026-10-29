/**
 * P8 executor limits and allow lists (design spec §10.5).
 *
 * The executor-wide allow list bounds the hosts a browser context may open a
 * WebSocket to. It defaults to loopback so an executor that is reachable by the
 * API cannot be used as a generic WebSocket proxy; deployments that test a
 * remote target add its hosts to EZTEST_EXEC_ALLOWED_HOSTS. A context's own
 * allowedHosts stays authoritative too: a WebSocket must pass both lists.
 */

import { invalidArguments, isAllowedHost, normalizeAllowedHosts } from "./security.mjs"

/** Loopback-only default of EZTEST_EXEC_ALLOWED_HOSTS. */
export const DEFAULT_ALLOWED_HOSTS = Object.freeze(["localhost", "127.0.0.1"])

/**
 * Parses EZTEST_EXEC_ALLOWED_HOSTS (comma-separated host entries). An unset or
 * empty value selects the loopback default; an explicit but empty list is a
 * startup error so a typo cannot silently disable remote WebSocket testing.
 */
export function parseAllowedHosts(raw) {
  const value = typeof raw === "string" ? raw.trim() : ""
  if (value.length === 0) {
    return [...DEFAULT_ALLOWED_HOSTS]
  }
  const entries = value
    .split(",")
    .map((entry) => entry.trim())
    .filter((entry) => entry.length > 0)
  if (entries.length === 0) {
    throw invalidArguments("EZTEST_EXEC_ALLOWED_HOSTS must contain at least one host")
  }
  return normalizeAllowedHosts(entries)
}

/**
 * Reports whether a browser context may open a WebSocket to url. Both the
 * context allow list and the executor-wide allow list must contain the host;
 * only the ws/wss schemes are valid.
 */
export function isWebSocketAllowed(url, contextAllowedHosts = [], executorAllowedHosts = DEFAULT_ALLOWED_HOSTS) {
  let parsed
  try {
    parsed = new URL(String(url))
  } catch {
    return false
  }
  if (parsed.protocol !== "ws:" && parsed.protocol !== "wss:") {
    return false
  }
  const host = parsed.hostname
  if (host.length === 0) {
    return false
  }
  return (
    isAllowedHost(host, Array.isArray(contextAllowedHosts) ? contextAllowedHosts : []) &&
    isAllowedHost(host, Array.isArray(executorAllowedHosts) ? executorAllowedHosts : [])
  )
}
