package tools

// Host clarification retrieval tool (v0.22 B §8.2, spec R-B5-3).
//
// clarification.search is the read-only half of the cross-Host handoff: the
// Design Host can look up structured Q/A, decisions/constraints and message
// summaries from the clarification state instead of receiving the raw chat.
// Every result carries its sources (message_id / question id / timestamp /
// requirement id) and a bounded snippet; whole raw messages are never returned.
//
// version_id is never part of the tool schema: it always comes from the
// runtime-injected tools.Scope, so the model cannot search another version.
// The tool additionally drops any port hit whose VersionID differs from the
// scope as defense in depth, and enforces the snippet/limit bounds again.
//
// Wiring: like question.ask/plan.update this task deliberately does not
// register the tool. The composition root (Task 36) must register
// NewClarificationSearchTool(adapter) for clarification_host/design_host and
// add tools.ToolClarificationSearch to their profile tool lists; the adapter is
// implemented over the version context's question/fact/conversation-message
// read models (the version context must not be imported here).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// ToolClarificationSearch is the read-only clarification retrieval tool.
const ToolClarificationSearch = "clarification.search"

// Result kinds and their contract strings.
const (
	ClarificationSearchKindQA         = "qa"
	ClarificationSearchKindDecision   = "decision"
	ClarificationSearchKindConstraint = "constraint"
	ClarificationSearchKindMessage    = "message"
)

// Search bounds. The default keeps the result payload small; the cap is the
// largest page a model may request.
const (
	DefaultClarificationSearchLimit   = 10
	MaxClarificationSearchLimit       = 50
	MaxClarificationSearchQueryLength = 500
	// MaxClarificationSearchSnippet is the hard per-result text bound. The
	// adapter is expected to return a summary, never a whole message; the tool
	// truncates again so a buggy adapter cannot flood the context.
	MaxClarificationSearchSnippet = 400
)

// ClarificationSearchKinds returns every result kind in deterministic order.
func ClarificationSearchKinds() []string {
	return []string{
		ClarificationSearchKindQA,
		ClarificationSearchKindDecision,
		ClarificationSearchKindConstraint,
		ClarificationSearchKindMessage,
	}
}

// ClarificationSearchQuery is the validated port input. VersionID is always the
// injected scope version. Kinds is explicit (never empty): an omitted model
// filter is expanded to every kind before the port call.
type ClarificationSearchQuery struct {
	VersionID     int64
	Query         string
	RequirementID int64
	Kinds         []string
	Limit         int
}

// ClarificationSearchHit is one port result. VersionID is the owning version and
// is re-checked by the tool; MessageID/QuestionID identify the source row (0 =
// not applicable), Snippet must be a bounded excerpt and Source the typed
// reference ("human_request:501", "version_fact:601", ...).
type ClarificationSearchHit struct {
	VersionID     int64
	Kind          string
	RequirementID int64
	MessageID     int64
	QuestionID    int64
	Snippet       string
	Timestamp     time.Time
	Source        string
}

// ClarificationSearcher is the read-only retrieval port. The production
// implementation is an adapter over the version context's stores; tests use a
// fake. Business failures must be returned as *tools.Error or *platform.Error.
type ClarificationSearcher interface {
	SearchClarification(ctx context.Context, query ClarificationSearchQuery) ([]ClarificationSearchHit, error)
}

// ClarificationSearchHitView is the model-facing projection of one hit. IDs are
// decimal strings per the platform's JSON convention.
type ClarificationSearchHitView struct {
	VersionID     string `json:"version_id"`
	Kind          string `json:"kind"`
	RequirementID string `json:"requirement_id,omitempty"`
	MessageID     string `json:"message_id,omitempty"`
	QuestionID    string `json:"question_id,omitempty"`
	Snippet       string `json:"snippet"`
	Timestamp     string `json:"timestamp,omitempty"`
	Source        string `json:"source"`
}

// ClarificationSearchResult is the model-facing search response.
type ClarificationSearchResult struct {
	VersionID string                       `json:"version_id"`
	Query     string                       `json:"query,omitempty"`
	Count     int                          `json:"count"`
	Results   []ClarificationSearchHitView `json:"results"`
}

// ClarificationSearchTool is the clarification.search tool.
type ClarificationSearchTool struct {
	searcher ClarificationSearcher
}

// NewClarificationSearchTool builds the tool around the retrieval port.
func NewClarificationSearchTool(searcher ClarificationSearcher) *ClarificationSearchTool {
	return &ClarificationSearchTool{searcher: searcher}
}

var _ Tool = (*ClarificationSearchTool)(nil)

// Name implements Tool.
func (t *ClarificationSearchTool) Name() string { return ToolClarificationSearch }

// Description implements Tool.
func (t *ClarificationSearchTool) Description() string {
	return "检索当前版本的澄清结果（clarification.search）：按 query 和/或 requirement_id 搜索结构化问答、决策/约束与消息摘要；结果返回来源（问题/消息 id、时间、需求 id）和受限摘要，不返回整段原始聊天。只读；版本范围由运行时注入，不能由参数指定。"
}

// Parameters implements Tool. version_id is intentionally absent: it comes from
// the injected scope. At least one of query/requirement_id is required by the
// Execute validation (the JSON schema cannot express the oneOf cleanly for all
// providers).
func (t *ClarificationSearchTool) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"query": map[string]any{
			"type":        "string",
			"maxLength":   MaxClarificationSearchQueryLength,
			"description": "检索词（大小写不敏感，可省略但此时必须提供 requirement_id）。",
		},
		"requirement_id": map[string]any{
			"type":        "string",
			"pattern":     `^[1-9][0-9]*$`,
			"description": "只检索该需求关联的问答/决策/消息摘要（十进制字符串）。",
		},
		"kinds": map[string]any{
			"type":        "array",
			"maxItems":    len(ClarificationSearchKinds()),
			"description": "结果类型过滤；省略表示全部：qa、decision、constraint、message。",
			"items": map[string]any{
				"type": "string",
				"enum": ClarificationSearchKinds(),
			},
		},
		"limit": map[string]any{
			"type":        "integer",
			"minimum":     1,
			"maximum":     MaxClarificationSearchLimit,
			"description": "最多返回多少条（默认 10，上限 50）。",
		},
	})
}

// clarificationSearchParams is the strict model parameter shape.
type clarificationSearchParams struct {
	Query         string          `json:"query"`
	RequirementID json.RawMessage `json:"requirement_id"`
	Kinds         []string        `json:"kinds"`
	Limit         *int            `json:"limit"`
}

// Execute implements Tool.
func (t *ClarificationSearchTool) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	if t == nil || t.searcher == nil {
		return nil, missingDep("clarification search")
	}
	if scope.VersionID <= 0 {
		return nil, planInvalid("scope", "clarification.search requires the injected version scope")
	}
	var params clarificationSearchParams
	if err := decodeParams(raw, &params); err != nil {
		err.Retryable = true
		return nil, err
	}
	query, requirementID, kinds, limit, validationErr := validateClarificationSearch(params)
	if validationErr != nil {
		return nil, validationErr
	}

	hits, err := t.searcher.SearchClarification(ctx, ClarificationSearchQuery{
		VersionID:     scope.VersionID,
		Query:         query,
		RequirementID: requirementID,
		Kinds:         kinds,
		Limit:         limit,
	})
	if err != nil {
		return nil, mapClarificationSearchError(err)
	}

	views := make([]ClarificationSearchHitView, 0, min(len(hits), limit))
	for _, hit := range hits {
		if hit.VersionID != scope.VersionID {
			// Defense in depth: a scoped adapter must never return another
			// version's clarification, and a buggy one must not leak through.
			continue
		}
		if !containsString(kinds, hit.Kind) {
			continue
		}
		source := strings.TrimSpace(hit.Source)
		if source == "" {
			// Every result must be attributable; an anonymous hit is dropped
			// rather than shown without its source.
			continue
		}
		snippet := truncateSearchSnippet(hit.Snippet)
		if snippet == "" {
			continue
		}
		views = append(views, ClarificationSearchHitView{
			VersionID:     idString(hit.VersionID),
			Kind:          hit.Kind,
			RequirementID: optionalSearchID(hit.RequirementID),
			MessageID:     optionalSearchID(hit.MessageID),
			QuestionID:    optionalSearchID(hit.QuestionID),
			Snippet:       snippet,
			Timestamp:     formatSearchTime(hit.Timestamp),
			Source:        source,
		})
	}
	sortClarificationSearchHits(views)
	if len(views) > limit {
		views = views[:limit]
	}
	if views == nil {
		views = []ClarificationSearchHitView{}
	}
	return &ClarificationSearchResult{
		VersionID: idString(scope.VersionID),
		Query:     query,
		Count:     len(views),
		Results:   views,
	}, nil
}

// validateClarificationSearch validates/normalizes the model payload. The empty
// input, blank query, invalid requirement id, unknown kind, out-of-range limit
// and empty query without a requirement filter are all typed invalid_argument
// errors (retryable: the model can fix the arguments).
func validateClarificationSearch(params clarificationSearchParams) (string, int64, []string, int, *Error) {
	query := strings.TrimSpace(params.Query)
	if len([]rune(query)) > MaxClarificationSearchQueryLength {
		return "", 0, nil, 0, planInvalid("query", fmt.Sprintf("must be at most %d characters", MaxClarificationSearchQueryLength))
	}

	var requirementID int64
	rawRequirement := strings.TrimSpace(string(params.RequirementID))
	if rawRequirement != "" && rawRequirement != "null" {
		parsed, toolErr := parsePlanPositiveInt("requirement_id", params.RequirementID, "must be a positive decimal ID")
		if toolErr != nil {
			return "", 0, nil, 0, toolErr
		}
		requirementID = parsed
	}
	if query == "" && requirementID == 0 {
		return "", 0, nil, 0, planInvalid("query", "must not be empty when requirement_id is not set")
	}

	kinds := make([]string, 0, len(ClarificationSearchKinds()))
	seen := make(map[string]struct{}, len(params.Kinds))
	for _, rawKind := range params.Kinds {
		kind := strings.ToLower(strings.TrimSpace(rawKind))
		if kind == "" {
			continue
		}
		if !containsString(ClarificationSearchKinds(), kind) {
			return "", 0, nil, 0, planInvalid("kinds", "must contain only qa, decision, constraint, message")
		}
		if _, duplicate := seen[kind]; duplicate {
			continue
		}
		seen[kind] = struct{}{}
		kinds = append(kinds, kind)
	}
	if len(kinds) == 0 {
		kinds = ClarificationSearchKinds()
	}

	limit := DefaultClarificationSearchLimit
	if params.Limit != nil {
		limit = *params.Limit
		if limit < 1 || limit > MaxClarificationSearchLimit {
			return "", 0, nil, 0, planInvalid("limit", fmt.Sprintf("must be between 1 and %d", MaxClarificationSearchLimit))
		}
	}
	return query, requirementID, kinds, limit, nil
}

// mapClarificationSearchError translates port failures: typed/business errors
// are model-visible, unexpected failures stay internal and fail the run.
func mapClarificationSearchError(err error) error {
	if err == nil {
		return nil
	}
	var toolErr *Error
	if errors.As(err, &toolErr) {
		return toolErr
	}
	var apiErr *platform.Error
	if errors.As(err, &apiErr) {
		return mapPortError(apiErr)
	}
	return fmt.Errorf("clarification.search: %w", err)
}

func truncateSearchSnippet(value string) string {
	trimmed := strings.TrimSpace(value)
	runes := []rune(trimmed)
	if len(runes) <= MaxClarificationSearchSnippet {
		return trimmed
	}
	return string(runes[:MaxClarificationSearchSnippet])
}

func optionalSearchID(value int64) string {
	if value <= 0 {
		return ""
	}
	return idString(value)
}

func formatSearchTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

// sortClarificationSearchHits orders newest first, then by kind and source id so
// the same inputs always produce the same response context.
func sortClarificationSearchHits(hits []ClarificationSearchHitView) {
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Timestamp != hits[j].Timestamp {
			return hits[i].Timestamp > hits[j].Timestamp
		}
		if hits[i].Kind != hits[j].Kind {
			return hits[i].Kind < hits[j].Kind
		}
		if hits[i].QuestionID != hits[j].QuestionID {
			return hits[i].QuestionID < hits[j].QuestionID
		}
		return hits[i].MessageID < hits[j].MessageID
	})
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
