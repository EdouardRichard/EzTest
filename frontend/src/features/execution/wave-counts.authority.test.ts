import { readFileSync } from "node:fs"

import { describe, expect, it } from "vitest"

import { COUNT_STATUS_KEYS } from "./wave-counts"
import {
  readWaveCountStatusKeys,
  waveCountJsonTagsFromSource,
  waveCountStatusKeysFromSource,
  waveCountsFieldLines,
  waveGoAuthorityPath,
} from "./wave-counts.authority"

// Pins the frontend count-key set to the Go authority by READING the real
// backend struct (constitution 五.5: consistency checked by CI, not memory). A
// hardcoded copy here would miss a backend addition just like the old inline
// tuple did; parsing wave.go means a new WaveCounts status field fails this test.
// The path resolves from the module's own location, so the gate does not depend
// on the runner's working dir. The reverse Go-side gate is owned by Lead.

/** Insert one extra WaveCounts field carrying `tag`, to simulate producer drift. */
function withExtraCountField(source: string, tag: string): string {
  return source.replace(
    /(type WaveCounts struct \{[\s\S]*?)(\n\})/,
    `$1\n\tExtra int \`json:"${tag}"\`$2`,
  )
}

/**
 * Insert one extra raw WaveCounts field LINE (verbatim), to simulate the five
 * producer shapes the JSON_TAG pattern cannot parse (H-C4). Unlike
 * withExtraCountField this does not wrap the payload in a valid `json:"..."`
 * tag, so it can express a tagless field, an embedded struct, or a malformed tag.
 */
function withExtraFieldLine(source: string, line: string): string {
  return source.replace(/(type WaveCounts struct \{[\s\S]*?)(\n\})/, `$1\n\t${line}$2`)
}

describe("wave-count status keys authority (execution/domain/wave.go)", () => {
  it("parses the real WaveCounts struct, aggregates included", () => {
    const tags = waveCountJsonTagsFromSource(readFileSync(waveGoAuthorityPath(), "utf8"))
    // Sanity: the parse really saw the aggregate fields it filters out.
    expect(tags).toContain("total")
    expect(tags).toContain("evidence")
    expect(tags).toContain("by_sut")
  })

  it("matches the backend WaveCounts status fields exactly (set equality)", () => {
    expect([...COUNT_STATUS_KEYS].sort()).toEqual(readWaveCountStatusKeys().sort())
  })

  it("has no duplicate count keys", () => {
    expect(new Set(COUNT_STATUS_KEYS).size).toBe(COUNT_STATUS_KEYS.length)
  })
})

// Mutation guards: the three producer shapes a bare-lowercase-only tag pattern
// silently missed (review I1). Each must change the parsed set so the set-
// equality gate above FAILS on that drift — a guard that passes here is fail-open.
describe("wave-count authority catches producer drift (review I1 mutations)", () => {
  const real = readFileSync(waveGoAuthorityPath(), "utf8")
  const baseline = [...COUNT_STATUS_KEYS].sort()

  it("catches a new ,omitempty count field (the house tag style)", () => {
    const parsed = waveCountStatusKeysFromSource(withExtraCountField(real, "queued,omitempty"))
    expect(parsed).toContain("queued")
    expect(parsed.sort()).not.toEqual(baseline)
  })

  it("catches a new digit-bearing count field", () => {
    const parsed = waveCountStatusKeysFromSource(withExtraCountField(real, "p95"))
    expect(parsed).toContain("p95")
    expect(parsed.sort()).not.toEqual(baseline)
  })

  it("catches a new uppercase count field", () => {
    const parsed = waveCountStatusKeysFromSource(withExtraCountField(real, "bySUT2"))
    expect(parsed).toContain("bySUT2")
    expect(parsed.sort()).not.toEqual(baseline)
  })
})

// H-C4 fail-closed coverage invariant: the JSON_TAG pattern is fail-OPEN on five
// producer shapes — each adds a real field line that yields ZERO parsed tags, so
// a new/renamed count key would be silently dropped from the derived set. The
// guard must assert parsed-tag count == non-comment field-line count and THROW on
// any mismatch. Each sample below is a real bad sample (真实坏样本): before the
// invariant the parse returns silently (these `.toThrow` assertions are RED);
// after it, each throws (GREEN). A guard that passes here without throwing is
// fail-open (born-green).
// T6 minor②: the field-line denominator must recognize Go BLOCK comments, not
// only // lines. A multi-line block comment must not count as field lines (it
// would break the coverage invariant), and a commented-out field inside a block
// comment must not contribute a json tag (it would leak a ghost count key). The
// real wave.go has no block comments today; these pin the parser's correctness
// if one is added.
describe("wave-count authority handles block comments (T6 minor②)", () => {
  const real = readFileSync(waveGoAuthorityPath(), "utf8")

  it("ignores a multi-line block comment (no phantom field lines)", () => {
    const source = withExtraFieldLine(real, "/*\n\t a multi-line explanation\n\t*/")
    expect(() => waveCountJsonTagsFromSource(source)).not.toThrow()
    expect(waveCountStatusKeysFromSource(source).sort()).toEqual(
      waveCountStatusKeysFromSource(real).sort(),
    )
  })

  it("does not leak a commented-out field's json tag", () => {
    const source = withExtraFieldLine(real, '/* Extra int `json:"ghost"` */')
    const tags = waveCountJsonTagsFromSource(source)
    expect(tags).not.toContain("ghost")
    expect(waveCountStatusKeysFromSource(source).sort()).toEqual(
      waveCountStatusKeysFromSource(real).sort(),
    )
  })

  it("parses a field carrying an inline block comment", () => {
    const source = withExtraFieldLine(real, 'Extra /* note */ int `json:"extra"`')
    expect(waveCountJsonTagsFromSource(source)).toContain("extra")
  })
})

describe("wave-count authority fails closed on unparseable tag shapes (H-C4)", () => {
  const real = readFileSync(waveGoAuthorityPath(), "utf8")

  it("invariant ④: parsed tag count == non-comment field-line count on the real authority", () => {
    const tags = waveCountJsonTagsFromSource(real)
    const fieldLines = waveCountsFieldLines(real)
    // Both sides derived from wave.go (not a hand-copied count); non-vacuous.
    expect(fieldLines.length).toBeGreaterThan(0)
    expect(tags.length).toBe(fieldLines.length)
  })

  it("shape 1: a field with no json tag", () => {
    const mutated = withExtraFieldLine(real, "Extra int")
    expect(() => waveCountJsonTagsFromSource(mutated)).toThrow(/coverage/i)
    expect(() => waveCountStatusKeysFromSource(mutated)).toThrow(/coverage/i)
  })

  it("shape 2: an embedded struct field (no tag)", () => {
    const mutated = withExtraFieldLine(real, "SUTCounts")
    expect(() => waveCountJsonTagsFromSource(mutated)).toThrow(/coverage/i)
    expect(() => waveCountStatusKeysFromSource(mutated)).toThrow(/coverage/i)
  })

  it("shape 3: a tag name containing '-' or '.'", () => {
    const dash = withExtraFieldLine(real, 'Extra int `json:"by-sut"`')
    const dot = withExtraFieldLine(real, 'Extra int `json:"foo.bar"`')
    expect(() => waveCountJsonTagsFromSource(dash)).toThrow(/coverage/i)
    expect(() => waveCountJsonTagsFromSource(dot)).toThrow(/coverage/i)
  })

  it("shape 4: a tag option containing uppercase", () => {
    const mutated = withExtraFieldLine(real, 'Extra int `json:"extra,String"`')
    expect(() => waveCountJsonTagsFromSource(mutated)).toThrow(/coverage/i)
    expect(() => waveCountStatusKeysFromSource(mutated)).toThrow(/coverage/i)
  })

  it("shape 5: whitespace inside the tag", () => {
    const mutated = withExtraFieldLine(real, 'Extra int `json:"extra, omitempty"`')
    expect(() => waveCountJsonTagsFromSource(mutated)).toThrow(/coverage/i)
    expect(() => waveCountStatusKeysFromSource(mutated)).toThrow(/coverage/i)
  })
})
