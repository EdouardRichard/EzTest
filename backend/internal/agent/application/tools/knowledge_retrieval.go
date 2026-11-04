package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Structured retrieval tool names (spec §10.2). knowledge.search returns the
// bounded ranked summaries injected/curated knowledge; knowledge.read fetches
// one full item by the reference ID. Both are read-only and are exposed only to
// roles whose closed whitelist contains them (knowledge_curator today; the
// runtime RoleToolGuard enforces the whitelist before Execute).
const (
	ToolKnowledgeSearch = "knowledge.search"
	ToolKnowledgeRead   = "knowledge.read"

	// defaultKnowledgeSearchLimit / maxKnowledgeSearchLimit bound one search
	// result page so a single tool call cannot dominate the model context.
	defaultKnowledgeSearchLimit = 10
	maxKnowledgeSearchLimit     = 50
	// maxKnowledgeSearchTextRunes mirrors the knowledge application's query
	// bound (MaxRetrievalTextRunes).
	maxKnowledgeSearchTextRunes = 512
	// knowledgeSnippetMaxRunes mirrors knowledge.SnippetMaxRunes.
	knowledgeSnippetMaxRunes = 500
	// maxKnowledgeBindingKeyRunes / maxKnowledgeAccountRoleRunes mirror the
	// knowledge_item binding limits so the model gets a typed error before a
	// useless repository call.
	maxKnowledgeBindingKeyRunes  = 500
	maxKnowledgeAccountRoleRunes = 64
)

// KnowledgeSearchFilters is one validated knowledge.search request. IDs are
// parsed decimal IDs and the free text is trimmed; VersionID is the
// runtime-injected version scope, never a model argument.
type KnowledgeSearchFilters struct {
	VersionID     int64
	SystemID      *int64
	EnvironmentID *int64
	PageKey       *string
	APIKey        *string
	AccountRole   *string
	Text          string
	Page          int
	Limit         int
}

// KnowledgeHit is one bounded search hit: IDs, title, kind, relevance score and
// the excerpt only. The full body is deliberately absent.
type KnowledgeHit struct {
	ID      int64
	Title   string
	Kind    string
	Score   float64
	Snippet string
}

// KnowledgeSearchResult is one ranked page of hits.
type KnowledgeSearchResult struct {
	Hits    []KnowledgeHit
	Page    int
	Limit   int
	HasMore bool
}

// KnowledgeBindings is the structured SUT binding view of one knowledge item.
// Unset dimensions are not bound; the fields are identifiers, never secrets.
type KnowledgeBindings struct {
	SystemID      *int64
	EnvironmentID *int64
	PageKey       *string
	APIKey        *string
	AccountRole   *string
}

// KnowledgeDetail is the full knowledge.read result. Content is the item body:
// read is the only knowledge tool that returns it (Task 33 keeps stale/suspect
// items readable even though they no longer inject).
type KnowledgeDetail struct {
	ID             int64
	Title          string
	Kind           string
	Content        string
	Scope          string
	Status         string
	Tags           []string
	Revision       int
	LastVerifiedAt *time.Time
	Bindings       KnowledgeBindings
}

// KnowledgeRetrievalPort is the P8 structured retrieval surface. The adapter
// bridge (agent/infrastructure/knowledgeadapter) owns the version→SUT-family
// resolution and the knowledge application call; the tools package stays
// independent of the knowledge application types. Implementations return
// *platform.Error for business failures (mapped to typed tool errors by
// mapPortError) and plain errors for internal failures.
type KnowledgeRetrievalPort interface {
	SearchKnowledge(ctx context.Context, filters KnowledgeSearchFilters) (KnowledgeSearchResult, error)
	ReadKnowledge(ctx context.Context, scope Scope, id int64) (KnowledgeDetail, error)
}

/* ------------------------------ knowledge.search -------------------------- */

type knowledgeSearch struct{ deps Deps }

func (t *knowledgeSearch) Name() string { return ToolKnowledgeSearch }

func (t *knowledgeSearch) Description() string {
	return "按系统/环境/页面/API/账号角色结构化检索已审核知识，返回有界摘要（ID/标题/类型/相关度/片段）；不含全文、凭据或内部路径。需全文时用 knowledge.read。"
}

func (t *knowledgeSearch) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"system_id": map[string]any{
			"type":        "string",
			"description": "SUT system ID（十进制字符串）。",
		},
		"environment_id": map[string]any{
			"type":        "string",
			"description": "环境 ID（十进制字符串）。",
		},
		"page_key": map[string]any{
			"type":        "string",
			"maxLength":   maxKnowledgeBindingKeyRunes,
			"description": "页面标识（route/template）。",
		},
		"api_key": map[string]any{
			"type":        "string",
			"maxLength":   maxKnowledgeBindingKeyRunes,
			"description": "API 标识（method + path template）。",
		},
		"account_role": map[string]any{
			"type":        "string",
			"maxLength":   maxKnowledgeAccountRoleRunes,
			"description": "账号角色（不含凭据）。",
		},
		"text": map[string]any{
			"type":        "string",
			"maxLength":   maxKnowledgeSearchTextRunes,
			"description": "自由文本查询（中文按字符 trigram 模糊匹配）。",
		},
		"page": map[string]any{
			"type":    "integer",
			"minimum": 1,
			"default": 1,
		},
		"limit": map[string]any{
			"type":    "integer",
			"minimum": 1,
			"maximum": maxKnowledgeSearchLimit,
			"default": defaultKnowledgeSearchLimit,
		},
	})
}

// knowledgeSearchPayload is the model-visible argument shape. The version scope
// is injected out of band and is not part of the schema; decodeParams rejects a
// model-supplied version_id or any other unknown property.
type knowledgeSearchPayload struct {
	SystemID      *string `json:"system_id"`
	EnvironmentID *string `json:"environment_id"`
	PageKey       *string `json:"page_key"`
	APIKey        *string `json:"api_key"`
	AccountRole   *string `json:"account_role"`
	Text          string  `json:"text"`
	Page          *int    `json:"page"`
	Limit         *int    `json:"limit"`
}

func (t *knowledgeSearch) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	if t.deps.P7.Retrieval == nil {
		return nil, missingDep("knowledge retrieval service")
	}
	filters, toolErr := buildKnowledgeSearchFilters(scope, raw)
	if toolErr != nil {
		return nil, toolErr
	}
	result, err := t.deps.P7.Retrieval.SearchKnowledge(ctx, filters)
	if err != nil {
		return nil, mapPortError(err)
	}
	return renderKnowledgeSearchResult(result), nil
}

func buildKnowledgeSearchFilters(scope Scope, raw json.RawMessage) (KnowledgeSearchFilters, *Error) {
	var params knowledgeSearchPayload
	if err := decodeParams(raw, &params); err != nil {
		return KnowledgeSearchFilters{}, err
	}
	if scope.VersionID <= 0 {
		return KnowledgeSearchFilters{}, Invalid("scope", "the version scope is required")
	}
	filters := KnowledgeSearchFilters{
		VersionID: scope.VersionID,
		Text:      strings.TrimSpace(params.Text),
		Page:      1,
		Limit:     defaultKnowledgeSearchLimit,
	}
	if utf8.RuneCountInString(filters.Text) > maxKnowledgeSearchTextRunes {
		return KnowledgeSearchFilters{}, Invalid("text", fmt.Sprintf("must be at most %d characters", maxKnowledgeSearchTextRunes))
	}
	systemID, toolErr := parseOptionalPositiveID("system_id", params.SystemID)
	if toolErr != nil {
		return KnowledgeSearchFilters{}, toolErr
	}
	environmentID, toolErr := parseOptionalPositiveID("environment_id", params.EnvironmentID)
	if toolErr != nil {
		return KnowledgeSearchFilters{}, toolErr
	}
	pageKey, toolErr := normalizeOptionalBindingText("page_key", params.PageKey, maxKnowledgeBindingKeyRunes)
	if toolErr != nil {
		return KnowledgeSearchFilters{}, toolErr
	}
	apiKey, toolErr := normalizeOptionalBindingText("api_key", params.APIKey, maxKnowledgeBindingKeyRunes)
	if toolErr != nil {
		return KnowledgeSearchFilters{}, toolErr
	}
	accountRole, toolErr := normalizeOptionalBindingText("account_role", params.AccountRole, maxKnowledgeAccountRoleRunes)
	if toolErr != nil {
		return KnowledgeSearchFilters{}, toolErr
	}
	filters.SystemID = systemID
	filters.EnvironmentID = environmentID
	filters.PageKey = pageKey
	filters.APIKey = apiKey
	filters.AccountRole = accountRole

	if params.Page != nil {
		if *params.Page < 0 {
			return KnowledgeSearchFilters{}, Invalid("page", "must not be negative")
		}
		if *params.Page > 0 {
			filters.Page = *params.Page
		}
	}
	if params.Limit != nil {
		if *params.Limit < 0 {
			return KnowledgeSearchFilters{}, Invalid("limit", "must not be negative")
		}
		if *params.Limit > 0 {
			filters.Limit = *params.Limit
		}
		if filters.Limit > maxKnowledgeSearchLimit {
			filters.Limit = maxKnowledgeSearchLimit
		}
	}
	return filters, nil
}

func renderKnowledgeSearchResult(result KnowledgeSearchResult) map[string]any {
	items := make([]map[string]any, 0, len(result.Hits))
	for _, hit := range result.Hits {
		items = append(items, map[string]any{
			"id":      idString(hit.ID),
			"title":   hit.Title,
			"kind":    hit.Kind,
			"score":   hit.Score,
			"snippet": boundedKnowledgeSnippet(hit.Snippet),
		})
	}
	page := result.Page
	if page <= 0 {
		page = 1
	}
	limit := result.Limit
	if limit <= 0 {
		limit = defaultKnowledgeSearchLimit
	}
	if limit > maxKnowledgeSearchLimit {
		limit = maxKnowledgeSearchLimit
	}
	return map[string]any{
		"items":    items,
		"page":     page,
		"limit":    limit,
		"has_more": result.HasMore,
	}
}

/* ------------------------------- knowledge.read --------------------------- */

type knowledgeRead struct{ deps Deps }

func (t *knowledgeRead) Name() string { return ToolKnowledgeRead }

func (t *knowledgeRead) Description() string {
	return "按 ID 读取一条知识条目全文（ID 来自 knowledge.search 或注入引用）；已失效（stale）/可疑（suspect）条目仍可读取，不使用凭据或内部路径。"
}

func (t *knowledgeRead) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"id": map[string]any{
			"type":        "string",
			"description": "知识条目 ID（十进制字符串）。",
		},
	}, "id")
}

type knowledgeReadPayload struct {
	ID string `json:"id"`
}

func (t *knowledgeRead) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	if t.deps.P7.Retrieval == nil {
		return nil, missingDep("knowledge retrieval service")
	}
	var params knowledgeReadPayload
	if err := decodeParams(raw, &params); err != nil {
		return nil, err
	}
	if scope.VersionID <= 0 {
		return nil, Invalid("scope", "the version scope is required")
	}
	id, toolErr := parsePositiveID("id", params.ID)
	if toolErr != nil {
		return nil, toolErr
	}
	detail, err := t.deps.P7.Retrieval.ReadKnowledge(ctx, scope, id)
	if err != nil {
		return nil, mapPortError(err)
	}
	return renderKnowledgeDetail(detail), nil
}

func renderKnowledgeDetail(detail KnowledgeDetail) map[string]any {
	tags := detail.Tags
	if tags == nil {
		tags = []string{}
	}
	bindings := map[string]any{}
	if detail.Bindings.SystemID != nil {
		bindings["system_id"] = idString(*detail.Bindings.SystemID)
	}
	if detail.Bindings.EnvironmentID != nil {
		bindings["environment_id"] = idString(*detail.Bindings.EnvironmentID)
	}
	if detail.Bindings.PageKey != nil {
		bindings["page_key"] = *detail.Bindings.PageKey
	}
	if detail.Bindings.APIKey != nil {
		bindings["api_key"] = *detail.Bindings.APIKey
	}
	if detail.Bindings.AccountRole != nil {
		bindings["account_role"] = *detail.Bindings.AccountRole
	}
	result := map[string]any{
		"id":       idString(detail.ID),
		"title":    detail.Title,
		"kind":     detail.Kind,
		"content":  detail.Content,
		"scope":    detail.Scope,
		"status":   detail.Status,
		"tags":     tags,
		"revision": detail.Revision,
		"bindings": bindings,
	}
	if detail.LastVerifiedAt != nil {
		result["last_verified_at"] = detail.LastVerifiedAt.UTC().Format(time.RFC3339)
	}
	return result
}

/* -------------------------------- helpers -------------------------------- */

func parseOptionalPositiveID(field string, raw *string) (*int64, *Error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil, nil
	}
	value, err := parsePositiveID(field, *raw)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func normalizeOptionalBindingText(field string, raw *string, maxRunes int) (*string, *Error) {
	if raw == nil {
		return nil, nil
	}
	value := strings.TrimSpace(*raw)
	if value == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(value) > maxRunes {
		return nil, Invalid(field, fmt.Sprintf("must be at most %d characters", maxRunes))
	}
	return &value, nil
}

// boundedKnowledgeSnippet keeps the tool contract's 500-rune snippet bound even
// if a future adapter returns a longer excerpt.
func boundedKnowledgeSnippet(snippet string) string {
	runes := []rune(snippet)
	if len(runes) <= knowledgeSnippetMaxRunes {
		return snippet
	}
	return string(runes[:knowledgeSnippetMaxRunes]) + "…"
}
