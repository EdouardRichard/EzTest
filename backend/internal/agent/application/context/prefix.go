package agentcontext

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// Placeholders anchor the dynamic situation inside the byte-stable prefix
// (spec §6.3). They are deliberately literal: the prefix is rendered once and
// then reused verbatim, so replacing them with per-round data would invalidate
// the provider prefix cache. New situation data is appended to the suffix.
const (
	PlaceholderLatestSituation = "{{LATEST_SITUATION}}"
	PlaceholderRecentActivity  = "{{RECENT_ACTIVITY}}"
)

// MaxToolResultBytes is the spec §6.3 head+ref threshold. A tool result larger
// than this is appended as its byte head plus a lazy-load reference instead of
// being replayed whole on every later round.
const MaxToolResultBytes = 8 * 1024

// Stable names of the assembled segments. They are the machine identity of a
// segment and never change between renders, so a persisted packet can be
// compared and compacted by name.
const (
	SegmentPrefix     = "prefix"
	SegmentBriefing   = "briefing"
	SegmentFacts      = "facts"
	SegmentHistory    = "history"
	SegmentToolResult = "tool_result"
)

const (
	roleSystem = "system"
	roleUser   = "user"
	roleTool   = "tool"

	unknownStage = "未知"
)

// Invalidation is the boundary that may rebuild the stable prefix (spec §6.3).
// The zero value is a normal append-only round, where the prefix must not
// change at all.
type Invalidation string

const (
	// InvalidationNone is a normal round: only the append region may grow.
	InvalidationNone Invalidation = ""
	// InvalidationStageBoundary is a version stage transition. Stage is part of
	// the stable prefix, so the prefix is rebuilt once and re-cached.
	InvalidationStageBoundary Invalidation = "stage_boundary"
	// InvalidationSessionReset is a new conversation/session: a fresh prompt
	// chain starts with no cached prefix.
	InvalidationSessionReset Invalidation = "session_reset"
	// InvalidationWindowFull is a context-window-triggered rebuild.
	InvalidationWindowFull Invalidation = "window_full"
)

func (b Invalidation) valid() bool {
	switch b {
	case InvalidationNone, InvalidationStageBoundary,
		InvalidationSessionReset, InvalidationWindowFull:
		return true
	default:
		return false
	}
}

// Message is one assembled LLM transcript message. It matches the
// conversation message shape used by the agent application package; ID is
// carried through for history messages and is zero for prefix/dynamic
// messages. ToolCallID is set for tool messages and copied from history
// verbatim.
type Message struct {
	ID         int64
	Role       string
	Content    string
	ToolCallID string
}

// ToolResult is one tool result appended to the transcript suffix. Ref is the
// lazy-loading handle of the full result; callers normally supply it, and a
// deterministic tool_result:<name>/<call_id> handle is used when it is empty.
type ToolResult struct {
	CallID  string
	Name    string
	Content string
	Ref     string
}

// Segment is one ordered block of the assembled context packet. Prefix marks
// the byte-stable cacheable head; every non-prefix segment belongs to the
// append-only suffix. Tokens is the conservative local token estimate.
type Segment struct {
	Name       string
	Role       string
	Content    string
	ToolCallID string
	// MessageID is the persisted conversation message the segment replayed
	// (R-A5-12); it is the watermark source of the folded range. Zero for
	// derived segments (prefix/briefing/facts/summary/tool results).
	MessageID int64
	Prefix    bool
	Tokens    int
}

// ContextPacket records the cache-first bookkeeping of one assembly (spec
// §6.3): the stable prefix digest, its ordered segments, the reuse estimate and
// the dynamic briefing digest.
//
// CachedTokens is a local conservative estimate of the reusable stable-prefix
// tokens. The provider-reported usage (contracts §18.2.6) is authoritative and
// must overwrite it on the runner path when context.cache_stats is published;
// the assembler runs before the model call and therefore cannot know it.
type ContextPacket struct {
	PrefixHash         string
	Segments           []Segment
	CachedTokens       int
	CacheHit           bool
	InvalidationReason string
	BriefingHash       string
}

// Assembly is one fully rendered prompt: the ordered messages plus the packet
// metadata the runner persists as context_packet.
type Assembly struct {
	Messages []Message
	Packet   ContextPacket
}

// AssemblyRequest is one assembly round. System, Invariants and Stage are the
// only stable-prefix inputs; every other field is append-only dynamic material.
//
// Previous is the packet assembled for the same prompt chain on the previous
// round. Nil starts a new chain (no cache hit). Invalidation declares a known
// prefix rebuild boundary; a prefix change without one is rejected so dynamic
// data can never silently break the prefix cache.
type AssemblyRequest struct {
	// System is the rendered role prompt: role/rules/tool schema/output
	// contract (spec §6.3 stable prefix).
	System string
	// Invariants are the rendered version invariant lines (goal, acceptance
	// criteria, confirmed requirements, boundaries).
	Invariants []string
	// Stage is the version stage. It is part of the stable prefix, so a stage
	// transition changes PrefixHash and must be declared via Invalidation.
	Stage string
	// Invalidation is the known boundary trigger; the zero value is a normal
	// append-only round.
	Invalidation Invalidation
	// Previous is the last packet of this prompt chain, used for cache
	// accounting and prefix-rewrite detection. Nil on the first round.
	Previous *ContextPacket
	// Briefing is the rendered version briefing (Task 12). It is appended at
	// the tail of the suffix on every round.
	Briefing string
	// Facts are rendered current fact lines, appended after the briefing.
	Facts []string
	// History is the append-only conversation transcript, replayed verbatim.
	History []Message
	// ToolResults are newly produced tool results. Oversized results are
	// truncated to head + lazy-load reference.
	ToolResults []ToolResult
}

// ErrPrefixChanged reports a stable-prefix rewrite that was not declared
// through AssemblyRequest.Invalidation. The prefix may only change at a
// stage boundary, a session reset or a window-full rebuild (spec §6.3).
var ErrPrefixChanged = errors.New("agentcontext: stable prefix changed outside a declared invalidation boundary")

// Assembler renders the cache-first context layout. The zero value and a nil
// receiver are both ready to use; NewAssembler exists for discoverability.
type Assembler struct{}

// NewAssembler returns a cache-first context assembler.
func NewAssembler() *Assembler { return &Assembler{} }

// Build renders the stable prefix and the append-only suffix of one round.
//
// The prefix is derived only from System, Invariants and Stage (plus the fixed
// placeholders); it is hashed and compared with Previous.PrefixHash. Dynamic
// changes never touch it. When Previous is set and the prefix changed without
// an Invalidation, Build fails closed with ErrPrefixChanged instead of caching
// an invalidated prefix. Stage, session-reset and window-full rebuilds are
// reported through ContextPacket.InvalidationReason.
func (a *Assembler) Build(ctx context.Context, request AssemblyRequest) (Assembly, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return Assembly{}, err
		}
	}
	if !request.Invalidation.valid() {
		return Assembly{}, platform.Invalid("invalidation",
			"must be one of stage_boundary, session_reset, window_full")
	}

	prefix := Segment{
		Name:    SegmentPrefix,
		Role:    roleSystem,
		Content: stablePrefixContent(request.System, request.Invariants, request.Stage),
		Prefix:  true,
	}
	prefix.Tokens = estimateBriefingTokens(prefix.Content)
	prefixHash := hashPrefix([]Segment{prefix})

	previous := request.Previous
	prefixChanged := previous != nil && previous.PrefixHash != "" && previous.PrefixHash != prefixHash
	if prefixChanged && request.Invalidation == InvalidationNone {
		return Assembly{}, fmt.Errorf("%w: previous prefix_hash %s, current %s",
			ErrPrefixChanged, previous.PrefixHash, prefixHash)
	}
	cacheHit := request.Invalidation == InvalidationNone &&
		previous != nil && previous.PrefixHash == prefixHash

	segments := make([]Segment, 0, 1+2+len(request.History)+len(request.ToolResults))
	messages := make([]Message, 0, cap(segments))
	segments = append(segments, prefix)
	messages = append(messages, Message{Role: prefix.Role, Content: prefix.Content})

	if briefing := renderBriefing(request.Briefing); briefing != "" {
		segments = append(segments, withTokens(Segment{
			Name:    SegmentBriefing,
			Role:    roleUser,
			Content: briefing,
		}))
		messages = append(messages, Message{Role: roleUser, Content: briefing})
	}
	if facts := renderFacts(request.Facts); facts != "" {
		segments = append(segments, withTokens(Segment{
			Name:    SegmentFacts,
			Role:    roleUser,
			Content: facts,
		}))
		messages = append(messages, Message{Role: roleUser, Content: facts})
	}
	// A compacted packet carries its rolling summary in the suffix. The summary
	// is not derivable from the request fields, so Build carries it forward from
	// Previous.Segments and places it before the replayed transcript: it
	// describes older history the caller no longer replays. This is how a
	// compacted packet re-enters the next assembly; without it the summary would
	// only survive on the persisted packet and the next round would lose it.
	// A session_reset starts a fresh prompt chain: a stale summary from the
	// previous chain must not leak into the new session, even when a caller
	// still passes the previous packet.
	if request.Invalidation != InvalidationSessionReset {
		for _, summary := range previousSummaries(previous) {
			summary.Prefix = false
			summary = withTokens(summary)
			segments = append(segments, summary)
			messages = append(messages, Message{
				Role:       summary.Role,
				Content:    summary.Content,
				ToolCallID: summary.ToolCallID,
			})
		}
	}
	// History is replayed verbatim: no merging, reordering or truncation. IDs
	// and tool-call identities are preserved so an earlier append is never
	// rewritten.
	for _, message := range request.History {
		segments = append(segments, withTokens(Segment{
			Name:       SegmentHistory,
			Role:       message.Role,
			Content:    message.Content,
			ToolCallID: message.ToolCallID,
			MessageID:  message.ID,
		}))
		messages = append(messages, message)
	}
	for _, result := range request.ToolResults {
		content := renderToolResult(result)
		segments = append(segments, withTokens(Segment{
			Name:       SegmentToolResult,
			Role:       roleTool,
			Content:    content,
			ToolCallID: result.CallID,
		}))
		messages = append(messages, Message{
			Role:       roleTool,
			Content:    content,
			ToolCallID: result.CallID,
		})
	}

	packet := ContextPacket{
		PrefixHash:         prefixHash,
		Segments:           segments,
		CacheHit:           cacheHit,
		InvalidationReason: string(request.Invalidation),
		BriefingHash:       digestText(renderBriefing(request.Briefing)),
	}
	if cacheHit {
		packet.CachedTokens = cachedPrefixTokens(segments)
	}
	return Assembly{Messages: messages, Packet: packet}, nil
}

// stablePrefixContent renders the byte-stable prefix. It is a pure function of
// its inputs: the system prompt, the invariant lines, the stage and the two
// fixed placeholders. Dynamic data must never be passed in here.
func stablePrefixContent(system string, invariants []string, stage string) string {
	var builder strings.Builder
	if prompt := strings.TrimSpace(system); prompt != "" {
		builder.WriteString(prompt)
		builder.WriteString("\n\n")
	}
	builder.WriteString("# 版本不变量\n")
	for _, invariant := range invariants {
		line := strings.TrimSpace(invariant)
		if line == "" {
			continue
		}
		builder.WriteString("- ")
		builder.WriteString(line)
		builder.WriteString("\n")
	}
	if stage = strings.TrimSpace(stage); stage == "" {
		stage = unknownStage
	}
	builder.WriteString("阶段: ")
	builder.WriteString(stage)
	builder.WriteString("\n\n")
	builder.WriteString(PlaceholderLatestSituation)
	builder.WriteString("\n")
	builder.WriteString(PlaceholderRecentActivity)
	return builder.String()
}

func renderBriefing(briefing string) string {
	briefing = strings.TrimSpace(briefing)
	if briefing == "" {
		return ""
	}
	return "# 版本简报\n" + briefing
}

func renderFacts(facts []string) string {
	lines := make([]string, 0, len(facts))
	for _, fact := range facts {
		if line := strings.TrimSpace(fact); line != "" {
			lines = append(lines, "- "+line)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "# 版本事实\n" + strings.Join(lines, "\n")
}

// renderToolResult applies the head+ref rule to one tool result. Small results
// are returned untouched; oversized results keep their byte head, a marker with
// the omitted byte count and a lazy-load reference.
func renderToolResult(result ToolResult) string {
	if len(result.Content) <= MaxToolResultBytes {
		return result.Content
	}
	head := byteHead(result.Content, MaxToolResultBytes)
	omitted := len(result.Content) - len(head)
	ref := strings.TrimSpace(result.Ref)
	if ref == "" {
		ref = fallbackToolResultRef(result)
	}
	return head + fmt.Sprintf("\n\n…[truncated: %d bytes omitted; ref %s]", omitted, ref)
}

// byteHead returns the longest prefix of text no longer than limit bytes that
// does not split a UTF-8 rune.
func byteHead(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	end := limit
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end]
}

func fallbackToolResultRef(result ToolResult) string {
	callID := strings.TrimSpace(result.CallID)
	name := strings.TrimSpace(result.Name)
	switch {
	case name != "" && callID != "":
		return "tool_result:" + name + "/" + callID
	case callID != "":
		return "tool_result:" + callID
	case name != "":
		return "tool_result:" + name
	default:
		return "tool_result:unknown"
	}
}

func withTokens(segment Segment) Segment {
	segment.Tokens = estimateBriefingTokens(segment.Content)
	return segment
}

// previousSummaries returns the rolling summary segments of the previous
// packet in packet order. Build carries them into the next assembly verbatim
// (only the token estimate is recomputed), so a compacted packet's summary
// survives a rebuild without being re-rendered from the transcript. Callers
// must not use them for a session_reset chain.
func previousSummaries(previous *ContextPacket) []Segment {
	if previous == nil {
		return nil
	}
	summaries := make([]Segment, 0, 1)
	for _, segment := range previous.Segments {
		if segment.Name == SegmentSummary {
			summaries = append(summaries, segment)
		}
	}
	return summaries
}

func cachedPrefixTokens(segments []Segment) int {
	total := 0
	for _, segment := range segments {
		if segment.Prefix {
			total += segment.Tokens
		}
	}
	return total
}

// hashPrefix hashes exactly the bytes that make up the stable prefix: the
// ordered role/content pairs of its segments, length-prefixed so adjacent
// fields cannot be confused. A change to any prefix byte changes the digest;
// suffix changes never do.
func hashPrefix(segments []Segment) string {
	digest := sha256.New()
	for _, segment := range segments {
		if !segment.Prefix {
			continue
		}
		writeLengthPrefixed(digest, segment.Role)
		writeLengthPrefixed(digest, segment.Content)
		writeLengthPrefixed(digest, segment.ToolCallID)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func digestText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func writeLengthPrefixed(digest interface{ Write([]byte) (int, error) }, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = digest.Write(size[:])
	_, _ = digest.Write([]byte(value))
}
