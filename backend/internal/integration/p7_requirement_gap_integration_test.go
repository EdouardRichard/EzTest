//go:build integration

package integration_test

import (
	"context"
	"testing"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	deliverydomain "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	deliverypostgres "github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/postgres"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	executiondelivery "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/delivery"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// p7RequirementGapVersionReader accepts every version; the version-existence
// check itself is T2b coverage, not this P7 escalation persistence test.
type p7RequirementGapVersionReader struct{}

func (p7RequirementGapVersionReader) VersionExists(context.Context, int64) (bool, error) {
	return true, nil
}

// p7RequirementGapRefs accepts the requirement refs; cross-context ownership is
// covered by the ref validator's own tests.
type p7RequirementGapRefs struct{}

func (p7RequirementGapRefs) ValidateRefs(context.Context, int64, []deliverydomain.IssueRef) error {
	return nil
}

// p7RequirementGapIDs mints deterministic ids for the row under test.
type p7RequirementGapIDs struct{ next int64 }

func (g *p7RequirementGapIDs) Next() int64 {
	g.next++
	return g.next
}

// TestP7RequirementGapIssuesIntegration proves the supplement escalation
// materialization over the real PostgreSQL issue repository: the worker-facing
// adapter creates one AI requirement_gap issue with severity major, medium
// confidence, the deterministic §5.4 dedupe key and the requirement refs, and a
// replay is idempotent (no second issue, no recurrence bump).
func TestP7RequirementGapIssuesIntegration(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	const (
		versionID = int64(9901)
		batchID   = int64(99001)
		actor     = int64(42)
	)
	p7SeedVersion(t, pool, versionID, "executing", "", false)
	p7SeedBatch(t, pool, batchID, versionID, 1, "done", "baseline", nil, true)

	ctx := context.Background()
	service := deliveryapp.NewIssueService(deliveryapp.IssueServiceDeps{
		Store:    deliverypostgres.NewIssueRepository(pool),
		Versions: p7RequirementGapVersionReader{},
		Refs:     p7RequirementGapRefs{},
		IDs:      &p7RequirementGapIDs{next: 7000},
	})
	port := executiondelivery.NewRequirementGapIssues(service)
	escalation := executionapp.RequirementGapEscalation{
		Title:          "Missing refund requirement",
		Detail:         "no requirement covers partial refunds",
		RequirementIDs: []int64{56, 55, 56},
	}
	created, err := port.CreateRequirementGapIssues(ctx, versionID, batchID,
		[]executionapp.RequirementGapEscalation{escalation}, actor)
	if err != nil || created != 1 {
		t.Fatalf("CreateRequirementGapIssues = %d/%v, want 1/nil", created, err)
	}

	var (
		issueID                            int64
		source, category, severity, status string
		confidence, title, description     string
		dedupeKey                          string
		caseNodeID, caseRunID              *int64
		recurrence                         int
	)
	if err := pool.QueryRow(ctx, `
		SELECT id, source, category, severity, status, confidence, title, description,
		       dedupe_key, case_node_id, case_run_id, recurrence_count
		FROM issue WHERE version_id = $1`, versionID).Scan(
		&issueID, &source, &category, &severity, &status, &confidence, &title, &description,
		&dedupeKey, &caseNodeID, &caseRunID, &recurrence); err != nil {
		t.Fatalf("read requirement-gap issue: %v", err)
	}
	if source != deliverydomain.SourceAI || category != deliverydomain.CategoryRequirementGap ||
		severity != deliverydomain.SeverityMajor || status != deliverydomain.StatusOpen ||
		confidence != deliverydomain.ConfidenceMedium {
		t.Fatalf("issue fields=(%s,%s,%s,%s,%s) want ai/requirement_gap/major/open/medium",
			source, category, severity, status, confidence)
	}
	if title != "Missing refund requirement" || description != "no requirement covers partial refunds" {
		t.Fatalf("issue text=(%q,%q)", title, description)
	}
	if want := deliverydomain.RequirementGapDedupeKey(versionID, []int64{55, 56}); dedupeKey != want {
		t.Fatalf("dedupe key=%q want %q", dedupeKey, want)
	}
	if caseNodeID != nil || caseRunID != nil {
		t.Fatalf("requirement-gap issue carries case links: node=%v run=%v", caseNodeID, caseRunID)
	}

	rows, err := pool.Query(ctx, `
		SELECT ref_kind, ref_id FROM issue_ref WHERE issue_id = $1 ORDER BY ref_kind, ref_id`, issueID)
	if err != nil {
		t.Fatalf("read issue refs: %v", err)
	}
	defer rows.Close()
	var refs []string
	for rows.Next() {
		var kind, id string
		if err := rows.Scan(&kind, &id); err != nil {
			t.Fatalf("scan issue ref: %v", err)
		}
		refs = append(refs, kind+":"+id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read issue refs: %v", err)
	}
	if len(refs) != 2 || refs[0] != "requirement:55" || refs[1] != "requirement:56" {
		t.Fatalf("issue refs=%v want the two requirement refs", refs)
	}

	// Replay must not create a second issue or bump the recurrence.
	if created, err := port.CreateRequirementGapIssues(ctx, versionID, batchID,
		[]executionapp.RequirementGapEscalation{escalation}, actor); err != nil || created != 1 {
		t.Fatalf("replay CreateRequirementGapIssues = %d/%v, want 1/nil", created, err)
	}
	var (
		issueCount int
		storedID   int64
		storedRec  int
	)
	if err := pool.QueryRow(ctx, `
		SELECT count(*), min(id), max(recurrence_count) FROM issue WHERE version_id = $1`,
		versionID).Scan(&issueCount, &storedID, &storedRec); err != nil {
		t.Fatalf("read replay state: %v", err)
	}
	if issueCount != 1 || storedID != issueID || storedRec != 0 {
		t.Fatalf("replay created state: count=%d id=%d recurrence=%d", issueCount, storedID, storedRec)
	}
}
