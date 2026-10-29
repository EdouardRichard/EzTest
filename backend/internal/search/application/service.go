package application

import (
	"context"
	"errors"
	"strings"
	"sync"
)

// providerBinding pairs one cross-context provider with the result types it
// owns (spec §3.3).
type providerBinding struct {
	provider Provider
	types    []string
}

// Service aggregates the per-context providers into the global-search read
// model. It holds no state and performs no caching: every Search call is
// independent.
type Service struct {
	providers Providers
}

// NewService builds the global-search aggregation service.
func NewService(providers Providers) *Service {
	return &Service{providers: providers}
}

// Search validates the query, calls the relevant providers in parallel and
// assembles the canonical group order. A provider failure only marks the
// snapshot degraded: the healthy groups are still returned (spec §6.2).
func (s *Service) Search(ctx context.Context, query Query) (Snapshot, error) {
	if s == nil {
		return Snapshot{}, errors.New("search: service is not configured")
	}
	text, err := ValidateQueryText(query.Text)
	if err != nil {
		return Snapshot{}, err
	}
	requestedTypes, err := normalizeTypes(query.Types)
	if err != nil {
		return Snapshot{}, err
	}
	requested := make(map[string]struct{}, len(requestedTypes))
	for _, typeName := range requestedTypes {
		requested[typeName] = struct{}{}
	}
	limit := normalizeLimit(query.Limit)

	bindings := []providerBinding{
		{provider: s.providers.Version, types: []string{TypeVersion, TypeRequirement}},
		{provider: s.providers.Mindmap, types: []string{TypeCase}},
		{provider: s.providers.Delivery, types: []string{TypeIssue}},
	}

	// Every provider owns at least one requested type: query it concurrently.
	outcomes := make([]providerOutcome, len(bindings))
	var wait sync.WaitGroup
	for index, binding := range bindings {
		if !ownsRequested(binding.types, requested) {
			continue
		}
		if binding.provider == nil {
			outcomes[index].err = errors.New("search: provider is not configured")
			continue
		}
		wait.Add(1)
		go func(index int, provider Provider) {
			defer wait.Done()
			groups, searchErr := provider.Search(ctx, text, limit)
			outcomes[index] = providerOutcome{groups: groups, err: searchErr}
		}(index, binding.provider)
	}
	wait.Wait()

	itemsByType := make(map[string][]Item, len(requestedTypes))
	for _, typeName := range requestedTypes {
		itemsByType[typeName] = make([]Item, 0)
	}
	degraded := false
	truncated := false
	for index, binding := range bindings {
		if !ownsRequested(binding.types, requested) {
			continue
		}
		outcome := outcomes[index]
		if outcome.err != nil {
			// One failing context must not fail the whole search: keep the
			// healthy groups and tell the caller the result is partial.
			degraded = true
			continue
		}
		for _, group := range outcome.groups {
			if _, wanted := requested[group.Type]; !wanted {
				// A provider may only return its own types; ignore anything else
				// so a buggy adapter cannot smuggle a group into the response.
				continue
			}
			items := group.Items
			if items == nil {
				items = make([]Item, 0)
			}
			if len(items) > limit {
				items = items[:limit]
			}
			if len(items) >= limit {
				truncated = true
			}
			itemsByType[group.Type] = append(itemsByType[group.Type], items...)
		}
	}

	groups := make([]Group, 0, len(requestedTypes))
	total := 0
	for _, typeName := range requestedTypes {
		items := itemsByType[typeName]
		if items == nil {
			items = make([]Item, 0)
		}
		total += len(items)
		groups = append(groups, Group{Type: typeName, Items: items})
	}

	return Snapshot{
		Groups:    groups,
		Total:     total,
		Truncated: truncated,
		Degraded:  degraded,
	}, nil
}

// providerOutcome is the result slot of one provider goroutine. Each goroutine
// writes a distinct slice element, so no lock is needed.
type providerOutcome struct {
	groups []Group
	err    error
}

// ownsRequested reports whether any type of the provider was requested.
func ownsRequested(owned []string, requested map[string]struct{}) bool {
	for _, typeName := range owned {
		if _, ok := requested[typeName]; ok {
			return true
		}
	}
	return false
}

// ValidateTypes validates the requested type filter. It is exported so the HTTP
// layer can reject an unknown type before it constructs a request.
func ValidateTypes(types []string) error {
	_, err := normalizeTypes(types)
	return err
}

// normalizeLimit applies the documented per-type bounds: a zero/negative limit
// selects the default and anything above the maximum is clamped.
func normalizeLimit(limit int) int {
	if limit <= 0 {
		return DefaultLimit
	}
	if limit > MaxLimit {
		return MaxLimit
	}
	return limit
}

// normalizeTypes validates the requested type filter and returns it in canonical
// order. An empty filter selects every type.
func normalizeTypes(types []string) ([]string, error) {
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
			return nil, &ErrInvalidArgument{
				Field:   "types",
				Reason:  ReasonInvalidArgument,
				Message: "types must be a subset of version,requirement,case,issue",
			}
		}
		requested[typeName] = struct{}{}
	}
	if len(requested) == 0 {
		return nil, &ErrInvalidArgument{
			Field:   "types",
			Reason:  ReasonInvalidArgument,
			Message: "types must be a subset of version,requirement,case,issue",
		}
	}
	ordered := make([]string, 0, len(requested))
	for _, typeName := range AllTypes {
		if _, ok := requested[typeName]; ok {
			ordered = append(ordered, typeName)
		}
	}
	return ordered, nil
}

// isSupportedType reports whether typeName is one of the four search types.
func isSupportedType(typeName string) bool {
	for _, candidate := range AllTypes {
		if candidate == typeName {
			return true
		}
	}
	return false
}
