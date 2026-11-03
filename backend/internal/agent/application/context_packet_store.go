package agentapp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"

	agentcontext "github.com/EdouardRichard/EzTest/backend/internal/agent/application/context"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// contextPacketMetaSection is the reserved section name of the cache-first
// persistence metadata. The ordered segments themselves are stored as ordinary
// context sections; the metadata carries the segment roles/tool-call identities
// and the frozen packet hashes, which the generic context_packet columns cannot
// represent directly. It is an internal storage detail of the packet, never
// part of the rendered prompt or the read-model segments.
const contextPacketMetaSection = "__context_packet__"

// contextPacketMeta is the JSON shape of the metadata section.
type contextPacketMeta struct {
	PrefixHash         string                     `json:"prefix_hash"`
	BriefingHash       string                     `json:"briefing_hash"`
	InvalidationReason string                     `json:"invalidation_reason,omitempty"`
	Segments           []contextPacketSegmentMeta `json:"segments"`
}

// contextPacketSegmentMeta is the per-segment metadata that the plain sections
// JSONB list cannot express (role, tool-call identity, cache-prefix flag and the
// conservative token estimate).
type contextPacketSegmentMeta struct {
	Role       string `json:"role"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	Prefix     bool   `json:"prefix,omitempty"`
	Tokens     int    `json:"tokens"`
}

// EncodeContextPacket projects the cache-first packet produced by the assembler
// onto the frozen context_packet row (db-schema §18.2 #9): the ordered prefix
// and suffix segments are written to sections in packet order, token_counts
// keeps the per-segment/total estimates plus the cache bookkeeping, and hash is
// the stable prefix digest. One reserved metadata section carries the fields the
// generic row cannot express; a persisted row therefore round-trips through
// DecodeContextPacket without a schema migration.
func EncodeContextPacket(runID, versionID int64, packet agentcontext.ContextPacket) agent.ContextPacket {
	sections := make([]agent.ContextSection, 0, len(packet.Segments)+1)
	counts := make(map[string]int, len(packet.Segments)+4)
	meta := contextPacketMeta{
		PrefixHash:         packet.PrefixHash,
		BriefingHash:       packet.BriefingHash,
		InvalidationReason: packet.InvalidationReason,
		Segments:           make([]contextPacketSegmentMeta, 0, len(packet.Segments)),
	}
	total := 0
	for index, segment := range packet.Segments {
		sections = append(sections, agent.ContextSection{
			Name:    segment.Name,
			Content: segment.Content,
		})
		counts["segment_"+strconv.Itoa(index)] = segment.Tokens
		total += segment.Tokens
		meta.Segments = append(meta.Segments, contextPacketSegmentMeta{
			Role:       segment.Role,
			ToolCallID: segment.ToolCallID,
			Prefix:     segment.Prefix,
			Tokens:     segment.Tokens,
		})
	}
	counts["__total__"] = total
	counts["cached_tokens"] = packet.CachedTokens
	counts["cache_hit"] = boolInt(packet.CacheHit)

	encodedMeta, err := json.Marshal(meta)
	if err != nil {
		// The metadata is plain strings/ints; a marshal failure is impossible.
		encodedMeta = []byte("{}")
	}
	sections = append(sections, agent.ContextSection{
		Name:    contextPacketMetaSection,
		Content: string(encodedMeta),
	})

	hash := packet.PrefixHash
	if !isSHA256Hex(hash) {
		hash = hashPacketRow(versionID, sections, counts)
	}
	return agent.ContextPacket{
		RunID:       runID,
		VersionID:   versionID,
		Sections:    sections,
		TokenCounts: counts,
		Hash:        hash,
	}
}

// DecodeContextPacket reverses EncodeContextPacket. A row without the
// cache-first metadata section is a legacy P1 builder packet and is reported as
// not-found, never mis-decoded as a cache-first packet. The provider cache
// bookkeeping is read from token_counts because that is the only mutable part
// of the row.
func DecodeContextPacket(stored agent.ContextPacket) (agentcontext.ContextPacket, error) {
	metaIndex := -1
	for index, section := range stored.Sections {
		if section.Name == contextPacketMetaSection {
			metaIndex = index
			break
		}
	}
	if metaIndex < 0 {
		return agentcontext.ContextPacket{}, agentcontext.ErrContextPacketNotFound
	}
	var meta contextPacketMeta
	if err := json.Unmarshal([]byte(stored.Sections[metaIndex].Content), &meta); err != nil {
		return agentcontext.ContextPacket{}, fmt.Errorf("decode context packet metadata: %w", err)
	}
	segmentCount := len(stored.Sections) - 1
	if len(meta.Segments) != segmentCount {
		return agentcontext.ContextPacket{}, fmt.Errorf(
			"decode context packet: %d segment metadata entries for %d sections",
			len(meta.Segments), segmentCount)
	}
	packet := agentcontext.ContextPacket{
		PrefixHash:         meta.PrefixHash,
		BriefingHash:       meta.BriefingHash,
		InvalidationReason: meta.InvalidationReason,
		CachedTokens:       stored.TokenCounts["cached_tokens"],
		CacheHit:           stored.TokenCounts["cache_hit"] != 0,
		Segments:           make([]agentcontext.Segment, 0, segmentCount),
	}
	if packet.PrefixHash == "" {
		packet.PrefixHash = stored.Hash
	}
	metaIndexWalk := 0
	for index, section := range stored.Sections {
		if index == metaIndex {
			continue
		}
		segment := meta.Segments[metaIndexWalk]
		metaIndexWalk++
		packet.Segments = append(packet.Segments, agentcontext.Segment{
			Name:       section.Name,
			Role:       segment.Role,
			Content:    section.Content,
			ToolCallID: segment.ToolCallID,
			Prefix:     segment.Prefix,
			Tokens:     segment.Tokens,
		})
	}
	return packet, nil
}

// hashPacketRow derives a stable 64-hex digest for a packet whose prefix hash is
// missing (only a defensive fallback; the assembler always hashes the prefix).
func hashPacketRow(versionID int64, sections []agent.ContextSection, counts map[string]int) string {
	payload := struct {
		VersionID   int64                  `json:"version_id"`
		Sections    []agent.ContextSection `json:"sections"`
		TokenCounts map[string]int         `json:"token_counts"`
	}{VersionID: versionID, Sections: sections, TokenCounts: counts}
	raw, err := json.Marshal(payload)
	if err != nil {
		raw = []byte(fmt.Sprintf("marshal-error:%v", err))
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func isSHA256Hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
