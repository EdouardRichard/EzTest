package versionadapter

import (
	"bytes"
	"context"
	"strings"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
)

const contentWriterMaxName = 80

// PutText spills an oversized tool result into the content ledger as a text
// object, acting as the system actor (audit actor 0 semantics). The content
// SHA-256 is the stable content_ref exposed to the model.
func (a *Adapter) PutText(ctx context.Context, owner string, data []byte) (agentapp.ContentRef, error) {
	if a.storage == nil {
		return agentapp.ContentRef{}, a.missing("storage")
	}
	owner = strings.TrimSpace(owner)
	if owner == "" {
		owner = "agent_tool_result"
	}
	object, err := a.storage.Upload(ctx, sanitizeContentName(owner)+".txt", "text/plain",
		bytes.NewReader(data), int64(len(data)), 0)
	if err != nil {
		return agentapp.ContentRef{}, err
	}
	return agentapp.ContentRef{SHA256: object.SHA256, Ref: object.SHA256}, nil
}

func sanitizeContentName(value string) string {
	var builder strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			builder.WriteRune(r)
		default:
			builder.WriteRune('_')
		}
		if builder.Len() >= contentWriterMaxName {
			break
		}
	}
	name := builder.String()
	if name == "" {
		name = "agent_tool_result"
	}
	return name
}
