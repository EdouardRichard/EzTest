package versionadapter

import (
	"context"
	"errors"
	"fmt"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentcontext "github.com/EdouardRichard/EzTest/backend/internal/agent/application/context"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// ContextPacketSource is the narrow read seam behind the production context
// packet reader. The PostgreSQL runtime repository implements it structurally
// with the version-scoped latest query; tests inject an in-memory source.
type ContextPacketSource interface {
	LatestVersionContextPacket(ctx context.Context, versionID int64) (agent.ContextPacket, error)
}

// ContextPacketReader implements the admin-only context_packet read API for one
// version (Task 48). It is read-only: it returns the newest persisted
// cache-first packet of the requested version and never assembles or compacts.
// A legacy P1 packet row has no cache-first metadata and is therefore reported
// as not-found instead of being mis-projected.
type ContextPacketReader struct {
	source ContextPacketSource
}

// NewContextPacketReader wires the production reader over the PostgreSQL
// runtime repository. The reader never writes.
func NewContextPacketReader(source ContextPacketSource) *ContextPacketReader {
	return &ContextPacketReader{source: source}
}

// LatestContextPacket returns the latest persisted cache-first packet of one
// version, or agentcontext.ErrContextPacketNotFound when the version has no such
// packet. A non-positive version ID is a typed 400.
func (r *ContextPacketReader) LatestContextPacket(ctx context.Context, versionID int64) (agentcontext.ContextPacket, error) {
	if versionID <= 0 {
		return agentcontext.ContextPacket{}, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if r == nil || r.source == nil {
		return agentcontext.ContextPacket{}, platform.Unavailable("context packet reader is not configured")
	}
	stored, err := r.source.LatestVersionContextPacket(ctx, versionID)
	if err != nil {
		if errors.Is(err, agent.ErrNotFound) {
			return agentcontext.ContextPacket{}, agentcontext.ErrContextPacketNotFound
		}
		return agentcontext.ContextPacket{}, fmt.Errorf("load latest context packet for version %d: %w", versionID, err)
	}
	packet, err := agentapp.DecodeContextPacket(stored)
	if err != nil {
		return agentcontext.ContextPacket{}, err
	}
	return packet, nil
}
