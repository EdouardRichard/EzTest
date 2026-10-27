//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/postgres"
)

// openClarificationTestDB opens the migrated test database and the clarification,
// requirement and version repositories on it.
func openClarificationTestDB(t *testing.T) (*postgres.ClarificationRepository, *postgres.RequirementRepository, *postgres.VersionRepository) {
	t.Helper()
	pool := testutil.OpenTestDB(t)
	return postgres.NewClarificationRepository(pool),
		postgres.NewRequirementRepository(pool),
		postgres.NewVersionRepository(pool)
}

// insertQuestion stores one question through the repository.
func insertQuestion(t *testing.T, repo *postgres.ClarificationRepository, question domain.Question) domain.Question {
	t.Helper()
	if question.Status == "" {
		question.Status = domain.QuestionOpen
	}
	if question.Source == "" {
		question.Source = domain.SourceAI
	}
	if question.AnsweredVia == "" {
		question.AnsweredVia = domain.AnsweredViaUI
	}
	if err := repo.CreateQuestion(context.Background(), &question); err != nil {
		t.Fatalf("create question %d: %v", question.ID, err)
	}
	return question
}

// TestClarificationSessionRepository covers the single-active-session constraint,
// completion and context-summary updates against the real schema.
func TestClarificationSessionRepository(t *testing.T) {
	ctx := context.Background()
	clar, _, versions := openClarificationTestDB(t)
	version := insertVersion(t, versions, domain.Version{ID: 1001, Name: "Order v2", Status: domain.VersionClarifying, CreatedBy: 7, UpdatedBy: 7})
	otherVersion := insertVersion(t, versions, domain.Version{ID: 1002, Name: "Other", Status: domain.VersionClarifying, CreatedBy: 7, UpdatedBy: 7})

	session := &domain.ClarificationSession{
		ID: 9001, VersionID: version.ID, Status: domain.SessionActive, StartedBy: 42,
	}
	if err := clar.CreateSession(ctx, session); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if session.StartedAt.IsZero() {
		t.Fatalf("session started_at not populated: %+v", session)
	}
	active, err := clar.GetActiveSession(ctx, version.ID)
	if err != nil {
		t.Fatalf("GetActiveSession: %v", err)
	}
	if active == nil || active.ID != session.ID || active.Status != domain.SessionActive ||
		active.StartedBy != 42 || !active.StartedAt.Equal(session.StartedAt) ||
		!active.StartedAt.Equal(session.StartedAt) {
		t.Fatalf("active session = %+v", active)
	}
	if missing, err := clar.GetActiveSession(ctx, otherVersion.ID); err != nil || missing != nil {
		t.Fatalf("active session of another version = %+v/%v", missing, err)
	}

	// The partial unique index allows only one active session per version.
	second := &domain.ClarificationSession{ID: 9002, VersionID: version.ID, Status: domain.SessionActive, StartedBy: 43}
	assertRequirementKind(t, clar.CreateSession(ctx, second), domain.KindConflict, "clarification_session_active")

	// A missing version is a foreign-key failure mapped to a missing version, and
	// a duplicate explicit id is a uniqueness conflict.
	orphan := &domain.ClarificationSession{ID: 9003, VersionID: 999999, Status: domain.SessionActive, StartedBy: 42}
	assertRequirementKind(t, clar.CreateSession(ctx, orphan), domain.KindNotFound, "not_found")
	duplicate := &domain.ClarificationSession{ID: 9001, VersionID: otherVersion.ID, Status: domain.SessionActive, StartedBy: 42}
	assertRequirementKind(t, clar.CreateSession(ctx, duplicate), domain.KindConflict, "duplicate_key")

	// The active session's context summary is updated with a timestamp.
	if err := clar.UpdateContextSummary(ctx, version.ID, "the user wants coupons", 43); err != nil {
		t.Fatalf("UpdateContextSummary: %v", err)
	}
	stored, err := clar.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if stored.ContextSummary != "the user wants coupons" || stored.SummaryUpdatedAt == nil {
		t.Fatalf("context summary = %+v", stored)
	}

	// Completion is a status compare-and-swap.
	completed, err := clar.CompleteSession(ctx, session.ID, 44)
	if err != nil {
		t.Fatalf("CompleteSession: %v", err)
	}
	if completed.Status != domain.SessionCompleted || completed.EndedAt == nil {
		t.Fatalf("completed session = %+v", completed)
	}
	assertRequirementKind(t, mustCompleteSession(clar, ctx, session.ID, 44), domain.KindStateConflict, "clarification_session_not_active")
	assertRequirementKind(t, clar.UpdateContextSummary(ctx, version.ID, "late", 44), domain.KindStateConflict, "clarification_session_not_active")
	if active, err := clar.GetActiveSession(ctx, version.ID); err != nil || active != nil {
		t.Fatalf("active session after completion = %+v/%v", active, err)
	}

	// A new session may start once the previous one completed.
	third := &domain.ClarificationSession{ID: 9004, VersionID: version.ID, Status: domain.SessionActive, StartedBy: 45}
	if err := clar.CreateSession(ctx, third); err != nil {
		t.Fatalf("CreateSession after completion: %v", err)
	}
	if err := clar.UpdateContextSummary(ctx, version.ID, "second summary", 45); err != nil {
		t.Fatalf("UpdateContextSummary second: %v", err)
	}
	if _, err := clar.GetSession(ctx, 999999); err == nil {
		t.Fatal("GetSession accepted a missing id")
	}
}

// mustCompleteSession runs a session completion and returns its error.
func mustCompleteSession(repo *postgres.ClarificationRepository, ctx context.Context, id, actor int64) error {
	_, err := repo.CompleteSession(ctx, id, actor)
	return err
}

// TestClarificationQuestionRepository covers question create/get/list/update and
// the open-question transitions against the real schema.
func TestClarificationQuestionRepository(t *testing.T) {
	ctx := context.Background()
	clar, requirements, versions := openClarificationTestDB(t)
	version := insertVersion(t, versions, domain.Version{ID: 1001, Name: "Order v2", Status: domain.VersionClarifying, CreatedBy: 7, UpdatedBy: 7})
	otherVersion := insertVersion(t, versions, domain.Version{ID: 1002, Name: "Other", Status: domain.VersionClarifying, CreatedBy: 7, UpdatedBy: 7})
	requirement := insertRequirement(t, requirements, domain.Requirement{
		ID: 2001, VersionID: version.ID, Title: "Checkout flow", Priority: domain.PriorityShould,
		Revision: 1, CreatedBy: 7, UpdatedBy: 7,
	})
	foreign := insertRequirement(t, requirements, domain.Requirement{
		ID: 2002, VersionID: otherVersion.ID, Title: "Foreign", Priority: domain.PriorityShould,
		Revision: 1, CreatedBy: 7, UpdatedBy: 7,
	})

	withRequirement := insertQuestion(t, clar, domain.Question{
		ID: 9101, VersionID: version.ID, RequirementID: &requirement.ID,
		Question: "Which coupons apply?", Status: domain.QuestionOpen,
		Source: domain.SourceAI, AnsweredVia: domain.AnsweredViaUI, CreatedBy: 42,
	})
	versionLevel := insertQuestion(t, clar, domain.Question{
		ID: 9102, VersionID: version.ID, Question: "Which locales?",
		Status: domain.QuestionOpen, Source: domain.SourceUser, CreatedBy: 43,
	})
	if withRequirement.CreatedAt.IsZero() || withRequirement.UpdatedAt.IsZero() {
		t.Fatalf("question timestamps not populated: %+v", withRequirement)
	}

	loaded, err := clar.GetQuestion(ctx, withRequirement.ID)
	if err != nil {
		t.Fatalf("GetQuestion: %v", err)
	}
	if loaded.Question != "Which coupons apply?" || loaded.RequirementID == nil ||
		*loaded.RequirementID != requirement.ID || loaded.Status != domain.QuestionOpen ||
		loaded.Source != domain.SourceAI || loaded.AnsweredVia != domain.AnsweredViaUI ||
		loaded.CreatedBy != 42 {
		t.Fatalf("loaded question = %+v", loaded)
	}
	if _, err := clar.GetQuestion(ctx, 999999); err == nil {
		t.Fatal("GetQuestion accepted a missing id")
	}

	// Listing is ordered by id and filters by status and requirement.
	all, more, err := clar.ListQuestions(ctx, domain.QuestionFilter{VersionID: version.ID, Page: domain.Page{Limit: 10}})
	if err != nil || more || len(all) != 2 || all[0].ID != 9101 || all[1].ID != 9102 {
		t.Fatalf("all questions = %+v more=%v err=%v", all, more, err)
	}
	byRequirement, _, err := clar.ListQuestions(ctx, domain.QuestionFilter{
		VersionID: version.ID, RequirementID: &requirement.ID, Page: domain.Page{Limit: 10},
	})
	if err != nil || len(byRequirement) != 1 || byRequirement[0].ID != 9101 {
		t.Fatalf("requirement questions = %+v err=%v", byRequirement, err)
	}
	firstPage, more, err := clar.ListQuestions(ctx, domain.QuestionFilter{VersionID: version.ID, Page: domain.Page{Limit: 1}})
	if err != nil || !more || len(firstPage) != 1 || firstPage[0].ID != 9101 {
		t.Fatalf("first page = %+v more=%v err=%v", firstPage, more, err)
	}
	nextPage, more, err := clar.ListQuestions(ctx, domain.QuestionFilter{
		VersionID: version.ID,
		Page:      domain.Page{Limit: 1, Cursor: platform.EncodeCursor(firstPage[0].ID)},
	})
	if err != nil || more || len(nextPage) != 1 || nextPage[0].ID != 9102 {
		t.Fatalf("second page = %+v more=%v err=%v", nextPage, more, err)
	}
	if _, _, err := clar.ListQuestions(ctx, domain.QuestionFilter{VersionID: version.ID, Page: domain.Page{}}); err == nil {
		t.Fatal("ListQuestions accepted a zero limit")
	}

	// Answering is a status compare-and-swap.
	answered := *loaded
	answered.Status = domain.QuestionAnswered
	answered.Answer = "All coupons"
	answered.AnsweredVia = domain.AnsweredViaAgent
	answered.AnsweredBy = ptrInt64(43)
	answeredAt := time.Now().UTC()
	answered.AnsweredAt = &answeredAt
	if err := clar.UpdateQuestion(ctx, &answered, domain.QuestionOpen); err != nil {
		t.Fatalf("UpdateQuestion answer: %v", err)
	}
	reloaded, err := clar.GetQuestion(ctx, loaded.ID)
	if err != nil {
		t.Fatalf("GetQuestion after answer: %v", err)
	}
	if reloaded.Status != domain.QuestionAnswered || reloaded.Answer != "All coupons" ||
		reloaded.AnsweredVia != domain.AnsweredViaAgent || reloaded.AnsweredBy == nil ||
		*reloaded.AnsweredBy != 43 || reloaded.AnsweredAt == nil {
		t.Fatalf("answered question = %+v", reloaded)
	}
	assertRequirementKind(t, clar.UpdateQuestion(ctx, &answered, domain.QuestionOpen),
		domain.KindStateConflict, "clarification_question_state_conflict")

	// The status CHECK constraint backstops answered-without-answer rows.
	invalid := *reloaded
	invalid.Status = domain.QuestionAnswered
	invalid.Answer = ""
	assertRequirementKind(t, clar.UpdateQuestion(ctx, &invalid, domain.QuestionAnswered),
		domain.KindInvalid, "invalid_answer")

	// Reopening clears the answer and the audit fields.
	reopened := *reloaded
	reopened.Status = domain.QuestionOpen
	reopened.Answer = ""
	reopened.AnsweredBy = nil
	reopened.AnsweredAt = nil
	if err := clar.UpdateQuestion(ctx, &reopened, domain.QuestionAnswered); err != nil {
		t.Fatalf("UpdateQuestion reopen: %v", err)
	}
	reloaded, err = clar.GetQuestion(ctx, loaded.ID)
	if err != nil {
		t.Fatalf("GetQuestion after reopen: %v", err)
	}
	if reloaded.Status != domain.QuestionOpen || reloaded.Answer != "" ||
		reloaded.AnsweredBy != nil || reloaded.AnsweredAt != nil {
		t.Fatalf("reopened question = %+v", reloaded)
	}

	// Version-level questions round-trip a nil requirement and nil session.
	if versionLevel.RequirementID != nil || versionLevel.SessionID != nil {
		t.Fatalf("version-level question links = %+v", versionLevel)
	}

	// Foreign keys and uniqueness are mapped to domain errors.
	orphan := &domain.Question{
		ID: 9103, VersionID: 999999, Question: "Orphan", Status: domain.QuestionOpen,
		Source: domain.SourceUser, AnsweredVia: domain.AnsweredViaUI, CreatedBy: 42,
	}
	assertRequirementKind(t, clar.CreateQuestion(ctx, orphan), domain.KindNotFound, "not_found")
	orphanRequirement := &domain.Question{
		ID: 9104, VersionID: version.ID, RequirementID: ptrInt64(999999),
		Question: "Orphan requirement", Status: domain.QuestionOpen,
		Source: domain.SourceUser, AnsweredVia: domain.AnsweredViaUI, CreatedBy: 42,
	}
	assertRequirementKind(t, clar.CreateQuestion(ctx, orphanRequirement), domain.KindNotFound, "not_found")
	duplicate := &domain.Question{
		ID: 9101, VersionID: version.ID, Question: "Duplicate id", Status: domain.QuestionOpen,
		Source: domain.SourceUser, AnsweredVia: domain.AnsweredViaUI, CreatedBy: 42,
	}
	assertRequirementKind(t, clar.CreateQuestion(ctx, duplicate), domain.KindConflict, "duplicate_key")
	invalidStatus := &domain.Question{
		ID: 9105, VersionID: version.ID, Question: "Bad status", Status: "pending",
		Source: domain.SourceUser, AnsweredVia: domain.AnsweredViaUI, CreatedBy: 42,
	}
	assertRequirementKind(t, clar.CreateQuestion(ctx, invalidStatus), domain.KindInvalid, "invalid_status")
	assertRequirementKind(t, mustUpdateQuestion(clar, ctx, &domain.Question{ID: 999999}, domain.QuestionOpen),
		domain.KindNotFound, "not_found")

	// The foreign requirement is visible to the same query only in its version.
	foreignQuestions, _, err := clar.ListQuestions(ctx, domain.QuestionFilter{
		VersionID: otherVersion.ID, RequirementID: &foreign.ID, Page: domain.Page{Limit: 10},
	})
	if err != nil || len(foreignQuestions) != 0 {
		t.Fatalf("foreign questions = %+v err=%v", foreignQuestions, err)
	}
}

// mustUpdateQuestion runs a question update and returns its error.
func mustUpdateQuestion(repo *postgres.ClarificationRepository, ctx context.Context, question *domain.Question, fromStatus string) error {
	return repo.UpdateQuestion(ctx, question, fromStatus)
}

// TestClarificationDecisionRepository covers decision creation, the terminal
// transitions and the same-transaction answer of a linked open question.
func TestClarificationDecisionRepository(t *testing.T) {
	ctx := context.Background()
	clar, requirements, versions := openClarificationTestDB(t)
	version := insertVersion(t, versions, domain.Version{ID: 1001, Name: "Order v2", Status: domain.VersionClarifying, CreatedBy: 7, UpdatedBy: 7})
	requirement := insertRequirement(t, requirements, domain.Requirement{
		ID: 2001, VersionID: version.ID, Title: "Checkout flow", Priority: domain.PriorityShould,
		Revision: 1, CreatedBy: 7, UpdatedBy: 7,
	})
	open := insertQuestion(t, clar, domain.Question{
		ID: 9101, VersionID: version.ID, RequirementID: &requirement.ID,
		Question: "Which coupons?", Status: domain.QuestionOpen,
		Source: domain.SourceAI, AnsweredVia: domain.AnsweredViaUI, CreatedBy: 42,
	})

	proposed := &domain.Decision{
		ID: 9201, VersionID: version.ID, RequirementID: &requirement.ID, QuestionID: &open.ID,
		Topic: "Coupons", Decision: "Apply all coupons", Rationale: "user said so",
		Status: domain.DecisionProposed, Source: domain.SourceAI, ProposedBy: 42,
	}
	if err := clar.CreateDecision(ctx, proposed); err != nil {
		t.Fatalf("CreateDecision: %v", err)
	}
	if proposed.CreatedAt.IsZero() || proposed.UpdatedAt.IsZero() {
		t.Fatalf("decision timestamps not populated: %+v", proposed)
	}
	storedOpen, err := clar.GetQuestion(ctx, open.ID)
	if err != nil || storedOpen.Status != domain.QuestionOpen {
		t.Fatalf("proposal answered the question: %+v/%v", storedOpen, err)
	}

	accepted := *proposed
	accepted.Status = domain.DecisionAccepted
	accepted.DecidedBy = ptrInt64(43)
	decidedAt := time.Now().UTC()
	accepted.DecidedAt = &decidedAt
	if err := clar.UpdateDecision(ctx, &accepted, domain.DecisionProposed); err != nil {
		t.Fatalf("UpdateDecision accept: %v", err)
	}
	storedAccepted, err := clar.GetDecision(ctx, proposed.ID)
	if err != nil {
		t.Fatalf("GetDecision: %v", err)
	}
	if storedAccepted.Status != domain.DecisionAccepted || storedAccepted.DecidedBy == nil ||
		*storedAccepted.DecidedBy != 43 || storedAccepted.DecidedAt == nil {
		t.Fatalf("accepted decision = %+v", storedAccepted)
	}
	// The linked question is answered in the same transaction with the decision
	// as the answer and the decider as the answerer.
	storedAnswered, err := clar.GetQuestion(ctx, open.ID)
	if err != nil {
		t.Fatalf("GetQuestion after accept: %v", err)
	}
	if storedAnswered.Status != domain.QuestionAnswered ||
		storedAnswered.Answer != "Apply all coupons" ||
		storedAnswered.AnsweredVia != domain.AnsweredViaUI ||
		storedAnswered.AnsweredBy == nil || *storedAnswered.AnsweredBy != 43 ||
		storedAnswered.AnsweredAt == nil {
		t.Fatalf("linked question after accept = %+v", storedAnswered)
	}
	// Accepted is terminal: a second transition from proposed never writes.
	assertRequirementKind(t, clar.UpdateDecision(ctx, &accepted, domain.DecisionProposed),
		domain.KindStateConflict, "decision_state_conflict")

	// Rejecting a proposed decision leaves an open linked question open.
	open2 := insertQuestion(t, clar, domain.Question{
		ID: 9102, VersionID: version.ID, RequirementID: &requirement.ID,
		Question: "Which tax rate?", Status: domain.QuestionOpen,
		Source: domain.SourceAI, AnsweredVia: domain.AnsweredViaUI, CreatedBy: 42,
	})
	proposal2 := &domain.Decision{
		ID: 9202, VersionID: version.ID, RequirementID: &requirement.ID, QuestionID: &open2.ID,
		Topic: "Tax", Decision: "Use 20%", Status: domain.DecisionProposed,
		Source: domain.SourceAI, ProposedBy: 42,
	}
	if err := clar.CreateDecision(ctx, proposal2); err != nil {
		t.Fatalf("CreateDecision second: %v", err)
	}
	rejected := *proposal2
	rejected.Status = domain.DecisionRejected
	rejected.DecidedBy = ptrInt64(43)
	rejected.DecidedAt = &decidedAt
	if err := clar.UpdateDecision(ctx, &rejected, domain.DecisionProposed); err != nil {
		t.Fatalf("UpdateDecision reject: %v", err)
	}
	storedRejected, err := clar.GetDecision(ctx, proposal2.ID)
	if err != nil || storedRejected.Status != domain.DecisionRejected {
		t.Fatalf("rejected decision = %+v/%v", storedRejected, err)
	}
	storedOpen2, err := clar.GetQuestion(ctx, open2.ID)
	if err != nil || storedOpen2.Status != domain.QuestionOpen || storedOpen2.Answer != "" {
		t.Fatalf("rejection answered the question: %+v/%v", storedOpen2, err)
	}

	// A human-created accepted decision answers the linked open question in the
	// same insert transaction.
	open3 := insertQuestion(t, clar, domain.Question{
		ID: 9103, VersionID: version.ID, Question: "Which currency?", Status: domain.QuestionOpen,
		Source: domain.SourceAI, AnsweredVia: domain.AnsweredViaUI, CreatedBy: 42,
	})
	human := &domain.Decision{
		ID: 9203, VersionID: version.ID, QuestionID: &open3.ID,
		Topic: "Currency", Decision: "EUR", Status: domain.DecisionAccepted,
		Source: domain.SourceUser, ProposedBy: 44, DecidedBy: ptrInt64(44), DecidedAt: &decidedAt,
	}
	if err := clar.CreateDecision(ctx, human); err != nil {
		t.Fatalf("CreateDecision human: %v", err)
	}
	storedHumanQuestion, err := clar.GetQuestion(ctx, open3.ID)
	if err != nil || storedHumanQuestion.Status != domain.QuestionAnswered || storedHumanQuestion.Answer != "EUR" {
		t.Fatalf("human decision question = %+v/%v", storedHumanQuestion, err)
	}

	// Dismissed questions are not re-answered by an accepted decision.
	open4 := insertQuestion(t, clar, domain.Question{
		ID: 9104, VersionID: version.ID, Question: "Which cms?", Status: domain.QuestionOpen,
		Source: domain.SourceAI, AnsweredVia: domain.AnsweredViaUI, CreatedBy: 42,
	})
	dismissed := open4
	dismissed.Status = domain.QuestionDismissed
	if err := clar.UpdateQuestion(ctx, &dismissed, domain.QuestionOpen); err != nil {
		t.Fatalf("dismiss question: %v", err)
	}
	proposal4 := &domain.Decision{
		ID: 9204, VersionID: version.ID, QuestionID: &open4.ID,
		Topic: "CMS", Decision: "None", Status: domain.DecisionProposed,
		Source: domain.SourceAI, ProposedBy: 42,
	}
	if err := clar.CreateDecision(ctx, proposal4); err != nil {
		t.Fatalf("CreateDecision fourth: %v", err)
	}
	accepted4 := *proposal4
	accepted4.Status = domain.DecisionAccepted
	accepted4.DecidedBy = ptrInt64(43)
	accepted4.DecidedAt = &decidedAt
	if err := clar.UpdateDecision(ctx, &accepted4, domain.DecisionProposed); err != nil {
		t.Fatalf("UpdateDecision fourth: %v", err)
	}
	storedDismissed, err := clar.GetQuestion(ctx, open4.ID)
	if err != nil || storedDismissed.Status != domain.QuestionDismissed || storedDismissed.Answer != "" {
		t.Fatalf("dismissed question changed on accept: %+v/%v", storedDismissed, err)
	}

	// Listing filters by status and is ordered by id.
	rejectedList, more, err := clar.ListDecisions(ctx, domain.DecisionFilter{
		VersionID: version.ID, Status: domain.DecisionRejected, Page: domain.Page{Limit: 10},
	})
	if err != nil || more || len(rejectedList) != 1 || rejectedList[0].ID != proposal2.ID {
		t.Fatalf("rejected decisions = %+v more=%v err=%v", rejectedList, more, err)
	}
	all, more, err := clar.ListDecisions(ctx, domain.DecisionFilter{VersionID: version.ID, Page: domain.Page{Limit: 2}})
	if err != nil || !more || len(all) != 2 || all[0].ID != 9201 || all[1].ID != 9202 {
		t.Fatalf("decision page = %+v more=%v err=%v", all, more, err)
	}
	if _, _, err := clar.ListDecisions(ctx, domain.DecisionFilter{VersionID: version.ID, Page: domain.Page{}}); err == nil {
		t.Fatal("ListDecisions accepted a zero limit")
	}

	// Foreign keys and uniqueness are mapped to domain errors.
	orphanQuestion := &domain.Decision{
		ID: 9205, VersionID: version.ID, QuestionID: ptrInt64(999999),
		Topic: "Orphan", Decision: "x", Status: domain.DecisionProposed,
		Source: domain.SourceAI, ProposedBy: 42,
	}
	assertRequirementKind(t, clar.CreateDecision(ctx, orphanQuestion), domain.KindNotFound, "not_found")
	duplicate := &domain.Decision{
		ID: 9201, VersionID: version.ID, Topic: "Duplicate", Decision: "x",
		Status: domain.DecisionProposed, Source: domain.SourceAI, ProposedBy: 42,
	}
	assertRequirementKind(t, clar.CreateDecision(ctx, duplicate), domain.KindConflict, "duplicate_key")
	if _, err := clar.GetDecision(ctx, 999999); err == nil {
		t.Fatal("GetDecision accepted a missing id")
	}
	assertRequirementKind(t, clar.UpdateDecision(ctx, &domain.Decision{ID: 999999}, domain.DecisionProposed),
		domain.KindNotFound, "not_found")
}
