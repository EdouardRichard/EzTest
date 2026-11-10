package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

// v0.22 A-6 tool-result governance constants (spec R-A6-1..R-A6-5, A §8).
//
// Byte-vs-character semantics are deliberately explicit:
//   - the spill threshold is measured in UTF-8 bytes (agent.tool_result_max_bytes);
//   - returned_chars/next_offset and the spill head/tail are measured in
//     characters (runes), so CJK content is never cut mid-rune.
const (
	// SettingToolResultMaxBytes is the settings-catalog key of the spill
	// threshold (R-A6-5, Task 4 catalog default 32768).
	SettingToolResultMaxBytes = "agent.tool_result_max_bytes"

	// DefaultToolResultMaxBytes is the fallback threshold when the setting is
	// missing or unreadable.
	DefaultToolResultMaxBytes = 32768

	// DefaultToolResultReadChars is the default page size of attachment.read_text
	// and tool_result.read (R-A6-2).
	DefaultToolResultReadChars = 8000

	// MaxToolResultReadChars is the page-size cap: larger requested pages are
	// clamped, and next_offset still advances by the actual returned count.
	MaxToolResultReadChars = 20000

	// ToolResultSpillHeadChars is the head kept inline when a result spills
	// (R-A6-3).
	ToolResultSpillHeadChars = 2000

	// ToolResultSpillTailChars is the tail kept inline when a result spills
	// (R-A6-3); head-only spill is explicitly forbidden.
	ToolResultSpillTailChars = 1000

	// PruneKeepRecentToolPairs is the number of newest tool call/result pairs
	// that compaction never rewrites (R-A6-4).
	PruneKeepRecentToolPairs = 2

	// toolResultSpillOwner is the content-ledger owner label shared with the
	// runner's existing spill path.
	toolResultSpillOwner = "agent_tool_result"

	// prunedToolResultNote tells the model how to recover pruned content.
	prunedToolResultNote = "older tool result elided before compaction; call tool_result.read with content_ref, offset and limit to read it back"
)

// ContentRef is the content-ledger reference of one spilled tool result. The
// tools package cannot import the agent application's identical type, so the
// runtime adapter converts between them (Task 36 wiring).
type ContentRef struct {
	Ref    string
	SHA256 string
	Size   int
}

// ContentWriter persists the full text of an oversized tool result. It is the
// tools-package view of the runtime's content storage port.
type ContentWriter interface {
	PutText(ctx context.Context, owner string, data []byte) (ContentRef, error)
}

// ContentReader reads a spilled tool result back by content_ref (R-A6-1).
type ContentReader interface {
	ReadText(ctx context.Context, contentRef string) (string, error)
}

// ContentStore lets tool_result.read and the spill/pruner helpers share one
// content-storage port.
type ContentStore interface {
	ContentWriter
	ContentReader
}

// SettingsPort is the narrow settings-catalog read surface used to resolve the
// spill threshold. settingsapp.Service.Int satisfies it (R-A6-5).
type SettingsPort interface {
	Int(ctx context.Context, key string) (int64, error)
}

// ToolResultDeps is the additive v0.22 tool-result governance surface. It is
// nested inside Deps so P1–P7 wiring keeps compiling untouched. A nil Content
// leaves tool_result.read unregistered (wire-or-don't-add); a nil Settings
// falls back to the catalog default threshold.
type ToolResultDeps struct {
	Content  ContentStore
	Settings SettingsPort
}

// ToolResultPolicy is the resolved spill policy of one run.
type ToolResultPolicy struct {
	// MaxBytes is the UTF-8 byte threshold above which a result spills. A
	// non-positive value disables spilling (the runner keeps its inline result).
	MaxBytes int
}

// LoadToolResultPolicy resolves agent.tool_result_max_bytes through the
// settings port (R-A6-5). A missing/unreadable setting or a non-positive value
// falls back to DefaultToolResultMaxBytes, matching the runner's intSetting
// behavior.
func LoadToolResultPolicy(ctx context.Context, settings SettingsPort) ToolResultPolicy {
	return ToolResultPolicy{MaxBytes: ToolResultMaxBytes(ctx, settings)}
}

// ToolResultMaxBytes returns the configured byte threshold with its catalog
// fallback.
func ToolResultMaxBytes(ctx context.Context, settings SettingsPort) int {
	if settings == nil {
		return DefaultToolResultMaxBytes
	}
	value, err := settings.Int(ctx, SettingToolResultMaxBytes)
	if err != nil || value <= 0 {
		return DefaultToolResultMaxBytes
	}
	return int(value)
}

// SpilledToolResult is the model-visible spill envelope (R-A6-3): head + tail +
// content_ref, never head-only. Note tells the model that the envelope's
// total_chars/truncated describe this result JSON, not the business object it
// carries (R2-C-03). The JSON tags are the payload the model sees; the runner
// may marshal the value directly.
type SpilledToolResult struct {
	Head       string `json:"head"`
	Tail       string `json:"tail,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	ContentRef string `json:"content_ref,omitempty"`
	Truncated  bool   `json:"truncated"`
	TotalChars int    `json:"total_chars"`
	TotalBytes int    `json:"total_bytes"`
	Note       string `json:"note,omitempty"`
}

// spilledToolResultNote disambiguates the envelope metadata from the payload's
// own business fields (for example attachment.read_text's attachment summary).
const spilledToolResultNote = "本结果是分页信封：head/tail 仅为片段，可用 tool_result.read(content_ref, offset, limit) 读回完整 JSON；信封的 total_chars/truncated 只描述本工具结果 JSON 的长度，不代表被读取业务对象（如附件全文）的字符数或完整性。"

// Payload renders the spill envelope with the stable field names. It is
// equivalent to marshaling the struct; the map form is convenient for callers
// that merge extra keys (for example the pruner's pruned/note flags).
func (s SpilledToolResult) Payload() map[string]any {
	payload := map[string]any{
		"head":        s.Head,
		"truncated":   s.Truncated,
		"total_chars": s.TotalChars,
		"total_bytes": s.TotalBytes,
	}
	if s.Tail != "" {
		payload["tail"] = s.Tail
	}
	if s.ContentRef != "" {
		payload["content_ref"] = s.ContentRef
	}
	if s.SHA256 != "" {
		payload["sha256"] = s.SHA256
	}
	if s.Note != "" {
		payload["note"] = s.Note
	}
	return payload
}

// SpillToolResult applies R-A6-3 to one encoded tool result: results at or
// below the byte threshold stay inline (Head is the full content and no ref is
// written); larger results are persisted in full through the content storage
// port and returned as head 2000 + tail 1000 + content_ref.
func SpillToolResult(ctx context.Context, writer ContentWriter, content string, policy ToolResultPolicy) (SpilledToolResult, error) {
	result := SpilledToolResult{
		TotalChars: utf8.RuneCountInString(content),
		TotalBytes: len(content),
	}
	if policy.MaxBytes <= 0 || len(content) <= policy.MaxBytes {
		result.Head = content
		return result, nil
	}
	if writer == nil {
		return SpilledToolResult{}, missingDep("tool result content storage")
	}
	ref, err := writer.PutText(ctx, toolResultSpillOwner, []byte(content))
	if err != nil {
		return SpilledToolResult{}, fmt.Errorf("spill tool result: %w", err)
	}
	result.Head, result.Tail = splitToolResultHeadTail(content)
	result.SHA256 = ref.SHA256
	result.ContentRef = ref.Ref
	result.Truncated = true
	result.Note = spilledToolResultNote
	return result, nil
}

// splitToolResultHeadTail cuts head/tail by characters so multi-byte runes are
// never split. When the content is shorter than head+tail, the two windows
// cover it completely instead of duplicating runes.
func splitToolResultHeadTail(content string) (string, string) {
	runes := []rune(content)
	head := ToolResultSpillHeadChars
	if head > len(runes) {
		head = len(runes)
	}
	tail := ToolResultSpillTailChars
	if remaining := len(runes) - head; tail > remaining {
		tail = remaining
	}
	tailStart := len(runes) - tail
	return string(runes[:head]), string(runes[tailStart:])
}

// toolResultReadParams is the model-facing argument shape of tool_result.read.
// pagination follows attachment.read_text: offset and limit are characters.
type toolResultReadParams struct {
	ContentRef string `json:"content_ref"`
	Offset     int    `json:"offset"`
	Limit      int    `json:"limit"`
}

// toolResultRead reads a spilled tool result back page by page (R-A6-1).
type toolResultRead struct{ deps Deps }

func (t *toolResultRead) Name() string { return ToolToolResultRead }

func (t *toolResultRead) Description() string {
	return "按 content_ref 分页读回工具结果 spill 的完整内容。offset 从 0 开始，limit 默认 8000、最多 20000；响应返回实际 returned_chars 与 next_offset，必须按 next_offset 前进，禁止按请求的 limit 跳段。注意：spill 信封的 total_chars 是被 spill 的工具结果 JSON 的字符数，不是被读取业务对象（如附件）的长度；业务对象的完整性看其自身字段（attachment.read_text 看 attachment.total_chars/text_complete）。"
}

func (t *toolResultRead) Parameters() json.RawMessage {
	return schemaObject(map[string]any{
		"content_ref": map[string]any{
			"type":        "string",
			"minLength":   1,
			"description": "spill 结果返回的 content_ref。",
		},
		"offset": map[string]any{
			"type":        "integer",
			"minimum":     0,
			"description": "起始字符偏移，默认 0。",
		},
		"limit": map[string]any{
			"type":        "integer",
			"minimum":     1,
			"description": "本次最多返回的字符数，默认 8000，超过 20000 按 20000 截断。",
		},
	}, "content_ref")
}

func (t *toolResultRead) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	params := toolResultReadParams{Limit: DefaultToolResultReadChars}
	if err := decodeParams(raw, &params); err != nil {
		return nil, err
	}
	contentRef := strings.TrimSpace(params.ContentRef)
	if contentRef == "" {
		return nil, Invalid("content_ref", "must not be empty")
	}
	if params.Offset < 0 {
		return nil, Invalid("offset", "must not be negative")
	}
	if params.Limit <= 0 {
		return nil, Invalid("limit", "must be positive")
	}
	if params.Limit > MaxToolResultReadChars {
		params.Limit = MaxToolResultReadChars
	}
	if t.deps.ToolResult.Content == nil {
		return nil, missingDep("tool result content storage")
	}
	content, err := t.deps.ToolResult.Content.ReadText(ctx, contentRef)
	if err != nil {
		return nil, mapPortError(err)
	}
	runes := []rune(content)
	total := len(runes)
	offset := params.Offset
	if offset > total {
		offset = total
	}
	end := offset + params.Limit
	if end > total {
		end = total
	}
	returned := end - offset
	nextOffset := offset + returned
	return map[string]any{
		"content_ref":    contentRef,
		"offset":         offset,
		"limit":          params.Limit,
		"text":           string(runes[offset:end]),
		"returned_chars": returned,
		"next_offset":    nextOffset,
		"total_chars":    total,
		"truncated":      nextOffset < total,
	}, nil
}

// PruneToolResultMessages rewrites the content of tool results older than the
// newest keepRecent tool call/result pairs (R-A6-4). It never drops or reorders
// messages and never touches assistant tool calls, so tool call/result pairing
// stays intact. The caller's slice and message contents are not mutated; a
// shallow copy with rewritten tool contents is returned.
//
// keepRecent < 0 uses PruneKeepRecentToolPairs; keepRecent == 0 prunes every
// tool result. Older results are stored in full through writer and replaced by
// head/tail + content_ref. Already-bounded results (spill or prune envelopes)
// are left alone so repeated pruning is idempotent and does not duplicate
// content objects.
func PruneToolResultMessages(ctx context.Context, messages []llmapp.ChatMessage, writer ContentWriter, keepRecent int) ([]llmapp.ChatMessage, error) {
	if keepRecent < 0 {
		keepRecent = PruneKeepRecentToolPairs
	}
	pruned := append([]llmapp.ChatMessage(nil), messages...)
	toolIndexes := make([]int, 0, len(messages))
	for index := len(messages) - 1; index >= 0; index-- {
		if strings.TrimSpace(messages[index].Role) == "tool" {
			toolIndexes = append(toolIndexes, index)
		}
	}
	if len(toolIndexes) <= keepRecent {
		return pruned, nil
	}
	for _, index := range toolIndexes[keepRecent:] {
		content := messages[index].Content
		if content == "" || toolResultAlreadyBounded(content) {
			continue
		}
		if writer == nil {
			return nil, missingDep("tool result content storage")
		}
		ref, err := writer.PutText(ctx, toolResultSpillOwner, []byte(content))
		if err != nil {
			return nil, fmt.Errorf("prune tool result: %w", err)
		}
		head, tail := splitToolResultHeadTail(content)
		spilled := SpilledToolResult{
			Head:       head,
			Tail:       tail,
			SHA256:     ref.SHA256,
			ContentRef: ref.Ref,
			Truncated:  true,
			TotalChars: utf8.RuneCountInString(content),
			TotalBytes: len(content),
		}
		replacement, err := prunedToolResultContent(spilled)
		if err != nil {
			return nil, err
		}
		pruned[index].Content = replacement
	}
	return pruned, nil
}

// prunedToolResultContent renders the bounded replacement of one old tool
// result, reusing the spill envelope plus the pruned/note markers.
func prunedToolResultContent(spilled SpilledToolResult) (string, error) {
	payload := spilled.Payload()
	payload["pruned"] = true
	payload["note"] = prunedToolResultNote
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode pruned tool result: %w", err)
	}
	return string(encoded), nil
}

// toolResultAlreadyBounded reports whether content is already a spill/prune
// envelope carrying a content_ref, so a repeated pruner pass is a no-op.
func toolResultAlreadyBounded(content string) bool {
	var envelope struct {
		ContentRef string `json:"content_ref"`
		Truncated  bool   `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(content), &envelope); err != nil {
		return false
	}
	return envelope.ContentRef != "" && envelope.Truncated
}
