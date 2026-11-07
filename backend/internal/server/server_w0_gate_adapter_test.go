package server

// Task 37 fix round 1: legacy hard-gate adapter semantics.
//
// The adapter must pin each hard gate's source stage before confirming any
// entity (no drafts may be frozen while the stage request is rejected), the
// automatic clarification convergence must no-op outside clarifying like the
// pre-v0.22 StageMachine, the reject route must be the audited StageService
// back edge, and a denied non-user gate attempt must be audited.

import (
	"context"
	"testing"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

type adapterStage struct {
	stage       version.Stage
	requests    int
	rejects     int
	lastRequest version.Stage
	rejectErr   error
}

func (s *adapterStage) Stage(_ context.Context, versionID int64) (versionapp.StageView, error) {
	return versionapp.StageView{VersionID: versionID, Stage: s.stage}, nil
}
func (s *adapterStage) Request(_ context.Context, _ int64, to version.Stage) error {
	s.requests++
	s.lastRequest = to
	s.stage = to
	return nil
}
func (s *adapterStage) Advance(context.Context, int64) error                   { return nil }
func (s *adapterStage) Block(context.Context, int64, versionapp.Blocker) error { return nil }
func (s *adapterStage) Unblock(context.Context, int64) error                   { return nil }
func (s *adapterStage) Pause(context.Context, int64, string) error             { return nil }
func (s *adapterStage) Resume(context.Context, int64) error                    { return nil }
func (s *adapterStage) Reject(_ context.Context, _ int64, _ string, _ *version.Stage) error {
	s.rejects++
	if s.rejectErr != nil {
		return s.rejectErr
	}
	s.stage = version.StageClarifying
	return nil
}

type adapterConfirmer struct {
	requirementCalls int
	caseCalls        int
	err              error
}

func (c *adapterConfirmer) BatchConfirm(context.Context, int64, []int64) error {
	c.requirementCalls++
	return c.err
}
func (c *adapterConfirmer) ConfirmCases(context.Context, int64, []int64) error {
	c.caseCalls++
	return c.err
}

type adapterAudit struct{ events []auditapp.Event }

func (a *adapterAudit) Record(_ context.Context, event auditapp.Event) error {
	a.events = append(a.events, event)
	return nil
}

func adapterErrorCode(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	return platform.AsError(err).Code
}

func TestStageHTTPAdapterPinsGateSourceStage(t *testing.T) {
	t.Run("requirement gate at designing confirms nothing", func(t *testing.T) {
		stage := &adapterStage{stage: version.StageDesigning}
		confirmer := &adapterConfirmer{}
		adapter := &stageHTTPAdapter{workflow: stage, requirements: confirmer, cases: confirmer}
		_, err := adapter.OnRequirementConfirmed(context.Background(), 7, []int64{1})
		if code := adapterErrorCode(t, err); code != "invalid_stage_for_gate" {
			t.Fatalf("error code = %q, want invalid_stage_for_gate", code)
		}
		if confirmer.requirementCalls != 0 || stage.requests != 0 {
			t.Fatalf("wrong-stage gate mutated state: confirm=%d request=%d", confirmer.requirementCalls, stage.requests)
		}
	})
	t.Run("case gate at requirement_review confirms nothing", func(t *testing.T) {
		stage := &adapterStage{stage: version.StageRequirementReview}
		confirmer := &adapterConfirmer{}
		adapter := &stageHTTPAdapter{workflow: stage, requirements: confirmer, cases: confirmer}
		_, err := adapter.OnCasesConfirmed(context.Background(), 7, []int64{1})
		if code := adapterErrorCode(t, err); code != "invalid_stage_for_gate" {
			t.Fatalf("error code = %q, want invalid_stage_for_gate", code)
		}
		if confirmer.caseCalls != 0 || stage.requests != 0 {
			t.Fatalf("wrong-stage gate mutated state: confirm=%d request=%d", confirmer.caseCalls, stage.requests)
		}
	})
	t.Run("requirement gate at requirement_review confirms then advances", func(t *testing.T) {
		stage := &adapterStage{stage: version.StageRequirementReview}
		confirmer := &adapterConfirmer{}
		adapter := &stageHTTPAdapter{workflow: stage, requirements: confirmer, cases: confirmer}
		view, err := adapter.OnRequirementConfirmed(context.Background(), 7, []int64{1, 2})
		if err != nil {
			t.Fatalf("OnRequirementConfirmed: %v", err)
		}
		if confirmer.requirementCalls != 1 || stage.requests != 1 || stage.lastRequest != version.StageDesigning {
			t.Fatalf("gate calls = confirm:%d requests:%d last:%q", confirmer.requirementCalls, stage.requests, stage.lastRequest)
		}
		if view.Stage != version.StageDesigning {
			t.Fatalf("view stage = %q, want designing", view.Stage)
		}
	})
	t.Run("confirmation failure leaves the stage untouched", func(t *testing.T) {
		stage := &adapterStage{stage: version.StageRequirementReview}
		confirmer := &adapterConfirmer{err: platform.NotFound("requirement")}
		adapter := &stageHTTPAdapter{workflow: stage, requirements: confirmer, cases: confirmer}
		if _, err := adapter.OnRequirementConfirmed(context.Background(), 7, []int64{1}); err == nil {
			t.Fatal("confirmation failure must propagate")
		}
		if stage.requests != 0 || stage.stage != version.StageRequirementReview {
			t.Fatalf("failed confirmation moved the stage: %#v", stage)
		}
	})
}

func TestStageHTTPAdapterConvergeClarificationOnlyActsInClarifying(t *testing.T) {
	for _, current := range []version.Stage{version.StageCreated, version.StageDesigning, version.StageExecuting} {
		stage := &adapterStage{stage: current}
		adapter := &stageHTTPAdapter{workflow: stage}
		if err := adapter.ConvergeClarification(context.Background(), 7); err != nil {
			t.Fatalf("ConvergeClarification at %s = %v, want nil", current, err)
		}
		if stage.requests != 0 {
			t.Fatalf("ConvergeClarification at %s moved the stage", current)
		}
	}
	stage := &adapterStage{stage: version.StageClarifying}
	adapter := &stageHTTPAdapter{workflow: stage}
	if err := adapter.ConvergeClarification(context.Background(), 7); err != nil {
		t.Fatalf("ConvergeClarification: %v", err)
	}
	if stage.requests != 1 || stage.lastRequest != version.StageRequirementReview {
		t.Fatalf("ConvergeClarification at clarifying = requests:%d last:%q", stage.requests, stage.lastRequest)
	}
}

func TestStageHTTPAdapterRejectBackEdge(t *testing.T) {
	stage := &adapterStage{stage: version.StageRequirementReview}
	adapter := &stageHTTPAdapter{workflow: stage}
	view, err := adapter.Reject(context.Background(), 7, "missing acceptance criteria", nil)
	if err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if stage.rejects != 1 || view.Stage != version.StageClarifying {
		t.Fatalf("reject state/view = rejects:%d view:%#v", stage.rejects, view)
	}

	stage = &adapterStage{stage: version.StageExecuting, rejectErr: versionapp.InvalidStageForReject(version.StageExecuting)}
	adapter = &stageHTTPAdapter{workflow: stage}
	_, err = adapter.Reject(context.Background(), 7, "nope", nil)
	if code := adapterErrorCode(t, err); code != "invalid_stage_for_gate" {
		t.Fatalf("reject outside a gate code = %q, want invalid_stage_for_gate", code)
	}
}

func TestStageHTTPAdapterRecordsDeniedGateAudit(t *testing.T) {
	audit := &adapterAudit{}
	adapter := &stageHTTPAdapter{audit: audit}
	ctx := platform.WithActor(context.Background(), platform.Actor{ID: 77, Role: "assistant"})
	adapter.RecordGateDenied(ctx, 7, "requirement")
	if len(audit.events) != 1 {
		t.Fatalf("audit events = %#v, want one denial", audit.events)
	}
	event := audit.events[0]
	if event.Action != versionapp.StageAuditGateDenied || event.TargetID != "7" {
		t.Fatalf("audit event = %#v", event)
	}
	if event.ActorID == nil || *event.ActorID != 77 {
		t.Fatalf("audit actor = %#v", event.ActorID)
	}
	if event.Metadata["gate"] != "requirement" || event.Metadata["role"] != "assistant" {
		t.Fatalf("audit metadata = %#v", event.Metadata)
	}
}
