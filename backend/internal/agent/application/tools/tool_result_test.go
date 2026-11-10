package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

// --- fakes -----------------------------------------------------------------

type fakeToolResultSettings struct {
	values map[string]int64
	err    error
}

func (f *fakeToolResultSettings) Int(_ context.Context, key string) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	if value, ok := f.values[key]; ok {
		return value, nil
	}
	return 0, fmt.Errorf("setting %q is not configured", key)
}

type fakeContentStore struct {
	objects map[string]string
	owners  []string
	next    int
	putErr  error
	readErr error
}

func newFakeContentStore() *fakeContentStore {
	return &fakeContentStore{objects: make(map[string]string)}
}

func (f *fakeContentStore) PutText(_ context.Context, owner string, data []byte) (ContentRef, error) {
	if f.putErr != nil {
		return ContentRef{}, f.putErr
	}
	f.next++
	ref := ContentRef{
		Ref:    fmt.Sprintf("content-ref-%d", f.next),
		SHA256: fmt.Sprintf("sha256-%d", f.next),
		Size:   len(data),
	}
	f.objects[ref.Ref] = string(data)
	f.owners = append(f.owners, owner)
	return ref, nil
}

func (f *fakeContentStore) ReadText(_ context.Context, contentRef string) (string, error) {
	if f.readErr != nil {
		return "", f.readErr
	}
	text, ok := f.objects[contentRef]
	if !ok {
		return "", NotFound("content")
	}
	return text, nil
}

// spillWithStore runs the production spill helper against a fake content store.
func spillWithStore(t *testing.T, content string) (SpilledToolResult, *fakeContentStore) {
	t.Helper()
	store := newFakeContentStore()
	out, err := SpillToolResult(context.Background(), store, content, ToolResultPolicy{MaxBytes: DefaultToolResultMaxBytes})
	if err != nil {
		t.Fatalf("SpillToolResult: %v", err)
	}
	return out, store
}

// --- spill ----------------------------------------------------------------

func TestSpillReturnsHeadAndTail(t *testing.T) {
	content := strings.Repeat("a", 40000)
	out, store := spillWithStore(t, content)

	if !strings.Contains(out.Head, "a") || !strings.Contains(out.Tail, "a") {
		t.Fatalf("spill lacks head/tail: head=%d tail=%d", len(out.Head), len(out.Tail))
	}
	if out.ContentRef == "" || out.SHA256 == "" {
		t.Fatalf("spill ref = %+v", out)
	}
	if got := utf8.RuneCountInString(out.Head); got != ToolResultSpillHeadChars {
		t.Fatalf("head chars = %d, want %d", got, ToolResultSpillHeadChars)
	}
	if got := utf8.RuneCountInString(out.Tail); got != ToolResultSpillTailChars {
		t.Fatalf("tail chars = %d, want %d", got, ToolResultSpillTailChars)
	}
	if !out.Truncated || out.TotalBytes != len(content) || out.TotalChars != len(content) {
		t.Fatalf("spill metadata = %+v", out)
	}
	if store.objects[out.ContentRef] != content {
		t.Fatal("spilled content is not retrievable in full by content_ref")
	}
	t.Logf("spill: content_ref=%s sha256=%s head_chars=%d tail_chars=%d total_chars=%d total_bytes=%d",
		out.ContentRef, out.SHA256, utf8.RuneCountInString(out.Head), utf8.RuneCountInString(out.Tail),
		out.TotalChars, out.TotalBytes)

	encoded, err := json.Marshal(out.Payload())
	if err != nil {
		t.Fatalf("marshal spill payload: %v", err)
	}
	payload := string(encoded)
	for _, want := range []string{`"head"`, `"tail"`, `"content_ref"`, `"truncated":true`} {
		if !strings.Contains(payload, want) {
			t.Fatalf("spill payload %s lacks %s", payload, want)
		}
	}
}

// TestSpillKeepsRuneBoundaries pins R-A6-6: head/tail slicing is character
// based, so CJK content is never cut mid-rune.
func TestSpillKeepsRuneBoundaries(t *testing.T) {
	content := strings.Repeat("中文", 20000) // 40000 chars / 120000 bytes
	out, _ := spillWithStore(t, content)

	for name, value := range map[string]string{"head": out.Head, "tail": out.Tail} {
		if !utf8.ValidString(value) {
			t.Fatalf("%s is not valid UTF-8", name)
		}
		if strings.ContainsRune(value, utf8.RuneError) {
			t.Fatalf("%s contains a replacement rune (mid-rune cut)", name)
		}
	}
	if !strings.HasSuffix(out.Head, "文") || !strings.HasPrefix(out.Tail, "中") {
		t.Fatalf("head/tail split does not follow rune boundaries: head=%q tail=%q",
			string([]rune(out.Head)[len([]rune(out.Head))-1:]), string([]rune(out.Tail)[:1]))
	}
}

// TestSpillUsesByteThreshold proves the threshold is bytes, not characters: a
// CJK result below the byte limit stays inline even though its rune count is
// also below; one above the byte limit spills.
func TestSpillUsesByteThreshold(t *testing.T) {
	inline := strings.Repeat("中", (DefaultToolResultMaxBytes/len("中"))-1)
	store := newFakeContentStore()
	out, err := SpillToolResult(context.Background(), store, inline, ToolResultPolicy{MaxBytes: DefaultToolResultMaxBytes})
	if err != nil {
		t.Fatalf("SpillToolResult: %v", err)
	}
	if out.Truncated || out.ContentRef != "" || len(store.owners) != 0 {
		t.Fatalf("content under the byte limit was spilled: %+v", out)
	}

	content := strings.Repeat("中", 12000) // 12000 chars, 36000 bytes
	out, err = SpillToolResult(context.Background(), store, content, ToolResultPolicy{MaxBytes: DefaultToolResultMaxBytes})
	if err != nil {
		t.Fatalf("SpillToolResult: %v", err)
	}
	if !out.Truncated || out.TotalChars != 12000 || out.TotalBytes != 36000 {
		t.Fatalf("CJK byte threshold metadata = %+v", out)
	}
	if out.TotalChars >= DefaultToolResultMaxBytes || out.TotalBytes <= DefaultToolResultMaxBytes {
		t.Fatalf("test content is not discriminating: chars=%d bytes=%d", out.TotalChars, out.TotalBytes)
	}
}

// TestLoadToolResultPolicyReadsSettings pins R-A6-5: the threshold comes from
// the settings catalog (agent.tool_result_max_bytes), never a hardcoded call-site.
func TestLoadToolResultPolicyReadsSettings(t *testing.T) {
	settings := &fakeToolResultSettings{values: map[string]int64{SettingToolResultMaxBytes: 1024}}
	if got := ToolResultMaxBytes(context.Background(), settings); got != 1024 {
		t.Fatalf("ToolResultMaxBytes = %d, want configured 1024", got)
	}
	if got := LoadToolResultPolicy(context.Background(), settings).MaxBytes; got != 1024 {
		t.Fatalf("policy max bytes = %d, want 1024", got)
	}

	for name, reader := range map[string]SettingsPort{
		"nil":          nil,
		"unconfigured": &fakeToolResultSettings{values: map[string]int64{}},
		"error":        &fakeToolResultSettings{err: fmt.Errorf("settings unavailable")},
	} {
		if got := ToolResultMaxBytes(context.Background(), reader); got != DefaultToolResultMaxBytes {
			t.Fatalf("%s reader: ToolResultMaxBytes = %d, want default %d", name, got, DefaultToolResultMaxBytes)
		}
	}
}

// TestSpillUsesConfiguredThreshold lowers the setting through the fake settings
// reader and proves the spill decision follows it.
func TestSpillUsesConfiguredThreshold(t *testing.T) {
	settings := &fakeToolResultSettings{values: map[string]int64{SettingToolResultMaxBytes: 512}}
	policy := LoadToolResultPolicy(context.Background(), settings)
	store := newFakeContentStore()
	content := strings.Repeat("x", 1000)

	out, err := SpillToolResult(context.Background(), store, content, policy)
	if err != nil {
		t.Fatalf("SpillToolResult: %v", err)
	}
	if !out.Truncated || out.ContentRef == "" {
		t.Fatalf("lowered threshold did not spill: %+v", out)
	}

	// The same content under the default threshold stays inline.
	defaultOut, err := SpillToolResult(context.Background(), store, content,
		ToolResultPolicy{MaxBytes: DefaultToolResultMaxBytes})
	if err != nil {
		t.Fatalf("SpillToolResult: %v", err)
	}
	if defaultOut.Truncated || len(store.owners) != 1 {
		t.Fatalf("default threshold spilled inline content: %+v (uploads=%d)", defaultOut, len(store.owners))
	}
}

func TestSpillWithoutContentStoreFailsClosed(t *testing.T) {
	if _, err := SpillToolResult(context.Background(), nil, strings.Repeat("a", 40000),
		ToolResultPolicy{MaxBytes: DefaultToolResultMaxBytes}); err == nil {
		t.Fatal("SpillToolResult succeeded without a content store")
	}
}

// TestSpillToolResultNoteExplainsEnvelope pins R2-C-03's spill-envelope note:
// the envelope's total_chars/truncated describe the tool result JSON, not the
// business object (for example the attachment full text), and tool_result.read
// is how the full JSON is read back.
func TestSpillToolResultNoteExplainsEnvelope(t *testing.T) {
	out, _ := spillWithStore(t, strings.Repeat("a", 40000))
	for _, want := range []string{"不代表被读取业务对象", "tool_result.read"} {
		if !strings.Contains(out.Note, want) {
			t.Fatalf("spill note lacks %q: %s", want, out.Note)
		}
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal spill envelope: %v", err)
	}
	if !strings.Contains(string(encoded), `"note"`) {
		t.Fatalf("spill envelope JSON lacks the note field: %s", encoded)
	}
}

// --- tool_result.read ------------------------------------------------------

func TestToolResultReadPaginationAndLimitCap(t *testing.T) {
	content := strings.Repeat("中", 25000)
	spilled, store := spillWithStore(t, content)
	registry := NewP7Registry(Deps{ToolResult: ToolResultDeps{Content: store}})

	result, err := execute(t, registry, Scope{VersionID: 10}, ToolToolResultRead,
		fmt.Sprintf(`{"content_ref":%q}`, spilled.ContentRef))
	if err != nil {
		t.Fatalf("tool_result.read: %v", err)
	}
	body := resultMap(t, result)
	if body["returned_chars"] != float64(DefaultToolResultReadChars) || body["next_offset"] != float64(DefaultToolResultReadChars) {
		t.Fatalf("default page = %#v, want 8000/8000", body)
	}

	// A requested limit above the 20000 cap is clamped, and next_offset moves by
	// the actual returned count rather than the requested limit.
	result, err = execute(t, registry, Scope{VersionID: 10}, ToolToolResultRead,
		fmt.Sprintf(`{"content_ref":%q,"offset":0,"limit":30000}`, spilled.ContentRef))
	if err != nil {
		t.Fatalf("tool_result.read capped: %v", err)
	}
	body = resultMap(t, result)
	if body["limit"] != float64(MaxToolResultReadChars) ||
		body["returned_chars"] != float64(MaxToolResultReadChars) ||
		body["next_offset"] != float64(MaxToolResultReadChars) {
		t.Fatalf("capped page = %#v, want limit/returned/next = 20000", body)
	}

	result, err = execute(t, registry, Scope{VersionID: 10}, ToolToolResultRead,
		fmt.Sprintf(`{"content_ref":%q,"offset":20000,"limit":30000}`, spilled.ContentRef))
	if err != nil {
		t.Fatalf("tool_result.read final page: %v", err)
	}
	body = resultMap(t, result)
	if body["returned_chars"] != float64(5000) || body["next_offset"] != float64(25000) ||
		body["total_chars"] != float64(25000) || body["truncated"] != false {
		t.Fatalf("final page = %#v", body)
	}
}

// TestToolResultReadDescriptionDistinguishesEnvelopeFromBusinessObject pins the
// description half of R2-C-03: the spill envelope's total_chars is the tool
// result JSON length, while attachment completeness is attachment.total_chars.
func TestToolResultReadDescriptionDistinguishesEnvelopeFromBusinessObject(t *testing.T) {
	description := (&toolResultRead{}).Description()
	for _, want := range []string{"业务对象", "attachment.total_chars"} {
		if !strings.Contains(description, want) {
			t.Fatalf("tool_result.read description lacks %q: %s", want, description)
		}
	}
}

// TestToolResultRead95kPagination is the A-8 acceptance read-through: a 95k
// character spilled result is reconstructed page by page with no skipped
// segment. The verbose output is captured as Task 14 evidence.
func TestToolResultRead95kPagination(t *testing.T) {
	content := strings.Repeat("中", 95000)
	spilled, store := spillWithStore(t, content)
	registry := NewP7Registry(Deps{ToolResult: ToolResultDeps{Content: store}})

	var rebuilt strings.Builder
	offset := 0
	pages := 0
	for pages < 20 {
		result, err := execute(t, registry, Scope{VersionID: 10}, ToolToolResultRead,
			fmt.Sprintf(`{"content_ref":%q,"offset":%d,"limit":20000}`, spilled.ContentRef, offset))
		if err != nil {
			t.Fatalf("page at offset %d: %v", offset, err)
		}
		body := resultMap(t, result)
		returned := int(body["returned_chars"].(float64))
		next := int(body["next_offset"].(float64))
		text, _ := body["text"].(string)
		if next != offset+returned {
			t.Fatalf("page offset=%d returned=%d next=%d breaks next_offset = offset + returned", offset, returned, next)
		}
		rebuilt.WriteString(text)
		pages++
		t.Logf("page %d: offset=%d limit=%d returned=%d next_offset=%d total=%d",
			pages, offset, MaxToolResultReadChars, returned, next, int(body["total_chars"].(float64)))
		if returned == 0 || next >= len([]rune(content)) {
			break
		}
		if next <= offset {
			t.Fatalf("pagination did not advance: offset=%d next=%d", offset, next)
		}
		offset = next
	}
	if pages != 5 {
		t.Fatalf("95k read-through pages = %d, want 5", pages)
	}
	if rebuilt.String() != content {
		t.Fatalf("read-through mismatch: rebuilt %d runes, want %d", len([]rune(rebuilt.String())), len([]rune(content)))
	}
	if !utf8.ValidString(rebuilt.String()) {
		t.Fatal("rebuilt content is not valid UTF-8")
	}
	t.Logf("read-through complete: %d pages, %d chars, no skipped segment", pages, len([]rune(rebuilt.String())))
}

func TestToolResultReadRejectsInvalidArguments(t *testing.T) {
	store := newFakeContentStore()
	store.objects["ref"] = "abc"
	registry := NewP7Registry(Deps{ToolResult: ToolResultDeps{Content: store}})
	for _, params := range []string{
		`{}`,
		`{"content_ref":""}`,
		`{"content_ref":"ref","offset":-1}`,
		`{"content_ref":"ref","limit":0}`,
		`{"content_ref":"ref","limit":-5}`,
		`{"content_ref":"ref","unknown":true}`,
	} {
		if _, err := execute(t, registry, Scope{VersionID: 10}, ToolToolResultRead, params); err == nil {
			t.Fatalf("expected an error for %s", params)
		}
	}

	// An offset past the end is a successful empty page whose next_offset is the
	// content end, never a skipped/overflowed offset.
	result, err := execute(t, registry, Scope{VersionID: 10}, ToolToolResultRead,
		`{"content_ref":"ref","offset":10,"limit":100}`)
	if err != nil {
		t.Fatalf("offset past end: %v", err)
	}
	body := resultMap(t, result)
	if body["returned_chars"] != float64(0) || body["next_offset"] != float64(3) || body["text"] != "" {
		t.Fatalf("offset past end page = %#v", body)
	}
}

// --- pruner ----------------------------------------------------------------

func TestPrunerKeepsRecentTwoToolPairs(t *testing.T) {
	old1 := strings.Repeat("旧", 5000)
	old2 := "small old result 二"
	third := "third result"
	fourth := "fourth result"
	messages := []llmapp.ChatMessage{
		{Role: "user", Content: "hello"},
		{Role: "assistant", ToolCalls: []llmapp.ToolCall{{ID: "call-1", Name: "attachment.read_text"}}},
		{Role: "tool", ToolCallID: "call-1", Content: old1},
		{Role: "assistant", ToolCalls: []llmapp.ToolCall{{ID: "call-2", Name: "attachment.read_text"}}},
		{Role: "tool", ToolCallID: "call-2", Content: old2},
		{Role: "assistant", ToolCalls: []llmapp.ToolCall{{ID: "call-3", Name: "attachment.read_text"}}},
		{Role: "tool", ToolCallID: "call-3", Content: third},
		{Role: "assistant", ToolCalls: []llmapp.ToolCall{{ID: "call-4", Name: "attachment.read_text"}}},
		{Role: "tool", ToolCallID: "call-4", Content: fourth},
	}
	store := newFakeContentStore()

	pruned, err := PruneToolResultMessages(context.Background(), messages, store, PruneKeepRecentToolPairs)
	if err != nil {
		t.Fatalf("PruneToolResultMessages: %v", err)
	}
	if len(pruned) != len(messages) {
		t.Fatalf("pruner broke pairing: %d messages, want %d", len(pruned), len(messages))
	}
	for i := range messages {
		if pruned[i].Role != messages[i].Role || pruned[i].ToolCallID != messages[i].ToolCallID {
			t.Fatalf("pruner changed message %d pairing: %+v -> %+v", i, messages[i], pruned[i])
		}
	}
	if pruned[1].ToolCalls[0].ID != "call-1" || pruned[7].ToolCalls[0].ID != "call-4" {
		t.Fatal("pruner touched assistant tool calls")
	}
	if messages[2].Content != old1 || messages[4].Content != old2 {
		t.Fatal("pruner mutated the caller's transcript")
	}
	if pruned[6].Content != third || pruned[8].Content != fourth {
		t.Fatalf("pruner touched the two newest tool pairs: %q / %q", pruned[6].Content, pruned[8].Content)
	}

	refs := make([]string, 0, 2)
	for _, index := range []int{2, 4} {
		var envelope struct {
			Pruned     bool   `json:"pruned"`
			Head       string `json:"head"`
			Tail       string `json:"tail"`
			ContentRef string `json:"content_ref"`
			Truncated  bool   `json:"truncated"`
		}
		if err := json.Unmarshal([]byte(pruned[index].Content), &envelope); err != nil {
			t.Fatalf("pruned old result %d is not a bounded envelope: %v\n%s", index, err, pruned[index].Content)
		}
		if !envelope.Pruned || !envelope.Truncated || envelope.ContentRef == "" || envelope.Head == "" {
			t.Fatalf("pruned envelope %d = %+v", index, envelope)
		}
		refs = append(refs, envelope.ContentRef)
	}
	if store.objects[refs[0]] != old1 || store.objects[refs[1]] != old2 {
		t.Fatal("pruned content is not retrievable in full by content_ref")
	}
	t.Logf("pruner: kept tool pairs tool_call_id=%s,%s; pruned refs=%s,%s (old1 %d chars, old2 %d chars)",
		pruned[6].ToolCallID, pruned[8].ToolCallID, refs[0], refs[1],
		utf8.RuneCountInString(old1), utf8.RuneCountInString(old2))

	registry := NewP7Registry(Deps{ToolResult: ToolResultDeps{Content: store}})
	result, err := execute(t, registry, Scope{VersionID: 10}, ToolToolResultRead,
		fmt.Sprintf(`{"content_ref":%q,"limit":20000}`, refs[0]))
	if err != nil {
		t.Fatalf("read pruned content: %v", err)
	}
	if body := resultMap(t, result); body["text"] != old1 {
		t.Fatal("tool_result.read did not read the pruned result back")
	}
}

func TestPrunerWithoutContentStoreFailsClosed(t *testing.T) {
	messages := []llmapp.ChatMessage{
		{Role: "assistant", ToolCalls: []llmapp.ToolCall{{ID: "call-1"}}},
		{Role: "tool", ToolCallID: "call-1", Content: strings.Repeat("a", 5000)},
		{Role: "assistant", ToolCalls: []llmapp.ToolCall{{ID: "call-2"}}},
		{Role: "tool", ToolCallID: "call-2", Content: "newest"},
		{Role: "assistant", ToolCalls: []llmapp.ToolCall{{ID: "call-3"}}},
		{Role: "tool", ToolCallID: "call-3", Content: "newest two"},
	}
	if _, err := PruneToolResultMessages(context.Background(), messages, nil, PruneKeepRecentToolPairs); err == nil {
		t.Fatal("pruner dropped content without a content store")
	}
}
