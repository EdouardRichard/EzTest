package adapters

// R-EX-4/DB-19: the execution binding must carry every repository of the
// environment code pool (v0.2.4-B GD-03), not only the single deterministic
// baseline repository. The pool is derived from the SUTs of the environment the
// version binds, so it is de-duplicated and ordered ascending by repository ID.
//
// V-0024: each repository is paired with its OWN pinned commit. The former
// adapter borrowed the version-level catalog.CodeBaselineCommit for the single
// baseline id, which could be a different repository's commit; the union now
// reads every row's own sut_repository.code_baseline_commit.

import (
	"context"
	"reflect"
	"strings"
	"testing"

	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

type repositoryVersionService struct {
	view versionapp.VersionView
}

func (s repositoryVersionService) Get(context.Context, int64) (versionapp.VersionView, error) {
	return s.view, nil
}

// bindingWithRepositories builds a pool whose every row carries its own pinned
// commit (repositoryCommit). The rows are passed in creation order to prove the
// adapter normalizes the union ascending by repository ID.
func bindingWithRepositories(repositories ...string) versionapp.VersionView {
	rows := make([]versionapp.BindingRepository, 0, len(repositories))
	for _, id := range repositories {
		rows = append(rows, versionapp.BindingRepository{
			Repository:         resourceapp.CodeRepositoryView{ID: id, Branch: "main"},
			SUTID:              2,
			Role:               "primary",
			CodeBaselineCommit: repositoryCommit,
		})
	}
	baseline := ""
	if len(rows) > 0 {
		baseline = rows[0].CodeBaselineCommit
	}
	return versionapp.VersionView{
		ID: 100, EnvironmentID: 3,
		Binding: versionapp.EnvironmentSUTCatalog{
			VersionID: 100, EnvironmentID: 3,
			Environment: resourceapp.EnvironmentView{
				ID: "3", Name: "env",
				SUTs: []resourceapp.SUTView{{
					ID: "2", EnvironmentID: "3", Name: "sut", BaseURL: "http://127.0.0.1:13333",
				}},
			},
			Repositories:       rows,
			CodeBaselineCommit: baseline,
		},
	}
}

func TestVersionAdapterMapsEveryEnvironmentRepository(t *testing.T) {
	adapter := NewVersionAdapter(repositoryVersionService{view: bindingWithRepositories("9", "7", "9")})
	binding, err := adapter.Get(context.Background(), 100)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(binding.SUTs) != 1 {
		t.Fatalf("SUTs = %d, want 1", len(binding.SUTs))
	}
	got := binding.SUTs[0].RepositoryIDs
	t.Logf("binding repository_ids = %v (CodeRepoID=%v)", got, binding.SUTs[0].CodeRepoID)
	if want := []int64{7, 9}; !reflect.DeepEqual(got, want) {
		t.Fatalf("repository_ids = %v, want the deduplicated ascending pool %v", got, want)
	}
	// The deterministic baseline repository is the first row of the pool.
	if binding.SUTs[0].CodeRepoID == nil || *binding.SUTs[0].CodeRepoID != 9 {
		t.Fatalf("CodeRepoID = %v, want the first pool repository 9", binding.SUTs[0].CodeRepoID)
	}
	// V-0024: the union pairs each repository with its own commit, ascending.
	baselines := binding.SUTs[0].RepositoryBaselines
	if len(baselines) != 2 || baselines[0].RepoID != 7 || baselines[1].RepoID != 9 {
		t.Fatalf("repository_baselines = %+v, want the ascending deduplicated pool [7 9]", baselines)
	}
	for _, baseline := range baselines {
		if baseline.Commit != repositoryCommit {
			t.Fatalf("repository %d commit = %q, want its own pinned %q", baseline.RepoID, baseline.Commit, repositoryCommit)
		}
	}
}

func TestVersionAdapterWithoutRepositoriesHasNoBaseline(t *testing.T) {
	view := bindingWithRepositories()
	adapter := NewVersionAdapter(repositoryVersionService{view: view})
	binding, err := adapter.Get(context.Background(), 100)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got := binding.SUTs[0].RepositoryIDs
	if len(got) != 0 {
		t.Fatalf("repository_ids = %v, want none for an empty environment pool", got)
	}
	if binding.SUTs[0].CodeRepoID != nil {
		t.Fatalf("CodeRepoID = %v, want nil without a pool", binding.SUTs[0].CodeRepoID)
	}
	if len(binding.SUTs[0].RepositoryBaselines) != 0 {
		t.Fatalf("repository_baselines = %+v, want none without a pool", binding.SUTs[0].RepositoryBaselines)
	}
}

// TestVersionAdapterPairsEachRepositoryWithItsOwnCommit pins the V-0024 mismatch
// fix: a repository whose own commit is unresolved keeps an empty commit in the
// union — it must NOT borrow a later repository's resolved commit. The former
// first-non-empty-across-repos pairing (baselineCommitOf) paired Repositories[0]'s
// id with another repository's commit.
func TestVersionAdapterPairsEachRepositoryWithItsOwnCommit(t *testing.T) {
	commitB := strings.Repeat("b", 40)
	view := versionapp.VersionView{
		ID: 100, EnvironmentID: 3,
		Binding: versionapp.EnvironmentSUTCatalog{
			VersionID: 100, EnvironmentID: 3,
			Environment: resourceapp.EnvironmentView{
				ID: "3", Name: "env",
				SUTs: []resourceapp.SUTView{{
					ID: "2", EnvironmentID: "3", Name: "sut", BaseURL: "http://127.0.0.1:13333",
				}},
			},
			// Repo 7 (first) is unresolved; repo 9 (later) is pinned. The union
			// must keep repo 7 empty and repo 9 = commitB, never repo 7 = commitB.
			Repositories: []versionapp.BindingRepository{
				{Repository: resourceapp.CodeRepositoryView{ID: "7", Branch: "main"}, SUTID: 2, CodeBaselineCommit: ""},
				{Repository: resourceapp.CodeRepositoryView{ID: "9", Branch: "main"}, SUTID: 2, CodeBaselineCommit: commitB},
			},
			CodeBaselineCommit: "",
		},
	}
	adapter := NewVersionAdapter(repositoryVersionService{view: view})
	binding, err := adapter.Get(context.Background(), 100)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	baselines := binding.SUTs[0].RepositoryBaselines
	if len(baselines) != 2 {
		t.Fatalf("repository_baselines = %+v, want two pairs", baselines)
	}
	if baselines[0].RepoID != 7 || baselines[0].Commit != "" {
		t.Fatalf("repo 7 pair = %+v, want its OWN empty commit (not borrowed)", baselines[0])
	}
	if baselines[1].RepoID != 9 || baselines[1].Commit != commitB {
		t.Fatalf("repo 9 pair = %+v, want its own commit %q", baselines[1], commitB)
	}
}
