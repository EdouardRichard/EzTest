package agentapp

// H-F10b #32 (I-B4 judge independence): artifactRefs used to gate only
// conversation_id for a design.judge run, leaving conversation_message and
// attachment ungated. No judge payload carries those ids today, so the gap was
// latent — but an artifact ref is a READ HANDLE, so the exclusion must cover every
// conversation-scoped kind, not only the one that happened to leak.
//
// This pins the whole conversation-scoped set for both branches: a judge run gets
// none of them, a non-judge design run gets all of them (the discriminating
// control — a change that dropped the refs for every profile would fail here).

import (
	"testing"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

func TestJudgeArtifactRefsExcludeEveryConversationScopedKind(t *testing.T) {
	payload := WorkItemPayload{
		ConversationID: 20,
		MessageID:      30,
		AttachmentID:   40,
		RequirementID:  501,
	}
	conversationScoped := map[string]bool{
		"conversation":         true,
		"conversation_message": true,
		"attachment":           true,
	}

	for _, tc := range []struct {
		name     string
		workType string
		wantRefs bool
	}{
		{"design judge", WorkTypeDesignJudge, false},
		{"design critic (control)", WorkTypeDesignCritic, true},
		{"design author (control)", WorkTypeDesignRequirement, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := agent.WorkItem{Type: tc.workType}
			refs := artifactRefs(&runState{item: item, payload: payload})
			seen := map[string]bool{}
			for _, ref := range refs {
				seen[ref.Kind] = true
			}
			for kind := range conversationScoped {
				if seen[kind] != tc.wantRefs {
					t.Errorf("%s: artifact ref %q present = %v, want %v (I-B4: a judge run carries no conversation-scoped read handle)",
						tc.name, kind, seen[kind], tc.wantRefs)
				}
			}
			// The requirement ref is objective scope, not author reasoning: it must
			// survive for BOTH branches, so the gate cannot pass by starving the
			// judge of every artifact.
			if !seen["requirement"] {
				t.Errorf("%s: the objective requirement artifact ref was dropped", tc.name)
			}
		})
	}
}
