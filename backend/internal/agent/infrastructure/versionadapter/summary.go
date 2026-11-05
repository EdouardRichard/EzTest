package versionadapter

// v0.22 W0 fix round 1 (Task 36 review Critical 2): the production
// agentapp.SummaryStore adapter. It persists compaction summaries through the
// version ConversationService, which owns the conversation_summary revision
// sequence, the R-A5-12 monotonic covered_message_id and the DB advisory lock.
// The agent application cannot import the version context, so this adapter lives
// on the version side of the boundary.

import (
	"context"
	"errors"
	"fmt"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// compactionSummaryWriter is the narrow version-conversation write surface of
// the summary store. *versionapp.ConversationService satisfies it.
type compactionSummaryWriter interface {
	AppendCompactionSummary(ctx context.Context, write versionapp.CompactionSummaryWrite) (versionapp.SummaryView, error)
}

// SummaryStore is the production agentapp.SummaryStore over the version
// conversation service (R-A5-12).
type SummaryStore struct {
	writer compactionSummaryWriter
}

var _ agentapp.SummaryStore = (*SummaryStore)(nil)

// NewSummaryStore wires the summary store to the conversation service.
func NewSummaryStore(writer compactionSummaryWriter) *SummaryStore {
	return &SummaryStore{writer: writer}
}

// AppendSummary implements agentapp.SummaryStore. A stale covered_message_id is
// reported with the agentapp sentinel; every other failure is returned as-is.
func (s *SummaryStore) AppendSummary(ctx context.Context, commit agentapp.SummaryCommit) (agentapp.SummaryRecord, error) {
	if s == nil || s.writer == nil {
		return agentapp.SummaryRecord{}, errors.New("versionadapter: compaction summary writer is not configured")
	}
	view, err := s.writer.AppendCompactionSummary(ctx, versionapp.CompactionSummaryWrite{
		ConversationID:   commit.ConversationID,
		Summary:          commit.Summary,
		PinnedFacts:      commit.PinnedFacts,
		TokenEstimate:    commit.TokenEstimate,
		CoveredMessageID: commit.CoveredMessageID,
		CompactionID:     commit.CompactionID,
		Trigger:          string(commit.Trigger),
		SummaryMeta:      commit.SummaryMeta,
	})
	if err != nil {
		if errors.Is(err, version.ErrSummaryWatermarkStale) {
			return agentapp.SummaryRecord{}, fmt.Errorf("%w: %v", agentapp.ErrSummaryWatermarkStale, err)
		}
		return agentapp.SummaryRecord{}, err
	}
	return agentapp.SummaryRecord{
		Version:       view.Version,
		UpdatedAt:     view.UpdatedAt,
		SummaryCommit: commit,
	}, nil
}
