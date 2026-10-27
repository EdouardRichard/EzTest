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

	// detail: ids/counts only, never code content or diff text.
	if len(event.Detail) > 64<<10 {
		t.Fatalf("detail is %d bytes, want at most 64 KiB", len(event.Detail))
	}
	detail := detailMap(t, event.Detail)
	if detail["analysisId"] != float64(1001) || detail["baseCommit"] != "base-commit" || detail["headCommit"] != "head-commit" {
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
	if detail["analysisId"] != float64(2002) || detail["analysisVersion"] != float64(3) ||
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
