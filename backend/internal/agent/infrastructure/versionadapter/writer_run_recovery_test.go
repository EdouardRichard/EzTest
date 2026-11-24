package versionadapter_test

// v0.2.4-H T12 (H-E2): the adapter's run-scoped crash recovery surface. The crash
// sweeper speaks its own ConversationPart shape and a run id (no writer lease);
// the adapter must convert the part to the contract MessagePart and call the
// version service's run-scoped entry, passing a partial-recovery report through
// untouched instead of swallowing it.

import (
	"context"
	"errors"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	versionadapter "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/versionadapter"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

type fakeRunRecoveryConversations struct {
	*fakeConversations
	calls []struct {
		conversationID int64
		runID          int64
		part           version.MessagePart
	}
	sealed int
	err    error
}

func (f *fakeRunRecoveryConversations) InterruptRunOrphanStreams(_ context.Context, conversationID, runID int64, part version.MessagePart) (int, error) {
	f.calls = append(f.calls, struct {
		conversationID int64
		runID          int64
		part           version.MessagePart
	}{conversationID, runID, part})
	return f.sealed, f.err
}

func TestAdapterRunScopedStreamRecovery(t *testing.T) {
	ctx := context.Background()
	fake := &fakeRunRecoveryConversations{fakeConversations: &fakeConversations{}, sealed: 2}
	adapter := versionadapter.New(nil, fake, nil, nil, nil)

	sealed, err := adapter.InterruptRunOrphanStreams(ctx, 20, 900, agentapp.ConversationPart{
		Type: "error",
		Data: map[string]any{"code": "stream_interrupted", "message": "连接中断", "retryable": false},
	})
	if err != nil || sealed != 2 {
		t.Fatalf("InterruptRunOrphanStreams = %d (%v), want the service count", sealed, err)
	}
	if len(fake.calls) != 1 || fake.calls[0].conversationID != 20 || fake.calls[0].runID != 900 {
		t.Fatalf("recovery call = %#v, want (20,900)", fake.calls)
	}
	part := fake.calls[0].part
	if part.Type != version.MessagePartError || part.Code != "stream_interrupted" || part.Message != "连接中断" || part.Retryable {
		t.Fatalf("converted recovery part = %#v, want the contract error part", part)
	}
}

func TestAdapterRunScopedRecoveryPassesPartialReportThrough(t *testing.T) {
	ctx := context.Background()
	partial := &version.OrphanSealError{Failures: []version.OrphanSealFailure{
		{MessageID: 902, Err: errors.New("too many parts")},
	}}
	fake := &fakeRunRecoveryConversations{fakeConversations: &fakeConversations{}, sealed: 1, err: partial}
	adapter := versionadapter.New(nil, fake, nil, nil, nil)

	sealed, err := adapter.InterruptRunOrphanStreams(ctx, 20, 900, agentapp.ConversationPart{
		Type: "error", Data: map[string]any{"code": "stream_interrupted", "message": "连接中断"},
	})
	if sealed != 1 {
		t.Fatalf("sealed = %d, want the heads that WERE sealed reported alongside the failure", sealed)
	}
	var reported *version.OrphanSealError
	if !errors.As(err, &reported) || len(reported.Failures) != 1 || reported.Failures[0].MessageID != 902 {
		t.Fatalf("recovery error = %v, want the service's OrphanSealError unchanged", err)
	}
}

// TestAdapterRunScopedRecoveryWithoutTheSurface pins the pre-T12 assembly: the
// call fails explicitly instead of silently reporting "0 heads recovered".
func TestAdapterRunScopedRecoveryWithoutTheSurface(t *testing.T) {
	ctx := context.Background()
	adapter := versionadapter.New(nil, &fakeConversations{}, nil, nil, nil)
	if _, err := adapter.InterruptRunOrphanStreams(ctx, 20, 900, agentapp.ConversationPart{
		Type: "error", Data: map[string]any{"code": "stream_interrupted", "message": "连接中断"},
	}); err == nil {
		t.Fatal("an adapter without the run-scoped surface reported success")
	}
	empty := versionadapter.New(nil, nil, nil, nil, nil)
	if _, err := empty.InterruptRunOrphanStreams(ctx, 20, 900, agentapp.ConversationPart{Type: "error"}); err == nil {
		t.Fatal("an adapter without a conversation service reported success")
	}
}
