package application

import (
	"sort"
	"strconv"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

const (
	// maxContractsPerFact bounds the contract edges of one side (design spec
	// §6.5: 单侧事实最多匹配 20 条边).
	maxContractsPerFact = 20
	// maxContractEvidence bounds the merged evidence of one contract edge. The
	// spec only bounds per-fact evidence; reusing that cap keeps an edge (two
	// merged facts) within the documented evidence budget.
	maxContractEvidence = domain.MaxEvidencePerFact
)

// ContractPair is one matched cross-stack API contract (design spec §6.5): a
// client_call fact and an api fact whose normalized (method, path) pair is
// identical. Entity ids and fact keys/versions let the caller materialise the
// route_to_api edge without re-matching; Evidence is the deduplicated union of
// both sides' valid evidence.
type ContractPair struct {
	ClientEntityID int64
	APIEntityID    int64
	ClientFactKey  string
	APIFactKey     string
	ClientVersion  int
	APIVersion     int
	Method         string
	NormalizedPath string
	Confidence     string
	Evidence       []domain.Evidence
}

// MatchContracts matches every eligible client_call fact against every eligible
// api fact of one head commit and returns the exact (method, normalized path)
// pairs. Only high/medium confidence facts participate (INV-1: low clues never
// produce automatic relationships), a single fact matches at most
// maxContractsPerFact counterparts, and the output order is deterministic: by
// client fact key, then by api fact key. The function is pure so T13 and the
// tests can reuse it without any repository.
func MatchContracts(facts []domain.CodeEntity) []ContractPair {
	return matchContractStats(facts).pairs
}

// contractMatchStats is the full result of one matching pass. The analysis
// service persists stats.unmatchedContracts and stats.contractTruncated from
// unmatched/truncated; MatchContracts exposes only the pairs because that is
// the materialisation input.
type contractMatchStats struct {
	pairs     []ContractPair
	unmatched int
	truncated int
}

// contractEndpoint is one eligible fact projected onto its matching key.
type contractEndpoint struct {
	entity domain.CodeEntity
	method string
	path   string
	key    string
}

// matchContractStats runs the deterministic contract matching pass.
func matchContractStats(facts []domain.CodeEntity) contractMatchStats {
	var stats contractMatchStats

	clients := make([]contractEndpoint, 0, len(facts))
	apis := make([]contractEndpoint, 0, len(facts))
	for _, fact := range facts {
		endpoint, ok := contractEndpointFor(fact)
		if !ok {
			continue
		}
		switch endpoint.entity.Kind {
		case domain.KindClientCall:
			clients = append(clients, endpoint)
		case domain.KindAPI:
			apis = append(apis, endpoint)
		}
	}
	sort.Slice(clients, func(i, j int) bool {
		if factKeyOf(clients[i].entity) != factKeyOf(clients[j].entity) {
			return factKeyOf(clients[i].entity) < factKeyOf(clients[j].entity)
		}
		return clients[i].entity.ID < clients[j].entity.ID
	})
	sort.Slice(apis, func(i, j int) bool {
		if factKeyOf(apis[i].entity) != factKeyOf(apis[j].entity) {
			return factKeyOf(apis[i].entity) < factKeyOf(apis[j].entity)
		}
		return apis[i].entity.ID < apis[j].entity.ID
	})

	apisByKey := make(map[string][]contractEndpoint, len(apis))
	for _, api := range apis {
		apisByKey[api.key] = append(apisByKey[api.key], api)
	}
	apiUses := make(map[int64]int, len(apis))
	clientMatched := make(map[int64]bool, len(clients))

	for _, client := range clients {
		candidates := apisByKey[client.key]
		kept := 0
		for _, api := range candidates {
			if kept >= maxContractsPerFact || apiUses[api.entity.ID] >= maxContractsPerFact {
				stats.truncated++
				continue
			}
			kept++
			apiUses[api.entity.ID]++
			clientMatched[client.entity.ID] = true
			stats.pairs = append(stats.pairs, ContractPair{
				ClientEntityID: client.entity.ID,
				APIEntityID:    api.entity.ID,
				ClientFactKey:  factKeyOf(client.entity),
				APIFactKey:     factKeyOf(api.entity),
				ClientVersion:  factVersionOf(client.entity),
				APIVersion:     factVersionOf(api.entity),
				Method:         client.method,
				NormalizedPath: client.path,
				Confidence:     weakerConfidence(client.entity.Confidence, api.entity.Confidence),
				Evidence:       mergeContractEvidence(client.entity.Evidence, api.entity.Evidence),
			})
		}
	}

	for _, client := range clients {
		if !clientMatched[client.entity.ID] {
			stats.unmatched++
		}
	}
	for _, api := range apis {
		if apiUses[api.entity.ID] == 0 {
			stats.unmatched++
		}
	}
	return stats
}

// contractEndpointFor projects one fact onto its matching key. Only client_call
// and api facts with a stable fact identity at high/medium confidence and a
// normalizable HTTP method + path are eligible (design spec §6.5).
func contractEndpointFor(fact domain.CodeEntity) (contractEndpoint, bool) {
	if fact.Kind != domain.KindClientCall && fact.Kind != domain.KindAPI {
		return contractEndpoint{}, false
	}
	if fact.FactKey == nil || *fact.FactKey == "" {
		return contractEndpoint{}, false
	}
	if fact.Confidence != domain.ConfidenceHigh && fact.Confidence != domain.ConfidenceMedium {
		return contractEndpoint{}, false
	}
	method, path := domain.NormalizeMethodPath("", fact.Name)
	if method == "" || path == "" {
		return contractEndpoint{}, false
	}
	return contractEndpoint{
		entity: fact,
		method: method,
		path:   path,
		key:    method + " " + path,
	}, true
}

// factKeyOf returns the fact key or "" when the entity has none.
func factKeyOf(entity domain.CodeEntity) string {
	if entity.FactKey == nil {
		return ""
	}
	return *entity.FactKey
}

// factVersionOf returns the fact version or 0 when the entity has none.
func factVersionOf(entity domain.CodeEntity) int {
	if entity.FactVersion == nil {
		return 0
	}
	return *entity.FactVersion
}

// weakerConfidence returns the lower of two confidence levels (design spec
// §6.5: confidence = min(两侧)).
func weakerConfidence(left, right string) string {
	if confidenceRankForContract(right) > confidenceRankForContract(left) {
		return right
	}
	return left
}

// confidenceRankForContract orders high < medium < low.
func confidenceRankForContract(confidence string) int {
	switch confidence {
	case domain.ConfidenceHigh:
		return 0
	case domain.ConfidenceMedium:
		return 1
	case domain.ConfidenceLow:
		return 2
	default:
		return 3
	}
}

// mergeContractEvidence deduplicates, sorts and caps the evidence of both sides
// of a contract. The excerpt hash is part of the identity so two different
// excerpts at the same lines are both kept.
func mergeContractEvidence(parts ...[]domain.Evidence) []domain.Evidence {
	unique := make(map[string]struct{})
	merged := make([]domain.Evidence, 0, maxContractEvidence)
	for _, part := range parts {
		for _, evidence := range part {
			if evidence.File == "" {
				continue
			}
			key := evidence.File + "\x00" + evidence.ExcerptHash +
				"\x00" + strconv.Itoa(evidence.StartLine) + "\x00" + strconv.Itoa(evidence.EndLine)
			if _, ok := unique[key]; ok {
				continue
			}
			unique[key] = struct{}{}
			merged = append(merged, evidence)
		}
	}
	sort.Slice(merged, func(i, j int) bool {
		left, right := merged[i], merged[j]
		if left.File != right.File {
			return left.File < right.File
		}
		if left.StartLine != right.StartLine {
			return left.StartLine < right.StartLine
		}
		if left.EndLine != right.EndLine {
			return left.EndLine < right.EndLine
		}
		return left.ExcerptHash < right.ExcerptHash
	})
	if len(merged) > maxContractEvidence {
		merged = merged[:maxContractEvidence]
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}
