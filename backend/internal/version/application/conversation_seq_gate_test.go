package versionapp

// v0.2.4-H H-E19: cross-language behaviour gate for the conversation streaming
// seq assignment. The server assigns a 1-based seq, in part order, ONLY to an
// assistant message's parts whose type is in the one streaming enum; snapshot-only
// types (image/rollback_proposal) stay unsequenced. This rule lives in
// publishMessageEvent (:1137-1144) and assistantStreamParts (:1180-1200).
//
// There is no Go->TS codegen pipeline in this worktree, so the single-source
// mechanism is the one the enum unions already use: a SHARED, checked case set
// (testdata/conversation_seq_gate.json). This test evaluates the Go AUTHORITY
// against it; frontend/src/features/clarification/conversation-seq.authority.test.ts
// evaluates the TypeScript mirror against the SAME file. A semantic drift on
// either side fails its own gate instead of silently diverging.

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// gateMinCases is an anti-vacuity floor: an empty or trimmed fixture must fail
// loudly, never pass because there is nothing to check.
const gateMinCases = 6

type seqGatePart struct {
	Type string `json:"type"`
}

type seqGateCase struct {
	Name         string        `json:"name"`
	Role         string        `json:"role"`
	Parts        []seqGatePart `json:"parts"`
	ExpectedSeqs []*int        `json:"expected_seqs"`
}

type seqGateFixture struct {
	Cases []seqGateCase `json:"cases"`
}

func loadConversationSeqGate(t *testing.T) seqGateFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/conversation_seq_gate.json")
	if err != nil {
		t.Fatalf("read shared conversation seq gate fixture: %v", err)
	}
	var fixture seqGateFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decode shared conversation seq gate fixture: %v", err)
	}
	if len(fixture.Cases) < gateMinCases {
		t.Fatalf("shared seq gate fixture has %d cases, want >= %d (would pass vacuously)",
			len(fixture.Cases), gateMinCases)
	}
	var sawSnapshotOnly, sawNonAssistant bool
	for _, tc := range fixture.Cases {
		if len(tc.Parts) != len(tc.ExpectedSeqs) {
			t.Fatalf("case %q: %d parts but %d expected_seqs (must be equal length)",
				tc.Name, len(tc.Parts), len(tc.ExpectedSeqs))
		}
		if tc.Role != version.MessageRoleAssistant {
			sawNonAssistant = true
		}
		for _, part := range tc.Parts {
			if !version.IsStreamingMessagePartType(part.Type) {
				sawSnapshotOnly = true
			}
		}
	}
	if !sawSnapshotOnly {
		t.Fatal("shared seq gate fixture has no snapshot-only part type; the gate would miss the rule")
	}
	if !sawNonAssistant {
		t.Fatal("shared seq gate fixture has no non-assistant role; the gate would miss the role gate")
	}
	return fixture
}

func gateMessageParts(parts []seqGatePart) []version.MessagePart {
	out := make([]version.MessagePart, len(parts))
	for i, part := range parts {
		out[i] = version.MessagePart{Type: part.Type}
	}
	return out
}

// TestConversationSeqGateStreamParts verifies the DB-writing authority
// (assistantStreamParts) assigns the fixture's expected 1-based seqs to exactly
// the sequenced parts, in part order.
func TestConversationSeqGateStreamParts(t *testing.T) {
	fixture := loadConversationSeqGate(t)
	now := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			parts := gateMessageParts(tc.Parts)
			// A non-assistant message never streams (the role gate in
			// publishMessageEvent); assistantStreamParts is only ever called for an
			// assistant head, so evaluate it for assistant cases only.
			if tc.Role == version.MessageRoleAssistant {
				rows, err := assistantStreamParts(parts, now)
				if err != nil {
					t.Fatalf("assistantStreamParts: %v", err)
				}
				var wantSeqs []int
				var wantTypes []string
				for i, expected := range tc.ExpectedSeqs {
					if expected != nil {
						wantSeqs = append(wantSeqs, *expected)
						wantTypes = append(wantTypes, tc.Parts[i].Type)
					}
				}
				if len(rows) != len(wantSeqs) {
					t.Fatalf("assistantStreamParts produced %d rows, want %d sequenced parts",
						len(rows), len(wantSeqs))
				}
				for i, row := range rows {
					if row.Seq != wantSeqs[i] || row.Type != wantTypes[i] {
						t.Fatalf("row %d = (seq %d, type %q), want (seq %d, type %q)",
							i, row.Seq, row.Type, wantSeqs[i], wantTypes[i])
					}
				}
			}
			// Tie the fixture to the ONE predicate BOTH producers call. The
			// publishMessageEvent loop condition is literally
			// `message.Role == version.MessageRoleAssistant &&
			// version.IsStreamingMessagePartType(part.Type)`; asserting the same
			// conjunction here (never a re-typed type list) proves the fixture —
			// and therefore the frontend mirror — is anchored to that authority for
			// both the role gate and the vocabulary.
			for i, part := range parts {
				wantSequenced := tc.Role == version.MessageRoleAssistant &&
					version.IsStreamingMessagePartType(part.Type)
				gotSequenced := tc.ExpectedSeqs[i] != nil
				if wantSequenced != gotSequenced {
					t.Fatalf("part %d (type %q, role %q): fixture expects sequenced=%v, authority says %v",
						i, part.Type, tc.Role, gotSequenced, wantSequenced)
				}
			}
		})
	}
}
