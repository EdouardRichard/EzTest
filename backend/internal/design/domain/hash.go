package design

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// contentHashPayload is the canonical, order-independent projection of a case
// version's editable content. Identity, state, revision and timestamps are
// deliberately excluded: the hash answers "is this the same content?".
//
// v0.2.4-B (§3.6): default_version_sut_id / version_sut_ids / page_ids /
// api_ids are gone and sut_description / covered_acs take their place. Field
// ORDER is part of the contract -- encoding/json emits struct fields in
// declaration order, so reordering these changes every hash. The authoritative
// order is:
//
//	title, description, sut_description, preconditions, covered_acs,
//	test_data, priority, steps, assertions, sources
type contentHashPayload struct {
	Title          string                 `json:"title"`
	Description    string                 `json:"description"`
	SutDescription string                 `json:"sut_description"`
	Preconditions  string                 `json:"preconditions"`
	CoveredACs     []string               `json:"covered_acs"`
	TestData       json.RawMessage        `json:"test_data"`
	Priority       string                 `json:"priority"`
	Steps          []contentHashStep      `json:"steps"`
	Assertions     []contentHashAssertion `json:"assertions"`
	Sources        []contentHashSource    `json:"sources"`
}

// contentHashStep order: seq, action, expected, target_description,
// account_role, data.
type contentHashStep struct {
	Seq               int             `json:"seq"`
	Action            string          `json:"action"`
	Expected          string          `json:"expected"`
	TargetDescription string          `json:"target_description"`
	AccountRole       string          `json:"account_role"`
	Data              json.RawMessage `json:"data"`
}

// contentHashAssertion order: step_seq, kind, target, expected, sut_id.
type contentHashAssertion struct {
	StepSeq  int    `json:"step_seq"`
	Kind     string `json:"kind"`
	Target   string `json:"target"`
	Expected string `json:"expected"`
	SutID    *int64 `json:"sut_id"`
}

type contentHashSource struct {
	Kind  string `json:"kind"`
	RefID string `json:"ref_id"`
	Path  string `json:"path"`
	Line  *int   `json:"line"`
	SHA   string `json:"sha"`
}

// ComputeCaseContentHash returns the stable sha256 hex of one case version's
// content. Set-valued fields are sorted and JSON object keys canonicalized, so
// equivalent content always hashes identically (spec §4.1 content_hash).
//
// GD-05: the v0.2.4-B payload shape and ID domain both changed, so every hash
// computed before design_0004 differs from the one computed after. That is
// expected and there is no backfill -- the legacy rows it would apply to were
// already discarded by version_0016.
func ComputeCaseContentHash(version CaseVersion) (string, error) {
	testData, err := NormalizeJSONObject(version.TestData)
	if err != nil {
		return "", err
	}
	priority, err := NormalizePriority(version.Priority)
	if err != nil {
		return "", err
	}
	coveredACs := NormalizeACIDSet(version.CoveredACs)

	steps := make([]contentHashStep, 0, len(version.Steps))
	for _, step := range version.Steps {
		data, err := NormalizeJSONObject(step.Data)
		if err != nil {
			return "", err
		}
		steps = append(steps, contentHashStep{
			Seq:               step.Seq,
			Action:            step.Action,
			Expected:          step.Expected,
			TargetDescription: step.TargetDescription,
			AccountRole:       step.AccountRole,
			Data:              data,
		})
	}
	sort.SliceStable(steps, func(i, j int) bool { return steps[i].Seq < steps[j].Seq })

	assertions := make([]contentHashAssertion, 0, len(version.Assertions))
	for _, assertion := range version.Assertions {
		assertions = append(assertions, contentHashAssertion{
			StepSeq:  assertion.StepSeq,
			Kind:     assertion.Kind,
			Target:   assertion.Target,
			Expected: assertion.Expected,
			SutID:    assertion.SUTID,
		})
	}
	sort.SliceStable(assertions, func(i, j int) bool {
		left, right := assertions[i], assertions[j]
		if left.StepSeq != right.StepSeq {
			return left.StepSeq < right.StepSeq
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		if left.Target != right.Target {
			return left.Target < right.Target
		}
		if left.Expected != right.Expected {
			return left.Expected < right.Expected
		}
		return optionalInt64String(left.SutID) < optionalInt64String(right.SutID)
	})

	sources := make([]contentHashSource, 0, len(version.Sources))
	for _, source := range version.Sources {
		sources = append(sources, contentHashSource{
			Kind:  source.Kind,
			RefID: source.RefID,
			Path:  source.Path,
			Line:  source.Line,
			SHA:   source.SHA,
		})
	}
	sort.SliceStable(sources, func(i, j int) bool {
		left, right := sources[i], sources[j]
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		if left.RefID != right.RefID {
			return left.RefID < right.RefID
		}
		if left.Path != right.Path {
			return left.Path < right.Path
		}
		return optionalIntString(left.Line) < optionalIntString(right.Line)
	})

	payload := contentHashPayload{
		Title:          version.Title,
		Description:    version.Description,
		SutDescription: version.SutDescription,
		Preconditions:  version.Preconditions,
		CoveredACs:     coveredACs,
		TestData:       testData,
		Priority:       priority,
		Steps:          steps,
		Assertions:     assertions,
		Sources:        sources,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func optionalInt64String(value *int64) string {
	if value == nil {
		return ""
	}
	return formatInt64(*value)
}

func optionalIntString(value *int) string {
	if value == nil {
		return ""
	}
	return formatInt64(int64(*value))
}
