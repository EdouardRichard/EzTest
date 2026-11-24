package db_test

// v0.2.4-D T8 stream_state single-source gate for agent_message (constitution
// 五). The agent transcript reuses the ONE stream-state authority of the project
// (version.MessageStreamStates); agent.MessageStreamStates() is a derived
// projection of it, and the agent_message CHECK/default are a persisted
// projection of the same authority. This gate parses the append-only migration
// SQL and asserts set equality with the authority — nothing here is hand-copied,
// so a change on either side fails until the other follows. It runs without a
// database; the applied-constraint pin lives in the agent infrastructure
// integration test.

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

const agentStreamStateMigrationFile = "migrations/agent_0012_v024_stream_state.sql"

var (
	agentStreamStateCheckRE   = regexp.MustCompile(`(?s)stream_state\s+IN\s*\(([^)]*)\)`)
	agentStreamStateDefaultRE = regexp.MustCompile(`stream_state[^,]*?DEFAULT\s+'([^']+)'`)
)

func TestAgentMessageStreamStateMigrationDerivesFromAuthority(t *testing.T) {
	raw, err := os.ReadFile(agentStreamStateMigrationFile)
	if err != nil {
		t.Fatalf("read %s: %v", agentStreamStateMigrationFile, err)
	}
	sql := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if !strings.Contains(sql, "ALTER TABLE agent_message") {
		t.Fatalf("%s does not alter agent_message", agentStreamStateMigrationFile)
	}

	checkMatch := agentStreamStateCheckRE.FindStringSubmatch(sql)
	if checkMatch == nil {
		t.Fatalf("%s has no stream_state IN (...) CHECK", agentStreamStateMigrationFile)
	}
	var got []string
	for _, m := range quotedLiteralRE.FindAllStringSubmatch(checkMatch[1], -1) {
		got = append(got, m[1])
	}
	want := append([]string(nil), version.MessageStreamStates()...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("agent_message stream_state CHECK set = %v, want authority %v", got, want)
	}

	defaultMatch := agentStreamStateDefaultRE.FindStringSubmatch(sql)
	if defaultMatch == nil {
		t.Fatalf("%s has no stream_state DEFAULT", agentStreamStateMigrationFile)
	}
	if defaultMatch[1] != version.MessageStreamStateComplete {
		t.Fatalf("stream_state DEFAULT = %q, want the authority's complete %q",
			defaultMatch[1], version.MessageStreamStateComplete)
	}
}

// TestAgentStreamStatesAreTheVersionAuthority pins the derivation itself: the
// agent context must not grow a second hand-written enum. If someone replaces
// the derived list with a literal copy that drifts, this fails.
func TestAgentStreamStatesAreTheVersionAuthority(t *testing.T) {
	got := append([]string(nil), agent.MessageStreamStates()...)
	want := append([]string(nil), version.MessageStreamStates()...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("agent.MessageStreamStates() = %v, want version authority %v", got, want)
	}
	if agent.MessageStreamStateStreaming != version.MessageStreamStateStreaming ||
		agent.MessageStreamStateComplete != version.MessageStreamStateComplete ||
		agent.MessageStreamStateInterrupted != version.MessageStreamStateInterrupted {
		t.Fatalf("agent stream-state constants diverged from the version authority")
	}
}
