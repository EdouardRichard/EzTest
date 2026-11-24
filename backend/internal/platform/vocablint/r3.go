package vocablint

// The R3 vocabulary SELECTION authority (Ruling T11d-R1, extended by T11e-5
// Part C). Before T11e-5 each gate re-implemented the same selection beside its
// own copy of the group slicing, and the rule was three name patterns only:
//
//	R3: the DECLARED TYPE name, the declaration's OWN name, or ANY name of its
//	    const/var GROUP matches the semantic pattern.
//
// That left 19+ measured blind-spot groups unprotected (H-A21①②④⑪): a const group
// with no declared type, no semantic name and no authority function is invisible
// to all three, so the live predicates it owns stayed naked while the gate stayed
// green. Widening to "every domain string" (R2) is NOT the fix — T11d proved it
// swallows execution/domain/requirement_deviation.go's NLP word lists.
//
// T11e-5 adds two DERIVED rules and one explicit, fail-closed registration face:
//
//	R3-E (exported group): a const/var group with at least one EXPORTED name is
//	     vocabulary. An exported domain constant is part of the context's
//	     published vocabulary — the value every other layer must source. The NLP
//	     word lists, the CSV header row, the dash glyph and the id prefixes are all
//	     UNEXPORTED implementation data, so they stay out. This rule alone covers
//	     76 of the measured blind-spot groups (agent.Team, chain.Layer*,
//	     execution.TerminalCause*/NextAction*, version.Conversation*/MessagePart*,
//	     version.Attachment* status enums, knowledge.Scope, codeintel.EdgeRel/
//	     Confidence/CandidateSource, design.CaseTrigger*/Source*/Assertion*,
//	     storage.Format*, llm.Protocol, settings.Type, identity.Role, ...).
//
//	R3-R (registry set): a set (slice/array elements or map keys) whose EVERY
//	     member is a bare identifier selects the declarations it enumerates. A
//	     registry enumerates vocabulary BY REFERENCE; it is the authority-function
//	     shape without needing a function. This covers chain's unexported
//	     countRequirements…countEvidence through chainCountKeys. A literal-member
//	     list (every NLP word list, csvHeader) is provably not a registry.
//
//	Registered groups: the residual — an UNEXPORTED set of literal strings that is
//	     nonetheless a closed vocabulary (execution.executorLimitationCodes). It is
//	     registered by DECLARATION NAME (scope), never by value: the values are
//	     still derived. A registration that names a missing declaration, a
//	     non-string group, or a group the rules already select is a problem, so the
//	     face cannot rot into a silent second list.
//
// Every rule is a pure function of the AST/derived declarations. The counter-example
// that must never be selected is the NLP word lists; TestR3RulesExcludeNLPWordLists
// pins that on the real source, and the synthetic positive/negative pairs pin each
// rule's boundary.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"slices"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/govocab"
)

// RegisteredGroup names one residual const/var group the derived rules do not
// select. Pkg is the declared package name, Name any ONE declaration of the group
// (the whole group joins), Reason why it is vocabulary despite being unexported
// and literal-valued.
type RegisteredGroup struct {
	Pkg    string
	Name   string
	Reason string
}

// R3Selector is a gate's selection configuration: the semantic pattern plus the
// residual registrations. The RULES themselves are not configurable — a gate
// cannot opt out of R3-E/R3-R and silently unprotect a blind-spot group.
type R3Selector struct {
	Pattern    *regexp.Regexp
	Registered []RegisteredGroup
}

// GroupIsVocabulary is R3 + R3-E on one group: the declared type name, any
// declaration's own name, or any name of the group matches the semantic pattern,
// OR the group declares an exported name.
func (s R3Selector) GroupIsVocabulary(group []ConstSpec) bool {
	for _, spec := range group {
		if s.Pattern != nil && s.Pattern.MatchString(spec.Type) {
			return true
		}
		for _, name := range spec.Names {
			if s.Pattern != nil && s.Pattern.MatchString(name) {
				return true
			}
			if ast.IsExported(name) {
				return true
			}
		}
	}
	return false
}

// RegistryReferences returns the names a group enumerates BY REFERENCE (R3-R):
// every value expression must be a set literal (slice/array elements, or the keys
// of a map) with at least one member, and EVERY member of EVERY set must be a bare
// same-package identifier. Anything else — a literal member, a call, a selector,
// a scalar alias — returns nil, so the rule can never reach a word list.
func RegistryReferences(group []ConstSpec) []string {
	var names []string
	sawSet := false
	for _, spec := range group {
		for _, expr := range spec.Exprs {
			lit, ok := expr.(*ast.CompositeLit)
			if !ok {
				return nil
			}
			var members []ast.Expr
			switch lit.Type.(type) {
			case *ast.ArrayType:
				members = lit.Elts
			case *ast.MapType:
				for _, elt := range lit.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						return nil
					}
					members = append(members, kv.Key)
				}
			default:
				return nil
			}
			if len(members) == 0 {
				return nil
			}
			sawSet = true
			for _, member := range members {
				ident, ok := member.(*ast.Ident)
				if !ok {
					return nil
				}
				names = append(names, ident.Name)
			}
		}
	}
	if !sawSet {
		return nil
	}
	return names
}

// Authority is one enum authority function's values, labelled with the function
// that produced them. Values are taken by CALLING the authority, never by a
// hand-copied table.
type Authority struct {
	Label  string
	Values []string
}

// AuthorityOf calls an enum authority and labels its values. The generic
// conversion keeps every authority's label next to its own call, so reordering
// the list can never attach one authority's values to another's label.
func AuthorityOf[T ~string](label string, values []T) Authority {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, string(value))
	}
	return Authority{Label: label, Values: out}
}

// R3Vocabulary is the derived protected value set, the per-declared-type value
// counts the anti-vacuity floors check, the selected "package.Name" labels (for
// coverage assertions) and the derivation problems. Problems are returned rather
// than fatal so the machinery stays a pure production package and a gate can
// cache one derivation for all of its tests (H-A22④).
type R3Vocabulary struct {
	Values   map[string][]string
	PerType  map[string][]string
	Selected map[string]bool
	Problems []string
}

// AddAuthorities reinforces the derived set by reference: an enum whose type and
// names carry no semantic pattern still joins through its own authority function.
func (v *R3Vocabulary) AddAuthorities(authorities []Authority) {
	for _, authority := range authorities {
		for _, value := range authority.Values {
			if value == "" {
				continue
			}
			if !slices.Contains(v.Values[value], authority.Label) {
				v.Values[value] = append(v.Values[value], authority.Label)
			}
		}
	}
}

// AddValues unions another derived value set in, label by label. It is the
// ADDITIVE face a gate uses for a vocabulary source outside the domain layer
// (version's application-layer extraction statuses): 扩面不弱化, never a
// replacement of the derived set.
func (v *R3Vocabulary) AddValues(values map[string][]string) {
	for value, labels := range values {
		for _, label := range labels {
			if !slices.Contains(v.Values[value], label) {
				v.Values[value] = append(v.Values[value], label)
			}
		}
	}
}

// DeriveR3 derives the protected value set from the vocabulary files under the
// selector's rules. It owns the whole selection: the resolver call, the loud
// classification rules (DeriveVocabulary), the elision-aware group walk, R3/R3-E/
// R3-R, the registration face and the per-type counts. A gate keeps only its
// SCOPE (which files define the vocabulary, which files it guards), its authority
// functions, its floors and its adjudicated exemptions.
func DeriveR3(resolver *govocab.Resolver, files []string, selector R3Selector, nonLiteral map[string]string) R3Vocabulary {
	out := R3Vocabulary{
		Values:   map[string][]string{},
		PerType:  map[string][]string{},
		Selected: map[string]bool{},
	}
	derived := DeriveVocabulary(resolver, files, nonLiteral)
	out.Problems = append(out.Problems, derived.Problems...)

	type groupRec struct {
		pkg    string
		specs  []ConstSpec
		labels []string
		byRule bool
	}
	var groups []groupRec
	labelToGroup := map[string]int{}
	declByLabel := map[string]govocab.Decl{}
	for _, path := range files {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			out.Problems = append(out.Problems, "parse vocabulary file "+path+": "+err.Error())
			continue
		}
		decls, err := resolver.Decls(path, false)
		if err != nil {
			out.Problems = append(out.Problems, "derive declarations of "+path+": "+err.Error())
			continue
		}
		pkg := file.Name.Name
		for _, decl := range decls {
			declByLabel[decl.Pkg+"."+decl.Name] = decl
			pkg = decl.Pkg
		}
		for _, tok := range []token.Token{token.CONST, token.VAR} {
			for _, group := range ConstGroups(file, tok) {
				rec := groupRec{pkg: pkg, specs: group}
				for _, spec := range group {
					for _, name := range spec.Names {
						label := pkg + "." + name
						rec.labels = append(rec.labels, label)
						labelToGroup[label] = len(groups)
					}
				}
				if selector.GroupIsVocabulary(group) {
					rec.byRule = true
				}
				groups = append(groups, rec)
			}
		}
	}
	// SELECTION runs only after EVERY group of EVERY file has been indexed. R3-R
	// resolves a registry's references through labelToGroup, so selecting while
	// collecting could only ever reach groups declared EARLIER: a forward
	// reference marked the referenced NAME Selected but left the rest of its
	// group unselected and its per-type bucket empty (T11e-5 §11.11, closed by
	// T11e-6 M6). With the index complete, a reference in either direction
	// adopts the whole referenced group.
	//
	// Every group's references are resolved even when the group is ALREADY
	// selected (T11e-6 review Minor 4): a registry adopted by an earlier
	// registry must still enumerate its own references, or a chain A -> B -> C
	// would stop at B. Re-resolving an already-selected group is idempotent.
	for i := range groups {
		refs := RegistryReferences(groups[i].specs)
		if len(refs) == 0 {
			continue
		}
		groups[i].byRule = true
		for _, ref := range refs {
			label := groups[i].pkg + "." + ref
			out.Selected[label] = true
			if gi, ok := labelToGroup[label]; ok {
				groups[gi].byRule = true
			}
		}
	}
	// A group adopted by an EARLIER registry reference is marked byRule after its
	// own turn in the walk, so group membership is propagated to the Selected
	// labels in a separate pass over the recorded groups.
	for _, rec := range groups {
		if !rec.byRule {
			continue
		}
		for _, label := range rec.labels {
			out.Selected[label] = true
		}
	}
	for _, registration := range selector.Registered {
		label := registration.Pkg + "." + registration.Name
		if strings.TrimSpace(registration.Reason) == "" {
			out.Problems = append(out.Problems, "registered vocabulary group "+label+" has an empty reason")
		}
		gi, ok := labelToGroup[label]
		if !ok {
			out.Problems = append(out.Problems, "registered vocabulary group "+label+
				" does not exist (declaration removed/renamed or not a const/var group); remove or fix the registration")
			continue
		}
		rec := groups[gi]
		if rec.byRule {
			out.Problems = append(out.Problems, "registered vocabulary group "+label+
				" is already selected by the derived R3 rules; remove the redundant registration")
			continue
		}
		decl, ok := declByLabel[label]
		if !ok || !decl.IsString {
			out.Problems = append(out.Problems, "registered vocabulary group "+label+
				" carries no derived string value; a registration may only name a string vocabulary")
			continue
		}
		for _, l := range rec.labels {
			out.Selected[l] = true
		}
		groups[gi].byRule = true
	}
	for _, rec := range groups {
		if !rec.byRule {
			continue
		}
		for _, spec := range rec.specs {
			bucket := spec.Type
			if bucket == "" {
				bucket = "(untyped)"
			}
			for _, name := range spec.Names {
				decl, ok := declByLabel[rec.pkg+"."+name]
				if !ok || !decl.IsString {
					continue
				}
				out.PerType[bucket] = append(out.PerType[bucket], decl.Values...)
			}
		}
	}
	for value, labels := range derived.Values {
		for _, label := range labels {
			if out.Selected[label] {
				out.Values[value] = labels
				break
			}
		}
	}
	if len(out.Values) == 0 {
		out.Problems = append(out.Problems, "no vocabulary values derived; the gate would go vacuous")
	}
	return out
}
