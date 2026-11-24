package designapp

import (
	"context"
	"errors"
	"strings"
	"testing"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// fakeJudgmentRepo is the in-memory JudgmentRepository used by the service
// tests. CountReworks mirrors the controller ruling (max(round)-1 over the
// case's judgment facts); the real SQL derivation is pinned by the integration
// test against PostgreSQL, so the two producers are covered separately.
type fakeJudgmentRepo struct {
	judgments         []design.Judgment
	overrides         []design.Override
	exhausted         map[[2]int64]bool
	insertJudgmentErr error
}

func (r *fakeJudgmentRepo) InsertJudgment(_ context.Context, judgment design.Judgment) error {
	if r.insertJudgmentErr != nil {
		return r.insertJudgmentErr
	}
	for _, existing := range r.judgments {
		if existing.CaseID == judgment.CaseID && existing.CaseVersionID == judgment.CaseVersionID {
			return design.ErrDuplicate
		}
	}
	r.judgments = append(r.judgments, judgment)
	return nil
}

func (r *fakeJudgmentRepo) LatestJudgment(_ context.Context, caseID int64) (*design.Judgment, error) {
	var latest *design.Judgment
	for i := range r.judgments {
		j := &r.judgments[i]
		if j.CaseID != caseID {
			continue
		}
		if latest == nil || j.CreatedAt.After(latest.CreatedAt) ||
			(j.CreatedAt.Equal(latest.CreatedAt) && j.ID > latest.ID) {
			latest = j
		}
	}
	return latest, nil
}

func (r *fakeJudgmentRepo) ListJudgmentsByRequirement(_ context.Context, versionID, requirementID int64) ([]design.Judgment, error) {
	matches := make([]design.Judgment, 0)
	for _, j := range r.judgments {
		if j.VersionID == versionID && j.RequirementID == requirementID {
			matches = append(matches, j)
		}
	}
	return matches, nil
}

func (r *fakeJudgmentRepo) InsertOverride(_ context.Context, override design.Override) error {
	if !ValidOverrideCondition(override.Condition) {
		return platform.Invalid("condition", "must be one of fail_exhausted, judge_exhausted")
	}
	r.overrides = append(r.overrides, override)
	return nil
}

func (r *fakeJudgmentRepo) LatestOverride(_ context.Context, caseID, caseVersionID int64) (*design.Override, error) {
	var latest *design.Override
	for i := range r.overrides {
		o := &r.overrides[i]
		if o.CaseID != caseID || o.CaseVersionID != caseVersionID {
			continue
		}
		if latest == nil || o.CreatedAt.After(latest.CreatedAt) ||
			(o.CreatedAt.Equal(latest.CreatedAt) && o.ID > latest.ID) {
			latest = o
		}
	}
	return latest, nil
}

func (r *fakeJudgmentRepo) CountReworks(_ context.Context, caseID int64) (int, error) {
	maxRound := 0
	for _, j := range r.judgments {
		if j.CaseID == caseID && j.Round > maxRound {
			maxRound = j.Round
		}
	}
	if maxRound == 0 {
		return 0, nil
	}
	return maxRound - 1, nil
}

func (r *fakeJudgmentRepo) InsertJudgeExhausted(_ context.Context, _, _, caseID, caseVersionID int64) (bool, error) {
	if r.exhausted == nil {
		r.exhausted = map[[2]int64]bool{}
	}
	key := [2]int64{caseID, caseVersionID}
	created := !r.exhausted[key]
	r.exhausted[key] = true
	return created, nil
}

func (r *fakeJudgmentRepo) HasJudgeExhausted(_ context.Context, caseID, caseVersionID int64) (bool, error) {
	return r.exhausted[[2]int64{caseID, caseVersionID}], nil
}

// fakeJudgmentCaseReader serves ReadCase from a fixed map. The judgment service
// must always read the CURRENT version (nil selector); any other selector is a
// question this stub refuses to answer (method discipline: panic, not zero).
type fakeJudgmentCaseReader struct {
	cases       map[int64]design.CaseData
	readErrByID map[int64]error
}

func (r *fakeJudgmentCaseReader) ReadCase(_ context.Context, versionID, caseID int64, caseVersionID *int64) (design.CaseData, error) {
	if caseVersionID != nil {
		panic("judgment service must read the current version with a nil selector")
	}
	if err := r.readErrByID[caseID]; err != nil {
		return design.CaseData{}, err
	}
	data, ok := r.cases[caseID]
	if !ok || data.Requirement.VersionID != versionID {
		return design.CaseData{}, design.ErrNotFound
	}
	return data, nil
}

// fakeJudgmentSettings answers only registered keys; an unregistered key is a
// question the production path never asks.
type fakeJudgmentSettings map[string]int64

func (s fakeJudgmentSettings) Int(_ context.Context, key string) (int64, error) {
	value, ok := s[key]
	if !ok {
		panic("unexpected settings key: " + key)
	}
	return value, nil
}

type judgmentFixture struct {
	repo     *fakeJudgmentRepo
	cases    *fakeJudgmentCaseReader
	settings fakeJudgmentSettings
	events   *fakePublisher
	svc      *JudgmentService
}

func newJudgmentFixture() *judgmentFixture {
	f := &judgmentFixture{
		repo:     &fakeJudgmentRepo{exhausted: map[[2]int64]bool{}},
		cases:    &fakeJudgmentCaseReader{cases: map[int64]design.CaseData{}},
		settings: fakeJudgmentSettings{maxReworkRoundsKey: 1},
		events:   &fakePublisher{},
	}
	f.svc = NewJudgmentService(f.repo, f.cases, &fakeIDs{}, newFixedClock(), f.events, f.settings)
	return f
}

func (f *judgmentFixture) seedCase(caseID, caseVersionID, requirementID int64, contentHash string) {
	current := caseVersionID
	f.cases.cases[caseID] = design.CaseData{
		Case:        design.CaseEntity{ID: caseID, CurrentVersionID: &current},
		Requirement: design.RequirementRef{ID: requirementID, VersionID: 1001},
		Selected:    design.CaseVersion{ID: caseVersionID, CaseID: caseID, ContentHash: contentHash},
	}
}

func (f *judgmentFixture) seedJudgment(id, caseID, caseVersionID int64, round int, verdict string) {
	f.repo.judgments = append(f.repo.judgments, design.Judgment{
		ID: id, VersionID: 1001, RequirementID: 10, CaseID: caseID,
		CaseVersionID: caseVersionID, ContentHash: "seed-hash", Round: round, Verdict: verdict,
	})
}

func validSubmitInput() SubmitJudgmentInput {
	return SubmitJudgmentInput{
		CaseID:        7001,
		CaseVersionID: 5,
		Verdict:       design.VerdictPass,
		Findings: []design.JudgmentFinding{
			{Dimension: "coverage", Severity: design.SeverityMinor, Message: "边界断言可再补一条", CaseRef: "7001"},
		},
	}
}

func requireInvalidField(t *testing.T, err error, field string) {
	t.Helper()
	apiErr := platform.AsError(err)
	if apiErr == nil || apiErr.Status != 400 {
		t.Fatalf("want 400 invalid for %s, got %v", field, err)
	}
	if apiErr.Details["field"] != field {
		t.Fatalf("want field %q, got details %v (err %v)", field, apiErr.Details, err)
	}
}

func TestJudgmentServiceSubmitValidatesPayload(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*SubmitJudgmentInput)
		field  string
	}{
		{"bad verdict", func(in *SubmitJudgmentInput) { in.Verdict = "maybe" }, "verdict"},
		{"pass with blocker", func(in *SubmitJudgmentInput) {
			in.Findings[0].Severity = design.SeverityBlocker
		}, "verdict"},
		{"pass with important", func(in *SubmitJudgmentInput) {
			in.Findings[0].Severity = design.SeverityImportant
		}, "verdict"},
		{"bad severity", func(in *SubmitJudgmentInput) {
			in.Verdict = design.VerdictFail
			in.Findings[0].Severity = "critical"
		}, "findings[0].severity"},
		{"empty message", func(in *SubmitJudgmentInput) { in.Findings[0].Message = "  " }, "findings[0].message"},
		{"empty dimension", func(in *SubmitJudgmentInput) { in.Findings[0].Dimension = "" }, "findings[0].dimension"},
		{"missing case id", func(in *SubmitJudgmentInput) { in.CaseID = 0 }, "case_id"},
		{"missing case version id", func(in *SubmitJudgmentInput) { in.CaseVersionID = 0 }, "case_version_id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newJudgmentFixture()
			f.seedCase(7001, 5, 10, strings.Repeat("a", 64))
			in := validSubmitInput()
			tc.mutate(&in)
			_, err := f.svc.SubmitJudgment(context.Background(), 1001, 10, 900, in)
			requireInvalidField(t, err, tc.field)
			if len(f.repo.judgments) != 0 {
				t.Fatal("invalid submission must not write a fact")
			}
			if len(f.events.events) != 0 {
				t.Fatal("invalid submission must not emit an event")
			}
		})
	}
}

func TestJudgmentServiceSubmitTooManyFindings(t *testing.T) {
	f := newJudgmentFixture()
	f.seedCase(7001, 5, 10, strings.Repeat("a", 64))
	in := validSubmitInput()
	in.Verdict = design.VerdictFail
	in.Findings = make([]design.JudgmentFinding, MaxJudgmentFindings+1)
	for i := range in.Findings {
		in.Findings[i] = design.JudgmentFinding{Dimension: "coverage", Severity: design.SeverityMinor, Message: "m"}
	}
	_, err := f.svc.SubmitJudgment(context.Background(), 1001, 10, 900, in)
	requireInvalidField(t, err, "findings")
}

func TestJudgmentServiceSubmitCaseScope(t *testing.T) {
	t.Run("unknown case", func(t *testing.T) {
		f := newJudgmentFixture()
		_, err := f.svc.SubmitJudgment(context.Background(), 1001, 10, 900, validSubmitInput())
		if apiErr := platform.AsError(err); apiErr == nil || apiErr.Status != 404 {
			t.Fatalf("want 404 for unknown case, got %v", err)
		}
	})
	t.Run("case of another requirement", func(t *testing.T) {
		f := newJudgmentFixture()
		f.seedCase(7001, 5, 11, strings.Repeat("a", 64))
		_, err := f.svc.SubmitJudgment(context.Background(), 1001, 10, 900, validSubmitInput())
		requireInvalidField(t, err, "case_id")
	})
	t.Run("stale case version", func(t *testing.T) {
		f := newJudgmentFixture()
		f.seedCase(7001, 6, 10, strings.Repeat("b", 64))
		in := validSubmitInput() // still points at version 5
		_, err := f.svc.SubmitJudgment(context.Background(), 1001, 10, 900, in)
		if apiErr := platform.AsError(err); apiErr == nil || apiErr.Status != 409 {
			t.Fatalf("want 409 for a non-current case version, got %v", err)
		}
		if len(f.repo.judgments) != 0 {
			t.Fatal("stale submission must not write a fact")
		}
	})
	t.Run("case without current version", func(t *testing.T) {
		f := newJudgmentFixture()
		f.cases.cases[7001] = design.CaseData{
			Case:        design.CaseEntity{ID: 7001},
			Requirement: design.RequirementRef{ID: 10, VersionID: 1001},
		}
		_, err := f.svc.SubmitJudgment(context.Background(), 1001, 10, 900, validSubmitInput())
		if apiErr := platform.AsError(err); apiErr == nil || apiErr.Status != 409 {
			t.Fatalf("want 409 for a case without a current version, got %v", err)
		}
	})
}

func TestJudgmentServiceSubmitCaseRefMembership(t *testing.T) {
	t.Run("foreign requirement ref rejected", func(t *testing.T) {
		f := newJudgmentFixture()
		f.seedCase(7001, 5, 10, strings.Repeat("a", 64))
		f.seedCase(7002, 9, 11, strings.Repeat("c", 64))
		in := validSubmitInput()
		in.Findings[0].CaseRef = "7002"
		_, err := f.svc.SubmitJudgment(context.Background(), 1001, 10, 900, in)
		requireInvalidField(t, err, "findings[0].case_ref")
	})
	t.Run("sibling case of same requirement accepted", func(t *testing.T) {
		f := newJudgmentFixture()
		f.seedCase(7001, 5, 10, strings.Repeat("a", 64))
		f.seedCase(7002, 9, 10, strings.Repeat("c", 64))
		in := validSubmitInput()
		in.Findings[0].CaseRef = "7002"
		if _, err := f.svc.SubmitJudgment(context.Background(), 1001, 10, 900, in); err != nil {
			t.Fatalf("sibling case ref rejected: %v", err)
		}
	})
	t.Run("non numeric ref rejected", func(t *testing.T) {
		f := newJudgmentFixture()
		f.seedCase(7001, 5, 10, strings.Repeat("a", 64))
		in := validSubmitInput()
		in.Findings[0].CaseRef = "case-one"
		_, err := f.svc.SubmitJudgment(context.Background(), 1001, 10, 900, in)
		requireInvalidField(t, err, "findings[0].case_ref")
	})
	t.Run("transient ref read failure is not a field error", func(t *testing.T) {
		f := newJudgmentFixture()
		f.seedCase(7001, 5, 10, strings.Repeat("a", 64))
		f.cases.readErrByID = map[int64]error{7002: errors.New("db read failed")}
		in := validSubmitInput()
		in.Findings[0].CaseRef = "7002"
		_, err := f.svc.SubmitJudgment(context.Background(), 1001, 10, 900, in)
		if err == nil {
			t.Fatal("transient read failure must surface as an error")
		}
		if apiErr := platform.AsError(err); apiErr != nil && apiErr.Status == 400 {
			t.Fatalf("infrastructure failure misattributed as a 400 field error: %v", err)
		}
		if len(f.repo.judgments) != 0 {
			t.Fatal("failed ref validation must not write a fact")
		}
	})
	t.Run("empty ref is a requirement-level finding", func(t *testing.T) {
		f := newJudgmentFixture()
		f.seedCase(7001, 5, 10, strings.Repeat("a", 64))
		in := validSubmitInput()
		in.Findings[0].CaseRef = ""
		if _, err := f.svc.SubmitJudgment(context.Background(), 1001, 10, 900, in); err != nil {
			t.Fatalf("empty case ref rejected: %v", err)
		}
	})
}

func TestJudgmentServiceSubmitBindsCurrentContent(t *testing.T) {
	f := newJudgmentFixture()
	hash := strings.Repeat("a", 64)
	f.seedCase(7001, 5, 10, hash)
	result, err := f.svc.SubmitJudgment(context.Background(), 1001, 10, 900, validSubmitInput())
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if result.ContentHash != hash {
		t.Fatalf("content hash = %q, want the server-read current hash %q", result.ContentHash, hash)
	}
	if result.Round != 1 {
		t.Fatalf("first judgment round = %d, want 1", result.Round)
	}
	if result.JudgmentID <= 0 {
		t.Fatalf("judgment id = %d, want a generated positive id", result.JudgmentID)
	}
	if len(f.repo.judgments) != 1 {
		t.Fatalf("facts = %d, want 1", len(f.repo.judgments))
	}
	fact := f.repo.judgments[0]
	if fact.AgentRunID != 900 || fact.CaseVersionID != 5 || fact.ContentHash != hash ||
		fact.VersionID != 1001 || fact.RequirementID != 10 || fact.Verdict != design.VerdictPass {
		t.Fatalf("fact binding wrong: %+v", fact)
	}
	if fact.CreatedAt.IsZero() {
		t.Fatal("fact must carry a server timestamp")
	}
	events := f.events.byType("design.judgment_submitted")
	if len(events) != 1 {
		t.Fatalf("design.judgment_submitted events = %d, want 1", len(events))
	}
	event := events[0]
	if event.Channel != "version:1001" {
		t.Fatalf("channel = %q", event.Channel)
	}
	if event.Payload["case_id"] != "7001" || event.Payload["case_version_id"] != "5" ||
		event.Payload["verdict"] != design.VerdictPass || event.Payload["round"] != 1 {
		t.Fatalf("event payload wrong: %v", event.Payload)
	}
}

func TestJudgmentServiceSubmitRoundRules(t *testing.T) {
	t.Run("rework round after fail", func(t *testing.T) {
		f := newJudgmentFixture()
		f.seedCase(7001, 6, 10, strings.Repeat("b", 64))
		f.seedJudgment(1, 7001, 5, 1, design.VerdictFail)
		in := validSubmitInput()
		in.CaseVersionID = 6
		in.Round = 2
		result, err := f.svc.SubmitJudgment(context.Background(), 1001, 10, 900, in)
		if err != nil {
			t.Fatalf("rework round rejected: %v", err)
		}
		if result.Round != 2 {
			t.Fatalf("round = %d, want 2", result.Round)
		}
	})
	t.Run("host edit after fail keeps the round", func(t *testing.T) {
		f := newJudgmentFixture()
		f.seedCase(7001, 6, 10, strings.Repeat("b", 64))
		f.seedJudgment(1, 7001, 5, 1, design.VerdictFail)
		in := validSubmitInput()
		in.CaseVersionID = 6 // Round 0: conservative same-round default
		result, err := f.svc.SubmitJudgment(context.Background(), 1001, 10, 900, in)
		if err != nil {
			t.Fatalf("host-edit rejudge rejected: %v", err)
		}
		if result.Round != 1 {
			t.Fatalf("host edit consumed quota: round = %d, want 1", result.Round)
		}
		count, err := f.repo.CountReworks(context.Background(), 7001)
		if err != nil || count != 0 {
			t.Fatalf("rework count = %d (err %v), want 0 after a harmless host edit", count, err)
		}
	})
	t.Run("increment after pass rejected", func(t *testing.T) {
		f := newJudgmentFixture()
		f.seedCase(7001, 6, 10, strings.Repeat("b", 64))
		f.seedJudgment(1, 7001, 5, 1, design.VerdictPass)
		in := validSubmitInput()
		in.CaseVersionID = 6
		in.Round = 2
		_, err := f.svc.SubmitJudgment(context.Background(), 1001, 10, 900, in)
		requireInvalidField(t, err, "round")
	})
	t.Run("increment beyond budget rejected", func(t *testing.T) {
		f := newJudgmentFixture() // max_rework_rounds = 1
		f.seedCase(7001, 7, 10, strings.Repeat("c", 64))
		f.seedJudgment(1, 7001, 5, 1, design.VerdictFail)
		f.seedJudgment(2, 7001, 6, 2, design.VerdictFail)
		in := validSubmitInput()
		in.CaseVersionID = 7
		in.Round = 3
		_, err := f.svc.SubmitJudgment(context.Background(), 1001, 10, 900, in)
		requireInvalidField(t, err, "round")
	})
	t.Run("round skip rejected", func(t *testing.T) {
		f := newJudgmentFixture()
		f.seedCase(7001, 6, 10, strings.Repeat("b", 64))
		f.seedJudgment(1, 7001, 5, 1, design.VerdictFail)
		in := validSubmitInput()
		in.CaseVersionID = 6
		in.Round = 3
		_, err := f.svc.SubmitJudgment(context.Background(), 1001, 10, 900, in)
		requireInvalidField(t, err, "round")
	})
	t.Run("round regression rejected", func(t *testing.T) {
		f := newJudgmentFixture()
		f.seedCase(7001, 7, 10, strings.Repeat("c", 64))
		f.seedJudgment(1, 7001, 5, 1, design.VerdictFail)
		f.seedJudgment(2, 7001, 6, 2, design.VerdictFail)
		in := validSubmitInput()
		in.CaseVersionID = 7
		in.Round = 1
		_, err := f.svc.SubmitJudgment(context.Background(), 1001, 10, 900, in)
		requireInvalidField(t, err, "round")
	})
	t.Run("first judgment must be round one", func(t *testing.T) {
		f := newJudgmentFixture()
		f.seedCase(7001, 5, 10, strings.Repeat("a", 64))
		in := validSubmitInput()
		in.Round = 2
		_, err := f.svc.SubmitJudgment(context.Background(), 1001, 10, 900, in)
		requireInvalidField(t, err, "round")
	})
}

func TestJudgmentServiceSubmitDuplicateConflict(t *testing.T) {
	f := newJudgmentFixture()
	f.seedCase(7001, 5, 10, strings.Repeat("a", 64))
	f.repo.insertJudgmentErr = design.ErrDuplicate
	_, err := f.svc.SubmitJudgment(context.Background(), 1001, 10, 900, validSubmitInput())
	apiErr := platform.AsError(err)
	if apiErr == nil || apiErr.Status != 409 {
		t.Fatalf("want 409 conflict for a duplicate judgment, got %v", err)
	}
	if len(f.events.events) != 0 {
		t.Fatal("duplicate submission must not emit an event")
	}
}

func TestJudgmentServiceOverrideFailExhausted(t *testing.T) {
	f := newJudgmentFixture()
	hash := strings.Repeat("b", 64)
	f.seedCase(7001, 6, 10, hash)
	f.seedJudgment(1, 7001, 5, 1, design.VerdictFail)
	f.seedJudgment(2, 7001, 6, 2, design.VerdictFail) // rework budget 1 exhausted
	override, err := f.svc.Override(context.Background(), 1001, 7001, 42, "人工复核后接受该用例")
	if err != nil {
		t.Fatalf("override: %v", err)
	}
	if override.Condition != OverrideConditionFailExhausted {
		t.Fatalf("condition = %q, want %q", override.Condition, OverrideConditionFailExhausted)
	}
	if override.JudgmentID == nil || *override.JudgmentID != 2 {
		t.Fatalf("judgment id = %v, want the latest fact id 2", override.JudgmentID)
	}
	if override.CaseVersionID != 6 || override.ContentHash != hash {
		t.Fatalf("override must bind the current version content: %+v", override)
	}
	if override.ActorID != 42 || override.Reason != "人工复核后接受该用例" {
		t.Fatalf("override actor/reason wrong: %+v", override)
	}
	if len(f.repo.overrides) != 1 {
		t.Fatalf("override audit rows = %d, want 1", len(f.repo.overrides))
	}
	if len(f.repo.judgments) != 2 {
		t.Fatal("override must not mutate judgment facts")
	}
	events := f.events.byType("design.judgment_overridden")
	if len(events) != 1 {
		t.Fatalf("design.judgment_overridden events = %d, want 1", len(events))
	}
	if events[0].ActorID == nil || *events[0].ActorID != 42 {
		t.Fatalf("override event must carry the actor: %+v", events[0])
	}
	if events[0].Payload["condition"] != OverrideConditionFailExhausted {
		t.Fatalf("event condition = %v", events[0].Payload["condition"])
	}
}

func TestJudgmentServiceOverrideJudgeExhausted(t *testing.T) {
	f := newJudgmentFixture()
	hash := strings.Repeat("c", 64)
	f.seedCase(7001, 5, 10, hash)
	if _, err := f.repo.InsertJudgeExhausted(context.Background(), 1001, 10, 7001, 5); err != nil {
		t.Fatalf("seed exhausted fact: %v", err)
	}
	override, err := f.svc.Override(context.Background(), 1001, 7001, 42, "裁判不可用，人工审查")
	if err != nil {
		t.Fatalf("override: %v", err)
	}
	if override.Condition != OverrideConditionJudgeExhausted {
		t.Fatalf("condition = %q, want %q", override.Condition, OverrideConditionJudgeExhausted)
	}
	if override.JudgmentID != nil {
		t.Fatalf("judge_exhausted override must not reference a judgment: %v", override.JudgmentID)
	}
	if override.CaseVersionID != 5 || override.ContentHash != hash {
		t.Fatalf("override must bind the current version content: %+v", override)
	}
}

func TestJudgmentServiceOverrideRejectedWhenNotExhausted(t *testing.T) {
	cases := []struct {
		name string
		seed func(*judgmentFixture)
	}{
		{"no facts at all", func(f *judgmentFixture) {}},
		{"fail with budget remaining", func(f *judgmentFixture) {
			f.seedJudgment(1, 7001, 5, 1, design.VerdictFail)
		}},
		{"pass verdict", func(f *judgmentFixture) {
			f.seedJudgment(1, 7001, 5, 1, design.VerdictPass)
		}},
		{"exhausted fail on an older version", func(f *judgmentFixture) {
			f.seedJudgment(1, 7001, 4, 1, design.VerdictFail)
			f.seedJudgment(2, 7001, 4, 2, design.VerdictFail)
		}},
		{"exhausted fact on an older version", func(f *judgmentFixture) {
			_, _ = f.repo.InsertJudgeExhausted(context.Background(), 1001, 10, 7001, 4)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newJudgmentFixture()
			f.seedCase(7001, 5, 10, strings.Repeat("a", 64))
			tc.seed(f)
			_, err := f.svc.Override(context.Background(), 1001, 7001, 42, "提前放行")
			if apiErr := platform.AsError(err); apiErr == nil || apiErr.Status != 409 {
				t.Fatalf("want 409 for a premature override, got %v", err)
			}
			if len(f.repo.overrides) != 0 {
				t.Fatal("rejected override must not write an audit row")
			}
			if len(f.events.events) != 0 {
				t.Fatal("rejected override must not emit an event")
			}
		})
	}
}

func TestJudgmentServiceOverrideValidatesReasonAndActor(t *testing.T) {
	f := newJudgmentFixture()
	f.seedCase(7001, 5, 10, strings.Repeat("a", 64))
	_, _ = f.repo.InsertJudgeExhausted(context.Background(), 1001, 10, 7001, 5)
	_, err := f.svc.Override(context.Background(), 1001, 7001, 42, "   ")
	requireInvalidField(t, err, "reason")
	_, err = f.svc.Override(context.Background(), 1001, 7001, 0, "人工审查")
	requireInvalidField(t, err, "actor_id")
}

func TestJudgmentServiceReviewStateFor(t *testing.T) {
	cases := []struct {
		name string
		seed func(*judgmentFixture)
		want design.ReviewState
	}{
		{"no facts", func(f *judgmentFixture) {}, design.ReviewStatePending},
		{"pass", func(f *judgmentFixture) {
			f.seedJudgment(1, 7001, 5, 1, design.VerdictPass)
		}, design.ReviewStatePass},
		{"fail with budget remaining", func(f *judgmentFixture) {
			f.seedJudgment(1, 7001, 5, 1, design.VerdictFail)
		}, design.ReviewStateReworking},
		{"fail exhausted", func(f *judgmentFixture) {
			f.seedJudgment(1, 7001, 4, 1, design.VerdictFail)
			f.seedJudgment(2, 7001, 5, 2, design.VerdictFail)
		}, design.ReviewStateEscalatedFail},
		{"judge exhausted outranks pass", func(f *judgmentFixture) {
			f.seedJudgment(1, 7001, 5, 1, design.VerdictPass)
			_, _ = f.repo.InsertJudgeExhausted(context.Background(), 1001, 10, 7001, 5)
		}, design.ReviewStateJudgeUnavailable},
		{"stale verdict derives pending", func(f *judgmentFixture) {
			f.seedJudgment(1, 7001, 4, 1, design.VerdictPass)
		}, design.ReviewStatePending},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newJudgmentFixture()
			f.seedCase(7001, 5, 10, strings.Repeat("a", 64))
			tc.seed(f)
			got, err := f.svc.ReviewStateFor(context.Background(), 1001, 7001, 5)
			if err != nil {
				t.Fatalf("review state: %v", err)
			}
			if got != tc.want {
				t.Fatalf("review state = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestJudgmentServiceSettingsUnwiredFailsClosed(t *testing.T) {
	f := newJudgmentFixture()
	f.seedCase(7001, 5, 10, strings.Repeat("a", 64))
	svc := NewJudgmentService(f.repo, f.cases, &fakeIDs{}, newFixedClock(), f.events, nil)
	_, err := svc.ReviewStateFor(context.Background(), 1001, 7001, 5)
	if apiErr := platform.AsError(err); apiErr == nil || apiErr.Status != 503 {
		t.Fatalf("unwired settings must fail closed with 503, got %v", err)
	}
}

// judgmentGraphReader is a fixed, canned design.GraphData for ListJudgments
// tests -- it does not need the full fakeRepo surface, only the graph read.
type judgmentGraphReader struct {
	graph design.GraphData
	err   error
}

func (r judgmentGraphReader) ReadGraph(context.Context, int64) (design.GraphData, error) {
	return r.graph, r.err
}

func judgmentTestGraph(requirementID int64, cases ...design.CaseGraph) design.GraphData {
	return design.GraphData{
		VersionID: 1001,
		Requirements: []design.RequirementGraph{{
			Requirement: design.RequirementRef{ID: requirementID, VersionID: 1001, CurrentVersionID: 3002},
			Scenarios:   []design.ScenarioGraph{{Scenario: design.Scenario{ID: 5001}, Cases: cases}},
		}},
	}
}

func judgmentTestCase(caseID, caseVersionID int64) design.CaseGraph {
	current := caseVersionID
	return design.CaseGraph{
		Case:    design.CaseEntity{ID: caseID, CurrentVersionID: &current, Status: design.CaseStatusDraft},
		Current: design.CaseVersion{ID: caseVersionID, CaseID: caseID, Status: design.CaseStatusDraft},
	}
}

// TestJudgmentServiceListJudgments pins the T16 read report: one entry per
// current case (judged or not), the latest fact bound to the current version
// (a stale verdict never masquerades as current), and review_state derived
// through the SAME ReviewStateFor authority Confirm/the review gate use.
func TestJudgmentServiceListJudgments(t *testing.T) {
	f := newJudgmentFixture()
	f.seedCase(7001, 5, 10, strings.Repeat("a", 64))
	f.seedJudgment(1, 7001, 5, 1, design.VerdictPass)
	f.seedCase(7003, 8, 10, strings.Repeat("b", 64))
	f.seedJudgment(2, 7003, 7, 1, design.VerdictFail) // stale: bound to an older version
	f.seedCase(7005, 9, 10, strings.Repeat("c", 64))  // never judged

	graphs := judgmentGraphReader{graph: judgmentTestGraph(10,
		judgmentTestCase(7001, 5), judgmentTestCase(7003, 8), judgmentTestCase(7005, 9),
	)}
	svc := NewJudgmentService(f.repo, f.cases, &fakeIDs{}, newFixedClock(), f.events, f.settings).SetGraphReader(graphs)

	view, err := svc.ListJudgments(context.Background(), 1001, nil)
	if err != nil {
		t.Fatalf("ListJudgments: %v", err)
	}
	if len(view.Cases) != 3 {
		t.Fatalf("cases = %+v, want 3", view.Cases)
	}
	byCase := map[int64]JudgmentCaseJudgmentView{}
	for _, item := range view.Cases {
		byCase[item.CaseID] = item
	}
	pass := byCase[7001]
	if pass.ReviewState != design.ReviewStatePass || pass.Judgment == nil || pass.Judgment.ID != 1 {
		t.Fatalf("case 7001 = %+v, want pass with judgment 1", pass)
	}
	stale := byCase[7003]
	if stale.ReviewState != design.ReviewStatePending || stale.Judgment != nil {
		t.Fatalf("case 7003 = %+v, want pending with no current-version judgment (stale verdict must not leak)", stale)
	}
	unjudged := byCase[7005]
	if unjudged.ReviewState != design.ReviewStatePending || unjudged.Judgment != nil {
		t.Fatalf("case 7005 = %+v, want pending with no judgment", unjudged)
	}
}

// TestJudgmentServiceListJudgmentsRequirementFilter pins that a requirement_id
// filter narrows the graph enumeration before any fact read, and a requirement
// with no cases yields an empty report (not an error).
func TestJudgmentServiceListJudgmentsRequirementFilter(t *testing.T) {
	f := newJudgmentFixture()
	f.seedCase(7001, 5, 10, strings.Repeat("a", 64))
	f.seedJudgment(1, 7001, 5, 1, design.VerdictPass)
	graphs := judgmentGraphReader{graph: design.GraphData{
		VersionID: 1001,
		Requirements: []design.RequirementGraph{
			{
				Requirement: design.RequirementRef{ID: 10, VersionID: 1001, CurrentVersionID: 3002},
				Scenarios:   []design.ScenarioGraph{{Scenario: design.Scenario{ID: 5001}, Cases: []design.CaseGraph{judgmentTestCase(7001, 5)}}},
			},
			{
				Requirement: design.RequirementRef{ID: 20, VersionID: 1001, CurrentVersionID: 3004},
				Scenarios:   []design.ScenarioGraph{{Scenario: design.Scenario{ID: 5002}, Cases: []design.CaseGraph{judgmentTestCase(7009, 15)}}},
			},
		},
	}}
	svc := NewJudgmentService(f.repo, f.cases, &fakeIDs{}, newFixedClock(), f.events, f.settings).SetGraphReader(graphs)

	requirementID := int64(10)
	view, err := svc.ListJudgments(context.Background(), 1001, &requirementID)
	if err != nil {
		t.Fatalf("ListJudgments: %v", err)
	}
	if len(view.Cases) != 1 || view.Cases[0].CaseID != 7001 {
		t.Fatalf("filtered cases = %+v, want only case 7001", view.Cases)
	}

	other := int64(999)
	view, err = svc.ListJudgments(context.Background(), 1001, &other)
	if err != nil {
		t.Fatalf("ListJudgments (no match): %v", err)
	}
	if len(view.Cases) != 0 {
		t.Fatalf("unmatched requirement filter = %+v, want empty", view.Cases)
	}
}

// TestJudgmentServiceListJudgmentsUnwiredGraphFailsClosed pins constitution
// VII.6: without a graph reader wired, the report cannot be assembled and must
// fail closed instead of silently answering "no cases".
func TestJudgmentServiceListJudgmentsUnwiredGraphFailsClosed(t *testing.T) {
	f := newJudgmentFixture()
	_, err := f.svc.ListJudgments(context.Background(), 1001, nil)
	if apiErr := platform.AsError(err); apiErr == nil || apiErr.Status != 503 {
		t.Fatalf("unwired graph reader must fail closed with 503, got %v", err)
	}
}

// fakeReworkBudgetReader answers the escalation-budget port from a fixed
// terminal-rework count (production: the composition-root reader over the
// design.rework work_item rows).
type fakeReworkBudgetReader struct {
	terminal     int
	err          error
	calls        int
	gotVersionID int64
	gotCaseID    int64
	// terminalForVersion models the escalation dead-end read: a design.rework
	// bound to the failed version that is done / dead / failed-at-cap, i.e. can
	// no longer produce a revision for that fail.
	terminalForVersion bool
	terminalErr        error
	terminalCalls      int
	gotTerminalVersion int64
}

func (r *fakeReworkBudgetReader) TerminalReworkCount(_ context.Context, versionID, caseID int64) (int, error) {
	r.calls++
	r.gotVersionID = versionID
	r.gotCaseID = caseID
	return r.terminal, r.err
}

func (r *fakeReworkBudgetReader) HasTerminalReworkForVersion(_ context.Context, versionID, caseID, caseVersionID int64) (bool, error) {
	r.terminalCalls++
	r.gotVersionID = versionID
	r.gotCaseID = caseID
	r.gotTerminalVersion = caseVersionID
	return r.terminalForVersion, r.terminalErr
}

// TestJudgmentServiceReworkDeathEscalates pins spec SC-E4 "无永久 pending" on the
// rework side: a design.rework that retried to exhaustion is TERMINAL but
// produced no revised draft and no new judgment round, so CountReworks
// (max(round)-1) stays 0. Deriving the escalation budget from that counter alone
// kept review_state at reworking forever and rejected the human override — the
// case was stranded with no reachable decision point. The escalation budget is
// max(round-derived, terminal rework count).
func TestJudgmentServiceReworkDeathEscalates(t *testing.T) {
	seed := func(f *judgmentFixture) {
		f.seedCase(7001, 5, 10, strings.Repeat("a", 64))
		f.seedJudgment(1, 7001, 5, 1, design.VerdictFail) // round 1 -> CountReworks 0
	}

	t.Run("dead rework escalates the review state", func(t *testing.T) {
		f := newJudgmentFixture()
		seed(f)
		budget := &fakeReworkBudgetReader{terminal: 1}
		if got := f.svc.SetReworkBudgetReader(budget); got != f.svc {
			t.Fatal("SetReworkBudgetReader must return the service")
		}
		state, err := f.svc.ReviewStateFor(context.Background(), 1001, 7001, 5)
		if err != nil {
			t.Fatalf("review state: %v", err)
		}
		if state != design.ReviewStateEscalatedFail {
			t.Fatalf("review state = %q, want escalated_fail (a dead rework spent the budget)", state)
		}
		if budget.gotVersionID != 1001 || budget.gotCaseID != 7001 {
			t.Fatalf("budget reader queried with (%d,%d), want (1001,7001)", budget.gotVersionID, budget.gotCaseID)
		}
	})
	t.Run("dead rework makes the human override reachable", func(t *testing.T) {
		f := newJudgmentFixture()
		seed(f)
		f.svc.SetReworkBudgetReader(&fakeReworkBudgetReader{terminal: 1})
		override, err := f.svc.Override(context.Background(), 1001, 7001, 42, "打回不可用，人工审查后接受")
		if err != nil {
			t.Fatalf("override after rework death: %v", err)
		}
		if override.Condition != OverrideConditionFailExhausted {
			t.Fatalf("condition = %q, want %q", override.Condition, OverrideConditionFailExhausted)
		}
		if override.JudgmentID == nil || *override.JudgmentID != 1 {
			t.Fatalf("override must reference the fail fact: %v", override.JudgmentID)
		}
		if override.CaseVersionID != 5 || override.ContentHash != strings.Repeat("a", 64) {
			t.Fatalf("override must bind the current version content: %+v", override)
		}
	})
	t.Run("nil reader keeps the round-derived behavior", func(t *testing.T) {
		f := newJudgmentFixture()
		seed(f)
		state, err := f.svc.ReviewStateFor(context.Background(), 1001, 7001, 5)
		if err != nil || state != design.ReviewStateReworking {
			t.Fatalf("unwired budget reader: state=%q err=%v, want reworking (CountReworks alone)", state, err)
		}
		if _, err := f.svc.Override(context.Background(), 1001, 7001, 42, "提前放行"); err == nil {
			t.Fatal("unwired budget reader must not grant an override the facts do not support")
		}
	})
	t.Run("terminal count below the round count never lowers the budget", func(t *testing.T) {
		f := newJudgmentFixture()
		f.seedCase(7001, 6, 10, strings.Repeat("b", 64))
		f.seedJudgment(1, 7001, 5, 1, design.VerdictFail)
		f.seedJudgment(2, 7001, 6, 2, design.VerdictFail) // CountReworks 1
		f.svc.SetReworkBudgetReader(&fakeReworkBudgetReader{terminal: 0})
		state, err := f.svc.ReviewStateFor(context.Background(), 1001, 7001, 6)
		if err != nil || state != design.ReviewStateEscalatedFail {
			t.Fatalf("state=%q err=%v, want escalated_fail (max of both counters)", state, err)
		}
	})
	t.Run("in-flight rework still leaves the case reworking", func(t *testing.T) {
		f := newJudgmentFixture()
		seed(f)
		f.svc.SetReworkBudgetReader(&fakeReworkBudgetReader{terminal: 0})
		state, err := f.svc.ReviewStateFor(context.Background(), 1001, 7001, 5)
		if err != nil || state != design.ReviewStateReworking {
			t.Fatalf("state=%q err=%v, want reworking (the rework is still owed)", state, err)
		}
	})
	t.Run("budget read failure fails closed", func(t *testing.T) {
		f := newJudgmentFixture()
		seed(f)
		f.svc.SetReworkBudgetReader(&fakeReworkBudgetReader{err: errors.New("work item store down")})
		if _, err := f.svc.ReviewStateFor(context.Background(), 1001, 7001, 5); err == nil {
			t.Fatal("an unreadable rework budget must fail closed, not silently answer reworking")
		}
		if _, err := f.svc.Override(context.Background(), 1001, 7001, 42, "人工审查"); err == nil {
			t.Fatal("an unreadable rework budget must not grant an override")
		}
		if len(f.repo.overrides) != 0 {
			t.Fatal("failed budget read must not write an audit row")
		}
	})
	t.Run("round progression still uses the round-derived count", func(t *testing.T) {
		// The succeeded rework is already terminal when its revised draft is
		// submitted; a terminal-count budget here would REJECT the legitimate
		// re-judge round. resolveRound must keep CountReworks.
		f := newJudgmentFixture()
		f.seedCase(7001, 6, 10, strings.Repeat("b", 64))
		f.seedJudgment(1, 7001, 5, 1, design.VerdictFail)
		f.svc.SetReworkBudgetReader(&fakeReworkBudgetReader{terminal: 1})
		in := validSubmitInput()
		in.CaseVersionID = 6
		in.Round = 2
		in.Verdict = design.VerdictPass
		in.Findings = nil
		result, err := f.svc.SubmitJudgment(context.Background(), 1001, 10, 900, in)
		if err != nil {
			t.Fatalf("re-judge of a succeeded rework must be accepted: %v", err)
		}
		if result.Round != 2 {
			t.Fatalf("settled round = %d, want 2", result.Round)
		}
	})
}

// TestJudgmentServiceReworkDeathEscalatesWithBudgetTwo pins the SECOND half of
// the no-permanent-pending invariant (spec SC-E4, review fix round 1): the
// rework request key rework:<case>:<failedVersion> has no generation and the
// work-item store never revives a terminal row, so with
// design.max_rework_rounds >= 2 a single dead rework leaves budget
// (count = max(0,1) = 1 < 2) while no further rework can EVER be created for
// that fail. The budget test alone would keep the case reworking forever; the
// terminal-rework fact escalates it.
func TestJudgmentServiceReworkDeathEscalatesWithBudgetTwo(t *testing.T) {
	seed := func(f *judgmentFixture) {
		f.settings[maxReworkRoundsKey] = 2
		f.seedCase(7001, 5, 10, strings.Repeat("a", 64))
		f.seedJudgment(1, 7001, 5, 1, design.VerdictFail) // CountReworks 0, terminal 1, budget 2
	}

	t.Run("dead rework escalates even with budget left", func(t *testing.T) {
		f := newJudgmentFixture()
		seed(f)
		budget := &fakeReworkBudgetReader{terminal: 1, terminalForVersion: true}
		f.svc.SetReworkBudgetReader(budget)
		state, err := f.svc.ReviewStateFor(context.Background(), 1001, 7001, 5)
		if err != nil {
			t.Fatalf("review state: %v", err)
		}
		if state != design.ReviewStateEscalatedFail {
			t.Fatalf("review state = %q, want escalated_fail (the rework for this fail can never run again)", state)
		}
		if budget.gotTerminalVersion != 5 {
			t.Fatalf("terminal-rework read asked for version %d, want the current/failed version 5", budget.gotTerminalVersion)
		}
	})
	t.Run("override is reachable with budget left", func(t *testing.T) {
		f := newJudgmentFixture()
		seed(f)
		f.svc.SetReworkBudgetReader(&fakeReworkBudgetReader{terminal: 1, terminalForVersion: true})
		override, err := f.svc.Override(context.Background(), 1001, 7001, 42, "打回执行死亡，人工审查后接受")
		if err != nil {
			t.Fatalf("override with budget 2 and a dead rework: %v", err)
		}
		if override.Condition != OverrideConditionFailExhausted {
			t.Fatalf("condition = %q, want %q", override.Condition, OverrideConditionFailExhausted)
		}
		if override.CaseVersionID != 5 {
			t.Fatalf("override must bind the current version: %+v", override)
		}
	})
	t.Run("a live rework with budget left does not escalate", func(t *testing.T) {
		// Discriminating: no TERMINAL rework is bound to this fail (it is still
		// in flight, or the done one belongs to an earlier version), so the loop
		// proceeds and the case stays reworking until the revision lands.
		f := newJudgmentFixture()
		seed(f)
		f.svc.SetReworkBudgetReader(&fakeReworkBudgetReader{terminal: 1, terminalForVersion: false})
		state, err := f.svc.ReviewStateFor(context.Background(), 1001, 7001, 5)
		if err != nil || state != design.ReviewStateReworking {
			t.Fatalf("state=%q err=%v, want reworking (a live rework still owes the revision)", state, err)
		}
		if _, err := f.svc.Override(context.Background(), 1001, 7001, 42, "提前放行"); err == nil {
			t.Fatal("a live rework with budget left must not unlock the override")
		}
	})
	t.Run("terminal-rework read failure fails closed", func(t *testing.T) {
		f := newJudgmentFixture()
		seed(f)
		f.svc.SetReworkBudgetReader(&fakeReworkBudgetReader{terminal: 1, terminalErr: errors.New("work item store down")})
		if _, err := f.svc.ReviewStateFor(context.Background(), 1001, 7001, 5); err == nil {
			t.Fatal("an unreadable terminal-rework fact must fail closed")
		}
		if _, err := f.svc.Override(context.Background(), 1001, 7001, 42, "人工审查"); err == nil {
			t.Fatal("an unreadable terminal-rework fact must not grant an override")
		}
	})
	t.Run("nil reader keeps the budget-only behavior", func(t *testing.T) {
		f := newJudgmentFixture()
		seed(f)
		state, err := f.svc.ReviewStateFor(context.Background(), 1001, 7001, 5)
		if err != nil || state != design.ReviewStateReworking {
			t.Fatalf("state=%q err=%v, want reworking (degraded assembly, CountReworks alone)", state, err)
		}
	})
}

// TestJudgmentServiceReworkDoneNoRevisionEscalates pins DEV-E4 (review fix
// round 2): a rework can complete DONE without proposing a revised case version
// (the run ends without a verified completion). The latest fail is then still
// bound to the CURRENT version and the generation-less request key
// rework:<case>:<failedVersion> is occupied by a TERMINAL row, so with budget
// left no further rework can ever be created — the same permanent pending.
// The escalation branch must therefore ask for a TERMINAL rework (done OR dead
// OR failed-at-cap), not only a dead one.
func TestJudgmentServiceReworkDoneNoRevisionEscalates(t *testing.T) {
	seed := func(f *judgmentFixture) {
		f.settings[maxReworkRoundsKey] = 3
		f.seedCase(7001, 6, 10, strings.Repeat("b", 64))
		f.seedJudgment(1, 7001, 5, 1, design.VerdictFail)
		f.seedJudgment(2, 7001, 6, 2, design.VerdictFail) // CountReworks 1, terminal 2, budget 3
	}

	t.Run("done rework with no revision escalates", func(t *testing.T) {
		f := newJudgmentFixture()
		seed(f)
		budget := &fakeReworkBudgetReader{terminal: 2, terminalForVersion: true}
		f.svc.SetReworkBudgetReader(budget)
		state, err := f.svc.ReviewStateFor(context.Background(), 1001, 7001, 6)
		if err != nil {
			t.Fatalf("review state: %v", err)
		}
		if state != design.ReviewStateEscalatedFail {
			t.Fatalf("review state = %q, want escalated_fail (the rework finished without advancing the case)", state)
		}
		if budget.gotTerminalVersion != 6 {
			t.Fatalf("terminal-rework read asked for version %d, want the current/failed version 6", budget.gotTerminalVersion)
		}
	})
	t.Run("override is reachable", func(t *testing.T) {
		f := newJudgmentFixture()
		seed(f)
		f.svc.SetReworkBudgetReader(&fakeReworkBudgetReader{terminal: 2, terminalForVersion: true})
		override, err := f.svc.Override(context.Background(), 1001, 7001, 42, "打回未产出修订，人工决断")
		if err != nil {
			t.Fatalf("override after a no-revision done rework: %v", err)
		}
		if override.Condition != OverrideConditionFailExhausted || override.CaseVersionID != 6 {
			t.Fatalf("override = %+v, want fail_exhausted bound to version 6", override)
		}
	})
	t.Run("a live rework still leaves the case reworking", func(t *testing.T) {
		f := newJudgmentFixture()
		seed(f)
		f.svc.SetReworkBudgetReader(&fakeReworkBudgetReader{terminal: 2, terminalForVersion: false})
		state, err := f.svc.ReviewStateFor(context.Background(), 1001, 7001, 6)
		if err != nil || state != design.ReviewStateReworking {
			t.Fatalf("state=%q err=%v, want reworking (a live rework can still produce the revision)", state, err)
		}
	})
}
