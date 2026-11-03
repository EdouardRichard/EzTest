package main

import (
	"context"
	"errors"
	"testing"
)

// fakeStageRoundStarter records the design.cases adapter delegation.
type fakeStageRoundStarter struct {
	calls          int
	versionID      int64
	requirementIDs []int64
	conversationID int64
	err            error
}

func (f *fakeStageRoundStarter) StartStageRound(_ context.Context, versionID int64, requirementIDs []int64) (int64, error) {
	f.calls++
	f.versionID = versionID
	f.requirementIDs = append([]int64(nil), requirementIDs...)
	return f.conversationID, f.err
}

// fakeBatchIntentEnsurer records the execute.batch adapter delegation.
type fakeBatchIntentEnsurer struct {
	calls     int
	versionID int64
	caseIDs   []int64
	created   int
	err       error
}

func (f *fakeBatchIntentEnsurer) EnsureInitialIntentsForCases(_ context.Context, versionID int64, caseIDs []int64) (int, error) {
	f.calls++
	f.versionID = versionID
	f.caseIDs = append([]int64(nil), caseIDs...)
	return f.created, f.err
}

// TestStageEntryWiringAdapters pins the composition-root wiring of the two
// deterministic 0.2.1 stage-entry consumers (Task 65): design.cases delegates to
// the design planner's StartStageRound with the hard-gate requirement scope, and
// execute.batch refreshes the execution planner's initial intents. Missing
// collaborators fail closed instead of panicking.
func TestStageEntryWiringAdapters(t *testing.T) {
	rounds := &fakeStageRoundStarter{conversationID: 9001}
	adapter := designRoundStarterAdapter{planner: rounds}
	conversationID, err := adapter.StartDesignRound(context.Background(), 74, []int64{501, 502})
	if err != nil {
		t.Fatalf("StartDesignRound: %v", err)
	}
	if conversationID != 9001 || rounds.calls != 1 || rounds.versionID != 74 {
		t.Fatalf("round adapter delegation = %d calls=%d version=%d", conversationID, rounds.calls, rounds.versionID)
	}
	if len(rounds.requirementIDs) != 2 || rounds.requirementIDs[0] != 501 || rounds.requirementIDs[1] != 502 {
		t.Fatalf("round adapter requirement ids = %v", rounds.requirementIDs)
	}
	rounds.err = errors.New("design planner down")
	if _, err := adapter.StartDesignRound(context.Background(), 74, nil); err == nil {
		t.Fatal("design round adapter swallowed the planner error")
	}
	if _, err := (designRoundStarterAdapter{}).StartDesignRound(context.Background(), 74, nil); err == nil {
		t.Fatal("design round adapter without a planner did not fail closed")
	}

	intents := &fakeBatchIntentEnsurer{created: 2}
	batch := executionBatchStarterAdapter{intents: intents}
	if err := batch.StartBatch(context.Background(), 75, []int64{7001}); err != nil {
		t.Fatalf("StartBatch: %v", err)
	}
	if intents.calls != 1 || intents.versionID != 75 {
		t.Fatalf("batch adapter delegation = %d version=%d", intents.calls, intents.versionID)
	}
	if len(intents.caseIDs) != 1 || intents.caseIDs[0] != 7001 {
		t.Fatalf("batch adapter case ids = %v, want the execute.batch selection", intents.caseIDs)
	}
	intents.err = errors.New("intent scan down")
	if err := batch.StartBatch(context.Background(), 75, nil); err == nil {
		t.Fatal("batch adapter swallowed the intent error")
	}
	if err := (executionBatchStarterAdapter{}).StartBatch(context.Background(), 75, nil); err == nil {
		t.Fatal("batch adapter without an intent service did not fail closed")
	}
}
