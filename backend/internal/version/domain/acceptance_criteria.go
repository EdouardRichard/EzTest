package version

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// AcceptanceCriterion is one Gherkin-shaped acceptance criterion of a
// requirement revision (design v1.3.1 §5.1).
//
// ID is server-assigned and stable across revisions (§3.6). A model or a human
// never chooses it: AssignACIDs derives the whole set from the previous
// revision plus the incoming content, so the same criterion keeps its id while
// it is unchanged and a changed one is a new criterion with a new id.
type AcceptanceCriterion struct {
	ID    string `json:"id"`
	Given string `json:"given"`
	When  string `json:"when"`
	Then  string `json:"then"`
}

// Acceptance-criterion limits. PartMax is the §5.1 bound for each of
// given/when/then. MaxAcceptanceCriteria caps the array: §5.1 gives no item
// count, but the flat TEXT column this replaced was bounded at 20000 characters
// in total, and an uncapped array would let one revision grow without bound. 200
// matches §5.2's MaxCasesPerRequirement, the other per-requirement cardinality
// in the design.
const (
	AcceptanceCriterionPartMax = 2000
	MaxAcceptanceCriteria      = 200
)

// ACIDPrefix is the prefix of every server-assigned acceptance-criterion id.
// The full shape is `AC-\d+` (design §3.6); the pattern below is the canonical
// form the server produces -- a positive decimal with no leading zeros -- so an
// id and its sequence number always round-trip.
const ACIDPrefix = "AC-"

var acIDPattern = regexp.MustCompile(`^AC-([1-9][0-9]*)$`)

// NormalizeACText canonicalizes one given/when/then value: surrounding
// whitespace is dropped and every inner whitespace run collapses to one space.
//
// This is the single definition of "same content" for acceptance criteria. Both
// the id-inheritance match key and the ContentHash payload call it, so the two
// can never disagree about whether two criteria are identical.
func NormalizeACText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

// FormatACID renders the canonical id of a sequence number: 1 -> "AC-1".
func FormatACID(sequence int) string {
	return ACIDPrefix + strconv.Itoa(sequence)
}

// ACIDSequence returns the numeric part of a canonical AC id. It reports false
// for anything the server would not have produced (empty, "AC-0", "AC-007",
// "ac-1", a bare number), so a malformed id can never be inherited from.
func ACIDSequence(id string) (int, bool) {
	match := acIDPattern.FindStringSubmatch(strings.TrimSpace(id))
	if match == nil {
		return 0, false
	}
	sequence, err := strconv.Atoi(match[1])
	if err != nil || sequence <= 0 {
		return 0, false
	}
	return sequence, true
}

// AcceptanceCriterionKey is the identity of one criterion: the normalized
// given/when/then triple. The id is deliberately not part of the key -- the key
// is what decides the id. The separator cannot occur in a normalized part,
// because NormalizeACText collapses every whitespace run to one space.
//
// It is exported because the application layer needs the same notion of "the
// same criterion" when it merges duplicate requirements; two definitions of it
// would drift.
func AcceptanceCriterionKey(criterion AcceptanceCriterion) string {
	return NormalizeACText(criterion.Given) + "\n" +
		NormalizeACText(criterion.When) + "\n" +
		NormalizeACText(criterion.Then)
}

// AssignACIDs gives every criterion of next a stable server-assigned id, using
// prev -- the previous revision's criteria, in any order -- as the inheritance
// source. next is returned in its own order with the ids filled in; neither
// input is modified.
//
// Rules (design §3.6):
//
//   - Inheritance: identical normalized given/when/then keeps prev's id. The
//     match is by content alone and never by position, so reordering next
//     changes no id -- and because ComputeContentHash sorts by id, reordering
//     changes no ContentHash either.
//   - Append: a criterion with no unmatched prev twin takes the next free
//     sequence, max(sequence over ALL of prev)+1 and then ++.
//   - Hole, never reused: a prev id that next no longer contains simply
//     disappears. Because appending starts above every prev sequence, that id
//     is never handed out again.
//   - EDITED criterion (same position, changed g/w/t): this is a delete plus an
//     append. The old id becomes a permanent hole and the criterion gets a new
//     one. Editing therefore never keeps an id, which is what makes an edit
//     always change the ContentHash and always signal stale design.
//   - A content key repeated inside prev is matched as a FIFO queue of prev ids
//     in id order, so N identical criteria inherit the same N ids revision after
//     revision instead of the second and later copies being re-minted every
//     time. Re-minting would move the ContentHash on a no-op edit and -- worse
//     for design -- leave every covered_acs link to that criterion dangling
//     after the next update. The write path also de-duplicates identical
//     criteria (DedupeAcceptanceCriteria), so this is defence in depth rather
//     than the normal route; it exists so no future caller can reintroduce the
//     churn. Ids in the result are still always unique: a queued id is consumed
//     at most once, and an appended id starts above every prev sequence.
//   - A prev criterion whose id is not canonical is ignored for both maxSeq and
//     inheritance, so its content is treated as new rather than corrupting the
//     sequence.
func AssignACIDs(prev, next []AcceptanceCriterion) []AcceptanceCriterion {
	// content key -> the prev ids carrying it, in id order. Ordering by id rather
	// than by prev's storage order makes the mapping independent of the order a
	// caller happens to hold prev in, so the same content always re-inherits the
	// same ids.
	inherited := make(map[string][]int, len(prev))
	maxSequence := 0
	for _, criterion := range prev {
		sequence, ok := ACIDSequence(criterion.ID)
		if !ok {
			continue
		}
		if sequence > maxSequence {
			maxSequence = sequence
		}
		key := AcceptanceCriterionKey(criterion)
		inherited[key] = append(inherited[key], sequence)
	}
	for _, sequences := range inherited {
		sort.Ints(sequences)
	}

	assigned := make([]AcceptanceCriterion, 0, len(next))
	for _, criterion := range next {
		key := AcceptanceCriterionKey(criterion)
		queue := inherited[key]
		var id string
		if len(queue) > 0 {
			id = FormatACID(queue[0])
			inherited[key] = queue[1:]
		} else {
			maxSequence++
			id = FormatACID(maxSequence)
		}
		item := criterion
		item.ID = id
		assigned = append(assigned, item)
	}
	return assigned
}

// DedupeAcceptanceCriteria drops criteria whose normalized content repeats an
// earlier one in the same list, keeping the first occurrence and the input
// order. It is the write-path rule (normalizeDraft) and matches what Merge has
// always done when unioning two revisions' criteria.
//
// One statement should be one criterion with one id. Without this, two identical
// criteria in a single draft become two rows with two different ids for the same
// sentence, which is noise for the design coverage matrix and -- before
// AssignACIDs grew its per-key FIFO -- re-minted the second id on every revision.
func DedupeAcceptanceCriteria(criteria []AcceptanceCriterion) []AcceptanceCriterion {
	unique := make([]AcceptanceCriterion, 0, len(criteria))
	seen := make(map[string]struct{}, len(criteria))
	for _, criterion := range criteria {
		key := AcceptanceCriterionKey(criterion)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, criterion)
	}
	return unique
}

// SortAcceptanceCriteria returns the criteria ordered by id sequence, which is
// the ContentHash canonical order (design §3.6: "按 id 排序"). The sort is total
// and deterministic even before ids are assigned: criteria without a canonical
// id go last, ordered by content key.
func SortAcceptanceCriteria(criteria []AcceptanceCriterion) []AcceptanceCriterion {
	sorted := append([]AcceptanceCriterion(nil), criteria...)
	sort.SliceStable(sorted, func(i, j int) bool {
		left, leftOK := ACIDSequence(sorted[i].ID)
		right, rightOK := ACIDSequence(sorted[j].ID)
		switch {
		case leftOK != rightOK:
			return leftOK
		case leftOK:
			return left < right
		default:
			return AcceptanceCriterionKey(sorted[i]) < AcceptanceCriterionKey(sorted[j])
		}
	})
	return sorted
}

// ACIDs returns the ids of criteria in canonical (id-sequence) order. It is the
// AC set a design case's covered_acs must be a subset of, and the projection
// exposed on the requirement read faces.
func ACIDs(criteria []AcceptanceCriterion) []string {
	sorted := SortAcceptanceCriteria(criteria)
	ids := make([]string, 0, len(sorted))
	for _, criterion := range sorted {
		ids = append(ids, criterion.ID)
	}
	return ids
}

// ValidateAcceptanceCriteria checks the §5.1 structural contract: at least one
// criterion, at most MaxAcceptanceCriteria of them, every given/when/then
// 1..AcceptanceCriterionPartMax, every id canonical and unique.
//
// This is deliberately the LAX draft gate (I-B1). The completeness and clarity
// rules -- the placeholder-term blacklist and the minimum-description-chars
// floor, with their validation.placeholder_terms /
// validation.min_description_chars settings keys -- belong to the confirmation
// gate owned by subproject E and are not checked here.
func ValidateAcceptanceCriteria(criteria []AcceptanceCriterion) error {
	if len(criteria) == 0 {
		return platform.Invalid("acceptance_criteria", "must contain at least one item")
	}
	if len(criteria) > MaxAcceptanceCriteria {
		return platform.Invalid("acceptance_criteria",
			"must contain at most "+strconv.Itoa(MaxAcceptanceCriteria)+" items")
	}
	seen := make(map[string]struct{}, len(criteria))
	for index, criterion := range criteria {
		field := "acceptance_criteria[" + strconv.Itoa(index) + "]"
		sequence, ok := ACIDSequence(criterion.ID)
		if !ok {
			return platform.Invalid(field+".id", "must be a server-assigned id of the form AC-<n>")
		}
		id := FormatACID(sequence)
		if _, duplicate := seen[id]; duplicate {
			return platform.Invalid(field+".id", "must be unique within one revision")
		}
		seen[id] = struct{}{}
		for _, part := range []struct{ name, value string }{
			{"given", criterion.Given},
			{"when", criterion.When},
			{"then", criterion.Then},
		} {
			if err := validation.String(field+"."+part.name, part.value, 1, AcceptanceCriterionPartMax); err != nil {
				return err
			}
		}
	}
	return nil
}

// NormalizeAcceptanceCriteria trims and whitespace-collapses every part of
// every criterion, dropping criteria left completely empty. It is the write-side
// canonicalization: after it, what is stored is what ComputeContentHash hashes,
// so a read-back revision never differs from its own hash by whitespace.
func NormalizeAcceptanceCriteria(criteria []AcceptanceCriterion) []AcceptanceCriterion {
	normalized := make([]AcceptanceCriterion, 0, len(criteria))
	for _, criterion := range criteria {
		item := criterion
		item.ID = strings.TrimSpace(criterion.ID)
		item.Given = NormalizeACText(criterion.Given)
		item.When = NormalizeACText(criterion.When)
		item.Then = NormalizeACText(criterion.Then)
		if item.Given == "" && item.When == "" && item.Then == "" {
			continue
		}
		normalized = append(normalized, item)
	}
	return normalized
}
