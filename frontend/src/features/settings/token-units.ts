/**
 * Token display/input helpers shared by the LLM model dialogs.
 *
 * The v0.22 convention (A §4.1) is 1K = 1000 tokens for both display and input;
 * the API and storage always use raw tokens. Keeping the conversion here avoids
 * the add/edit dialogs drifting apart again.
 */
export const TOKENS_PER_K = 1000
export const MAX_MODEL_TOKENS = 10_000_000
const MAX_K_DECIMALS = 3

/** Renders raw tokens as K with at most 3 decimals (128000 -> "128"). */
export function formatTokensK(tokens: number): string {
  return Number.parseFloat((tokens / TOKENS_PER_K).toFixed(MAX_K_DECIMALS)).toString()
}

/** Row label: 0 means auto/unknown and is called out explicitly. */
export function formatTokensKWithAuto(tokens: number): string {
  return tokens === 0 ? "0K（自动）" : `${formatTokensK(tokens)}K`
}

export type ParsedK = { ok: true; tokens: number } | { ok: false; error: string }

/**
 * Parses a K input into raw tokens with an exact integer conversion. The
 * backend remains authoritative; this only keeps obvious mistakes from being
 * sent and matches the server's 0 ≤ W ≤ 10,000,000 / O ≤ W rules.
 */
export function parseKInput(text: string, label: string): ParsedK {
  const trimmed = text.trim()
  if (trimmed.length === 0 || !/^\d+(\.\d{1,3})?$/.test(trimmed)) {
    return { ok: false, error: `${label}必须是 0 或更大的数字（最多 3 位小数）` }
  }
  const tokens = Math.round(Number(trimmed) * TOKENS_PER_K)
  if (!Number.isFinite(tokens) || tokens < 0) {
    return { ok: false, error: `${label}必须是 0 或更大的数字（最多 3 位小数）` }
  }
  if (tokens > MAX_MODEL_TOKENS) {
    return { ok: false, error: `${label}不能超过 ${MAX_MODEL_TOKENS / TOKENS_PER_K}K` }
  }
  return { ok: true, tokens }
}
