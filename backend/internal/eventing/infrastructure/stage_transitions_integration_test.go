//go:build integration

package eventingpg_test

import (
	"context"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
	eventingpg "github.com/EdouardRichard/EzTest/backend/internal/eventing/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// TestStageTransitionReaderMatchesTheCurrentVisit pins the R49 transition-reason
// query: the reader answers with the reason of the recorded stage change that
// created the observed stage visit (same stage_entered_at) and reports an empty
// reason for an unknown visit or version, so R49 can never repair a resume,
// reject or legacy re-entry.
func TestStageTransitionReaderMatchesTheCurrentVisit(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := eventingpg.New(pool)
	reader := eventingpg.NewStageTransitionReader(pool)
	ctx := context.Background()
	const versionID = int64(9101)
	channel := "version:9101"

	confirmedAt := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	resumedAt := confirmedAt.Add(time.Hour)
	for _, event := range []eventing.Event{
		{
			Channel: channel,
			Type:    "version.stage_changed",
			Payload: map[string]any{
				"from": "requirement_review", "to": "designing", "reason": "requirement_confirmed",
				"stage_entered_at": confirmedAt.Format(time.RFC3339),
			},
		},
		{
			Channel: channel,
			Type:    "version.stage_changed",
			Payload: map[string]any{
				"from": "blocked", "to": "designing", "reason": "resumed",
				"stage_entered_at": resumedAt.Format(time.RFC3339),
			},
		},
	} {
		if _, err := repo.Append(ctx, event); err != nil {
			t.Fatalf("append stage event: %v", err)
		}
	}

	reason, err := reader.LastStageReason(ctx, versionID, resumedAt)
	if err != nil {
		t.Fatalf("LastStageReason(resumed visit): %v", err)
	}
	if reason != "resumed" {
		t.Fatalf("resumed visit reason = %q, want resumed", reason)
	}

	reason, err = reader.LastStageReason(ctx, versionID, confirmedAt)
	if err != nil {
		t.Fatalf("LastStageReason(confirmed visit): %v", err)
	}
	if reason != "requirement_confirmed" {
		t.Fatalf("confirmed visit reason = %q, want requirement_confirmed", reason)
	}

	// A visit with no recorded transition cannot prove it owns an entry job.
	reason, err = reader.LastStageReason(ctx, versionID, resumedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("LastStageReason(unknown visit): %v", err)
	}
	if reason != "" {
		t.Fatalf("unknown visit reason = %q, want empty", reason)
	}

	// A version without any stage event is unknown, not an error.
	reason, err = reader.LastStageReason(ctx, versionID+1, resumedAt)
	if err != nil {
		t.Fatalf("LastStageReason(unknown version): %v", err)
	}
	if reason != "" {
		t.Fatalf("unknown version reason = %q, want empty", reason)
	}
}
