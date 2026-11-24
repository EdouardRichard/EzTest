package db_test

// closure_kind migration single-source gate (v0.2.4-G T2, constitution 五.1).
// The version.closure_kind CHECK enum and column default are a persisted
// projection of the Go domain authority (version.ClosureKinds /
// version.ClosureKindNormal). This gate parses the append-only migration SQL and
// asserts set equality with the authority — the expected values are DERIVED from
// the domain constants, never hand-copied, so a domain change fails here until the
// migration is updated (and vice versa). It runs without a database; the
// applied-constraint pin lives in TestGMigrationSchema. quotedLiteralRE is shared
// with stream_state_enum_test.go (same package).

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

const closureKindMigrationFile = "migrations/version_0019_v024_g_waiver.sql"

var (
	closureKindCheckRE   = regexp.MustCompile(`(?s)closure_kind\s+IN\s*\(([^)]*)\)`)
	closureKindDefaultRE = regexp.MustCompile(`closure_kind\s+TEXT[^,]*?DEFAULT\s+'([^']+)'`)
)

func TestClosureKindMigrationCheckDerivesFromDomain(t *testing.T) {
	raw, err := os.ReadFile(closureKindMigrationFile)
	if err != nil {
		t.Fatalf("read %s: %v", closureKindMigrationFile, err)
	}
	sql := strings.ReplaceAll(string(raw), "\r\n", "\n")

	checkMatch := closureKindCheckRE.FindStringSubmatch(sql)
	if checkMatch == nil {
		t.Fatalf("%s has no closure_kind IN (...) CHECK", closureKindMigrationFile)
	}
	var got []string
	for _, m := range quotedLiteralRE.FindAllStringSubmatch(checkMatch[1], -1) {
		got = append(got, m[1])
	}
	want := version.ClosureKinds() // derived from the Go authority
	sort.Strings(got)
	wantSorted := append([]string(nil), want...)
	sort.Strings(wantSorted)
	if strings.Join(got, ",") != strings.Join(wantSorted, ",") {
		t.Fatalf("closure_kind CHECK set = %v, want domain authority %v", got, wantSorted)
	}

	defaultMatch := closureKindDefaultRE.FindStringSubmatch(sql)
	if defaultMatch == nil {
		t.Fatalf("%s has no closure_kind DEFAULT", closureKindMigrationFile)
	}
	if defaultMatch[1] != version.ClosureKindNormal {
		t.Fatalf("closure_kind DEFAULT = %q, want domain normal %q", defaultMatch[1], version.ClosureKindNormal)
	}
}
