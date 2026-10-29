package eventing_test

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	codeinteleventing "github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/eventing"
	eventingdomain "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
)

// fakeEventPublisher captures the mapped eventing envelope.
type fakeEventPublisher struct {
	events     []eventingdomain.NewEvent
	publishErr error
}

// Publish records the event and returns the configured failure.
func (f *fakeEventPublisher) Publish(_ context.Context, event eventingdomain.NewEvent) (eventingdomain.Event, error) {
	f.events = append(f.events, event)
	if f.publishErr != nil {
		return eventingdomain.Event{}, f.publishErr
	}
	return eventingdomain.Event{Channel: event.Channel, Type: event.Type}, nil
}

// detailMap decodes one event detail for assertions.
func detailMap(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var detail map[string]any
	if err := json.Unmarshal(raw, &detail); err != nil {
		t.Fatalf("detail %s is not a JSON object: %v", raw, err)
	}
	return detail
}

// longSummaryPath is longer than the 500-rune summary cap, so a summary that
// embeds it must be truncated.
var longSummaryPath = strings.Repeat("汉", 600)

// TestImpactEventPayloadCaps pins the code.changed event contract: the fixed
// version channel, the documented payload keys, refs.code deduplicated/sorted
// and capped at 20, a summary of at most 500 runes, detail at most 64 KiB, and
// no code content or diff smuggled through the stats JSON.
func TestImpactEventPayloadCaps(t *testing.T) {
	ctx := context.Background()
	recorder := &fakeEventPublisher{}
	publisher := codeinteleventing.NewPublisher(recorder)

	paths := []string{longSummaryPath}
	for i := 1; i <= 21; i++ {
		paths = append(paths, "src/file-"+twoDigits(i)+".go")
	}
	paths = append(paths, "src/file-01.go") // duplicate: refs.code must dedupe
	stats := []byte(`{"changedFiles":22,"additions":10,"deletions":4,"truncated":true,` +
		`"content":"TOP_SECRET_CODE","diff":"+package main"}`)

	err := publisher.Changed(ctx, 42, codeintelapp.ChangedEvent{
		AnalysisID: 1001,
		BaseCommit: "base-commit",
		HeadCommit: "head-commit",
		Stats:      stats,
		Paths:      paths,
	})
	if err != nil {
		t.Fatalf("Changed: %v", err)
	}
	if len(recorder.events) != 1 {
		t.Fatalf("published %d events, want 1", len(recorder.events))
	}
	event := recorder.events[0]
	if event.Channel != "version:42" {
		t.Fatalf("channel=%q want version:42", event.Channel)
	}
	if event.VersionID == nil || *event.VersionID != "42" {
		t.Fatalf("version_id=%v want \"42\"", event.VersionID)
	}
	if event.Type != "code.changed" {
		t.Fatalf("type=%q want code.changed", event.Type)
	}

	// refs.code: deduplicated, sorted and capped at 20.
	if len(event.Refs.Code) != 20 {
		t.Fatalf("refs.code=%v want 20 entries", event.Refs.Code)
	}
	if !sort.StringsAreSorted(event.Refs.Code) {
		t.Fatalf("refs.code=%v want sorted", event.Refs.Code)
	}
	seen := make(map[string]struct{}, len(event.Refs.Code))
	for _, path := range event.Refs.Code {
		if _, ok := seen[path]; ok {
			t.Fatalf("refs.code contains duplicate %q", path)
		}
		seen[path] = struct{}{}
	}
	if event.Refs.Code[0] != "src/file-01.go" || event.Refs.Code[19] != "src/file-20.go" {
		t.Fatalf("refs.code=%v want the first 20 sorted paths", event.Refs.Code)
	}
	for _, path := range event.Refs.Code {
		if path == longSummaryPath {
			t.Fatalf("refs.code unexpectedly contains the over-cap path")
		}
	}

	// summary: valid UTF-8, at most 500 runes, and actually truncated.
	if !utf8.ValidString(event.Summary) {
		t.Fatalf("summary is not valid UTF-8: %q", event.Summary)
	}
	if runes := utf8.RuneCountInString(event.Summary); runes > 500 {
		t.Fatalf("summary has %d runes, want at most 500", runes)
	}
	if strings.Contains(event.Summary, longSummaryPath) {
		t.Fatal("summary was not truncated")
	}

	// detail: ids/counts only, never code content or diff text. analysisId is a
	// codeintel snowflake id, so D29/INV-9 require a decimal string rather than
	// a JSON number that a JavaScript consumer would round.
	if len(event.Detail) > 64<<10 {
		t.Fatalf("detail is %d bytes, want at most 64 KiB", len(event.Detail))
	}
	detail := detailMap(t, event.Detail)
	analysisID, ok := detail["analysisId"].(string)
	if !ok || analysisID != "1001" {
		t.Fatalf("detail.analysisId=%v (%T) want the decimal string %q", detail["analysisId"], detail["analysisId"], "1001")
	}
	if detail["baseCommit"] != "base-commit" || detail["headCommit"] != "head-commit" {
		t.Fatalf("detail=%v", detail)
	}
	statsDetail, ok := detail["stats"].(map[string]any)
	if !ok {
		t.Fatalf("detail.stats=%T want an object", detail["stats"])
	}
	if statsDetail["changedFiles"] != float64(22) || statsDetail["additions"] != float64(10) ||
		statsDetail["deletions"] != float64(4) || statsDetail["truncated"] != true {
		t.Fatalf("detail.stats=%v", statsDetail)
	}
	if _, ok := statsDetail["content"]; ok {
		t.Fatalf("detail.stats leaks content: %v", statsDetail)
	}
	if _, ok := statsDetail["diff"]; ok {
		t.Fatalf("detail.stats leaks diff: %v", statsDetail)
	}
	if raw := string(event.Detail); strings.Contains(raw, "TOP_SECRET_CODE") || strings.Contains(raw, "package main") {
		t.Fatalf("detail leaks code content: %s", raw)
	}
}

// TestImpactComputedEventPayloadCaps pins the code.impact_computed contract:
// only the four documented counters, deduplicated/sorted evidence paths, a
// bounded summary and detail, and no code content.
func TestImpactComputedEventPayloadCaps(t *testing.T) {
	ctx := context.Background()
	recorder := &fakeEventPublisher{}
	publisher := codeinteleventing.NewPublisher(recorder)

	paths := []string{longSummaryPath}
	for i := 1; i <= 21; i++ {
		paths = append(paths, "src/evidence-"+twoDigits(i)+".ts")
	}
	paths = append(paths, "src/evidence-05.ts") // duplicate: refs.code must dedupe
	counts := map[string]int{
		"requirements": 2,
		"appModels":    1,
		"unmapped":     3,
		"candidates":   6,
		"content":      99, // not a documented counter, must be dropped
		"prompt":       1,
	}

	err := publisher.ImpactComputed(ctx, 7, codeintelapp.ImpactEvent{
		AnalysisID:      2002,
		AnalysisVersion: 3,
		Status:          "done",
		Degraded:        true,
		Counts:          counts,
		Paths:           paths,
	})
	if err != nil {
		t.Fatalf("ImpactComputed: %v", err)
	}
	if len(recorder.events) != 1 {
		t.Fatalf("published %d events, want 1", len(recorder.events))
	}
	event := recorder.events[0]
	if event.Channel != "version:7" || event.VersionID == nil || *event.VersionID != "7" {
		t.Fatalf("channel/version_id=%q/%v", event.Channel, event.VersionID)
	}
	if event.Type != "code.impact_computed" {
		t.Fatalf("type=%q want code.impact_computed", event.Type)
	}

	if len(event.Refs.Code) != 20 {
		t.Fatalf("refs.code=%v want 20 entries", event.Refs.Code)
	}
	if !sort.StringsAreSorted(event.Refs.Code) {
		t.Fatalf("refs.code=%v want sorted", event.Refs.Code)
	}
	seen := make(map[string]struct{}, len(event.Refs.Code))
	for _, path := range event.Refs.Code {
		if _, ok := seen[path]; ok {
			t.Fatalf("refs.code contains duplicate %q", path)
		}
		seen[path] = struct{}{}
	}
	if event.Refs.Code[0] != "src/evidence-01.ts" || event.Refs.Code[19] != "src/evidence-20.ts" {
		t.Fatalf("refs.code=%v want the first 20 sorted paths", event.Refs.Code)
	}

	if runes := utf8.RuneCountInString(event.Summary); runes > 500 {
		t.Fatalf("summary has %d runes, want at most 500", runes)
	}
	if strings.Contains(event.Summary, longSummaryPath) {
		t.Fatal("summary was not truncated")
	}

	if len(event.Detail) > 64<<10 {
		t.Fatalf("detail is %d bytes, want at most 64 KiB", len(event.Detail))
	}
	detail := detailMap(t, event.Detail)
	analysisID, ok := detail["analysisId"].(string)
	if !ok || analysisID != "2002" {
		t.Fatalf("detail.analysisId=%v (%T) want the decimal string %q", detail["analysisId"], detail["analysisId"], "2002")
	}
	if detail["analysisVersion"] != float64(3) ||
		detail["status"] != "done" || detail["degraded"] != true {
		t.Fatalf("detail=%v", detail)
	}
	countsDetail, ok := detail["counts"].(map[string]any)
	if !ok {
		t.Fatalf("detail.counts=%T want an object", detail["counts"])
	}
	if len(countsDetail) != 4 {
		t.Fatalf("detail.counts=%v want exactly the four documented counters", countsDetail)
	}
	if countsDetail["requirements"] != float64(2) || countsDetail["appModels"] != float64(1) ||
		countsDetail["unmapped"] != float64(3) || countsDetail["candidates"] != float64(6) {
		t.Fatalf("detail.counts=%v", countsDetail)
	}
	if _, ok := countsDetail["content"]; ok {
		t.Fatalf("detail.counts leaks content: %v", countsDetail)
	}
}

// TestVersionChangedEventCarriesBatchSummaryOnly pins the P8 §11.2
// version_change code.changed payload: the fixed batch/commit/count/rewritten
// keys only, a decimal-string batch id (D29), no code content and no paths.
func TestVersionChangedEventCarriesBatchSummaryOnly(t *testing.T) {
	ctx := context.Background()
	recorder := &fakeEventPublisher{}
	publisher := codeinteleventing.NewPublisher(recorder)

	err := publisher.VersionChanged(ctx, 42, codeintelapp.VersionChangeEvent{
		BatchID:          11,
		BaseCommit:       "base-commit",
		HeadCommit:       "head-commit",
		ChangedFiles:     3,
		ImpactedCases:    2,
		HistoryRewritten: true,
	})
	if err != nil {
		t.Fatalf("VersionChanged: %v", err)
	}
	if len(recorder.events) != 1 {
		t.Fatalf("published %d events, want 1", len(recorder.events))
	}
	event := recorder.events[0]
	if event.Channel != "version:42" || event.VersionID == nil || *event.VersionID != "42" {
		t.Fatalf("channel/version_id = %q/%v", event.Channel, event.VersionID)
	}
	if event.Type != "code.changed" {
		t.Fatalf("type = %q, want code.changed", event.Type)
	}
	if len(event.Refs.Code) != 0 {
		t.Fatalf("refs.code = %v, want no code paths on the batch summary", event.Refs.Code)
	}
	if runes := utf8.RuneCountInString(event.Summary); runes == 0 || runes > 500 {
		t.Fatalf("summary = %q (%d runes), want 1..500", event.Summary, runes)
	}
	if len(event.Detail) > 64<<10 {
		t.Fatalf("detail is %d bytes, want at most 64 KiB", len(event.Detail))
	}
	detail := detailMap(t, event.Detail)
	if len(detail) != 6 {
		t.Fatalf("detail = %v, want exactly the six documented keys", detail)
	}
	if detail["batchId"] != "11" {
		t.Fatalf("detail.batchId = %v (%T), want the decimal string \"11\"", detail["batchId"], detail["batchId"])
	}
	if detail["baseCommit"] != "base-commit" || detail["headCommit"] != "head-commit" {
		t.Fatalf("detail commits = %v", detail)
	}
	if detail["changedFiles"] != float64(3) || detail["impactedCases"] != float64(2) ||
		detail["historyRewritten"] != true {
		t.Fatalf("detail counters = %v", detail)
	}
	for _, forbidden := range []string{"content", "diff", "items", "caseIds", "paths"} {
		if _, ok := detail[forbidden]; ok {
			t.Fatalf("detail leaks %q: %v", forbidden, detail)
		}
	}
}

// TestPublisherContract covers the remaining envelope rules: nil stats become
// an empty object, invalid stats fail before publishing, publish failures are
// returned and a nil publisher is a disabled no-op.
func TestPublisherContract(t *testing.T) {
	ctx := context.Background()

	t.Run("nil stats become an empty object", func(t *testing.T) {
		recorder := &fakeEventPublisher{}
		publisher := codeinteleventing.NewPublisher(recorder)
		if err := publisher.Changed(ctx, 5, codeintelapp.ChangedEvent{AnalysisID: 1}); err != nil {
			t.Fatalf("Changed: %v", err)
		}
		detail := detailMap(t, recorder.events[0].Detail)
		stats, ok := detail["stats"].(map[string]any)
		if !ok || len(stats) != 0 {
			t.Fatalf("detail.stats=%v want an empty object", detail["stats"])
		}
	})

	t.Run("invalid stats are rejected before publishing", func(t *testing.T) {
		recorder := &fakeEventPublisher{}
		publisher := codeinteleventing.NewPublisher(recorder)
		err := publisher.Changed(ctx, 5, codeintelapp.ChangedEvent{Stats: []byte("not json")})
		if err == nil {
			t.Fatal("Changed(invalid stats)=nil want an error")
		}
		if len(recorder.events) != 0 {
			t.Fatalf("published %d events on invalid stats", len(recorder.events))
		}
	})

	t.Run("an over-64KiB detail is rejected before publishing", func(t *testing.T) {
		recorder := &fakeEventPublisher{}
		publisher := codeinteleventing.NewPublisher(recorder)
		// The stats block is allowlisted to four counters, but the commit
		// fields are caller strings: an oversized one must trip the envelope
		// bound instead of reaching the event store.
		err := publisher.Changed(ctx, 5, codeintelapp.ChangedEvent{
			AnalysisID: 1,
			BaseCommit: strings.Repeat("a", 64<<10),
			Stats:      []byte(`{"changedFiles":1}`),
		})
		if err == nil {
			t.Fatal("Changed(oversized detail)=nil want the 64 KiB bound error")
		}
		if !strings.Contains(err.Error(), "detail") {
			t.Fatalf("error %v is not the detail size failure", err)
		}
		if len(recorder.events) != 0 {
			t.Fatalf("published %d events with an over-limit detail", len(recorder.events))
		}
	})

	t.Run("publish failures are returned", func(t *testing.T) {
		publisher := codeinteleventing.NewPublisher(&fakeEventPublisher{publishErr: errors.New("event store down")})
		if err := publisher.ImpactComputed(ctx, 5, codeintelapp.ImpactEvent{AnalysisID: 1}); err == nil {
			t.Fatal("ImpactComputed error=nil want the event store failure")
		}
	})

	t.Run("a nil publisher is a disabled no-op", func(t *testing.T) {
		publisher := codeinteleventing.NewPublisher(nil)
		if err := publisher.Changed(ctx, 5, codeintelapp.ChangedEvent{AnalysisID: 1}); err != nil {
			t.Fatalf("Changed(nil publisher)=%v want nil", err)
		}
		if err := publisher.ImpactComputed(ctx, 5, codeintelapp.ImpactEvent{AnalysisID: 1}); err != nil {
			t.Fatalf("ImpactComputed(nil publisher)=%v want nil", err)
		}
		if err := publisher.VersionChanged(ctx, 5, codeintelapp.VersionChangeEvent{BatchID: 1}); err != nil {
			t.Fatalf("VersionChanged(nil publisher)=%v want nil", err)
		}
	})
}

// twoDigits formats 1..99 as a zero-padded two-digit string for deterministic
// path ordering.
func twoDigits(value int) string {
	if value < 10 {
		return "0" + string(rune('0'+value))
	}
	return string(rune('0'+value/10)) + string(rune('0'+value%10))
}
