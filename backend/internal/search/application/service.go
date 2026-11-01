package searchapp

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// Service aggregates the per-type providers into the global-search snapshot. It
// is stateless and performs no caching: every Search call is independent.
type Service struct {
	providers map[string]Provider
	titles    VersionTitleReader
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

// WithVersionTitles attaches the version title resolver used to fill the items
// whose provider cannot see another context's version table. A nil resolver
// leaves the titles empty instead of degrading the search; the production
// assembly root must wire the version QueryService.
func (s *Service) WithVersionTitles(titles VersionTitleReader) *Service {
	if s != nil {
		s.titles = titles
	}
	return s
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
	s.decorateVersionTitles(ctx, &snapshot)
	return snapshot, nil
}

// decorateVersionTitles fills the missing version titles in one batch read. The
// lookup is best-effort: a failing title reader marks the snapshot degraded and
// keeps the titles that the providers already resolved, because the groups
// themselves are healthy and spec §4.3.1 requires a 200 partial result rather
// than a 5xx. A nil reader is an unwired decoration and stays silent.
func (s *Service) decorateVersionTitles(ctx context.Context, snapshot *Snapshot) {
	if s == nil || s.titles == nil {
		return
	}
	ids := missingVersionTitleIDs(snapshot.Groups)
	if len(ids) == 0 {
		return
	}
	titles, err := s.titles.VersionTitles(ctx, ids)
	if err != nil {
		snapshot.Degraded = true
		return
	}
	applyVersionTitles(snapshot.Groups, titles)
}

// missingVersionTitleIDs returns the deduplicated positive version IDs of the
// items that still need a title, sorted so the single batch read is
// deterministic.
func missingVersionTitleIDs(groups []Group) []int64 {
	seen := make(map[int64]struct{})
	ids := make([]int64, 0)
	for _, group := range groups {
		for _, item := range group.Items {
			if item.VersionTitle != "" {
				continue
			}
			id, ok := parseVersionID(item.VersionID)
			if !ok {
				continue
			}
			if _, duplicate := seen[id]; duplicate {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(left, right int) bool { return ids[left] < ids[right] })
	return ids
}

func applyVersionTitles(groups []Group, titles map[int64]string) {
	if len(titles) == 0 {
		return
	}
	for groupIndex := range groups {
		items := groups[groupIndex].Items
		for itemIndex := range items {
			if items[itemIndex].VersionTitle != "" {
				continue
			}
			id, ok := parseVersionID(items[itemIndex].VersionID)
			if !ok {
				continue
			}
			if title, found := titles[id]; found {
				items[itemIndex].VersionTitle = title
			}
		}
	}
}

// parseVersionID decodes the decimal-string ID of the JSON boundary and drops
// the zero, negative and malformed IDs that cannot name a version.
func parseVersionID(raw string) (int64, bool) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
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
