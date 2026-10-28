package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	agenttools "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/tools"
	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	codeinteldomain "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/config"
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

// stubToolIndex, stubToolQuery, stubToolAnalysis and stubToolAnchors are the
// minimal codeintel ports the nine P4 agent tools consume. The test below only
// builds the tool set, so every method can answer empty successfully.
type stubToolIndex struct{}

func (stubToolIndex) GetLatest(context.Context, int64) (codeinteldomain.IndexRun, error) {
	return codeinteldomain.IndexRun{}, nil
}

type stubToolQuery struct{}

func (stubToolQuery) Search(context.Context, int64, int64, string, string, string, int, string) ([]codeinteldomain.CodeEntity, string, bool, error) {
	return nil, "", false, nil
}

func (stubToolQuery) Diff(context.Context, int64, string, string, int, string) (codeinteldomain.ChangeSetData, error) {
	return codeinteldomain.ChangeSetData{}, nil
}

func (stubToolQuery) Facts(context.Context, int64, codeinteldomain.EntityFilter) ([]codeinteldomain.CodeEntity, string, bool, error) {
	return nil, "", false, nil
}

type stubToolAnalysis struct{}

func (stubToolAnalysis) Start(context.Context, int64, int64, codeintelapp.StartAnalysisInput) (codeinteldomain.CodeAnalysis, bool, error) {
	return codeinteldomain.CodeAnalysis{}, false, nil
}

func (stubToolAnalysis) GetLatest(context.Context, int64) (codeinteldomain.CodeAnalysis, error) {
	return codeinteldomain.CodeAnalysis{}, nil
}

func (stubToolAnalysis) Get(context.Context, int64, int64) (codeinteldomain.CodeAnalysis, error) {
	return codeinteldomain.CodeAnalysis{}, nil
}

func (stubToolAnalysis) ListCandidates(context.Context, int64) ([]codeinteldomain.ImpactCandidate, error) {
	return nil, nil
}

type stubToolAnchors struct{}

func (stubToolAnchors) List(context.Context, int64, codeinteldomain.AnchorFilter) ([]codeinteldomain.CodeAnchor, error) {
	return nil, nil
}

// stubLLMCompleter is a non-nil codeintel LLMCompleter for the degradation test.
type stubLLMCompleter struct{}

func (stubLLMCompleter) Complete(context.Context, string, string) (string, error) { return "", nil }

func (stubLLMCompleter) Model() string { return "stub" }

// TestCodeIntelToolsRegistersNine proves the composition root's P4 tool helper
// returns the nine tools (design spec §8.1), in the frozen order. The HTTP
// catalog test in internal/server builds its own registry fixture; this test
// covers the helper main.go actually appends before NewToolRegistry.
func TestCodeIntelToolsRegistersNine(t *testing.T) {
	tools := codeIntelTools(agenttools.CodeIntelToolDeps{
		Repos:    codeIntelRepoCatalog{repos: stubCodeIntelRepoCatalog{}},
		Indexes:  stubToolIndex{},
		Queries:  stubToolQuery{},
		Analyses: stubToolAnalysis{},
		Anchors:  stubToolAnchors{},
	})
	want := []string{
		"code.repos", "code.search", "code.diff", "code.facts", "code.routes",
		"code.apis", "code.analyze", "code.impact", "code.anchors",
	}
	if len(tools) != len(want) {
		t.Fatalf("codeIntelTools returned %d tools, want %d", len(tools), len(want))
	}
	for index, tool := range tools {
		if tool == nil {
			t.Fatalf("codeIntelTools[%d] is nil, want a tool", index)
		}
		if name := tool.Name(); name != want[index] {
			t.Fatalf("codeIntelTools[%d]=%q, want %q", index, name, want[index])
		}
	}

	// A missing port disables the whole set (NewCodeIntelTools contract): the
	// composition root must keep injecting all five ports.
	partial := codeIntelTools(agenttools.CodeIntelToolDeps{
		Repos: codeIntelRepoCatalog{repos: stubCodeIntelRepoCatalog{}},
	})
	if len(partial) != 0 {
		t.Fatalf("codeIntelTools with a missing port returned %d tools, want 0", len(partial))
	}
}

// TestCodeIntelShutdownTimeout proves the graceful worker stop budget follows
// the configured analysis timeout (design spec §6.8) instead of a fixed 10 s.
// It must stay positive and bounded even when Config carries an invalid value.
func TestCodeIntelShutdownTimeout(t *testing.T) {
	tests := []struct {
		name      string
		timeoutMS int
		want      time.Duration
	}{
		{"default 180s", 180000, 190 * time.Second},
		{"custom 300s", 300000, 310 * time.Second},
		{"max 600s", 600000, 610 * time.Second},
		{"zero falls back to default", 0, 190 * time.Second},
		{"negative falls back to default", -1000, 190 * time.Second},
		{"above max is capped", 900000, 610 * time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := codeintelShutdownTimeout(config.Config{CodeIntelAnalysisTimeoutMS: test.timeoutMS})
			if got != test.want {
				t.Fatalf("codeintelShutdownTimeout(%dms)=%v, want %v", test.timeoutMS, got, test.want)
			}
			if got <= 0 {
				t.Fatalf("codeintelShutdownTimeout(%dms)=%v, want a positive budget", test.timeoutMS, got)
			}
			// For every in-bounds timeout the budget must cover the run the
			// worker may still be finishing; the cap only shortens an invalid
			// above-max value, which config.Load never produces.
			if test.timeoutMS > 0 && test.timeoutMS <= 600000 &&
				got < time.Duration(test.timeoutMS)*time.Millisecond {
				t.Fatalf("budget %v does not cover the %dms analysis timeout", got, test.timeoutMS)
			}
		})
	}
}

// TestCodeIntelLLMGatewayFailureDegrades proves a gateway construction failure
// is the intentional degraded mode (design spec §6.2), not a startup failure:
// the composition root keeps serving with a nil completer and analyses fall
// back to deterministic results.
func TestCodeIntelLLMGatewayFailureDegrades(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if got := codeIntelLLMCompleter(nil, errors.New("llm_not_configured"), logger); got != nil {
		t.Fatalf("failed gateway produced completer %T, want nil (degraded mode)", got)
	}

	gateway := stubLLMCompleter{}
	got := codeIntelLLMCompleter(gateway, nil, logger)
	if got == nil {
		t.Fatal("successful gateway was dropped, want the configured completer")
	}
	if _, ok := got.(stubLLMCompleter); !ok {
		t.Fatalf("successful gateway became %T, want stubLLMCompleter", got)
	}
}
