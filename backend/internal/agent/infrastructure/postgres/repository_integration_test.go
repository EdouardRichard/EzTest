//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

var ctx = context.Background()

// seedProfile inserts the builtin profile through the repository and returns
// the stored row, which also covers EnsureBuiltin + Get + List wiring.
func seedProfile(t *testing.T, pool *pgxpool.Pool) agent.AgentProfile {
	t.Helper()
	profiles := postgres.NewProfileRepository(pool)
	if _, err := profiles.EnsureBuiltin(ctx, agent.BuiltinProfile()); err != nil {
		t.Fatalf("ensure builtin profile: %v", err)
	}
	all, err := profiles.List(ctx)
	if err != nil {
		t.Fatalf("list profiles: %v", err)
	}
	for _, profile := range all {
		if profile.Key == agent.BuiltinProfileKey {
			return profile
		}
	}
	t.Fatalf("builtin profile %q not found after EnsureBuiltin", agent.BuiltinProfileKey)
	return agent.AgentProfile{}
}

func seedSession(t *testing.T, pool *pgxpool.Pool) (agent.AgentProfile, agent.Session) {
	t.Helper()
	profile := seedProfile(t, pool)
	session, err := postgres.NewSessionRepository(pool).Create(ctx, profile.ID, "", nil)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return profile, session
}

func TestMigrateAndCreateTurnIsAtomic(t *testing.T) {
	pool := testutil.OpenTestDB(t)

	// The migration must create all four tables (migrating twice is covered by
	// the P0 migration tests).
	for _, table := range []string{"agent_profiles", "agent_sessions", "agent_runs", "agent_messages"} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatalf("check table %s: %v", table, err)
		}
		if !exists {
			t.Fatalf("table %s was not created by the migration", table)
		}
	}

	profile, session := seedSession(t, pool)
	if session.ID == 0 || session.ProfileID != profile.ID || session.ProfileKey != profile.Key {
		t.Fatalf("created session=%+v profile=%+v", session, profile)
	}
	if session.Title != "" {
		t.Fatalf("title=%q want empty", session.Title)
	}

	runs := postgres.NewRunRepository(pool)
	messages := postgres.NewMessageRepository(pool)
	ids, err := runs.CreateTurn(ctx, session.ID, []agent.Part{{Type: "text", Text: "hello"}}, "hello")
	if err != nil {
		t.Fatalf("create turn: %v", err)
	}
	if ids.RunID <= 0 || ids.UserMessageID <= 0 {
		t.Fatalf("turn ids=%+v, both ids must be positive", ids)
	}

	run, err := runs.Get(ctx, ids.RunID)
	if err != nil {
		t.Fatalf("get created run: %v", err)
	}
	if run.Status != agent.RunRunning || run.SessionID != session.ID || run.Turns != 0 || run.EndedAt != nil {
		t.Fatalf("created run=%+v", run)
	}

	message, err := messages.Get(ctx, ids.UserMessageID)
	if err != nil {
		t.Fatalf("get user message: %v", err)
	}
	if message.Role != "user" || message.RunID == nil || *message.RunID != ids.RunID {
		t.Fatalf("user message=%+v", message)
	}
	if len(message.Parts) != 1 || message.Parts[0].Type != "text" || message.Parts[0].Text != "hello" {
		t.Fatalf("user message parts=%+v", message.Parts)
	}

	stored, err := postgres.NewSessionRepository(pool).Get(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session after create turn: %v", err)
	}
	if stored.Title != "hello" {
		t.Fatalf("title=%q want the first message preview", stored.Title)
	}
	if stored.LastActivityAt.Before(stored.CreatedAt) {
		t.Fatalf("last_activity_at=%v is before created_at=%v", stored.LastActivityAt, stored.CreatedAt)
	}

	// A second turn while the first run is still running must fail on the
	// partial unique index and must not leave a second user message behind.
	if _, err := runs.CreateTurn(ctx, session.ID, []agent.Part{{Type: "text", Text: "again"}}, "again"); !errors.Is(err, agent.ErrRunActive) {
		t.Fatalf("second create turn err=%v, want ErrRunActive", err)
	}
	listed, err := messages.ListBySession(ctx, session.ID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("messages after failed turn=%d want 1", len(listed))
	}
	var runCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_runs WHERE session_id = $1`, session.ID).Scan(&runCount); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if runCount != 1 {
		t.Fatalf("runs after failed turn=%d want 1", runCount)
	}
	stored, err = postgres.NewSessionRepository(pool).Get(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session after failed turn: %v", err)
	}
	if stored.Title != "hello" {
		t.Fatalf("title=%q, the failed turn must not change it", stored.Title)
	}
}

func TestEnsureBuiltinAndMarkInterrupted(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	profiles := postgres.NewProfileRepository(pool)

	created, err := profiles.EnsureBuiltin(ctx, agent.BuiltinProfile())
	if err != nil {
		t.Fatalf("first EnsureBuiltin: %v", err)
	}
	if !created {
		t.Fatal("first EnsureBuiltin reported created=false")
	}

	overwrite := agent.BuiltinProfile()
	overwrite.Name = "覆盖后的名字"
	overwrite.MaxTurns = 99
	created, err = profiles.EnsureBuiltin(ctx, overwrite)
	if err != nil {
		t.Fatalf("second EnsureBuiltin: %v", err)
	}
	if created {
		t.Fatal("second EnsureBuiltin reported created=true")
	}
	listed, err := profiles.List(ctx)
	if err != nil {
		t.Fatalf("list profiles: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("profiles=%d want 1", len(listed))
	}
	stored := listed[0]
	if stored.Name != "运行时验证 Agent" || stored.MaxTurns != 12 {
		t.Fatalf("EnsureBuiltin overwrote the seeded profile: %+v", stored)
	}

	_, session := seedSession(t, pool)
	runs := postgres.NewRunRepository(pool)

	first, err := runs.CreateTurn(ctx, session.ID, []agent.Part{{Type: "text", Text: "first"}}, "first")
	if err != nil {
		t.Fatalf("create first turn: %v", err)
	}
	if err := runs.Finish(ctx, first.RunID, agent.RunResult{
		Status:         agent.RunDone,
		TerminalReason: agent.ReasonCompleted,
		Turns:          1,
	}); err != nil {
		t.Fatalf("finish first run: %v", err)
	}

	second, err := runs.CreateTurn(ctx, session.ID, []agent.Part{{Type: "text", Text: "second"}}, "second")
	if err != nil {
		t.Fatalf("create second turn: %v", err)
	}

	marked, err := runs.MarkRunningInterrupted(ctx)
	if err != nil {
		t.Fatalf("mark running interrupted: %v", err)
	}
	if marked != 1 {
		t.Fatalf("marked=%d want 1", marked)
	}
	interrupted, err := runs.Get(ctx, second.RunID)
	if err != nil {
		t.Fatalf("get interrupted run: %v", err)
	}
	if interrupted.Status != agent.RunInterrupted || interrupted.TerminalReason != agent.ReasonInterrupted {
		t.Fatalf("interrupted run=%+v", interrupted)
	}
	if interrupted.EndedAt == nil {
		t.Fatal("interrupted run has no ended_at")
	}
	finished, err := runs.Get(ctx, first.RunID)
	if err != nil {
		t.Fatalf("get finished run: %v", err)
	}
	if finished.Status != agent.RunDone || finished.TerminalReason != agent.ReasonCompleted || finished.EndedAt == nil {
		t.Fatalf("terminal run was changed by MarkRunningInterrupted: %+v", finished)
	}

	marked, err = runs.MarkRunningInterrupted(ctx)
	if err != nil {
		t.Fatalf("second mark running interrupted: %v", err)
	}
	if marked != 0 {
		t.Fatalf("second mark=%d want 0", marked)
	}
}

func TestMessagePlaceholderLifecycle(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	_, session := seedSession(t, pool)
	runs := postgres.NewRunRepository(pool)
	messages := postgres.NewMessageRepository(pool)

	turn, err := runs.CreateTurn(ctx, session.ID, []agent.Part{{Type: "text", Text: "question"}}, "question")
	if err != nil {
		t.Fatalf("create turn: %v", err)
	}

	messageID, err := messages.AppendAssistantPlaceholder(ctx, session.ID, turn.RunID)
	if err != nil {
		t.Fatalf("append assistant placeholder: %v", err)
	}
	if messageID <= 0 {
		t.Fatalf("placeholder id=%d", messageID)
	}
	placeholder, err := messages.Get(ctx, messageID)
	if err != nil {
		t.Fatalf("get placeholder: %v", err)
	}
	if placeholder.Role != "assistant" || placeholder.SessionID != session.ID {
		t.Fatalf("placeholder=%+v", placeholder)
	}
	if placeholder.RunID == nil || *placeholder.RunID != turn.RunID {
		t.Fatalf("placeholder run_id=%v want %d", placeholder.RunID, turn.RunID)
	}
	if len(placeholder.Parts) != 0 || placeholder.Reasoning != "" || len(placeholder.ToolCalls) != 0 || placeholder.Usage != nil {
		t.Fatalf("placeholder is not empty: %+v", placeholder)
	}

	parts := []agent.Part{{Type: "text", Text: "answer"}}
	calls := []agent.ToolCall{{ID: "call_1", Name: "file.read", Arguments: `{"path":"README.md"}`}}
	usage := &agent.Usage{PromptTokens: 10, CompletionTokens: 20, ReasoningTokens: 30, TotalTokens: 60}
	if err := messages.CompleteAssistant(ctx, messageID, parts, "why", calls, usage); err != nil {
		t.Fatalf("complete assistant: %v", err)
	}
	completed, err := messages.Get(ctx, messageID)
	if err != nil {
		t.Fatalf("get completed assistant: %v", err)
	}
	if !slices.Equal(completed.Parts, parts) {
		t.Fatalf("parts=%+v want %+v", completed.Parts, parts)
	}
	if completed.Reasoning != "why" {
		t.Fatalf("reasoning=%q", completed.Reasoning)
	}
	if !slices.Equal(completed.ToolCalls, calls) {
		t.Fatalf("tool_calls=%+v want %+v", completed.ToolCalls, calls)
	}
	if completed.Usage == nil || *completed.Usage != *usage {
		t.Fatalf("usage=%+v want %+v", completed.Usage, usage)
	}
	if completed.RunID == nil || *completed.RunID != turn.RunID {
		t.Fatalf("completed run_id=%v want %d", completed.RunID, turn.RunID)
	}

	listed, err := messages.ListBySession(ctx, session.ID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(listed) != 2 || listed[0].Role != "user" || listed[1].Role != "assistant" {
		t.Fatalf("messages=%+v", listed)
	}

	toolRunID := turn.RunID
	appended, err := messages.Append(ctx, agent.Message{
		SessionID:  session.ID,
		RunID:      &toolRunID,
		Role:       "tool",
		Parts:      []agent.Part{{Type: "text", Text: "file contents"}},
		ToolCallID: "call_1",
		ToolName:   "file.read",
	})
	if err != nil {
		t.Fatalf("append tool message: %v", err)
	}
	if appended.ID <= 0 || appended.Role != "tool" || appended.ToolCallID != "call_1" || appended.ToolName != "file.read" {
		t.Fatalf("appended tool message=%+v", appended)
	}
	if len(appended.Parts) != 1 || appended.Parts[0].Text != "file contents" {
		t.Fatalf("appended parts=%+v", appended.Parts)
	}

	if err := messages.CompleteAssistant(ctx, 999999, parts, "", nil, nil); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("complete unknown message err=%v, want ErrNotFound", err)
	}
	if _, err := messages.Get(ctx, 999999); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("get unknown message err=%v, want ErrNotFound", err)
	}
	// Completing a user message must not be possible: the assistant-only
	// update reports it as not found.
	if err := messages.CompleteAssistant(ctx, turn.UserMessageID, parts, "", nil, nil); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("complete user message err=%v, want ErrNotFound", err)
	}
}

func TestProfileRepositoryCRUD(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	profiles := postgres.NewProfileRepository(pool)
	profile := seedProfile(t, pool)

	all, err := profiles.List(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("profiles=%d err=%v", len(all), err)
	}

	systemPrompt := "updated prompt"
	model := "custom-model"
	tools := []string{"file.list"}
	maxTurns := 3
	runSeconds := 120
	hardTimeout := 15
	thinking := "auto"
	effort := "high"
	maxOutput := 2048
	enabled := false
	updated, err := profiles.Update(ctx, profile.ID, agent.UpdateParams{
		SystemPrompt:       &systemPrompt,
		Model:              &model,
		AllowedTools:       &tools,
		MaxTurns:           &maxTurns,
		RunSeconds:         &runSeconds,
		HardTimeoutSeconds: &hardTimeout,
		Thinking:           &thinking,
		ReasoningEffort:    &effort,
		MaxOutputTokens:    &maxOutput,
		Enabled:            &enabled,
	})
	if err != nil {
		t.Fatalf("update profile: %v", err)
	}
	if updated.SystemPrompt != systemPrompt || updated.Model != model || !slices.Equal(updated.AllowedTools, tools) ||
		updated.MaxTurns != maxTurns || updated.RunSeconds != runSeconds || updated.HardTimeoutSeconds != hardTimeout ||
		updated.Thinking != thinking || updated.ReasoningEffort != effort || updated.MaxOutputTokens != maxOutput ||
		updated.Enabled != enabled {
		t.Fatalf("updated profile=%+v", updated)
	}
	if updated.CreatedAt.IsZero() || updated.UpdatedAt.Before(updated.CreatedAt) {
		t.Fatalf("updated timestamps created=%v updated=%v", updated.CreatedAt, updated.UpdatedAt)
	}
	stored, err := profiles.Get(ctx, profile.ID)
	if err != nil {
		t.Fatalf("get updated profile: %v", err)
	}
	if stored.SystemPrompt != systemPrompt || stored.MaxTurns != maxTurns || stored.Enabled {
		t.Fatalf("stored profile=%+v", stored)
	}

	if _, err := profiles.Get(ctx, 999999); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("get unknown profile err=%v, want ErrNotFound", err)
	}
	if _, err := profiles.Update(ctx, 999999, agent.UpdateParams{MaxTurns: &maxTurns}); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("update unknown profile err=%v, want ErrNotFound", err)
	}

	// An invalid update is rejected by the repository as well and leaves the
	// stored row untouched.
	invalid := 101
	if _, err := profiles.Update(ctx, profile.ID, agent.UpdateParams{MaxTurns: &invalid}); err == nil {
		t.Fatal("update with max_turns=101 succeeded")
	}
	stored, err = profiles.Get(ctx, profile.ID)
	if err != nil || stored.MaxTurns != maxTurns {
		t.Fatalf("invalid update changed the profile: %+v err=%v", stored, err)
	}
}

func TestSessionRepositoryListCursorAndRunSummaries(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	profiles := postgres.NewProfileRepository(pool)
	firstProfile := seedProfile(t, pool)

	secondSeed := agent.BuiltinProfile()
	secondSeed.Key = "second-agent"
	secondSeed.Name = "第二个 Agent"
	if created, err := profiles.EnsureBuiltin(ctx, secondSeed); err != nil || !created {
		t.Fatalf("ensure second profile created=%v err=%v", created, err)
	}
	allProfiles, err := profiles.List(ctx)
	if err != nil {
		t.Fatalf("list profiles: %v", err)
	}
	var secondProfile agent.AgentProfile
	for _, profile := range allProfiles {
		if profile.Key == secondSeed.Key {
			secondProfile = profile
		}
	}
	if secondProfile.ID == 0 {
		t.Fatalf("second profile not found in %+v", allProfiles)
	}

	sessions := postgres.NewSessionRepository(pool)
	runs := postgres.NewRunRepository(pool)

	first, err := sessions.Create(ctx, firstProfile.ID, "first", nil)
	if err != nil {
		t.Fatalf("create first session: %v", err)
	}
	second, err := sessions.Create(ctx, firstProfile.ID, "second", nil)
	if err != nil {
		t.Fatalf("create second session: %v", err)
	}
	third, err := sessions.Create(ctx, secondProfile.ID, "third", nil)
	if err != nil {
		t.Fatalf("create third session: %v", err)
	}
	if first.ProfileKey != firstProfile.Key || third.ProfileKey != secondProfile.Key {
		t.Fatalf("profile keys first=%q third=%q", first.ProfileKey, third.ProfileKey)
	}

	// first: one running run.
	firstTurn, err := runs.CreateTurn(ctx, first.ID, []agent.Part{{Type: "text", Text: "running"}}, "running")
	if err != nil {
		t.Fatalf("create running turn: %v", err)
	}
	// second: one completed run.
	secondTurn, err := runs.CreateTurn(ctx, second.ID, []agent.Part{{Type: "text", Text: "done"}}, "done")
	if err != nil {
		t.Fatalf("create done turn: %v", err)
	}
	if err := runs.Finish(ctx, secondTurn.RunID, agent.RunResult{
		Status:         agent.RunDone,
		TerminalReason: agent.ReasonCompleted,
		Turns:          1,
	}); err != nil {
		t.Fatalf("finish second run: %v", err)
	}
	// third: no runs at all.

	summaries, hasMore, err := sessions.List(ctx, 0, 10, nil)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if hasMore {
		t.Fatal("hasMore=true for a full page")
	}
	if len(summaries) != 3 {
		t.Fatalf("sessions=%d want 3", len(summaries))
	}
	// Newest ID first: third, second, first.
	if summaries[0].Session.ID != third.ID || summaries[1].Session.ID != second.ID || summaries[2].Session.ID != first.ID {
		t.Fatalf("session order=%d,%d,%d want %d,%d,%d",
			summaries[0].Session.ID, summaries[1].Session.ID, summaries[2].Session.ID,
			third.ID, second.ID, first.ID)
	}
	if summaries[0].ActiveRun != nil || summaries[0].LastRun != nil {
		t.Fatalf("third session should have no runs: %+v", summaries[0])
	}
	if summaries[1].ActiveRun != nil {
		t.Fatalf("second session should have no active run: %+v", summaries[1].ActiveRun)
	}
	if summaries[1].LastRun == nil || summaries[1].LastRun.ID != secondTurn.RunID || summaries[1].LastRun.Status != agent.RunDone {
		t.Fatalf("second session last run=%+v want %d done", summaries[1].LastRun, secondTurn.RunID)
	}
	if summaries[2].ActiveRun == nil || summaries[2].ActiveRun.ID != firstTurn.RunID || summaries[2].ActiveRun.Status != agent.RunRunning {
		t.Fatalf("first session active run=%+v want %d running", summaries[2].ActiveRun, firstTurn.RunID)
	}
	if summaries[2].LastRun == nil || summaries[2].LastRun.ID != firstTurn.RunID {
		t.Fatalf("first session last run=%+v want %d", summaries[2].LastRun, firstTurn.RunID)
	}
	if summaries[2].Session.ProfileKey != firstProfile.Key || summaries[0].Session.ProfileKey != secondProfile.Key {
		t.Fatalf("profile keys in list=%q,%q", summaries[2].Session.ProfileKey, summaries[0].Session.ProfileKey)
	}

	page, hasMore, err := sessions.List(ctx, 0, 2, nil)
	if err != nil {
		t.Fatalf("list first page: %v", err)
	}
	if !hasMore || len(page) != 2 || page[0].Session.ID != third.ID || page[1].Session.ID != second.ID {
		t.Fatalf("first page hasMore=%v page=%+v", hasMore, page)
	}
	next, hasMore, err := sessions.List(ctx, page[1].Session.ID, 2, nil)
	if err != nil {
		t.Fatalf("list second page: %v", err)
	}
	if hasMore || len(next) != 1 || next[0].Session.ID != first.ID {
		t.Fatalf("second page hasMore=%v page=%+v", hasMore, next)
	}

	filtered, hasMore, err := sessions.List(ctx, 0, 10, &firstProfile.ID)
	if err != nil {
		t.Fatalf("list filtered sessions: %v", err)
	}
	if hasMore || len(filtered) != 2 || filtered[0].Session.ID != second.ID || filtered[1].Session.ID != first.ID {
		t.Fatalf("filtered sessions hasMore=%v page=%+v", hasMore, filtered)
	}
	filteredPage, hasMore, err := sessions.List(ctx, 0, 1, &firstProfile.ID)
	if err != nil {
		t.Fatalf("list filtered page: %v", err)
	}
	if !hasMore || len(filteredPage) != 1 || filteredPage[0].Session.ID != second.ID {
		t.Fatalf("filtered page hasMore=%v page=%+v", hasMore, filteredPage)
	}

	stored, err := sessions.Get(ctx, first.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if stored.ProfileKey != firstProfile.Key {
		t.Fatalf("get session profile_key=%q want %q", stored.ProfileKey, firstProfile.Key)
	}
	if _, err := sessions.Get(ctx, 999999); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("get unknown session err=%v, want ErrNotFound", err)
	}
}

func TestRunRepositoryProgressActiveLatestFinish(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	_, session := seedSession(t, pool)
	runs := postgres.NewRunRepository(pool)

	if _, err := runs.Active(ctx, session.ID); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("active with no runs err=%v, want ErrNotFound", err)
	}
	if _, err := runs.Latest(ctx, session.ID); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("latest with no runs err=%v, want ErrNotFound", err)
	}

	first, err := runs.CreateTurn(ctx, session.ID, []agent.Part{{Type: "text", Text: "one"}}, "one")
	if err != nil {
		t.Fatalf("create first turn: %v", err)
	}
	active, err := runs.Active(ctx, session.ID)
	if err != nil || active.ID != first.RunID {
		t.Fatalf("active=%+v err=%v want %d", active, err, first.RunID)
	}
	latest, err := runs.Latest(ctx, session.ID)
	if err != nil || latest.ID != first.RunID {
		t.Fatalf("latest=%+v err=%v want %d", latest, err, first.RunID)
	}

	if err := runs.UpdateProgress(ctx, first.RunID, 2, agent.Usage{
		PromptTokens:     11,
		CompletionTokens: 22,
		ReasoningTokens:  33,
		TotalTokens:      66,
	}); err != nil {
		t.Fatalf("update progress: %v", err)
	}
	progress, err := runs.Get(ctx, first.RunID)
	if err != nil {
		t.Fatalf("get progressing run: %v", err)
	}
	if progress.Turns != 2 || progress.InputTokens != 11 || progress.OutputTokens != 22 || progress.ReasoningTokens != 33 {
		t.Fatalf("progress=%+v", progress)
	}
	if err := runs.UpdateProgress(ctx, 999999, 1, agent.Usage{}); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("update unknown progress err=%v, want ErrNotFound", err)
	}

	result := agent.RunResult{
		Status:         agent.RunDone,
		TerminalReason: agent.ReasonCompleted,
		Turns:          2,
		Usage:          agent.Usage{PromptTokens: 11, CompletionTokens: 22, ReasoningTokens: 33, TotalTokens: 66},
	}
	if err := runs.Finish(ctx, first.RunID, result); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	finished, err := runs.Get(ctx, first.RunID)
	if err != nil {
		t.Fatalf("get finished run: %v", err)
	}
	if finished.Status != agent.RunDone || finished.TerminalReason != agent.ReasonCompleted {
		t.Fatalf("finished run status=%q reason=%q", finished.Status, finished.TerminalReason)
	}
	if finished.Turns != 2 || finished.InputTokens != 11 || finished.OutputTokens != 22 || finished.ReasoningTokens != 33 {
		t.Fatalf("finished usage=%+v", finished)
	}
	if finished.EndedAt == nil {
		t.Fatal("finished run has no ended_at")
	}
	if _, err := runs.Active(ctx, session.ID); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("active after finish err=%v, want ErrNotFound", err)
	}
	latest, err = runs.Latest(ctx, session.ID)
	if err != nil || latest.ID != first.RunID {
		t.Fatalf("latest after finish=%+v err=%v", latest, err)
	}

	second, err := runs.CreateTurn(ctx, session.ID, []agent.Part{{Type: "text", Text: "two"}}, "two")
	if err != nil {
		t.Fatalf("create second turn: %v", err)
	}
	aborted := agent.RunResult{
		Status:          agent.RunAborted,
		TerminalReason:  agent.ReasonAbortedTools,
		AbortCode:       "user_abort",
		AbortMessage:    "stopped by user",
		ErrorCode:       "llm_timeout",
		ErrorMessage:    "upstream timed out",
		Turns:           1,
		Usage:           agent.Usage{PromptTokens: 1, CompletionTokens: 2, ReasoningTokens: 3, TotalTokens: 6},
		LastToolName:    "file.read",
		LastToolPending: true,
	}
	if err := runs.Finish(ctx, second.RunID, aborted); err != nil {
		t.Fatalf("finish aborted run: %v", err)
	}
	stored, err := runs.Get(ctx, second.RunID)
	if err != nil {
		t.Fatalf("get aborted run: %v", err)
	}
	if stored.Status != agent.RunAborted || stored.TerminalReason != agent.ReasonAbortedTools {
		t.Fatalf("aborted run status=%q reason=%q", stored.Status, stored.TerminalReason)
	}
	if stored.AbortCode != "user_abort" || stored.AbortMessage != "stopped by user" ||
		stored.ErrorCode != "llm_timeout" || stored.ErrorMessage != "upstream timed out" {
		t.Fatalf("aborted run fields=%+v", stored)
	}
	if stored.LastToolName != "file.read" || !stored.LastToolPending {
		t.Fatalf("aborted run tool state=%+v", stored)
	}

	if err := runs.Finish(ctx, 999999, result); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("finish unknown run err=%v, want ErrNotFound", err)
	}
	if err := runs.Finish(ctx, second.RunID, agent.RunResult{}); err == nil {
		t.Fatal("finish with an empty status succeeded")
	}
	if _, err := runs.Get(ctx, 999999); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("get unknown run err=%v, want ErrNotFound", err)
	}
}

func TestSessionUpdateTitleAndDeleteCascade(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	_, session := seedSession(t, pool)
	sessions := postgres.NewSessionRepository(pool)
	runs := postgres.NewRunRepository(pool)
	messages := postgres.NewMessageRepository(pool)

	updated, err := sessions.UpdateTitle(ctx, session.ID, "renamed")
	if err != nil {
		t.Fatalf("update title: %v", err)
	}
	if updated.Title != "renamed" || updated.ProfileKey == "" {
		t.Fatalf("updated session=%+v", updated)
	}
	stored, err := sessions.Get(ctx, session.ID)
	if err != nil || stored.Title != "renamed" {
		t.Fatalf("stored session=%+v err=%v", stored, err)
	}
	if _, err := sessions.UpdateTitle(ctx, 999999, "nope"); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("update unknown title err=%v, want ErrNotFound", err)
	}

	turn, err := runs.CreateTurn(ctx, session.ID, []agent.Part{{Type: "text", Text: "hello"}}, "hello")
	if err != nil {
		t.Fatalf("create turn: %v", err)
	}
	if _, err := messages.AppendAssistantPlaceholder(ctx, session.ID, turn.RunID); err != nil {
		t.Fatalf("append placeholder: %v", err)
	}

	if err := sessions.Delete(ctx, session.ID); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	if _, err := sessions.Get(ctx, session.ID); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("get deleted session err=%v, want ErrNotFound", err)
	}
	if _, err := runs.Get(ctx, turn.RunID); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("run survived the session delete: %v", err)
	}
	deletedMessages, err := messages.ListBySession(ctx, session.ID)
	if err != nil {
		t.Fatalf("list messages of deleted session: %v", err)
	}
	if len(deletedMessages) != 0 {
		t.Fatalf("messages survived the session delete: %+v", deletedMessages)
	}
	if err := sessions.Delete(ctx, session.ID); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("second delete err=%v, want ErrNotFound", err)
	}
}

// TestRepositoryMapsMissingParentsToNotFound pins the foreign-key mapping of
// the create paths: a session, run or profile that does not exist surfaces as
// domain.ErrNotFound, never as a raw constraint error.
func TestRepositoryMapsMissingParentsToNotFound(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	sessions := postgres.NewSessionRepository(pool)
	messages := postgres.NewMessageRepository(pool)
	runs := postgres.NewRunRepository(pool)

	if _, err := sessions.Create(ctx, 999999, "", nil); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("create session with unknown profile err=%v, want ErrNotFound", err)
	}
	if _, err := runs.CreateTurn(ctx, 999999, []agent.Part{{Type: "text", Text: "x"}}, "x"); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("create turn for unknown session err=%v, want ErrNotFound", err)
	}
	if _, err := messages.AppendAssistantPlaceholder(ctx, 999999, 999999); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("placeholder for unknown session err=%v, want ErrNotFound", err)
	}
	if _, err := messages.Append(ctx, agent.Message{
		SessionID: 999999,
		Role:      "user",
		Parts:     []agent.Part{{Type: "text", Text: "x"}},
	}); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("append message to unknown session err=%v, want ErrNotFound", err)
	}
}
