// Host structured-question tool (v0.22 B §6.3, spec R-B3-2/R-B3-5).
//
// question.ask is the model-facing half of the structured-question write path.
// The Host supplies only the 1-5 question payload; version_id, conversation_id
// and the owning agent_run_id come from the runtime-injected Scope so the model
// can neither ask in another conversation nor forge another run's ownership.
// The execution application's QuestionService creates the pending
// human_request plus the conversation_message(kind=question) card and returns
// the pending request id.
//
// Wiring: like plan.update, this task deliberately does not register the tool.
// The composition root / Host profile work (Task 36) must add
// NewQuestionAskTool(adapter) to the Host registry and allow tools.ToolQuestionAsk
// in the clarification_host/design_host tool lists.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// ToolQuestionAsk is the Host structured-question tool (R-B3-5).
const ToolQuestionAsk = "question.ask"

// Structured-question bounds, mirrored from the execution domain so the tool
// validates model input before any state is written.
const (
	minAskQuestions         = 1
	maxAskQuestions         = 5
	maxAskOptions           = 10
	maxAskSourceRefs        = 10
	maxAskIDLength          = 64
	maxAskTextLength        = 2000
	maxAskOptionLabelLength = 200
	maxAskSourceRefLength   = 500
	maxAskBatchIDLength     = 64
)

// QuestionOption is one answer option supplied by the model.
type QuestionOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// QuestionItem is one structured question supplied by the model.
type QuestionItem struct {
	ID            string           `json:"id"`
	Text          string           `json:"text"`
	Options       []QuestionOption `json:"options"`
	Multiple      bool             `json:"multiple"`
	AllowFreeText bool             `json:"allow_free_text"`
	Required      bool             `json:"required"`
	SourceRefs    []string         `json:"source_refs"`
}

// QuestionAskInput is the validated question payload handed to the port.
type QuestionAskInput struct {
	Questions []QuestionItem
	BatchID   string
	ExpiresAt *time.Time
}

// QuestionAskResult is the created pending human_request projection.
type QuestionAskResult struct {
	ID        int64
	MessageID int64
	BatchID   string
	Status    string
	ExpiresAt time.Time
}

// QuestionAsker creates the pending structured question. The production
// implementation is executionapp.QuestionService.AskFromTool; tests use a fake.
// Business failures must be returned as *tools.Error or *platform.Error.
type QuestionAsker interface {
	AskQuestions(ctx context.Context, scope Scope, input QuestionAskInput) (QuestionAskResult, error)
}

// QuestionAskTool is the Host question.ask tool.
type QuestionAskTool struct {
	asker QuestionAsker
}

// NewQuestionAskTool builds the question.ask tool around the question service.
func NewQuestionAskTool(asker QuestionAsker) *QuestionAskTool {
	return &QuestionAskTool{asker: asker}
}

var _ Tool = (*QuestionAskTool)(nil)

// Name implements Tool.
func (t *QuestionAskTool) Name() string { return ToolQuestionAsk }

// Description implements Tool.
func (t *QuestionAskTool) Description() string {
	return "向用户发起结构化提问（question.ask）：一次提交 1-5 个问题，每题可带选项/多选/必填/允许自由文本/来源引用；创建 pending 问题并渲染提问卡，返回问题 ID。用户回答后会唤醒当前对话；跳过后问题置 cancelled、不写回答消息也不唤醒；提问不会阻塞阶段推进。"
}

// Parameters implements Tool. version_id/conversation_id/agent_run_id are
// intentionally absent: they come from the injected scope.
func (t *QuestionAskTool) Parameters() json.RawMessage {
	questionSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id": map[string]any{
				"type":        "string",
				"description": "问题 ID（批内唯一，如 q1）。",
			},
			"text": map[string]any{
				"type":        "string",
				"description": "题干；必填且不超过 2000 字。",
			},
			"options": map[string]any{
				"type":        "array",
				"maxItems":    maxAskOptions,
				"description": "选项列表；没有选项时必须允许自由文本。",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":    map[string]any{"type": "string", "description": "选项 ID（题内唯一）。"},
						"label": map[string]any{"type": "string", "description": "选项文案。"},
					},
					"required":             []string{"id", "label"},
					"additionalProperties": false,
				},
			},
			"multiple": map[string]any{
				"type":        "boolean",
				"description": "是否允许多选；默认 false。",
			},
			"allow_free_text": map[string]any{
				"type":        "boolean",
				"description": "是否允许自由文本回答；默认 false。",
			},
			"required": map[string]any{
				"type":        "boolean",
				"description": "是否必答；必答问题必须有选项或允许自由文本。",
			},
			"source_refs": map[string]any{
				"type":        "array",
				"maxItems":    maxAskSourceRefs,
				"description": "来源引用（附件/需求等），用于提问卡展示。",
				"items":       map[string]any{"type": "string"},
			},
		},
		"required":             []string{"id", "text"},
		"additionalProperties": false,
	}
	return schemaObject(map[string]any{
		"questions": map[string]any{
			"type":        "array",
			"minItems":    minAskQuestions,
			"maxItems":    maxAskQuestions,
			"description": "1-5 个问题；关键歧义建议单题，其余批量 3-5 题。",
			"items":       questionSchema,
		},
		"source_refs": map[string]any{
			"type":        "array",
			"maxItems":    maxAskSourceRefs,
			"description": "批次级共享来源引用（附件/需求等），会合并进每个问题的 source_refs；题目可用自身 source_refs 追加。",
			"items":       map[string]any{"type": "string"},
		},
		"batch_id": map[string]any{
			"type":        "string",
			"description": "批次 ID（可选；缺省由服务端生成）。",
		},
		"expires_at": map[string]any{
			"type":        "string",
			"format":      "date-time",
			"description": "过期时间（RFC3339，可选；默认 24h）。超时后问题置 expired，不阻塞阶段。",
		},
	}, "questions")
}

// questionAskParams is the strict model parameter shape.
type questionAskParams struct {
	Questions  []QuestionItem `json:"questions"`
	SourceRefs []string       `json:"source_refs"`
	BatchID    string         `json:"batch_id"`
	ExpiresAt  *string        `json:"expires_at"`
}

// Execute implements Tool.
func (t *QuestionAskTool) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	if t == nil || t.asker == nil {
		return nil, missingDep("question service")
	}
	if scope.VersionID <= 0 || scope.ConversationID <= 0 || scope.AgentRunID <= 0 {
		return nil, planInvalid("scope", "question.ask requires the injected version, conversation and agent run")
	}
	var params questionAskParams
	if err := decodeParams(raw, &params); err != nil {
		err.Retryable = true
		return nil, err
	}
	input, err := validateQuestionAskParams(params)
	if err != nil {
		return nil, err
	}
	result, portErr := t.asker.AskQuestions(ctx, scope, input)
	if portErr != nil {
		return nil, mapQuestionAskError(portErr)
	}
	payload := map[string]any{
		"question_id": idString(result.ID),
		"batch_id":    result.BatchID,
		"status":      result.Status,
		"expires_at":  result.ExpiresAt.UTC().Format(time.RFC3339),
	}
	if result.MessageID > 0 {
		payload["message_id"] = idString(result.MessageID)
	}
	return payload, nil
}

// validateQuestionAskParams validates the model payload: 1-5 questions, unique
// ids, non-empty text/options, and the required/free-text consistency.
func validateQuestionAskParams(params questionAskParams) (QuestionAskInput, *Error) {
	if len(params.Questions) < minAskQuestions || len(params.Questions) > maxAskQuestions {
		return QuestionAskInput{}, planInvalid("questions", fmt.Sprintf(
			"must contain between %d and %d questions", minAskQuestions, maxAskQuestions))
	}
	batchID := strings.TrimSpace(params.BatchID)
	if len(batchID) > maxAskBatchIDLength {
		return QuestionAskInput{}, planInvalid("batch_id", "must be at most 64 characters")
	}
	batchRefs, batchErr := validateBatchSourceRefs(params.SourceRefs)
	if batchErr != nil {
		return QuestionAskInput{}, batchErr
	}
	seenQuestionIDs := map[string]struct{}{}
	questions := make([]QuestionItem, 0, len(params.Questions))
	for index, question := range params.Questions {
		field := fmt.Sprintf("questions[%d]", index)
		id := strings.TrimSpace(question.ID)
		if id == "" || len(id) > maxAskIDLength {
			return QuestionAskInput{}, planInvalid(field+".id", "must be non-empty and at most 64 characters")
		}
		if _, duplicate := seenQuestionIDs[id]; duplicate {
			return QuestionAskInput{}, planInvalid(field+".id", "must be unique within the batch")
		}
		seenQuestionIDs[id] = struct{}{}
		text := strings.TrimSpace(question.Text)
		if text == "" || len(text) > maxAskTextLength {
			return QuestionAskInput{}, planInvalid(field+".text", "must be non-empty and at most 2000 characters")
		}
		if len(question.Options) > maxAskOptions {
			return QuestionAskInput{}, planInvalid(field+".options", "must contain at most 10 options")
		}
		if len(question.Options) == 0 && !question.AllowFreeText {
			return QuestionAskInput{}, planInvalid(field, "must offer options or allow_free_text so the user can answer")
		}
		options := make([]QuestionOption, 0, len(question.Options))
		seenOptionIDs := map[string]struct{}{}
		for optionIndex, option := range question.Options {
			optionField := fmt.Sprintf("%s.options[%d]", field, optionIndex)
			optionID := strings.TrimSpace(option.ID)
			if optionID == "" || len(optionID) > maxAskIDLength {
				return QuestionAskInput{}, planInvalid(optionField+".id", "must be non-empty and at most 64 characters")
			}
			if _, duplicate := seenOptionIDs[optionID]; duplicate {
				return QuestionAskInput{}, planInvalid(optionField+".id", "must be unique within the question")
			}
			seenOptionIDs[optionID] = struct{}{}
			label := strings.TrimSpace(option.Label)
			if label == "" || len(label) > maxAskOptionLabelLength {
				return QuestionAskInput{}, planInvalid(optionField+".label", "must be non-empty and at most 200 characters")
			}
			options = append(options, QuestionOption{ID: optionID, Label: label})
		}
		if len(question.SourceRefs) > maxAskSourceRefs {
			return QuestionAskInput{}, planInvalid(field+".source_refs", "must contain at most 10 refs")
		}
		sourceRefs := make([]string, 0, len(question.SourceRefs))
		for refIndex, ref := range question.SourceRefs {
			trimmed := strings.TrimSpace(ref)
			if trimmed == "" || len(trimmed) > maxAskSourceRefLength {
				return QuestionAskInput{}, planInvalid(fmt.Sprintf("%s.source_refs[%d]", field, refIndex),
					"must be non-empty and at most 500 characters")
			}
			sourceRefs = append(sourceRefs, trimmed)
		}
		sourceRefs = mergeSourceRefs(sourceRefs, batchRefs)
		if len(sourceRefs) > maxAskSourceRefs {
			return QuestionAskInput{}, planInvalid(field+".source_refs",
				"must contain at most 10 refs after merging batch source_refs")
		}
		questions = append(questions, QuestionItem{
			ID:            id,
			Text:          text,
			Options:       options,
			Multiple:      question.Multiple,
			AllowFreeText: question.AllowFreeText,
			Required:      question.Required,
			SourceRefs:    sourceRefs,
		})
	}
	input := QuestionAskInput{Questions: questions, BatchID: batchID}
	if params.ExpiresAt != nil && strings.TrimSpace(*params.ExpiresAt) != "" {
		expiresAt, parseErr := time.Parse(time.RFC3339, strings.TrimSpace(*params.ExpiresAt))
		if parseErr != nil {
			return QuestionAskInput{}, planInvalid("expires_at", "must be an RFC3339 timestamp")
		}
		input.ExpiresAt = &expiresAt
	}
	return input, nil
}

// validateBatchSourceRefs trims and validates the batch-level refs shared by
// every question in the ask: at most 10 refs, each non-empty within 500 chars.
func validateBatchSourceRefs(refs []string) ([]string, *Error) {
	if len(refs) > maxAskSourceRefs {
		return nil, planInvalid("source_refs", "must contain at most 10 refs")
	}
	trimmed := make([]string, 0, len(refs))
	for index, ref := range refs {
		value := strings.TrimSpace(ref)
		if value == "" || len(value) > maxAskSourceRefLength {
			return nil, planInvalid(fmt.Sprintf("source_refs[%d]", index),
				"must be non-empty and at most 500 characters")
		}
		trimmed = append(trimmed, value)
	}
	return trimmed, nil
}

// mergeSourceRefs appends the batch-level refs to the question's own refs,
// dropping duplicates while preserving first-seen order.
func mergeSourceRefs(own, batch []string) []string {
	merged := make([]string, 0, len(own)+len(batch))
	seen := make(map[string]struct{}, len(own)+len(batch))
	for _, refs := range [][]string{own, batch} {
		for _, ref := range refs {
			if _, duplicate := seen[ref]; duplicate {
				continue
			}
			seen[ref] = struct{}{}
			merged = append(merged, ref)
		}
	}
	return merged
}

// mapQuestionAskError turns question-service failures into model-visible tool
// errors. A 403 (not the owning Host run) is a state conflict for the model;
// unexpected failures stay internal.
func mapQuestionAskError(err error) error {
	var apiErr *platform.Error
	if errors.As(err, &apiErr) && apiErr.Status == 403 {
		return &Error{Code: CodeStateConflict, Message: apiErr.Message, Details: apiErr.Details}
	}
	return mapPortError(err)
}
