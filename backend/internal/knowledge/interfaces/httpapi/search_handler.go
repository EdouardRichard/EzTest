package httpapi

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	knowledgeapp "github.com/EdouardRichard/EzTest/backend/internal/knowledge/application"
	knowledge "github.com/EdouardRichard/EzTest/backend/internal/knowledge/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/pagination"
)

// Search GET /api/knowledge/search (login). It is the user-facing structured
// search: the list engine provides the id-desc/ts_rank order, the cursor page
// and the shared page envelope, while this handler applies the 0.2.1 policy:
//
//   - approved-only by default, so unpublished knowledge never appears unless
//     the caller explicitly filters a valid status (review/curation views);
//   - structured SUT binding filters (system/environment/page/API/account
//     role), which the repository applies as strict JSONB containment;
//   - the free-text query under the retrieval text bound;
//   - cursor/limit pagination identical to GET /api/knowledge/items.
//
// The response is the regular KnowledgeItemView page, so it exposes bindings
// as decimal strings and last_verified_at but no credential field.
func (h *Handler) Search(w http.ResponseWriter, r *http.Request) {
	if _, err := requireActor(r); err != nil {
		httpx.Error(w, r, err)
		return
	}
	query := r.URL.Query()
	text := strings.TrimSpace(query.Get("q"))
	if text == "" {
		// `query` is the 0.2.1 spelling; `q` stays the frozen P7 alias.
		text = strings.TrimSpace(query.Get("query"))
	}
	if utf8.RuneCountInString(text) > knowledgeapp.MaxRetrievalTextRunes {
		httpx.Error(w, r, platform.Invalid("query", "must be at most 512 characters"))
		return
	}
	status := knowledge.Status(strings.TrimSpace(query.Get("status")))
	if status == "" {
		status = knowledge.StatusApproved
	} else if !status.Valid() {
		httpx.Error(w, r, platform.Invalid("status",
			"status must be draft, approved, suspect, stale, rejected or revoked"))
		return
	}
	bindings, err := parseSearchBindings(query)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	cursor, err := parseCursor(query.Get("cursor"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	limit, err := parseLimit(query.Get("limit"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.service == nil {
		httpx.Error(w, r, platform.Unavailable("knowledge service is not configured"))
		return
	}
	page, err := h.service.List(r.Context(), knowledgeapp.ListFilter{
		Status:   status,
		Query:    text,
		Bindings: bindings,
		Cursor:   cursor,
		Limit:    limit,
	})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	views := make([]knowledgeItemView, 0, len(page.Items))
	for _, item := range page.Items {
		views = append(views, toItemView(item))
	}
	nextCursor := ""
	if page.HasMore {
		nextCursor = pagination.NextCursor(true, page.NextCursor)
	}
	httpx.List(w, http.StatusOK, views, nextCursor, page.HasMore)
}

// parseSearchBindings reads the five optional structured binding filters. Empty
// values are unset dimensions; IDs must be positive decimal strings and keys
// stay within the domain bounds. Normalize returns the canonical bindings so
// the repository containment filter receives exactly what was queried.
func parseSearchBindings(query url.Values) (knowledge.Bindings, error) {
	systemID, err := parseOptionalID("system_id", optionalQueryPointer(query.Get("system_id")))
	if err != nil {
		return knowledge.Bindings{}, err
	}
	environmentID, err := parseOptionalID("environment_id", optionalQueryPointer(query.Get("environment_id")))
	if err != nil {
		return knowledge.Bindings{}, err
	}
	pageKey, err := parseBindingText("page_key", query.Get("page_key"), knowledge.MaxBindingKeyRunes)
	if err != nil {
		return knowledge.Bindings{}, err
	}
	apiKey, err := parseBindingText("api_key", query.Get("api_key"), knowledge.MaxBindingKeyRunes)
	if err != nil {
		return knowledge.Bindings{}, err
	}
	accountRole, err := parseBindingText("account_role", query.Get("account_role"),
		knowledge.MaxAccountRoleRunes)
	if err != nil {
		return knowledge.Bindings{}, err
	}
	bindings, err := knowledge.Bindings{
		SystemID:      systemID,
		EnvironmentID: environmentID,
		PageKey:       pageKey,
		APIKey:        apiKey,
		AccountRole:   accountRole,
	}.Normalize()
	if err != nil {
		return knowledge.Bindings{}, err
	}
	return bindings, nil
}

// optionalQueryPointer returns nil for an absent/blank query value so
// parseOptionalID treats "unset" differently from an explicitly invalid ID.
func optionalQueryPointer(raw string) *string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// parseBindingText trims an optional binding key and enforces its rune bound.
func parseBindingText(field, raw string, maxRunes int) (*string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(trimmed) > maxRunes {
		return nil, platform.Invalid(field, "must be at most "+strconv.Itoa(maxRunes)+" characters")
	}
	return &trimmed, nil
}
