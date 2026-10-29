package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// fakeKnowledgeRepo is an in-memory domain.KnowledgeRepository with the
// semantics the service relies on: draft-only content edits and the revision
// compare-and-swap that the PostgreSQL repository implements.
type fakeKnowledgeRepo struct {
	entries      map[int64]domain.KnowledgeEntry
	versionNames map[int64]string

	writes      int
	createErr   error
	listErr     error
	approvedErr error
}

// newFakeKnowledgeRepo returns an empty repository.
func newFakeKnowledgeRepo() *fakeKnowledgeRepo {
	return &fakeKnowledgeRepo{
		entries:      map[int64]domain.KnowledgeEntry{},
		versionNames: map[int64]string{},
	}
}

// seed stores one entry with server timestamps.
func (r *fakeKnowledgeRepo) seed(entry domain.KnowledgeEntry) domain.KnowledgeEntry {
	if entry.Status == "" {
		entry.Status = domain.KnowledgeDraft
	}
	if entry.Confidence == "" {
		entry.Confidence = domain.KnowledgeConfidenceMedium
	}
	if entry.Revision == 0 {
		entry.Revision = 1
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = testNow
	}
	if entry.UpdatedAt.IsZero() {
		entry.UpdatedAt = entry.CreatedAt
	}
	if entry.Tags == nil {
		entry.Tags = []string{}
	}
	if entry.SourceRefs == nil {
		entry.SourceRefs = []domain.KnowledgeSourceRef{}
	}
	r.entries[entry.ID] = entry
	return entry
}

// Create inserts the entry and writes the stored row back.
func (r *fakeKnowledgeRepo) Create(_ context.Context, entry *domain.KnowledgeEntry) error {
	if r.createErr != nil {
		return r.createErr
	}
	if _, exists := r.entries[entry.ID]; exists {
		return domain.NewConflict("duplicate_key", "knowledge entry already exists")
	}
	entry.CreatedAt = testNow
	entry.UpdatedAt = testNow
	r.entries[entry.ID] = *entry
	r.writes++
	return nil
}

// Get returns one entry by id.
func (r *fakeKnowledgeRepo) Get(_ context.Context, id int64) (*domain.KnowledgeEntry, error) {
	stored, ok := r.entries[id]
	if !ok {
		return nil, domain.NewNotFound("knowledge", id)
	}
	copied := stored
	return &copied, nil
}

// List filters one version's entries and applies the keyset page.
func (r *fakeKnowledgeRepo) List(_ context.Context, filter domain.KnowledgeFilter) ([]domain.KnowledgeEntry, bool, error) {
	if r.listErr != nil {
		return nil, false, r.listErr
	}
	matches := make([]domain.KnowledgeEntry, 0, len(r.entries))
	for _, stored := range r.entries {
		if stored.VersionID != filter.VersionID {
			continue
		}
		if filter.Status != "" && stored.Status != filter.Status {
			continue
		}
		if filter.Kind != "" && stored.Kind != filter.Kind {
			continue
		}
		if name, ok := r.versionNames[stored.VersionID]; ok {
			stored.VersionName = name
		}
		matches = append(matches, stored)
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })
	return knowledgePage(matches, filter.Page), false, nil
}

// ListGlobal filters the whole table. It mirrors the global read's approved-only
// default: an empty status means approved.
func (r *fakeKnowledgeRepo) ListGlobal(_ context.Context, filter domain.GlobalKnowledgeFilter) ([]domain.KnowledgeEntry, bool, error) {
	status := filter.Status
	if status == "" {
		status = domain.KnowledgeApproved
	}
	matches := make([]domain.KnowledgeEntry, 0, len(r.entries))
	for _, stored := range r.entries {
		if stored.Status != status {
			continue
		}
		if filter.VersionID != nil && stored.VersionID != *filter.VersionID {
			continue
		}
		if filter.Kind != "" && stored.Kind != filter.Kind {
			continue
		}
		if name, ok := r.versionNames[stored.VersionID]; ok {
			stored.VersionName = name
		}
		matches = append(matches, stored)
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })
	return knowledgePage(matches, filter.Page), false, nil
}

// ListApproved returns a version's approved entries ordered by id ascending.
func (r *fakeKnowledgeRepo) ListApproved(_ context.Context, versionID int64, limit int) ([]domain.KnowledgeEntry, error) {
	if r.approvedErr != nil {
		return nil, r.approvedErr
	}
	matches := make([]domain.KnowledgeEntry, 0, len(r.entries))
	for _, stored := range r.entries {
		if stored.VersionID == versionID && stored.Status == domain.KnowledgeApproved {
			matches = append(matches, stored)
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })
	if limit > 0 && len(matches) > limit {
		matches = matches[:limit]
	}
	return matches, nil
}

// UpdateContent applies the revision compare-and-swap and bumps the revision.
func (r *fakeKnowledgeRepo) UpdateContent(_ context.Context, entry *domain.KnowledgeEntry, expected int) error {
	stored, ok := r.entries[entry.ID]
	if !ok {
		return domain.NewNotFound("knowledge", entry.ID)
	}
	if stored.Status != domain.KnowledgeDraft {
		return domain.NewStateConflict("knowledge_state_conflict", "knowledge entry is not a draft")
	}
	if stored.Revision != expected {
		return domain.NewStateConflict("knowledge_revision_conflict", "knowledge revision changed")
	}
	entry.Revision = stored.Revision + 1
	entry.CreatedAt = stored.CreatedAt
	entry.UpdatedAt = testNow.Add(1_000_000)
	r.entries[entry.ID] = *entry
	r.writes++
	return nil
}

// Review applies the revision compare-and-swap and stores the review fields.
func (r *fakeKnowledgeRepo) Review(_ context.Context, entry *domain.KnowledgeEntry, expected int) error {
	stored, ok := r.entries[entry.ID]
	if !ok {
		return domain.NewNotFound("knowledge", entry.ID)
	}
	if stored.Status != domain.KnowledgeDraft {
		return domain.NewStateConflict("knowledge_state_conflict", "knowledge entry is not a draft")
	}
	if stored.Revision != expected {
		return domain.NewStateConflict("knowledge_revision_conflict", "knowledge revision changed")
	}
	entry.Revision = stored.Revision + 1
	entry.CreatedAt = stored.CreatedAt
	entry.UpdatedAt = testNow.Add(1_000_000)
	r.entries[entry.ID] = *entry
	r.writes++
	return nil
}

// knowledgePage applies the keyset cursor and limit to sorted matches.
func knowledgePage(matches []domain.KnowledgeEntry, page domain.Page) []domain.KnowledgeEntry {
	cursorID, _ := platform.DecodeCursor(page.Cursor)
	if cursorID != 0 {
		trimmed := matches[:0]
		for _, stored := range matches {
			if stored.ID > cursorID {
				trimmed = append(trimmed, stored)
			}
		}
		matches = trimmed
	}
	limit := page.Limit
	if limit <= 0 {
		limit = 20
	}
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return matches
}

// knowledgeFixture wires the real knowledge service to in-memory fakes.
type knowledgeFixture struct {
	service  *application.KnowledgeService
	repo     *fakeKnowledgeRepo
	versions *fakeVersionRepo
	events   *fakeEvents
	ids      *fixedIDs
}

// newKnowledgeFixture builds the fixture with the default injection budget.
func newKnowledgeFixture() *knowledgeFixture {
	f := &knowledgeFixture{
		repo:     newFakeKnowledgeRepo(),
		versions: newFakeVersionRepo(),
		events:   &fakeEvents{},
		ids:      &fixedIDs{next: 9000},
	}
	f.service = application.NewKnowledgeService(
		f.repo, f.versions, f.events,
		application.WithKnowledgeIDGenerator(f.ids),
	)
	return f
}

// seedVersion stores a version the service can find.
func (f *knowledgeFixture) seedVersion(id int64, name string) domain.Version {
	version := f.versions.seed(domain.Version{
		ID: id, Name: name, Status: domain.VersionDesigning, Revision: 1, CreatedBy: 1, UpdatedBy: 1,
	})
	f.repo.versionNames[id] = name
	return version
}

// seedEntry stores one entry scoped to versionID.
func (f *knowledgeFixture) seedEntry(versionID int64, entry domain.KnowledgeEntry) domain.KnowledgeEntry {
	entry.VersionID = versionID
	if entry.Kind == "" {
		entry.Kind = domain.KnowledgeKindPattern
	}
	if entry.Title == "" {
		entry.Title = "Checkout retries"
	}
	if entry.Body == "" {
		entry.Body = "Retry the payment call three times before failing the case."
	}
	return f.repo.seed(entry)
}

// TestKnowledgeCreateIsDraftAndPublishesIDsOnly covers create: any logged-in
// user creates a draft, the write publishes knowledge.created, and the event
// detail carries ids/kind/status only - never the knowledge body or title
// (spec §9.4/§11.2).
func TestKnowledgeCreateIsDraftAndPublishesIDsOnly(t *testing.T) {
	ctx := context.Background()
	f := newKnowledgeFixture()
	f.seedVersion(100, "Order v2")
	f.events.events = nil

	entry, err := f.service.Create(ctx, 100, application.CreateKnowledgeInput{
		Kind:       domain.KnowledgeKindPitfall,
		Title:      "Payment timeout",
		Body:       "The sandbox gateway times out after 30s; use the fake clock.",
		Confidence: domain.KnowledgeConfidenceHigh,
		Tags:       []string{"payment", "flaky"},
		SourceRefs: []domain.KnowledgeSourceRef{{Kind: domain.KnowledgeRefRequirement, ID: 7}},
	}, 42)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if entry.ID != 9000 || entry.VersionID != 100 || entry.Status != domain.KnowledgeDraft ||
		entry.Revision != 1 || entry.CreatedBy != 42 {
		t.Fatalf("created entry = %+v", entry)
	}
	if entry.ReviewedBy != nil || entry.ReviewNote != "" {
		t.Fatalf("draft review fields = %+v", entry)
	}
	if len(f.events.events) != 1 {
		t.Fatalf("events = %v, want [knowledge.created]", eventTypesOf(f.events.events))
	}
	event := f.events.events[0]
	if event.Type != "knowledge.created" || event.VersionID != 100 || event.ActorID != 42 {
		t.Fatalf("knowledge.created event = %+v", event)
	}
	if event.Detail["knowledgeId"] != "9000" || event.Detail["kind"] != domain.KnowledgeKindPitfall ||
		event.Detail["status"] != domain.KnowledgeDraft {
		t.Fatalf("knowledge.created detail = %+v", event.Detail)
	}
	encoded, err := json.Marshal(event.Detail)
	if err != nil {
		t.Fatalf("marshal detail: %v", err)
	}
	if strings.Contains(string(encoded), "sandbox gateway") || strings.Contains(string(encoded), "Payment timeout") {
		t.Fatalf("event detail leaked knowledge content: %s", encoded)
	}

	// A missing version is a 404 and writes nothing.
	if _, err := f.service.Create(ctx, 404, application.CreateKnowledgeInput{
		Kind: domain.KnowledgeKindPattern, Title: "x", Body: "y",
	}, 42); err == nil {
		t.Fatal("Create on a missing version must fail")
	}
}

// TestKnowledgeValidation covers the frozen enums and body/tags/source bounds.
func TestKnowledgeValidation(t *testing.T) {
	ctx := context.Background()
	f := newKnowledgeFixture()
	f.seedVersion(100, "Order v2")

	tooLongBody := strings.Repeat("x", domain.MaxKnowledgeBodyBytes+1)
	cases := []struct {
		name  string
		input application.CreateKnowledgeInput
		want  string
	}{
		{
			name:  "invalid kind",
			input: application.CreateKnowledgeInput{Kind: "story", Title: "t", Body: "b"},
			want:  "invalid_knowledge_kind",
		},
		{
			name:  "missing title",
			input: application.CreateKnowledgeInput{Kind: domain.KnowledgeKindPattern, Body: "b"},
			want:  "invalid_knowledge_title",
		},
		{
			name:  "missing body",
			input: application.CreateKnowledgeInput{Kind: domain.KnowledgeKindPattern, Title: "t"},
			want:  "invalid_knowledge_body",
		},
		{
			name: "body over 16 KiB",
			input: application.CreateKnowledgeInput{
				Kind: domain.KnowledgeKindPattern, Title: "t", Body: tooLongBody,
			},
			want: "invalid_knowledge_body",
		},
		{
			name: "too many tags",
			input: application.CreateKnowledgeInput{
				Kind: domain.KnowledgeKindPattern, Title: "t", Body: "b",
				Tags: make([]string, domain.MaxKnowledgeTags+1),
			},
			want: "invalid_knowledge_tags",
		},
		{
			name: "unknown source ref kind",
			input: application.CreateKnowledgeInput{
				Kind: domain.KnowledgeKindPattern, Title: "t", Body: "b",
				SourceRefs: []domain.KnowledgeSourceRef{{Kind: "user", ID: 1}},
			},
			want: "invalid_knowledge_source_refs",
		},
		{
			name: "invalid confidence",
			input: application.CreateKnowledgeInput{
				Kind: domain.KnowledgeKindPattern, Title: "t", Body: "b", Confidence: "certain",
			},
			want: "invalid_knowledge_confidence",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.service.Create(ctx, 100, tc.input, 42)
			requireKnowledgeReason(t, err, tc.want)
			if f.repo.writes != 0 {
				t.Fatalf("invalid create wrote %d entries", f.repo.writes)
			}
			if len(f.events.events) != 0 {
				t.Fatalf("invalid create published events: %v", eventTypesOf(f.events.events))
			}
		})
	}
}

// TestKnowledgeUpdateRevisionCAS covers the draft-only creator/admin edit and
// the revision compare-and-swap (spec §9.4).
func TestKnowledgeUpdateRevisionCAS(t *testing.T) {
	ctx := context.Background()
	f := newKnowledgeFixture()
	f.seedVersion(100, "Order v2")
	entry := f.seedEntry(100, domain.KnowledgeEntry{ID: 10, Status: domain.KnowledgeDraft, CreatedBy: 7})

	title, body := "Retry policy", "Retry twice then fail."
	updated, err := f.service.Update(ctx, 100, entry.ID, application.UpdateKnowledgeInput{
		Revision: 1, Title: &title, Body: &body,
	}, 7, false)
	if err != nil {
		t.Fatalf("creator update: %v", err)
	}
	if updated.Title != title || updated.Body != body || updated.Revision != 2 {
		t.Fatalf("updated entry = %+v", updated)
	}

	// A stale revision is a knowledge_revision_conflict and writes nothing.
	stale := "stale"
	_, err = f.service.Update(ctx, 100, entry.ID, application.UpdateKnowledgeInput{
		Revision: 1, Title: &stale,
	}, 7, false)
	requireKnowledgeReason(t, err, "knowledge_revision_conflict")
	if stored, _ := f.repo.Get(ctx, entry.ID); stored.Title != title {
		t.Fatalf("stale update changed the entry: %+v", stored)
	}

	// A different regular user may not edit another author's draft.
	other := "hijacked"
	_, err = f.service.Update(ctx, 100, entry.ID, application.UpdateKnowledgeInput{
		Revision: 2, Title: &other,
	}, 8, false)
	assertDomainReason(t, err, domain.KindForbidden, "knowledge_forbidden")

	// An administrator may edit any draft.
	adminTitle := "Admin retry policy"
	if _, err := f.service.Update(ctx, 100, entry.ID, application.UpdateKnowledgeInput{
		Revision: 2, Title: &adminTitle,
	}, 9, true); err != nil {
		t.Fatalf("admin update: %v", err)
	}

	// An approved entry is no longer editable.
	approved := f.seedEntry(100, domain.KnowledgeEntry{
		ID: 11, Status: domain.KnowledgeApproved, CreatedBy: 7, Revision: 3,
	})
	_, err = f.service.Update(ctx, 100, approved.ID, application.UpdateKnowledgeInput{
		Revision: 3, Title: &title,
	}, 7, false)
	requireKnowledgeReason(t, err, "knowledge_state_conflict")

	// A cross-version entry id is a 404.
	_, err = f.service.Update(ctx, 200, entry.ID, application.UpdateKnowledgeInput{
		Revision: 3, Title: &title,
	}, 7, true)
	assertDomainReason(t, err, domain.KindNotFound, "not_found")
}

// TestKnowledgeReviewTransitions covers draft -> approved/rejected, the required
// rejection reason, the revision CAS and the ids-only knowledge.reviewed event.
func TestKnowledgeReviewTransitions(t *testing.T) {
	ctx := context.Background()
	f := newKnowledgeFixture()
	f.seedVersion(100, "Order v2")
	approved := f.seedEntry(100, domain.KnowledgeEntry{ID: 20, CreatedBy: 7})
	rejected := f.seedEntry(100, domain.KnowledgeEntry{ID: 21, CreatedBy: 7})
	f.events.events = nil

	ok, err := f.service.Review(ctx, 100, approved.ID, application.ReviewKnowledgeInput{
		Revision: 1, Approve: true, Reason: "matches the sandbox behaviour",
	}, 42)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if ok.Status != domain.KnowledgeApproved || ok.ReviewedBy == nil || *ok.ReviewedBy != 42 ||
		ok.Revision != 2 || ok.ReviewNote != "matches the sandbox behaviour" {
		t.Fatalf("approved entry = %+v", ok)
	}

	// Rejecting requires a non-empty reason (422 invalid_review_note).
	_, err = f.service.Review(ctx, 100, rejected.ID, application.ReviewKnowledgeInput{
		Revision: 1, Approve: false, Reason: "  ",
	}, 42)
	requireKnowledgeReason(t, err, "invalid_review_note")
	if stored, _ := f.repo.Get(ctx, rejected.ID); stored.Status != domain.KnowledgeDraft {
		t.Fatalf("rejected-without-reason entry changed: %+v", stored)
	}

	rejectedEntry, err := f.service.Review(ctx, 100, rejected.ID, application.ReviewKnowledgeInput{
		Revision: 1, Approve: false, Reason: "duplicates decision D12",
	}, 42)
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if rejectedEntry.Status != domain.KnowledgeRejected || rejectedEntry.ReviewNote != "duplicates decision D12" {
		t.Fatalf("rejected entry = %+v", rejectedEntry)
	}

	// The review is terminal: a second review is a state conflict.
	_, err = f.service.Review(ctx, 100, approved.ID, application.ReviewKnowledgeInput{
		Revision: 2, Approve: false, Reason: "late",
	}, 43)
	requireKnowledgeReason(t, err, "knowledge_state_conflict")

	// A stale revision keeps the entry untouched.
	pending := f.seedEntry(100, domain.KnowledgeEntry{ID: 22, CreatedBy: 7})
	_, err = f.service.Review(ctx, 100, pending.ID, application.ReviewKnowledgeInput{
		Revision: 9, Approve: true,
	}, 42)
	requireKnowledgeReason(t, err, "knowledge_revision_conflict")

	if len(f.events.events) != 2 {
		t.Fatalf("events = %v, want created+reviewed", eventTypesOf(f.events.events))
	}
	for _, event := range f.events.events {
		if event.Type != "knowledge.reviewed" {
			continue
		}
		if event.Detail["knowledgeId"] == nil || event.Detail["status"] == nil ||
			event.Detail["reviewedBy"] != int64(42) {
			t.Fatalf("knowledge.reviewed detail = %+v", event.Detail)
		}
		encoded, _ := json.Marshal(event.Detail)
		if strings.Contains(string(encoded), "sandbox") || strings.Contains(string(encoded), "duplicates") {
			t.Fatalf("review event leaked content: %s", encoded)
		}
	}
}

// TestKnowledgeListScoping covers the version list filters and the global
// approved-only read (spec §9.4/§11.1).
func TestKnowledgeListScoping(t *testing.T) {
	ctx := context.Background()
	f := newKnowledgeFixture()
	f.seedVersion(100, "Order v2")
	f.seedVersion(200, "Other")
	first := f.seedEntry(100, domain.KnowledgeEntry{ID: 30, Kind: domain.KnowledgeKindPattern})
	second := f.seedEntry(100, domain.KnowledgeEntry{
		ID: 31, Kind: domain.KnowledgeKindPitfall, Status: domain.KnowledgeApproved,
	})
	f.seedEntry(200, domain.KnowledgeEntry{ID: 32, Status: domain.KnowledgeApproved})

	all, more, err := f.service.List(ctx, 100, "", "", domain.Page{Limit: 10})
	if err != nil || more || len(all) != 2 || all[0].ID != first.ID || all[1].ID != second.ID {
		t.Fatalf("version list = %+v more=%v err=%v", all, more, err)
	}
	if all[1].VersionName != "Order v2" {
		t.Fatalf("version name = %q", all[1].VersionName)
	}
	drafts, _, err := f.service.List(ctx, 100, domain.KnowledgeDraft, "", domain.Page{Limit: 10})
	if err != nil || len(drafts) != 1 || drafts[0].ID != first.ID {
		t.Fatalf("draft list = %+v err=%v", drafts, err)
	}
	pitfalls, _, err := f.service.List(ctx, 100, "", domain.KnowledgeKindPitfall, domain.Page{Limit: 10})
	if err != nil || len(pitfalls) != 1 || pitfalls[0].ID != second.ID {
		t.Fatalf("kind filter = %+v err=%v", pitfalls, err)
	}

	global, _, err := f.service.ListGlobal(ctx, nil, "", "", domain.Page{Limit: 10})
	if err != nil || len(global) != 2 {
		t.Fatalf("global approved list = %+v err=%v", global, err)
	}
	for _, entry := range global {
		if entry.Status != domain.KnowledgeApproved {
			t.Fatalf("global list returned %q", entry.Status)
		}
	}
	versionID := int64(100)
	filtered, _, err := f.service.ListGlobal(ctx, &versionID, "", "", domain.Page{Limit: 10})
	if err != nil || len(filtered) != 1 || filtered[0].ID != second.ID {
		t.Fatalf("global version filter = %+v err=%v", filtered, err)
	}
	queue, _, err := f.service.ListGlobal(ctx, nil, domain.KnowledgeDraft, "", domain.Page{Limit: 10})
	if err != nil || len(queue) != 1 || queue[0].ID != first.ID {
		t.Fatalf("global draft queue = %+v err=%v", queue, err)
	}
}

// TestApprovedKnowledgeSectionBudget covers the injection read: only approved
// entries, at most MaxInject, at most MaxBytes rendered bytes, truncated when
// the budget cuts the list, and the knowledgeRefs list of included entries.
func TestApprovedKnowledgeSectionBudget(t *testing.T) {
	ctx := context.Background()
	f := newKnowledgeFixture()
	f.seedVersion(100, "Order v2")
	// Six approved entries; the two non-approved ones must never be injected.
	for id := int64(40); id < 46; id++ {
		f.repo.seed(domain.KnowledgeEntry{
			ID: id, VersionID: 100, Kind: domain.KnowledgeKindPattern,
			Title: "approved", Body: strings.Repeat("a", 700), Status: domain.KnowledgeApproved,
		})
	}
	f.seedEntry(100, domain.KnowledgeEntry{ID: 50, Status: domain.KnowledgeDraft})
	f.seedEntry(100, domain.KnowledgeEntry{ID: 51, Status: domain.KnowledgeRejected})
	f.repo.approvedErr = nil

	section, err := f.service.ApprovedKnowledgeSection(ctx, 100)
	if err != nil {
		t.Fatalf("ApprovedKnowledgeSection: %v", err)
	}
	if !strings.HasPrefix(section.Text, "## 版本知识") || !strings.Contains(section.Text, "knowledgeRefs: 40,41") {
		t.Fatalf("section text = %q", section.Text)
	}
	if len(section.Text) > domain.DefaultKnowledgeMaxBytes {
		t.Fatalf("section bytes = %d, want <= %d", len(section.Text), domain.DefaultKnowledgeMaxBytes)
	}
	if len(section.Refs) > domain.DefaultKnowledgeMaxInject {
		t.Fatalf("refs = %v, want at most %d", section.Refs, domain.DefaultKnowledgeMaxInject)
	}
	if !section.Truncated {
		t.Fatal("six approved entries must be truncated at the five-entry/4 KiB budget")
	}
	if strings.Contains(section.Text, "draft") || strings.Contains(section.Text, "rejected") {
		t.Fatalf("section leaked non-approved status: %q", section.Text)
	}

	// A zero-entry version injects nothing.
	empty, err := f.service.ApprovedKnowledgeSection(ctx, 999)
	if err != nil || empty.Text != "" || len(empty.Refs) != 0 || empty.Truncated {
		t.Fatalf("empty section = %+v err=%v", empty, err)
	}

	// A tiny budget still never exceeds the byte cap and never splits a rune.
	f.service = application.NewKnowledgeService(f.repo, f.versions, f.events,
		application.WithKnowledgeIDGenerator(f.ids),
		application.WithKnowledgeLimits(application.KnowledgeLimits{MaxInject: 5, MaxBytes: 300}))
	tiny, err := f.service.ApprovedKnowledgeSection(ctx, 100)
	if err != nil {
		t.Fatalf("tiny budget: %v", err)
	}
	if len(tiny.Text) > 300 {
		t.Fatalf("tiny section bytes = %d, want <= 300", len(tiny.Text))
	}
	if tiny.Text != "" && !strings.HasPrefix(tiny.Text, "## 版本知识") {
		t.Fatalf("tiny section prefix = %q", tiny.Text)
	}

	// A configured zero injects nothing but still reports the truncation.
	f.service = application.NewKnowledgeService(f.repo, f.versions, f.events,
		application.WithKnowledgeIDGenerator(f.ids),
		application.WithKnowledgeLimits(application.KnowledgeLimits{MaxInject: 0, MaxBytes: 4096}))
	off, err := f.service.ApprovedKnowledgeSection(ctx, 100)
	if err != nil || off.Text != "" || !off.Truncated {
		t.Fatalf("maxInject=0 section = %+v err=%v", off, err)
	}

	// A repository failure is returned to the caller (the run starters log it
	// and continue without injection).
	f.repo.approvedErr = errors.New("database down")
	if _, err := f.service.ApprovedKnowledgeSection(ctx, 100); err == nil {
		t.Fatal("repository failure must be returned")
	}
}

// TestKnowledgeLimitsFromEnv covers the EZTEST_KNOWLEDGE_MAX_INJECT/MAX_BYTES
// contract (defaults 5/4096, ranges 0-20 and 0-16384, spec §11.4).
func TestKnowledgeLimitsFromEnv(t *testing.T) {
	defaults := application.DefaultKnowledgeLimits()
	if defaults.MaxInject != 5 || defaults.MaxBytes != 4096 {
		t.Fatalf("defaults = %+v", defaults)
	}

	env := map[string]string{
		"EZTEST_KNOWLEDGE_MAX_INJECT": "12",
		"EZTEST_KNOWLEDGE_MAX_BYTES":  "8192",
	}
	limits := application.KnowledgeLimitsFromEnv(func(key string) string { return env[key] })
	if limits.MaxInject != 12 || limits.MaxBytes != 8192 {
		t.Fatalf("parsed limits = %+v", limits)
	}
	env["EZTEST_KNOWLEDGE_MAX_INJECT"] = "99"
	env["EZTEST_KNOWLEDGE_MAX_BYTES"] = "1"
	clamped := application.KnowledgeLimitsFromEnv(func(key string) string { return env[key] })
	if clamped.MaxInject != 20 || clamped.MaxBytes != 1 {
		t.Fatalf("clamped limits = %+v", clamped)
	}
	env["EZTEST_KNOWLEDGE_MAX_INJECT"] = "not-a-number"
	env["EZTEST_KNOWLEDGE_MAX_BYTES"] = "-3"
	fallback := application.KnowledgeLimitsFromEnv(func(key string) string { return env[key] })
	if fallback.MaxInject != 5 || fallback.MaxBytes != 4096 {
		t.Fatalf("fallback limits = %+v", fallback)
	}
}

// TestClarificationRunInjectsApprovedKnowledge covers the P3 side of the
// injection: analysis and recon runs read the version's approved knowledge into
// the instruction, while an unapproved entry never reaches it.
func TestClarificationRunInjectsApprovedKnowledge(t *testing.T) {
	ctx := context.Background()
	f := newClarificationFixture()
	version := f.seedVersion(domain.VersionClarifying)
	f.startSession(t, version.ID)

	knowledge := newFakeKnowledgeRepo()
	knowledge.seed(domain.KnowledgeEntry{
		ID: 500, VersionID: version.ID, Kind: domain.KnowledgeKindDecision,
		Title: "Approved decision", Body: "Use the sandbox gateway.",
		Status: domain.KnowledgeApproved,
	})
	knowledge.seed(domain.KnowledgeEntry{
		ID: 501, VersionID: version.ID, Kind: domain.KnowledgeKindPattern,
		Title: "Draft secret", Body: "Never inject me.", Status: domain.KnowledgeDraft,
	})
	f.service = application.NewClarificationService(
		f.repo, f.requirements, f.versions, f.agents, f.events,
		application.WithClarificationIDGenerator(f.ids),
		application.WithClarificationKnowledge(application.NewKnowledgeService(
			knowledge, f.versions, f.events,
			application.WithKnowledgeIDGenerator(f.ids),
		)),
	)

	if _, err := f.service.StartAnalysis(ctx, version.ID, 42); err != nil {
		t.Fatalf("StartAnalysis: %v", err)
	}
	if len(f.agents.texts) != 1 {
		t.Fatalf("analysis texts = %d", len(f.agents.texts))
	}
	instruction := f.agents.texts[0].Text
	if !strings.Contains(instruction, "## 版本知识") || !strings.Contains(instruction, "Approved decision") ||
		!strings.Contains(instruction, "knowledgeRefs: 500") {
		t.Fatalf("analysis instruction = %q", instruction)
	}
	if strings.Contains(instruction, "Never inject me") || strings.Contains(instruction, "Draft secret") {
		t.Fatalf("analysis instruction leaked a draft: %q", instruction)
	}

	if _, err := f.service.StartReconWithParts(ctx, version.ID, nil, 42); err != nil {
		t.Fatalf("StartReconWithParts: %v", err)
	}
	if len(f.agents.partRuns) != 1 || len(f.agents.partRuns[0].Parts) == 0 {
		t.Fatalf("recon parts = %+v", f.agents.partRuns)
	}
	first := f.agents.partRuns[0].Parts[0]
	if first.Type != "text" || !strings.Contains(first.Text, "## 版本知识") {
		t.Fatalf("recon first part = %+v", first)
	}
}

// TestClarificationKnowledgeReadFailureDoesNotBlockRuns pins the degraded
// policy: an injection read failure is logged and the run still starts.
func TestClarificationKnowledgeReadFailureDoesNotBlockRuns(t *testing.T) {
	ctx := context.Background()
	f := newClarificationFixture()
	version := f.seedVersion(domain.VersionClarifying)
	f.startSession(t, version.ID)

	knowledge := newFakeKnowledgeRepo()
	knowledge.approvedErr = errors.New("database down")
	f.service = application.NewClarificationService(
		f.repo, f.requirements, f.versions, f.agents, f.events,
		application.WithClarificationIDGenerator(f.ids),
		application.WithClarificationKnowledge(application.NewKnowledgeService(
			knowledge, f.versions, f.events, application.WithKnowledgeIDGenerator(f.ids),
		)),
	)
	if _, err := f.service.StartAnalysis(ctx, version.ID, 42); err != nil {
		t.Fatalf("StartAnalysis with a failing knowledge read: %v", err)
	}
	if len(f.agents.texts) != 1 || strings.Contains(f.agents.texts[0].Text, "## 版本知识") {
		t.Fatalf("instruction = %+v", f.agents.texts)
	}
}

// requireKnowledgeReason asserts a version domain error with the stable reason.
func requireKnowledgeReason(t *testing.T, err error, reason string) {
	t.Helper()
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("error = %v, want a domain error with reason %q", err, reason)
	}
	if domainErr.Reason != reason {
		t.Fatalf("error reason = %q, want %q (%v)", domainErr.Reason, reason, err)
	}
}
