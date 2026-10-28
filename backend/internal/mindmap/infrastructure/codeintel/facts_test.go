package codeintel_test

import (
	"context"
	"errors"
	"testing"

	codeinteldomain "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	mindmapapp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	mindmapcodeintel "github.com/EdouardRichard/EzTest/backend/internal/mindmap/infrastructure/codeintel"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

const baselineCommit = "0123456789abcdef0123456789abcdef01234567"

// fakeAnalysisReader serves the latest analysis and its candidates.
type fakeAnalysisReader struct {
	analysis    codeinteldomain.CodeAnalysis
	err         error
	versionID   int64
	candidates  map[int64][]codeinteldomain.ImpactCandidate
	candidateID int64
}

// GetLatest implements the codeintel analysis read.
func (f *fakeAnalysisReader) GetLatest(_ context.Context, versionID int64) (codeinteldomain.CodeAnalysis, error) {
	f.versionID = versionID
	if f.err != nil {
		return codeinteldomain.CodeAnalysis{}, f.err
	}
	return f.analysis, nil
}

// ListCandidates implements the codeintel candidate read.
func (f *fakeAnalysisReader) ListCandidates(_ context.Context, analysisID int64) ([]codeinteldomain.ImpactCandidate, error) {
	f.candidateID = analysisID
	return f.candidates[analysisID], nil
}

// fileCall records the arguments of one File read.
type fileCall struct {
	actorID   int64
	repoID    int64
	commit    string
	path      string
	startLine int
	endLine   int
}

// fakeCodeQuery serves one file read and one facts page.
type fakeCodeQuery struct {
	fileWindow  codeinteldomain.FileWindow
	fileErr     error
	fileCalls   []fileCall
	entities    []codeinteldomain.CodeEntity
	factsRepo   int64
	factsFilter codeinteldomain.EntityFilter
	treeEntries []codeinteldomain.TreeEntry
	treeRepo    int64
	treeCommit  string
}

// File implements the codeintel commit-pinned file read.
func (f *fakeCodeQuery) File(_ context.Context, actorID, repoID int64, commit, path string, startLine, endLine int) (codeinteldomain.FileWindow, error) {
	f.fileCalls = append(f.fileCalls, fileCall{actorID: actorID, repoID: repoID, commit: commit, path: path, startLine: startLine, endLine: endLine})
	if f.fileErr != nil {
		return codeinteldomain.FileWindow{}, f.fileErr
	}
	return f.fileWindow, nil
}

// Facts implements the codeintel entity facts read.
func (f *fakeCodeQuery) Facts(_ context.Context, repoID int64, filter codeinteldomain.EntityFilter) ([]codeinteldomain.CodeEntity, string, bool, error) {
	f.factsRepo = repoID
	f.factsFilter = filter
	return f.entities, "", false, nil
}

// Tree implements the codeintel commit-pinned file tree read.
func (f *fakeCodeQuery) Tree(_ context.Context, repoID int64, _, commit, _ string, limit int) (codeinteldomain.TreePage, error) {
	f.treeRepo = repoID
	f.treeCommit = commit
	entries := f.treeEntries
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	return codeinteldomain.TreePage{Commit: commit, Entries: entries}, nil
}

// fakeVersionReader returns one version binding.
type fakeVersionReader struct {
	version *versionapp.Version
	err     error
	id      int64
}

// Get implements the version binding read.
func (f *fakeVersionReader) Get(_ context.Context, id int64) (*versionapp.Version, error) {
	f.id = id
	if f.err != nil {
		return nil, f.err
	}
	return f.version, nil
}

// TestCodeFactsAdapterReportsNoAnalysis pins the documented zero snapshot: the
// version has no analysis yet, so the not-found read is not an error.
func TestCodeFactsAdapterReportsNoAnalysis(t *testing.T) {
	ctx := context.Background()
	reader := &fakeAnalysisReader{err: codeinteldomain.NewNotFound("analysis_not_found", "codeintel: version 7 has no analysis")}
	adapter := mindmapcodeintel.NewCodeFactsAdapter(reader, &fakeCodeQuery{}, &fakeVersionReader{})
	var _ mindmapapp.CodeFactsGateway = adapter

	snapshot, err := adapter.GetLatestAnalysis(ctx, 7)
	if err != nil {
		t.Fatalf("GetLatestAnalysis: %v", err)
	}
	if snapshot != (mindmapapp.ImpactSnapshot{}) {
		t.Fatalf("snapshot=%+v want the zero snapshot", snapshot)
	}
	if reader.versionID != 7 {
		t.Fatalf("analysis reader received version %d want 7", reader.versionID)
	}
}

// TestCodeFactsAdapterMapsAnalysisAndCandidates pins the impact projection.
func TestCodeFactsAdapterMapsAnalysisAndCandidates(t *testing.T) {
	ctx := context.Background()
	reader := &fakeAnalysisReader{
		analysis: codeinteldomain.CodeAnalysis{
			ID:         33,
			VersionID:  7,
			BaseCommit: "aaaa",
			HeadCommit: "bbbb",
			Status:     codeinteldomain.RunDone,
			Degraded:   true,
		},
		candidates: map[int64][]codeinteldomain.ImpactCandidate{
			33: {{
				ID:          44,
				AnalysisID:  33,
				SubjectKind: codeinteldomain.CandidateAppModel,
				SubjectID:   21,
				Reason:      "route changed",
				Confidence:  codeinteldomain.ConfidenceHigh,
				Source:      codeinteldomain.CandidateContract,
			}},
		},
	}
	adapter := mindmapcodeintel.NewCodeFactsAdapter(reader, &fakeCodeQuery{}, &fakeVersionReader{})

	snapshot, err := adapter.GetLatestAnalysis(ctx, 7)
	if err != nil {
		t.Fatalf("GetLatestAnalysis: %v", err)
	}
	want := mindmapapp.ImpactSnapshot{AnalysisID: 33, BaseCommit: "aaaa", HeadCommit: "bbbb", Status: codeinteldomain.RunDone, Degraded: true}
	if snapshot != want {
		t.Fatalf("snapshot=%+v want %+v", snapshot, want)
	}

	candidates, err := adapter.ListCandidates(ctx, 33)
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("mapped %d candidates, want 1", len(candidates))
	}
	got := candidates[0]
	if got.ID != 44 || got.SubjectKind != codeinteldomain.CandidateAppModel || got.SubjectID != 21 ||
		got.Reason != "route changed" || got.Confidence != codeinteldomain.ConfidenceHigh ||
		got.Source != codeinteldomain.CandidateContract {
		t.Fatalf("candidate=%+v want the source fields", got)
	}
	if reader.candidateID != 33 {
		t.Fatalf("candidate reader received analysis %d want 33", reader.candidateID)
	}
}

// TestCodeFactsAdapterFileExistsPinsTheBaselineCommit pins that existence is
// checked at the version's code baseline commit and that a missing file is
// false, not an error.
func TestCodeFactsAdapterFileExistsUsesBaselineCommit(t *testing.T) {
	ctx := context.Background()
	repoID := int64(9)
	commit := baselineCommit
	reader := &fakeVersionReader{version: &versionapp.Version{
		ID:                 7,
		CodeRepositoryID:   &repoID,
		CodeBaselineCommit: &commit,
	}}
	query := &fakeCodeQuery{}
	adapter := mindmapcodeintel.NewCodeFactsAdapter(&fakeAnalysisReader{}, query, reader)

	exists, err := adapter.FileExists(ctx, 7, "internal/app/login.go")
	if err != nil {
		t.Fatalf("FileExists: %v", err)
	}
	if !exists {
		t.Fatal("FileExists=false want true for a readable file")
	}
	if len(query.fileCalls) != 1 {
		t.Fatalf("file calls=%+v want 1", query.fileCalls)
	}
	call := query.fileCalls[0]
	if call.actorID != 0 || call.repoID != repoID || call.commit != commit || call.path != "internal/app/login.go" {
		t.Fatalf("file call=%+v want actor 0, repo %d, commit %q", call, repoID, commit)
	}

	query.fileErr = codeinteldomain.NewInvalid("path", "path_not_found", "codeintel: path not found")
	exists, err = adapter.FileExists(ctx, 7, "internal/app/missing.go")
	if err != nil {
		t.Fatalf("FileExists(missing): %v", err)
	}
	if exists {
		t.Fatal("FileExists=true want false for path_not_found")
	}
}

// TestCodeFactsAdapterFileExistsSkipsUnboundVersions pins that a version without
// a code binding reports false without touching the code reader.
func TestCodeFactsAdapterFileExistsSkipsUnboundVersions(t *testing.T) {
	ctx := context.Background()
	query := &fakeCodeQuery{}
	adapter := mindmapcodeintel.NewCodeFactsAdapter(&fakeAnalysisReader{}, query, &fakeVersionReader{version: &versionapp.Version{ID: 7}})

	exists, err := adapter.FileExists(ctx, 7, "a.go")
	if err != nil {
		t.Fatalf("FileExists: %v", err)
	}
	if exists {
		t.Fatal("FileExists=true want false without a code binding")
	}
	if len(query.fileCalls) != 0 {
		t.Fatalf("file calls=%+v want none", query.fileCalls)
	}
}

// TestCodeFactsAdapterListFactsMapsEntitiesAndLimit pins the fact projection and
// the limit/baseline filter forwarded to the code reader.
func TestCodeFactsAdapterListFactsMapsEntitiesAndLimit(t *testing.T) {
	ctx := context.Background()
	repoID := int64(9)
	commit := baselineCommit
	factKey := "route:/login"
	query := &fakeCodeQuery{entities: []codeinteldomain.CodeEntity{
		{Kind: codeinteldomain.KindRoute, Path: "src/routes.ts", Name: "login", Signature: "POST /login", FactKey: &factKey},
		{Kind: codeinteldomain.KindFile, Path: "src/app.ts", Name: "app", Signature: "main entry"},
	}}
	adapter := mindmapcodeintel.NewCodeFactsAdapter(&fakeAnalysisReader{}, query, &fakeVersionReader{version: &versionapp.Version{
		ID:                 7,
		CodeRepositoryID:   &repoID,
		CodeBaselineCommit: &commit,
	}})

	facts, err := adapter.ListFacts(ctx, 7, 5)
	if err != nil {
		t.Fatalf("ListFacts: %v", err)
	}
	if len(facts) != 2 {
		t.Fatalf("mapped %d facts, want 2", len(facts))
	}
	if facts[0].Kind != codeinteldomain.KindRoute || facts[0].Key != factKey || facts[0].Path != "src/routes.ts" ||
		facts[0].Name != "login" || facts[0].Detail != "POST /login" {
		t.Fatalf("first fact=%+v", facts[0])
	}
	if facts[1].Key != "src/app.ts" {
		t.Fatalf("fallback key=%q want the path", facts[1].Key)
	}
	if query.factsRepo != repoID || query.factsFilter.Commit != commit || query.factsFilter.Limit != 5 {
		t.Fatalf("facts call repo/commit/limit=%d/%q/%d want %d/%q/5", query.factsRepo, query.factsFilter.Commit, query.factsFilter.Limit, repoID, commit)
	}
}

// TestCodeFactsAdapterMapsOperationalErrors pins that a non-domain codeintel
// failure is returned unchanged.
func TestCodeFactsAdapterMapsOperationalErrors(t *testing.T) {
	ctx := context.Background()
	operational := errors.New("git checkout is gone")
	adapter := mindmapcodeintel.NewCodeFactsAdapter(&fakeAnalysisReader{err: operational}, &fakeCodeQuery{}, &fakeVersionReader{})
	if _, err := adapter.GetLatestAnalysis(ctx, 7); !errors.Is(err, operational) {
		t.Fatalf("GetLatestAnalysis error=%v want the operational error", err)
	}
}

// TestCodeFactsAdapterListBaselineFiles covers the T0 fallback: paths come from
// the version's baseline commit tree and directories are skipped.
func TestCodeFactsAdapterListBaselineFiles(t *testing.T) {
	ctx := context.Background()
	repoID := int64(9)
	commit := baselineCommit
	reader := &fakeVersionReader{version: &versionapp.Version{
		ID:                 7,
		CodeRepositoryID:   &repoID,
		CodeBaselineCommit: &commit,
	}}
	query := &fakeCodeQuery{treeEntries: []codeinteldomain.TreeEntry{
		{Path: "src", Type: "tree"},
		{Path: "src/order.ts", Type: "blob"},
		{Path: "src/order/page.tsx", Type: "blob"},
	}}
	adapter := mindmapcodeintel.NewCodeFactsAdapter(&fakeAnalysisReader{}, query, reader)

	paths, err := adapter.ListBaselineFiles(ctx, 7, 10)
	if err != nil {
		t.Fatalf("ListBaselineFiles: %v", err)
	}
	if len(paths) != 2 || paths[0] != "src/order.ts" || paths[1] != "src/order/page.tsx" {
		t.Fatalf("paths=%v want the two blob paths", paths)
	}
	if query.treeRepo != repoID || query.treeCommit != commit {
		t.Fatalf("tree read repo=%d commit=%q want %d/%q", query.treeRepo, query.treeCommit, repoID, commit)
	}
}
