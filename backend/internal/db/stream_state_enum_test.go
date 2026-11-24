package db_test

// stream_state migration single-source gate (v0.2.4-D T5, constitution 五).
// The conversation_message.stream_state CHECK enum and column default are a
// persisted projection of the Go domain authority (version.MessageStreamStates /
// version.MessageStreamStateComplete). This gate parses the append-only migration
// SQL and asserts set equality with the authority — the expected values are
// DERIVED from the domain constants, never hand-copied, so a domain change fails
// here until the migration is updated (and vice versa). It runs without a
// database; the applied-constraint pin lives in the version infrastructure
// integration test (conversation_stream_integration_test.go).

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

const streamStateMigrationFile = "migrations/version_0018_v024_stream_state.sql"

var (
	streamStateCheckRE   = regexp.MustCompile(`(?s)stream_state\s+IN\s*\(([^)]*)\)`)
	streamStateDefaultRE = regexp.MustCompile(`stream_state[^,]*?DEFAULT\s+'([^']+)'`)
	quotedLiteralRE      = regexp.MustCompile(`'([^']+)'`)
)

func TestStreamStateMigrationCheckDerivesFromDomain(t *testing.T) {
	raw, err := os.ReadFile(streamStateMigrationFile)
	if err != nil {
		t.Fatalf("read %s: %v", streamStateMigrationFile, err)
	}
	sql := strings.ReplaceAll(string(raw), "\r\n", "\n")

	checkMatch := streamStateCheckRE.FindStringSubmatch(sql)
	if checkMatch == nil {
		t.Fatalf("%s has no stream_state IN (...) CHECK", streamStateMigrationFile)
	}
	var got []string
	for _, m := range quotedLiteralRE.FindAllStringSubmatch(checkMatch[1], -1) {
		got = append(got, m[1])
	}
	want := version.MessageStreamStates() // derived from the Go authority
	sort.Strings(got)
	wantSorted := append([]string(nil), want...)
	sort.Strings(wantSorted)
	if strings.Join(got, ",") != strings.Join(wantSorted, ",") {
		t.Fatalf("stream_state CHECK set = %v, want domain authority %v", got, wantSorted)
	}

	defaultMatch := streamStateDefaultRE.FindStringSubmatch(sql)
	if defaultMatch == nil {
		t.Fatalf("%s has no stream_state DEFAULT", streamStateMigrationFile)
	}
	if defaultMatch[1] != version.MessageStreamStateComplete {
		t.Fatalf("stream_state DEFAULT = %q, want domain complete %q",
			defaultMatch[1], version.MessageStreamStateComplete)
	}
}
