package adapters

// R-EX-4/DB-19: the execution binding must carry every effective code
// repository of a version_sut (BindingView.Repositories), not only the single
// deterministic baseline repository. Legacy data without the multi-repository
// projection still falls back to the single CodeRepoID.

import (
	"context"
	"reflect"
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

func bindingWithRepositories(repositories ...string) versionapp.VersionView {
	rows := make([]versionapp.BindingRepositoryView, 0, len(repositories))
	for _, id := range repositories {
		rows = append(rows, versionapp.BindingRepositoryView{
			Repository: resourceapp.CodeRepositoryView{ID: id},
			Role:       "primary",
			Source:     "sut",
		})
	}
	return versionapp.VersionView{
		ID: 100,
		Bindings: []versionapp.BindingView{{
			ID:           5001,
			SUT:          resourceapp.SUTView{ID: "2", Name: "sut"},
			Environment:  resourceapp.EnvironmentView{ID: "3", Name: "env", BaseURL: "http://127.0.0.1:13333"},
			Repository:   &resourceapp.CodeRepositoryView{ID: "77", Branch: "main"},
			Repositories: rows,
		}},
	}
}

func TestVersionAdapterMapsEveryEffectiveRepository(t *testing.T) {
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
		t.Fatalf("repository_ids = %v, want the deduplicated ascending effective set %v", got, want)
	}
	if binding.SUTs[0].CodeRepoID == nil || *binding.SUTs[0].CodeRepoID != 77 {
		t.Fatalf("CodeRepoID = %v, want the single baseline repository 77", binding.SUTs[0].CodeRepoID)
	}
}

func TestVersionAdapterFallsBackToSingleRepository(t *testing.T) {
	view := bindingWithRepositories()
	adapter := NewVersionAdapter(repositoryVersionService{view: view})
	binding, err := adapter.Get(context.Background(), 100)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got := binding.SUTs[0].RepositoryIDs
	if want := []int64{77}; !reflect.DeepEqual(got, want) {
		t.Fatalf("repository_ids = %v, want the legacy single-repository fallback %v", got, want)
	}
}
