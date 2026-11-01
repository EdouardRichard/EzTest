package searchapp

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// Service aggregates the per-type providers into the global-search snapshot. It
// is stateless and performs no caching: every Search call is independent.
type Service struct {
	providers map[string]Provider
}

// NewService wires the five search-type providers.
func NewService(providers Providers) *Service {
	return &Service{providers: map[string]Provider{
		TypeVersion:     providers.Version,
		TypeRequirement: providers.Requirement,
		TypeCase:        providers.Case,
		TypeIssue:       providers.Issue,
		TypeReport:      providers.Report,
	}}
}

// Search validates the request, queries each requested type exactly once and
// assembles the canonical group order. A provider failure only empties its own
// group and sets degraded=true: the healthy groups are still returned and a
// total provider outage is a 200 snapshot, never a 5xx (spec §4.3.1).
func (s *Service) Search(ctx context.Context, q string, types []string, limit int) (Snapshot, error) {
	if s == nil {
		return Snapshot{}, platform.Unavailable("search service is not configured")
	}
	text, err := NormalizeQuery(q)
	if err != nil {
		return Snapshot{}, err
	}
	requested, err := NormalizeTypes(types)
	if err != nil {
		return Snapshot{}, err
	}
	perTypeLimit, err := NormalizeLimit(limit)
	if err != nil {
		return Snapshot{}, err
	}

	snapshot := Snapshot{Groups: make([]Group, 0, len(requested))}
	for _, typeName := range requested {
		items := make([]Item, 0, perTypeLimit)
		provider := s.providers[typeName]
		if provider == nil {
			snapshot.Degraded = true
		} else if hits, searchErr := provider.Search(ctx, text, perTypeLimit); searchErr != nil {
			// One failing context must not fail the whole search: keep the
			// healthy groups and tell the caller the result is partial.
			snapshot.Degraded = true
		} else {
			if len(hits) > perTypeLimit {
				hits = hits[:perTypeLimit]
			}
			if len(hits) >= perTypeLimit {
				snapshot.Truncated = true
			}
			items = append(items, hits...)
		}
		snapshot.Total += len(items)
		snapshot.Groups = append(snapshot.Groups, Group{Type: typeName, Items: items})
	}
	return snapshot, nil
}

// NormalizeQuery trims the raw keyword and enforces the 1–100 character bound.
func NormalizeQuery(raw string) (string, error) {
	text := strings.TrimSpace(raw)
	length := utf8.RuneCountInString(text)
	if length < MinQueryRunes || length > MaxQueryRunes {
		return "", platform.Invalid("q", "q must be between 1 and 100 characters")
	}
	return text, nil
}

// NormalizeTypes validates and canonicalizes the requested type filter. An empty
// filter selects every type.
func NormalizeTypes(types []string) ([]string, error) {
	if len(types) == 0 {
		return append([]string(nil), AllTypes...), nil
	}
	requested := make(map[string]struct{}, len(types))
	for _, raw := range types {
		typeName := strings.ToLower(strings.TrimSpace(raw))
		if typeName == "" {
			continue
		}
		if !isSupportedType(typeName) {
			return nil, invalidTypesError()
		}
		requested[typeName] = struct{}{}
	}
	if len(requested) == 0 {
		return nil, invalidTypesError()
	}
	ordered := make([]string, 0, len(requested))
	for _, typeName := range AllTypes {
		if _, ok := requested[typeName]; ok {
			ordered = append(ordered, typeName)
		}
	}
	return ordered, nil
}

// NormalizeLimit applies the documented per-type bounds: zero selects the
// default and negative or above-maximum values are rejected.
func NormalizeLimit(limit int) (int, error) {
	if limit == 0 {
		return DefaultLimit, nil
	}
	if limit < MinLimit || limit > MaxLimit {
		return 0, platform.Invalid("limit", "limit must be between 1 and 20")
	}
	return limit, nil
}

func invalidTypesError() *platform.Error {
	return platform.Invalid("types",
		"types must be a subset of version,requirement,case,issue,report")
}

func isSupportedType(typeName string) bool {
	for _, candidate := range AllTypes {
		if candidate == typeName {
			return true
		}
	}
	return false
}
