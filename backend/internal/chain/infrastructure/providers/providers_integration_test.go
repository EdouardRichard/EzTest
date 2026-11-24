//go:build integration

package providers

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	chainapp "github.com/EdouardRichard/EzTest/backend/internal/chain/application"
	chain "github.com/EdouardRichard/EzTest/backend/internal/chain/domain"
	deliverypg "github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/postgres"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	designpg "github.com/EdouardRichard/EzTest/backend/internal/design/infrastructure/postgres"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	eventing "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
	eventingpg "github.com/EdouardRichard/EzTest/backend/internal/eventing/infrastructure"
	executionpg "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	resourcepg "github.com/EdouardRichard/EzTest/backend/internal/resource/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versionpg "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Fixed snowflake-shaped fixtures of the P4 chain integration database.
const (
	itVersionID = 9001

	itSUT1  = 9101
	itSUT2  = 9102
	itEnv1  = 9201
	itPage1 = 9301
	itPage2 = 9302

	itRequirement1  = 9501
	itRequirementV1 = 9511
	itRequirement2  = 9502
	itRequirementV2 = 9512

	itScenario1 = 9601
	itScenario2 = 9602

	itCase1           = 9701
	itCaseV1          = 9711
	itCase2           = 9702
	itCaseV2          = 9712
	itCaseVSuperseded = 9713

	itWave1   = 9801
	itWave2   = 9802
	itIntent1 = 9811
	itIntent2 = 9812
	itRun1    = 9821
	itRun2    = 9822
	itSUTRun2 = 9831

	itIssueID     = 9901
	itReportID    = 9911
	itReportRevID = 9912
	itClosureID   = 9951
	itEventCursor = 3
)

var itClock = clock.System{}

func itHash(letter string) string { return strings.Repeat(letter, 64) }

// itDedupeKey is a valid 32-hex delivery dedupe key.
func itDedupeKey() string { return strings.Repeat("c", 32) }

// newChainProviders builds every real provider over one migrated test schema
// and returns them together with the assembled chain service.
func newChainProviders(t *testing.T) (*pgxpool.Pool, *ChainServiceBundle) {
	t.Helper()
	pool := testutil.NewDB(t)
	eventTimes := insertChainFixtures(t, pool)

	ids, err := platformid.NewSnowflake(1)
	if err != nil {
		t.Fatalf("snowflake generator: %v", err)
	}
	resourceRepo := resourcepg.New(pool)
	resourceSvc := resourceapp.New(resourceRepo, ids, itClock, nil, nil, nil)
	versionRepo := versionpg.New(pool, ids)
	versionSvc := versionapp.New(versionRepo, resourceSvc, ids, itClock, nil, nil, nil)
	requirementRepo := versionpg.NewRequirementRepository(pool)
	requirementSvc := versionapp.NewRequirementService(
		requirementRepo, nil, nil, nil, nil, ids, itClock,
	)
	designSvc := designapp.NewGraphService(designpg.New(pool), ids, itClock, nil, nil, nil)
	executionRepos := executionpg.NewRepository(pool, ids)
	closureRepo := versionpg.NewClosureRepository(pool)
	closureSvc := versionapp.NewClosureService(versionapp.ClosureServiceDeps{
		Closures:     closureRepo,
		Requirements: requirementRepo,
		Clock:        itClock,
	})
	issueRepo := deliverypg.NewIssueRepository(pool, ids)
	reportRepo := deliverypg.NewReportRepository(pool, ids)
	eventRepo := eventingpg.New(pool)
	eventSvc := eventingapp.New(eventRepo, nil)

	versionProvider := NewVersionProvider(requirementSvc, versionSvc)
	designProvider := NewDesignProvider(designSvc)
	executionProvider := NewExecutionProvider(executionRepos.Waves, executionRepos.CaseRuns, executionRepos.SUTContexts)
	deliveryProvider := NewDeliveryProvider(issueRepo, reportRepo, requirementSvc, closureSvc)
	resourceProvider := NewResourceProvider(versionSvc, resourceSvc)
	cursorProvider := NewEventCursorProvider(eventSvc)

	service := chainapp.NewChainService(
		versionProvider, designProvider, executionProvider, deliveryProvider,
		resourceProvider, cursorProvider, cursorProvider, itClock,
	)
	return pool, &ChainServiceBundle{
		Service:      service,
		Version:      versionProvider,
		Design:       designProvider,
		Execution:    executionProvider,
		Delivery:     deliveryProvider,
		Resource:     resourceProvider,
		Cursor:       cursorProvider,
		DesignReader: designSvc,
		EventTimes:   eventTimes,
	}
}

// ChainServiceBundle groups the constructed providers for direct assertions.
type ChainServiceBundle struct {
	Service      *chainapp.ChainService
	Version      *VersionProvider
	Design       *DesignProvider
	Execution    *ExecutionProvider
	Delivery     *DeliveryProvider
	Resource     *ResourceProvider
	Cursor       *EventCursorProvider
	DesignReader *designapp.GraphService
	EventTimes   map[int64]time.Time
}

// insertChainFixtures writes the P4 spec §8.5 integration dataset: 2
// requirements, 2 current cases plus 1 superseded case version, 2 waves, 2 case
// runs, 1 issue, 1 report and 1 closure. It returns the created_at timestamps of
// the three appended channel events keyed by seq (1..3).
func insertChainFixtures(t *testing.T, pool *pgxpool.Pool) map[int64]time.Time {
	t.Helper()
	ctx := context.Background()
	statements := []struct {
		sql  string
		args []any
	}{
		// v0.2.4-B: environment is top-level; the version binds one environment
		// whose SUTs own the pages. There is no version_sut binding row anymore,
		// so the SUT id is the key the design/execution/delivery rows reference.
		{`INSERT INTO environment (id, name, status) VALUES ($1, 'P4 chain env', 'active')`,
			[]any{itEnv1}},
		{`INSERT INTO sut (id, environment_id, name, intro, base_url, created_by)
		  VALUES ($1, $3, 'Payments', '', 'https://pay.example.test', 1),
		         ($2, $3, 'Checkout', '', 'https://checkout.example.test', 1)`,
			[]any{itSUT1, itSUT2, itEnv1}},
		{`INSERT INTO sut_page (id, sut_id, name, url, route_template)
		  VALUES ($1, $2, 'Checkout page', 'https://pay.example.test/checkout', '/checkout'),
		         ($3, $4, 'Refund page', 'https://checkout.example.test/refund', '/refund')`,
			[]any{itPage1, itSUT1, itPage2, itSUT2}},
		{`INSERT INTO version (id, title, status, auto_design_new_requirements, created_by, environment_id, created_at, updated_at)
		  VALUES ($1, 'P4 chain integration', 'running', false, 1, $2, now(), now())`,
			[]any{itVersionID, itEnv1}},
		{`INSERT INTO requirement (id, version_id, current_version_id, status,
		                          closed_at, created_at, updated_at)
		  VALUES ($1, $4, $2, 'clarified', NULL, now(), now()),
		         ($3, $4, $5, 'closed', now(), now(), now())`,
			[]any{itRequirement1, itRequirementV1, itRequirement2, itVersionID, itRequirementV2}},
		{`INSERT INTO requirement_version (id, requirement_id, revision, title, priority,
		                                   content_hash, created_at)
		  VALUES ($1, $2, 1, 'Pay flow', 'P1', $3, now()),
		         ($4, $5, 1, 'Refund flow', 'P2', $6, now())`,
			[]any{itRequirementV1, itRequirement1, itHash("a"), itRequirementV2, itRequirement2, itHash("b")}},
		{`INSERT INTO case_scenario (id, version_id, requirement_id, requirement_version_id, name,
		                             description, sort_order)
		  VALUES ($1, $3, $4, $5, 'Pay scenario', '', 0),
		         ($2, $3, $6, $7, 'Refund scenario', '', 0)`,
			[]any{itScenario1, itScenario2, itVersionID, itRequirement1, itRequirementV1, itRequirement2, itRequirementV2}},
		{`INSERT INTO case_entity (id, scenario_id, current_version_id, status)
		  VALUES ($1, $3, $5, 'confirmed'), ($2, $4, $6, 'confirmed')`,
			[]any{itCase1, itCase2, itScenario1, itScenario2, itCaseV1, itCaseV2}},
		{`INSERT INTO case_version (id, case_id, revision, title, description, priority,
		                            sut_description, covered_acs,
		                            status, content_hash, created_at, updated_at)
		  VALUES ($1, $4, 1, 'Pay with card', '', 'P1',
		          'Payments checkout page and the pricing API.', '["AC-1","AC-2"]'::jsonb,
		          'confirmed', $6, now(), now()),
		         ($2, $5, 1, 'Refund order', '', 'P2',
		          'Checkout refund flow.', '["AC-1"]'::jsonb,
		          'confirmed', $7, now(), now()),
		         ($3, $4, 2, 'Superseded pay title', '', 'P1',
		          'Payments checkout page.', '["AC-1"]'::jsonb,
		          'superseded', $8, now(), now())`,
			[]any{itCaseV1, itCaseV2, itCaseVSuperseded, itCase1, itCase2, itHash("c"), itHash("d"), itHash("e")}},
		// v0.2.4-B (T27 fixture follow-up): case_scenario / case_version no longer
		// carry version_sut_ids / page_ids / api_ids / default_version_sut_id --
		// design_0004_v024_case_contract.sql dropped them and the natural-language
		// case contract (sut_description + covered_acs) replaced them. The seeds
		// above now match the current schema; the chain case node projects those
		// two typed fields (T27/U8). execution_intent.sut_ids /
		// case_run_sut_context.sut_id were renamed by execution_0008_v024_rekey (T11).
		{`INSERT INTO execution_intent (id, version_id, case_version_id, sut_ids,
		                                environment_fingerprint, purpose, status, dedupe_key)
		  VALUES ($1, $3, $4, '["9101"]'::jsonb, $5, 'initial', 'done', 'chain-intent-1'),
		         ($2, $3, $6, '["9102"]'::jsonb, $7, 'initial', 'done', 'chain-intent-2')`,
			[]any{itIntent1, itIntent2, itVersionID, itCaseV1, itHash("f"), itCaseVSuperseded, itHash("1")}},
		{`INSERT INTO run_wave (id, version_id, status, trigger, counts, finished_at, created_at, updated_at)
		  VALUES ($1, $3, 'done', 'auto',
		          '{"total":1,"passed":1,"failed":0,"evidence":1}'::jsonb, now(), now(), now()),
		         ($2, $3, 'failed', 'auto',
		          '{"total":1,"passed":0,"failed":1,"evidence":2}'::jsonb, now(), now(), now())`,
			[]any{itWave1, itWave2, itVersionID}},
		{`INSERT INTO case_run (id, wave_id, intent_id, case_version_id, status, failure_class,
		                        summary, created_at, updated_at)
		  VALUES ($1, $3, $5, $7, 'passed', '',
		          '{"step_total":2,"step_failed":0,"assertion_total":1,"assertion_failed":0,"evidence_count":1}'::jsonb,
		          now(), now()),
		         ($2, $4, $6, $8, 'failed', 'application',
		          '{"step_total":3,"step_failed":1,"assertion_total":2,"assertion_failed":1,"evidence_count":2,"verdict_reason":"amount mismatch"}'::jsonb,
		          now(), now())`,
			[]any{itRun1, itRun2, itWave1, itWave2, itIntent1, itIntent2, itCaseV1, itCaseVSuperseded}},
		{`INSERT INTO case_run_sut_context (id, case_run_id, sut_id, account_role, status)
		  VALUES ($1, $2, $3, 'admin', 'closed')`,
			[]any{itSUTRun2, itRun2, itSUT2}},
		{`INSERT INTO delivery_issue (id, version_id, title, summary, status, category, severity,
		                              dedupe_key, source, expected_revision, created_at, updated_at)
		  VALUES ($1, $2, 'Amount mismatch', 'expected 10 got 20', 'open', 'api', 'major',
		          $3, 'failure_candidate', 1, now(), now())`,
			[]any{itIssueID, itVersionID, itDedupeKey()}},
		{`INSERT INTO delivery_issue_ref (id, issue_id, kind, ref_id, sut_id, role)
		  VALUES (901, $1, 'case_run', $2, $3, 'source'),
		         (902, $1, 'wave', $4, NULL, 'source'),
		         (903, $1, 'requirement', $5, NULL, 'source')`,
			[]any{itIssueID, itRun2, itSUT2, itWave2, itRequirement1}},
		{`INSERT INTO delivery_report (id, version_id, wave_id, kind, current_revision_id, status, input_fingerprint)
		  VALUES ($1, $2, $4, 'wave', $3, 'published', $5)`,
			[]any{itReportID, itVersionID, itReportRevID, itWave2, itHash("2")}},
		{`INSERT INTO delivery_report_revision (id, report_id, revision, status, input_fingerprint,
		                                        as_of, template_version, render_status)
		  VALUES ($1, $2, 1, 'published', $3, now(), '1', 'done')`,
			[]any{itReportRevID, itReportID, itHash("2")}},
		{`INSERT INTO delivery_report_ref (id, report_id, report_revision_id, kind, ref_id)
		  VALUES (911, $1, $2, 'issue', $3), (912, $1, $2, 'case_run', $4),
		         (913, $1, $2, 'wave', $5), (914, $1, $2, 'sut', $6),
		         (915, $1, $2, 'requirement', $7)`,
			[]any{itReportID, itReportRevID, itIssueID, itRun2, itWave2, itSUT1, itRequirement1}},
		{`INSERT INTO requirement_closure (id, requirement_id, requirement_version_id, close_round,
		                                   check_result, evidence_refs, closed_at, reopen_count)
		  VALUES ($1, $2, $3, 1,
		          '{"requirement_state":true,"has_confirmed_case":true,"cases_terminal":true,"failed_case_has_issue":true,"no_open_work":true,"no_active_intent":true,"no_inflight_case_run":true,"coverage_recorded":true,"waived":false}'::jsonb,
		          '[]'::jsonb, now(), 1)`,
			[]any{itClosureID, itRequirement2, itRequirementV2}},
	}
	for index, statement := range statements {
		if _, err := pool.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatalf("fixture statement %d: %v\n%s", index, err, statement.sql)
		}
	}

	eventRepo := eventingpg.New(pool)
	eventTimes := make(map[int64]time.Time, 3)
	for _, eventType := range []string{"version.started", "requirement.clarified", "case.confirmed"} {
		stored, err := eventRepo.Append(ctx, eventing.Event{
			Channel: "version:" + strconv.FormatInt(itVersionID, 10),
			Type:    eventType,
			Scope:   map[string]any{},
		})
		if err != nil {
			t.Fatalf("append chain event %q: %v", eventType, err)
		}
		eventTimes[stored.Seq] = stored.CreatedAt
	}
	return eventTimes
}

// nodeIndex maps the chain view node ids to their node for assertions.
func nodeIndex(nodes []chain.Node) map[string]chain.Node {
	index := make(map[string]chain.Node, len(nodes))
	for _, node := range nodes {
		index[node.ID] = node
	}
	return index
}

func edgeSet(edges []chain.Edge) map[string]bool {
	set := make(map[string]bool, len(edges))
	for _, edge := range edges {
		set[string(edge.Kind)+"|"+edge.From+"|"+edge.To] = true
	}
	return set
}

func TestProvidersChainIntegration(t *testing.T) {
	_, bundle := newChainProviders(t)
	ctx := context.Background()

	view, err := bundle.Service.Read(ctx, itVersionID, chain.ChainFilter{})
	if err != nil {
		t.Fatalf("ChainService.Read: %v", err)
	}
	if view.VersionID != strconv.FormatInt(itVersionID, 10) {
		t.Errorf("version id = %q", view.VersionID)
	}
	if view.SnapshotCursor != itEventCursor {
		t.Errorf("snapshot cursor = %d, want %d", view.SnapshotCursor, itEventCursor)
	}
	wantCounts := map[string]int{
		"requirements": 2,
		"scenarios":    2,
		"cases":        3, // 2 current + 1 historical superseded
		"waves":        2,
		"case_runs":    2,
		"issues":       1,
		"reports":      1,
		"evidence":     3,
	}
	for key, want := range wantCounts {
		if got := view.Counts[key]; got != want {
			t.Errorf("counts[%s] = %d, want %d (all counts %v)", key, got, want, view.Counts)
		}
	}

	nodes := nodeIndex(view.Nodes)
	for id, label := range map[string]string{
		"requirement:9501":  "Pay flow",
		"scenario:9601":     "Pay scenario",
		"case_version:9711": "Pay with card",
		"case_version:9713": "Superseded pay title",
		"wave:9801":         "",
		"case_run:9822":     "",
		"issue:9901":        "Amount mismatch",
		"report:9911":       "",
		"closure:9951":      "",
	} {
		node, ok := nodes[id]
		if !ok {
			t.Errorf("node %q missing from the chain view", id)
			continue
		}
		if label != "" && node.Label != label {
			t.Errorf("node %q label = %q, want %q", id, node.Label, label)
		}
	}
	if node := nodes["case_version:9713"]; node.Status != "superseded" {
		t.Errorf("historical case status = %q, want superseded", node.Status)
	}
	if node := nodes["case_run:9822"]; node.Status != "failed" {
		t.Errorf("case run status = %q, want failed", node.Status)
	}
	if node := nodes["case_run:9822"]; !slices.Equal(node.SUTIDs, []int64{itSUT2}) {
		t.Errorf("case run SUT ids = %v, want [%d]", node.SUTIDs, itSUT2)
	}
	if node := nodes["closure:9951"]; node.Status != versionapp.ClosureSummaryStatusReopened {
		t.Errorf("closure status = %q, want reopened for the incremented reopen_count", node.Status)
	}
	if node := nodes["scenario:9601"]; !node.CreatedAt.IsZero() || !node.UpdatedAt.IsZero() {
		t.Errorf("scenario timestamps = %v/%v, want both zero so event-range replay keeps it", node.CreatedAt, node.UpdatedAt)
	}
	if node := nodes["issue:9901"]; node.CaseRunID == nil || *node.CaseRunID != itRun2 ||
		node.WaveID == nil || *node.WaveID != itWave2 ||
		node.RequirementID == nil || *node.RequirementID != itRequirement1 {
		t.Errorf("issue refs were not projected: %+v", node)
	}

	// v0.2.4-B (T27 fixture follow-up): a case version no longer binds SUT/page
	// ids -- design_0004 deleted that capability and the natural-language case
	// contract replaced it. The pre-inversion assertions here checked cross-SUT
	// labels (Payments + Checkout) and page labels (Checkout page) resolved from
	// the case's version_sut_ids/page_ids; those columns no longer exist, so a
	// case node carries no sut_labels/page_labels. They are replaced by assertions
	// on the typed sut_description/covered_acs fields the chain now projects
	// (T27/U8 promoted them out of the untyped Meta map). SUT/page labels still
	// attach to the execution/delivery nodes that reference a SUT (see the filter
	// test and TestProvidersDirectReads).
	caseNode := nodes["case_version:9711"]
	if caseNode.SutDescription != "Payments checkout page and the pricing API." {
		t.Errorf("case sut_description = %q, want the seeded natural-language description", caseNode.SutDescription)
	}
	if !slices.Equal(caseNode.CoveredACs, []string{"AC-1", "AC-2"}) {
		t.Errorf("case covered_acs = %v, want [AC-1 AC-2]", caseNode.CoveredACs)
	}
	if _, found := caseNode.Meta["sut_labels"]; found {
		t.Errorf("case node still carries pre-inversion sut_labels: %v", caseNode.Meta["sut_labels"])
	}
	if _, found := caseNode.Meta["page_labels"]; found {
		t.Errorf("case node still carries pre-inversion page_labels: %v", caseNode.Meta["page_labels"])
	}

	edges := edgeSet(view.Edges)
	for _, want := range []string{
		"has_scenario|requirement:9501|scenario:9601",
		"has_case|scenario:9601|case_version:9711",
		"executed_by|case_version:9713|case_run:9822",
		"contains|wave:9802|case_run:9822",
		"raised|case_run:9822|issue:9901",
		"reports|wave:9802|report:9911",
		"closed_by|requirement:9502|closure:9951",
		"included_in|issue:9901|report:9911",
		"included_in|case_run:9822|report:9911",
	} {
		if !edges[want] {
			t.Errorf("edge %q missing from %v", want, view.Edges)
		}
	}
}

// TestProvidersChainIntegrationRequirementBudget pins the root pagination fix
// against the real version repository: one RequirementService.List page is
// capped at 100 rows, so only a provider that follows the has_more/beforeID
// cursor can hand BuildChain the MaxRequirements+1-th root and make it report
// requirement_budget instead of silently serving the page cap.
func TestProvidersChainIntegrationRequirementBudget(t *testing.T) {
	pool, bundle := newChainProviders(t)
	ctx := context.Background()

	const extraRoots = chain.DefaultMaxRequirements + 5
	if _, err := pool.Exec(ctx, `
		INSERT INTO requirement (id, version_id, current_version_id, status,
		                          created_at, updated_at)
		SELECT 10000 + g, $1::bigint, 20000 + g, 'clarified', now(), now()
		FROM generate_series(1, $2::int) AS g`,
		itVersionID, extraRoots); err != nil {
		t.Fatalf("insert bulk requirements: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO requirement_version (id, requirement_id, revision, title, priority,
		                                 content_hash, created_at)
		SELECT 20000 + g, 10000 + g, 1, 'Bulk requirement', 'P2', repeat('a', 64), now()
		FROM generate_series(1, $1::int) AS g`, extraRoots); err != nil {
		t.Fatalf("insert bulk requirement versions: %v", err)
	}

	view, err := bundle.Service.Read(ctx, itVersionID, chain.ChainFilter{})
	if err != nil {
		t.Fatalf("ChainService.Read: %v", err)
	}
	if !view.Truncated || view.Truncation.Reason != chain.TruncationReasonRequirementBudget {
		t.Fatalf("truncation = %+v, want requirement_budget", view.Truncation)
	}
	if got := view.Truncation.MaxRequirements; got != chain.DefaultMaxRequirements {
		t.Errorf("Truncation.MaxRequirements = %d, want %d", got, chain.DefaultMaxRequirements)
	}
	if got := view.Counts["requirements"]; got != chain.DefaultMaxRequirements+1 {
		t.Errorf("counts[requirements] = %d, want the paged %d roots before truncation", got, chain.DefaultMaxRequirements+1)
	}
	requirements := 0
	for _, node := range view.Nodes {
		if node.Kind == chain.NodeRequirement {
			requirements++
		}
	}
	if requirements != chain.DefaultMaxRequirements {
		t.Errorf("requirement nodes = %d, want the %d selected roots", requirements, chain.DefaultMaxRequirements)
	}
}

func TestProvidersDesignHistoryReturnsSupersededTitle(t *testing.T) {
	_, bundle := newChainProviders(t)
	ctx := context.Background()

	rows, err := bundle.DesignReader.ReadCaseVersionsByIDs(ctx, itVersionID, []int64{
		itCaseVSuperseded, itCaseV1, 123456789,
	})
	if err != nil {
		t.Fatalf("ReadCaseVersionsByIDs: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want 2 (missing ids skipped)", rows)
	}
	byID := map[int64]designapp.CaseVersionHistoryView{}
	for _, row := range rows {
		byID[row.ID] = row
	}
	superseded, ok := byID[itCaseVSuperseded]
	if !ok {
		t.Fatalf("superseded case version missing: %+v", rows)
	}
	if superseded.Title != "Superseded pay title" || superseded.Status != "superseded" || superseded.Revision != 2 {
		t.Errorf("superseded row = %+v", superseded)
	}
	if _, ok := byID[itCaseV1]; !ok {
		t.Errorf("current case version missing: %+v", rows)
	}

	// Version scoping: a foreign version id must not return the row even when
	// the caller knows its case-version id.
	foreign, err := bundle.DesignReader.ReadCaseVersionsByIDs(ctx, 999999, []int64{itCaseVSuperseded})
	if err != nil {
		t.Fatalf("foreign ReadCaseVersionsByIDs: %v", err)
	}
	if len(foreign) != 0 {
		t.Errorf("foreign version rows = %+v, want none", foreign)
	}
}

func TestProvidersDirectReads(t *testing.T) {
	_, bundle := newChainProviders(t)
	ctx := context.Background()

	requirements, err := bundle.Version.Requirements(ctx, itVersionID, chain.DefaultMaxRequirements)
	if err != nil {
		t.Fatalf("Requirements: %v", err)
	}
	if len(requirements) != 2 {
		t.Errorf("requirements = %d, want 2", len(requirements))
	}
	for _, node := range requirements {
		if node.Kind != chain.NodeRequirement || node.Label == "" {
			t.Errorf("requirement node = %+v", node)
		}
	}

	scenarios, err := bundle.Design.Scenarios(ctx, itVersionID)
	if err != nil {
		t.Fatalf("Scenarios: %v", err)
	}
	cases, err := bundle.Design.Cases(ctx, itVersionID)
	if err != nil {
		t.Fatalf("Cases: %v", err)
	}
	if len(scenarios) != 2 || len(cases) != 2 {
		t.Errorf("design nodes = %d scenarios, %d cases; want 2/2", len(scenarios), len(cases))
	}
	for _, node := range scenarios {
		if !node.CreatedAt.IsZero() || !node.UpdatedAt.IsZero() {
			t.Errorf("scenario node timestamps = %v/%v, want zero", node.CreatedAt, node.UpdatedAt)
		}
	}
	for _, node := range cases {
		if node.ScenarioID == nil || node.RequirementID == nil {
			t.Errorf("case node misses its edge endpoints: %+v", node)
		}
	}

	waves, err := bundle.Execution.Waves(ctx, itVersionID)
	if err != nil {
		t.Fatalf("Waves: %v", err)
	}
	runs, err := bundle.Execution.CaseRuns(ctx, itVersionID)
	if err != nil {
		t.Fatalf("CaseRuns: %v", err)
	}
	if len(waves) != 2 || len(runs) != 2 {
		t.Errorf("execution nodes = %d waves, %d runs; want 2/2", len(waves), len(runs))
	}
	for _, node := range runs {
		if node.WaveID == nil || node.CaseVersionID == nil {
			t.Errorf("case run node misses its edges: %+v", node)
		}
	}

	issues, err := bundle.Delivery.Issues(ctx, itVersionID)
	if err != nil {
		t.Fatalf("Issues: %v", err)
	}
	reports, err := bundle.Delivery.Reports(ctx, itVersionID)
	if err != nil {
		t.Fatalf("Reports: %v", err)
	}
	closures, err := bundle.Delivery.Closures(ctx, itVersionID)
	if err != nil {
		t.Fatalf("Closures: %v", err)
	}
	if len(issues) != 1 || len(reports) != 1 || len(closures) != 1 {
		t.Errorf("delivery nodes = %d issues, %d reports, %d closures; want 1/1/1", len(issues), len(reports), len(closures))
	}
	if reports[0].WaveID == nil || *reports[0].WaveID != itWave2 {
		t.Errorf("report node wave = %v, want %d", reports[0].WaveID, itWave2)
	}
	if refs, _ := reports[0].Meta["issue_ids"].([]int64); !slices.Contains(refs, int64(itIssueID)) {
		t.Errorf("report issue refs = %v", reports[0].Meta)
	}
	// v0.2.4-B regression guard for the kept cross-context report-ref kind. The
	// 'sut' delivery_report_ref seed (ref_id=itSUT1) must reach the report
	// node's SUTIDs through the delivery.ReportRefKindSUT switch (T11e-4 deleted the
	// local alias group; the provider now reads the delivery domain constant).
	// T14 renamed delivery's emitted kind to 'sut' and updated that switch in
	// lockstep; if a future edit drifts, the SUT ids silently vanish. The filter
	// test cannot catch this (it filters by itSUT2 while the report carries
	// itSUT1), so assert the report SUT ids directly here.
	if !slices.Equal(reports[0].SUTIDs, []int64{itSUT1}) {
		t.Errorf("report SUT ids = %v, want [%d] from the 'sut' report ref", reports[0].SUTIDs, itSUT1)
	}
	if reports[0].SUTID == nil || *reports[0].SUTID != itSUT1 {
		t.Errorf("report SUT id = %v, want %d", reports[0].SUTID, itSUT1)
	}
	// The issue's sut_id ref column (itSUT2) must reach the issue node.
	if !slices.Equal(issues[0].SUTIDs, []int64{itSUT2}) {
		t.Errorf("issue SUT ids = %v, want [%d] from the issue ref sut_id", issues[0].SUTIDs, itSUT2)
	}
	if closures[0].RequirementID == nil || *closures[0].RequirementID != itRequirement2 {
		t.Errorf("closure node requirement = %v", closures[0].RequirementID)
	}

	sutNames, err := bundle.Resource.SUTNames(ctx, itVersionID)
	if err != nil {
		t.Fatalf("SUTNames: %v", err)
	}
	if sutNames[itSUT1] != "Payments" || sutNames[itSUT2] != "Checkout" {
		t.Errorf("SUT names = %v", sutNames)
	}
	pageNames, err := bundle.Resource.PageNames(ctx, itVersionID)
	if err != nil {
		t.Fatalf("PageNames: %v", err)
	}
	if pageNames[itPage1] != "Checkout page" || pageNames[itPage2] != "Refund page" {
		t.Errorf("page names = %v", pageNames)
	}

	cursor, err := bundle.Cursor.LatestCursor(ctx, "version:"+strconv.FormatInt(itVersionID, 10))
	if err != nil {
		t.Fatalf("LatestCursor: %v", err)
	}
	if cursor != itEventCursor {
		t.Errorf("cursor = %d, want %d", cursor, itEventCursor)
	}
}

func TestProvidersEventTimeBySeq(t *testing.T) {
	_, bundle := newChainProviders(t)
	ctx := context.Background()
	channel := "version:" + strconv.FormatInt(itVersionID, 10)

	for seq := int64(1); seq <= 3; seq++ {
		at, found, err := bundle.Cursor.EventTime(ctx, channel, seq)
		if err != nil || !found {
			t.Fatalf("EventTime(%d) = %v/%v/%v", seq, at, found, err)
		}
		if !at.Equal(bundle.EventTimes[seq]) {
			t.Errorf("EventTime(%d) = %v, want %v", seq, at, bundle.EventTimes[seq])
		}
	}
	if _, found, err := bundle.Cursor.EventTime(ctx, channel, 4); err != nil || found {
		t.Errorf("EventTime(4) = found %v err %v, want not found", found, err)
	}
}

func TestProvidersChainIntegrationEventRange(t *testing.T) {
	pool, bundle := newChainProviders(t)
	ctx := context.Background()
	eventTimes := bundle.EventTimes
	if len(eventTimes) != 3 {
		t.Fatalf("event times = %v, want three seqs", eventTimes)
	}

	// Five requirements around the event window: one before the first event,
	// one at each event boundary, and one after the last event. The version
	// provider projects UpdatedAt into the chain node CreatedAt, so updated_at
	// is the timestamp the range filter sees.
	requirements := []struct {
		id          int64
		versionID   int64
		at          time.Time
		contentHash string
	}{
		{id: 9961, versionID: 9971, at: eventTimes[1].Add(-time.Hour), contentHash: itHash("7")},
		{id: 9962, versionID: 9972, at: eventTimes[1], contentHash: itHash("8")},
		{id: 9963, versionID: 9973, at: eventTimes[2], contentHash: itHash("9")},
		{id: 9964, versionID: 9974, at: eventTimes[3], contentHash: itHash("a")},
		{id: 9965, versionID: 9975, at: eventTimes[3].Add(time.Hour), contentHash: itHash("b")},
	}
	for _, requirement := range requirements {
		if _, err := pool.Exec(ctx, `
			INSERT INTO requirement (id, version_id, current_version_id, status,
			                          created_at, updated_at)
			VALUES ($1, $2, $3, 'clarified', $4, $4)`,
			requirement.id, itVersionID, requirement.versionID, requirement.at); err != nil {
			t.Fatalf("insert range requirement %d: %v", requirement.id, err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO requirement_version (id, requirement_id, revision, title, priority,
			                                 content_hash, created_at)
			VALUES ($1, $2, 1, 'Range requirement', 'P2', $3, $4)`,
			requirement.versionID, requirement.id, requirement.contentHash, requirement.at); err != nil {
			t.Fatalf("insert range requirement version %d: %v", requirement.versionID, err)
		}
	}

	from, to := int64(1), int64(3)
	view, err := bundle.Service.Read(ctx, itVersionID, chain.ChainFilter{EventFrom: &from, EventTo: &to})
	if err != nil {
		t.Fatalf("event-range Read: %v", err)
	}
	if view.Replay.Mode != chain.ReplayModeRange || !view.Replay.Approximate {
		t.Errorf("replay = %+v, want range/approximate", view.Replay)
	}
	if got := view.Counts["requirements"]; got != 3 {
		t.Errorf("range requirements = %d, want the three boundary requirements", got)
	}
	nodes := nodeIndex(view.Nodes)
	for _, id := range []string{"requirement:9962", "requirement:9963", "requirement:9964"} {
		if _, ok := nodes[id]; !ok {
			t.Errorf("boundary requirement %q missing from the range graph", id)
		}
	}
	for _, id := range []string{"requirement:9961", "requirement:9965", "requirement:9501"} {
		if _, ok := nodes[id]; ok {
			t.Errorf("node %q outside the event range survived: %v", id, view.Nodes)
		}
	}
	// Scenario rows do not expose timestamps. The provider keeps CreatedAt /
	// UpdatedAt zero so this filter retains them; stamping them with the
	// request-time graph snapshot would drop every scenario from the range.
	for _, id := range []string{"scenario:9601", "scenario:9602"} {
		node, ok := nodes[id]
		if !ok {
			t.Errorf("scenario %q missing from the event range: %v", id, view.Nodes)
			continue
		}
		if !node.CreatedAt.IsZero() || !node.UpdatedAt.IsZero() {
			t.Errorf("scenario %q timestamps = %v/%v, want zero inside the range replay", id, node.CreatedAt, node.UpdatedAt)
		}
	}
	if got := view.Counts["scenarios"]; got != 2 {
		t.Errorf("range scenarios = %d, want 2", got)
	}
}

func TestProvidersChainIntegrationFilters(t *testing.T) {
	_, bundle := newChainProviders(t)
	ctx := context.Background()

	sutID := int64(itSUT2)
	view, err := bundle.Service.Read(ctx, itVersionID, chain.ChainFilter{SUTID: &sutID})
	if err != nil {
		t.Fatalf("filtered Read: %v", err)
	}
	for _, node := range view.Nodes {
		matches := node.SUTID != nil && *node.SUTID == sutID
		for _, id := range node.SUTIDs {
			matches = matches || id == sutID
		}
		if !matches {
			t.Errorf("node %q does not match sut_id=%d", node.ID, sutID)
		}
	}
	nodes := nodeIndex(view.Nodes)
	// v0.2.4-B (T27 fixture follow-up): a SUT-scoped filter keeps the nodes that
	// actually reference the SUT -- the case run that executed against it
	// (case_run_sut_context.sut_id = itSUT2) and the issue raised on it
	// (delivery_issue_ref.sut_id = itSUT2). Case versions no longer bind SUT ids
	// (design_0004 deleted that capability), so the pre-inversion assertion that
	// case_version:9712 survives a SUT filter is replaced: no case_version node is
	// SUT-scoped any more, and the run/issue nodes that carry itSUT2 are kept.
	if _, ok := nodes["case_run:9822"]; !ok {
		t.Errorf("SUT-scoped filter lost the run node that executed against itSUT2: %v", view.Nodes)
	}
	if _, ok := nodes["issue:9901"]; !ok {
		t.Errorf("SUT-scoped filter lost the issue node referencing itSUT2: %v", view.Nodes)
	}
	for id := range nodes {
		if strings.HasPrefix(id, "case_version:") {
			t.Errorf("case_version node %q survived a SUT filter; case versions no longer bind SUT ids", id)
		}
	}
}
