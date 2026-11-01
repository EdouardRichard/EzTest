// Package searchapp implements the P6 read-only global-search use case. It
// aggregates five per-type providers (version, requirement, case, issue,
// report), validates the documented query bounds and degrades a failing
// provider to an empty group instead of failing the whole request (spec
// §4.3.1/§6).
//
// The application layer owns the cross-context port only: it declares the
// Provider interface and the wire value types. Adapters live in
// infrastructure/providers and call each owning context's QueryService; this
// package never imports another context and never touches SQL.
package searchapp

import "context"

// Search result types and the canonical group order of the response
// (openapi.yaml `SearchGroup.type`).
const (
	TypeVersion     = "version"
	TypeRequirement = "requirement"
	TypeCase        = "case"
	TypeIssue       = "issue"
	TypeReport      = "report"
)

// AllTypes is the canonical group order and the default `types` filter.
var AllTypes = []string{TypeVersion, TypeRequirement, TypeCase, TypeIssue, TypeReport}

// Query bounds of spec §4.3.1: q is 1–100 characters after trimming and limit
// is the per-type cap (default 5, maximum 20, so at most 100 items overall).
const (
	DefaultLimit  = 5
	MinLimit      = 1
	MaxLimit      = 20
	MinQueryRunes = 1
	MaxQueryRunes = 100
)

// Item is one cross-entity search result (openapi.yaml `SearchItem`). IDs are
// decimal strings at the API boundary; URL is the server-built deep link of the
// 0.2 workspace. No credential, evidence body, prompt or transcript is part of
// the projection.
type Item struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Subtitle     string `json:"subtitle"`
	VersionID    string `json:"version_id"`
	VersionTitle string `json:"version_title"`
	Status       string `json:"status"`
	URL          string `json:"url"`
}

// Group is the per-type result group. Items is always a non-nil slice so an
// empty group renders `[]`, never `null`.
type Group struct {
	Type  string `json:"type"`
	Items []Item `json:"items"`
}

// Snapshot is the global-search response (openapi.yaml `SearchResultView`).
// Degraded is true when at least one requested provider failed; the healthy
// groups are still returned and the endpoint answers 200 instead of 5xx.
type Snapshot struct {
	Groups    []Group `json:"groups"`
	Total     int     `json:"total"`
	Truncated bool    `json:"truncated"`
	Degraded  bool    `json:"degraded"`
}

// Provider is one search-type adapter. A provider is bound to exactly one type
// by Service's provider map, so it returns items of that type only and issues
// exactly one read-only call against its owning context. Implementations must
// honor the passed per-type limit.
type Provider interface {
	Search(ctx context.Context, q string, limit int) ([]Item, error)
}

// VersionTitleReader is the narrow version application port used to decorate
// items whose provider cannot read another context's version table. It resolves
// every requested version ID in one batch (spec §9: version_title comes from the
// version context, never from another context's SQL). The version application
// QueryService satisfies it without an adapter.
type VersionTitleReader interface {
	VersionTitles(ctx context.Context, ids []int64) (map[int64]string, error)
}

// Providers groups the five per-type adapters. A nil adapter degrades its group
// (the search still succeeds with degraded=true).
type Providers struct {
	Version     Provider
	Requirement Provider
	Case        Provider
	Issue       Provider
	Report      Provider
}
