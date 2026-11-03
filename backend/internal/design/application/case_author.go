package designapp

// Case-author discovery facts. The case author designs cases from the current
// requirements; the exploration layer's page/API/gap/login facts are its
// evidence for what the SUT actually exposes (spec §11, D25). This projection
// is bounded, credential-free and read-only: it never mutates a requirement, a
// case or a fact.
//
// The reader port is declared over a neutral record so the design context does
// not import the version context; the composition root adapts
// versionapp.FactService.Current onto it.

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// Discovery fact kinds read by the case author. They mirror the version_fact
// kinds written by the exploration capabilities (page/api/gap/login_session).
const (
	DiscoveryFactKindPage         = "page"
	DiscoveryFactKindAPI          = "api"
	DiscoveryFactKindGap          = "gap"
	DiscoveryFactKindLoginSession = "login_session"
)

// Case-author fact bounds. One case-author read is bounded per kind and in
// total, so a large exploration run cannot flood the design context.
const (
	maxCaseAuthorFacts        = 100
	maxCaseAuthorFactsPerKind = 50
	maxCaseAuthorFactSummary  = 200
)

// discoveryFactKindOrder freezes the rendering order of the fact kinds.
var discoveryFactKindOrder = []string{
	DiscoveryFactKindPage,
	DiscoveryFactKindAPI,
	DiscoveryFactKindGap,
	DiscoveryFactKindLoginSession,
}

// DiscoveryFactRecord is the neutral projection of one current version fact.
// Payload is carried verbatim only inside the design context; CaseAuthorFacts
// renders a bounded summary and never exposes the payload itself.
type DiscoveryFactRecord struct {
	ID         int64
	Kind       string
	Key        string
	Payload    map[string]any
	Confidence *float64
}

// DiscoveryFactsReader reads the current facts of one version. An empty kind
// widens the query to every kind.
type DiscoveryFactsReader interface {
	Current(ctx context.Context, versionID int64, kind string) ([]DiscoveryFactRecord, error)
}

// DiscoveryFact is one bounded, credential-free discovery fact summary shown to
// the case author.
type DiscoveryFact struct {
	ID         int64
	Kind       string
	Key        string
	Summary    string
	Confidence *float64
}

// SetDiscoveryFacts attaches the read-only discovery-facts projection used by
// the case author. It returns the service for fluent wiring.
func (s *GraphService) SetDiscoveryFacts(reader DiscoveryFactsReader) *GraphService {
	if s != nil {
		s.discovery = reader
	}
	return s
}

// CaseAuthorFacts returns the bounded discovery facts the case author reads
// before proposing cases. The read is best effort: a missing reader, a read
// failure or a malformed payload yields fewer facts instead of failing the
// design graph read, because the graph remains valid without exploration
// evidence.
func (s *GraphService) CaseAuthorFacts(ctx context.Context, versionID int64) ([]DiscoveryFact, error) {
	if versionID <= 0 {
		return nil, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if s == nil || s.discovery == nil {
		return []DiscoveryFact{}, nil
	}
	facts := make([]DiscoveryFact, 0)
	for _, kind := range discoveryFactKindOrder {
		if len(facts) >= maxCaseAuthorFacts {
			break
		}
		records, err := s.discovery.Current(ctx, versionID, kind)
		if err != nil {
			return []DiscoveryFact{}, nil
		}
		sort.SliceStable(records, func(i, j int) bool { return records[i].ID < records[j].ID })
		if len(records) > maxCaseAuthorFactsPerKind {
			records = records[:maxCaseAuthorFactsPerKind]
		}
		for _, record := range records {
			if len(facts) >= maxCaseAuthorFacts {
				break
			}
			facts = append(facts, DiscoveryFact{
				ID:         record.ID,
				Kind:       trimFactKind(record.Kind, kind),
				Key:        clipFactString(record.Key, maxCaseAuthorFactSummary),
				Summary:    discoveryFactSummary(kind, record.Payload),
				Confidence: record.Confidence,
			})
		}
	}
	if facts == nil {
		facts = []DiscoveryFact{}
	}
	return facts, nil
}

// discoveryFactSummary renders the allow-listed summary fields of one fact. The
// payload's element inventories, page URLs with query values and session/storage
// references are deliberately never rendered: the case author needs the
// knowledge key, not the raw exploration payload.
func discoveryFactSummary(kind string, payload map[string]any) string {
	switch kind {
	case DiscoveryFactKindPage:
		return joinFactParts(
			payloadString(payload, "route_template", "page_key"),
			payloadString(payload, "title"),
		)
	case DiscoveryFactKindAPI:
		return joinFactParts(
			payloadString(payload, "api_key"),
			payloadString(payload, "state"),
			payloadString(payload, "auth"),
		)
	case DiscoveryFactKindGap:
		requirement := payloadScalar(payload, "requirement_id")
		return joinFactParts(
			payloadString(payload, "dimension"),
			requirement,
			payloadString(payload, "target"),
			payloadString(payload, "reason"),
		)
	case DiscoveryFactKindLoginSession:
		return joinFactParts(
			payloadString(payload, "account_role"),
			payloadString(payload, "route"),
		)
	default:
		return ""
	}
}

func joinFactParts(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			kept = append(kept, trimmed)
		}
	}
	return clipFactString(strings.Join(kept, " · "), maxCaseAuthorFactSummary)
}

func payloadString(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := payload[key].(string); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

// payloadScalar renders an allow-listed numeric identifier (requirement_id) as a
// decimal string; other payload values are never rendered.
func payloadScalar(payload map[string]any, key string) string {
	switch value := payload[key].(type) {
	case int64:
		return strconv.FormatInt(value, 10)
	case int:
		return strconv.Itoa(value)
	case float64:
		return strconv.FormatInt(int64(value), 10)
	case string:
		return strings.TrimSpace(value)
	default:
		return ""
	}
}

func trimFactKind(kind, fallback string) string {
	if trimmed := strings.TrimSpace(kind); trimmed != "" {
		return trimmed
	}
	return fallback
}

func clipFactString(value string, limit int) string {
	trimmed := strings.TrimSpace(value)
	if limit <= 0 {
		return ""
	}
	runes := []rune(trimmed)
	if len(runes) <= limit {
		return trimmed
	}
	return string(runes[:limit])
}
