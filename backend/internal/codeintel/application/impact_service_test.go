package codeintelapp

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// --- fakes for impact analysis ---------------------------------------------------

type fakeCandidateStore struct {
	candidates map[int64][]codeintel.ImpactCandidate
	nextID     int64
	replaceErr error
	listErr    error
	updateErr  error
}

func newFakeCandidateStore() *fakeCandidateStore {
	return &fakeCandidateStore{candidates: map[int64][]codeintel.ImpactCandidate{}}
}

func (f *fakeCandidateStore) ReplaceForAnalysis(_ context.Context, analysisID int64, candidates []codeintel.ImpactCandidate) error {
	if f.replaceErr != nil {
		return f.replaceErr
	}
	stored := make([]codeintel.ImpactCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		f.nextID++
		candidate.ID = f.nextID
		candidate.AnalysisID = analysisID
		stored = append(stored, candidate)
	}
	f.candidates[analysisID] = stored
	return nil
}

func (f *fakeCandidateStore) ListByAnalysis(_ context.Context, analysisID int64) ([]codeintel.ImpactCandidate, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.candidates[analysisID], nil
}

func (f *fakeCandidateStore) UpdateSelection(_ context.Context, analysisID, candidateID, actorID int64, selected bool) (codeintel.ImpactCandidate, error) {
	if f.updateErr != nil {
		return codeintel.ImpactCandidate{}, f.updateErr
	}
	for index, candidate := range f.candidates[analysisID] {
		if candidate.ID != candidateID {
			continue
		}
		candidate.Selected = selected
		candidate.SelectionUpdatedBy = &actorID
		now := time.Date(2026, 11, 1, 11, 0, 0, 0, time.UTC)
		candidate.SelectionUpdatedAt = &now
		f.candidates[analysisID][index] = candidate
		return candidate, nil
	}
	return codeintel.ImpactCandidate{}, codeintel.ErrNotFound
}

type fakeImpactDesign struct {
	requirements []RequirementRef
	caseVersions []CaseVersionRef
	relations    []RequirementRelationRef
	err          error
}

func (f *fakeImpactDesign) Requirements(context.Context, int64) ([]RequirementRef, error) {
	return f.requirements, f.err
}

func (f *fakeImpactDesign) CaseVersions(context.Context, int64) ([]CaseVersionRef, error) {
	return f.caseVersions, f.err
}

func (f *fakeImpactDesign) RequirementRelations(context.Context, int64) ([]RequirementRelationRef, error) {
	return f.relations, f.err
}

type fakeImpactEdges struct {
	edges []ImpactEdge
	err   error
}

func (f *fakeImpactEdges) ImpactEdges(context.Context, int64, string) ([]ImpactEdge, error) {
	return f.edges, f.err
}

type fakeCaseSources struct {
	sources []CaseSourceRef
	err     error
}

func (f *fakeCaseSources) CaseSources(context.Context, int64) ([]CaseSourceRef, error) {
	return f.sources, f.err
}

// --- harness ---------------------------------------------------------------------

type impactHarness struct {
	service    *ImpactService
	analyses   *fakeAnalysisRepo
	candidates *fakeCandidateStore
	facts      *fakeFactRepo
	anchors    *fakeAnchorStore
	reviews    *fakeReviewStore
	edges      *fakeImpactEdges
	cases      *fakeCaseSources
	design     *fakeImpactDesign
	resources  *fakeResourceReader
	versions   *fakeVersionReader
	extractor  *fakeExtractor
	subjects   *fakeAnchorSubjects
	events     *fakeEvents
	audit      *fakeAudit
	now        time.Time
	ctx        context.Context
}

const impactBaseCommit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func newImpactHarness(t *testing.T) *impactHarness {
	t.Helper()
	now := time.Date(2026, 11, 1, 11, 0, 0, 0, time.UTC)
	ctx := platform.WithActor(context.Background(), platform.Actor{ID: 42, Role: "admin"})
	analyses := newFakeAnalysisRepo()
	changed := []map[string]any{
		{"path": "src/a.ts", "status": "modified", "additions": 1, "deletions": 0, "binary": false},
		{"path": "src/b.ts", "status": "modified", "additions": 1, "deletions": 0, "binary": false},
		{"path": "src/routes.ts", "status": "modified", "additions": 1, "deletions": 0, "binary": false},
	}
	_, err := analyses.Create(ctx, codeintel.Analysis{
		ID: 5, VersionID: 1, RepoID: 10, BaseCommitSHA: impactBaseCommit,
		HeadCommitSHA: anchorCommitB, Status: codeintel.AnalysisDone,
		ChangedFiles: mustJSON(changed), Stats: []byte(`{}`), Unmapped: []byte(`[]`),
		Errors: []byte(`[]`), FactVersions: mustJSON(map[string]int{
			"route:/orders":           1,
			"api:POST /api/refund":    1,
			"client:POST /api/refund": 1,
		}),
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("seed analysis: %v", err)
	}
	facts := &fakeFactRepo{}
	for _, fact := range []codeintel.Fact{
		{RepoID: 10, CommitSHA: anchorCommitB, FactKey: "route:/orders", Version: 1,
			Kind: codeintel.EntityRoute, Path: "src/routes.ts", Name: "/orders",
			Source: "ai", Confidence: codeintel.ConfidenceHigh},
		{RepoID: 10, CommitSHA: anchorCommitB, FactKey: "api:POST /api/refund", Version: 1,
			Kind: codeintel.EntityAPI, Path: "src/routes.ts", Name: "POST /api/refund",
			Source: "ai", Confidence: codeintel.ConfidenceMedium},
		{RepoID: 10, CommitSHA: anchorCommitB, FactKey: "client:POST /api/refund", Version: 1,
			Kind: codeintel.EntityClientCall, Path: "src/client.ts", Name: "POST /api/refund",
			Source: "ai", Confidence: codeintel.ConfidenceHigh},
	} {
		fact.Body = []byte(`{}`)
		fact.Evidence = []byte(`[]`)
		fact.CreatedAt = now
		fact.UpdatedAt = now
		if _, _, err := facts.InsertVersion(ctx, fact); err != nil {
			t.Fatalf("seed fact: %v", err)
		}
	}

	anchors := newFakeAnchorStore()
	for _, anchor := range []codeintel.Anchor{
		{VersionID: 1, RepoID: 10, SubjectKind: codeintel.AnchorSubjectRequirement, SubjectID: 100,
			FilePath: "src/a.ts", Status: "valid"},
		{VersionID: 1, RepoID: 10, SubjectKind: codeintel.AnchorSubjectCaseVersion, SubjectID: 200,
			FilePath: "src/a.ts", Status: "valid"},
	} {
		anchor.CreatedAt = now
		anchor.UpdatedAt = now
		if _, err := anchors.Create(ctx, anchor); err != nil {
			t.Fatalf("seed anchor: %v", err)
		}
	}
	reviews := newFakeReviewStore()
	versions := &fakeVersionReader{bindings: []VersionSUTBinding{
		{VersionSUTID: 11, VersionID: 1, SUTID: 21, RepoID: 10, CodeBaselineCommit: impactBaseCommit},
		{VersionSUTID: 12, VersionID: 1, SUTID: 22, RepoID: 11, CodeBaselineCommit: impactBaseCommit},
	}}
	git := &fakeGit{windows: map[string]codeintel.FileWindow{
		"src/a.ts": {Path: "src/a.ts", Lines: []string{"export const x = 1;", "changed"}, StartLine: 1, EndLine: 2, TotalLines: 2},
	}}
	resources := &fakeResourceReader{
		repos: map[int64]RepoRef{
			10: {ID: 10, Name: "a", Path: "/repo-a", Branch: "main"},
			11: {ID: 11, Name: "b", Path: "/repo-b", Branch: "main"},
		},
		pages: map[int64][]PageRef{
			11: {{ID: 500, Name: "Orders", RouteTemplate: "/orders", Status: "approved", Source: "admin"}},
		},
		apis: map[int64][]APIRef{
			11: {{ID: 600, Name: "Refund", Method: "POST", PathTemplate: "/api/refund", Status: "approved", Source: "manual"}},
		},
	}
	design := &fakeImpactDesign{
		requirements: []RequirementRef{{ID: 100, Title: "checkout"}, {ID: 101, Title: "refund"}},
		caseVersions: []CaseVersionRef{
			{ID: 200, CaseID: 20, RequirementID: 100, Status: "confirmed"},
			{ID: 300, CaseID: 30, RequirementID: 101, Status: "confirmed"},
		},
		relations: []RequirementRelationRef{{FromRequirementID: 100, ToRequirementID: 101, Kind: "depends_on"}},
	}
	edges := &fakeImpactEdges{edges: []ImpactEdge{{
		SrcFactKey: "client:POST /api/refund", DstFactKey: "api:POST /api/refund",
		Rel: codeintel.EdgeRouteToAPI, Confidence: codeintel.ConfidenceMedium,
	}}}
	cases := &fakeCaseSources{sources: []CaseSourceRef{{CaseVersionID: 201, Path: "src/b.ts", RepoID: 10}}}
	extractor := &fakeExtractor{}
	subjects := &fakeAnchorSubjects{
		requirementVersions: map[int64]bool{110: true},
		caseVersions:        map[int64]bool{200: true, 300: true},
	}
	events := &fakeEvents{}
	audit := &fakeAudit{}
	service := NewImpactService(ImpactDeps{
		Analyses:    analyses,
		Candidates:  newFakeCandidateStore(),
		Facts:       facts,
		Anchors:     anchors,
		Reviews:     reviews,
		Edges:       edges,
		CaseSources: cases,
		Design:      design,
		Resources:   resources,
		Versions:    versions,
		Extractor:   extractor,
		Bulk:        git,
		Subjects:    subjects,
		Events:      events,
		Audit:       audit,
		Settings:    &fakeSettings{ints: map[string]int64{"codeintel.max_file_bytes": 65536, "codeintel.max_excerpt_lines": 40}},
		Clock:       fakeClock{now: now},
		IDs:         &fakeIDs{},
	})
	return &impactHarness{
		service: service, analyses: analyses, candidates: service.candidates.(*fakeCandidateStore),
		facts: facts, anchors: anchors, reviews: reviews, edges: edges, cases: cases,
		design: design, resources: resources, versions: versions, extractor: extractor,
		subjects: subjects, events: events, audit: audit, now: now, ctx: ctx,
	}
}

func (h *impactHarness) bySubject(candidates []codeintel.ImpactCandidate, kind codeintel.SubjectKind, id int64) (codeintel.ImpactCandidate, bool) {
	for _, candidate := range candidates {
		if candidate.SubjectKind == kind && candidate.SubjectID == id {
			return candidate, true
		}
	}
	return codeintel.ImpactCandidate{}, false
}

func (h *impactHarness) compute(t *testing.T) []codeintel.ImpactCandidate {
	t.Helper()
	candidates, err := h.service.Compute(h.ctx, 5)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	return candidates
}

// --- tests -----------------------------------------------------------------------

func TestImpactDeterministicCandidates(t *testing.T) {
	h := newImpactHarness(t)
	candidates := h.compute(t)

	requirement, ok := h.bySubject(candidates, codeintel.SubjectRequirement, 100)
	if !ok || requirement.Source != codeintel.CandidateDirect || requirement.Confidence != codeintel.ConfidenceHigh || !requirement.Selected {
		t.Fatalf("direct requirement = %+v", requirement)
	}
	if requirement.Reason == "" {
		t.Fatal("direct candidate needs a reason")
	}
	anchorCase, ok := h.bySubject(candidates, codeintel.SubjectCaseVersion, 200)
	if !ok || anchorCase.Source != codeintel.CandidateDirect {
		t.Fatalf("anchor case = %+v", anchorCase)
	}
	sourceCase, ok := h.bySubject(candidates, codeintel.SubjectCaseVersion, 201)
	if !ok || sourceCase.Source != codeintel.CandidateDirect {
		t.Fatalf("case_source case = %+v", sourceCase)
	}
	page, ok := h.bySubject(candidates, codeintel.SubjectSUTPage, 500)
	if !ok || page.Source != codeintel.CandidateDirect || page.Confidence != codeintel.ConfidenceHigh {
		t.Fatalf("page = %+v", page)
	}
	api, ok := h.bySubject(candidates, codeintel.SubjectSUTAPI, 600)
	if !ok || api.Source != codeintel.CandidateDirect {
		t.Fatalf("api = %+v", api)
	}
	releatedRequirement, ok := h.bySubject(candidates, codeintel.SubjectRequirement, 101)
	if !ok || releatedRequirement.Source != codeintel.CandidateRelation || releatedRequirement.Confidence != codeintel.ConfidenceMedium {
		t.Fatalf("relation requirement = %+v", releatedRequirement)
	}
	relationCase, ok := h.bySubject(candidates, codeintel.SubjectCaseVersion, 300)
	if !ok || relationCase.Source != codeintel.CandidateRelation {
		t.Fatalf("relation case = %+v", relationCase)
	}

	// Ordering: source priority, then confidence, then subject kind/id.
	seen := ""
	for _, candidate := range candidates {
		key := string(candidate.Source)
		if key != seen {
			if seen != "" && candidateSourceRank(candidate.Source) < candidateSourceRank(codeintel.CandidateSource(seen)) {
				t.Fatalf("candidate order regressed at %+v", candidate)
			}
			seen = key
		}
	}
	if len(h.events.events) != 1 || h.events.events[0].Type != "code.impact_computed" {
		t.Fatalf("events = %v", h.events.types())
	}
	payload := h.events.events[0].Payload
	if payload["analysis_id"] != "5" || payload["selected_count"] != len(candidates) {
		t.Fatalf("event payload = %+v", payload)
	}
}

func TestImpactContractOneHop(t *testing.T) {
	h := newImpactHarness(t)
	// Drop both API contract facts from the fact projection; the analyzed key set
	// still records the changed client call, so only the one-hop edge remains.
	h.facts.mu.Lock()
	kept := make([]codeintel.Fact, 0)
	for _, fact := range h.facts.current {
		if fact.FactKey == "api:POST /api/refund" || fact.FactKey == "client:POST /api/refund" {
			continue
		}
		kept = append(kept, fact)
	}
	h.facts.current = kept
	h.facts.mu.Unlock()
	analysis, _ := h.analyses.Get(h.ctx, 5)
	analysis.FactVersions = mustJSON(map[string]int{"route:/orders": 1, "client:POST /api/refund": 1})
	if err := h.analyses.Update(h.ctx, analysis); err != nil {
		t.Fatalf("update analysis: %v", err)
	}

	candidates := h.compute(t)
	api, ok := h.bySubject(candidates, codeintel.SubjectSUTAPI, 600)
	if !ok {
		t.Fatalf("contract candidate missing: %+v", candidates)
	}
	if api.Source != codeintel.CandidateContract || api.Confidence != codeintel.ConfidenceMedium {
		t.Fatalf("contract candidate = %+v", api)
	}
}

func TestImpactMergesAIDuplicatesAndValidatesSubjects(t *testing.T) {
	h := newImpactHarness(t)
	h.extractor.impactOut = ImpactInferenceOutput{Candidates: []ImpactDraft{
		{SubjectKind: codeintel.SubjectRequirement, SubjectID: 100, Reason: "AI also sees checkout", Confidence: codeintel.ConfidenceHigh},
		{SubjectKind: codeintel.SubjectCaseVersion, SubjectID: 300, Reason: "AI case", Confidence: codeintel.ConfidenceHigh},
		{SubjectKind: codeintel.SubjectRequirementVersion, SubjectID: 110, Reason: "version impacted", Confidence: codeintel.ConfidenceHigh,
			Evidence: []EvidenceDraft{{File: "src/a.ts", StartLine: 1, EndLine: 1}}},
		{SubjectKind: codeintel.SubjectSUTPage, SubjectID: 9999, Reason: "unknown", Confidence: codeintel.ConfidenceHigh},
		{SubjectKind: "nonsense", SubjectID: 1, Reason: "bad kind", Confidence: codeintel.ConfidenceHigh},
	}, InvalidCandidates: 1}
	candidates := h.compute(t)

	requirement, _ := h.bySubject(candidates, codeintel.SubjectRequirement, 100)
	if requirement.Source != codeintel.CandidateDirect {
		t.Fatalf("duplicate requirement source = %q", requirement.Source)
	}
	if requirement.Reason != "anchor path is in the change set; AI also sees checkout" {
		t.Fatalf("merged reason = %q", requirement.Reason)
	}
	requirementVersion, ok := h.bySubject(candidates, codeintel.SubjectRequirementVersion, 110)
	if !ok || requirementVersion.Source != codeintel.CandidateAI {
		t.Fatalf("requirement version = %+v", requirementVersion)
	}
	var evidence []map[string]any
	if err := json.Unmarshal(requirementVersion.Evidence, &evidence); err != nil || len(evidence) != 1 {
		t.Fatalf("evidence = %s err=%v", requirementVersion.Evidence, err)
	}
	if evidence[0]["excerpt_hash"] != codeintel.ExcerptHash([]string{"export const x = 1;"}) {
		t.Fatalf("evidence hash = %+v", evidence[0])
	}
	if _, ok := h.bySubject(candidates, codeintel.SubjectSUTPage, 9999); ok {
		t.Fatal("unknown AI subject must be dropped")
	}
	if len(h.extractor.impactIn) != 1 {
		t.Fatalf("impact calls = %d", len(h.extractor.impactIn))
	}
	input := h.extractor.impactIn[0]
	if len(input.Requirements) != 2 || len(input.Selected) == 0 || input.CommitSHA != anchorCommitB {
		t.Fatalf("impact input = %+v", input)
	}
}

func TestImpactDegradedAndIncorrectFactExcluded(t *testing.T) {
	h := newImpactHarness(t)
	h.reviews.verdicts["route:/orders"] = "incorrect"
	h.extractor.impactErr = fmt.Errorf("llm unavailable")

	candidates := h.compute(t)
	if _, ok := h.bySubject(candidates, codeintel.SubjectSUTPage, 500); ok {
		t.Fatal("incorrect fact must not produce an automatic page candidate")
	}
	analysis, err := h.analyses.Get(h.ctx, 5)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !analysis.Degraded {
		t.Fatal("analysis must be degraded when the impact call fails")
	}
	var unmapped []map[string]any
	if err := json.Unmarshal(analysis.Unmapped, &unmapped); err != nil {
		t.Fatalf("unmapped: %v", err)
	}
	found := false
	for _, entry := range unmapped {
		if entry["reason"] == "ai_unavailable" {
			found = true
		}
	}
	if !found {
		t.Fatalf("unmapped = %+v", unmapped)
	}
	if len(candidates) == 0 {
		t.Fatal("deterministic candidates must survive an AI failure")
	}
}

func TestImpactSetSelectedAndList(t *testing.T) {
	h := newImpactHarness(t)
	candidates := h.compute(t)
	requirement, _ := h.bySubject(candidates, codeintel.SubjectRequirement, 100)

	updated, err := h.service.SetSelected(h.ctx, 5, requirement.ID, 42, false)
	if err != nil {
		t.Fatalf("SetSelected: %v", err)
	}
	if updated.Selected || updated.SelectionUpdatedBy == nil || *updated.SelectionUpdatedBy != 42 {
		t.Fatalf("updated = %+v", updated)
	}
	if len(h.audit.entries) != 1 || h.audit.entries[0].Action != "code.impact.candidate_selected" {
		t.Fatalf("audit = %+v", h.audit.entries)
	}
	_, err = h.service.SetSelected(h.ctx, 5, 99999, 42, true)
	assertPlatformError(t, err, 404, "not_found")
	_, err = h.service.SetSelected(h.ctx, 999, requirement.ID, 42, true)
	assertPlatformError(t, err, 404, "not_found")

	views, err := h.service.List(h.ctx, 5)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(views) != len(candidates) {
		t.Fatalf("views = %d", len(views))
	}
	if views[0].RepoID != 10 || len(views[0].VersionSUTIDs) == 0 || views[0].VersionSUTIDs[0] != 11 {
		t.Fatalf("view tags = %+v", views[0])
	}
}

func TestImpactUnionMultiRepo(t *testing.T) {
	h := newImpactHarness(t)
	// A second done analysis on repo 11 with one direct requirement candidate.
	second, err := h.analyses.Create(h.ctx, codeintel.Analysis{
		ID: 6, VersionID: 1, RepoID: 11, BaseCommitSHA: impactBaseCommit, HeadCommitSHA: anchorCommitB,
		Status: codeintel.AnalysisDone, ChangedFiles: []byte(`[{"path":"src/a.ts","status":"modified"}]`),
		Stats: []byte(`{}`), Unmapped: []byte(`[]`), Errors: []byte(`[]`), FactVersions: []byte(`{}`),
		CreatedAt: h.now, UpdatedAt: h.now,
	})
	if err != nil {
		t.Fatalf("second analysis: %v", err)
	}
	if err := h.candidates.ReplaceForAnalysis(h.ctx, second.ID, []codeintel.ImpactCandidate{{
		SubjectKind: codeintel.SubjectRequirement, SubjectID: 100, Reason: "indirect dependency", Source: codeintel.CandidateRelation,
		Confidence: codeintel.ConfidenceMedium, Evidence: []byte(`[]`), Selected: true,
	}}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	h.compute(t)

	union, err := h.service.Union(h.ctx, 1)
	if err != nil {
		t.Fatalf("Union: %v", err)
	}
	merged, ok := findView(union, codeintel.SubjectRequirement, 100)
	if !ok || merged.Source != codeintel.CandidateDirect {
		t.Fatalf("union merged = %+v", merged)
	}
	if merged.Reason != "anchor path is in the change set; indirect dependency" {
		t.Fatalf("union reason = %q", merged.Reason)
	}
	if merged.RepoID != 10 {
		t.Fatalf("union repo = %d", merged.RepoID)
	}
	if merged.ID <= 0 || merged.AnalysisID <= 0 {
		t.Fatalf("union must preserve persisted candidate ids: %+v", merged)
	}
	page, ok := findView(union, codeintel.SubjectSUTPage, 500)
	if !ok || page.RepoID != 10 {
		t.Fatalf("union page = %+v", page)
	}
}

func findView(views []ImpactCandidateView, kind codeintel.SubjectKind, id int64) (ImpactCandidateView, bool) {
	for _, view := range views {
		if view.SubjectKind == kind && view.SubjectID == id {
			return view, true
		}
	}
	return ImpactCandidateView{}, false
}
