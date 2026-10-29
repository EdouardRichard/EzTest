// Package application holds the P8 global-search read model: the cross-context
// provider ports it consumes and the read-only service that aggregates them
// (design spec §6.2/D55).
//
// This file is the single home of the search cross-context port declarations.
// It contains value types and interfaces only: no pgx, HTTP or other-context
// imports. Adapters live in each context's infrastructure/search package and
// are wired by the composition root, so the search context never imports
// another bounded context.
package application

import (
	"context"
	"strings"
	"unicode/utf8"
)

// Result types rendered by the global search (spec §6.2).
const (
	TypeVersion     = "version"
	TypeRequirement = "requirement"
	TypeCase        = "case"
	TypeIssue       = "issue"
)

// AllTypes is the canonical group order and the default `types` filter of the
// search endpoint.
var AllTypes = []string{TypeVersion, TypeRequirement, TypeCase, TypeIssue}

// Query bounds of spec §6.2: `limit` is the per-type cap (default 5, max 20)
// and `q` is 1–100 characters.
const (
	DefaultLimit   = 5
	MaxLimit       = 20
	MinLimit       = 1
	MinQueryLength = 1
	MaxQueryLength = 100
)

// ReasonInvalidArgument is the stable details.reason of the 400 envelope the
// HTTP layer renders for invalid query parameters.
const ReasonInvalidArgument = "invalid_argument"

// ErrInvalidArgument reports a rejected query parameter. Field is the public
// parameter name (`q`, `types` or `limit`); the handler renders it as
// invalid_argument{field}.
type ErrInvalidArgument struct {
	Field   string
	Reason  string
	Message string
}

// Error implements the error interface. The message is user-facing and never
// carries provider or database details.
func (e *ErrInvalidArgument) Error() string {
	if e == nil {
		return "invalid argument"
	}
	if e.Message != "" {
		return e.Message
	}
	return "invalid " + e.Field
}

// Item is one search result (spec §6.2). Every id crosses the JSON boundary as a
// decimal string (D29); the URL is the frontend deep link into the owning
// version tab. No credential, evidence body or other sensitive field is part of
// the projection.
type Item struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Subtitle    string `json:"subtitle"`
	VersionID   string `json:"versionId"`
	VersionName string `json:"versionName"`
	Status      string `json:"status"`
	URL         string `json:"url"`
}

// Group is the per-type result group. Items is always a non-nil slice so the
// JSON envelope renders `[]`, never `null`, for an empty group.
type Group struct {
	Type  string `json:"type"`
	Items []Item `json:"items"`
}

// Snapshot is the global-search read model. Degraded is true when at least one
// provider failed: the healthy groups are still returned and the HTTP layer
// answers 200 instead of failing the whole request (spec §6.2).
type Snapshot struct {
	Groups    []Group `json:"groups"`
	Total     int     `json:"total"`
	Truncated bool    `json:"truncated"`
	Degraded  bool    `json:"degraded,omitempty"`
}

// Query is one global-search request. Text is the keyword, Types the requested
// groups (empty means every type) and Limit the per-type cap.
type Query struct {
	Text  string
	Types []string
	Limit int
}

// Provider supplies the result groups a bounded context owns: the version
// context owns version and requirement results, the mindmap context owns cases
// and the delivery context owns issues (spec §3.3). Implementations issue
// read-only queries against their own context only.
type Provider interface {
	Search(ctx context.Context, keyword string, limit int) ([]Group, error)
}

// Providers groups the three cross-context search providers.
type Providers struct {
	Version  Provider
	Mindmap  Provider
	Delivery Provider
}

// ValidateQueryText validates and normalizes the search keyword. It is exported
// so the HTTP layer can reject a bad query before it constructs a request (and
// so both layers share the exact bounds).
func ValidateQueryText(raw string) (string, error) {
	text := strings.TrimSpace(raw)
	length := utf8.RuneCountInString(text)
	if length < MinQueryLength || length > MaxQueryLength {
		return "", &ErrInvalidArgument{
			Field:   "q",
			Reason:  ReasonInvalidArgument,
			Message: "q must be between 1 and 100 characters",
		}
	}
	return text, nil
}
