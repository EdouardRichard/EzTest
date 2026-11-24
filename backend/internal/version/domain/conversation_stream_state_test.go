package version_test

// stream_state single-source tests (v0.2.4-D T5, R-D2/SC-D2). The Go domain is
// the ONE authority for the conversation_message.stream_state enum; the DB CHECK
// and the migration SQL are checked projections of it (gates live in
// db/stream_state_enum_test.go and the version infrastructure integration test).

import (
	"testing"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// TestMessageStreamStateAuthority pins the canonical authority list itself, so
// an accidental omission/reorder in MessageStreamStates() is caught before the
// derived-copy gates run.
func TestMessageStreamStateAuthority(t *testing.T) {
	want := []string{
		version.MessageStreamStateStreaming,
		version.MessageStreamStateComplete,
		version.MessageStreamStateInterrupted,
	}
	got := version.MessageStreamStates()
	if len(got) != len(want) {
		t.Fatalf("MessageStreamStates() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("MessageStreamStates()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	// The wire values are frozen contract (OpenAPI stream_state enum).
	if version.MessageStreamStateStreaming != "streaming" ||
		version.MessageStreamStateComplete != "complete" ||
		version.MessageStreamStateInterrupted != "interrupted" {
		t.Fatalf("stream_state wire values drifted: %v", got)
	}
}

func TestValidateMessageStreamState(t *testing.T) {
	for _, state := range version.MessageStreamStates() {
		if err := version.ValidateMessageStreamState(state); err != nil {
			t.Errorf("ValidateMessageStreamState(%q) = %v, want nil", state, err)
		}
	}
	for _, bad := range []string{"", "running", "done", "STREAMING", "partial"} {
		if err := version.ValidateMessageStreamState(bad); err == nil {
			t.Errorf("ValidateMessageStreamState(%q) accepted a non-enum value", bad)
		}
	}
}

// TestNormalizeMessageStreamState pins the legacy empty default: an empty value
// (batch-path messages written before the column existed) normalizes to
// complete, never to streaming, so existing rows stay compatible.
func TestNormalizeMessageStreamState(t *testing.T) {
	if got := version.NormalizeMessageStreamState(""); got != version.MessageStreamStateComplete {
		t.Fatalf("NormalizeMessageStreamState(\"\") = %q, want complete", got)
	}
	for _, state := range version.MessageStreamStates() {
		if got := version.NormalizeMessageStreamState(state); got != state {
			t.Fatalf("NormalizeMessageStreamState(%q) = %q, want unchanged", state, got)
		}
	}
}
