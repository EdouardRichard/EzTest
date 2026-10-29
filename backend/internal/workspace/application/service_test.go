package application_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	workspace "github.com/EdouardRichard/EzTest/backend/internal/workspace/application"
)

// fakeReviewProvider is the version-side todo provider seam (spec §6.3).
type fakeReviewProvider struct {
	rows  []workspace.ReviewRow
	err   error
	calls int
}

func (f *fakeReviewProvider) ReviewingVersions(context.Context) ([]workspace.ReviewRow, error) {
	f.calls++
	return f.rows, f.err
}

// fakeExecutionProvider is the execution-side todo provider seam. It records
// whether a non-admin request ever reached it: the service must filter the
// takeover/gate groups before querying (spec §6.3).
type fakeExecutionProvider struct {
	rows  []workspace.PendingRow
	err   error
	calls int
}

func (f *fakeExecutionProvider) PendingTakeovers(context.Context) ([]workspace.PendingRow, error) {
	f.calls++
	return f.rows, f.err
}

func int64Pointer(value int64) *int64 { return &value }

// reviewRows is a two-version review fixture with one duplicate row: the service
// returns one item per version even when a provider reports it twice.
func reviewRows(base time.Time) []workspace.ReviewRow {
	return []workspace.ReviewRow{
		{VersionID: 11, VersionName: "版本甲", Status: "reviewing", ReviewingAt: base.Add(time.Hour)},
		{VersionID: 12, VersionName: "版本乙", Status: "reviewing", ReviewingAt: base},
		{VersionID: 11, VersionName: "版本甲", Status: "reviewing", ReviewingAt: base.Add(time.Hour)},
	}
}

// TestListAggregatesReviewTakeoverAndGate proves the §6.3 aggregation for an
// administrator: review rows are deduped per version, waiting takeovers become
// `takeover` items, blocked runs without a waiting takeover become `gate` items,
// and a case run is emitted at most once (takeover/gate dedupe).
func TestListAggregatesReviewTakeoverAndGate(t *testing.T) {
	base := time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)
	takeoverID := int64(9001)
	review := &fakeReviewProvider{rows: reviewRows(base)}
	execution := &fakeExecutionProvider{rows: []workspace.PendingRow{
		{
			CaseRunID: 21, VersionID: 12, VersionName: "版本乙", CaseTitle: "登录流程",
			FailureReason: "mfa", CreatedAt: base.Add(2 * time.Hour), TakeoverID: &takeoverID,
		},
		{
			CaseRunID: 22, VersionID: 12, VersionName: "版本乙", CaseTitle: "下单流程",
			FailureReason: "no session", CreatedAt: base.Add(3 * time.Hour),
		},
		// The same case run reported twice must not duplicate the item.
		{
			CaseRunID: 21, VersionID: 12, VersionName: "版本乙", CaseTitle: "登录流程",
			FailureReason: "mfa", CreatedAt: base.Add(2 * time.Hour), TakeoverID: &takeoverID,
		},
	}}

	service := workspace.NewService(workspace.Providers{Review: review, Execution: execution})
	snapshot, err := service.List(context.Background(), workspace.Query{Admin: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if snapshot.Counts.Review != 2 || snapshot.Counts.Takeover != 1 || snapshot.Counts.Gate != 1 {
		t.Fatalf("counts = %+v, want review=2 takeover=1 gate=1", snapshot.Counts)
	}
	if len(snapshot.Items) != 4 {
		t.Fatalf("items = %d, want 4: %+v", len(snapshot.Items), snapshot.Items)
	}

	byKind := map[string][]workspace.Item{}
	for _, item := range snapshot.Items {
		byKind[item.Kind] = append(byKind[item.Kind], item)
	}
	if len(byKind[workspace.KindReview]) != 2 {
		t.Fatalf("review items = %+v, want 2", byKind[workspace.KindReview])
	}
	if len(byKind[workspace.KindTakeover]) != 1 || len(byKind[workspace.KindGate]) != 1 {
		t.Fatalf("takeover/gate items = %+v/%+v, want 1/1",
			byKind[workspace.KindTakeover], byKind[workspace.KindGate])
	}

	reviewByVersion := map[string]workspace.Item{}
	for _, item := range byKind[workspace.KindReview] {
		reviewByVersion[item.VersionID] = item
	}
	first := reviewByVersion["11"]
	if first.Title != "版本甲" || first.VersionName != "版本甲" || first.Severity != workspace.SeverityInfo {
		t.Fatalf("review item 11 = %+v", first)
	}
	if first.URL != "/versions/11?tab=mindmap" {
		t.Fatalf("review item 11 url = %q, want the mindmap deep link", first.URL)
	}
	if !first.CreatedAt.Equal(base.Add(time.Hour)) {
		t.Fatalf("review item 11 createdAt = %v, want %v", first.CreatedAt, base.Add(time.Hour))
	}

	takeover := byKind[workspace.KindTakeover][0]
	if takeover.Title != "登录流程" || takeover.VersionID != "12" || takeover.VersionName != "版本乙" {
		t.Fatalf("takeover item = %+v", takeover)
	}
	if takeover.Severity != workspace.SeverityWarning {
		t.Fatalf("takeover severity = %q, want %q", takeover.Severity, workspace.SeverityWarning)
	}
	if takeover.URL != "/versions/12?tab=sessions" {
		t.Fatalf("takeover url = %q, want the execution-tab deep link", takeover.URL)
	}

	gate := byKind[workspace.KindGate][0]
	if gate.Title != "下单流程" || gate.VersionID != "12" {
		t.Fatalf("gate item = %+v", gate)
	}
	if gate.Severity != workspace.SeverityMajor {
		t.Fatalf("gate severity = %q, want %q", gate.Severity, workspace.SeverityMajor)
	}
	if gate.URL != "/versions/12?tab=sessions" {
		t.Fatalf("gate url = %q, want the execution-tab deep link", gate.URL)
	}
}

// TestListNormalUserSeesOnlyReview proves the permission filter of §6.3: a
// regular user never reaches the execution provider and gets zero takeover/gate
// counts, even when the fake provider could return rows.
func TestListNormalUserSeesOnlyReview(t *testing.T) {
	base := time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)
	takeoverID := int64(9001)
	review := &fakeReviewProvider{rows: reviewRows(base)}
	execution := &fakeExecutionProvider{rows: []workspace.PendingRow{{
		CaseRunID: 21, VersionID: 12, VersionName: "版本乙", CaseTitle: "登录流程",
		CreatedAt: base, TakeoverID: &takeoverID,
	}}}

	snapshot, err := workspace.NewService(workspace.Providers{
		Review: review, Execution: execution,
	}).List(context.Background(), workspace.Query{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if execution.calls != 0 {
		t.Fatalf("execution provider calls = %d, want 0 for a regular user", execution.calls)
	}
	if snapshot.Counts.Takeover != 0 || snapshot.Counts.Gate != 0 {
		t.Fatalf("counts = %+v, want takeover/gate zero", snapshot.Counts)
	}
	if snapshot.Counts.Review != 2 || len(snapshot.Items) != 2 {
		t.Fatalf("snapshot = %+v, want the two review items only", snapshot)
	}
	for _, item := range snapshot.Items {
		if item.Kind != workspace.KindReview {
			t.Fatalf("regular user item = %+v, want kind review", item)
		}
	}
}

// TestListDefaultsAndTruncatesLimit covers `limit`: zero selects the documented
// default of 50, explicit values truncate the item list but never the counts.
func TestListDefaultsAndTruncatesLimit(t *testing.T) {
	base := time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)
	rows := make([]workspace.ReviewRow, 0, 55)
	for index := 0; index < 55; index++ {
		rows = append(rows, workspace.ReviewRow{
			VersionID:   int64(100 + index),
			VersionName: fmt.Sprintf("版本 %02d", index),
			Status:      "reviewing",
			ReviewingAt: base.Add(time.Duration(index) * time.Minute),
		})
	}
	service := workspace.NewService(workspace.Providers{Review: &fakeReviewProvider{rows: rows}})

	defaulted, err := service.List(context.Background(), workspace.Query{})
	if err != nil {
		t.Fatalf("List(default): %v", err)
	}
	if len(defaulted.Items) != 50 {
		t.Fatalf("default items = %d, want 50", len(defaulted.Items))
	}
	if defaulted.Counts.Review != 55 {
		t.Fatalf("default counts.review = %d, want the pre-truncation 55", defaulted.Counts.Review)
	}

	limited, err := service.List(context.Background(), workspace.Query{Limit: 3})
	if err != nil {
		t.Fatalf("List(limit=3): %v", err)
	}
	if len(limited.Items) != 3 {
		t.Fatalf("limited items = %d, want 3", len(limited.Items))
	}
	if limited.Counts.Review != 55 {
		t.Fatalf("limited counts.review = %d, want 55", limited.Counts.Review)
	}
}

// TestListProviderErrors proves a provider failure aborts the aggregation (the
// HTTP layer renders the shared 500 envelope) instead of returning partial data.
func TestListProviderErrors(t *testing.T) {
	service := workspace.NewService(workspace.Providers{
		Review: &fakeReviewProvider{err: errors.New("version db unavailable")},
	})
	_, err := service.List(context.Background(), workspace.Query{})
	if err == nil || !strings.Contains(err.Error(), "load reviews") {
		t.Fatalf("review error = %v, want a wrapped load reviews error", err)
	}

	service = workspace.NewService(workspace.Providers{
		Review:    &fakeReviewProvider{},
		Execution: &fakeExecutionProvider{err: errors.New("execution db unavailable")},
	})
	_, err = service.List(context.Background(), workspace.Query{Admin: true})
	if err == nil || !strings.Contains(err.Error(), "load takeovers") {
		t.Fatalf("execution error = %v, want a wrapped load takeovers error", err)
	}
}

// TestListAdminWithoutExecutionProvider proves the execution port is optional:
// a process that wires only the version provider still serves the review group.
func TestListAdminWithoutExecutionProvider(t *testing.T) {
	base := time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)
	snapshot, err := workspace.NewService(workspace.Providers{
		Review: &fakeReviewProvider{rows: reviewRows(base)},
	}).List(context.Background(), workspace.Query{Admin: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if snapshot.Counts.Review != 2 || len(snapshot.Items) != 2 {
		t.Fatalf("snapshot = %+v, want the two review items", snapshot)
	}
}

// TestNewServiceRequiresReviewProvider proves a process without the review port
// fails loudly instead of silently serving an empty todo centre.
func TestNewServiceRequiresReviewProvider(t *testing.T) {
	_, err := workspace.NewService(workspace.Providers{}).List(context.Background(), workspace.Query{})
	if err == nil || !strings.Contains(err.Error(), "review provider is not configured") {
		t.Fatalf("error = %v, want the missing review provider error", err)
	}
}
