package main

import (
	"context"
	"errors"
	"testing"
	"time"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
)

// stubCodeIntelRepoCatalog is the codeintel application RepoCatalog fixture the
// composition root adapts to the agent tools' local projection.
type stubCodeIntelRepoCatalog struct {
	infos []codeintelapp.RepoInfo
	err   error
}

func (s stubCodeIntelRepoCatalog) Get(context.Context, int64) (codeintelapp.RepoInfo, error) {
	return codeintelapp.RepoInfo{}, errors.New("not used")
}

func (s stubCodeIntelRepoCatalog) List(context.Context) ([]codeintelapp.RepoInfo, error) {
	return s.infos, s.err
}

// TestCodeIntelRepoCatalogProjection proves the composition-root adapter that
// backs the code.repos agent tool: the P2 summary fields are forwarded to the
// tools' local CodeIntelRepo and an empty catalog yields an empty list.
func TestCodeIntelRepoCatalogProjection(t *testing.T) {
	indexedAt := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	adapter := codeIntelRepoCatalog{repos: stubCodeIntelRepoCatalog{infos: []codeintelapp.RepoInfo{{
		ID: 7, Key: "web", Name: "Web app", LocalPath: "/srv/repos/web",
		FixedBranch: "main", Status: "active", LastIndexedAt: &indexedAt,
	}}}}

	repos, err := adapter.ListRepos(context.Background())
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("ListRepos returned %d repos, want 1", len(repos))
	}
	got := repos[0]
	if got.ID != 7 || got.Key != "web" || got.Name != "Web app" || got.Status != "active" ||
		got.FixedBranch != "main" {
		t.Fatalf("projection=%+v, want the P2 summary fields", got)
	}
	if got.LastIndexedAt == nil || !got.LastIndexedAt.Equal(indexedAt) {
		t.Fatalf("LastIndexedAt=%v, want %v", got.LastIndexedAt, indexedAt)
	}

	empty := codeIntelRepoCatalog{repos: stubCodeIntelRepoCatalog{}}
	repos, err = empty.ListRepos(context.Background())
	if err != nil || len(repos) != 0 {
		t.Fatalf("empty catalog returned %v, %v; want an empty list", repos, err)
	}
}

// TestCodeIntelRepoCatalogPropagatesErrors proves a repository read failure
// reaches the agent tool instead of being hidden behind an empty list.
func TestCodeIntelRepoCatalogPropagatesErrors(t *testing.T) {
	want := errors.New("resource catalog down")
	adapter := codeIntelRepoCatalog{repos: stubCodeIntelRepoCatalog{err: want}}
	if _, err := adapter.ListRepos(context.Background()); !errors.Is(err, want) {
		t.Fatalf("ListRepos error=%v want %v", err, want)
	}
}
