package application

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// contractTestFact builds one AI-produced client_call/api fact entity. The fact
// key is computed through the domain rules so the tests exercise the same
// identity the analysis materialises.
func contractTestFact(t *testing.T, id int64, kind, name, path, confidence string, evidence ...domain.Evidence) domain.CodeEntity {
	t.Helper()
	key, err := domain.FactKey(kind, name, path)
	if err != nil {
		t.Fatalf("FactKey(%q, %q, %q): %v", kind, name, path, err)
	}
	version := 1
	return domain.CodeEntity{
		ID:          id,
		RepoID:      3,
		Commit:      testCommitB,
		Kind:        kind,
		Name:        name,
		Path:        path,
		Source:      domain.SourceAI,
		Confidence:  confidence,
		Evidence:    evidence,
		FactKey:     &key,
		FactVersion: &version,
	}
}

// contractTestEvidence builds one verified evidence entry.
func contractTestEvidence(file string, startLine, endLine int) domain.Evidence {
	return domain.Evidence{
		File:        file,
		StartLine:   startLine,
		EndLine:     endLine,
		ExcerptHash: fmt.Sprintf("sha256:%064x", startLine),
	}
}

// TestMatchContracts pins the pure cross-stack contract matching of design spec
// §6.5: method uppercase + normalized path equality, dynamic segments folded to
// {}, low-confidence facts excluded, evidence merged and the per-side cap
// reported.
func TestMatchContracts(t *testing.T) {
	t.Run("method and dynamic path segments match", func(t *testing.T) {
		client := contractTestFact(t, 101, domain.KindClientCall, "post /api/refund/{id}",
			"web/src/api/refund.ts", domain.ConfidenceMedium,
			contractTestEvidence("web/src/api/refund.ts", 3, 5))
		api := contractTestFact(t, 102, domain.KindAPI, "POST /api/refund/{orderId}",
			"server/refund.go", domain.ConfidenceHigh,
			contractTestEvidence("server/refund.go", 7, 9))

		pairs := MatchContracts([]domain.CodeEntity{client, api})
		if len(pairs) != 1 {
			t.Fatalf("pairs = %d, want 1: %+v", len(pairs), pairs)
		}
		pair := pairs[0]
		if pair.ClientEntityID != 101 || pair.APIEntityID != 102 {
			t.Fatalf("pair ids = %d/%d, want 101/102", pair.ClientEntityID, pair.APIEntityID)
		}
		if pair.Method != "POST" {
			t.Fatalf("method = %q, want POST", pair.Method)
		}
		if pair.NormalizedPath != "/api/refund/{}" {
			t.Fatalf("normalizedPath = %q, want /api/refund/{}", pair.NormalizedPath)
		}
		if pair.Confidence != domain.ConfidenceMedium {
			t.Fatalf("confidence = %q, want the weaker medium", pair.Confidence)
		}
		if len(pair.Evidence) != 2 {
			t.Fatalf("evidence = %+v, want both sides merged", pair.Evidence)
		}
	})

	t.Run("absolute URL query and trailing slash normalize", func(t *testing.T) {
		client := contractTestFact(t, 201, domain.KindClientCall,
			"POST https://api.example.test//api/refund/?id=42#frag",
			"web/src/api/refund.ts", domain.ConfidenceHigh)
		api := contractTestFact(t, 202, domain.KindAPI, "post /api/refund",
			"server/refund.go", domain.ConfidenceHigh)

		pairs := MatchContracts([]domain.CodeEntity{client, api})
		if len(pairs) != 1 || pairs[0].NormalizedPath != "/api/refund" || pairs[0].Method != "POST" {
			t.Fatalf("pairs = %+v, want one POST /api/refund contract", pairs)
		}
	})

	t.Run("different method or path does not match and counts unmatched", func(t *testing.T) {
		client := contractTestFact(t, 301, domain.KindClientCall, "POST /api/refund",
			"web/src/api/refund.ts", domain.ConfidenceHigh)
		api := contractTestFact(t, 302, domain.KindAPI, "GET /api/orders",
			"server/orders.go", domain.ConfidenceHigh)

		if pairs := MatchContracts([]domain.CodeEntity{client, api}); len(pairs) != 0 {
			t.Fatalf("pairs = %+v, want none", pairs)
		}
		stats := matchContractStats([]domain.CodeEntity{client, api})
		if stats.unmatched != 2 {
			t.Fatalf("unmatched = %d, want 2 (both sides)", stats.unmatched)
		}
	})

	t.Run("low confidence facts do not participate", func(t *testing.T) {
		client := contractTestFact(t, 401, domain.KindClientCall, "POST /api/refund",
			"web/src/api/refund.ts", domain.ConfidenceLow)
		api := contractTestFact(t, 402, domain.KindAPI, "POST /api/refund",
			"server/refund.go", domain.ConfidenceHigh)

		if pairs := MatchContracts([]domain.CodeEntity{client, api}); len(pairs) != 0 {
			t.Fatalf("pairs = %+v, want none for a low clue", pairs)
		}
		// The low client_call is not eligible; the high api fact is and remains
		// unmatched.
		if stats := matchContractStats([]domain.CodeEntity{client, api}); stats.unmatched != 1 {
			t.Fatalf("unmatched = %d, want 1 (the eligible unmatched api fact)", stats.unmatched)
		}
	})

	t.Run("duplicate evidence is merged once", func(t *testing.T) {
		evidence := contractTestEvidence("shared.ts", 10, 12)
		client := contractTestFact(t, 501, domain.KindClientCall, "POST /api/refund",
			"web/src/api/refund.ts", domain.ConfidenceHigh, evidence)
		api := contractTestFact(t, 502, domain.KindAPI, "POST /api/refund",
			"server/refund.go", domain.ConfidenceHigh, evidence)

		pairs := MatchContracts([]domain.CodeEntity{client, api})
		if len(pairs) != 1 || len(pairs[0].Evidence) != 1 {
			t.Fatalf("pairs = %+v, want one pair with one deduplicated evidence entry", pairs)
		}
	})

	t.Run("per-fact cap is 20 and truncation is counted", func(t *testing.T) {
		client := contractTestFact(t, 601, domain.KindClientCall, "POST /api/refund",
			"web/src/api/refund.ts", domain.ConfidenceHigh)
		facts := []domain.CodeEntity{client}
		for index := 0; index < 21; index++ {
			facts = append(facts, contractTestFact(t, int64(700+index), domain.KindAPI,
				"POST /api/refund", "server/refund.go", domain.ConfidenceHigh))
		}

		stats := matchContractStats(facts)
		if len(stats.pairs) != 20 {
			t.Fatalf("pairs = %d, want the 20-edge cap", len(stats.pairs))
		}
		if stats.truncated != 1 {
			t.Fatalf("truncated = %d, want 1", stats.truncated)
		}
		// The 21st api fact is eligible but beyond the per-side cap, so it stays
		// unmatched.
		if stats.unmatched != 1 {
			t.Fatalf("unmatched = %d, want 1 (the capped-out api fact)", stats.unmatched)
		}
	})

	t.Run("matching is deterministic", func(t *testing.T) {
		clientA := contractTestFact(t, 801, domain.KindClientCall, "POST /api/refund",
			"web/a.ts", domain.ConfidenceHigh)
		clientB := contractTestFact(t, 802, domain.KindClientCall, "POST /api/refund",
			"web/b.ts", domain.ConfidenceHigh)
		api := contractTestFact(t, 803, domain.KindAPI, "POST /api/refund",
			"server/refund.go", domain.ConfidenceHigh)

		first := MatchContracts([]domain.CodeEntity{clientB, api, clientA})
		second := MatchContracts([]domain.CodeEntity{clientA, clientB, api})
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("matching is not deterministic:\n%+v\n%+v", first, second)
		}
	})
}
