package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// reviewVersionLifecycle wraps the shared fixture with injectable failures so the
// review and baseline tests can prove the compensation path without a database.
type reviewVersionLifecycle struct {
	f          *runFixture
	enterErr   error
	rejectErr  error
	approveErr error

	// sawBaseline and sawReviewed record the cross-context write order of spec
	// §6.4 at the moment ApproveBaseline is called: baseline + reviewed nodes
	// must already be committed, the version transition comes last.
	sawBaseline bool
	sawReviewed bool
}

func (v *reviewVersionLifecycle) GetVersion(_ context.Context, versionID int64) (application.VersionSnapshot, error) {
	if versionID != v.f.version.ID {
		return application.VersionSnapshot{}, domain.NewNotFound("version", versionID)
	}
	return v.f.version, nil
}

func (v *reviewVersionLifecycle) EnterReview(_ context.Context, versionID, _ int64) (application.VersionSnapshot, error) {
	if v.enterErr != nil {
		return application.VersionSnapshot{}, v.enterErr
	}
	if versionID != v.f.version.ID {
		return application.VersionSnapshot{}, domain.NewNotFound("version", versionID)
	}
	switch v.f.version.Status {
	case "designing":
		v.f.version.Status = "reviewing"
	case "reviewing":
		return v.f.version, nil
	default:
		return application.VersionSnapshot{}, domain.NewStateConflict("version_state_conflict",
			"version is not designing")
	}
	return v.f.version, nil
}

func (v *reviewVersionLifecycle) RejectReview(_ context.Context, versionID, _ int64) (application.VersionSnapshot, error) {
	if v.rejectErr != nil {
		return application.VersionSnapshot{}, v.rejectErr
	}
	if versionID != v.f.version.ID {
		return application.VersionSnapshot{}, domain.NewNotFound("version", versionID)
	}
	if v.f.version.Status != "reviewing" {
		return application.VersionSnapshot{}, domain.NewStateConflict("version_state_conflict",
			"version is not reviewing")
	}
	v.f.version.Status = "designing"
	return v.f.version, nil
}

func (v *reviewVersionLifecycle) ApproveBaseline(_ context.Context, versionID, _ int64) (application.VersionSnapshot, error) {
	v.sawBaseline = v.f.baseline != nil
	v.sawReviewed = len(v.f.nodes) > 0
	for _, node := range v.f.nodes {
		if node.Status != domain.NodeStatusReviewed {
			v.sawReviewed = false
			break
		}
	}
	if v.approveErr != nil {
		return application.VersionSnapshot{}, v.approveErr
	}
	if versionID != v.f.version.ID {
		return application.VersionSnapshot{}, domain.NewNotFound("version", versionID)
	}
	if v.f.version.Status != "reviewing" {
		return application.VersionSnapshot{}, domain.NewStateConflict("version_state_conflict",
			"version is not reviewing")
	}
	v.f.version.Status = "executing"
	return v.f.version, nil
}

// reviewNodeRepo mirrors the PostgreSQL status CAS: freezing bumps the node
// revision so the compensation test can prove the rollback restores both status
// and revision.
type reviewNodeRepo struct {
	runNodeRepo
}

func (r reviewNodeRepo) UpdateStatusByVersion(_ context.Context, versionID int64, from, to string) (int, error) {
	count := 0
	for id, node := range r.f.nodes {
		if node.VersionID != versionID || node.Status != from {
			continue
		}
		node.Status = to
		node.Revision++
		node.UpdatedAt = r.f.now
		r.f.nodes[id] = node
		count++
	}
	return count, nil
}

// reviewGraph routes InTx through the shared fixture but binds the transaction to
// reviewNodeRepo so the freeze status update bumps revisions like the adapter.
type reviewGraph struct {
	f *runFixture
}

func (g reviewGraph) InTx(ctx context.Context, fn func(application.GraphTx) error) error {
	return g.f.InTx(ctx, func(application.GraphTx) error {
		return fn(reviewTx{g.f})
	})
}

type reviewTx struct {
	f *runFixture
}

func (tx reviewTx) Nodes() application.NodeRepository {
	return reviewNodeRepo{runNodeRepo{tx.f}}
}
func (tx reviewTx) Steps() application.StepRepository           { return runStepRepo{tx.f} }
func (tx reviewTx) Assertions() application.AssertionRepository { return runAssertionRepo{tx.f} }
func (tx reviewTx) Sources() application.SourceRepository       { return runSourceRepo{tx.f} }
func (tx reviewTx) Reviews() application.ReviewRepository       { return runReviewRepo{tx.f} }
func (tx reviewTx) Findings() application.FindingRepository     { return runFindingRepo{tx.f} }
func (tx reviewTx) Baselines() application.BaselineRepository   { return runBaselineRepo{tx.f} }
func (tx reviewTx) Runs() application.RunRepository             { return runRunRepo{tx.f} }

// reviewRunStarter records the delegated StartRunCommand and returns a queued run.
type reviewRunStarter struct {
	calls []application.StartRunCommand
}

func (s *reviewRunStarter) Start(_ context.Context, versionID, actor int64, cmd application.StartRunCommand) (domain.CaseDesignRun, error) {
	s.calls = append(s.calls, cmd)
	return domain.CaseDesignRun{
		ID:        7777,
		VersionID: versionID,
		Kind:      cmd.Kind,
		Status:    domain.RunQueued,
		CreatedBy: actor,
	}, nil
}

// baselineServiceFor wires the production baseline service to the shared fixture.
func baselineServiceFor(f *runFixture, lifecycle *reviewVersionLifecycle) *application.BaselineService {
	if lifecycle == nil {
		lifecycle = &reviewVersionLifecycle{f: f}
	}
	return application.NewBaselineService(application.BaselineDeps{
		Graph:      reviewGraph{f},
		Nodes:      reviewNodeRepo{runNodeRepo{f}},
		Steps:      runStepRepo{f},
		Assertions: runAssertionRepo{f},
		Sources:    runSourceRepo{f},
		Reviews:    runReviewRepo{f},
		Baselines:  runBaselineRepo{f},
		Versions:   lifecycle,
		IDs:        runIDGenerator{f},
		Clock:      runClock{f},
	})
}

// reviewServiceFor wires the production review service to the shared fixture.
func reviewServiceFor(f *runFixture, lifecycle *reviewVersionLifecycle, runs application.ReviewRunStarter) *application.ReviewService {
	if lifecycle == nil {
		lifecycle = &reviewVersionLifecycle{f: f}
	}
	return application.NewReviewService(application.ReviewDeps{
		Runs:      runs,
		Reviews:   runReviewRepo{f},
		Findings:  runFindingRepo{f},
		Baselines: runBaselineRepo{f},
		Versions:  lifecycle,
		Events:    runEventSink{f},
		Baseline:  baselineServiceFor(f, lifecycle),
		IDs:       runIDGenerator{f},
		Clock:     runClock{f},
	})
}

// seedReviewGraph stores one scenario, one complete case and its requirement
// source so the canonical hash and the node/case counts are non-trivial.
func seedReviewGraph(f *runFixture) domain.CaseNode {
	scenarioID := int64(1001)
	caseID := int64(1002)
	stepID := int64(2001)
	requirementID := int64(7001)
	parent := scenarioID

	f.nodes[scenarioID] = domain.CaseNode{
		ID:        scenarioID,
		VersionID: runTestVersionID,
		Kind:      domain.NodeKindScenario,
		Title:     "checkout",
		Priority:  domain.PriorityP2,
		Source:    domain.NodeSourceUserInput,
		Status:    domain.NodeStatusDraft,
		SortOrder: 1,
		Revision:  1,
		CreatedAt: f.now,
		UpdatedAt: f.now,
	}
	savedCase := domain.CaseNode{
		ID:        caseID,
		VersionID: runTestVersionID,
		ParentID:  &parent,
		Kind:      domain.NodeKindCase,
		Title:     "pay order",
		Priority:  domain.PriorityP1,
		Source:    domain.NodeSourceUserInput,
		Status:    domain.NodeStatusDraft,
		SortOrder: 2,
		Revision:  3,
		CreatedAt: f.now,
		UpdatedAt: f.now,
	}
	f.nodes[caseID] = savedCase
	f.steps[stepID] = domain.CaseStep{
		ID:        stepID,
		NodeID:    caseID,
		Seq:       1,
		Action:    "submit the order",
		Expected:  "the order is accepted",
		Source:    domain.StepSourceUserInput,
		CreatedAt: f.now,
		UpdatedAt: f.now,
	}
	f.assertions[3001] = domain.CaseAssertion{
		ID:         3001,
		NodeID:     caseID,
		StepID:     &stepID,
		Kind:       domain.AssertionKindUI,
		Target:     "/order/confirm",
		Expression: "order number is not empty",
		Expected:   "an order number is shown",
		Source:     domain.NodeSourceUserInput,
		CreatedAt:  f.now,
		UpdatedAt:  f.now,
	}
	f.sources[4001] = domain.CaseSource{
		ID:         4001,
		VersionID:  runTestVersionID,
		NodeID:     caseID,
		RefKind:    domain.RefRequirement,
		RefID:      &requirementID,
		Source:     domain.NodeSourceUserInput,
		Confidence: domain.ConfidenceHigh,
		CreatedAt:  f.now,
		UpdatedAt:  f.now,
	}
	return savedCase
}

// seedOpenBlocker stores one open blocker finding of the review.
func seedOpenBlocker(f *runFixture, reviewID int64) domain.CaseReviewFinding {
	finding := domain.CaseReviewFinding{
		ID:        5001,
		ReviewID:  reviewID,
		Category:  domain.FindingCoverage,
		Severity:  domain.SeverityBlocker,
		Detail:    "case does not cover the refund requirement",
		Status:    domain.FindingOpen,
		Source:    domain.FindingSourceAI,
		CreatedBy: 42,
		CreatedAt: f.now,
		UpdatedAt: f.now,
	}
	f.findings[finding.ID] = finding
	return finding
}

// reviewDomainReason extracts the stable details.reason of a domain error.
func reviewDomainReason(t *testing.T, err error) string {
	t.Helper()
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("error %v is not a domain error", err)
	}
	return domainErr.Reason
}

// TestApproveBlockedByOpenBlocker covers the approve gate of spec §6.3: open
// blocker findings without acknowledgeFindings are a 409 blocker_findings_open
// carrying the finding ids, and nothing is written or published.
func TestApproveBlockedByOpenBlocker(t *testing.T) {
	ctx := context.Background()
	fixture := newRunFixture()
	fixture.version.Status = "reviewing"
	seedReviewGraph(fixture)
	review := fixture.seedReview(9001, domain.ReviewDraft)
	blocker := seedOpenBlocker(fixture, review.ID)
	service := reviewServiceFor(fixture, nil, nil)

	_, err := service.Approve(ctx, runTestVersionID, 42, application.ReviewCommand{
		Decision: application.ReviewDecisionApprove,
	})
	if err == nil {
		t.Fatal("approve with an open blocker and no acknowledgement must fail")
	}
	if reason := reviewDomainReason(t, err); reason != "blocker_findings_open" {
		t.Fatalf("reason=%q want blocker_findings_open", reason)
	}
	var blockerErr *application.BlockerFindingsError
	if !errors.As(err, &blockerErr) {
		t.Fatalf("error %v does not expose the blocker finding ids", err)
	}
	if len(blockerErr.FindingIDs) != 1 || blockerErr.FindingIDs[0] != blocker.ID {
		t.Fatalf("blocker finding ids=%v want [%d]", blockerErr.FindingIDs, blocker.ID)
	}
	details := blockerErr.Details()
	if details["reason"] != "blocker_findings_open" {
		t.Fatalf("details reason=%v want blocker_findings_open", details["reason"])
	}
	detailIDs, ok := details["findingIds"].([]string)
	if !ok || len(detailIDs) != 1 || detailIDs[0] != "5001" {
		t.Fatalf("details findingIds=%v want [\"5001\"]", details["findingIds"])
	}
	if fixture.baseline != nil {
		t.Fatalf("baseline=%+v want none", fixture.baseline)
	}
	for id, node := range fixture.nodes {
		if node.Status != domain.NodeStatusDraft {
			t.Fatalf("node %d status=%s want draft", id, node.Status)
		}
	}
	if stored := fixture.reviews[review.ID]; stored.Status != domain.ReviewDraft {
		t.Fatalf("review status=%s want draft", stored.Status)
	}
	if len(fixture.events) != 0 {
		t.Fatalf("events=%v want none", fixture.events)
	}
	if fixture.txCount != 0 {
		t.Fatalf("txCount=%d want 0", fixture.txCount)
	}
}

// TestApproveWithAcknowledgeSucceeds covers the acknowledged approval: the
// review is approved, the baseline is frozen, every node becomes reviewed and the
// version becomes executing, with gate.resolved followed by
// case.baseline_frozen.
func TestApproveWithAcknowledgeSucceeds(t *testing.T) {
	ctx := context.Background()
	fixture := newRunFixture()
	fixture.version.Status = "reviewing"
	seedReviewGraph(fixture)
	review := fixture.seedReview(9001, domain.ReviewDraft)
	blocker := seedOpenBlocker(fixture, review.ID)
	lifecycle := &reviewVersionLifecycle{f: fixture}
	service := reviewServiceFor(fixture, lifecycle, nil)

	result, err := service.Approve(ctx, runTestVersionID, 42, application.ReviewCommand{
		Decision:            application.ReviewDecisionApprove,
		Comment:             "blocker accepted for this release",
		AcknowledgeFindings: true,
	})
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if result.Baseline.VersionID != runTestVersionID {
		t.Fatalf("baseline version=%d want %d", result.Baseline.VersionID, runTestVersionID)
	}
	if result.Baseline.ReviewID != review.ID {
		t.Fatalf("baseline review=%d want %d", result.Baseline.ReviewID, review.ID)
	}
	if result.Baseline.NodeCount != 2 || result.Baseline.CaseCount != 1 {
		t.Fatalf("baseline counts node=%d case=%d want 2/1", result.Baseline.NodeCount, result.Baseline.CaseCount)
	}
	if len(result.Baseline.GraphHash) != 64 {
		t.Fatalf("graph hash=%q want 64 hex chars", result.Baseline.GraphHash)
	}
	if result.Baseline.CodeBaselineCommit == nil || *result.Baseline.CodeBaselineCommit != *fixture.version.CodeBaselineCommit {
		t.Fatalf("baseline code commit=%v want %v", result.Baseline.CodeBaselineCommit, fixture.version.CodeBaselineCommit)
	}
	if result.Baseline.FrozenBy != 42 {
		t.Fatalf("frozen by=%d want 42", result.Baseline.FrozenBy)
	}
	if !result.Baseline.FrozenAt.Equal(fixture.now) {
		t.Fatalf("frozen at=%v want %v", result.Baseline.FrozenAt, fixture.now)
	}
	if stored := fixture.reviews[review.ID]; stored.Status != domain.ReviewApproved {
		t.Fatalf("review status=%s want approved", stored.Status)
	}
	if result.Review.Status != domain.ReviewApproved {
		t.Fatalf("result review status=%s want approved", result.Review.Status)
	}
	if fixture.reviews[review.ID].Comment != "blocker accepted for this release" {
		t.Fatalf("review comment=%q", fixture.reviews[review.ID].Comment)
	}
	if fixture.version.Status != "executing" {
		t.Fatalf("version status=%s want executing", fixture.version.Status)
	}
	for id, node := range fixture.nodes {
		if node.Status != domain.NodeStatusReviewed {
			t.Fatalf("node %d status=%s want reviewed", id, node.Status)
		}
	}
	if !lifecycle.sawBaseline {
		t.Fatal("ApproveBaseline ran before the baseline was written")
	}
	if !lifecycle.sawReviewed {
		t.Fatal("ApproveBaseline ran before every node was reviewed")
	}
	if stored := fixture.findings[blocker.ID]; stored.Status != domain.FindingOpen {
		t.Fatalf("acknowledged finding status=%s want it kept open", stored.Status)
	}
	if got := fixture.eventCount(domain.EventGateResolved); got != 1 {
		t.Fatalf("gate.resolved events=%d want 1", got)
	}
	if got := fixture.eventCount(domain.EventCaseBaselineFrozen); got != 1 {
		t.Fatalf("case.baseline_frozen events=%d want 1", got)
	}
	if len(fixture.events) != 2 {
		t.Fatalf("events=%v want exactly gate.resolved + case.baseline_frozen", fixture.events)
	}
	if fixture.events[0].Type != domain.EventGateResolved || fixture.events[1].Type != domain.EventCaseBaselineFrozen {
		t.Fatalf("event order=%s,%s want gate.resolved,case.baseline_frozen",
			fixture.events[0].Type, fixture.events[1].Type)
	}
	if got := fixture.events[0].Detail["gate"]; got != "mindmap_review" {
		t.Fatalf("gate detail=%v want mindmap_review", got)
	}
	if got := fixture.events[1].Detail["baselineId"]; got != result.Baseline.ID {
		t.Fatalf("baseline_frozen detail baselineId=%v want %d", got, result.Baseline.ID)
	}
	hashPrefix, _ := fixture.events[1].Detail["graphHash"].(string)
	if hashPrefix == "" || len(hashPrefix) > 12 || result.Baseline.GraphHash[:len(hashPrefix)] != hashPrefix {
		t.Fatalf("baseline_frozen detail graphHash=%q want a prefix of %q", hashPrefix, result.Baseline.GraphHash)
	}
}

// TestApproveCompensatesWhenVersionConflicts covers the compensation of spec
// §6.4: when ApproveBaseline loses the version race, the baseline is deleted and
// node status/revision are restored before the 409 is returned.
func TestApproveCompensatesWhenVersionConflicts(t *testing.T) {
	ctx := context.Background()
	fixture := newRunFixture()
	fixture.version.Status = "reviewing"
	savedCase := seedReviewGraph(fixture)
	fixture.seedReview(9001, domain.ReviewApproved)
	lifecycle := &reviewVersionLifecycle{
		f: fixture,
		approveErr: domain.NewStateConflict("version_state_conflict",
			"version moved out of reviewing concurrently"),
	}
	service := reviewServiceFor(fixture, lifecycle, nil)

	_, err := service.Approve(ctx, runTestVersionID, 42, application.ReviewCommand{
		Decision: application.ReviewDecisionApprove,
	})
	if err == nil {
		t.Fatal("approve must fail when ApproveBaseline conflicts")
	}
	if reason := reviewDomainReason(t, err); reason != "version_state_conflict" {
		t.Fatalf("reason=%q want version_state_conflict", reason)
	}
	if fixture.baseline != nil {
		t.Fatalf("baseline=%+v want it deleted by the compensation", fixture.baseline)
	}
	if fixture.version.Status != "reviewing" {
		t.Fatalf("version status=%s want reviewing", fixture.version.Status)
	}
	restored, ok := fixture.nodes[savedCase.ID]
	if !ok {
		t.Fatalf("case node %d disappeared", savedCase.ID)
	}
	if restored.Status != domain.NodeStatusDraft {
		t.Fatalf("case status=%s want draft after compensation", restored.Status)
	}
	if restored.Revision != savedCase.Revision {
		t.Fatalf("case revision=%d want %d after compensation", restored.Revision, savedCase.Revision)
	}
	for id, node := range fixture.nodes {
		if node.Status != domain.NodeStatusDraft {
			t.Fatalf("node %d status=%s want draft after compensation", id, node.Status)
		}
	}
	if len(fixture.events) != 0 {
		t.Fatalf("events=%v want none on a compensated approval", fixture.events)
	}
	if !lifecycle.sawBaseline || !lifecycle.sawReviewed {
		t.Fatalf("ApproveBaseline saw baseline=%v reviewedNodes=%v; spec §6.4 freezes before the version transition",
			lifecycle.sawBaseline, lifecycle.sawReviewed)
	}
	if fixture.txCount != 2 {
		t.Fatalf("txCount=%d want 2 (freeze + compensation)", fixture.txCount)
	}
}

// TestSecondApproveRejected covers the single-freeze rule: a second approve is a
// baseline_frozen conflict and publishes no duplicate events.
func TestSecondApproveRejected(t *testing.T) {
	ctx := context.Background()
	fixture := newRunFixture()
	fixture.version.Status = "reviewing"
	seedReviewGraph(fixture)
	fixture.seedReview(9001, domain.ReviewApproved)
	service := reviewServiceFor(fixture, nil, nil)

	first, err := service.Approve(ctx, runTestVersionID, 42, application.ReviewCommand{
		Decision: application.ReviewDecisionApprove,
	})
	if err != nil {
		t.Fatalf("first approve: %v", err)
	}
	eventsBefore := len(fixture.events)

	_, err = service.Approve(ctx, runTestVersionID, 42, application.ReviewCommand{
		Decision: application.ReviewDecisionApprove,
	})
	if err == nil {
		t.Fatal("second approve must fail")
	}
	if reason := reviewDomainReason(t, err); reason != "baseline_frozen" {
		t.Fatalf("reason=%q want baseline_frozen", reason)
	}
	if fixture.baseline == nil || fixture.baseline.ID != first.Baseline.ID {
		t.Fatalf("baseline=%+v want the original %d", fixture.baseline, first.Baseline.ID)
	}
	if len(fixture.events) != eventsBefore {
		t.Fatalf("events grew from %d to %d on a rejected approve", eventsBefore, len(fixture.events))
	}
	if fixture.version.Status != "executing" {
		t.Fatalf("version status=%s want executing", fixture.version.Status)
	}
}

// TestRejectReturnsToDesigning covers the reject path of spec §6.3: the draft
// review is rejected with the comment, the version returns to designing and no
// baseline is written.
func TestRejectReturnsToDesigning(t *testing.T) {
	ctx := context.Background()
	fixture := newRunFixture()
	fixture.version.Status = "reviewing"
	seedReviewGraph(fixture)
	review := fixture.seedReview(9001, domain.ReviewDraft)
	service := reviewServiceFor(fixture, nil, nil)

	result, err := service.Reject(ctx, runTestVersionID, 42, "缺少退款场景")
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if result.Version.Status != "designing" {
		t.Fatalf("version status=%s want designing", result.Version.Status)
	}
	if result.Review.ID != review.ID || result.Review.Status != domain.ReviewRejected {
		t.Fatalf("review=%+v want %d rejected", result.Review, review.ID)
	}
	if result.Review.Comment != "缺少退款场景" {
		t.Fatalf("review comment=%q", result.Review.Comment)
	}
	if result.Review.DecidedBy == nil || *result.Review.DecidedBy != 42 {
		t.Fatalf("review decided by=%v want 42", result.Review.DecidedBy)
	}
	if fixture.baseline != nil {
		t.Fatalf("baseline=%+v want none after reject", fixture.baseline)
	}
	for id, node := range fixture.nodes {
		if node.Status != domain.NodeStatusDraft {
			t.Fatalf("node %d status=%s want draft after reject", id, node.Status)
		}
	}
	if got := fixture.eventCount(domain.EventGateResolved); got != 1 {
		t.Fatalf("gate.resolved events=%d want 1", got)
	}
	if got := fixture.eventCount(domain.EventCaseBaselineFrozen); got != 0 {
		t.Fatalf("case.baseline_frozen events=%d want 0", got)
	}
	if got := fixture.events[0].Detail["decision"]; got != "reject" {
		t.Fatalf("gate.resolved decision=%v want reject", got)
	}
}

// TestReviewStartDelegatesToRunService covers the review run entry point: it
// starts the shared run coordinator with kind=review, reusing its validation and
// per-kind single-flight rules.
func TestReviewStartDelegatesToRunService(t *testing.T) {
	ctx := context.Background()
	fixture := newRunFixture()
	fixture.version.Status = "reviewing"
	starter := &reviewRunStarter{}
	service := reviewServiceFor(fixture, nil, starter)

	run, err := service.StartReview(ctx, runTestVersionID, 42)
	if err != nil {
		t.Fatalf("start review: %v", err)
	}
	if run.Kind != domain.RunKindReview || run.VersionID != runTestVersionID {
		t.Fatalf("run=%+v want kind review of version %d", run, runTestVersionID)
	}
	if len(starter.calls) != 1 {
		t.Fatalf("run starter calls=%d want 1", len(starter.calls))
	}
	if starter.calls[0].Kind != domain.RunKindReview {
		t.Fatalf("started kind=%q want review", starter.calls[0].Kind)
	}
}

// TestReviewListFindingsAndUpdate covers the review read path and the finding
// disposition rules: only fixed|dismissed are accepted and a finding of another
// version is not updatable through this version.
func TestReviewListFindingsAndUpdate(t *testing.T) {
	ctx := context.Background()
	fixture := newRunFixture()
	fixture.version.Status = "reviewing"
	review := fixture.seedReview(9001, domain.ReviewDraft)
	blocker := seedOpenBlocker(fixture, review.ID)
	service := reviewServiceFor(fixture, nil, nil)

	reviews, nextCursor, hasMore, err := service.List(ctx, runTestVersionID, 10, "")
	if err != nil {
		t.Fatalf("list reviews: %v", err)
	}
	if len(reviews) != 1 || reviews[0].ID != review.ID || nextCursor != "" || hasMore {
		t.Fatalf("reviews=%+v cursor=%q hasMore=%v", reviews, nextCursor, hasMore)
	}
	findings, err := service.ListFindings(ctx, runTestVersionID, domain.FindingOpen, domain.SeverityBlocker, nil)
	if err != nil {
		t.Fatalf("list findings: %v", err)
	}
	if len(findings) != 1 || findings[0].ID != blocker.ID {
		t.Fatalf("findings=%+v want [%d]", findings, blocker.ID)
	}
	updated, err := service.UpdateFinding(ctx, runTestVersionID, blocker.ID, 42, domain.FindingFixed)
	if err != nil {
		t.Fatalf("update finding: %v", err)
	}
	if updated.Status != domain.FindingFixed {
		t.Fatalf("finding status=%s want fixed", updated.Status)
	}
	if _, err := service.UpdateFinding(ctx, runTestVersionID, blocker.ID, 42, domain.FindingOpen); err == nil {
		t.Fatal("reopening a finding through PATCH must be rejected")
	} else if reason := reviewDomainReason(t, err); reason != "invalid_finding_status" {
		t.Fatalf("reason=%q want invalid_finding_status", reason)
	}
	if _, err := service.UpdateFinding(ctx, runTestVersionID, 999999, 42, domain.FindingDismissed); err == nil {
		t.Fatal("updating an unknown finding must fail")
	} else if reason := reviewDomainReason(t, err); reason != "finding_not_found" {
		t.Fatalf("reason=%q want finding_not_found", reason)
	}
}
