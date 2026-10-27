package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/filestore"
)

// toolGit is a GitReader double that records the code-tool calls and mimics the
// gitread adapter's bounds: the include/exclude globs are applied and the max cap
// is enforced after filtering.
type toolGit struct {
	commit  string
	entries []application.CodeEntry
	content string
	size    int64
	readErr error
	treeErr error

	treeMax    int
	treeCalled int
	treePrefix string
	readOffset int
	readMax    int
	readPath   string
	readCalled int
}

// ResolveCommit returns the configured commit.
func (g *toolGit) ResolveCommit(context.Context, string, string) (string, error) {
	if g.commit == "" {
		return testCommit, nil
	}
	return g.commit, nil
}

// Tree returns the glob-filtered entries up to max.
func (g *toolGit) Tree(_ context.Context, _, _ string, include, exclude []string, max int) ([]application.CodeEntry, error) {
	g.treeCalled++
	g.treeMax = max
	if g.treeErr != nil {
		return nil, g.treeErr
	}
	return g.treeWindow("", include, exclude, max), nil
}

// TreePrefix filters by prefix and globs before max, like the real adapter.
func (g *toolGit) TreePrefix(_ context.Context, _, _, prefix string, include, exclude []string, max int) ([]application.CodeEntry, error) {
	g.treeCalled++
	g.treeMax = max
	g.treePrefix = prefix
	if g.treeErr != nil {
		return nil, g.treeErr
	}
	return g.treeWindow(prefix, include, exclude, max), nil
}

// treeWindow applies the prefix and globs first, then the max cap.
func (g *toolGit) treeWindow(prefix string, include, exclude []string, max int) []application.CodeEntry {
	entries := make([]application.CodeEntry, 0, len(g.entries))
	for _, entry := range g.entries {
		if prefix != "" && !strings.HasPrefix(entry.Path, prefix+"/") {
			continue
		}
		if !fakePathIncluded(entry.Path, include, exclude) {
			continue
		}
		entries = append(entries, entry)
		if max > 0 && len(entries) >= max {
			break
		}
	}
	return entries
}

// fakePathIncluded applies the test glob subset the same way the real reader
// does: an exact name matches the entry or a descendant and prefix/** matches
// descendants.
func fakePathIncluded(entryPath string, include, exclude []string) bool {
	if len(include) > 0 && !fakeMatchesAny(entryPath, include) {
		return false
	}
	return !fakeMatchesAny(entryPath, exclude)
}

// fakeMatchesAny reports whether p matches at least one test glob.
func fakeMatchesAny(p string, globs []string) bool {
	for _, glob := range globs {
		glob = strings.TrimSpace(glob)
		if glob == "" {
			continue
		}
		if strings.HasSuffix(glob, "/**") {
			if strings.HasPrefix(p, strings.TrimSuffix(glob, "/**")+"/") {
				return true
			}
			continue
		}
		if p == glob || strings.HasPrefix(p, glob+"/") {
			return true
		}
	}
	return false
}

// Read returns the configured content.
func (g *toolGit) Read(_ context.Context, _, _, path string, maxBytes int) (string, error) {
	g.readCalled++
	g.readPath = path
	g.readMax = maxBytes
	if g.readErr != nil {
		return "", g.readErr
	}
	return g.window(0, maxBytes), nil
}

// ReadRange returns the configured window and blob size.
func (g *toolGit) ReadRange(_ context.Context, _, _, path string, offset, maxBytes int) (string, int64, error) {
	g.readCalled++
	g.readPath = path
	g.readOffset = offset
	g.readMax = maxBytes
	if g.readErr != nil {
		return "", 0, g.readErr
	}
	return g.window(offset, maxBytes), g.size, nil
}

// window slices the configured content the way an honest bounded reader does.
func (g *toolGit) window(offset, maxBytes int) string {
	if offset >= len(g.content) {
		return ""
	}
	end := len(g.content)
	if maxBytes > 0 && offset+maxBytes < end {
		end = offset + maxBytes
	}
	return g.content[offset:end]
}

// countingRequirementLister counts List calls so the context test can assert
// version.context_get loads the requirement slice once.
type countingRequirementLister struct {
	*fakeRequirementRepo
	listCalls int
}

// List counts the call and delegates to the in-memory repository.
func (r *countingRequirementLister) List(ctx context.Context, versionID int64, status string, page domain.Page) ([]domain.Requirement, bool, error) {
	r.listCalls++
	return r.fakeRequirementRepo.List(ctx, versionID, status, page)
}

// toolFixture wires the real AgentToolService to the shared in-memory fakes and
// a real filesystem attachment store.
type toolFixture struct {
	t                 *testing.T
	service           *application.AgentToolService
	versions          *fakeVersionRepo
	requirements      *fakeRequirementRepo
	requirementLister *countingRequirementLister
	clarifications    *fakeClarificationRepo
	attachments       *application.AttachmentService
	events            *fakeEvents
	git               *toolGit
	resources         *fakeCatalog
	ids               *fixedIDs
	root              string
}

// newToolFixture returns a fixture with the first generated id 5000.
func newToolFixture(t *testing.T) *toolFixture {
	t.Helper()
	f := &toolFixture{
		t:              t,
		versions:       newFakeVersionRepo(),
		requirements:   newFakeRequirementRepo(),
		clarifications: newFakeClarificationRepo(),
		events:         &fakeEvents{},
		git:            &toolGit{commit: testCommit},
		resources:      newFakeCatalog(),
		ids:            &fixedIDs{next: 5000},
		root:           filepath.Join(t.TempDir(), "version-attachments"),
	}
	f.requirementLister = &countingRequirementLister{fakeRequirementRepo: f.requirements}
	// The shared catalog's repository defaults to src/** include globs; the code
	// tool fixtures below are explicit about their globs so every tree entry
	// stays visible unless a test narrows it again.
	repository := f.resources.repos[9]
	repository.IncludeGlobs = nil
	repository.ExcludeGlobs = nil
	f.resources.repos[9] = repository
	requirementService := application.NewRequirementService(
		f.requirements, f.versions, f.events,
		application.WithRequirementIDGenerator(f.ids),
	)
	relationService := application.NewRelationService(
		f.requirements,
		application.WithRelationIDGenerator(f.ids),
		application.WithRelationEventPublisher(f.events),
	)
	clarificationService := application.NewClarificationService(
		f.clarifications, f.requirements, f.versions, newFakeAgentSessions(), f.events,
		application.WithClarificationIDGenerator(f.ids),
	)
	f.attachments = application.NewAttachmentService(
		f.requirements, filestore.New(f.root), application.DefaultLimits(), f.events,
		application.WithAttachmentIDGenerator(f.ids),
	)
	f.service = application.NewAgentToolService(application.AgentToolServiceDeps{
		Versions:             f.versions,
		Requirements:         f.requirementLister,
		Clarifications:       f.clarifications,
		RequirementService:   requirementService,
		RelationService:      relationService,
		ClarificationService: clarificationService,
		AttachmentService:    f.attachments,
		Resources:            f.resources,
		Git:                  f.git,
	})
	return f
}

// seedVersion stores one version.
func (f *toolFixture) seedVersion(id int64, status string) domain.Version {
	f.t.Helper()
	return f.versions.seed(domain.Version{
		ID: id, Name: "Order v2", Status: status, Revision: 2, CreatedBy: 7, UpdatedBy: 7,
	})
}

// bindCode binds repository 9 at testCommit to version id.
func (f *toolFixture) bindCode(id int64) {
	f.t.Helper()
	version := f.versions.versions[id]
	repositoryID := int64(9)
	branch := "main"
	commit := testCommit
	version.CodeRepositoryID = &repositoryID
	version.CodeBranch = &branch
	version.CodeBaselineCommit = &commit
	f.versions.versions[id] = version
}

// seedRequirement stores a requirement directly in the fake repository.
func (f *toolFixture) seedRequirement(versionID, id int64, title, status string) domain.Requirement {
	f.t.Helper()
	requirement := domain.Requirement{
		ID: id, VersionID: versionID, Title: title, Description: "desc " + title,
		AcceptanceCriteria: "accept " + title, Priority: domain.PriorityMust,
		Status: status, Source: domain.SourceUser, LastUpdateSource: domain.SourceUser,
		Revision: 1, CreatedBy: 7, UpdatedBy: 7, CreatedAt: testNow, UpdatedAt: testNow,
	}
	f.requirements.requirements[id] = requirement
	return requirement
}

// seedQuestion stores one question directly in the fake repository.
func (f *toolFixture) seedQuestion(versionID, id int64, requirementID *int64, text, status string) domain.Question {
	f.t.Helper()
	question := domain.Question{
		ID: id, VersionID: versionID, RequirementID: requirementID,
		Question: text, Status: status, Source: domain.SourceAI,
		AnsweredVia: domain.AnsweredViaUI, CreatedBy: 7, CreatedAt: testNow, UpdatedAt: testNow,
	}
	if status == domain.QuestionAnswered {
		question.Answer = "answered " + text
	}
	f.clarifications.questions[id] = question
	return question
}

// seedDecision stores one decision directly in the fake repository.
func (f *toolFixture) seedDecision(versionID, id int64, requirementID *int64, status string) domain.Decision {
	f.t.Helper()
	decision := domain.Decision{
		ID: id, VersionID: versionID, RequirementID: requirementID,
		Topic: "topic", Decision: "decision", Rationale: "rationale",
		Status: status, Source: domain.SourceAI, ProposedBy: 7, CreatedAt: testNow, UpdatedAt: testNow,
	}
	f.clarifications.decisions[id] = decision
	return decision
}

// seedSession stores one active clarification session.
func (f *toolFixture) seedSession(versionID, id int64, summary string) domain.ClarificationSession {
	f.t.Helper()
	session := domain.ClarificationSession{
		ID: id, VersionID: versionID, Status: domain.SessionActive,
		ContextSummary: summary, StartedBy: 7, StartedAt: testNow,
	}
	f.clarifications.sessions[id] = session
	return session
}

// seedAttachment writes one attachment file and metadata row.
func (f *toolFixture) seedAttachment(versionID, requirementID, id int64, filename, mimeType, kind, content string) domain.Attachment {
	f.t.Helper()
	storagePath := filepath.ToSlash(filepath.Join(
		"100", filename))
	absolute := filepath.Join(f.root, filepath.FromSlash(storagePath))
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		f.t.Fatalf("mkdir attachment: %v", err)
	}
	if err := os.WriteFile(absolute, []byte(content), 0o600); err != nil {
		f.t.Fatalf("write attachment: %v", err)
	}
	attachment := domain.Attachment{
		ID: id, VersionID: versionID, RequirementID: requirementID,
		Kind: kind, Filename: filename, MIMEType: mimeType,
		SizeBytes: int64(len(content)), SHA256: sha256Hex([]byte(content)),
		StoragePath: storagePath, CreatedBy: 7, CreatedAt: testNow,
	}
	f.requirements.attachments[id] = attachment
	return attachment
}

// toolOutcome decodes a successful tool JSON payload into out.
func toolOutcome(t *testing.T, content string, isError bool, err error, out any) {
	t.Helper()
	if err != nil {
		t.Fatalf("tool returned infrastructure error: %v", err)
	}
	if isError {
		t.Fatalf("tool reported a business error: %s", content)
	}
	if err := json.Unmarshal([]byte(content), out); err != nil {
		t.Fatalf("tool content %q: %v", content, err)
	}
}

// toolBusiness asserts a business failure with the wanted substring.
func toolBusiness(t *testing.T, content string, isError bool, err error, want string) {
	t.Helper()
	if err != nil {
		t.Fatalf("tool returned infrastructure error %v, want a correctable business error", err)
	}
	if !isError {
		t.Fatalf("tool reported success %q, want a business error containing %q", content, want)
	}
	if !strings.Contains(content, want) {
		t.Fatalf("business error %q does not contain %q", content, want)
	}
}

// TestToolServiceContextGet pins the bounded context projection consumed by all
// three P3 profiles.
func TestToolServiceContextGet(t *testing.T) {
	ctx := context.Background()
	f := newToolFixture(t)
	version := f.seedVersion(100, domain.VersionClarifying)
	f.versions.counts[version.ID] = domain.VersionCounts{
		RequirementCount: 2, ClarifiedCount: 1, OpenQuestionCount: 1,
	}
	draft := f.seedRequirement(version.ID, 501, "Checkout", domain.RequirementDraft)
	clarified := f.seedRequirement(version.ID, 502, "Refunds", domain.RequirementClarified)
	f.requirements.openQuestions[draft.ID] = 1
	f.requirements.relations[601] = domain.Relation{
		ID: 601, VersionID: version.ID, FromRequirementID: draft.ID, ToRequirementID: clarified.ID,
		RelationType: domain.RelationDependsOn, Description: "needs refunds", Source: domain.SourceAI,
	}
	open := f.seedQuestion(version.ID, 701, &draft.ID, "What is the refund window?", domain.QuestionOpen)
	f.seedQuestion(version.ID, 702, &clarified.ID, "Which currencies?", domain.QuestionAnswered)
	f.seedQuestion(version.ID, 703, &draft.ID, "Dismissed question", domain.QuestionDismissed)
	f.seedDecision(version.ID, 801, &draft.ID, domain.DecisionProposed)
	f.seedDecision(version.ID, 802, &clarified.ID, domain.DecisionAccepted)
	f.seedDecision(version.ID, 803, &draft.ID, domain.DecisionRejected)
	f.seedAttachment(version.ID, draft.ID, 901, "spec.txt", "text/plain", domain.AttachmentText, "spec body")
	f.seedAttachment(version.ID, clarified.ID, 902, "shot.png", "image/png", domain.AttachmentImage, "png body")
	f.seedSession(version.ID, 51, "known facts")

	content, isError, err := f.service.ContextGet(ctx, version.ID)
	var view struct {
		Version struct {
			ID       string `json:"id"`
			Status   string `json:"status"`
			Revision int    `json:"revision"`
			Counts   struct {
				RequirementCount  int `json:"requirementCount"`
				ClarifiedCount    int `json:"clarifiedCount"`
				OpenQuestionCount int `json:"openQuestionCount"`
			} `json:"counts"`
		} `json:"version"`
		Requirements []struct {
			ID                string `json:"id"`
			Title             string `json:"title"`
			Status            string `json:"status"`
			Revision          int    `json:"revision"`
			OpenQuestionCount int    `json:"openQuestionCount"`
		} `json:"requirements"`
		Relations []struct {
			ID                string `json:"id"`
			FromRequirementID string `json:"fromRequirementId"`
			ToRequirementID   string `json:"toRequirementId"`
			RelationType      string `json:"relationType"`
		} `json:"relations"`
		Questions []struct {
			ID            string  `json:"id"`
			Status        string  `json:"status"`
			RequirementID *string `json:"requirementId"`
		} `json:"questions"`
		Decisions []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"decisions"`
		Attachments []struct {
			ID            string `json:"id"`
			RequirementID string `json:"requirementId"`
			Filename      string `json:"filename"`
			MIMEType      string `json:"mimeType"`
			Kind          string `json:"kind"`
			SizeBytes     int64  `json:"sizeBytes"`
		} `json:"attachments"`
		ContextSummary string `json:"contextSummary"`
		Truncated      bool   `json:"truncated"`
	}
	toolOutcome(t, content, isError, err, &view)

	if view.Version.ID != "100" || view.Version.Status != domain.VersionClarifying || view.Version.Revision != 2 {
		t.Fatalf("version view=%+v", view.Version)
	}
	if view.Version.Counts.RequirementCount != 2 || view.Version.Counts.ClarifiedCount != 1 ||
		view.Version.Counts.OpenQuestionCount != 1 {
		t.Fatalf("counts view=%+v", view.Version.Counts)
	}
	if len(view.Requirements) != 2 {
		t.Fatalf("requirements=%+v want 2", view.Requirements)
	}
	for _, requirement := range view.Requirements {
		switch requirement.ID {
		case "501":
			if requirement.Title != "Checkout" || requirement.Status != domain.RequirementDraft ||
				requirement.Revision != 1 || requirement.OpenQuestionCount != 1 {
				t.Fatalf("draft requirement view=%+v", requirement)
			}
		case "502":
			if requirement.Status != domain.RequirementClarified || requirement.OpenQuestionCount != 0 {
				t.Fatalf("clarified requirement view=%+v", requirement)
			}
		default:
			t.Fatalf("unexpected requirement %+v", requirement)
		}
	}
	if len(view.Relations) != 1 || view.Relations[0].ID != "601" ||
		view.Relations[0].FromRequirementID != "501" || view.Relations[0].ToRequirementID != "502" ||
		view.Relations[0].RelationType != domain.RelationDependsOn {
		t.Fatalf("relations=%+v", view.Relations)
	}
	if len(view.Questions) != 2 {
		t.Fatalf("questions=%+v want open+answered only", view.Questions)
	}
	if len(view.Decisions) != 2 {
		t.Fatalf("decisions=%+v want proposed+accepted only", view.Decisions)
	}
	if len(view.Attachments) != 2 {
		t.Fatalf("attachments=%+v want 2", view.Attachments)
	}
	for _, attachment := range view.Attachments {
		if attachment.ID == "901" {
			if attachment.Filename != "spec.txt" || attachment.MIMEType != "text/plain" ||
				attachment.Kind != domain.AttachmentText || attachment.SizeBytes != int64(len("spec body")) ||
				attachment.RequirementID != "501" {
				t.Fatalf("attachment view=%+v", attachment)
			}
		}
	}
	if view.ContextSummary != "known facts" {
		t.Fatalf("contextSummary=%q", view.ContextSummary)
	}
	if view.Truncated {
		t.Fatal("Truncated=true for a small context")
	}
	if strings.Contains(content, "storagePath") {
		t.Fatalf("context leaked storagePath: %s", content)
	}
	// The open question is part of the context.
	foundOpen := false
	for _, question := range view.Questions {
		if question.ID == "701" && question.Status == domain.QuestionOpen &&
			question.RequirementID != nil && *question.RequirementID == "501" {
			foundOpen = true
		}
	}
	if !foundOpen {
		t.Fatalf("open question missing from %+v (question %d)", view.Questions, open.ID)
	}

	if _, business, err := f.service.ContextGet(ctx, 999999); err != nil || !business {
		t.Fatalf("ContextGet(missing version)=%v/%v want a business not-found", business, err)
	}
}

// TestToolServiceContextGetTruncates pins the 32 KiB bound: long fields and
// oversized collections are trimmed deterministically and the payload stays
// valid JSON.
func TestToolServiceContextGetTruncates(t *testing.T) {
	f := newToolFixture(t)
	version := f.seedVersion(100, domain.VersionClarifying)
	for i := int64(0); i < 40; i++ {
		requirement := f.seedRequirement(version.ID, 500+i, "Requirement", domain.RequirementDraft)
		requirement.Description = strings.Repeat("描述", 8000)
		requirement.AcceptanceCriteria = strings.Repeat("ac", 5000)
		f.requirements.requirements[requirement.ID] = requirement
	}
	content, isError, err := f.service.ContextGet(context.Background(), version.ID)
	if err != nil || isError {
		t.Fatalf("ContextGet=%v/%v want success", isError, err)
	}
	if len(content) > 32<<10 {
		t.Fatalf("context is %d bytes, want at most %d", len(content), 32<<10)
	}
	if !json.Valid([]byte(content)) {
		t.Fatal("truncated context is not valid JSON")
	}
	var view struct {
		Truncated bool `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(content), &view); err != nil || !view.Truncated {
		t.Fatalf("truncated=%v err=%v want true", view.Truncated, err)
	}
}

// TestToolServiceUpdateDraftCAS pins the AI draft edit: whitelisted patch
// fields, revision CAS, source=ai and correctable failures.
func TestToolServiceUpdateDraftCAS(t *testing.T) {
	ctx := context.Background()
	f := newToolFixture(t)
	version := f.seedVersion(100, domain.VersionClarifying)
	draft := f.seedRequirement(version.ID, 501, "Checkout", domain.RequirementDraft)
	clarified := f.seedRequirement(version.ID, 502, "Refunds", domain.RequirementClarified)

	patch := json.RawMessage(`{"title":"Checkout v2","priority":"must"}`)
	content, isError, err := f.service.UpdateDraft(ctx, version.ID, draft.ID, 1, patch, 7)
	var updated struct {
		ID               string `json:"id"`
		Title            string `json:"title"`
		Priority         string `json:"priority"`
		Status           string `json:"status"`
		Revision         int    `json:"revision"`
		LastUpdateSource string `json:"lastUpdateSource"`
	}
	toolOutcome(t, content, isError, err, &updated)
	if updated.ID != "501" || updated.Title != "Checkout v2" || updated.Priority != domain.PriorityMust ||
		updated.Status != domain.RequirementDraft || updated.Revision != 2 ||
		updated.LastUpdateSource != domain.SourceAI {
		t.Fatalf("updated requirement=%+v", updated)
	}
	stored := f.requirements.requirements[draft.ID]
	if stored.Title != "Checkout v2" || stored.LastUpdateSource != domain.SourceAI || stored.Revision != 2 {
		t.Fatalf("stored requirement=%+v", stored)
	}

	// A stale revision is a correctable conflict.
	content, business, err := f.service.UpdateDraft(ctx, version.ID, draft.ID, 1, json.RawMessage(`{"title":"stale"}`), 7)
	toolBusiness(t, content, business, err, "revision_conflict")

	// A clarified requirement is read-only.
	content, business, err = f.service.UpdateDraft(ctx, version.ID, clarified.ID, 1, json.RawMessage(`{"title":"x"}`), 7)
	toolBusiness(t, content, business, err, "requirement_state_conflict")

	// Unknown patch fields are rejected before writing.
	content, business, err = f.service.UpdateDraft(ctx, version.ID, draft.ID, 2, json.RawMessage(`{"status":"clarified"}`), 7)
	toolBusiness(t, content, business, err, "patch")
	if f.requirements.requirements[draft.ID].Status != domain.RequirementDraft {
		t.Fatal("an unknown patch field changed the requirement status")
	}

	// An empty patch is rejected.
	content, business, err = f.service.UpdateDraft(ctx, version.ID, draft.ID, 2, json.RawMessage(`{}`), 7)
	toolBusiness(t, content, business, err, "patch")

	// A requirement of another version is not found.
	content, business, err = f.service.UpdateDraft(ctx, version.ID, 999999, 1, json.RawMessage(`{"title":"x"}`), 7)
	toolBusiness(t, content, business, err, "not found")
}

// TestToolServiceClarificationWrites pins the question/decision/relation
// primitives, including the idempotent upsert and relation propose.
func TestToolServiceClarificationWrites(t *testing.T) {
	ctx := context.Background()
	f := newToolFixture(t)
	version := f.seedVersion(100, domain.VersionClarifying)
	first := f.seedRequirement(version.ID, 501, "Checkout", domain.RequirementDraft)
	second := f.seedRequirement(version.ID, 502, "Refunds", domain.RequirementDraft)

	// question_upsert is idempotent for the same requirement + text.
	content, isError, err := f.service.QuestionUpsert(ctx, version.ID, nil, &first.ID, "What is the refund window?", 7)
	var questionID struct {
		QuestionID string `json:"questionId"`
	}
	toolOutcome(t, content, isError, err, &questionID)
	if questionID.QuestionID == "" {
		t.Fatal("QuestionUpsert returned an empty questionId")
	}
	content, isError, err = f.service.QuestionUpsert(ctx, version.ID, nil, &first.ID, "What is the refund window?", 7)
	var sameQuestion struct {
		QuestionID string `json:"questionId"`
	}
	toolOutcome(t, content, isError, err, &sameQuestion)
	if sameQuestion.QuestionID != questionID.QuestionID {
		t.Fatalf("QuestionUpsert ids=%q/%q want idempotent", questionID.QuestionID, sameQuestion.QuestionID)
	}
	if len(f.clarifications.questions) != 1 {
		t.Fatalf("questions=%d want 1", len(f.clarifications.questions))
	}

	// question_answer transcribes an explicit user answer as agent.
	content, isError, err = f.service.QuestionAnswer(ctx, version.ID, 5000, "30 days", 7)
	var answered struct {
		QuestionID string `json:"questionId"`
		Status     string `json:"status"`
	}
	toolOutcome(t, content, isError, err, &answered)
	if answered.QuestionID != "5000" || answered.Status != domain.QuestionAnswered {
		t.Fatalf("answered=%+v", answered)
	}
	stored := f.clarifications.questions[5000]
	if stored.AnsweredVia != domain.AnsweredViaAgent || stored.AnsweredBy == nil || *stored.AnsweredBy != 7 {
		t.Fatalf("stored question=%+v", stored)
	}

	// decision_propose always creates a proposed AI decision.
	content, isError, err = f.service.DecisionPropose(ctx, version.ID, &first.ID, nil, "Refund window", "30 days", "industry standard", 7)
	var decisionID struct {
		DecisionID string `json:"decisionId"`
	}
	toolOutcome(t, content, isError, err, &decisionID)
	if decisionID.DecisionID == "" {
		t.Fatal("DecisionPropose returned an empty decisionId")
	}
	decision := f.clarifications.decisions[5001]
	if decision.Status != domain.DecisionProposed || decision.Source != domain.SourceAI {
		t.Fatalf("decision=%+v", decision)
	}

	// relation_propose is idempotent, normalizes undirected duplicates and
	// rejects self loops.
	content, isError, err = f.service.RelationPropose(ctx, version.ID, first.ID, second.ID, domain.RelationOverlaps, "shared cart", 7)
	var relationID struct {
		RelationID string `json:"relationId"`
	}
	toolOutcome(t, content, isError, err, &relationID)
	if relationID.RelationID == "" {
		t.Fatal("RelationPropose returned an empty relationId")
	}
	content, isError, err = f.service.RelationPropose(ctx, version.ID, second.ID, first.ID, domain.RelationOverlaps, "shared cart", 7)
	var duplicate struct {
		RelationID string `json:"relationId"`
	}
	toolOutcome(t, content, isError, err, &duplicate)
	if duplicate.RelationID != relationID.RelationID {
		t.Fatalf("duplicate relation ids=%q/%q", relationID.RelationID, duplicate.RelationID)
	}
	if len(f.requirements.relations) != 1 {
		t.Fatalf("relations=%d want 1", len(f.requirements.relations))
	}
	content, business, err := f.service.RelationPropose(ctx, version.ID, first.ID, first.ID, domain.RelationDependsOn, "", 7)
	toolBusiness(t, content, business, err, "itself")

	// context_summary_update writes the active session summary.
	f.seedSession(version.ID, 60, "")
	content, isError, err = f.service.ContextSummaryUpdate(ctx, version.ID, "Business rules: refunds within 30 days.", 7)
	var updated struct {
		OK bool `json:"ok"`
	}
	toolOutcome(t, content, isError, err, &updated)
	if !updated.OK || f.clarifications.sessions[60].ContextSummary == "" {
		t.Fatalf("summary update=%+v stored=%q", updated, f.clarifications.sessions[60].ContextSummary)
	}

	// Without an active session the summary write is a correctable conflict.
	other := f.seedVersion(101, domain.VersionClarifying)
	content, business, err = f.service.ContextSummaryUpdate(ctx, other.ID, "summary", 7)
	toolBusiness(t, content, business, err, "clarification_session_not_active")
}

// TestToolServiceAttachmentPrimitives pins attachment.list/read_text.
func TestToolServiceAttachmentPrimitives(t *testing.T) {
	ctx := context.Background()
	f := newToolFixture(t)
	version := f.seedVersion(100, domain.VersionClarifying)
	first := f.seedRequirement(version.ID, 501, "Checkout", domain.RequirementDraft)
	second := f.seedRequirement(version.ID, 502, "Refunds", domain.RequirementDraft)
	f.seedAttachment(version.ID, first.ID, 901, "spec.txt", "text/plain", domain.AttachmentText, "hello attachment")
	f.seedAttachment(version.ID, second.ID, 902, "shot.png", "image/png", domain.AttachmentImage, "png body")

	content, isError, err := f.service.AttachmentList(ctx, version.ID, nil)
	var list struct {
		Attachments []struct {
			ID            string `json:"id"`
			RequirementID string `json:"requirementId"`
			Filename      string `json:"filename"`
			MIMEType      string `json:"mimeType"`
		} `json:"attachments"`
		Truncated bool `json:"truncated"`
	}
	toolOutcome(t, content, isError, err, &list)
	if len(list.Attachments) != 2 || list.Truncated {
		t.Fatalf("attachment list=%+v", list)
	}
	if strings.Contains(content, "storagePath") {
		t.Fatalf("attachment list leaked storagePath: %s", content)
	}

	content, isError, err = f.service.AttachmentList(ctx, version.ID, &first.ID)
	toolOutcome(t, content, isError, err, &list)
	if len(list.Attachments) != 1 || list.Attachments[0].ID != "901" {
		t.Fatalf("filtered attachment list=%+v", list)
	}

	content, isError, err = f.service.AttachmentReadText(ctx, version.ID, 901, 0)
	var text struct {
		Filename  string `json:"filename"`
		Content   string `json:"content"`
		Truncated bool   `json:"truncated"`
	}
	toolOutcome(t, content, isError, err, &text)
	if text.Filename != "spec.txt" || text.Content != "hello attachment" || text.Truncated {
		t.Fatalf("read text=%+v", text)
	}

	content, isError, err = f.service.AttachmentReadText(ctx, version.ID, 901, 5)
	toolOutcome(t, content, isError, err, &text)
	if text.Content != "hello" || !text.Truncated {
		t.Fatalf("bounded read text=%+v", text)
	}

	content, business, err := f.service.AttachmentReadText(ctx, version.ID, 902, 0)
	toolBusiness(t, content, business, err, "unsupported_attachment_type")

	content, business, err = f.service.AttachmentReadText(ctx, version.ID, 999999, 0)
	toolBusiness(t, content, business, err, "not found")
}

// TestToolServiceCodePrimitives pins code.tree/code.read on the version's
// baseline commit, including path filtering and text safety.
func TestToolServiceCodePrimitives(t *testing.T) {
	ctx := context.Background()
	f := newToolFixture(t)
	version := f.seedVersion(100, domain.VersionClarifying)
	f.bindCode(version.ID)
	f.git.entries = []application.CodeEntry{
		{Path: "README.md", Type: "blob", Size: 10},
		{Path: "src", Type: "tree"},
		{Path: "src/main.go", Type: "blob", Size: 20},
	}

	content, isError, err := f.service.CodeTree(ctx, version.ID, "", 200)
	var tree struct {
		Entries []struct {
			Path string `json:"path"`
			Type string `json:"type"`
			Size int64  `json:"size"`
		} `json:"entries"`
		Truncated bool `json:"truncated"`
	}
	toolOutcome(t, content, isError, err, &tree)
	if len(tree.Entries) != 3 || tree.Truncated {
		t.Fatalf("code tree=%+v", tree)
	}
	if tree.Entries[2].Path != "src/main.go" || tree.Entries[2].Type != "blob" || tree.Entries[2].Size != 20 {
		t.Fatalf("code tree entry=%+v", tree.Entries[2])
	}

	content, isError, err = f.service.CodeTree(ctx, version.ID, "src", 200)
	toolOutcome(t, content, isError, err, &tree)
	if len(tree.Entries) != 1 || tree.Entries[0].Path != "src/main.go" {
		t.Fatalf("code tree under src=%+v", tree)
	}

	content, isError, err = f.service.CodeTree(ctx, version.ID, "", 1)
	toolOutcome(t, content, isError, err, &tree)
	if len(tree.Entries) != 1 || !tree.Truncated {
		t.Fatalf("bounded code tree=%+v want 1 truncated entry", tree)
	}

	// code.read returns the window and flags truncation.
	f.git.content = "hello code"
	f.git.size = int64(len(f.git.content))
	content, isError, err = f.service.CodeRead(ctx, version.ID, "src/main.go", 0, 5)
	var read struct {
		Path      string `json:"path"`
		Content   string `json:"content"`
		Truncated bool   `json:"truncated"`
	}
	toolOutcome(t, content, isError, err, &read)
	if read.Path != "src/main.go" || read.Content != "hello" || !read.Truncated {
		t.Fatalf("code read=%+v", read)
	}
	if f.git.readOffset != 0 || f.git.readMax != 5 {
		t.Fatalf("ReadRange offset/max=%d/%d want 0/5", f.git.readOffset, f.git.readMax)
	}

	content, isError, err = f.service.CodeRead(ctx, version.ID, "src/main.go", 6, 4)
	toolOutcome(t, content, isError, err, &read)
	if read.Content != "code" || read.Truncated {
		t.Fatalf("offset code read=%+v", read)
	}
	if f.git.readOffset != 6 {
		t.Fatalf("ReadRange offset=%d want 6", f.git.readOffset)
	}

	// Path escalation is a correctable error.
	content, business, err := f.service.CodeRead(ctx, version.ID, "../secret", 0, 0)
	toolBusiness(t, content, business, err, "path")
	content, business, err = f.service.CodeTree(ctx, version.ID, "../../etc", 0)
	toolBusiness(t, content, business, err, "path")

	// Binary content is a correctable error rather than a model-visible blob.
	f.git.content = "abc\x00def"
	f.git.size = int64(len(f.git.content))
	content, business, err = f.service.CodeRead(ctx, version.ID, "src/main.go", 0, 0)
	toolBusiness(t, content, business, err, "unsupported_content")

	// A version without a repository binding cannot read code.
	unbound := f.seedVersion(101, domain.VersionClarifying)
	content, business, err = f.service.CodeTree(ctx, unbound.ID, "", 0)
	toolBusiness(t, content, business, err, "resource_unavailable")

	// A hard-deleted P2 repository is resource_unavailable for the code tools.
	// The real catalog hard-deletes rows and reports code_repository_not_found;
	// that reason stays reserved for version binding validation.
	delete(f.resources.repos, 9)
	content, business, err = f.service.CodeRead(ctx, version.ID, "README.md", 0, 0)
	toolBusiness(t, content, business, err, "resource_unavailable")
}

// TestToolServiceInfrastructureErrorsStayErrors pins the business vs
// infrastructure split for the Git and repository dependency paths.
func TestToolServiceInfrastructureErrorsStayErrors(t *testing.T) {
	ctx := context.Background()
	f := newToolFixture(t)
	version := f.seedVersion(100, domain.VersionClarifying)
	f.bindCode(version.ID)
	f.git.treeErr = errors.New("git executable is missing")
	content, isError, err := f.service.CodeTree(ctx, version.ID, "", 0)
	if err == nil {
		t.Fatalf("CodeTree(git failure)=%q/%v want an infrastructure error", content, isError)
	}

	// A typed Git verdict is a business error.
	f.git.treeErr = &application.GitError{Reason: "resource_unavailable", Message: "commit is gone"}
	content, isError, err = f.service.CodeTree(ctx, version.ID, "", 0)
	toolBusiness(t, content, isError, err, "resource_unavailable")

	// A typed catalog error is a business error.
	f.resources.repoErrs[9] = &application.ResourceError{Reason: "resource_unavailable", Message: "repository is gone"}
	content, isError, err = f.service.CodeRead(ctx, version.ID, "README.md", 0, 0)
	toolBusiness(t, content, isError, err, "resource_unavailable")

	// Version lookups stay business errors.
	content, isError, err = f.service.CodeTree(ctx, 999999, "", 0)
	toolBusiness(t, content, isError, err, "not found")
}

// TestToolServiceCodeTreeFiltersPrefixBeforeCap pins the optional prefix-aware
// reader contract: a subtree that sorts after more than 200 other entries must
// still be listed for code.tree{path:"src"}.
func TestToolServiceCodeTreeFiltersPrefixBeforeCap(t *testing.T) {
	ctx := context.Background()
	f := newToolFixture(t)
	version := f.seedVersion(100, domain.VersionClarifying)
	f.bindCode(version.ID)
	repository := f.resources.repos[9]
	repository.IncludeGlobs = []string{"aaa/**", "src/**"}
	repository.ExcludeGlobs = []string{"src/skip.go"}
	f.resources.repos[9] = repository
	for i := 0; i < 250; i++ {
		f.git.entries = append(f.git.entries, application.CodeEntry{
			Path: fmt.Sprintf("aaa/file-%03d.txt", i), Type: "blob", Size: 1,
		})
	}
	f.git.entries = append(f.git.entries,
		application.CodeEntry{Path: "README.md", Type: "blob", Size: 5},
		application.CodeEntry{Path: "src/main.go", Type: "blob", Size: 20},
		application.CodeEntry{Path: "src/skip.go", Type: "blob", Size: 30},
	)

	content, isError, err := f.service.CodeTree(ctx, version.ID, "src", 200)
	var tree struct {
		Entries []struct {
			Path string `json:"path"`
			Type string `json:"type"`
			Size int64  `json:"size"`
		} `json:"entries"`
		Truncated bool `json:"truncated"`
	}
	toolOutcome(t, content, isError, err, &tree)
	if len(tree.Entries) != 1 || tree.Entries[0].Path != "src/main.go" ||
		tree.Entries[0].Type != "blob" || tree.Entries[0].Size != 20 {
		t.Fatalf("code tree under src entries=%+v want only src/main.go", tree.Entries)
	}
	if tree.Truncated {
		t.Fatal("code tree under src reported truncation for one entry")
	}
	if f.git.treePrefix != "src" {
		t.Fatalf("reader prefix=%q want the prefix-aware TreePrefix call", f.git.treePrefix)
	}
}

// TestToolServiceCodeReadTrimsSplitRuneWindow pins the UTF-8 window fix: a
// 64 KiB window at a non-zero offset can start and end inside a rune, and the
// split edges must not turn valid CJK/emoji text into unsupported_content.
func TestToolServiceCodeReadTrimsSplitRuneWindow(t *testing.T) {
	ctx := context.Background()
	f := newToolFixture(t)
	version := f.seedVersion(100, domain.VersionClarifying)
	f.bindCode(version.ID)
	content := "a" + strings.Repeat("你", 22000) + strings.Repeat("🙂", 2000)
	f.git.content = content
	f.git.size = int64(len(content))

	var read struct {
		Content   string `json:"content"`
		Truncated bool   `json:"truncated"`
	}
	// Offset 2 starts inside the first three-byte rune; the 64 KiB cap ends
	// inside a later three-byte rune.
	raw, isError, err := f.service.CodeRead(ctx, version.ID, "src/main.go", 2, 0)
	toolOutcome(t, raw, isError, err, &read)
	want := content[4:65536]
	if read.Content != want {
		t.Fatalf("split-rune read content=%d bytes, want %d bytes", len(read.Content), len(want))
	}
	if !utf8.ValidString(read.Content) {
		t.Fatal("split-rune read content is not valid UTF-8")
	}
	if !read.Truncated {
		t.Fatal("split-rune read did not report truncation")
	}

	// A small window inside the four-byte emoji run runs both trims at once.
	raw, isError, err = f.service.CodeRead(ctx, version.ID, "src/main.go", 66002, 8)
	toolOutcome(t, raw, isError, err, &read)
	if read.Content != "🙂" || !read.Truncated {
		t.Fatalf("emoji split read=%q truncated=%v want one emoji and truncation", read.Content, read.Truncated)
	}
}

// TestToolServiceCodeReadRejectsOffsetPastEOF pins the offset bound: a window
// that starts past the blob is a correctable invalid_offset, while an offset at
// EOF is an ordinary empty read.
func TestToolServiceCodeReadRejectsOffsetPastEOF(t *testing.T) {
	ctx := context.Background()
	f := newToolFixture(t)
	version := f.seedVersion(100, domain.VersionClarifying)
	f.bindCode(version.ID)
	f.git.content = "hello"
	f.git.size = int64(len(f.git.content))

	content, business, err := f.service.CodeRead(ctx, version.ID, "src/main.go", 6, 0)
	toolBusiness(t, content, business, err, "invalid_offset")

	content, isError, err := f.service.CodeRead(ctx, version.ID, "src/main.go", 5, 0)
	var read struct {
		Content   string `json:"content"`
		Truncated bool   `json:"truncated"`
	}
	toolOutcome(t, content, isError, err, &read)
	if read.Content != "" || read.Truncated {
		t.Fatalf("offset-at-EOF read=%+v want an empty non-truncated read", read)
	}
}

// TestToolServiceContextGetLoadsRequirementsOnce pins that version.context_get
// shares one requirement page load between its requirement and attachment
// sections instead of querying twice.
func TestToolServiceContextGetLoadsRequirementsOnce(t *testing.T) {
	ctx := context.Background()
	f := newToolFixture(t)
	version := f.seedVersion(100, domain.VersionClarifying)
	f.seedRequirement(version.ID, 501, "Checkout", domain.RequirementDraft)
	f.seedAttachment(version.ID, 501, 901, "spec.txt", "text/plain", domain.AttachmentText, "spec body")

	if _, isError, err := f.service.ContextGet(ctx, version.ID); err != nil || isError {
		t.Fatalf("ContextGet=%v/%v want success", isError, err)
	}
	if f.requirementLister.listCalls != 1 {
		t.Fatalf("requirement List calls=%d want 1 (context and attachments share one load)", f.requirementLister.listCalls)
	}
}

// TestToolServiceWritePublishesVersionEvent pins that a successful tool write
// still publishes the version event through the wrapped service.
func TestToolServiceWritePublishesVersionEvent(t *testing.T) {
	ctx := context.Background()
	f := newToolFixture(t)
	version := f.seedVersion(100, domain.VersionClarifying)
	draft := f.seedRequirement(version.ID, 501, "Checkout", domain.RequirementDraft)

	if _, isError, err := f.service.UpdateDraft(ctx, version.ID, draft.ID, 1, json.RawMessage(`{"title":"Checkout v2"}`), 7); err != nil || isError {
		t.Fatalf("UpdateDraft=%v/%v want success", isError, err)
	}
	if len(f.events.events) != 1 {
		t.Fatalf("published %d events, want 1", len(f.events.events))
	}
	event := f.events.events[0]
	if event.Type != "requirement.updated" || event.VersionID != version.ID || event.ActorID != 7 ||
		event.Source != domain.SourceAI || event.RefRequirementID == nil || *event.RefRequirementID != draft.ID {
		t.Fatalf("published event=%+v", event)
	}
}
