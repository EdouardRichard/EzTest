package vocabscope

// The ONE derivation pipeline every status-vocabulary gate shares: which files
// define the repository's vocabulary, which enum authorities reinforce it, which
// additive source outside the domain layer joins, and which floors keep the
// derivation from going vacuous.
//
// Before T11e-6 each of the four context gates wired this pipeline itself (its own
// resolver call, its own domain-file walk, its own authority list, its own floor
// check). The lists had already drifted: three gates called eight authorities and
// the fourth called ten. Measured on the T11e-6 tree the two extra authorities
// (execution.ObservationKinds / ObservationDecisions) add ZERO values -- R3-E
// already selects those exported groups -- so unifying the list is not a scope
// change for any gate, it is the removal of a second truth.

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/govocab"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/vocablint"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// DomainVocabularyFiles derives the vocabulary-defining files: every non-test .go
// file of every bounded context's domain layer, under the repository root. The
// floor is an anti-vacuity bound on the walk, not a coverage list.
func DomainVocabularyFiles(root string) ([]string, error) {
	files, err := vocablint.GoFilesUnder(filepath.Join(root, "backend", "internal"))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, file := range files {
		if strings.Contains(filepath.ToSlash(file), "/domain/") {
			out = append(out, file)
		}
	}
	if problem := vocablint.CountFloorProblem("domain vocabulary files", len(out), 100); problem != "" {
		return nil, errors.New(problem)
	}
	return out, nil
}

// AdditiveVocabularyFiles is the vocabulary source OUTSIDE the domain layer: the
// version APPLICATION-layer extraction run/segment statuses the pre-T11e-2 narrow
// gate protected (H-A17, T11b/T11c). No domain-wide rule can reach an application
// declaration, so this source stays (扩面不弱化).
//
// T11e-5 removed the three DOMAIN files the T11e-2 union also carried (version.go /
// requirement.go / attachment.go): they were a workaround for R3's semantic blind
// spot on the untyped attachment status / image-status / image-source enums
// (H-A21①). R3-E now selects those groups natively.
func AdditiveVocabularyFiles(root string) []string {
	return []string{
		filepath.Join(root, "backend", "internal", "version", "application", "extraction_worker.go"),
	}
}

// Authorities lists the enum authority functions reinforcing the R3 derivation.
// Values are taken by CALLING the authority, so there is no second hand-copied
// value table.
func Authorities() []vocablint.Authority {
	return []vocablint.Authority{
		vocablint.AuthorityOf("execution.TerminalStatuses", execution.TerminalStatuses()),
		vocablint.AuthorityOf("execution.ReclassifiableStatuses", execution.ReclassifiableStatuses()),
		vocablint.AuthorityOf("execution.EvidenceKinds", execution.EvidenceKinds()),
		vocablint.AuthorityOf("execution.ObservationKinds", execution.ObservationKinds()),
		vocablint.AuthorityOf("execution.ObservationDecisions", execution.ObservationDecisions()),
		vocablint.AuthorityOf("design.ReviewStates", design.ReviewStates()),
		vocablint.AuthorityOf("version.Statuses", version.Statuses()),
		vocablint.AuthorityOf("version.Stages", version.Stages()),
		vocablint.AuthorityOf("version.ClosureKinds", version.ClosureKinds()),
		vocablint.AuthorityOf("version.PlanItemTerminalStatuses", version.PlanItemTerminalStatuses()),
	}
}

// NonLiteralVocabulary classifies vocabulary declarations whose value the resolver
// cannot evaluate and which consciously carry no string value the guarded
// repositories must source. It is EMPTY on this tree: every vocabulary declaration
// resolves. An entry whose declaration DOES resolve to a string is rejected by
// vocablint.DeriveVocabulary (it must be derived and protected, never hidden here);
// a stale entry is reported.
func NonLiteralVocabulary() map[string]string {
	return map[string]string{}
}

// Derive runs the whole pipeline once: resolver -> R3 selection over every domain
// file -> enum authorities -> the additive application source -> per-type floors.
// Problems are RETURNED (never fatal) so a gate can cache one derivation for all of
// its tests and report them through its own *testing.T.
func Derive(root string) vocablint.R3Vocabulary {
	resolver, err := govocab.New(filepath.Join(root, "backend"))
	if err != nil {
		return vocablint.R3Vocabulary{Problems: []string{"vocabulary resolver: " + err.Error()}}
	}
	files, err := DomainVocabularyFiles(root)
	if err != nil {
		return vocablint.R3Vocabulary{Problems: []string{err.Error()}}
	}
	v := vocablint.DeriveR3(resolver, files, Selector(), NonLiteralVocabulary())
	v.AddAuthorities(Authorities())
	local := vocablint.DeriveVocabulary(resolver, AdditiveVocabularyFiles(root), NonLiteralVocabulary())
	v.Problems = append(v.Problems, local.Problems...)
	v.AddValues(local.Values)
	v.Problems = append(v.Problems, vocablint.TypeFloorProblems(v.PerType, TypeFloors())...)
	return v
}
