/**
 * ARIA snapshot helpers (spec §8.2): Playwright ariaSnapshot YAML, password
 * value stripping, and the 12KB protocol truncation.
 */

import { MAX_SNAPSHOT_CHARS } from "./security.mjs"

export const DEFAULT_MAX_SNAPSHOT_CHARS = MAX_SNAPSHOT_CHARS

const MAX_SNAPSHOT_BYTES = 12 * 1024

function trimToBytes(text, maxBytes) {
  const bytes = Buffer.from(text, "utf8")
  if (bytes.length <= maxBytes) {
    return text
  }
  let end = maxBytes
  // Never cut inside a UTF-8 sequence: back up to the previous start byte.
  while (end > 0 && (bytes[end] & 0xc0) === 0x80) {
    end -= 1
  }
  return bytes.subarray(0, end).toString("utf8")
}

export function truncateSnapshot(text, maxChars = DEFAULT_MAX_SNAPSHOT_CHARS) {
  const source = typeof text === "string" ? text : String(text ?? "")
  const requested = Number(maxChars)
  const limit = Math.max(
    1,
    Math.min(
      Number.isFinite(requested) ? Math.trunc(requested) : DEFAULT_MAX_SNAPSHOT_CHARS,
      DEFAULT_MAX_SNAPSHOT_CHARS,
    ),
  )
  if (source.length <= limit) {
    if (Buffer.byteLength(source, "utf8") <= MAX_SNAPSHOT_BYTES) {
      return { text: source, truncated: false }
    }
    return { text: trimToBytes(source, MAX_SNAPSHOT_BYTES), truncated: true }
  }
  return {
    text: trimToBytes(source.slice(0, limit), MAX_SNAPSHOT_BYTES),
    truncated: true,
  }
}

/** Replaces every occurrence of a known secret value with `***`. */
export function stripSecretValues(text, secrets = []) {
  let output = typeof text === "string" ? text : String(text ?? "")
  for (const secret of secrets) {
    if (typeof secret !== "string" || secret.length === 0) {
      continue
    }
    output = output.split(secret).join("***")
  }
  return output
}

/**
 * Captures `page.locator('body').ariaSnapshot()` and removes password values.
 * Values typed into `fill(secret=true)` are tracked by the context and stripped
 * by the caller-provided `secrets` list.
 */
export async function captureSnapshot(page, { maxChars = DEFAULT_MAX_SNAPSHOT_CHARS, secrets = [] } = {}) {
  const title = await page.title()
  const secretValues = new Set(
    (Array.isArray(secrets) ? secrets : []).filter(
      (value) => typeof value === "string" && value.length > 0,
    ),
  )
  try {
    const passwordValues = await page.$$eval('input[type="password"]', (elements) =>
      elements.map((element) => element.value).filter((value) => typeof value === "string" && value.length > 0),
    )
    for (const value of passwordValues) {
      secretValues.add(value)
    }
  } catch {
    // A page without a document (about:blank) has no password inputs.
  }
  const raw = await page.locator("body").ariaSnapshot()
  const masked = stripSecretValues(raw, [...secretValues])
  const { text, truncated } = truncateSnapshot(masked, maxChars)
  return {
    url: stripSecretValues(page.url(), [...secretValues]),
    title: stripSecretValues(title, [...secretValues]),
    aria: text,
    truncated,
  }
}
