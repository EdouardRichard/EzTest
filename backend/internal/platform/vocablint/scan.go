package vocablint

// The regex-over-stripped-source scanner: every quoted occurrence ('value' or
// "value") of a derived vocabulary value in a production file, minus the
// occurrences an adjudicated fragment sanctions. Comments and struct tags are
// stripped mechanically first, so doc counter-examples and json:"..." wire names
// never collide.

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Fragment is one exemption fragment BOUND TO THE FILE it may sanction in.
//
// File is REQUIRED (T11e-5 Part A1, H-A22① + T11e-4 M2). Before it existed the
// allowlist was global over the whole merged scan surface, so a short fragment
// (`case "failed":`, `case "text":`, `return "error"`) sanctioned the SAME TEXT in
// ANY scanned file — and because the original site still matched, the stale check
// stayed quiet too. A new real status switch in a different file would have been
// silently waved through. With the binding, a fragment can only ever sanction
// occurrences in its own file; the same text elsewhere is a finding.
//
// File is a repository-relative slash path, exactly as the gate reports its scan
// surface. Text is one whitespace-normalized source line (see Exemption's
// single-line constraint).
type Fragment struct {
	File string
	Text string
}

// FileFragments binds every text to one file, so a gate declares its per-file
// exemption sites once instead of repeating the path on every line.
func FileFragments(file string, texts ...string) []Fragment {
	out := make([]Fragment, 0, len(texts))
	for _, text := range texts {
		out = append(out, Fragment{File: file, Text: text})
	}
	return out
}

// JoinFragments concatenates per-file fragment groups into one entry's table,
// preserving order (the stale check addresses fragments by INDEX).
func JoinFragments(groups ...[]Fragment) []Fragment {
	var out []Fragment
	for _, group := range groups {
		out = append(out, group...)
	}
	return out
}

// AllowEntry is a fragment-scoped exemption: a value may appear as a quoted
// literal only in the occurrences one of Fragments spans, in that fragment's own
// file, for the given Reason.
type AllowEntry struct {
	Reason    string
	Fragments []Fragment
}

// FragmentKey identifies ONE allowlist fragment by its value and its INDEX in
// that entry's table. Keying by fragment TEXT (the pre-T11e-5 shape) let two
// entries — or two fragments of one entry — share a hit count, so an exemption
// that sanctioned nothing stayed green while its twin was hit (H-A22②).
type FragmentKey struct {
	Value string
	Index int
}

// Finding is one unexempted occurrence. Name lists ALL the "package.Name" labels
// that define the value, so the diagnostic hint is not last-writer-wins.
type Finding struct {
	File  string
	Line  int
	Value string
	Name  string
}

// OccurrenceSite identifies ONE occurrence of a guarded value: its source LINE
// and its INDEX among that value's occurrences on that line (source order).
//
// Line alone is not enough (T11e-6 review Minor 3): a frozen template line
// `<section id="issues" data-section="issues">` carries the value TWICE, and a
// fragment spanning the whole line adjudicates both. Keying sites by line let one
// fragment stand for two occurrences, so repaying either went undetected — the
// same defect M5 closed for two identical LINES, one granularity finer.
type OccurrenceSite struct {
	Line  int
	Index int
}

// String renders a site for diagnostics: "123" for the first occurrence on a line,
// "123#1" for the second, so a report names the exact occurrence.
func (s OccurrenceSite) String() string {
	if s.Index == 0 {
		return fmt.Sprint(s.Line)
	}
	return fmt.Sprintf("%d#%d", s.Line, s.Index)
}

// Scan is one file's scan result: the unexempted occurrences, the values seen at
// least once (diagnostics) and, per allowlist fragment (keyed by value + fragment
// index), how many occurrences it sanctioned and WHICH SITES it sanctioned. The
// site list is what makes the exemption registry 1:1 with the occurrences it
// adjudicates (T11e-6 M5 + review Minor 3).
type Scan struct {
	Findings      []Finding
	SeenValues    map[string]bool
	FragmentHits  map[FragmentKey]int
	FragmentSites map[FragmentKey][]OccurrenceSite
}

// ScanLiterals returns every quoted literal occurrence in src (comments and
// struct tags stripped) whose value is a derived vocabulary value NOT sanctioned
// by an occurrence-scoped, FILE-SCOPED allowlist fragment. file is the same
// repository-relative path the fragments carry.
func ScanLiterals(file, src string, values map[string][]string, allowlist map[string]AllowEntry) Scan {
	stripped := StripCommentsAndTags(src)
	scan := Scan{SeenValues: map[string]bool{}, FragmentHits: map[FragmentKey]int{}, FragmentSites: map[FragmentKey][]OccurrenceSite{}}
	keys := make([]string, 0, len(values))
	for v := range values {
		keys = append(keys, v)
	}
	sort.Strings(keys)
	type site struct {
		line  int
		token string
	}
	for _, v := range keys {
		re := regexp.MustCompile(`'` + regexp.QuoteMeta(v) + `'` + `|"` + regexp.QuoteMeta(v) + `"`)
		// One evaluation per (line, token): occurrence-scoped counting already
		// accounts for every occurrence of that token on the line.
		seenSite := map[site]bool{}
		var sites []site
		positions := map[site]int{}
		for _, loc := range re.FindAllStringIndex(stripped, -1) {
			scan.SeenValues[v] = true
			s := site{line: 1 + strings.Count(stripped[:loc[0]], "\n"), token: stripped[loc[0]:loc[1]]}
			if !seenSite[s] {
				seenSite[s] = true
				sites = append(sites, s)
				positions[s] = loc[0]
			}
		}
		entry := allowlist[v]
		// Only THIS file's fragments participate. The index recorded in hits is
		// the index in entry.Fragments (not in the filtered slice), so the stale
		// check addresses the same entry the gate wrote.
		var exemptions []Exemption
		var indices []int
		for i, fragment := range entry.Fragments {
			if fragment.File != file {
				continue
			}
			exemptions = append(exemptions, Exemption{Fragment: fragment.Text, Reason: entry.Reason})
			indices = append(indices, i)
		}
		for _, s := range sites {
			line := lineContaining(stripped, positions[s])
			covered, perExemption := OccurrenceCoverage(line, s.token, exemptions)
			ranks := occurrenceRanks(line, s.token, []string{`'` + v + `'`, `"` + v + `"`})
			for hit, occurrenceIndices := range perExemption {
				key := FragmentKey{Value: v, Index: indices[hit]}
				scan.FragmentHits[key] += len(occurrenceIndices)
				for _, j := range occurrenceIndices {
					scan.FragmentSites[key] = append(scan.FragmentSites[key], OccurrenceSite{Line: s.line, Index: ranks[j]})
				}
			}
			remaining := 0
			for _, c := range covered {
				if !c {
					remaining++
				}
			}
			if remaining > 0 {
				scan.Findings = append(scan.Findings, Finding{
					File: file, Line: s.line, Value: v, Name: strings.Join(values[v], ", "),
				})
			}
		}
	}
	return scan
}

// AllowlistShapeProblems rejects an entry without a reason or fragments, and a
// fragment without a file binding or text. The caller fails on the first problem
// (an ill-formed exemption table is not a usable adjudication).
func AllowlistShapeProblems(allowlist map[string]AllowEntry) []string {
	var problems []string
	for value, entry := range allowlist {
		if strings.TrimSpace(entry.Reason) == "" || len(entry.Fragments) == 0 {
			problems = append(problems, fmt.Sprintf("allowlist entry %q needs a non-empty reason and at least one fragment", value))
		}
		for i, fragment := range entry.Fragments {
			if strings.TrimSpace(fragment.File) == "" || strings.TrimSpace(fragment.Text) == "" {
				problems = append(problems, fmt.Sprintf("allowlist entry %q fragment #%d needs a non-empty file binding and text "+
					"(an unbound fragment would sanction the same text in every scanned file)", value, i))
			}
		}
	}
	sort.Strings(problems)
	return problems
}

// StaleFragmentProblems reports every allowlist fragment that sanctioned nothing:
// the exempted occurrence was repaid or moved, so the registry must be updated in
// the same change set (fragment-level, not value-level). hits is keyed by value +
// fragment INDEX, so two fragments with identical text never share a count.
func StaleFragmentProblems(allowlist map[string]AllowEntry, hits map[FragmentKey]int) []string {
	var problems []string
	for value, entry := range allowlist {
		for i, fragment := range entry.Fragments {
			if hits[FragmentKey{Value: value, Index: i}] == 0 {
				problems = append(problems, fmt.Sprintf("allowlist entry %q fragment #%d (%s) %q sanctioned nothing; remove the stale exemption in the same change set",
					value, i, fragment.File, fragment.Text))
			}
		}
	}
	sort.Strings(problems)
	return problems
}

// FragmentSiteProblems enforces ONE FRAGMENT PER OCCURRENCE (T11e-6 M5 + review
// Minor 3; T11e-5 Minor 5).
//
// The defect it closes: a fragment matches by TEXT, so when two source lines of one
// file carry the same text (versionadapter/requirements.go:50 and :63 both read
// `return tools.RequirementSummary{}, a.missing("requirements")`), ONE fragment
// sanctioned BOTH. Repaying either site left the fragment non-vacuous, so the stale
// check stayed quiet and the registry was never forced to shrink -- the inventory
// and the code could drift apart silently. Measured on the T11e-6 tree this was not
// a one-off: 14 of 144 permanent fragments sanctioned 2..4 sites each.
//
// The granularity is the OCCURRENCE, not the line (review Minor 3): a frozen
// `<section id="issues" data-section="issues">` carries the value twice on ONE
// line, and a whole-line fragment spans both. Counting lines would let one
// fragment stand for two occurrences and hide the repayment of either.
//
// The fix keys fragments by (file, SITE) without storing a line number: fragments of
// one entry that share a file and a whitespace-normalized text form a group, and the
// group must declare exactly as many fragments as it sanctions distinct occurrences.
// The site identity is DERIVED from the scan, so an unrelated edit that shifts line
// numbers cannot stale a whole file's exemptions (which storing hand-written line
// numbers would do), while every drift direction stays loud:
//
//   - repaying one of N sanctioned occurrences -> N fragments, N-1 sites -> problem;
//   - a NEW same-text occurrence appearing     -> N fragments, N+1 sites -> problem.
//
// sites maps each fragment (value + index) to the occurrences it sanctioned.
func FragmentSiteProblems(allowlist map[string]AllowEntry, sites map[FragmentKey][]OccurrenceSite) []string {
	type groupKey struct {
		value string
		file  string
		text  string
	}
	members := map[groupKey][]int{}
	var order []groupKey
	for value, entry := range allowlist {
		for i, fragment := range entry.Fragments {
			key := groupKey{value: value, file: fragment.File, text: strings.Join(strings.Fields(fragment.Text), " ")}
			if _, seen := members[key]; !seen {
				order = append(order, key)
			}
			members[key] = append(members[key], i)
		}
	}
	var problems []string
	for _, key := range order {
		seen := map[OccurrenceSite]bool{}
		for _, i := range members[key] {
			for _, site := range sites[FragmentKey{Value: key.value, Index: i}] {
				seen[site] = true
			}
		}
		if len(seen) == 0 {
			// Vacuous fragments are the stale check's business, not this one.
			continue
		}
		if len(members[key]) == len(seen) {
			continue
		}
		problems = append(problems, fmt.Sprintf("allowlist entry %q declares %d fragment(s) for %q in %s but they sanction %d occurrence(s) %v; "+
			"declare ONE FRAGMENT PER OCCURRENCE so repaying any single occurrence is detected (1:1)",
			key.value, len(members[key]), key.text, key.file, len(seen), sortedSites(seen)))
	}
	sort.Strings(problems)
	return problems
}

func sortedSites(set map[OccurrenceSite]bool) []string {
	sites := make([]OccurrenceSite, 0, len(set))
	for s := range set {
		sites = append(sites, s)
	}
	sort.Slice(sites, func(i, j int) bool {
		if sites[i].Line != sites[j].Line {
			return sites[i].Line < sites[j].Line
		}
		return sites[i].Index < sites[j].Index
	})
	out := make([]string, 0, len(sites))
	for _, s := range sites {
		out = append(out, s.String())
	}
	return out
}

// MergeAllowlists unions two exemption tables into a NEW table. A value present
// in both keeps both reasons (concatenated, so neither adjudication is lost) and
// both fragment tables (extra's fragments appended, so base's indices are
// unchanged). It is how a gate keeps its permanent adjudications separate from a
// temporary, routed registry without weakening either.
func MergeAllowlists(base, extra map[string]AllowEntry) map[string]AllowEntry {
	out := make(map[string]AllowEntry, len(base)+len(extra))
	for value, entry := range base {
		out[value] = AllowEntry{Reason: entry.Reason, Fragments: append([]Fragment(nil), entry.Fragments...)}
	}
	for value, entry := range extra {
		if existing, ok := out[value]; ok {
			out[value] = AllowEntry{
				Reason:    existing.Reason + " " + entry.Reason,
				Fragments: append(existing.Fragments, entry.Fragments...),
			}
			continue
		}
		out[value] = AllowEntry{Reason: entry.Reason, Fragments: append([]Fragment(nil), entry.Fragments...)}
	}
	return out
}

// ExemptionShapeProblems rejects an exemption with an empty fragment or reason,
// reporting its INDEX so the gate can name the offending entry.
func ExemptionShapeProblems(exemptions []Exemption) []string {
	var problems []string
	for i, ex := range exemptions {
		if strings.TrimSpace(ex.Fragment) == "" || strings.TrimSpace(ex.Reason) == "" {
			problems = append(problems, fmt.Sprintf("status vocabulary exemption #%d has an empty fragment or reason", i))
		}
	}
	return problems
}

// StaleExemptionProblems reports every exemption INDEX that sanctioned nothing.
// Index-keyed so two exemptions with the same fragment text never share a count.
func StaleExemptionProblems(exemptions []Exemption, hits map[int]int) []string {
	var problems []string
	for i, ex := range exemptions {
		if hits[i] == 0 {
			problems = append(problems, fmt.Sprintf("status vocabulary exemption %q (%s) sanctioned nothing; remove the stale exemption in the same change set", ex.Fragment, ex.Reason))
		}
	}
	return problems
}

// occurrenceRanks maps each occurrence index of token on line to its rank among
// ALL occurrences of the value on that line, counting BOTH quote forms. Without
// it, a line carrying 'v' and "v" would give two distinct occurrences the same
// (line, index) identity and collapse them into one site.
func occurrenceRanks(line, token string, forms []string) []int {
	norm := strings.Join(strings.Fields(line), " ")
	var all []int
	for _, form := range forms {
		all = append(all, AllIndices(norm, form)...)
	}
	sort.Ints(all)
	rank := make(map[int]int, len(all))
	for i, pos := range all {
		rank[pos] = i
	}
	starts := AllIndices(norm, token)
	out := make([]int, len(starts))
	for j, pos := range starts {
		out[j] = rank[pos]
	}
	return out
}

func lineContaining(src string, pos int) string {
	start := strings.LastIndex(src[:pos], "\n") + 1
	end := strings.Index(src[pos:], "\n")
	if end < 0 {
		return src[start:]
	}
	return src[start : pos+end]
}

var structTagPattern = regexp.MustCompile(`\w+:"`)

// StripCommentsAndTags blanks // and /* */ comments and struct-tag backtick
// literals (content matching \w+:"), preserving newlines and byte positions so line
// numbers stay accurate. String/rune/raw literals that are not struct tags are kept
// so a quoted value inside code is still matched.
func StripCommentsAndTags(src string) string {
	out := []byte(src)
	n := len(src)
	for i := 0; i < n; {
		c := src[i]
		switch {
		case c == '/' && i+1 < n && src[i+1] == '/':
			j := i
			for j < n && src[j] != '\n' {
				out[j] = ' '
				j++
			}
			i = j
		case c == '/' && i+1 < n && src[i+1] == '*':
			out[i], out[i+1] = ' ', ' '
			j := i + 2
			for j+1 < n && !(src[j] == '*' && src[j+1] == '/') {
				if src[j] != '\n' {
					out[j] = ' '
				}
				j++
			}
			if j+1 < n {
				out[j], out[j+1] = ' ', ' '
				j += 2
			}
			i = j
		case c == '"' || c == '\'' || c == '`':
			quote := c
			j := i + 1
			for j < n {
				if src[j] == '\\' && quote != '`' {
					j += 2
					continue
				}
				if src[j] == quote {
					j++
					break
				}
				j++
			}
			// A backtick literal that is a struct tag is blanked entirely.
			if quote == '`' && j <= n && structTagPattern.MatchString(src[i:j]) {
				for k := i; k < j && k < n; k++ {
					if out[k] != '\n' {
						out[k] = ' '
					}
				}
			}
			i = j
		default:
			i++
		}
	}
	return string(out)
}
