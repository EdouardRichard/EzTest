package main

// The STAGE-CODENAME gate (v0.2.4-H T25b, H-H2).
//
// The historical phase codenames (P0..P9 / p4 / p7 ...) must not survive in
// file names, Go identifiers, non-test backend string literals or runtime frontend
// text. T24/T24b/T25/T26 renamed the tree; this gate keeps it that way. It is a
// REGRESSION gate, not a cleanup: it must be green on the T25b tree and red the
// moment a new stage codename is introduced.
//
// # Why a Go test (Ruling T25b-R3 option A)
//
// The identifier surface MUST be scanned through go/parser (go/ast), never a
// regex over source text: a regex cannot tell an identifier from a comment or a
// string, and comments are explicitly allowed to mention history. The repository
// already collects gate tests in backend/cmd/eztest, so this file rides
// `go test ./...` and CI with no extra wiring; check-docs.ps1 does not need to
// change. A node script would have to re-implement Go parsing and bring no
// benefit.
//
// # The allow set is DERIVED, never a hand-written codename list (Ruling T25b-R1)
//
// The priority domain values P0..P3 are legitimate vocabulary. They are not
// allow-listed by name here; they are DERIVED from backend const declarations:
//
//  1. Route 1 (string-valued constants): a const spec whose string value is
//     exactly P0..P3 AND whose name contains "priority" (case-insensitive)
//     contributes its identifier name and its value token. This derives
//     ExtractionPriorityP0..P3 (tools/extraction_emit.go), PriorityP0..P3
//     (design/domain/case.go) and DefaultPriority (version/domain/requirement.go).
//  2. Route 2 (name-token constants): a const whose name contains "priority" AND
//     contains one of the tokens derived by route 1 contributes its name. This
//     derives designPriorityP0..P3 (design/application/planner.go), whose values
//     are integer weights, not P0..P3 strings.
//
// A name is therefore allowed only because it is a derived priority authority,
// not because it appears in a table. Changing a retained priority constant into a
// phase-shaped name (mutation 4: ExtractionPriorityP3 -> PhaseP3) removes it from
// the derivation and the new name becomes a finding.
//
// # Scan surfaces (Ruling T25b-R2)
//
//  1. Go identifiers: every backend/**/*.go (incl. _test.go) through go/parser.
//     Any *ast.Ident containing a [Pp][0-9]+ token is a candidate. Allowed iff it
//     is a derived priority name or a named, reasoned, file-prefix-scoped, live
//     exemption.
//  2. File AND directory names: backend/, scripts/, frontend/e2e/. Any file OR
//     directory whose BASENAME starts with [Pp][0-9] is a candidate (this covers
//     p9_probe.go / p9-probe.ps1 / a p9runtime/ directory, and is at least as
//     strict as the brief's p[0-9][_-] prefix). A legitimate name such as
//     p2p_transport.go has an auditable escape hatch: a named, reasoned,
//     stale-checked file-name exemption. Mid-name codenames left by T24/T25
//     (e.g. task_context_p4_issue.go, acceptance-p7-db.ps1) are outside this
//     prefix gate; they are tracked by T24b/T25 handoffs.
//  3. Backend non-test string literals: every string/char BasicLit in a non-test
//     .go file. A literal containing a [Pp][0-9]+ token is a candidate unless the
//     token is a derived priority value (P0..P3) or the literal is a named,
//     reasoned, stale-checked data value. AST scanning means comments (e.g.
//     render.go's "P4 five-section layout" note) are never scanned.
//  4. Frontend runtime text: every non-test frontend/src/**/*.tsx file, after
//     stripping // and /* */ comments, must not contain "受控网络", a full-width
//     (Pn) phase parenthesis, or "Pn 阶段".
//
// # Exemptions are named, reasoned and STALE-CHECKED (Ruling T25b-R2 last bullet)
//
// Every exemption carries a non-empty reason. It is also live: an identifier
// exemption must be used by an occurrence inside its file prefixes (a hit outside
// them is a finding), a data-value exemption whose literal count changed, a
// file-name exemption whose path no longer matches the prefix, or a historical
// surface that no longer exists is a red failure. This forces repayment/removal
// instead of letting an exemption rot into a silent hole. The tables below
// contain NO phase codenames: they are collisions (fp1, app0, ...),
// priority-semantic identifiers that are not constants (task79P0Count, ...),
// format/data values (p5.1, .mp4, VP8, 42P10, a DB CHECK 'p9' reference), and (on
// demand) a legitimate p-prefixed file/directory name. The phase-codename
// vocabulary itself is derived.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// stageCodenameTokenRE matches a P/p followed by one or more digits. The
// multi-digit form matters: data values such as PostgreSQL SQLSTATE 42P10 are
// P10, not priority P1, and must be adjudicated, not silently allowed.
var stageCodenameTokenRE = regexp.MustCompile(`[Pp][0-9]+`)

// stagePriorityValueRE is the DOMAIN definition of a priority value, used only to
// derive the priority vocabulary from backend constants. It is not a codename
// list; P0..P3 are the product's requirement/case priority levels.
var stagePriorityValueRE = regexp.MustCompile(`^P[0-3]$`)

// stageFilenamePrefixRE is anchored at the basename start: the prefix form the
// rename tasks eliminated (p0-e2e.ps1, p3_tools.go, p9-probe.ps1). It also
// catches bare p9Foo.go because no separator is required after the digit.
var stageFilenamePrefixRE = regexp.MustCompile(`^[Pp][0-9]`)

// stageIdentifierExemption sanctions a NON-codename Go identifier that happens to
// contain a P/p+digit token. The name must still occur somewhere as an identifier
// (stale check) and the reason must be non-empty.
type stageIdentifierExemption struct {
	Name string
	// FilePrefixes scopes the exemption to the directories/packages where the
	// non-codename identifier legitimately lives. A name-only exemption would
	// silently allow a future production `fp1`/`p0Tasks` anywhere in the backend;
	// a hit OUTSIDE these prefixes is a finding. At least one occurrence inside
	// the prefixes must exist (stale check).
	FilePrefixes []string
	Reason       string
}

// stageIdentifierExemptions are collisions and priority-semantic identifiers that
// are not const authorities. None of these is a phase codename. Each is scoped to
// the file prefixes where it is legitimate.
var stageIdentifierExemptions = []stageIdentifierExemption{
	// Priority semantics that are not constants. The derivation covers priority
	// CONSTANTS; these are test fixtures/assertions that measure P0-priority
	// behaviour and must keep the P0 in their name.
	{Name: "task79P0Count", FilePrefixes: []string{"backend/internal/design/application/"}, Reason: "P0-priority requirement count in the Task-79 design coverage fixture; not a phase codename."},
	{Name: "assertTask79P0Priority", FilePrefixes: []string{"backend/internal/design/application/"}, Reason: "asserts P0-priority ordering of design authoring tasks; not a phase codename."},
	{Name: "seedP0ClosureRequirement", FilePrefixes: []string{"backend/internal/version/application/"}, Reason: "seeds a P0-priority requirement revision for closure tests; not a phase codename."},
	{Name: "TestDesignCoversAllP0Requirements", FilePrefixes: []string{"backend/internal/design/application/"}, Reason: "test name: every P0-priority requirement is covered; not a phase codename."},
	{Name: "p0Tasks", FilePrefixes: []string{"backend/internal/design/application/"}, Reason: "local count of P0-priority design authoring tasks; not a phase codename."},
	// Lexical collisions: the P/p+digit is part of another word, not a token.
	{Name: "fp1", FilePrefixes: []string{"backend/internal/execution/application/"}, Reason: "fingerprint variable in replay/wave tests; the p1 is part of 'fp', not a phase codename."},
	{Name: "fp1Again", FilePrefixes: []string{"backend/internal/execution/application/"}, Reason: "second fingerprint variable in a replay test; the p1 is part of 'fp', not a phase codename."},
	{Name: "fp2", FilePrefixes: []string{"backend/internal/execution/application/"}, Reason: "fingerprint variable in replay/wave tests; the p2 is part of 'fp', not a phase codename."},
	{Name: "fp3", FilePrefixes: []string{"backend/internal/execution/application/"}, Reason: "fingerprint variable in replay/wave tests; the p3 is part of 'fp', not a phase codename."},
	{Name: "fp4", FilePrefixes: []string{"backend/internal/execution/application/"}, Reason: "fingerprint variable in replay/wave tests; the p4 is part of 'fp', not a phase codename."},
	{Name: "app0", FilePrefixes: []string{"backend/internal/version/infrastructure/attachments/"}, Reason: "JPEG APP0 segment byte variable in an attachment test; the p0 is part of 'app', not a phase codename."},
	{Name: "step1ID", FilePrefixes: []string{"backend/internal/delivery/infrastructure/adapters/"}, Reason: "identifier of 'step 1' in an issue-anchor test; the p1 is part of 'step', not a phase codename."},
	{Name: "assertTop3", FilePrefixes: []string{"backend/internal/agent/application/context/"}, Reason: "asserts the briefing keeps its top 3 facts; the p3 is part of 'Top', not a phase codename."},
	{Name: "TestBriefingKeepsTop3Facts", FilePrefixes: []string{"backend/internal/agent/application/context/"}, Reason: "test name for the top 3 briefing facts; the p3 is part of 'Top', not a phase codename."},
}

// stageDataValueExemption sanctions format/data values inside non-test backend
// string literals. Contains must itself carry a codename token (otherwise the
// exemption does nothing), and the number of string literals containing it in the
// named file must equal Count (stale check, both directions).
type stageDataValueExemption struct {
	File     string
	Contains string
	Count    int
	Reason   string
}

// stageDataValueExemptions is the complete data-value adjudication measured on
// the T25b tree. These are not runtime codename prose: they are external format
// markers, a prompt version, a SQLSTATE and a DB CHECK literal cited in an
// adjudication note.
var stageDataValueExemptions = []stageDataValueExemption{
	{File: "backend/internal/codeintel/application/index_service.go", Contains: "p5.1", Count: 1,
		Reason: "codeintel prompt version data value (T1 prompt catalog), not a phase codename."},
	{File: "backend/internal/settings/application/catalog.go", Contains: "p5.1", Count: 1,
		Reason: "default data value of the codeintel.prompt_version setting, not a phase codename."},
	{File: "backend/internal/execution/application/discovery_normalize.go", Contains: ".mp4", Count: 1,
		Reason: "MP4 media file extension in a static asset filter, not a phase codename."},
	{File: "backend/internal/testutil/vocabscope/allowlist_agent.go", Contains: "'p9'", Count: 2,
		Reason: "cited DB CHECK literal (execution_0006) inside adjudication reasons; the code quotes the historical value, it is not runtime codename prose."},
	{File: "backend/internal/testutil/vocabscope/allowlist_delivery.go", Contains: "42P10", Count: 2,
		Reason: "PostgreSQL SQLSTATE 42P10 (indeterminate datatype) quoted in adjudication notes, not a phase codename."},
	{File: "backend/internal/version/infrastructure/attachments/image.go", Contains: "VP8", Count: 3,
		Reason: "WEBP container chunk FourCC markers VP8/VP8L/VP8X, not a phase codename."},
}

// stageFileNameExemption sanctions a legitimate file OR directory whose basename
// starts with [Pp][0-9] but is not a phase codename (for example the "p2p"
// transport initialism, or a "p3d" graphics directory). Path is repo-relative.
// The stale check requires the path to still be encountered in the scan surface
// AND to still match the prefix pattern, so an exemption cannot rot into a hole.
type stageFileNameExemption struct {
	Path   string
	Reason string
}

// stageFileNameExemptions is EMPTY on the T25b tree: no legitimate p-prefixed
// file or directory exists yet. The mechanism exists so a future legitimate name
// has an auditable escape hatch instead of a hard red with no way to register the
// reason (Constitution 一.9 / 二.4: a restriction must not be a dead end). Mutation
// ⑤ in the task report exercises both directions (exempt -> green, drop -> red).
var stageFileNameExemptions = []stageFileNameExemption{}

// stageHistorySurface is a frozen historical harness that is out of the filename
// gate's scope. It is named and reasoned, and the stale check requires the
// surface to still contain at least one file.
type stageHistorySurface struct {
	Prefix string
	Reason string
}

var stageHistorySurfaces = []stageHistorySurface{
	{Prefix: "scripts/v022-", Reason: "frozen v0.2.2 acceptance harness retained by T20-R1; historical device, not current product code."},
	{Prefix: "scripts/acceptance-021/", Reason: "frozen 0.2.1 acceptance harness retained by T20-R1; historical device, not current product code."},
}

// stageFrontendBadPhrase is a runtime-copy regression guard for the phrases T26
// removed from the frontend.
type stageFrontendBadPhrase struct {
	Name string
	RE   *regexp.Regexp
}

var stageFrontendBadPhrases = []stageFrontendBadPhrase{
	{Name: "controlled-network copy", RE: regexp.MustCompile(`受控网络`)},
	{Name: "full-width (Pn) phase parenthesis", RE: regexp.MustCompile(`（\s*[Pp][0-9]+\s*）`)},
	{Name: "Pn phase suffix", RE: regexp.MustCompile(`[Pp][0-9]+\s*阶段`)},
}

// ---------------------------------------------------------------------------
// repository root and shared scan
// ---------------------------------------------------------------------------

func stageCodenameRepoRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("cannot locate the stage-codename gate source")
	}
	dir := filepath.Dir(file)
	for i := 0; i < 12; i++ {
		if _, err := os.Stat(filepath.Join(dir, "backend", "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("repository root containing backend/go.mod not found from %s", file)
}

// stageCodenameScanOnce runs every surface once per test binary.
var stageCodenameScanOnce = sync.OnceValue(runStageCodenameScan)

type stageCodenameScan struct {
	PriorityNames          []string
	PriorityTokens         []string
	GoFiles                int
	IdentifierCount        int
	DistinctCodenameIdents []string
	IdentFindings          []string
	IdentStale             []string
	StringLiteralCount     int
	StringFindings         []string
	StringStale            []string
	FileNameCount          int
	FileNameFindings       []string
	FileNameStale          []string
	TSXFileCount           int
	FrontendFindings       []string
	Problems               []string
}

func runStageCodenameScan() stageCodenameScan {
	root, err := stageCodenameRepoRoot()
	if err != nil {
		return stageCodenameScan{Problems: []string{err.Error()}}
	}
	var out stageCodenameScan

	goFiles, err := stageCodenameGoFiles(root)
	if err != nil {
		out.Problems = append(out.Problems, fmt.Sprintf("walk backend Go files: %v", err))
		return out
	}
	out.GoFiles = len(goFiles)

	vocab, problems := deriveStageCodenameVocabulary(root, goFiles)
	out.Problems = append(out.Problems, problems...)
	out.PriorityNames = sortedKeys(vocab.PriorityNames)
	out.PriorityTokens = sortedKeys(vocab.PriorityTokens)

	identNames, identHits, identCount, identProblems := scanStageIdentifiers(root, goFiles)
	out.IdentifierCount = identCount
	out.Problems = append(out.Problems, identProblems...)
	out.IdentFindings, out.IdentStale = evaluateStageIdentifierHits(vocab, identNames, identHits)
	if len(identProblems) == 0 && len(problems) == 0 {
		distinct := map[string]bool{}
		for _, h := range identHits {
			distinct[h.Name] = true
		}
		out.DistinctCodenameIdents = sortedKeys(distinct)
	}

	strCount, strFindings, strStale, strProblems := scanStageBackendStrings(root, goFiles, vocab)
	out.StringLiteralCount = strCount
	out.StringFindings = strFindings
	out.StringStale = strStale
	out.Problems = append(out.Problems, strProblems...)

	fileCount, fileFindings, fileStale, fileProblems := scanStageFileNames(root)
	out.FileNameCount = fileCount
	out.FileNameFindings = fileFindings
	out.FileNameStale = fileStale
	out.Problems = append(out.Problems, fileProblems...)

	tsxCount, frontendFindings, frontendProblems := scanStageFrontendRuntimeText(root)
	out.TSXFileCount = tsxCount
	out.FrontendFindings = frontendFindings
	out.Problems = append(out.Problems, frontendProblems...)

	sort.Strings(out.Problems)
	return out
}

// ---------------------------------------------------------------------------
// derivation
// ---------------------------------------------------------------------------

type stageCodenameVocabulary struct {
	PriorityNames  map[string]bool
	PriorityTokens map[string]bool
}

type stageConstInfo struct {
	name  string
	value string
}

func deriveStageCodenameVocabulary(root string, goFiles []string) (stageCodenameVocabulary, []string) {
	vocab := stageCodenameVocabulary{PriorityNames: map[string]bool{}, PriorityTokens: map[string]bool{}}
	var problems []string
	var consts []stageConstInfo

	for _, rel := range goFiles {
		fset, file, err := parseStageGoFile(root, rel)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: parse for priority derivation: %v", rel, err))
			continue
		}
		_ = fset
		ast.Inspect(file, func(n ast.Node) bool {
			gd, ok := n.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				return true
			}
			lastValue := ""
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				if len(vs.Values) == 0 {
					for _, id := range vs.Names {
						consts = append(consts, stageConstInfo{name: id.Name, value: lastValue})
					}
					continue
				}
				for i, id := range vs.Names {
					value := ""
					if len(vs.Values) == len(vs.Names) {
						value = stageStringLiteralValue(vs.Values[i])
					} else if len(vs.Values) == 1 {
						value = stageStringLiteralValue(vs.Values[0])
					}
					consts = append(consts, stageConstInfo{name: id.Name, value: value})
				}
				lastValue = stageStringLiteralValue(vs.Values[len(vs.Values)-1])
			}
			return true
		})
	}

	// Route 1: string-valued priority constants derive both the token set and
	// their own names.
	for _, c := range consts {
		if !strings.Contains(strings.ToLower(c.name), "priority") {
			continue
		}
		if stagePriorityValueRE.MatchString(c.value) {
			vocab.PriorityNames[c.name] = true
			vocab.PriorityTokens[normalizeStageToken(c.value)] = true
		}
	}
	// Route 2: const names whose own name carries an already-derived priority
	// token (integer weight constants such as designPriorityP0..P3).
	for _, c := range consts {
		if !strings.Contains(strings.ToLower(c.name), "priority") {
			continue
		}
		for _, tok := range stageCodenameTokenRE.FindAllString(c.name, -1) {
			if vocab.PriorityTokens[normalizeStageToken(tok)] {
				vocab.PriorityNames[c.name] = true
				break
			}
		}
	}
	return vocab, problems
}

func stageStringLiteralValue(expr ast.Expr) string {
	bl, ok := expr.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return ""
	}
	value, err := strconv.Unquote(bl.Value)
	if err != nil {
		return ""
	}
	return value
}

// ---------------------------------------------------------------------------
// identifier surface (go/parser AST)
// ---------------------------------------------------------------------------

type stageIdentHit struct {
	File string
	Line int
	Name string
}

func scanStageIdentifiers(root string, goFiles []string) (map[string]bool, []stageIdentHit, int, []string) {
	names := map[string]bool{}
	var hits []stageIdentHit
	var problems []string
	scanned := 0
	for _, rel := range goFiles {
		fset, file, err := parseStageGoFile(root, rel)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: parse identifiers: %v", rel, err))
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			scanned++
			names[id.Name] = true
			if stageCodenameTokenRE.MatchString(id.Name) {
				hits = append(hits, stageIdentHit{
					File: rel,
					Line: fset.Position(id.Pos()).Line,
					Name: id.Name,
				})
			}
			return true
		})
	}
	return names, hits, scanned, problems
}

func evaluateStageIdentifierHits(vocab stageCodenameVocabulary, names map[string]bool, hits []stageIdentHit) ([]string, []string) {
	var findings []string
	var stale []string

	used := map[string]bool{}
	for _, e := range stageIdentifierExemptions {
		if strings.TrimSpace(e.Reason) == "" {
			stale = append(stale, fmt.Sprintf("identifier exemption %q has an empty reason", e.Name))
		}
		if len(e.FilePrefixes) == 0 {
			stale = append(stale, fmt.Sprintf("identifier exemption %q has no file-prefix scope; a name-only exemption would allow the same identifier anywhere", e.Name))
		}
		for _, prefix := range e.FilePrefixes {
			if strings.TrimSpace(prefix) == "" {
				stale = append(stale, fmt.Sprintf("identifier exemption %q has an empty file prefix", e.Name))
			}
		}
	}

	seen := map[string]bool{}
	for _, h := range hits {
		if vocab.PriorityNames[h.Name] {
			continue
		}
		if e := stageIdentifierExemptionFor(h.Name, h.File); e != nil {
			used[e.Name] = true
			continue
		}
		key := fmt.Sprintf("%s:%d:%s", h.File, h.Line, h.Name)
		if seen[key] {
			continue
		}
		seen[key] = true
		findings = append(findings, fmt.Sprintf("%s:%d identifier %q carries a stage-codename token -- rename it to a functional name or add a named, reasoned, file-scoped exemption", h.File, h.Line, h.Name))
	}

	for _, e := range stageIdentifierExemptions {
		if !used[e.Name] {
			stale = append(stale, fmt.Sprintf("identifier exemption %q was not used by any occurrence under %v; an occurrence OUTSIDE the prefixes is a finding -- remove the exemption or update its file scope", e.Name, e.FilePrefixes))
		}
	}
	for name := range vocab.PriorityNames {
		if !names[name] {
			stale = append(stale, fmt.Sprintf("derived priority name %q is not present as an identifier; the derivation or the constant is broken", name))
		}
	}

	sort.Strings(findings)
	sort.Strings(stale)
	return findings, stale
}

func stageIdentifierExemptionFor(name, file string) *stageIdentifierExemption {
	for i := range stageIdentifierExemptions {
		e := &stageIdentifierExemptions[i]
		if e.Name != name {
			continue
		}
		for _, prefix := range e.FilePrefixes {
			if strings.HasPrefix(file, prefix) {
				return e
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// backend string-literal surface (non-test, AST BasicLit)
// ---------------------------------------------------------------------------

func scanStageBackendStrings(root string, goFiles []string, vocab stageCodenameVocabulary) (int, []string, []string, []string) {
	var findings []string
	var problems []string
	exemptionMatches := map[string]int{}
	scanned := 0

	for _, rel := range goFiles {
		if strings.HasSuffix(rel, "_test.go") {
			continue
		}
		fset, file, err := parseStageGoFile(root, rel)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: parse string literals: %v", rel, err))
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			bl, ok := n.(*ast.BasicLit)
			if !ok || (bl.Kind != token.STRING && bl.Kind != token.CHAR) {
				return true
			}
			lit, err := strconv.Unquote(bl.Value)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s:%d unquote literal: %v", rel, fset.Position(bl.Pos()).Line, err))
				return true
			}
			scanned++
			for _, e := range stageDataValueExemptions {
				if e.File == rel && strings.Contains(lit, e.Contains) {
					exemptionMatches[stageExemptionKey(e.File, e.Contains)]++
				}
			}
			for _, tok := range stageCodenameTokenRE.FindAllString(lit, -1) {
				if vocab.PriorityTokens[normalizeStageToken(tok)] {
					continue
				}
				if stageStringExemptionCovers(rel, lit, tok) {
					continue
				}
				findings = append(findings, fmt.Sprintf("%s:%d string value contains stage-codename token %q (%s) -- use a functional value or add a named, reasoned, stale-checked data-value exemption", rel, fset.Position(bl.Pos()).Line, tok, truncateStageLiteral(lit)))
			}
			return true
		})
	}

	var stale []string
	for _, e := range stageDataValueExemptions {
		if strings.TrimSpace(e.Reason) == "" {
			stale = append(stale, fmt.Sprintf("data-value exemption %q in %s has an empty reason", e.Contains, e.File))
			continue
		}
		if e.Count <= 0 {
			stale = append(stale, fmt.Sprintf("data-value exemption %q in %s has a non-positive count", e.Contains, e.File))
		}
		if !stageCodenameTokenRE.MatchString(e.Contains) {
			stale = append(stale, fmt.Sprintf("data-value exemption %q in %s covers no stage-codename token; it does nothing", e.Contains, e.File))
		}
		got := exemptionMatches[stageExemptionKey(e.File, e.Contains)]
		if got != e.Count {
			stale = append(stale, fmt.Sprintf("data-value exemption %q in %s is stale: %d live literal(s), expected %d; update or remove the exemption", e.Contains, e.File, got, e.Count))
		}
	}

	sort.Strings(findings)
	sort.Strings(stale)
	return scanned, findings, stale, problems
}

func stageStringExemptionCovers(file, lit, token string) bool {
	for _, e := range stageDataValueExemptions {
		if e.File != file || !strings.Contains(lit, e.Contains) {
			continue
		}
		for _, covered := range stageCodenameTokenRE.FindAllString(e.Contains, -1) {
			if normalizeStageToken(covered) == normalizeStageToken(token) {
				return true
			}
		}
	}
	return false
}

func stageExemptionKey(file, contains string) string {
	return file + "\x00" + contains
}

func truncateStageLiteral(lit string) string {
	const max = 80
	if len(lit) <= max {
		return lit
	}
	return lit[:max] + "..."
}

// ---------------------------------------------------------------------------
// file-name surface
// ---------------------------------------------------------------------------

func scanStageFileNames(root string) (int, []string, []string, []string) {
	var findings []string
	var problems []string
	scanned := 0
	historyFiles := map[string]int{}
	exemptUsed := map[string]bool{}

	for _, surface := range []string{"backend", "scripts", "frontend/e2e"} {
		base := filepath.Join(root, filepath.FromSlash(surface))
		if _, err := os.Stat(base); err != nil {
			problems = append(problems, fmt.Sprintf("file-name surface %s is missing: %v", surface, err))
			continue
		}
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				switch d.Name() {
				case ".git", "node_modules", ".superpowers":
					return filepath.SkipDir
				}
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if rel == surface {
				// The walk root itself (backend / scripts / frontend/e2e) is not a
				// candidate, but its children still are.
				return nil
			}
			scanned++
			if prefix, ok := stageHistorySurfaceFor(rel); ok {
				historyFiles[prefix]++
				return nil
			}
			if !stageFilenamePrefixRE.MatchString(d.Name()) {
				return nil
			}
			if stageFileNameExempt(rel) {
				exemptUsed[rel] = true
				// Keep walking: files inside an exempted directory are still checked.
				return nil
			}
			kind := "file-name"
			if d.IsDir() {
				kind = "directory-name"
			}
			findings = append(findings, fmt.Sprintf("%s carries a stage-codename %s prefix -- rename it to a functional name or add a named, reasoned, stale-checked file-name exemption", rel, kind))
			return nil
		})
		if err != nil {
			problems = append(problems, fmt.Sprintf("walk file-name surface %s: %v", surface, err))
		}
	}

	var stale []string
	for _, e := range stageFileNameExemptions {
		if strings.TrimSpace(e.Reason) == "" {
			stale = append(stale, fmt.Sprintf("file-name exemption %q has an empty reason", e.Path))
		}
		if !stageFilenamePrefixRE.MatchString(filepath.Base(filepath.FromSlash(e.Path))) {
			stale = append(stale, fmt.Sprintf("file-name exemption %q does not match the stage-codename prefix pattern; it does nothing", e.Path))
		}
		if !exemptUsed[e.Path] {
			stale = append(stale, fmt.Sprintf("file-name exemption %q is stale: no scanned file or directory with that path matched the prefix; remove it or update the path", e.Path))
		}
	}
	for _, hs := range stageHistorySurfaces {
		if strings.TrimSpace(hs.Reason) == "" {
			stale = append(stale, fmt.Sprintf("historical-surface exemption %q has an empty reason", hs.Prefix))
		}
		if historyFiles[hs.Prefix] == 0 {
			stale = append(stale, fmt.Sprintf("historical-surface exemption %q is stale: no files remain under it; remove the exemption", hs.Prefix))
		}
	}

	sort.Strings(findings)
	sort.Strings(stale)
	return scanned, findings, stale, problems
}

func stageFileNameExempt(rel string) bool {
	for _, e := range stageFileNameExemptions {
		if e.Path == rel {
			return true
		}
	}
	return false
}

func stageHistorySurfaceFor(rel string) (string, bool) {
	for _, hs := range stageHistorySurfaces {
		if strings.HasPrefix(rel, hs.Prefix) {
			return hs.Prefix, true
		}
	}
	return "", false
}

// ---------------------------------------------------------------------------
// frontend runtime-text surface (regression guard, comments stripped)
// ---------------------------------------------------------------------------

func scanStageFrontendRuntimeText(root string) (int, []string, []string) {
	var findings []string
	var problems []string
	scanned := 0
	base := filepath.Join(root, "frontend", "src")
	if _, err := os.Stat(base); err != nil {
		return 0, nil, []string{fmt.Sprintf("frontend source surface is missing: %v", err)}
	}
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "node_modules" || d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".tsx") || strings.HasSuffix(name, ".test.tsx") || strings.HasSuffix(name, ".spec.tsx") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		raw, err := os.ReadFile(path)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: read tsx: %v", rel, err))
			return nil
		}
		scanned++
		stripped := stageStripTSXComments(string(raw))
		for i, line := range strings.Split(stripped, "\n") {
			for _, phrase := range stageFrontendBadPhrases {
				if phrase.RE.MatchString(line) {
					findings = append(findings, fmt.Sprintf("%s:%d runtime text contains %s -- remove the stage codename from user-visible copy", rel, i+1, phrase.Name))
				}
			}
		}
		return nil
	})
	if err != nil {
		problems = append(problems, fmt.Sprintf("walk frontend source: %v", err))
	}
	sort.Strings(findings)
	return scanned, findings, problems
}

// stageStripTSXComments removes // and /* */ comments while preserving line
// count, so reported line numbers stay accurate. It is a phrase-level regression
// guard, not a JSX parser: a :// URL is not treated as a line comment, and a
// block comment opened inside a string literal is a known limitation.
func stageStripTSXComments(src string) string {
	var out strings.Builder
	inBlock := false
	for _, line := range strings.Split(src, "\n") {
		var cleaned strings.Builder
		for i := 0; i < len(line); {
			if inBlock {
				if strings.HasPrefix(line[i:], "*/") {
					inBlock = false
					i += 2
				} else {
					i++
				}
				continue
			}
			if strings.HasPrefix(line[i:], "/*") {
				inBlock = true
				i += 2
				continue
			}
			if strings.HasPrefix(line[i:], "//") {
				if i > 0 && line[i-1] == ':' {
					cleaned.WriteString("//")
					i += 2
					continue
				}
				break
			}
			cleaned.WriteByte(line[i])
			i++
		}
		out.WriteString(cleaned.String())
		out.WriteByte('\n')
	}
	return out.String()
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func stageCodenameGoFiles(root string) ([]string, error) {
	var files []string
	base := filepath.Join(root, "backend")
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", ".superpowers":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func parseStageGoFile(root, rel string) (*token.FileSet, *ast.File, error) {
	fset := token.NewFileSet()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	file, err := parser.ParseFile(fset, abs, nil, 0)
	if err != nil {
		return nil, nil, err
	}
	return fset, file, nil
}

func normalizeStageToken(tok string) string {
	if len(tok) < 2 {
		return tok
	}
	return "P" + tok[1:]
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ---------------------------------------------------------------------------
// tests
// ---------------------------------------------------------------------------

func stageCodenameScanT(t *testing.T) stageCodenameScan {
	t.Helper()
	scan := stageCodenameScanOnce()
	if len(scan.Problems) > 0 {
		for _, p := range scan.Problems {
			t.Errorf("T25b stage-codename gate: %s", p)
		}
		t.Fatalf("%d scan problem(s) -- the gate fails closed on parse/read errors", len(scan.Problems))
	}
	return scan
}

func TestStageCodenameGateGoIdentifiers(t *testing.T) {
	scan := stageCodenameScanT(t)
	for _, f := range scan.IdentFindings {
		t.Errorf("T25b: %s", f)
	}
	for _, s := range scan.IdentStale {
		t.Errorf("T25b: %s", s)
	}
	if len(scan.IdentFindings) > 0 || len(scan.IdentStale) > 0 {
		t.Fatalf("%d stage-codename identifier finding(s), %d stale exemption(s)", len(scan.IdentFindings), len(scan.IdentStale))
	}
}

func TestStageCodenameGateFileNames(t *testing.T) {
	scan := stageCodenameScanT(t)
	for _, f := range scan.FileNameFindings {
		t.Errorf("T25b: %s", f)
	}
	for _, s := range scan.FileNameStale {
		t.Errorf("T25b: %s", s)
	}
	if len(scan.FileNameFindings) > 0 || len(scan.FileNameStale) > 0 {
		t.Fatalf("%d stage-codename file-name finding(s), %d stale exemption(s)", len(scan.FileNameFindings), len(scan.FileNameStale))
	}
}

func TestStageCodenameGateBackendStrings(t *testing.T) {
	scan := stageCodenameScanT(t)
	for _, f := range scan.StringFindings {
		t.Errorf("T25b: %s", f)
	}
	for _, s := range scan.StringStale {
		t.Errorf("T25b: %s", s)
	}
	if len(scan.StringFindings) > 0 || len(scan.StringStale) > 0 {
		t.Fatalf("%d stage-codename string finding(s), %d stale exemption(s)", len(scan.StringFindings), len(scan.StringStale))
	}
}

func TestStageCodenameGateFrontendRuntimeText(t *testing.T) {
	scan := stageCodenameScanT(t)
	for _, f := range scan.FrontendFindings {
		t.Errorf("T25b: %s", f)
	}
	if len(scan.FrontendFindings) > 0 {
		t.Fatalf("%d stage-codename frontend runtime-text finding(s)", len(scan.FrontendFindings))
	}
}

// TestStageCodenameGateSurfaceNonVacuous proves the gate is actually scanning a
// non-trivial surface and that the allow set was derived, not assumed. The 26
// distinct identifier names is a LOWER BOUND measured on the T25b tree
// (12 derived priority constants + 5 priority-semantic identifiers + 9 lexical
// collisions), not a closed value table: a broken walk or a silently-shrinking
// surface fails here.
func TestStageCodenameGateSurfaceNonVacuous(t *testing.T) {
	scan := stageCodenameScanT(t)
	t.Logf("surface: go_files=%d identifiers=%d distinct_codename_idents=%d string_literals=%d file_names=%d tsx_files=%d derived_priority_names=%d derived_priority_tokens=%v",
		scan.GoFiles, scan.IdentifierCount, len(scan.DistinctCodenameIdents), scan.StringLiteralCount, scan.FileNameCount, scan.TSXFileCount, len(scan.PriorityNames), scan.PriorityTokens)

	if scan.GoFiles == 0 || scan.IdentifierCount == 0 {
		t.Fatalf("identifier surface is empty (files=%d identifiers=%d); the gate would pass vacuously", scan.GoFiles, scan.IdentifierCount)
	}
	if scan.StringLiteralCount == 0 {
		t.Fatalf("non-test backend string-literal surface is empty; the gate would pass vacuously")
	}
	if scan.FileNameCount == 0 {
		t.Fatalf("file-name surface is empty; the gate would pass vacuously")
	}
	if scan.TSXFileCount == 0 {
		t.Fatalf("frontend .tsx surface is empty; the gate would pass vacuously")
	}
	if len(scan.PriorityNames) < 12 {
		t.Fatalf("priority derivation produced %d names, want at least 12 (the three P0..P3 constant families): %v", len(scan.PriorityNames), scan.PriorityNames)
	}
	for _, tok := range []string{"P0", "P1", "P2", "P3"} {
		found := false
		for _, got := range scan.PriorityTokens {
			if got == tok {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("priority derivation did not produce value token %s (got %v); P0..P3 string values would be flagged", tok, scan.PriorityTokens)
		}
	}
	if len(scan.DistinctCodenameIdents) < 26 {
		t.Fatalf("derived identifier surface has %d distinct codename-token names, want at least 26; the scan surface may have silently shrunk. If this is a legitimate cleanup (renaming a lexical collision or a priority fixture), lower this floor and delete the matching identifier exemption IN THE SAME change set: %v", len(scan.DistinctCodenameIdents), scan.DistinctCodenameIdents)
	}
}
