package vocabscope

// Package vocabscope holds the REPOSITORY's vocabulary-gate policy: the one
// semantic pattern, the one residual registration table, the one per-declared-type
// anti-vacuity floor table, the one enum-authority list, the one derivation
// pipeline and the one set of adjudicated exemption tables that every
// status-vocabulary gate shares.
//
// # Why this is here and not in each gate
//
// All four gates (delivery T11e-1, version T11e-2, batch-3 T11e-3, batch-4
// T11e-4) guard the SAME vocabulary definition — every bounded context's domain
// layer under the SAME rules — and only their SCAN SURFACE differs. Keeping four
// copies of the pattern and the floors had already let them drift: before T11e-5
// delivery/version claimed an "(untyped)" floor of 229 while batch-4 claimed 243
// for the identical derivation, and the authority-reinforced value counts were
// 221 / 224. A floor that four gates state differently is a second truth
// (constitution 五.1/五.3).
//
// # Why this is a package of its own (Ruling T11e-5-R2 / T11e-6 M7)
//
// Until T11e-6 this data lived in platform/vocablint. It is TEST SUPPORT, but it
// is REPOSITORY-SPECIFIC: which contexts declare vocabulary, how many values each
// declared type must contribute, which unexported set is a closed vocabulary. The
// platform shared kernel must not know "which vocabulary this repository has" --
// it owns the ALGORITHM (govocab resolver, elision-aware const walk, R3 rules,
// occurrence-scoped sanction, scan-surface derivation), never the policy. So the
// policy lives here, under internal/testutil, and may import bounded contexts
// (the enum authorities it calls). platform/vocablint imports no context and no
// policy; the dependency points one way: vocabscope -> vocablint.
//
// A gate keeps only what is legitimately its own: its SCAN SURFACE (derived from
// the directories it guards) and its assertions. The exemption tables are here
// too, because the repository-level gate in cmd/eztest guards the UNION of every
// context's infrastructure and must reuse the SAME adjudications -- a fifth
// hand-written copy of ~150 file-bound fragments would be a second truth
// (constitution 五.1/五.3). Each context gate reads its own table; the repository
// gate reads RepositoryAllowlist(), the merge of all four.

import (
	"regexp"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/vocablint"
)

// Pattern is R3's semantic pattern, applied to the declared
// TYPE name, the declaration's own name, and every name of its const/var group.
func Pattern() *regexp.Regexp {
	return regexp.MustCompile(`Status|Kind|Stage|State|Verdict|FailureClass|Phase|Decision`)
}

// Registrations is the residual R3 registration face: the
// UNEXPORTED, literal-valued domain sets that are nonetheless closed
// vocabularies, which no derived rule may select without also risking the NLP
// word lists (a future stop-word set could just as well be a map[string]struct{}).
// Registering by declaration NAME keeps the decision explicit and auditable; the
// VALUES are still derived, never copied here.
//
// Every entry is fail-closed: a name that no longer exists, a group that carries
// no string value, or a group the derived rules already select is reported as a
// problem by DeriveR3, so this table cannot rot into a silent second list.
func Registrations() []vocablint.RegisteredGroup {
	return []vocablint.RegisteredGroup{
		{
			Pkg:  "execution",
			Name: "executorLimitationCodes",
			Reason: "The closed set of executor capability limitation codes " +
				"(execution/domain/requirement_deviation.go:85), validated by IsExecutorLimitationCode " +
				"and written into the report fingerprint's terminal_cause/executor_limitation payload. " +
				"It is an unexported map[string]struct{} of literal keys, so R3 sees no semantic name, " +
				"R3-E no exported name and R3-R no reference members; a shape rule for string sets would " +
				"also admit NLP stop-word sets, which Ruling T11d-R1 forbids. Registered by name instead.",
		},
	}
}

// TypeFloors are the per-declared-type anti-vacuity floors:
// the number of vocabulary VALUES each declared type must contribute, counted per
// NAME (a value defined twice under one type counts twice, so these are bounds on
// the derivation walk, NOT the size of the protected value set — H-A22③).
// "(untyped)" is the bucket for vocabulary const groups with no declared type.
//
// Measured on the T11e-6 tree with the extended rules (R3 + R3-E + R3-R + the
// registration above): 395 protected values, 513 untyped name-accumulations, 49
// declared-type buckets. T11e-6 raised the untyped floor from T11e-5's 509 because
// it added four vocabulary names (delivery.IssueSeverityMajor/Minor/Trivial and
// delivery.IssueHistoryReasonRecurrence); raising a floor is 扩面不弱化.
// TypeFloorProblems also reports any derived type MISSING from this table, so a
// newly declared guarded type cannot escape anti-vacuity protection.
func TypeFloors() map[string]int {
	return map[string]int{
		"(untyped)":          513,
		"ActivityType":       2,
		"AgentRunStatus":     5,
		"AnalysisStatus":     9,
		"AnchorStatus":       3,
		"AnchorSubject":      5,
		"AssertionVerdict":   4,
		"AttachmentKind":     9,
		"AttemptStatus":      6,
		"CandidateSource":    4,
		"CaseRunStatus":      10,
		"CaseRunVerdict":     4,
		"Confidence":         3,
		"EdgeKind":           9,
		"EdgeRel":            6,
		"EntityKind":         8,
		"EvidenceKind":       11,
		"ExecutorStatus":     3,
		"FailureClass":       9,
		"GCActorKind":        2,
		"GCMode":             2,
		"GCRunStatus":        3,
		"HumanKind":          5,
		"HumanRequestStatus": 4,
		"IndexStatus":        4,
		"IntentStatus":       7,
		"IssueStatus":        4,
		"MessageRole":        4,
		"NodeKind":           8,
		"PDFStatus":          5,
		"PostKind":           5,
		"Protocol":           2,
		"Purpose":            3,
		"ReportKind":         2,
		"ReportStatus":       3,
		"RequirementStatus":  4,
		"ReviewState":        5,
		"ReviewVerdict":      2,
		"Role":               2,
		"SUTContextStatus":   4,
		"Scope":              2,
		"Stage":              10,
		"Status":             12,
		"StepStatus":         6,
		"SubjectKind":        5,
		"Team":               6,
		"Type":               4,
		"WaveStatus":         6,
		"WorkItemStatus":     7,
	}
}

// Selector is the shared selection configuration: the semantic pattern
// plus the residual registrations. A gate passes it to vocablint.DeriveR3 unchanged.
func Selector() vocablint.R3Selector {
	return vocablint.R3Selector{
		Pattern:    Pattern(),
		Registered: Registrations(),
	}
}
