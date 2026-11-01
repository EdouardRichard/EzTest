package codeintelapp

import (
	"context"
	"strings"
	"testing"
	"time"

	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// fakeReviewStore is an append-only in-memory code_fact_review repository.
type fakeReviewStore struct {
	reviews     []codeintel.FactReview
	factKeys    map[int64]string
	factVersion map[int64]int
	verdicts    map[string]string
	nextID      int64
	createErr   error
	listErr     error
	latestErr   error
	latestCalls []string
}

func newFakeReviewStore() *fakeReviewStore {
	return &fakeReviewStore{factKeys: map[int64]string{}, factVersion: map[int64]int{}, verdicts: map[string]string{}}
}

func (f *fakeReviewStore) Create(_ context.Context, review codeintel.FactReview) (codeintel.FactReview, error) {
	if f.createErr != nil {
		return codeintel.FactReview{}, f.createErr
	}
	f.nextID++
	review.ID = f.nextID
	f.reviews = append(f.reviews, review)
	return review, nil
}

func (f *fakeReviewStore) List(_ context.Context, factID int64) ([]codeintel.FactReview, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	result := make([]codeintel.FactReview, 0, len(f.reviews))
	for _, review := range f.reviews {
		if review.FactID == factID {
			result = append(result, review)
		}
	}
	return result, nil
}

func (f *fakeReviewStore) LatestVerdicts(_ context.Context, repoID int64, commit string, factKeys []string) (map[string]string, error) {
	if f.latestErr != nil {
		return nil, f.latestErr
	}
	f.latestCalls = append(f.latestCalls, commit)
	result := map[string]string{}
	for factID, key := range f.factKeys {
		if !containsString(factKeys, key) {
			continue
		}
		latest := ""
		latestVersion := 0
		for _, review := range f.reviews {
			if review.FactID != factID || review.RepoID != repoID || review.CommitSHA != commit {
				continue
			}
			if review.FactVersion != f.factVersion[factID] {
				continue
			}
			if review.ID > int64(latestVersion) {
				latest = review.Verdict
				latestVersion = int(review.ID)
			}
		}
		if latest != "" {
			result[key] = latest
		} else if verdict, ok := f.verdicts[key]; ok {
			result[key] = verdict
		}
	}
	for key, verdict := range f.verdicts {
		if containsString(factKeys, key) {
			result[key] = verdict
		}
	}
	return result, nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

type reviewHarness struct {
	service  *ReviewService
	reviews  *fakeReviewStore
	facts    *fakeFactRepo
	versions *fakeVersionReader
	events   *fakeEvents
	audit    *fakeAudit
	fact     codeintel.Fact
	now      time.Time
	ctx      context.Context
}

func newReviewHarness() *reviewHarness {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	ctx := platform.WithActor(context.Background(), platform.Actor{ID: 42, Role: "admin"})
	facts := &fakeFactRepo{}
	fact, _, err := facts.InsertVersion(ctx, codeintel.Fact{
		ID: 7, RepoID: 10, CommitSHA: anchorCommitA, FactKey: "route:/orders", Version: 2,
		Kind: codeintel.EntityRoute, Path: "src/routes.ts", Name: "/orders", Source: "ai",
		Confidence: codeintel.ConfidenceHigh, Body: []byte(`{}`), Evidence: []byte(`[]`),
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		panic(err)
	}
	reviews := newFakeReviewStore()
	reviews.factKeys[fact.ID] = fact.FactKey
	reviews.factVersion[fact.ID] = fact.Version
	events := &fakeEvents{}
	audit := &fakeAudit{}
	versions := &fakeVersionReader{bindings: []VersionSUTBinding{{VersionSUTID: 11, VersionID: 1, RepoID: 10}}}
	service := NewReviewService(ReviewDeps{
		Reviews:  reviews,
		Facts:    facts,
		Versions: versions,
		Events:   events,
		Audit:    audit,
		Clock:    fakeClock{now: now},
		IDs:      &fakeIDs{},
	})
	return &reviewHarness{
		service: service, reviews: reviews, facts: facts,
		versions: versions,
		events:   events, audit: audit, fact: fact, now: now, ctx: ctx,
	}
}

func TestReviewAppendsAndKeepsLatestVerdict(t *testing.T) {
	h := newReviewHarness()
	first, err := h.service.Create(h.ctx, 1, h.fact.ID, 42, codeintel.ReviewCorrect, "looks right")
	if err != nil {
		t.Fatalf("Create correct: %v", err)
	}
	second, err := h.service.Create(h.ctx, 1, h.fact.ID, 42, codeintel.ReviewIncorrect, "renamed upstream")
	if err != nil {
		t.Fatalf("Create incorrect: %v", err)
	}
	if first.ID == second.ID || first.Verdict != "correct" || second.Verdict != "incorrect" {
		t.Fatalf("reviews = %+v / %+v", first, second)
	}
	if second.FactVersion != h.fact.Version || second.RepoID != 10 || second.CommitSHA != anchorCommitA {
		t.Fatalf("review snapshot = %+v", second)
	}
	views, err := h.service.List(h.ctx, 1, h.fact.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(views) != 2 || views[0].ID != first.ID || views[1].ID != second.ID {
		t.Fatalf("reviews list = %+v", views)
	}

	verdicts, err := h.reviews.LatestVerdicts(h.ctx, 10, anchorCommitA, []string{h.fact.FactKey})
	if err != nil {
		t.Fatalf("LatestVerdicts: %v", err)
	}
	if verdicts[h.fact.FactKey] != "incorrect" {
		t.Fatalf("latest verdict = %+v", verdicts)
	}

	if len(h.events.events) != 2 {
		t.Fatalf("events = %v", h.events.types())
	}
	event := h.events.events[1]
	if event.Type != "code.fact_reviewed" || event.Payload["verdict"] != "incorrect" || event.Payload["fact_id"] != formatID(h.fact.ID) {
		t.Fatalf("event = %+v", event)
	}
	if event.Payload["fact_version"] != 2 || event.Payload["reviewer_id"] != "42" {
		t.Fatalf("event payload = %+v", event.Payload)
	}
	if len(h.audit.entries) != 2 || h.audit.entries[1].Action != "code.fact_review" {
		t.Fatalf("audit = %+v", h.audit.entries)
	}
	if _, ok := h.audit.entries[1].Metadata["body"]; ok {
		t.Fatal("audit must not carry code bodies")
	}
}

func TestReviewRejectsInvalidFactsAndVerdicts(t *testing.T) {
	h := newReviewHarness()
	_, err := h.service.Create(h.ctx, 1, 999, 42, codeintel.ReviewCorrect, "")
	assertPlatformError(t, err, 404, "not_found")

	h.versions.bindings = nil
	_, err = h.service.Create(h.ctx, 2, h.fact.ID, 42, codeintel.ReviewCorrect, "")
	assertPlatformError(t, err, 422, "fact_not_in_version")

	_, err = h.service.Create(h.ctx, 1, h.fact.ID, 42, "maybe", "")
	assertPlatformError(t, err, 422, "invalid_verdict")

	_, err = h.service.Create(h.ctx, 1, h.fact.ID, 42, codeintel.ReviewCorrect, strings.Repeat("x", codeintel.MaxReasonChars+1))
	assertPlatformError(t, err, 422, "invalid_reason")

	_, err = h.service.Create(h.ctx, 1, h.fact.ID, 0, codeintel.ReviewCorrect, "")
	assertPlatformError(t, err, 401, "unauthenticated")
}

func TestReviewListRejectsUnknownFact(t *testing.T) {
	h := newReviewHarness()
	_, err := h.service.List(h.ctx, 1, 999)
	assertPlatformError(t, err, 404, "not_found")
}
