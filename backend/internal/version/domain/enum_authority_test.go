package version_test

// V-0017 enum single-source gate. The Go domain (version.Stages / version.Statuses)
// is the ONE authority for the version stage/status enums. The frontend keeps
// hand-written TypeScript unions (VersionStatus in features/versions/types.ts,
// VersionStage in features/design/types.ts); this gate validates them against the
// authority so they are a checked projection, never a second independent truth.
// It is a contract-validation gate (not codegen): the worktree has no OpenAPI/TS
// codegen pipeline, so a failing-on-drift test is the lightweight single-source
// mechanism. The frozen DB CHECKs are validated the same way in
// db/migrations021_test.go.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// TestEnumAuthorityLists pins the canonical authority lists themselves, so an
// accidental omission or reorder in Stages()/Statuses() is caught even before
// the derived-copy gates run.
func TestEnumAuthorityLists(t *testing.T) {
	wantStages := []version.Stage{
		version.StageCreated, version.StageClarifying, version.StageRequirementReview,
		version.StageDesigning, version.StageCaseReview, version.StageExecuting,
		version.StageReporting, version.StageCompleted, version.StageBlocked, version.StagePaused,
	}
	gotStages := version.Stages()
	if len(gotStages) != len(wantStages) {
		t.Fatalf("Stages() = %v, want %v", gotStages, wantStages)
	}
	for i, want := range wantStages {
		if gotStages[i] != want {
			t.Errorf("Stages()[%d] = %q, want %q", i, gotStages[i], want)
		}
	}
	wantStatuses := []string{version.StatusInitial, version.StatusRunning, version.StatusCompleted}
	gotStatuses := version.Statuses()
	if len(gotStatuses) != len(wantStatuses) {
		t.Fatalf("Statuses() = %v, want %v", gotStatuses, wantStatuses)
	}
	for i, want := range wantStatuses {
		if gotStatuses[i] != want {
			t.Errorf("Statuses()[%d] = %q, want %q", i, gotStatuses[i], want)
		}
	}
}

// TestEnumAuthorityFrontendUnions fails when either frontend union drifts from
// the Go authority: a member added/removed on either side is reported.
func TestEnumAuthorityFrontendUnions(t *testing.T) {
	root := repoRoot(t)
	statusSrc := readFrontendFile(t, root, "frontend/src/features/versions/types.ts")
	stageSrc := readFrontendFile(t, root, "frontend/src/features/design/types.ts")

	assertSameMembers(t, "VersionStatus", unionMembers(t, statusSrc, "VersionStatus"), version.Statuses())

	wantStages := make([]string, 0, len(version.Stages()))
	for _, stage := range version.Stages() {
		wantStages = append(wantStages, string(stage))
	}
	assertSameMembers(t, "VersionStage", unionMembers(t, stageSrc, "VersionStage"), wantStages)
}

// TestEnumAuthorityFrontendPlanStatus pins the frontend plan-item terminal mirror
// (features/versions/plan-status.ts PLAN_ITEM_TERMINAL_STATUSES) to the Go authority
// (planItemTerminalStatuses, via PlanItemTerminalStatuses()). The frontend pin test
// alone compares the constant to a hand-copied second list, so a backend authority
// change produces no CI signal there; this gate is the reverse (Go -> frontend)
// direction. A terminal status added/removed on EITHER side fails CI until the other
// is updated. The expected set is DERIVED from the Go authority, never hand-copied.
func TestEnumAuthorityFrontendPlanStatus(t *testing.T) {
	root := repoRoot(t)
	src := readFrontendFile(t, root, "frontend/src/features/versions/plan-status.ts")

	got := constArrayMembers(t, src, "PLAN_ITEM_TERMINAL_STATUSES")
	want := version.PlanItemTerminalStatuses() // derived from the Go authority
	assertSameMembers(t, "PLAN_ITEM_TERMINAL_STATUSES", got, want)

	// Normalization contract (lightweight pin): the frontend helper must normalize
	// the same way as Go PlanItemTerminal (trim + lower-case) before set membership,
	// or a padded/mixed-case status would classify differently across languages.
	fnIdx := strings.Index(src, "export function planItemTerminal(")
	if fnIdx < 0 {
		t.Fatal("frontend planItemTerminal helper not found")
	}
	body := src[fnIdx:]
	if end := strings.Index(body, "\n}"); end >= 0 {
		body = body[:end]
	}
	if !strings.Contains(body, ".trim()") || !strings.Contains(body, ".toLowerCase()") {
		t.Errorf("frontend planItemTerminal must trim + lower-case like Go PlanItemTerminal; body: %s", body)
	}
}

// TestEnumAuthorityFrontendStreamingPartTypes pins the frontend runtime mirror of
// the streaming part vocabulary (features/conversation/types.ts
// STREAMING_PART_TYPES) to the Go authority (StreamingMessagePartTypes()). The
// frontend DERIVES its StreamingPartType union from that list and uses it to
// assign the server's 1-based streaming seq to an HTTP snapshot's parts, so a
// member added/removed on either side would silently break incremental resume
// (v0.2.4-D fix wave, I-1). The expected set is derived, never hand-copied.
func TestEnumAuthorityFrontendStreamingPartTypes(t *testing.T) {
	root := repoRoot(t)
	src := readFrontendFile(t, root, "frontend/src/features/conversation/types.ts")

	got := constArrayMembers(t, src, "STREAMING_PART_TYPES")
	want := version.StreamingMessagePartTypes() // derived from the Go authority
	assertSameMembers(t, "STREAMING_PART_TYPES", got, want)

	// The union must stay DERIVED from the list, not a second hand-written union:
	// a runtime set and a compile-time union that can drift apart is exactly the
	// second-truth shape constitution V.3 forbids.
	if !strings.Contains(src, "export type StreamingPartType = (typeof STREAMING_PART_TYPES)[number]") {
		t.Error("frontend StreamingPartType must be derived from STREAMING_PART_TYPES")
	}
}

// TestEnumAuthorityFrontendJudgment pins the frontend judge-review mirrors
// (features/design/judgment.ts REVIEW_STATES / VERDICTS / SEVERITIES) to the Go
// authority (design.ReviewStates / Verdicts / Severities). T18 (spec SC-E5): the
// review_state badge, judgment report grouping and override gating all derive
// from these constants, so a member added/removed on EITHER side fails CI until
// the other is updated. The expected sets are DERIVED from the Go authority,
// never hand-copied (constitution V.3).
func TestEnumAuthorityFrontendJudgment(t *testing.T) {
	root := repoRoot(t)
	src := readFrontendFile(t, root, "frontend/src/features/design/judgment.ts")

	wantStates := make([]string, 0, len(design.ReviewStates()))
	for _, state := range design.ReviewStates() {
		wantStates = append(wantStates, string(state))
	}
	assertSameMembers(t, "REVIEW_STATES", constArrayMembers(t, src, "REVIEW_STATES"), wantStates)
	assertSameMembers(t, "VERDICTS", constArrayMembers(t, src, "VERDICTS"), design.Verdicts())
	assertSameMembers(t, "SEVERITIES", constArrayMembers(t, src, "SEVERITIES"), design.Severities())

	// The unions must stay DERIVED from the lists, not second hand-written unions
	// (a runtime set and a compile-time union that can drift is the V.3 shape).
	for _, derived := range []struct{ typeName, constName string }{
		{"ReviewState", "REVIEW_STATES"},
		{"Verdict", "VERDICTS"},
		{"Severity", "SEVERITIES"},
	} {
		marker := "export type " + derived.typeName + " = (typeof " + derived.constName + ")[number]"
		if !strings.Contains(src, marker) {
			t.Errorf("frontend %s must be derived from %s", derived.typeName, derived.constName)
		}
	}
}

// TestEnumAuthorityOpenAPI pins the OpenAPI version status/stage enum copies
// (docs/openapi/api.yaml) to the Go authority (version.Statuses / version.Stages).
// V-0017 (v0.2.4-H H-A3): the frontend unions and frozen DB CHECKs were already
// gated (above + db/migrations021_test.go), but the OpenAPI enum copies had no
// gate — a status/stage added or removed in the domain would silently drift the
// published contract. This is the contract-validation gate (not codegen): it
// derives the expected sets from the authority and fails on any drift.
//
// Copy identification is FAIL-CLOSED by subset relation (T8 M5), not by a
// distinctive discriminator that a drift could delete:
//   - an enum whose members are ALL version statuses must EQUAL Statuses(); a
//     proper subset (e.g. a status copy that lost "initial") is drift, not
//     "unrelated". The retest-purpose enum [initial, retest, manual] is not a
//     subset (retest/manual ∉ Statuses()), so it is skipped as unrelated;
//   - an enum whose members are ALL stages must EQUAL Stages() or match a
//     sanctioned proper-subset allowance (the stage-reject subset [clarifying,
//     designing]). A full stage copy that lost "created" becomes an UNSANCTIONED
//     proper subset and fails — it can no longer hide in the subset bucket;
//   - both flow-style (enum: [a, b]) and block-style (enum:\n  - a) enums are
//     parsed, so converting a copy to block style cannot escape validation.
//
// Many unrelated enums share "running"/"completed" (wave/case_run/intent/agent_run);
// they are not subsets of either authority and are skipped.
// yamlEnum is one enum literal extracted from api.yaml (flow or block style),
// with the line it starts on for diagnostics.
type yamlEnum struct {
	line    int
	members []string
}

var (
	enumFlow        = regexp.MustCompile(`enum:\s*\[([^\]]*)\]`)
	enumBlockHeader = regexp.MustCompile(`^\s*enum:\s*$`)
	enumBlockItem   = regexp.MustCompile(`^\s*-\s*(.+?)\s*$`)
)

// extractYAMLEnums parses BOTH flow-style (enum: [a, b]) and block-style
// (enum:\n  - a\n  - b) enums (T8 M5: the old gate saw only flow style, so a
// status/stage copy converted to block style escaped validation). A block-style
// header with NO parseable item (e.g. a comment/blank line between `enum:` and
// the first `- item`, which this parser cannot read) is a LOUD error, never a
// silent skip — a silently invisible enum is exactly the fail-open hole M5 closes
// (review fix-round M5). The caller fails the gate on that error.
func extractYAMLEnums(src string) ([]yamlEnum, error) {
	var out []yamlEnum
	for _, m := range enumFlow.FindAllStringSubmatchIndex(src, -1) {
		members := splitEnumMembers(src[m[2]:m[3]])
		if len(members) > 0 {
			out = append(out, yamlEnum{line: strings.Count(src[:m[0]], "\n") + 1, members: members})
		}
	}
	lines := strings.Split(src, "\n")
	for i := 0; i < len(lines); i++ {
		if !enumBlockHeader.MatchString(lines[i]) {
			continue
		}
		var members []string
		j := i + 1
		for ; j < len(lines); j++ {
			m := enumBlockItem.FindStringSubmatch(lines[j])
			if m == nil {
				break
			}
			members = append(members, strings.TrimSpace(strings.Trim(m[1], "\"'")))
		}
		if len(members) == 0 {
			return nil, fmt.Errorf("api.yaml:%d block-style enum header has no parseable item (a comment/blank line before the first '- item' makes the enum invisible; fail loud, not silent)", i+1)
		}
		out = append(out, yamlEnum{line: i + 1, members: members})
		i = j - 1
	}
	return out, nil
}

// stageSubsetAllowance sanctions a PROPER stage subset enum that is intentional
// (not a drifted full copy). members is the exact member set; reason documents
// why the subset is legitimate. Matched by set equality, non-vacuous.
type stageSubsetAllowance struct {
	members []string
	reason  string
}

// sanctionedStageSubsets: the stage-transition reject subset is the only proper
// stage subset the published contract carries today. A full stage copy that
// LOSES its discriminator (e.g. "created" deleted) lands here as an unsanctioned
// proper subset and FAILS — closing the discriminator-deletion escape (T8 M5).
var sanctionedStageSubsets = []stageSubsetAllowance{
	{[]string{"clarifying", "designing"}, "version stage-transition reject subset (api.yaml): the stages a transition may reject from, a proper subset of Stages() by design"},
}

// classifyEnumDrift is the testable fail-closed core (T8 M5 + review fix-round
// Important #1). A copy is RELATED to an authority by subset OR superset:
//   - status-related (set ⊆ Statuses() OR Statuses() ⊆ set): must EQUAL Statuses();
//     a proper subset (lost a member) AND a proper superset (gained a rogue member
//     such as Statuses()+"bogus") are BOTH drift. The superset arm restores the
//     added-value detection the BASE gate had (hasInitial && (subset||superset))
//     and the subset-only first hardening accidentally dropped (review Important #1).
//   - stage-related (set ⊆ Stages() OR Stages() ⊆ set): must EQUAL Stages(), or be a
//     sanctioned proper-subset allowance (the reject subset). A proper superset
//     (rogue extra stage) is drift — no sanctioned stage superset exists.
//   - anything else is unrelated (retest-purpose [initial,retest,manual] is neither
//     a subset nor a superset of either authority, so it stays skipped).
//
// This closes all three escapes: block-style copies are seen (extractYAMLEnums),
// deleting a discriminator yields an UNSANCTIONED proper subset (flagged), and
// adding a rogue value yields a proper SUPERSET (flagged) — none silently pass.
func classifyEnumDrift(enums []yamlEnum, statusSet, stageSet map[string]bool, allowances []stageSubsetAllowance, allowanceHits map[int]int) (findings []string, statusCopies, stageCopies, subsetMatches int) {
	for _, e := range enums {
		set := stringSet(e.members)
		statusRelated := subsetOf(set, statusSet) || subsetOf(statusSet, set)
		stageRelated := subsetOf(set, stageSet) || subsetOf(stageSet, set)
		switch {
		case statusRelated:
			statusCopies++
			if !equalSet(set, statusSet) {
				findings = append(findings, fmt.Sprintf("api.yaml:%d version status enum %v drifts from version.Statuses() (missing or rogue members)", e.line, e.members))
			}
		case stageRelated:
			if equalSet(set, stageSet) {
				stageCopies++
				continue
			}
			if subsetOf(set, stageSet) {
				// Proper subset: a sanctioned allowance, or a full copy that lost a member.
				matched := false
				for i, a := range allowances {
					if equalSet(set, stringSet(a.members)) {
						matched = true
						allowanceHits[i]++
						break
					}
				}
				if matched {
					subsetMatches++
				} else {
					findings = append(findings, fmt.Sprintf("api.yaml:%d stage enum %v is an unsanctioned proper subset of version.Stages() (a full copy that lost a member, or a new subset needing an allowance)", e.line, e.members))
				}
			} else {
				// Proper superset: a rogue extra stage value (no sanctioned superset).
				findings = append(findings, fmt.Sprintf("api.yaml:%d stage enum %v is a rogue superset of version.Stages() (extra members)", e.line, e.members))
			}
		default:
			// Unrelated enum (requirement/case/wave/agent_run/retest-purpose ...).
		}
	}
	return findings, statusCopies, stageCopies, subsetMatches
}

func TestEnumAuthorityOpenAPI(t *testing.T) {
	root := repoRoot(t)
	src := readFrontendFile(t, root, "docs/openapi/api.yaml")

	statuses := version.Statuses()
	stages := make([]string, 0, len(version.Stages()))
	for _, stage := range version.Stages() {
		stages = append(stages, string(stage))
	}
	statusSet := stringSet(statuses)
	stageSet := stringSet(stages)

	enums, err := extractYAMLEnums(src)
	if err != nil {
		t.Fatalf("V-0017/H-A3: extract OpenAPI enums: %v", err)
	}
	if len(enums) == 0 {
		t.Fatal("no enum literals found in api.yaml")
	}
	allowanceHits := make(map[int]int)
	findings, statusCopies, stageCopies, subsetMatches := classifyEnumDrift(enums, statusSet, stageSet, sanctionedStageSubsets, allowanceHits)
	for _, f := range findings {
		t.Errorf("V-0017/H-A3: %s", f)
	}
	// The copies must actually exist (a gate that silently matches nothing is no
	// gate): the published contract has version status and stage enums.
	if statusCopies == 0 {
		t.Error("no OpenAPI version status enum copy was validated (discriminator found none)")
	}
	if stageCopies == 0 {
		t.Error("no OpenAPI version stage enum copy was validated (discriminator found none)")
	}
	if subsetMatches == 0 {
		t.Error("no OpenAPI stage subset (reject) enum was validated")
	}
	for i, a := range sanctionedStageSubsets {
		if len(a.members) == 0 || strings.TrimSpace(a.reason) == "" {
			t.Errorf("V-0017/H-A3: stage subset allowance #%d has empty members or reason", i)
		}
		if allowanceHits[i] == 0 {
			t.Errorf("V-0017/H-A3: stage subset allowance %v (%s) matched nothing; remove the stale allowance in the same change set", a.members, a.reason)
		}
	}
}

// TestEnumAuthorityOpenAPIDriftPrecision is permanent deterministic proof of the
// T8 M5 fail-closed hardening + review fix-round Important #1/M5: block-style
// copies are seen, a discriminator deletion (proper subset) is flagged, a rogue
// added value (proper SUPERSET) is flagged, and an unreadable block header fails
// loud — none silently downgraded to "unrelated"/"sanctioned".
func TestEnumAuthorityOpenAPIDriftPrecision(t *testing.T) {
	statusSet := stringSet(version.Statuses())
	stageSet := stringSet([]string{"created", "clarifying", "requirement_review", "designing", "case_review", "executing", "reporting", "completed", "blocked", "paused"})

	// A block-style status copy missing "initial" must be flagged (old gate: blind
	// to block style, and exact-discriminant matching downgraded it to unrelated).
	blockDrift := "status:\n  enum:\n    - running\n    - completed\n"
	enums, err := extractYAMLEnums(blockDrift)
	if err != nil || len(enums) != 1 {
		t.Fatalf("block-style enum not extracted: %v err=%v", enums, err)
	}
	hits := make(map[int]int)
	findings, _, _, _ := classifyEnumDrift(enums, statusSet, stageSet, sanctionedStageSubsets, hits)
	if len(findings) != 1 {
		t.Fatalf("block-style status drift not flagged: %v", findings)
	}

	// Important #1: a status copy with a ROGUE ADDED value (Statuses()+"bogus") is a
	// proper superset and must be flagged. The subset-only first hardening let this
	// fall to "unrelated" (silent); the BASE gate caught it via the superset arm.
	statusSuperset := []yamlEnum{{line: 1, members: append(append([]string{}, version.Statuses()...), "bogus")}}
	hits = make(map[int]int)
	findings, statusCopies, _, _ := classifyEnumDrift(statusSuperset, statusSet, stageSet, sanctionedStageSubsets, hits)
	if len(findings) != 1 || statusCopies != 1 {
		t.Fatalf("rogue-added status superset not flagged: findings=%v statusCopies=%d", findings, statusCopies)
	}

	// Important #1 (stage): a full stage copy with a rogue added value is a proper
	// superset and must be flagged (no sanctioned stage superset exists).
	stageSuperset := []yamlEnum{{line: 1, members: []string{"created", "clarifying", "requirement_review", "designing", "case_review", "executing", "reporting", "completed", "blocked", "paused", "bogus"}}}
	hits = make(map[int]int)
	findings, _, _, _ = classifyEnumDrift(stageSuperset, statusSet, stageSet, sanctionedStageSubsets, hits)
	if len(findings) != 1 {
		t.Fatalf("rogue-added stage superset not flagged: %v", findings)
	}

	// A flow-style stage copy missing "created" must be flagged as an unsanctioned
	// proper subset (old gate treated any created-less stage subset as sanctioned).
	stageDrift := []yamlEnum{{line: 1, members: []string{"clarifying", "designing", "executing"}}}
	hits = make(map[int]int)
	findings, _, _, _ = classifyEnumDrift(stageDrift, statusSet, stageSet, sanctionedStageSubsets, hits)
	if len(findings) != 1 {
		t.Fatalf("discriminator-deleted stage copy not flagged: %v", findings)
	}

	// The sanctioned reject subset is clean; a full stage copy is clean; a full
	// status copy is clean.
	clean := []yamlEnum{
		{line: 1, members: []string{"clarifying", "designing"}},
		{line: 2, members: []string{"created", "clarifying", "requirement_review", "designing", "case_review", "executing", "reporting", "completed", "blocked", "paused"}},
		{line: 3, members: version.Statuses()},
	}
	hits = make(map[int]int)
	findings, statusCopies, stageCopies, subsetMatches := classifyEnumDrift(clean, statusSet, stageSet, sanctionedStageSubsets, hits)
	if len(findings) != 0 || statusCopies != 1 || stageCopies != 1 || subsetMatches != 1 {
		t.Fatalf("sanctioned subset/full copies wrongly flagged: findings=%v statusCopies=%d stageCopies=%d subsetMatches=%d", findings, statusCopies, stageCopies, subsetMatches)
	}

	// An unrelated enum sharing some values is skipped, not flagged (neither a
	// subset nor a superset of either authority).
	unrelated := []yamlEnum{{line: 1, members: []string{"initial", "retest", "manual"}}}
	hits = make(map[int]int)
	findings, _, _, _ = classifyEnumDrift(unrelated, statusSet, stageSet, sanctionedStageSubsets, hits)
	if len(findings) != 0 {
		t.Fatalf("unrelated retest-purpose enum wrongly flagged: %v", findings)
	}

	// M5: a block header with a comment/blank line before the first item is
	// unreadable by this parser and must FAIL LOUD (not silently skip the enum).
	for _, unreadable := range []string{
		"status:\n  enum:\n    # a comment\n    - running\n",
		"status:\n  enum:\n\n    - running\n",
		"status:\n  enum:\n",
	} {
		if _, err := extractYAMLEnums(unreadable); err == nil {
			t.Fatalf("unreadable block enum header silently skipped: %q", unreadable)
		}
	}
}

func stringSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, v := range values {
		set[v] = true
	}
	return set
}

func equalSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func subsetOf(a, b map[string]bool) bool {
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return len(a) > 0
}

func splitEnumMembers(inner string) []string {
	parts := strings.Split(inner, ",")
	members := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			members = append(members, p)
		}
	}
	return members
}

// repoRoot walks up from this test's source file to the directory that contains
// the frontend tree, so the gate works regardless of the test's working dir.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate enum authority test source")
	}
	dir := filepath.Dir(file)
	for i := 0; i < 12; i++ {
		probe := filepath.Join(dir, "frontend", "src", "features", "versions", "types.ts")
		if _, err := os.Stat(probe); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("repo root containing frontend/ not found from " + file)
	return ""
}

func readFrontendFile(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	// Normalize CRLF so the union boundary detection is line-ending agnostic
	// (the worktree carries LF/CRLF noise).
	return strings.ReplaceAll(string(data), "\r\n", "\n")
}

var unionLiteral = regexp.MustCompile(`"([^"]+)"`)

// unionMembers extracts the quoted string literals of `export type <name> = ...`
// up to the next blank line or export, in source order.
func unionMembers(t *testing.T, src, name string) []string {
	t.Helper()
	marker := "export type " + name + " ="
	idx := strings.Index(src, marker)
	if idx < 0 {
		t.Fatalf("frontend union %s not found", name)
	}
	rest := src[idx+len(marker):]
	end := len(rest)
	if i := strings.Index(rest, "\n\n"); i >= 0 && i < end {
		end = i
	}
	if i := strings.Index(rest, "\nexport "); i >= 0 && i < end {
		end = i
	}
	matches := unionLiteral.FindAllStringSubmatch(rest[:end], -1)
	members := make([]string, 0, len(matches))
	for _, m := range matches {
		members = append(members, m[1])
	}
	if len(members) == 0 {
		t.Fatalf("frontend union %s has no string members", name)
	}
	return members
}

// constArrayMembers extracts the quoted string literals of a TypeScript
// `export const <name> = [ ... ]` array (e.g. PLAN_ITEM_TERMINAL_STATUSES), in
// source order. Unlike unionMembers (a type union), the members live inside array
// brackets, so the scan is bounded by the first '[' and its matching ']'.
func constArrayMembers(t *testing.T, src, name string) []string {
	t.Helper()
	marker := "export const " + name + " ="
	idx := strings.Index(src, marker)
	if idx < 0 {
		t.Fatalf("frontend const %s not found", name)
	}
	rest := src[idx+len(marker):]
	open := strings.Index(rest, "[")
	if open < 0 {
		t.Fatalf("frontend const %s has no array literal", name)
	}
	rest = rest[open+1:]
	closeIdx := strings.Index(rest, "]")
	if closeIdx < 0 {
		t.Fatalf("frontend const %s array literal is not closed", name)
	}
	matches := unionLiteral.FindAllStringSubmatch(rest[:closeIdx], -1)
	members := make([]string, 0, len(matches))
	for _, m := range matches {
		members = append(members, m[1])
	}
	if len(members) == 0 {
		t.Fatalf("frontend const %s has no string members", name)
	}
	return members
}

// assertSameMembers reports any member present on one side but not the other,
// independent of order (a reorder is not drift; an add/remove is).
func assertSameMembers(t *testing.T, name string, got, want []string) {
	t.Helper()
	gotSet := make(map[string]bool, len(got))
	for _, g := range got {
		gotSet[g] = true
	}
	wantSet := make(map[string]bool, len(want))
	for _, w := range want {
		wantSet[w] = true
	}
	for _, w := range want {
		if !gotSet[w] {
			t.Errorf("%s: frontend union is missing domain member %q", name, w)
		}
	}
	for _, g := range got {
		if !wantSet[g] {
			t.Errorf("%s: frontend union has extra member %q absent from the domain authority", name, g)
		}
	}
	if len(got) != len(want) {
		t.Errorf("%s: frontend union has %d members %v, domain authority has %d %v", name, len(got), got, len(want), want)
	}
}
