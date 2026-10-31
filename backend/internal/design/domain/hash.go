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
type contentHashPayload struct {
	Title               string                 `json:"title"`
	Description         string                 `json:"description"`
	Preconditions       string                 `json:"preconditions"`
	TestData            json.RawMessage        `json:"test_data"`
	Priority            string                 `json:"priority"`
	DefaultVersionSUTID *int64                 `json:"default_version_sut_id"`
	VersionSUTIDs       []int64                `json:"version_sut_ids"`
	PageIDs             []int64                `json:"page_ids"`
	APIIDs              []int64                `json:"api_ids"`
	Steps               []contentHashStep      `json:"steps"`
	Assertions          []contentHashAssertion `json:"assertions"`
	Sources             []contentHashSource    `json:"sources"`
}

type contentHashStep struct {
	Seq                int             `json:"seq"`
	Action             string          `json:"action"`
	Expected           string          `json:"expected"`
	TargetVersionSUTID *int64          `json:"target_version_sut_id"`
	TargetPageID       *int64          `json:"target_page_id"`
	TargetAPIID        *int64          `json:"target_api_id"`
	AccountRole        string          `json:"account_role"`
	Data               json.RawMessage `json:"data"`
}

type contentHashAssertion struct {
	StepSeq            int    `json:"step_seq"`
	Kind               string `json:"kind"`
	Target             string `json:"target"`
	Expected           string `json:"expected"`
	TargetVersionSUTID *int64 `json:"target_version_sut_id"`
	TargetAPIID        *int64 `json:"target_api_id"`
	DatabaseID         *int64 `json:"database_id"`
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
func ComputeCaseContentHash(version CaseVersion) (string, error) {
	testData, err := NormalizeJSONObject(version.TestData)
	if err != nil {
		return "", err
	}
	priority, err := NormalizePriority(version.Priority)
	if err != nil {
		return "", err
	}
	versionSUTIDs, err := NormalizeIDSet(version.VersionSUTIDs)
	if err != nil {
		return "", err
	}
	pageIDs, err := NormalizeIDSet(version.PageIDs)
	if err != nil {
		return "", err
	}
	apiIDs, err := NormalizeIDSet(version.APIIDs)
	if err != nil {
		return "", err
	}

	steps := make([]contentHashStep, 0, len(version.Steps))
	for _, step := range version.Steps {
		data, err := NormalizeJSONObject(step.Data)
		if err != nil {
			return "", err
		}
		steps = append(steps, contentHashStep{
			Seq:                step.Seq,
			Action:             step.Action,
			Expected:           step.Expected,
			TargetVersionSUTID: step.TargetVersionSUTID,
			TargetPageID:       step.TargetPageID,
			TargetAPIID:        step.TargetAPIID,
			AccountRole:        step.AccountRole,
			Data:               data,
		})
	}
	sort.SliceStable(steps, func(i, j int) bool { return steps[i].Seq < steps[j].Seq })

	assertions := make([]contentHashAssertion, 0, len(version.Assertions))
	for _, assertion := range version.Assertions {
		assertions = append(assertions, contentHashAssertion{
			StepSeq:            assertion.StepSeq,
			Kind:               assertion.Kind,
			Target:             assertion.Target,
			Expected:           assertion.Expected,
			TargetVersionSUTID: assertion.TargetVersionSUTID,
			TargetAPIID:        assertion.TargetAPIID,
			DatabaseID:         assertion.DatabaseID,
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
		return optionalInt64String(left.TargetVersionSUTID) < optionalInt64String(right.TargetVersionSUTID)
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
		Title:               version.Title,
		Description:         version.Description,
		Preconditions:       version.Preconditions,
		TestData:            testData,
		Priority:            priority,
		DefaultVersionSUTID: version.DefaultVersionSUTID,
		VersionSUTIDs:       versionSUTIDs,
		PageIDs:             pageIDs,
		APIIDs:              apiIDs,
		Steps:               steps,
		Assertions:          assertions,
		Sources:             sources,
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
