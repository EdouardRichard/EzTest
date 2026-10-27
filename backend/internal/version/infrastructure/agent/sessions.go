// Package agent adapts the P3 AgentSessions and P1 ImageResolver ports to the
// P1 agent application services (design spec §3.5/§8.2). It lives in the
// version infrastructure layer so the P3 application layer never imports P1
// infrastructure: the composition root injects the concrete P1 services, which
// satisfy the narrow interfaces declared here.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// ProfileResolver is the narrow slice of the P1 profile service the adapter
// needs. *agent/application.ProfileService satisfies it.
type ProfileResolver interface {
	GetByKey(ctx context.Context, key string) (agentdomain.AgentProfile, error)
}

// SessionStore is the narrow slice of the P1 session service the adapter needs.
// *agent/application.SessionService satisfies it.
type SessionStore interface {
	CreateWithOptions(ctx context.Context, profileID int64, title string, actor *int64, opts agentapp.SessionOptions) (agentdomain.Session, error)
	ListMessages(ctx context.Context, sessionID, beforeID int64, limit int) ([]agentdomain.Message, bool, error)
}

// RunStarter is the narrow slice of the P1 run service the adapter needs.
// *agent/application.RunService satisfies it.
type RunStarter interface {
	Start(ctx context.Context, sessionID int64, message string, actor *int64) (agentdomain.StartResult, error)
	StartWithParts(ctx context.Context, sessionID int64, parts []agentdomain.Part, actor *int64) (agentdomain.StartResult, error)
	HasRunningByChannel(ctx context.Context, channel string) (bool, error)
}

// AgentSessionsAdapter implements application.AgentSessions on top of the P1
// session and run services.
type AgentSessionsAdapter struct {
	sessions SessionStore
	runs     RunStarter
	profiles ProfileResolver
}

// NewAgentSessions wires the adapter to the P1 session, run and profile
// services.
func NewAgentSessions(sessions SessionStore, runs RunStarter, profiles ProfileResolver) *AgentSessionsAdapter {
	return &AgentSessionsAdapter{sessions: sessions, runs: runs, profiles: profiles}
}

var _ application.AgentSessions = (*AgentSessionsAdapter)(nil)

// Create resolves the P3 profile key to its P1 profile id, creates the session
// with the version activity channel stamp and returns the new session id.
func (a *AgentSessionsAdapter) Create(ctx context.Context, profileKey, title string, actor int64, channel, versionID string) (int64, error) {
	if a.profiles == nil || a.sessions == nil {
		return 0, errors.New("version: agent sessions are not configured")
	}
	profile, err := a.profiles.GetByKey(ctx, profileKey)
	if err != nil {
		return 0, err
	}
	version := versionID
	session, err := a.sessions.CreateWithOptions(ctx, profile.ID, title, actorPtr(actor), agentapp.SessionOptions{
		ActivityChannel:   channel,
		ActivityVersionID: &version,
	})
	if err != nil {
		return 0, err
	}
	return session.ID, nil
}

// StartText starts one text-only P1 run and returns its run id.
func (a *AgentSessionsAdapter) StartText(ctx context.Context, agentSessionID int64, text string, actor int64) (int64, error) {
	if a.runs == nil {
		return 0, errors.New("version: agent runs are not configured")
	}
	result, err := a.runs.Start(ctx, agentSessionID, text, actorPtr(actor))
	if err != nil {
		return 0, err
	}
	return result.RunID, nil
}

// StartParts starts one multimodal P1 run (text plus attachment image
// references) and returns its run id.
func (a *AgentSessionsAdapter) StartParts(ctx context.Context, agentSessionID int64, parts []application.AgentPart, actor int64) (int64, error) {
	if a.runs == nil {
		return 0, errors.New("version: agent runs are not configured")
	}
	result, err := a.runs.StartWithParts(ctx, agentSessionID, toAgentParts(parts), actorPtr(actor))
	if err != nil {
		return 0, err
	}
	return result.RunID, nil
}

// Messages projects one page of P1 message history onto the P3 read model.
func (a *AgentSessionsAdapter) Messages(ctx context.Context, agentSessionID, beforeID int64, limit int) ([]application.AgentMessage, bool, error) {
	if a.sessions == nil {
		return nil, false, errors.New("version: agent sessions are not configured")
	}
	messages, hasMore, err := a.sessions.ListMessages(ctx, agentSessionID, beforeID, limit)
	if err != nil {
		return nil, false, err
	}
	projected := make([]application.AgentMessage, 0, len(messages))
	for _, message := range messages {
		var runID int64
		if message.RunID != nil {
			runID = *message.RunID
		}
		var toolCalls json.RawMessage
		if len(message.ToolCalls) > 0 {
			encoded, err := json.Marshal(message.ToolCalls)
			if err != nil {
				return nil, false, fmt.Errorf("version: encode agent tool calls: %w", err)
			}
			toolCalls = encoded
		}
		projected = append(projected, application.AgentMessage{
			ID:        message.ID,
			RunID:     runID,
			Role:      message.Role,
			Parts:     toAgentMessageParts(message.Parts),
			Reasoning: message.Reasoning,
			ToolCalls: toolCalls,
			ToolName:  message.ToolName,
			CreatedAt: message.CreatedAt,
		})
	}
	return projected, hasMore, nil
}

// HasRunningByChannel reports whether any P1 session stamped with channel still
// has a running run.
func (a *AgentSessionsAdapter) HasRunningByChannel(ctx context.Context, channel string) (bool, error) {
	if a.runs == nil {
		return false, errors.New("version: agent runs are not configured")
	}
	return a.runs.HasRunningByChannel(ctx, channel)
}

// toAgentParts maps the P3 agent parts onto the P1 part shape. An image
// reference always becomes an image_url part carrying only the reference; the
// P1 gateway resolves the data URL transiently at LLM call time.
func toAgentParts(parts []application.AgentPart) []agentdomain.Part {
	converted := make([]agentdomain.Part, 0, len(parts))
	for _, part := range parts {
		if part.ImageRef != "" {
			converted = append(converted, agentdomain.Part{Type: "image_url", ImageRef: part.ImageRef})
			continue
		}
		converted = append(converted, agentdomain.Part{Type: "text", Text: part.Text})
	}
	return converted
}

// toAgentMessageParts projects stored P1 parts onto the P3 API shape. Only the
// persisted image reference is exposed; a transient data URL is never read back
// from storage (D30).
func toAgentMessageParts(parts []agentdomain.Part) []application.AgentPart {
	converted := make([]application.AgentPart, 0, len(parts))
	for _, part := range parts {
		if part.ImageRef != "" {
			converted = append(converted, application.AgentPart{Type: "image", ImageRef: part.ImageRef})
			continue
		}
		converted = append(converted, application.AgentPart{Type: "text", Text: part.Text})
	}
	return converted
}

// actorPtr converts the P3 actor id to the P1 optional actor. A zero actor
// stays nil so system-started runs are not attributed to user 0.
func actorPtr(actor int64) *int64 {
	if actor <= 0 {
		return nil
	}
	return &actor
}
