import { readFileSync } from "node:fs"
import { dirname, resolve } from "node:path"
import { fileURLToPath } from "node:url"

/**
 * Derivation of the frontend wave-count status keys from the Go authority
 * `backend/internal/execution/domain/wave.go` (`WaveCounts`). Kept as pure
 * functions over source text so the parser is unit-testable against mutated
 * producer shapes (see wave-counts.authority.test.ts), not just the live file.
 */

const WAVE_COUNTS_STRUCT = /type WaveCounts struct \{([\s\S]*?)\n\}/

/**
 * A Go `json` tag: a name of letters/digits/underscore in ANY case, followed by
 * zero or more comma options (`,omitempty`, `,string`, ...). Matching the NAME
 * regardless of case/digits/options is what keeps the guard from failing open:
 * the house style is `,omitempty` (248 such tags under backend/internal), and a
 * bare-lowercase-only pattern would silently miss a new `queued,omitempty`,
 * `p95` or `bySUT2` count key — the exact drift this gate exists to catch.
 */
const JSON_TAG = /`json:"([A-Za-z_0-9]+)(?:,[a-z]+)*"`/g

/**
 * Absolute path to the Go authority, resolved from THIS module's own location
 * (cwd-independent, like the Go enum_authority_test.go that walks up from its
 * own file). We use `fileURLToPath(import.meta.url)` rather than
 * `new URL(rel, import.meta.url)`. Verified cause (doc 19 §4 invariant ⑬): the
 * `new URL(<literal>, import.meta.url)` SYNTAX is rewritten at build time by
 * Vite's `asset-import-meta-url` transform and vitest's `normalize-url`; the
 * latter only fires in non-client environments, and this project runs jsdom, so
 * it does not fire here. `import.meta.url` itself is a `file://` URL under jsdom
 * (observed, not the document base), so `fileURLToPath` reads it directly without
 * depending on those transforms.
 */
export function waveGoAuthorityPath(): string {
  // here = frontend/src/features/execution → four levels up = repo root.
  const here = dirname(fileURLToPath(import.meta.url))
  return resolve(here, "../../../../backend/internal/execution/domain/wave.go")
}

function waveCountsBody(source: string): string {
  const struct = source.match(WAVE_COUNTS_STRUCT)
  if (struct === null) {
    throw new Error("WaveCounts struct not found in wave.go — authority moved or renamed")
  }
  return struct[1]
}

/**
 * Strip Go block comments (/* ... *\/, including multi-line) from a struct body.
 * fieldLinesOfBody only excludes // lines; without this a block comment would be
 * counted as field lines (breaking the coverage invariant) or a commented-out
 * field's json tag would leak into the derived set (T6 minor②). The real wave.go
 * carries no block comments today; this keeps the parser correct if one is added.
 *
 * Known limitation (review fix-round M6, disclosed): deleting a CROSS-LINE block
 * comment that sits BETWEEN two field lines joins the surrounding text into one
 * line, so a field line can lose its line boundary and the tag-count == line-count
 * invariant would mis-fire RED. This is fail-CLOSED (a false alarm, never a silent
 * miss), and the current wave.go has no such shape; a future cross-line comment
 * between fields would need a line-aware stripper rather than this regex.
 */
function stripBlockComments(body: string): string {
  return body.replace(/\/\*[\s\S]*?\*\//g, "")
}

/**
 * The non-comment, non-blank field lines of a WaveCounts struct body. Go struct
 * fields are one per line (house style); a `// ...` comment line or a blank line
 * carries no field. This is the denominator of the fail-closed coverage invariant
 * below: every such line MUST yield exactly one parsed json tag.
 */
function fieldLinesOfBody(body: string): string[] {
  return body
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line.length > 0 && !line.startsWith("//"))
}

/**
 * Non-comment field lines of WaveCounts, derived from the Go authority source.
 * Exported so the test can assert the coverage invariant (tag count == field-line
 * count) directly against the real struct, not just via throw/no-throw.
 */
export function waveCountsFieldLines(source: string): string[] {
  return fieldLinesOfBody(stripBlockComments(waveCountsBody(source)))
}

/**
 * Every `json` tag name in WaveCounts, before the aggregate exclusion.
 *
 * FAIL-CLOSED coverage invariant (H-C4, constitution 五.5/七.6): the number of
 * parsed tags MUST equal the number of non-comment field lines. The JSON_TAG
 * pattern alone is fail-OPEN on five producer shapes — a field with no tag, an
 * embedded struct, a name containing `-`/`.`, an option with uppercase, or
 * whitespace inside the tag — each yields ZERO tags for a real field line, so a
 * new/renamed count key would be silently dropped from the derived set (the exact
 * drift this gate exists to catch). Asserting tag count == field line count turns
 * all five into a hard parse failure instead of a silent miss. The five mutation
 * samples in wave-counts.authority.test.ts pin this (each must throw).
 */
export function waveCountJsonTagsFromSource(source: string): string[] {
  const body = stripBlockComments(waveCountsBody(source))
  const tags = [...body.matchAll(JSON_TAG)].map((match) => match[1])
  const fieldLines = fieldLinesOfBody(body)
  if (tags.length !== fieldLines.length) {
    throw new Error(
      `WaveCounts tag coverage is fail-open: parsed ${tags.length} json tags but ${fieldLines.length} non-comment field lines. ` +
        "A field lost its tag (missing tag / embedded struct / name with '-' or '.' / uppercase option / whitespace in tag) — " +
        "the JSON_TAG pattern cannot derive it, so the count-key set would silently drift.",
    )
  }
  return tags
}

/**
 * The per-status count keys: every WaveCounts field except the `total`/`evidence`
 * scalar aggregates and the `by_sut` per-SUT map. The exclusion list fails closed
 * — an unexpected/renamed aggregate is treated as a status key and breaks set
 * equality rather than being silently dropped.
 */
export function waveCountStatusKeysFromSource(source: string): string[] {
  return waveCountJsonTagsFromSource(source).filter(
    (tag) => tag !== "total" && tag !== "evidence" && tag !== "by_sut",
  )
}

/** Read the live Go authority and derive its per-status count keys. */
export function readWaveCountStatusKeys(): string[] {
  return waveCountStatusKeysFromSource(readFileSync(waveGoAuthorityPath(), "utf8"))
}
