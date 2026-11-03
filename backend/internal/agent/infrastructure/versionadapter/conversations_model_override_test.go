package versionadapter_test

// Task 55: the agent runtime loads the conversation-level model override before
// model resolution, so the adapter must expose the stored model_override as a
// trimmed string ("" when unset).

import (
	"context"
	"testing"

	versionadapter "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/versionadapter"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

func TestConversationPortModelOverride(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name     string
		override *string
		want     string
	}{
		{name: "stored override", override: strPtr("vision-plus"), want: "vision-plus"},
		{name: "no override", override: nil, want: ""},
		{name: "blank override", override: strPtr("   "), want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeConversations{
				getFn: func(_ context.Context, versionID, conversationID int64) (versionapp.ConversationView, error) {
					if versionID != 10 || conversationID != 20 {
						return versionapp.ConversationView{}, NotFoundError("conversation")
					}
					return versionapp.ConversationView{ID: 20, VersionID: 10, ModelOverride: tc.override}, nil
				},
			}
			adapter := versionadapter.New(nil, fake, nil, nil, nil)

			got, err := adapter.ModelOverride(ctx, 10, 20)
			if err != nil {
				t.Fatalf("ModelOverride: %v", err)
			}
			if got != tc.want {
				t.Fatalf("ModelOverride = %q, want %q", got, tc.want)
			}
		})
	}
}

func strPtr(value string) *string { return &value }
