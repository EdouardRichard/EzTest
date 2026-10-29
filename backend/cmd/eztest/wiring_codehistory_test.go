package main

// This file pins the Task 17d composition-root fix: BatchServiceDeps.CodeHistory
// must be wired to a real codeintel-backed adapter so run_batch.head_commit is
// pinned at batch creation and the autonomy worker's P4 impact step runs
// automatically (spec §6.1 step 3b/§6.4/D49).
//
// The tests drive the exact helper main.go uses, the same public
// execution/application ports the worker consumes, and the source of main.go
// itself, so the original regression (a bare composition-root omission that no
// package test could see) fails the suite.

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	executiondomain "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

const wiringCodeHistoryHead = "3333333333333333333333333333333333333333"

// wiringCodeHistoryVersions is a codeintel VersionCatalog double: the adapter
// only reads Get, so the remaining methods stay on the embedded nil interface.
type wiringCodeHistoryVersions struct {
	codeintelapp.VersionCatalog
	info codeintelapp.VersionInfo
	err  error
}

func (v wiringCodeHistoryVersions) Get(context.Context, int64) (codeintelapp.VersionInfo, error) {
	if v.err != nil {
		return codeintelapp.VersionInfo{}, v.err
	}
	return v.info, nil
}

// wiringCodeHistoryRepos is a codeintel RepoCatalog double.
type wiringCodeHistoryRepos struct {
	codeintelapp.RepoCatalog
	repo codeintelapp.RepoInfo
	err  error
}

func (r wiringCodeHistoryRepos) Get(context.Context, int64) (codeintelapp.RepoInfo, error) {
	if r.err != nil {
		return codeintelapp.RepoInfo{}, r.err
	}
	return r.repo, nil
}

// wiringCodeHistoryGit records the read-only Git calls of the adapter.
type wiringCodeHistoryGit struct {
	codeintelapp.GitReader
	head        string
	headErr     error
	ancestor    bool
	ancestorErr error
	headCalls   []wiringCodeHistoryGitCall
}

type wiringCodeHistoryGitCall struct {
	localPath string
	branch    string
}

func (g *wiringCodeHistoryGit) Head(_ context.Context, localPath, branch string) (string, error) {
	g.headCalls = append(g.headCalls, wiringCodeHistoryGitCall{localPath: localPath, branch: branch})
	if g.headErr != nil {
		return "", g.headErr
	}
	return g.head, nil
}

func (g *wiringCodeHistoryGit) IsAncestor(_ context.Context, localPath, base, head string) (bool, error) {
	if g.ancestorErr != nil {
		return false, g.ancestorErr
	}
	return g.ancestor, nil
}

// TestExecutionCodeHistoryAdapterPinsFixedBranchHead proves the adapter the
// composition root wires resolves the version's repository fixed-branch tip and
// forwards the ancestry check: it must use the P2 fixed branch (never the
// display-only version code branch), exactly like P4 (D49/spec §6.1).
func TestExecutionCodeHistoryAdapterPinsFixedBranchHead(t *testing.T) {
	git := &wiringCodeHistoryGit{head: wiringCodeHistoryHead, ancestor: true}
	adapter := executionCodeHistory(
		wiringCodeHistoryVersions{info: codeintelapp.VersionInfo{ID: 7, HasRepo: true, RepoID: 42}},
		wiringCodeHistoryRepos{repo: codeintelapp.RepoInfo{
			ID: 42, LocalPath: `C:\repos\app`, FixedBranch: "release/2.0", Status: "active",
		}},
		git,
	)
	if adapter == nil {
		t.Fatal("executionCodeHistory returned nil")
	}

	head, err := adapter.HeadCommit(context.Background(), 7)
	if err != nil || head != wiringCodeHistoryHead {
		t.Fatalf("HeadCommit = %q/%v, want the fixed-branch tip", head, err)
	}
	if len(git.headCalls) != 1 || git.headCalls[0].localPath != `C:\repos\app` ||
		git.headCalls[0].branch != "release/2.0" {
		t.Fatalf("Head calls = %+v, want the repository local path and fixed branch", git.headCalls)
	}

	ancestor, err := adapter.IsAncestor(context.Background(), 7,
		"1111111111111111111111111111111111111111", wiringCodeHistoryHead)
	if err != nil || !ancestor {
		t.Fatalf("IsAncestor = %v/%v, want true/nil", ancestor, err)
	}
}

// TestExecutionCodeHistoryAdapterDegradesWithoutRepoBinding proves a version
// without a code repository binding is an error, never a panic: BatchService
// turns that error into head_commit=NULL and keeps the batch creatable
// (spec §6.1 "无代码仓" degraded mode).
func TestExecutionCodeHistoryAdapterDegradesWithoutRepoBinding(t *testing.T) {
	git := &wiringCodeHistoryGit{head: wiringCodeHistoryHead}
	adapter := executionCodeHistory(
		wiringCodeHistoryVersions{info: codeintelapp.VersionInfo{ID: 7}},
		wiringCodeHistoryRepos{},
		git,
	)
	if _, err := adapter.HeadCommit(context.Background(), 7); err == nil {
		t.Fatal("HeadCommit without a code repository binding must report an error")
	}
	if _, err := adapter.IsAncestor(context.Background(), 7,
		"1111111111111111111111111111111111111111", wiringCodeHistoryHead); err == nil {
		t.Fatal("IsAncestor without a code repository binding must report an error")
	}
	if len(git.headCalls) != 0 {
		t.Fatalf("Git was called for an unbound version: %+v", git.headCalls)
	}

	// A completely unwired adapter must fail closed instead of panicking.
	missing := executionCodeHistory(nil, nil, nil)
	if _, err := missing.HeadCommit(context.Background(), 7); err == nil {
		t.Fatal("a nil adapter must report a configuration error")
	}
	if _, err := missing.IsAncestor(context.Background(), 7,
		"1111111111111111111111111111111111111111", wiringCodeHistoryHead); err == nil {
		t.Fatal("a nil adapter must report a configuration error")
	}
}

// TestCompositionRootWiresCodeHistory proves main.go actually passes the helper
// into BatchServiceDeps. main.go has no runnable unit seam (it opens PostgreSQL
// and the HTTP server), so the test parses the composition root and fails on the
// exact regression Task 17d fixes: a missing or non-helper CodeHistory field.
func TestCompositionRootWiresCodeHistory(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}
	wired := false
	ast.Inspect(file, func(node ast.Node) bool {
		composite, ok := node.(*ast.CompositeLit)
		if !ok || len(composite.Elts) == 0 {
			return true
		}
		selector, ok := composite.Type.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "BatchServiceDeps" {
			return true
		}
		for _, element := range composite.Elts {
			pair, ok := element.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := pair.Key.(*ast.Ident)
			if !ok || key.Name != "CodeHistory" {
				continue
			}
			call, ok := pair.Value.(*ast.CallExpr)
			if !ok {
				t.Fatalf("BatchServiceDeps.CodeHistory must be the executionCodeHistory(...) call")
			}
			function, ok := call.Fun.(*ast.Ident)
			if !ok || function.Name != "executionCodeHistory" {
				t.Fatalf("BatchServiceDeps.CodeHistory must call executionCodeHistory, got %T", call.Fun)
			}
			if len(call.Args) != 3 {
				t.Fatalf("executionCodeHistory has %d arguments, want the version/repo catalogs and Git reader", len(call.Args))
			}
			wantArgs := []string{"codeIntelVersions", "codeIntelRepos", "codeIntelGit"}
			for index, argument := range call.Args {
				identifier, ok := argument.(*ast.Ident)
				if !ok || identifier.Name != wantArgs[index] {
					t.Fatalf("executionCodeHistory argument %d = %T, want %s", index, argument, wantArgs[index])
				}
			}
			wired = true
		}
		return true
	})
	if !wired {
		t.Fatal("BatchServiceDeps.CodeHistory is not wired in the composition root; run_batch.head_commit stays NULL and the autonomy impact step is skipped")
	}
}

// wiringCodeHistoryStore is the autonomy store double of the designing segment:
// only the two scans and the finalize sweep are read on this path.
type wiringCodeHistoryStore struct {
	executionapp.AutonomyBatchStore
	parent *executiondomain.RunBatch
}

func (s wiringCodeHistoryStore) PendingAutonomyBatches(context.Context, int) ([]executiondomain.RunBatch, error) {
	return nil, nil
}

func (s wiringCodeHistoryStore) DesigningVersions(context.Context, int) ([]executionapp.DesigningVersion, error) {
	if s.parent == nil {
		return nil, nil
	}
	return []executionapp.DesigningVersion{{
		Version: executionapp.VersionRef{
			ID: s.parent.VersionID, Status: "continuing", ContinuePhase: "designing",
		},
		Parent: s.parent,
	}}, nil
}

func (s wiringCodeHistoryStore) VersionsAwaitingFinalize(context.Context, int) ([]int64, error) {
	return nil, nil
}

// wiringCodeHistoryVersionsPort is the VersionPort double of the worker.
type wiringCodeHistoryVersionsPort struct {
	executionapp.VersionPort
	version executionapp.VersionRef
}

func (v wiringCodeHistoryVersionsPort) GetVersion(context.Context, int64) (executionapp.VersionRef, error) {
	return v.version, nil
}

// wiringCodeHistorySupplements is the SupplementPort double: no stored run, so
// the designing segment reaches the P4 impact step.
type wiringCodeHistorySupplements struct {
	executionapp.SupplementPort
}

func (wiringCodeHistorySupplements) GetSupplementRun(context.Context, int64) (*executionapp.SupplementRunView, error) {
	return nil, nil
}

// wiringCodeHistoryImpact records the automatic P4 impact start.
type wiringCodeHistoryImpact struct {
	executionapp.CodeImpactPort
	startCalls int
	base       string
	versionID  int64
}

func (i *wiringCodeHistoryImpact) LatestImpact(context.Context, int64) (*executionapp.ImpactView, error) {
	return nil, nil
}

func (i *wiringCodeHistoryImpact) StartImpact(_ context.Context, versionID int64, baseCommit string) (int64, error) {
	i.startCalls++
	i.versionID = versionID
	i.base = baseCommit
	return 55, nil
}

func (i *wiringCodeHistoryImpact) GetImpact(_ context.Context, _, analysisID int64) (*executionapp.ImpactView, error) {
	return &executionapp.ImpactView{ID: analysisID, Status: "queued", BaseCommit: i.base}, nil
}

// TestAutonomyImpactStartsFromPinnedBatchHead proves the wiring closes the
// worker gap: the head pinned by the CodeHistory adapter becomes the parent
// batch head, and one worker tick automatically starts the P4 impact analysis
// with that base (spec §6.1 step 3b/§6.4) - no manual trigger involved.
func TestAutonomyImpactStartsFromPinnedBatchHead(t *testing.T) {
	adapter := executionCodeHistory(
		wiringCodeHistoryVersions{info: codeintelapp.VersionInfo{ID: 7, HasRepo: true, RepoID: 42}},
		wiringCodeHistoryRepos{repo: codeintelapp.RepoInfo{
			ID: 42, LocalPath: `C:\repos\app`, FixedBranch: "main", Status: "active",
		}},
		&wiringCodeHistoryGit{head: wiringCodeHistoryHead},
	)
	head, err := adapter.HeadCommit(context.Background(), 7)
	if err != nil {
		t.Fatalf("HeadCommit: %v", err)
	}

	parent := &executiondomain.RunBatch{
		ID: 61, VersionID: 7, BatchNo: 1, Status: executiondomain.BatchStatusDone,
		Kind: executiondomain.BatchKindBaseline, HeadCommit: &head,
	}
	impact := &wiringCodeHistoryImpact{}
	worker := executionapp.NewAutonomyWorker(executionapp.AutonomyWorkerDeps{
		Store: wiringCodeHistoryStore{parent: parent},
		Versions: wiringCodeHistoryVersionsPort{version: executionapp.VersionRef{
			ID: 7, Status: "continuing", ContinuePhase: "designing",
		}},
		Supplements:    wiringCodeHistorySupplements{},
		Impact:         impact,
		BatchesPerTick: 4,
	})

	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if impact.startCalls != 1 {
		t.Fatalf("automatic StartImpact calls = %d, want 1", impact.startCalls)
	}
	if impact.versionID != 7 || impact.base != wiringCodeHistoryHead {
		t.Fatalf("StartImpact = version %d base %q, want version 7 base %q",
			impact.versionID, impact.base, wiringCodeHistoryHead)
	}
}
