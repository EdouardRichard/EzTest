package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versionagent "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/agent"
)

// fakeProfiles resolves a configured profile key.
type fakeProfiles struct {
	profile agentdomain.AgentProfile
	err     error
	keys    []string
}

// GetByKey records the key and returns the configured profile.
func (f *fakeProfiles) GetByKey(_ context.Context, key string) (agentdomain.AgentProfile, error) {
	f.keys = append(f.keys, key)
	if f.err != nil {
		return agentdomain.AgentProfile{}, f.err
	}
	return f.profile, nil
}

// fakeSessionService records the P1 session calls.
type fakeSessionService struct {
	createdProfileID int64
	createdTitle     string
	createdActor     *int64
	createdOptions   agentapp.SessionOptions
	createdSession   agentdomain.Session
	createErr        error
	createCalls      int

	messages    []agentdomain.Message
	hasMore     bool
	messagesErr error
	beforeID    int64
	limit       int
	messageID   int64
	listCalls   int
}

// CreateWithOptions records the P3 channel stamp.
func (f *fakeSessionService) CreateWithOptions(_ context.Context, profileID int64, title string, actor *int64, opts agentapp.SessionOptions) (agentdomain.Session, error) {
	f.createCalls++
	f.createdProfileID = profileID
	f.createdTitle = title
	f.createdActor = actor
	f.createdOptions = opts
	if f.createErr != nil {
		return agentdomain.Session{}, f.createErr
	}
	return f.createdSession, nil
}

// ListMessages records the cursor and returns the configured history.
func (f *fakeSessionService) ListMessages(_ context.Context, sessionID, beforeID int64, limit int) ([]agentdomain.Message, bool, error) {
	f.listCalls++
	f.messageID = sessionID
	f.beforeID = beforeID
	f.limit = limit
	if f.messagesErr != nil {
		return nil, false, f.messagesErr
	}
	return f.messages, f.hasMore, nil
}

// fakeRunService records the P1 run starts.
type fakeRunService struct {
	texts     []string
	parts     [][]agentdomain.Part
	actors    []*int64
	runID     int64
	startErr  error
	running   bool
	runningID string
}

// Start records the text run.
func (f *fakeRunService) Start(_ context.Context, _ int64, message string, actor *int64) (agentdomain.StartResult, error) {
	f.texts = append(f.texts, message)
	f.actors = append(f.actors, actor)
	if f.startErr != nil {
		return agentdomain.StartResult{}, f.startErr
	}
	return agentdomain.StartResult{RunID: f.runID}, nil
}

// StartWithParts records the multimodal run.
func (f *fakeRunService) StartWithParts(_ context.Context, _ int64, parts []agentdomain.Part, actor *int64) (agentdomain.StartResult, error) {
	f.parts = append(f.parts, parts)
	f.actors = append(f.actors, actor)
	if f.startErr != nil {
		return agentdomain.StartResult{}, f.startErr
	}
	return agentdomain.StartResult{RunID: f.runID}, nil
}

// HasRunningByChannel records the channel and returns the configured busy flag.
func (f *fakeRunService) HasRunningByChannel(_ context.Context, channel string) (bool, error) {
	f.runningID = channel
	return f.running, nil
}

// TestAgentSessionsAdapterUsesVersionChannel pins the P1 adapter: profile key
// resolution, the version channel stamp and the run entry points.
func TestAgentSessionsAdapterUsesVersionChannel(t *testing.T) {
	ctx := context.Background()

	t.Run("Create resolves the profile key and stamps the version channel", func(t *testing.T) {
		sessions := &fakeSessionService{createdSession: agentdomain.Session{ID: 700}}
		profiles := &fakeProfiles{profile: agentdomain.AgentProfile{ID: 55, Key: "clarification-dialog"}}
		runs := &fakeRunService{}
		adapter := versionagent.NewAgentSessions(sessions, runs, profiles)

		id, err := adapter.Create(ctx, "clarification-dialog", "Clarification for version 42", 7, "version:42", "42")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if id != 700 {
			t.Fatalf("Create id=%d want 700", id)
		}
		if len(profiles.keys) != 1 || profiles.keys[0] != "clarification-dialog" {
			t.Fatalf("profile keys=%v", profiles.keys)
		}
		if sessions.createdProfileID != 55 || sessions.createdTitle != "Clarification for version 42" {
			t.Fatalf("created profile/title=%d/%q", sessions.createdProfileID, sessions.createdTitle)
		}
		if sessions.createdActor == nil || *sessions.createdActor != 7 {
			t.Fatalf("created actor=%v want 7", sessions.createdActor)
		}
		if sessions.createdOptions.ActivityChannel != "version:42" {
			t.Fatalf("activity channel=%q", sessions.createdOptions.ActivityChannel)
		}
		if sessions.createdOptions.ActivityVersionID == nil || *sessions.createdOptions.ActivityVersionID != "42" {
			t.Fatalf("activity version id=%v want \"42\"", sessions.createdOptions.ActivityVersionID)
		}
	})

	t.Run("Create keeps a zero actor as nil and reports profile failures", func(t *testing.T) {
		sessions := &fakeSessionService{}
		profiles := &fakeProfiles{profile: agentdomain.AgentProfile{ID: 55}}
		adapter := versionagent.NewAgentSessions(sessions, &fakeRunService{}, profiles)
		if _, err := adapter.Create(ctx, "context-recon", "Recon", 0, "version:42", "42"); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if sessions.createdActor != nil {
			t.Fatalf("created actor=%v want nil for a zero actor", sessions.createdActor)
		}

		failing := versionagent.NewAgentSessions(&fakeSessionService{}, &fakeRunService{}, &fakeProfiles{err: errors.New("profile missing")})
		if _, err := failing.Create(ctx, "context-recon", "Recon", 7, "version:42", "42"); err == nil {
			t.Fatal("Create(profile failure) error=nil")
		}
	})

	t.Run("StartText and StartParts return the run id", func(t *testing.T) {
		sessions := &fakeSessionService{}
		runs := &fakeRunService{runID: 900}
		adapter := versionagent.NewAgentSessions(sessions, runs, &fakeProfiles{})

		runID, err := adapter.StartText(ctx, 700, "hello", 7)
		if err != nil {
			t.Fatalf("StartText: %v", err)
		}
		if runID != 900 || len(runs.texts) != 1 || runs.texts[0] != "hello" {
			t.Fatalf("StartText run=%d texts=%v", runID, runs.texts)
		}
		if runs.actors[0] == nil || *runs.actors[0] != 7 {
			t.Fatalf("StartText actor=%v", runs.actors[0])
		}

		parts := []application.AgentPart{
			{Type: "text", Text: "look"},
			{Type: "image", ImageRef: "attachment:42"},
		}
		runID, err = adapter.StartParts(ctx, 700, parts, 7)
		if err != nil {
			t.Fatalf("StartParts: %v", err)
		}
		if runID != 900 {
			t.Fatalf("StartParts run=%d want 900", runID)
		}
		if len(runs.parts) != 1 {
			t.Fatalf("StartParts forwarded %d runs, want 1", len(runs.parts))
		}
		got := runs.parts[0]
		if len(got) != 2 || got[0].Type != "text" || got[0].Text != "look" ||
			got[1].Type != "image_url" || got[1].ImageRef != "attachment:42" {
			t.Fatalf("StartParts mapped parts=%+v", got)
		}
	})

	t.Run("Messages maps the P1 projection", func(t *testing.T) {
		runID := int64(9)
		created := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
		sessions := &fakeSessionService{
			hasMore: true,
			messages: []agentdomain.Message{
				{
					ID: 11, SessionID: 700, RunID: &runID, Role: "user",
					Parts:     []agentdomain.Part{{Type: "text", Text: "hi"}, {Type: "image_url", ImageRef: "attachment:42"}},
					CreatedAt: created,
				},
				{
					ID: 12, SessionID: 700, RunID: &runID, Role: "assistant",
					Parts:     []agentdomain.Part{{Type: "text", Text: "answer"}},
					Reasoning: "thinking",
					ToolCalls: []agentdomain.ToolCall{{ID: "call-1", Name: "version.context_get", Arguments: `{"versionId":"42"}`}},
					CreatedAt: created.Add(time.Second),
				},
			},
		}
		adapter := versionagent.NewAgentSessions(sessions, &fakeRunService{}, &fakeProfiles{})

		messages, hasMore, err := adapter.Messages(ctx, 700, 12, 2)
		if err != nil {
			t.Fatalf("Messages: %v", err)
		}
		if !hasMore || sessions.beforeID != 12 || sessions.limit != 2 || sessions.messageID != 700 {
			t.Fatalf("Messages cursor args session=%d before=%d limit=%d hasMore=%v", sessions.messageID, sessions.beforeID, sessions.limit, hasMore)
		}
		if len(messages) != 2 {
			t.Fatalf("Messages len=%d want 2", len(messages))
		}
		if messages[0].ID != 11 || messages[0].RunID != 9 || messages[0].Role != "user" ||
			len(messages[0].Parts) != 2 || messages[0].Parts[1].Type != "image" ||
			messages[0].Parts[1].ImageRef != "attachment:42" {
			t.Fatalf("user message=%+v", messages[0])
		}
		if messages[1].Reasoning != "thinking" || len(messages[1].ToolCalls) == 0 {
			t.Fatalf("assistant message=%+v", messages[1])
		}
		var calls []agentdomain.ToolCall
		if err := json.Unmarshal(messages[1].ToolCalls, &calls); err != nil {
			t.Fatalf("tool calls %s: %v", messages[1].ToolCalls, err)
		}
		if len(calls) != 1 || calls[0].Name != "version.context_get" {
			t.Fatalf("tool calls=%+v", calls)
		}
	})

	t.Run("Messages and HasRunningByChannel report failures and state", func(t *testing.T) {
		sessions := &fakeSessionService{messagesErr: errors.New("history down")}
		adapter := versionagent.NewAgentSessions(sessions, &fakeRunService{running: true}, &fakeProfiles{})
		if _, _, err := adapter.Messages(ctx, 700, 0, 20); err == nil {
			t.Fatal("Messages(repository failure) error=nil")
		}
		running, err := adapter.HasRunningByChannel(ctx, "version:42")
		if err != nil || !running || sessions.listCalls != 1 {
			t.Fatalf("HasRunningByChannel=%v/%v want true", running, err)
		}
	})
}

// TestAgentSessionsAdapterMapsImageOnlyTitle pins that an image-only run still
// reaches StartWithParts with a non-empty title hint derived from the parts.
func TestAgentSessionsAdapterMapsImageOnlyParts(t *testing.T) {
	runs := &fakeRunService{runID: 5}
	adapter := versionagent.NewAgentSessions(&fakeSessionService{}, runs, &fakeProfiles{})
	parts := []application.AgentPart{{Type: "image", ImageRef: "attachment:42"}}
	if _, err := adapter.StartParts(context.Background(), 1, parts, 7); err != nil {
		t.Fatalf("StartParts: %v", err)
	}
	if got := runs.parts[0]; len(got) != 1 || got[0].Type != "image_url" || got[0].ImageRef != "attachment:42" {
		t.Fatalf("image-only parts=%+v", got)
	}
}

// TestAgentSessionsAdapterNormalizesPartTypes pins the accepted AgentPart type
// spellings: text/image and the already-canonical image_url all map to the P1
// part shape.
func TestAgentSessionsAdapterNormalizesPartTypes(t *testing.T) {
	runs := &fakeRunService{runID: 5}
	adapter := versionagent.NewAgentSessions(&fakeSessionService{}, runs, &fakeProfiles{})
	parts := []application.AgentPart{
		{Type: "image_url", ImageRef: "attachment:7"},
		{Type: "", Text: "plain"},
	}
	if _, err := adapter.StartParts(context.Background(), 1, parts, 7); err != nil {
		t.Fatalf("StartParts: %v", err)
	}
	got := runs.parts[0]
	if len(got) != 2 || got[0].Type != "image_url" || got[0].ImageRef != "attachment:7" ||
		got[1].Type != "text" || got[1].Text != "plain" {
		t.Fatalf("normalized parts=%+v", got)
	}
}

// TestAgentSessionsAdapterPropagatesRunFailure pins run start failures.
func TestAgentSessionsAdapterPropagatesRunFailure(t *testing.T) {
	runs := &fakeRunService{startErr: errors.New("llm unavailable")}
	adapter := versionagent.NewAgentSessions(&fakeSessionService{}, runs, &fakeProfiles{})
	if _, err := adapter.StartText(context.Background(), 1, "hi", 7); err == nil ||
		!strings.Contains(err.Error(), "llm unavailable") {
		t.Fatalf("StartText error=%v want the run failure", err)
	}
}
