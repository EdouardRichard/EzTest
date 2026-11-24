// Package textunit provides shared text-measurement primitives for the
// application layers of multiple bounded contexts. It is a platform shared
// kernel (constitution 五.1: one authority, every other representation derived),
// not a bounded context and not domain vocabulary.
package textunit

import "unicode/utf16"

// UTF16Length returns the number of UTF-16 code units of s. It is the ONE
// authority for the streaming append offset used by both the agent transcript
// (agent/application/transcript_stream.go) and the version conversation
// (version/application/conversation_stream.go): the offset matches JavaScript
// String.length so the frontend's offset-based replay dedup is exact — a BMP
// rune counts 1, an astral rune (e.g. "😀") counts 2, never the rune count (1)
// nor the byte count (4).
//
// Cross-context pure-function reuse is ratified under R-H9 (no entity state, no
// side effects, single source); see docs/design/architecture-constraints.md
// §5.1 and its ratification register (v0.2.4-H T14). The two consumers live in
// different contexts' application layers, which must not import each other
// (AGENTS §2.1), so the shared kernel — not either context's domain — is the
// correct home.
func UTF16Length(s string) int {
	return len(utf16.Encode([]rune(s)))
}
