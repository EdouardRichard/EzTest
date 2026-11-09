package tools

// v0.22 requirement.extraction_emit (spec R-A7-4/R-A7-5, docs/version-V0.22/A
// §7.5). The deterministic extraction worker sends one segment at a time to the
// model; the model answers through this tool instead of relying on provider
// response_format support.
//
// The core schema is fixed (external_id/title/description/acceptance_criteria/
// priority/category/source_quote/confidence + optional constraints_json) so
// batches can be merged, deduped and reconciled. source_quote must be a verbatim
// substring of the exact segment text the worker injected into the scope; a
// batch with one invalid requirement is rejected as a whole before the sink is
// called, so there is never a partial write.
//
// The sink is the persistence port owned by Task 17/18 (draft write / requirement
// upsert). This file defines the contract; the tool is registered only when a
// sink is wired.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

// ToolRequirementExtractionEmit is the structured extraction output tool.
const ToolRequirementExtractionEmit = "requirement.extraction_emit"

// ErrSourceQuoteNotFound is the sentinel wrapped by a source_quote validation
// failure. Callers classify the failure with errors.Is while still receiving the
// typed invalid_argument tool error (with the requirements[i].source_quote field).
var ErrSourceQuoteNotFound = errors.New("source_quote must be a verbatim substring of the segment text")

// Fixed core-schema enums (spec R-A7-4).
const (
	ExtractionPriorityP0 = "P0"
	ExtractionPriorityP1 = "P1"
	ExtractionPriorityP2 = "P2"
	ExtractionPriorityP3 = "P3"

	ExtractionCategoryFunctional    = "functional"
	ExtractionCategoryNonFunctional = "non_functional"
	ExtractionCategoryPlanned       = "planned"
)

var (
	extractionPriorities = []string{
		ExtractionPriorityP0, ExtractionPriorityP1, ExtractionPriorityP2, ExtractionPriorityP3,
	}
	extractionCategories = []string{
		ExtractionCategoryFunctional, ExtractionCategoryNonFunctional, ExtractionCategoryPlanned,
	}
)

// Field limits keep one malformed model answer from consuming the whole segment
// budget. They are deliberately generous; the parser's 2 MiB extracted-text and
// 8,000-rune segment limits bound the real input.
const (
	maxExtractionExternalIDChars    = 128
	maxExtractionTitleChars         = 500
	maxExtractionDescriptionChars   = 20000
	maxExtractionSourceQuoteChars   = 20000
	maxExtractionCriteria           = 50
	maxExtractionCriterionChars     = 2000
	maxExtractionConstraintsJSONLen = 64 * 1024
)

// ExtractionSource identifies where one emitted batch came from so the sink can
// link requirement.extraction_run_id / source position (R-A7-6).
type ExtractionSource struct {
	VersionID    int64
	AttachmentID int64
	RunID        int64
	SegmentSeq   int
}

// ExtractionMergeMode is the in-process no-downgrade merge policy of one
// emitted requirement. It is never persisted and never model supplied: the
// deterministic processor marks FR body declarations replace and catalog table
// rows fill_empty so a later appendix row cannot overwrite the body.
type ExtractionMergeMode string

const (
	// ExtractionMergeReplace overwrites non-empty fields (but never clears a
	// non-empty title/description/AC with an empty value).
	ExtractionMergeReplace ExtractionMergeMode = "replace"
	// ExtractionMergeFillEmpty only fills fields that are still empty in the
	// current row.
	ExtractionMergeFillEmpty ExtractionMergeMode = "fill_empty"
)

// ExtractionRequirement is one validated, normalized requirement of the fixed
// core schema. SourceQuote is kept byte-for-byte (it passed the verbatim
// substring check); ConstraintsJSON is the optional open JSON object.
// Confidence is 0 when the model omitted the optional field. MergeMode is the
// in-process merge policy (zero value = replace) and is not persisted.
type ExtractionRequirement struct {
	ExternalID         string
	Title              string
	Description        string
	AcceptanceCriteria []string
	Priority           string
	Category           string
	SourceQuote        string
	Confidence         float64
	ConstraintsJSON    json.RawMessage
	MergeMode          ExtractionMergeMode
}

// ExtractionEmitCounts is the sink's created/updated result for one batch.
// Skipped counts non-draft requirements the sink declined to overwrite.
type ExtractionEmitCounts struct {
	Created int
	Updated int
	Skipped int
}

// ExtractionSink is the requirement draft write / upsert port (Task 17/18 owns
// the database implementation; this task ships the contract and test fake). The
// sink must dedupe by (version_id, external_id) and update instead of inserting
// duplicates (R-A7-7).
type ExtractionSink interface {
	EmitExtractedRequirements(ctx context.Context, source ExtractionSource, requirements []ExtractionRequirement) (ExtractionEmitCounts, error)
}

// ExtractionScope is the out-of-band extraction context of one tool call. The
// extraction worker injects it; it is never part of a tool schema. SegmentText
// is the exact chunk the model was asked to extract, and is the only accepted
// source of source_quote substrings.
type ExtractionScope struct {
	RunID        int64
	AttachmentID int64
	SegmentSeq   int
	SegmentText  string
}

// ExtractionEmitTool is the requirement.extraction_emit tool.
type ExtractionEmitTool struct {
	sink ExtractionSink
}

// NewExtractionEmitTool builds the tool around a requirement sink.
func NewExtractionEmitTool(sink ExtractionSink) *ExtractionEmitTool {
	return &ExtractionEmitTool{sink: sink}
}

var _ Tool = (*ExtractionEmitTool)(nil)

// Name implements Tool.
func (t *ExtractionEmitTool) Name() string { return ToolRequirementExtractionEmit }

// Description implements Tool.
func (t *ExtractionEmitTool) Description() string {
	return "提交当前需求文档片段（segment）中抽取出的结构化需求。固定字段：external_id、title、description、acceptance_criteria、priority(P0-P3)、category(functional/non_functional/planned)、source_quote、confidence(0-1)，额外约束放 constraints_json。source_quote 必须是当前片段原文的逐字连续子串（不得改写/翻译/拼接）；同批 external_id 重复会合并，不产生重复需求。"
}

// Parameters implements Tool. version_id/run/segment are injected by the worker,
// never model supplied.
func (t *ExtractionEmitTool) Parameters() json.RawMessage {
	requirement := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"external_id": map[string]any{
				"type":        "string",
				"minLength":   1,
				"maxLength":   maxExtractionExternalIDChars,
				"description": "文档中的需求编号（如 FR-001）。同批重复的编号会合并为一条需求。",
			},
			"title": map[string]any{
				"type": "string", "minLength": 1, "maxLength": maxExtractionTitleChars,
				"description": "需求标题（简洁、可对账）。",
			},
			"description": map[string]any{
				"type": "string", "maxLength": maxExtractionDescriptionChars,
				"description": "需求正文；原文没有可写空字符串。",
			},
			"acceptance_criteria": map[string]any{
				"type": "array", "maxItems": maxExtractionCriteria,
				"items":       map[string]any{"type": "string", "maxLength": maxExtractionCriterionChars},
				"description": "验收标准列表；原文没有可写空数组。",
			},
			"priority": map[string]any{
				"type": "string", "enum": extractionPriorities,
				"description": "优先级：P0/P1/P2/P3。",
			},
			"category": map[string]any{
				"type": "string", "enum": extractionCategories,
				"description": "分类：functional/non_functional/planned。",
			},
			"source_quote": map[string]any{
				"type": "string", "minLength": 1, "maxLength": maxExtractionSourceQuoteChars,
				"description": "当前片段原文中逐字出现的连续片段，用于来源校验。",
			},
			"confidence": map[string]any{
				"type": "number", "minimum": 0, "maximum": 1,
				"description": "抽取置信度 0-1（可选；缺省表示模型未给出，不会覆盖同批前一条的值）。",
			},
			"constraints_json": map[string]any{
				"type":        "object",
				"description": "额外约束的开放 JSON 对象（可选）。",
			},
		},
		"required": []string{
			"external_id", "title", "description", "acceptance_criteria",
			"priority", "category", "source_quote",
		},
		"additionalProperties": false,
	}
	return schemaObject(map[string]any{
		"requirements": map[string]any{
			"type": "array", "items": requirement,
			"description": "本片段抽取出的需求列表；本片段没有需求时传空数组。",
		},
	}, "requirements")
}

// extractionEmitParams is the strict top-level argument shape. A pointer field
// distinguishes an absent requirements key from an explicit empty array.
type extractionEmitParams struct {
	Requirements *[]extractionEmitRequirementParams `json:"requirements"`
}

// extractionEmitRequirementParams is the strict per-requirement argument shape.
// Unknown fields are rejected by decodeParams' DisallowUnknownFields decoder.
// Description/Confidence are pointers to distinguish absent from empty/0:
// description is schema-required (nil is an explicit required-field error) and
// confidence is optional (nil means "not supplied", so a duplicate merge keeps
// the earlier value instead of overwriting it with 0).
type extractionEmitRequirementParams struct {
	ExternalID         string          `json:"external_id"`
	Title              string          `json:"title"`
	Description        *string         `json:"description"`
	AcceptanceCriteria *[]string       `json:"acceptance_criteria"`
	Priority           string          `json:"priority"`
	Category           string          `json:"category"`
	SourceQuote        string          `json:"source_quote"`
	Confidence         *float64        `json:"confidence"`
	ConstraintsJSON    json.RawMessage `json:"constraints_json"`
}

// Execute implements Tool using the segment text injected into the scope.
func (t *ExtractionEmitTool) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	return t.Emit(ctx, scope, scope.Extraction.SegmentText, raw)
}

// Emit validates one model batch against segmentText and writes the deduped
// requirements through the sink. The worker may call it directly with the text
// it sent to the model; Execute uses scope.Extraction.SegmentText.
func (t *ExtractionEmitTool) Emit(ctx context.Context, scope Scope, segmentText string, raw json.RawMessage) (any, error) {
	if t == nil || t.sink == nil {
		return nil, missingDep("requirement extraction sink")
	}
	if scope.VersionID <= 0 {
		// A missing version scope is a worker/wiring bug, not a model error:
		// keep it internal (and non-retryable) instead of invalid_argument.
		return nil, fmt.Errorf("requirement.extraction_emit: %w", missingDep("version scope"))
	}
	var params extractionEmitParams
	if err := decodeParams(raw, &params); err != nil {
		err.Retryable = true
		return nil, err
	}
	if params.Requirements == nil {
		return nil, extractionFieldError("requirements", "requirements is required; use [] when this segment yields none", nil)
	}

	normalized := make([]ExtractionRequirement, 0, len(*params.Requirements))
	indexByExternalID := make(map[string]int, len(*params.Requirements))
	for index, item := range *params.Requirements {
		requirement, toolErr := validateExtractionRequirement(index, item, segmentText)
		if toolErr != nil {
			return nil, toolErr
		}
		if at, exists := indexByExternalID[requirement.ExternalID]; exists {
			merged := mergeExtractionRequirement(normalized[at], requirement)
			// Only a supplied confidence replaces the earlier value; an omitted
			// confidence must not zero the duplicate's earlier 0.9, etc.
			if item.Confidence != nil {
				merged.Confidence = requirement.Confidence
			} else {
				merged.Confidence = normalized[at].Confidence
			}
			normalized[at] = merged
			continue
		}
		indexByExternalID[requirement.ExternalID] = len(normalized)
		normalized = append(normalized, requirement)
	}
	if len(normalized) == 0 {
		return extractionEmitResult(0, ExtractionEmitCounts{}), nil
	}

	source := ExtractionSource{
		VersionID:    scope.VersionID,
		AttachmentID: scope.Extraction.AttachmentID,
		RunID:        scope.Extraction.RunID,
		SegmentSeq:   scope.Extraction.SegmentSeq,
	}
	counts, err := t.sink.EmitExtractedRequirements(ctx, source, normalized)
	if err != nil {
		return nil, mapPortError(err)
	}
	if counts.Created < 0 || counts.Updated < 0 {
		return nil, fmt.Errorf("requirement.extraction_emit: sink returned negative counts %+v", counts)
	}
	return extractionEmitResult(len(normalized), counts), nil
}

// extractionEmitResult is the model-visible batch result: one entry per deduped
// requirement plus the persistence counts.
func extractionEmitResult(total int, counts ExtractionEmitCounts) map[string]any {
	return map[string]any{
		"requirements": total,
		"created":      counts.Created,
		"updated":      counts.Updated,
		"skipped":      counts.Skipped,
	}
}

// validateExtractionRequirement validates and normalizes one fixed-core-schema
// requirement. The first failure is returned with its requirements[i].field path.
func validateExtractionRequirement(index int, params extractionEmitRequirementParams, segmentText string) (ExtractionRequirement, *Error) {
	field := func(name string) string { return fmt.Sprintf("requirements[%d].%s", index, name) }

	externalID := strings.TrimSpace(params.ExternalID)
	if externalID == "" {
		return ExtractionRequirement{}, extractionFieldError(field("external_id"), "must not be empty", nil)
	}
	if utf8.RuneCountInString(externalID) > maxExtractionExternalIDChars {
		return ExtractionRequirement{}, extractionFieldError(field("external_id"),
			fmt.Sprintf("must be at most %d characters", maxExtractionExternalIDChars), nil)
	}
	title := strings.TrimSpace(params.Title)
	if title == "" {
		return ExtractionRequirement{}, extractionFieldError(field("title"), "must not be empty", nil)
	}
	if utf8.RuneCountInString(title) > maxExtractionTitleChars {
		return ExtractionRequirement{}, extractionFieldError(field("title"),
			fmt.Sprintf("must be at most %d characters", maxExtractionTitleChars), nil)
	}
	if strings.ContainsRune(title, '|') {
		// A Markdown table row is never a valid title (C-03 guardrail): the
		// column-aware parser must write the cell, not the raw line.
		return ExtractionRequirement{}, extractionFieldError(field("title"),
			"must not contain a Markdown table pipe (|); extract the cell value", nil)
	}
	if params.Description == nil {
		return ExtractionRequirement{}, extractionFieldError(field("description"),
			"is required; use an empty string when the source has no description", nil)
	}
	description := strings.TrimSpace(*params.Description)
	if utf8.RuneCountInString(description) > maxExtractionDescriptionChars {
		return ExtractionRequirement{}, extractionFieldError(field("description"),
			fmt.Sprintf("must be at most %d characters", maxExtractionDescriptionChars), nil)
	}
	if strings.ContainsRune(description, '|') {
		return ExtractionRequirement{}, extractionFieldError(field("description"),
			"must not contain a Markdown table pipe (|); extract the cell value", nil)
	}
	if params.AcceptanceCriteria == nil {
		return ExtractionRequirement{}, extractionFieldError(field("acceptance_criteria"),
			"is required; use [] when the requirement has none", nil)
	}
	criteria := make([]string, 0, len(*params.AcceptanceCriteria))
	for _, criterion := range *params.AcceptanceCriteria {
		trimmed := strings.TrimSpace(criterion)
		if trimmed == "" {
			continue
		}
		if utf8.RuneCountInString(trimmed) > maxExtractionCriterionChars {
			return ExtractionRequirement{}, extractionFieldError(field("acceptance_criteria"),
				fmt.Sprintf("items must be at most %d characters", maxExtractionCriterionChars), nil)
		}
		criteria = append(criteria, trimmed)
	}
	if len(criteria) > maxExtractionCriteria {
		return ExtractionRequirement{}, extractionFieldError(field("acceptance_criteria"),
			fmt.Sprintf("must contain at most %d items", maxExtractionCriteria), nil)
	}
	priority := strings.ToUpper(strings.TrimSpace(params.Priority))
	if !containsExactString(extractionPriorities, priority) {
		return ExtractionRequirement{}, extractionFieldError(field("priority"),
			"must be one of P0/P1/P2/P3", map[string]any{
				"allowed_values": append([]string(nil), extractionPriorities...),
				"hint":           "使用文档声明的优先级；无法判断时按 P2 处理并在 description 中说明。",
			})
	}
	category := strings.ToLower(strings.TrimSpace(params.Category))
	if !containsExactString(extractionCategories, category) {
		return ExtractionRequirement{}, extractionFieldError(field("category"),
			"must be one of functional/non_functional/planned", map[string]any{
				"allowed_values": append([]string(nil), extractionCategories...),
				"hint":           "功能需求 functional，非功能需求 non_functional，规划/未实现需求 planned。",
			})
	}
	quote := params.SourceQuote
	if strings.TrimSpace(quote) == "" {
		return ExtractionRequirement{}, extractionFieldError(field("source_quote"),
			"must not be empty; copy a contiguous run of the segment text", nil)
	}
	if utf8.RuneCountInString(quote) > maxExtractionSourceQuoteChars {
		return ExtractionRequirement{}, extractionFieldError(field("source_quote"),
			fmt.Sprintf("must be at most %d characters", maxExtractionSourceQuoteChars), nil)
	}
	if !strings.Contains(segmentText, quote) {
		toolErr := extractionFieldError(field("source_quote"),
			"source_quote must be a verbatim substring of the current segment text", map[string]any{
				"hint": "从当前片段原文中复制一个连续片段；不要改写、翻译、拼接或引用其他片段。",
			})
		toolErr.Cause = ErrSourceQuoteNotFound
		return ExtractionRequirement{}, toolErr
	}
	confidence := 0.0
	if params.Confidence != nil {
		confidence = *params.Confidence
	}
	if math.IsNaN(confidence) || math.IsInf(confidence, 0) || confidence < 0 || confidence > 1 {
		return ExtractionRequirement{}, extractionFieldError(field("confidence"),
			"must be a number between 0 and 1", nil)
	}
	constraints := json.RawMessage(nil)
	if trimmed := bytes.TrimSpace(params.ConstraintsJSON); len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
		if len(trimmed) > maxExtractionConstraintsJSONLen {
			return ExtractionRequirement{}, extractionFieldError(field("constraints_json"),
				fmt.Sprintf("must be at most %d bytes", maxExtractionConstraintsJSONLen), nil)
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &object); err != nil || object == nil {
			return ExtractionRequirement{}, extractionFieldError(field("constraints_json"),
				"must be a JSON object", nil)
		}
		constraints = append(json.RawMessage(nil), trimmed...)
	}
	return ExtractionRequirement{
		ExternalID:         externalID,
		Title:              title,
		Description:        description,
		AcceptanceCriteria: criteria,
		Priority:           priority,
		Category:           category,
		SourceQuote:        quote,
		Confidence:         confidence,
		ConstraintsJSON:    constraints,
	}, nil
}

// MergeExtractionRequirement applies the incoming row to the current one under
// the incoming merge policy (C-03 no-downgrade merge). It is shared by the
// in-batch duplicate merge and the PostgreSQL upsert so both paths enforce the
// same rules:
//   - fill_empty: only empty title/description/AC/source_quote are filled;
//   - replace: non-empty values replace, but empty values never clear a
//     non-empty title/description/AC/source_quote. Acceptance criteria are
//     taken verbatim from the incoming row (never unioned), so a re-upload can
//     drop a removed scenario; the current criteria are kept only when the
//     incoming list is empty;
//   - a valid priority may replace in either mode;
//   - constraints are merged as JSON objects (incoming wins on a key conflict);
//   - confidence: fill_empty takes the max, replace takes a supplied incoming
//     score and otherwise keeps the current one (0/omitted never overwrites).
func MergeExtractionRequirement(current, incoming ExtractionRequirement) ExtractionRequirement {
	merged := current
	fillEmpty := incoming.MergeMode == ExtractionMergeFillEmpty
	if fillEmpty {
		if merged.Title == "" {
			merged.Title = incoming.Title
		}
		if merged.Description == "" {
			merged.Description = incoming.Description
		}
		if len(merged.AcceptanceCriteria) == 0 {
			merged.AcceptanceCriteria = append([]string(nil), incoming.AcceptanceCriteria...)
		}
		if strings.TrimSpace(merged.SourceQuote) == "" {
			merged.SourceQuote = incoming.SourceQuote
		}
		if merged.Confidence < incoming.Confidence {
			merged.Confidence = incoming.Confidence
		}
	} else {
		if incoming.Title != "" {
			merged.Title = incoming.Title
		}
		if incoming.Description != "" {
			merged.Description = incoming.Description
		}
		if len(incoming.AcceptanceCriteria) > 0 {
			// Replace, never append: the persisted acceptance criteria are
			// newline-joined and re-split on the next upsert, so a union would
			// duplicate every multi-line criterion on each re-upload and could
			// never drop a removed scenario.
			merged.AcceptanceCriteria = append([]string(nil), incoming.AcceptanceCriteria...)
		}
		if strings.TrimSpace(incoming.SourceQuote) != "" {
			merged.SourceQuote = incoming.SourceQuote
		}
		if incoming.Confidence > 0 {
			merged.Confidence = incoming.Confidence
		} else if merged.Confidence < 0 {
			// -1 is the PostgreSQL NULL sentinel; an omitted model confidence
			// must not be persisted as a negative number.
			merged.Confidence = 0
		}
	}
	if IsExtractionPriority(incoming.Priority) {
		merged.Priority = incoming.Priority
	}
	if incoming.Category != "" {
		merged.Category = incoming.Category
	}
	merged.ConstraintsJSON = mergeExtractionConstraints(current.ConstraintsJSON, incoming.ConstraintsJSON)
	merged.ExternalID = current.ExternalID
	if merged.ExternalID == "" {
		merged.ExternalID = incoming.ExternalID
	}
	return merged
}

// IsExtractionPriority reports whether the value is a valid P0-P3 priority.
func IsExtractionPriority(value string) bool {
	return containsExactString(extractionPriorities, strings.ToUpper(strings.TrimSpace(value)))
}

// mergeExtractionConstraints merges two JSON objects. The incoming object wins
// on a key conflict; an empty side is treated as an empty object.
func mergeExtractionConstraints(current, incoming json.RawMessage) json.RawMessage {
	currentObject := decodeExtractionConstraints(current)
	incomingObject := decodeExtractionConstraints(incoming)
	if len(currentObject) == 0 && len(incomingObject) == 0 {
		return current
	}
	for key, value := range incomingObject {
		currentObject[key] = value
	}
	merged, err := json.Marshal(currentObject)
	if err != nil {
		if len(incoming) > 0 {
			return incoming
		}
		return current
	}
	return merged
}

// decodeExtractionConstraints decodes a constraints JSON object; malformed or
// empty input becomes an empty object so a merge never fails.
func decodeExtractionConstraints(raw json.RawMessage) map[string]json.RawMessage {
	object := map[string]json.RawMessage{}
	if len(raw) == 0 {
		return object
	}
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return map[string]json.RawMessage{}
	}
	return object
}

// mergeExtractionRequirement merges a duplicate external_id into the first
// occurrence using the same no-downgrade policy as the sink. Confidence is
// applied by Emit only when the duplicate supplied it, so an absent confidence
// never resets the earlier value to zero.
func mergeExtractionRequirement(base, update ExtractionRequirement) ExtractionRequirement {
	return MergeExtractionRequirement(base, update)
}

// extractionFieldError builds the retryable invalid_argument with the offending
// field path. Extra details (allowed values, hints) are merged into Details.
func extractionFieldError(field, message string, extra map[string]any) *Error {
	details := map[string]any{"field": field}
	for key, value := range extra {
		details[key] = value
	}
	return &Error{Code: CodeInvalidArgument, Message: message, Retryable: true, Details: details}
}

// containsExactString reports whether values already contain value exactly.
func containsExactString(values []string, value string) bool {
	for _, existing := range values {
		if existing == value {
			return true
		}
	}
	return false
}
