package agentapp_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

/* ------------------------------- fakes ---------------------------------- */

type fakeAgentCenterReader struct {
	catalogProfiles []agentapp.CatalogProfileRow
	catalogErr      error

	stats    map[string]agentapp.RunStats
	statsErr error

	runs       []agentapp.RunRow
	runsHasMore bool
	runsErr     error
	lastQuery   agentapp.RunQuery

	catalogCalls int
	statsCalls   int
	runCalls     int
}

func (f *fakeAgentCenterReader) ListCatalogProfiles(_ context.Context) ([]agentapp.CatalogProfileRow, error) {
	f.catalogCalls++
	return f.catalogProfiles, f.catalogErr
}

func (f *fakeAgentCenterReader) ProfileRunStats(_ context.Context) (map[string]agentapp.RunStats, error) {
	f.statsCalls++
	return f.stats, f.statsErr
}

func (f *fakeAgentCenterReader) ListRuns(_ context.Context, query agentapp.RunQuery) ([]agentapp.RunRow, bool, error) {
	f.runCalls++
	f.lastQuery = query
	return f.runs, f.runsHasMore, f.runsErr
}

type fakeCenterVersionTitles struct {
	titles map[int64]string
	err    error
	ids    []int64
	calls  int
}

func (f *fakeCenterVersionTitles) VersionTitles(_ context.Context, ids []int64) (map[int64]string, error) {
	f.calls++
	f.ids = ids
	return f.titles, f.err
}

type fakeLatestEvalReader struct {
	summary *agentapp.EvalSummary
	err     error
	calls   int
}

func (f *fakeLatestEvalReader) LatestEval(_ context.Context) (*agentapp.EvalSummary, error) {
	f.calls++
	return f.summary, f.err
}

/* ------------------------------- helpers -------------------------------- */

func agentCenterCatalogRows(t *testing.T) []agentapp.CatalogProfileRow {
	t.Helper()
	profiles := agentapp.Profiles()
	if len(profiles) != 14 {
		t.Fatalf("seed profiles = %d, want 14", len(profiles))
	}
	rows := make([]agentapp.CatalogProfileRow, 0, len(profiles))
	for _, profile := range profiles {
		rows = append(rows, agentapp.CatalogProfileRow{
			Profile:       profile,
			PromptVersion: 1,
		})
	}
	return rows
}

func mustCatalog(t *testing.T, service *agentapp.AgentCenterService) agentapp.Catalog {
	t.Helper()
	catalog, err := service.Catalog(context.Background())
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	return catalog
}

/* -------------------------------- catalog ------------------------------- */

func TestAgentCenterCatalogGroupsFourteenProfiles(t *testing.T) {
	lastRun := time.Date(2026, 11, 1, 10, 30, 0, 0, time.UTC)
	reader := &fakeAgentCenterReader{
		catalogProfiles: agentCenterCatalogRows(t),
		stats: map[string]agentapp.RunStats{
			agentapp.ProfileKeyClarificationHost: {
				RunCount:        5,
				DoneCount:       2,
				FailedCount:     1,
				AbortedCount:    1,
				InterruptedCount: 0,
				LastRunAt:       &lastRun,
			},
		},
	}
	service := agentapp.NewAgentCenterService(reader, nil, nil)

	catalog := mustCatalog(t, service)
	if len(catalog.Profiles) != 14 {
		t.Fatalf("catalog profiles = %d, want 14", len(catalog.Profiles))
	}

	teams := make(map[string]agentapp.AgentTeam, len(catalog.Teams))
	for _, team := range catalog.Teams {
		teams[team.Team] = team
	}
	for _, want := range []string{"clarification", "design", "execution", "delivery", "steward", "code"} {
		if _, ok := teams[want]; !ok {
			t.Fatalf("team %q missing from %#v", want, catalog.Teams)
		}
	}
	if len(teams["clarification"].ProfileKeys) != 3 {
		t.Fatalf("clarification profiles = %v", teams["clarification"].ProfileKeys)
	}
	if len(teams["design"].ProfileKeys) != 5 {
		t.Fatalf("design profiles = %v", teams["design"].ProfileKeys)
	}
	if len(teams["execution"].ProfileKeys) != 3 {
		t.Fatalf("execution profiles = %v", teams["execution"].ProfileKeys)
	}
	if len(teams["delivery"].ProfileKeys) != 2 {
		t.Fatalf("delivery profiles = %v", teams["delivery"].ProfileKeys)
	}
	if len(teams["steward"].ProfileKeys) != 1 {
		t.Fatalf("steward profiles = %v", teams["steward"].ProfileKeys)
	}
	if len(teams["code"].ProfileKeys) != 0 || teams["code"].Note == "" {
		t.Fatalf("code team = %#v, want empty with note", teams["code"])
	}
	if catalog.Profiles[0].ProfileKey != agentapp.ProfileKeyClarificationHost {
		t.Fatalf("first profile = %q", catalog.Profiles[0].ProfileKey)
	}
	if catalog.Profiles[0].Order != 1 || catalog.Profiles[0].Team != "clarification" {
		t.Fatalf("first profile team/order = %#v", catalog.Profiles[0])
	}
	for i, profile := range catalog.Profiles {
		if profile.Order != i+1 {
			t.Fatalf("profile %d order = %d, want %d", i, profile.Order, i+1)
		}
		if profile.Mode != "agent" {
			t.Fatalf("profile %q mode = %q", profile.ProfileKey, profile.Mode)
		}
		if profile.PromptVersion != 1 {
			t.Fatalf("profile %q prompt version = %d", profile.ProfileKey, profile.PromptVersion)
		}
	}

	host := catalog.Profiles[0]
	if host.Stats.RunCount != 5 || host.Stats.DoneCount != 2 || host.Stats.FailedCount != 1 {
		t.Fatalf("host stats = %#v", host.Stats)
	}
	if host.Stats.AbortedCount != 1 {
		t.Fatalf("host aborted = %d", host.Stats.AbortedCount)
	}
	wantRate := 2.0 / 3.0
	if host.Stats.SuccessRate() != wantRate {
		t.Fatalf("success rate = %v, want %v", host.Stats.SuccessRate(), wantRate)
	}
	if host.Stats.LastRunAt == nil || !host.Stats.LastRunAt.Equal(lastRun) {
		t.Fatalf("last run = %v", host.Stats.LastRunAt)
	}

	// A profile without runs reports zero stats, never a missing block.
	curator := catalog.Profiles[1]
	if curator.Stats.RunCount != 0 || curator.Stats.SuccessRate() != 0 {
		t.Fatalf("empty stats = %#v", curator.Stats)
	}

	// The catalog read model must never carry the prompt or transcript.
	raw, err := json.Marshal(catalog)
	if err != nil {
		t.Fatalf("marshal catalog: %v", err)
	}
	text := strings.ToLower(string(raw))
	for _, forbidden := range []string{"system_prompt", "transcript", "allowed_tools\":"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("catalog leaks %q: %s", forbidden, text)
		}
	}
}

func TestAgentCenterCatalogReaderError(t *testing.T) {
	want := errors.New("boom")
	service := agentapp.NewAgentCenterService(&fakeAgentCenterReader{catalogErr: want}, nil, nil)
	if _, err := service.Catalog(context.Background()); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func TestAgentCenterCatalogWithoutReaderIsUnavailable(t *testing.T) {
	service := agentapp.NewAgentCenterService(nil, nil, nil)
	_, err := service.Catalog(context.Background())
	if err == nil || platform.AsError(err).Status != 503 {
		t.Fatalf("err = %v, want 503", err)
	}
}

/* --------------------------------- runs --------------------------------- */

func TestAgentCenterRunsJoinsWorkItemAndVersionTitles(t *testing.T) {
	started := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	finished := started.Add(90 * time.Second)
	workItemID := int64(501)
	versionID := int64(7)
	conversationID := int64(900)
	reason := "goal_met"
	workType := "execute.case"
	reader := &fakeAgentCenterReader{
		runs: []agentapp.RunRow{{
			ID:             31,
			ProfileKey:     agentapp.ProfileKeyCaseExecutor,
			WorkItemID:     &workItemID,
			WorkType:       &workType,
			VersionID:      &versionID,
			ConversationID: &conversationID,
			Status:         "done",
			TerminalReason: &reason,
			Turns:          9,
			Usage:          map[string]int{"input_tokens": 1200, "output_tokens": 300},
			StartedAt:      &started,
			FinishedAt:     &finished,
		}},
		runsHasMore: true,
	}
	versions := &fakeCenterVersionTitles{titles: map[int64]string{7: "订单系统 Q4"}}
	service := agentapp.NewAgentCenterService(reader, versions, nil)

	runs, hasMore, err := service.Runs(context.Background(), agentapp.RunFilter{
		ProfileKey: agentapp.ProfileKeyCaseExecutor,
		Status:     "done",
		VersionID:  &versionID,
		BeforeID:   agentCenterInt64Ptr(99),
		Limit:      999,
	})
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}
	if !hasMore {
		t.Fatal("hasMore = false, want true")
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %d", len(runs))
	}
	run := runs[0]
	if run.ID != 31 || run.ProfileKey != agentapp.ProfileKeyCaseExecutor {
		t.Fatalf("run = %#v", run)
	}
	if run.WorkItemID == nil || *run.WorkItemID != 501 || run.WorkType == nil || *run.WorkType != "execute.case" {
		t.Fatalf("work item fields = %#v", run)
	}
	if run.VersionID == nil || *run.VersionID != 7 || run.VersionTitle != "订单系统 Q4" {
		t.Fatalf("version fields = %#v", run)
	}
	if run.ConversationID == nil || *run.ConversationID != 900 {
		t.Fatalf("conversation = %v", run.ConversationID)
	}
	if run.Status != "done" || run.Turns != 9 || run.Usage["input_tokens"] != 1200 {
		t.Fatalf("run = %#v", run)
	}
	if run.StartedAt == nil || !run.StartedAt.Equal(started) || run.FinishedAt == nil {
		t.Fatalf("times = %#v", run)
	}

	query := reader.lastQuery
	if query.ProfileKey != agentapp.ProfileKeyCaseExecutor || query.Status != "done" {
		t.Fatalf("query = %#v", query)
	}
	if query.VersionID == nil || *query.VersionID != 7 || query.BeforeID == nil || *query.BeforeID != 99 {
		t.Fatalf("query filters = %#v", query)
	}
	if query.Limit != agentapp.MaxAgentRunLimit {
		t.Fatalf("limit = %d, want %d", query.Limit, agentapp.MaxAgentRunLimit)
	}
	if versions.calls != 1 || len(versions.ids) != 1 || versions.ids[0] != 7 {
		t.Fatalf("version batch = calls=%d ids=%v", versions.calls, versions.ids)
	}
}

func TestAgentCenterRunsDefaultLimitAndVersionTitleBatch(t *testing.T) {
	a, b := int64(7), int64(8)
	reader := &fakeAgentCenterReader{runs: []agentapp.RunRow{
		{ID: 2, ProfileKey: "case_executor", VersionID: &a, Status: "done"},
		{ID: 1, ProfileKey: "case_executor", VersionID: &b, Status: "done"},
	}}
	versions := &fakeCenterVersionTitles{titles: map[int64]string{7: "A"}}
	service := agentapp.NewAgentCenterService(reader, versions, nil)

	runs, _, err := service.Runs(context.Background(), agentapp.RunFilter{})
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}
	if reader.lastQuery.Limit != agentapp.DefaultAgentRunLimit {
		t.Fatalf("default limit = %d", reader.lastQuery.Limit)
	}
	if len(versions.ids) != 2 || versions.ids[0] != 7 || versions.ids[1] != 8 {
		t.Fatalf("version title ids = %v", versions.ids)
	}
	if runs[0].VersionTitle != "A" {
		t.Fatalf("run 0 title = %q", runs[0].VersionTitle)
	}
	if runs[1].VersionTitle != "" {
		t.Fatalf("run 1 title = %q, want empty for deleted version", runs[1].VersionTitle)
	}
}

func TestAgentCenterRunsRejectsInvalidFilters(t *testing.T) {
	zero, negative := int64(0), int64(-2)
	cases := []struct {
		name   string
		filter agentapp.RunFilter
		field  string
	}{
		{name: "status", filter: agentapp.RunFilter{Status: "exploded"}, field: "status"},
		{name: "profile key", filter: agentapp.RunFilter{ProfileKey: "Bad Key"}, field: "profile_key"},
		{name: "version", filter: agentapp.RunFilter{VersionID: &zero}, field: "version_id"},
		{name: "before", filter: agentapp.RunFilter{BeforeID: &negative}, field: "before"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader := &fakeAgentCenterReader{}
			service := agentapp.NewAgentCenterService(reader, nil, nil)
			_, _, err := service.Runs(context.Background(), tc.filter)
			if err == nil {
				t.Fatal("expected validation error")
			}
			apiErr := platform.AsError(err)
			if apiErr.Status != 400 || apiErr.Details["field"] != tc.field {
				t.Fatalf("err = %#v", apiErr)
			}
			if reader.runCalls != 0 {
				t.Fatalf("reader calls = %d, want 0", reader.runCalls)
			}
		})
	}
}

func TestAgentCenterRunsReaderError(t *testing.T) {
	want := errors.New("boom")
	service := agentapp.NewAgentCenterService(&fakeAgentCenterReader{runsErr: want}, nil, nil)
	if _, _, err := service.Runs(context.Background(), agentapp.RunFilter{}); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

/* -------------------------------- quality ------------------------------- */

func TestAgentCenterQualityAggregatesRunStatsAndLatestEval(t *testing.T) {
	created := time.Date(2026, 11, 1, 11, 0, 0, 0, time.UTC)
	lastRun := created.Add(-time.Hour)
	reader := &fakeAgentCenterReader{stats: map[string]agentapp.RunStats{
		"case_executor": {RunCount: 4, DoneCount: 3, FailedCount: 1, LastRunAt: &lastRun},
		"design_host":   {RunCount: 2, DoneCount: 1, FailedCount: 0, AbortedCount: 1},
	}}
	evals := &fakeLatestEvalReader{summary: &agentapp.EvalSummary{
		ID: 9, Mode: "real_llm", Status: "failed", CreatedAt: created,
		MetricsCount: 7, MetricsPassed: 6,
	}}
	service := agentapp.NewAgentCenterService(reader, nil, evals)

	quality, err := service.Quality(context.Background())
	if err != nil {
		t.Fatalf("Quality: %v", err)
	}
	if quality.Latest == nil || quality.Latest.ID != 9 || quality.Latest.MetricsPassed != 6 {
		t.Fatalf("latest = %#v", quality.Latest)
	}
	stats := quality.RunStats
	if stats.RunCount != 6 || stats.DoneCount != 4 || stats.FailedCount != 1 || stats.AbortedCount != 1 {
		t.Fatalf("run stats = %#v", stats)
	}
	wantRate := 4.0 / 5.0
	if stats.SuccessRate() != wantRate {
		t.Fatalf("success rate = %v, want %v", stats.SuccessRate(), wantRate)
	}
	if stats.LastRunAt == nil || !stats.LastRunAt.Equal(lastRun) {
		t.Fatalf("last run = %v", stats.LastRunAt)
	}
	if evals.calls != 1 {
		t.Fatalf("eval calls = %d", evals.calls)
	}
}

func TestAgentCenterQualityWithoutEvalOrRuns(t *testing.T) {
	service := agentapp.NewAgentCenterService(&fakeAgentCenterReader{}, nil, &fakeLatestEvalReader{})
	quality, err := service.Quality(context.Background())
	if err != nil {
		t.Fatalf("Quality: %v", err)
	}
	if quality.Latest != nil {
		t.Fatalf("latest = %#v, want null", quality.Latest)
	}
	if quality.RunStats.RunCount != 0 || quality.RunStats.SuccessRate() != 0 {
		t.Fatalf("stats = %#v", quality.RunStats)
	}
}

func TestAgentCenterQualityEvalErrorPropagates(t *testing.T) {
	want := errors.New("boom")
	service := agentapp.NewAgentCenterService(&fakeAgentCenterReader{}, nil, &fakeLatestEvalReader{err: want})
	if _, err := service.Quality(context.Background()); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func agentCenterInt64Ptr(value int64) *int64 { return &value }
