//go:build integration

package designpg_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	designpg "github.com/EdouardRichard/EzTest/backend/internal/design/infrastructure/postgres"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

// capturePublisher records the design events of one test.
type capturePublisher struct {
	events []eventingapp.Event
}

func (p *capturePublisher) Publish(_ context.Context, event eventingapp.Event) error {
	p.events = append(p.events, event)
	return nil
}

func (p *capturePublisher) count(eventType string) int {
	matches := 0
	for _, event := range p.events {
		if event.Type == eventType {
			matches++
		}
	}
	return matches
}

// seedDiscoveryProvenance creates the executed case version + observation chain
// the additive revision is derived from. evidenceRows controls the R-EX-6 low
// confidence floor.
func seedDiscoveryProvenance(t *testing.T, pool *pgxpool.Pool, observationID, waveID, caseRunID, executedCaseVersionID, resourceID, evidenceRows int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO execution_intent (id, version_id, case_version_id, sut_ids,
			environment_fingerprint, dedupe_key)
		VALUES ($1, 1001, $2, '["9001"]'::jsonb, repeat('a', 64), $3)
	`, caseRunID-100, executedCaseVersionID, "intent-"+strings.Repeat("0", 1)); err != nil {
		t.Fatalf("seed execution intent: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO run_wave (id, version_id, status) VALUES ($1, 1001, 'done')
	`, waveID); err != nil {
		t.Fatalf("seed run wave: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO case_run (id, wave_id, intent_id, case_version_id, status)
		VALUES ($1, $2, $3, $4, 'passed')
	`, caseRunID, waveID, caseRunID-100, executedCaseVersionID); err != nil {
		t.Fatalf("seed case run: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO execution_discovery_observation (
			id, version_id, wave_id, case_run_id, sut_id, environment_id,
			kind, method, host, resource_template, decision, resource_id
		) VALUES ($1, 1001, $2, $3, 9001, 9001, 'page', '', 'orders.example.test',
		          '/orders/{id}', 'approved', $4)
	`, observationID, waveID, caseRunID, resourceID); err != nil {
		t.Fatalf("seed discovery observation: %v", err)
	}
	for index := int64(0); index < evidenceRows; index++ {
		if _, err := pool.Exec(ctx, `
			INSERT INTO evidence (id, case_run_id, kind, content_ref_id, sha256, size_bytes)
			VALUES ($1, $2, 'screenshot', $1, repeat('b', 64), 10)
		`, observationID*10+index, caseRunID); err != nil {
			t.Fatalf("seed evidence: %v", err)
		}
	}
}

func seedRequirementSource(t *testing.T, pool *pgxpool.Pool, caseVersionID int64) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO case_source (id, case_version_id, kind, ref_id, path)
		VALUES ($1, $2, 'requirement', '3002', 'requirement')
	`, caseVersionID+900000, caseVersionID); err != nil {
		t.Fatalf("seed case source: %v", err)
	}
}

// passJudgmentGate satisfies the confirmation judgment gate for this suite,
// whose subject is the discovery revision SQL, not the judge review. It answers
// "pass" and refuses the fact reads a pass state never asks for. The gate's own
// routing is pinned in design/application against the real judgment service.
type passJudgmentGate struct{}

func (passJudgmentGate) ReviewStateFor(context.Context, int64, int64, int64) (design.ReviewState, error) {
	return design.ReviewStatePass, nil
}

func (passJudgmentGate) LatestOverride(context.Context, int64, int64) (*design.Override, error) {
	panic("pass judgment gate must not be asked for an override")
}

func (passJudgmentGate) LatestJudgment(context.Context, int64) (*design.Judgment, error) {
	panic("pass judgment gate must not be asked for a judgment fact")
}

func newDiscoveryRevisionService(t *testing.T, pool *pgxpool.Pool) (*designapp.GraphService, *capturePublisher) {
	t.Helper()
	generator, err := id.NewSnowflake(1)
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	publisher := &capturePublisher{}
	service := designapp.NewGraphService(designpg.New(pool), generator, clock.System{}, nil, publisher, nil).
		SetJudgmentGate(passJudgmentGate{})
	service.SetDiscoveryRevisionStore(designpg.NewRevisionStore(pool))
	return service, publisher
}

func seedDiscoveryBase(t *testing.T, pool *pgxpool.Pool, evidenceRows int64) {
	t.Helper()
	seedVersion(t, pool, 1001, false)
	// The approved catalog page the observation is about (Critic ①). Under
	// v0.2.4-B this is catalog state only: there is no version_sut binding and no
	// version-scope predicate left to satisfy.
	seedApprovedPage(t, pool, 1001, 9001, 6001)
	seedRequirement(t, pool, 1001, 3001, 3002, "clarified", "订单详情", 1)
	// The executed case version is the provenance anchor of the observation. It
	// also supplies the covered_acs the additive draft inherits (§5.2), so it
	// must carry a non-empty set.
	seedScenario(t, pool, 1001, 3001, 3002, 5001, "已有场景", 0)
	seedCase(t, pool, 1001, 3001, 3002, 5001, 7001, 7002, 1, design.CaseStatusConfirmed, "已有用例")
	seedRequirementSource(t, pool, 7002)
	seedDiscoveryProvenance(t, pool, 8101, 9201, 9301, 7002, 6001, evidenceRows)
}

// TestDiscoveryRevisionInheritsContractFromProvenanceIntegration pins the
// v0.2.4-B discovery contract end to end against the real schema: the additive
// DRAFT (R-E2r: judgment_pending, never auto-confirmed) takes its sut_description
// from the requirement and its covered_acs from the executed case version, states
// an explicit "无" precondition, and keeps the discovered page as a provenance
// source (which is how the rollback path still reports a real resource_id).
func TestDiscoveryRevisionInheritsContractFromProvenanceIntegration(t *testing.T) {
	pool := testutil.NewDB(t)
	seedDiscoveryBase(t, pool, 2)
	service, _ := newDiscoveryRevisionService(t, pool)
	ctx := context.Background()

	view, err := service.OnResourceConfirmed(ctx, 1001, 6001, 8101, 3001)
	if err != nil {
		t.Fatalf("OnResourceConfirmed: %v", err)
	}
	if view.Status != design.CaseStatusDraft || view.LowConfidenceReason != "judgment_pending" {
		t.Fatalf("view = %+v, want draft judgment_pending", view)
	}
	if view.ResourceID != 6001 {
		t.Fatalf("resource id = %d, want 6001 recovered from the page source", view.ResourceID)
	}

	version, err := designpg.New(pool).ReadCaseVersionsByIDs(ctx, 1001, []int64{view.CaseVersionID})
	if err != nil {
		t.Fatalf("ReadCaseVersionsByIDs: %v", err)
	}
	if len(version) != 1 || version[0].CaseID != view.CaseID {
		t.Fatalf("history = %+v, want the draft revision of case %d", version, view.CaseID)
	}
	data, err := designpg.New(pool).ReadCase(ctx, 1001, view.CaseID, &view.CaseVersionID)
	if err != nil {
		t.Fatalf("ReadCase: %v", err)
	}
	selected := data.Selected
	if selected.SutDescription != seedSutDescription {
		t.Fatalf("sut_description = %q, want the requirement's %q", selected.SutDescription, seedSutDescription)
	}
	if len(selected.CoveredACs) != 1 || selected.CoveredACs[0] != "AC-1" {
		t.Fatalf("covered_acs = %v, want the executed version's [AC-1]", selected.CoveredACs)
	}
	if selected.Preconditions != "无" {
		t.Fatalf("preconditions = %q, want the explicit 无", selected.Preconditions)
	}
	if len(selected.Steps) != 1 || selected.Steps[0].TargetDescription == "" {
		t.Fatalf("steps = %+v, want one step with a target_description", selected.Steps)
	}
	var pageSource bool
	for _, source := range selected.Sources {
		if source.Kind == design.SourcePage && source.RefID == "6001" && source.Path == "discovery_delta" {
			pageSource = true
		}
	}
	if !pageSource {
		t.Fatalf("the discovered page must remain a provenance source: %+v", selected.Sources)
	}
}

// TestDiscoveryRevisionFailsClosedWithoutInheritableContractIntegration pins the
// gate that replaced Critic ⑥: when neither the requirement nor the executed case
// version can supply the §5.2 contract elements, the approval stays a
// low-confidence draft and writes nothing, rather than inventing coverage.
func TestDiscoveryRevisionFailsClosedWithoutInheritableContractIntegration(t *testing.T) {
	t.Run("missing_covered_acs", func(t *testing.T) {
		pool := testutil.NewDB(t)
		seedDiscoveryBase(t, pool, 2)
		// The executed case version predates the contract: no covered_acs to
		// inherit.
		if _, err := pool.Exec(context.Background(),
			`UPDATE case_version SET covered_acs = '[]'::jsonb WHERE id = 7002`); err != nil {
			t.Fatalf("clear covered_acs: %v", err)
		}
		service, publisher := newDiscoveryRevisionService(t, pool)
		view, err := service.OnResourceConfirmed(context.Background(), 1001, 6001, 8101, 3001)
		if err != nil {
			t.Fatalf("OnResourceConfirmed: %v", err)
		}
		if view.Status != design.CaseStatusDraft || view.LowConfidenceReason != "missing_covered_acs" {
			t.Fatalf("view = %+v, want draft missing_covered_acs", view)
		}
		if view.CaseID != 0 || view.CaseVersionID != 0 {
			t.Fatalf("a case was written for an uncoverable draft: %+v", view)
		}
		if publisher.count("case.revision.auto_confirmed") != 0 {
			t.Fatal("a fail-closed draft emitted an auto-confirm event")
		}
	})
	t.Run("missing_sut_description", func(t *testing.T) {
		pool := testutil.NewDB(t)
		seedDiscoveryBase(t, pool, 2)
		if _, err := pool.Exec(context.Background(),
			`UPDATE requirement_version SET sut_description = '' WHERE id = 3002`); err != nil {
			t.Fatalf("clear sut_description: %v", err)
		}
		service, _ := newDiscoveryRevisionService(t, pool)
		view, err := service.OnResourceConfirmed(context.Background(), 1001, 6001, 8101, 3001)
		if err != nil {
			t.Fatalf("OnResourceConfirmed: %v", err)
		}
		if view.Status != design.CaseStatusDraft || view.LowConfidenceReason != "missing_sut_description" {
			t.Fatalf("view = %+v, want draft missing_sut_description", view)
		}
		if view.CaseID != 0 || view.CaseVersionID != 0 {
			t.Fatalf("a case was written for an undescribable draft: %+v", view)
		}
	})
}

// seedLegacyConfirmedDiscoveryRevision models a pre-E system auto-confirmed
// discovery revision (confirmed + trigger=discovery_delta + auto_confirmed_by=
// system + the observation provenance + the discovered page source). After R-E2r
// OnResourceConfirmed never produces this shape, so the rollback test seeds the
// legacy history directly rather than through a code path that no longer creates
// it. Rollback is legacy-only: it exists to undo exactly this historical shape.
func seedLegacyConfirmedDiscoveryRevision(t *testing.T, pool *pgxpool.Pool, caseID, caseVersionID, observationID int64) {
	t.Helper()
	seedCase(t, pool, 1001, 3001, 3002, 5001, caseID, caseVersionID, 1, design.CaseStatusConfirmed, "legacy discovery")
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		UPDATE case_version
		SET trigger = 'discovery_delta', auto_confirmed_by = 'system', source_observation_id = $2
		WHERE id = $1
	`, caseVersionID, observationID); err != nil {
		t.Fatalf("seed legacy discovery revision: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO case_source (id, case_version_id, kind, ref_id, path)
		VALUES ($1, $2, 'page', '6001', 'discovery_delta')
	`, caseVersionID+900001, caseVersionID); err != nil {
		t.Fatalf("seed legacy discovery page source: %v", err)
	}
}

// TestDiscoveryRevisionDraftAndLegacyRollbackIntegration pins the R-E2r contract
// against the real schema: a resource approval whose five predicates pass yields
// a DRAFT (judgment_pending) — no confirmation row, no system actor, no
// auto-confirm event, and not executable until a human confirms it — and Rollback
// (legacy-only) undoes a pre-E system auto-confirm by superseding it and inserting
// a DRAFT replacement, without bypassing Confirm.
func TestDiscoveryRevisionDraftAndLegacyRollbackIntegration(t *testing.T) {
	pool := testutil.NewDB(t)
	seedDiscoveryBase(t, pool, 2)
	service, publisher := newDiscoveryRevisionService(t, pool)
	ctx := context.Background()

	beforeWaveIDs := currentWaveCaseRunIDs(t, pool)

	view, err := service.OnResourceConfirmed(ctx, 1001, 6001, 8101, 3001)
	if err != nil {
		t.Fatalf("OnResourceConfirmed: %v", err)
	}
	if view.Status != design.CaseStatusDraft || view.Trigger != design.CaseTriggerDiscoveryDelta || view.LowConfidenceReason != "judgment_pending" {
		t.Fatalf("view = %+v, want draft discovery_delta judgment_pending", view)
	}
	if after := currentWaveCaseRunIDs(t, pool); strings.Join(after, ",") != strings.Join(beforeWaveIDs, ",") {
		t.Fatalf("current wave case_run ids changed: %v -> %v", beforeWaveIDs, after)
	}

	var status, trigger, autoConfirmedBy, observationID string
	if err := pool.QueryRow(ctx, `
		SELECT status, trigger, COALESCE(auto_confirmed_by, ''), COALESCE(source_observation_id, 0)::text
		FROM case_version WHERE id = $1
	`, view.CaseVersionID).Scan(&status, &trigger, &autoConfirmedBy, &observationID); err != nil {
		t.Fatalf("read discovery draft: %v", err)
	}
	if status != "draft" || trigger != "discovery_delta" || autoConfirmedBy != "" || observationID != "8101" {
		t.Fatalf("revision = status=%q trigger=%q auto=%q observation=%q, want draft/discovery_delta/no system actor", status, trigger, autoConfirmedBy, observationID)
	}
	// Confirm is the only terminal entry: the discovery draft has no confirmation row.
	var confirmations int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM case_confirmation WHERE case_version_id = $1
	`, view.CaseVersionID).Scan(&confirmations); err != nil {
		t.Fatalf("count discovery confirmations: %v", err)
	}
	if confirmations != 0 {
		t.Fatalf("discovery draft wrote %d confirmation rows, want 0", confirmations)
	}
	if publisher.count("case.revision.auto_confirmed") != 0 {
		t.Fatalf("auto-confirm events = %d, want 0", publisher.count("case.revision.auto_confirmed"))
	}

	// A draft is NOT executable: only a human-confirmed case enters a later wave.
	executables, err := designpg.New(pool).ListExecutableCases(ctx, 1001)
	if err != nil {
		t.Fatalf("ListExecutableCases: %v", err)
	}
	for _, executable := range executables {
		if executable.Version.ID == view.CaseVersionID {
			t.Fatalf("discovery draft %d is executable before human confirm: %+v", view.CaseVersionID, executables)
		}
	}

	// Idempotent repeat: no second revision/event.
	again, err := service.OnResourceConfirmed(ctx, 1001, 6001, 8101, 3001)
	if err != nil || again.CaseVersionID != view.CaseVersionID {
		t.Fatalf("repeat = %+v err=%v", again, err)
	}
	if publisher.count("case.revision.auto_confirmed") != 0 {
		t.Fatalf("repeat emitted an auto-confirm event")
	}

	// Rollback is legacy-only after R-E2r: the discovery draft above is not
	// rollbackable, so seed a pre-E system auto-confirm and roll THAT back.
	seedLegacyConfirmedDiscoveryRevision(t, pool, 7101, 7102, 8101)
	replacement, err := service.Rollback(ctx, 1001, 7102, "探索误判", 9)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if replacement.RollbackOf != 7102 || replacement.Status != design.CaseStatusDraft || replacement.Trigger != design.CaseTriggerRollback {
		t.Fatalf("replacement = %+v, want draft rollback of 7102", replacement)
	}
	if got := caseVersionStatus(t, pool, 7102); got != "superseded" {
		t.Fatalf("original status = %q, want superseded", got)
	}
	var rollbackTrigger, rollbackOf, rollbackStatus, rollbackAuto string
	if err := pool.QueryRow(ctx, `
		SELECT trigger, COALESCE(rollback_of, 0)::text, status, COALESCE(auto_confirmed_by, '')
		FROM case_version WHERE id = $1
	`, replacement.CaseVersionID).Scan(&rollbackTrigger, &rollbackOf, &rollbackStatus, &rollbackAuto); err != nil {
		t.Fatalf("read rollback replacement: %v", err)
	}
	if rollbackTrigger != "rollback" || rollbackOf != strconv.FormatInt(7102, 10) || rollbackStatus != "draft" || rollbackAuto != "" {
		t.Fatalf("replacement trigger=%q rollback_of=%q status=%q auto=%q, want rollback/draft/no system actor", rollbackTrigger, rollbackOf, rollbackStatus, rollbackAuto)
	}
	// No confirmation row for the draft replacement: Confirm is the only terminal entry.
	var replacementConfirmations int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM case_confirmation WHERE case_version_id = $1
	`, replacement.CaseVersionID).Scan(&replacementConfirmations); err != nil {
		t.Fatalf("count replacement confirmations: %v", err)
	}
	if replacementConfirmations != 0 {
		t.Fatalf("rollback bypassed Confirm with %d confirmation rows", replacementConfirmations)
	}
	if replacement.ResourceID != 6001 {
		t.Fatalf("rollback resource_id = %d, want 6001 recovered from the page source", replacement.ResourceID)
	}
	// The rollback still notifies via the legacy EV-12 event; the discovery draft
	// emitted none, so exactly one exists (the rollback's). The event name/confidence
	// is a legacy published contract retained pending the T17 event-contract update.
	if publisher.count("case.revision.auto_confirmed") != 1 {
		t.Fatalf("rollback event missing: %d", publisher.count("case.revision.auto_confirmed"))
	}
	repeat, err := service.Rollback(ctx, 1001, 7102, "探索误判", 9)
	if err != nil || repeat.CaseVersionID != replacement.CaseVersionID {
		t.Fatalf("rollback not idempotent: repeat=%+v err=%v", repeat, err)
	}
}

func TestDiscoveryRevisionLowConfidenceStaysDraftIntegration(t *testing.T) {
	pool := testutil.NewDB(t)
	seedDiscoveryBase(t, pool, 1)
	service, publisher := newDiscoveryRevisionService(t, pool)

	view, err := service.OnResourceConfirmed(context.Background(), 1001, 6001, 8101, 3001)
	if err != nil {
		t.Fatalf("OnResourceConfirmed: %v", err)
	}
	if view.Status != design.CaseStatusDraft || view.LowConfidenceReason != "insufficient_evidence" {
		t.Fatalf("view = %+v, want draft insufficient_evidence", view)
	}
	if got := caseVersionStatus(t, pool, view.CaseVersionID); got != "draft" {
		t.Fatalf("stored status = %q, want draft", got)
	}
	if publisher.count("case.revision.auto_confirmed") != 0 {
		t.Fatalf("low confidence emitted an auto-confirm event")
	}
}

// TestDiscoveryRevisionDuplicateTargetLeavesNoNewCaseIntegration exercises the
// real duplicate-target path: a second observation of the same approved target,
// after the first additive revision is already CONFIRMED, must stay a low
// confidence draft and must not create or confirm another case. The unit fake
// stubs the duplicate lookup, so only this SQL path proves the Task 106 freeze
// (a confirmed same-title case) maps onto duplicate_target. R-E2r: the first
// revision is confirmed by a HUMAN (this suite's judgment gate passes), never
// auto-confirmed — a discovery draft awaits judge review then human confirm.
func TestDiscoveryRevisionDuplicateTargetLeavesNoNewCaseIntegration(t *testing.T) {
	pool := testutil.NewDB(t)
	seedDiscoveryBase(t, pool, 2)
	service, publisher := newDiscoveryRevisionService(t, pool)
	ctx := context.Background()

	first, err := service.OnResourceConfirmed(ctx, 1001, 6001, 8101, 3001)
	if err != nil {
		t.Fatalf("first OnResourceConfirmed: %v", err)
	}
	if first.Status != design.CaseStatusDraft || first.LowConfidenceReason != "judgment_pending" {
		t.Fatalf("first = %+v, want draft judgment_pending", first)
	}
	// A human confirms the discovery draft — the only route to a confirmed
	// discovery revision after R-E2r abolished auto-confirm.
	if _, err := service.Confirm(ctx, 1001, designapp.ConfirmInput{
		Scope: design.ConfirmScopeCase, TargetID: first.CaseID,
	}, 42); err != nil {
		t.Fatalf("human Confirm of discovery draft: %v", err)
	}
	if got := caseVersionStatus(t, pool, first.CaseVersionID); got != "confirmed" {
		t.Fatalf("first revision status = %q, want confirmed after human confirm", got)
	}
	var casesBefore int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM case_entity ce
		JOIN case_scenario s ON s.id = ce.scenario_id
		WHERE s.version_id = 1001 AND s.requirement_id = 3001
	`).Scan(&casesBefore); err != nil {
		t.Fatalf("count cases before: %v", err)
	}

	// A second execution observation of the same approved target. Only the
	// resource_template differs, to satisfy the observation unique key.
	seedDiscoveryObservation(t, pool, 8102, 9201, 9301, 6001, "/orders/{id}/observed-again")

	second, err := service.OnResourceConfirmed(ctx, 1001, 6001, 8102, 3001)
	if err != nil {
		t.Fatalf("second OnResourceConfirmed: %v", err)
	}
	if second.Status != design.CaseStatusDraft || second.LowConfidenceReason != "duplicate_target" {
		t.Fatalf("second = %+v, want draft duplicate_target", second)
	}
	if second.CaseID != 0 || second.CaseVersionID != 0 {
		t.Fatalf("duplicate target attached a case: %+v", second)
	}

	var casesAfter int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM case_entity ce
		JOIN case_scenario s ON s.id = ce.scenario_id
		WHERE s.version_id = 1001 AND s.requirement_id = 3001
	`).Scan(&casesAfter); err != nil {
		t.Fatalf("count cases after: %v", err)
	}
	if casesAfter != casesBefore {
		t.Fatalf("duplicate target created a case: %d -> %d", casesBefore, casesAfter)
	}
	var duplicateVersions int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM case_version WHERE source_observation_id = 8102
	`).Scan(&duplicateVersions); err != nil {
		t.Fatalf("count duplicate revisions: %v", err)
	}
	if duplicateVersions != 0 {
		t.Fatalf("duplicate target wrote %d case versions", duplicateVersions)
	}
	if got := caseVersionStatus(t, pool, first.CaseVersionID); got != "confirmed" {
		t.Fatalf("first revision status = %q, want confirmed", got)
	}
	// Discovery never auto-confirms (R-E2r); the human Confirm emits
	// design.case_confirmed, not case.revision.auto_confirmed.
	if publisher.count("case.revision.auto_confirmed") != 0 {
		t.Fatalf("events = %d, want 0 (no auto-confirm after R-E2r)", publisher.count("case.revision.auto_confirmed"))
	}
}

// seedDiscoveryObservation inserts one more observation of an already executed
// case_run. resource_template must be unique per version/SUT/kind.
func seedDiscoveryObservation(t *testing.T, pool *pgxpool.Pool, observationID, waveID, caseRunID, resourceID int64, resourceTemplate string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO execution_discovery_observation (
			id, version_id, wave_id, case_run_id, sut_id, environment_id,
			kind, method, host, resource_template, decision, resource_id
		) VALUES ($1, 1001, $2, $3, 9001, 9001, 'page', '', 'orders.example.test',
		          $4, 'approved', $5)
	`, observationID, waveID, caseRunID, resourceTemplate, resourceID); err != nil {
		t.Fatalf("seed discovery observation: %v", err)
	}
}

func currentWaveCaseRunIDs(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT id::text FROM case_run WHERE wave_id = 9201 ORDER BY id
	`)
	if err != nil {
		t.Fatalf("read current wave case runs: %v", err)
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan case run id: %v", err)
		}
		ids = append(ids, id)
	}
	return ids
}

func caseVersionStatus(t *testing.T, pool *pgxpool.Pool, caseVersionID int64) string {
	t.Helper()
	var status string
	if err := pool.QueryRow(context.Background(),
		`SELECT status FROM case_version WHERE id = $1`, caseVersionID).Scan(&status); err != nil {
		t.Fatalf("read case version status: %v", err)
	}
	return status
}
