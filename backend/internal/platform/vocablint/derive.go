package vocablint

// The vocabulary-derivation wrapper over platform/govocab: it turns the
// resolver's declarations into the protected value set, and it enforces the two
// escape-face rules that make a non-literal allowlist safe:
//
//   - a declaration the resolver could NOT evaluate must be explicitly classified
//     (loud), never silently dropped — including an unresolvable MEMBER of an
//     otherwise resolvable set (govocab I1);
//   - an allowlist entry whose declaration DOES resolve to a string is rejected,
//     so the allowlist can never hide a vocabulary value from the guard;
//   - an entry whose declaration disappeared is reported stale.

import (
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/govocab"
)

// Derivation is the result of deriving the protected value set. Problems are
// returned rather than fatal so a gate's own properties can be proven on
// synthetic vocabulary files (a real bad sample, not a hand-built map).
type Derivation struct {
	// Values maps a value to ALL the "package.Name" labels that define it.
	Values   map[string][]string
	Problems []string
}

// NeedsExplicitClassification reports whether a derived declaration must be
// explicitly classified: the resolver could not evaluate its string-shaped value
// (or a MEMBER of its value set) AND it is not in the non-literal allowlist.
// IsString is deliberately NOT a short-circuit: a partially resolvable set carries
// both signals, and its unresolvable member must still be classified (I1). A
// provably non-string declaration (a numeric expression, errors.New(...), a
// regexp) never needs classification.
func NeedsExplicitClassification(name string, decl govocab.Decl, allowlist map[string]string) bool {
	if !decl.Unresolved {
		return false
	}
	_, allowed := allowlist[name]
	return !allowed
}

// DeriveVocabulary derives every protected string value from the given vocabulary
// files. allowlist maps a declaration NAME to the reason it carries no string
// value the guarded files must source.
//
// The problem wording is inherited verbatim from the handoff gate this machinery
// was extracted from (T11e-0 is a zero-behavior-change move); generalizing the
// "handoff" nouns is a follow-up polish, not part of the migration.
func DeriveVocabulary(resolver *govocab.Resolver, files []string, allowlist map[string]string) Derivation {
	out := Derivation{Values: map[string][]string{}}
	seenNonLiteral := make(map[string]bool)
	for _, file := range files {
		decls, err := resolver.Decls(file, false)
		if err != nil {
			out.Problems = append(out.Problems, err.Error())
			continue
		}
		for _, decl := range decls {
			_, allowed := allowlist[decl.Name]
			if decl.IsString {
				// An allowlist entry may only classify what the resolver could
				// NOT evaluate. A fully resolved declaration must be derived,
				// never hidden behind an entry (the escape face H-B1 closes).
				if allowed && !decl.Unresolved {
					out.Problems = append(out.Problems, "non-literal allowlist entry "+decl.Name+
						" resolves to the string value(s) "+strings.Join(decl.Values, ",")+
						"; remove the entry so the guard derives and protects the value instead of hiding it")
					continue
				}
				for _, value := range decl.Values {
					if value == "" {
						continue
					}
					out.Values[value] = append(out.Values[value], decl.Pkg+"."+decl.Name)
				}
				// I1: a partially resolvable set keeps its resolved members
				// protected AND demands classification of the rest.
				if NeedsExplicitClassification(decl.Name, decl, allowlist) {
					out.Problems = append(out.Problems, "vocabulary declaration "+decl.Pkg+"."+decl.Name+
						" has an unresolvable string-shaped value or set member; "+
						"define it as a string literal so the guard can derive it, "+
						"otherwise allowlist it in nonLiteralHandoffConstants with a reason")
					continue
				}
				if allowed {
					seenNonLiteral[decl.Name] = true
				}
				continue
			}
			if NeedsExplicitClassification(decl.Name, decl, allowlist) {
				out.Problems = append(out.Problems, "vocabulary declaration "+decl.Pkg+"."+decl.Name+
					" has a string-shaped value the handoff literal guard cannot resolve; "+
					"if it is a string value the handoff must source, define it as a string literal, "+
					"otherwise allowlist it in nonLiteralHandoffConstants with a reason")
				continue
			}
			if allowed {
				seenNonLiteral[decl.Name] = true
			}
		}
	}
	for name := range allowlist {
		if !seenNonLiteral[name] {
			out.Problems = append(out.Problems, "non-literal allowlist entry "+name+
				" is unused (declaration removed/renamed or now a string literal); remove it")
		}
	}
	if len(out.Values) == 0 {
		out.Problems = append(out.Problems, "derived no domain values; vocabulary derivation is broken")
	}
	return out
}
