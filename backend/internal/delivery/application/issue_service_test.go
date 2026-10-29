package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

const (
	issueTestVersionID = int64(1)
	issueTestActor     = int64(42)
)

// issueTestNow is the frozen clock every service test runs on.
var issueTestNow = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

// --- test doubles -----------------------------------------------------------

type issueTestIDs struct{ next int64 }

func (g *issueTestIDs) Next() int64 {
	g.next++
	return g.next
}

type issueTestClock struct{ now time.Time }

func (c *issueTestClock) Now() time.Time { return c.now }

type issueFakeEvents struct{ published []IssueEvent }

func (f *issueFakeEvents) Publish(_ context.Context, event IssueEvent) error {
	f.published = append(f.published, event)
	return nil
}

type issueFakeAudit struct{ recorded []IssueAuditEvent }

func (f *issueFakeAudit) Record(_ context.Context, event IssueAuditEvent) error {
	f.recorded = append(f.recorded, event)
	return nil
}

// issueFakeVersionReader is the in-memory IssueVersionReader. The test version
// exists by default; err makes the lookup fail.
type issueFakeVersionReader struct {
	versions map[int64]bool
	calls    []int64
	err      error
}

func newIssueFakeVersionReader() *issueFakeVersionReader {
	return &issueFakeVersionReader{versions: map[int64]bool{issueTestVersionID: true}}
}

func (r *issueFakeVersionReader) VersionExists(_ context.Context, versionID int64) (bool, error) {
	r.calls = append(r.calls, versionID)
	if r.err != nil {
		return false, r.err
	}
	return r.versions[versionID], nil
}

// issueFakeStore is an in-memory IssueStore. Its InTx snapshots the maps and
// restores them when the callback fails, so rollback semantics are exercised by
// the unit tests without PostgreSQL. Reference ownership is not a store concern:
// issueFakeRefValidator stands in for the IssueRefValidator port.
type issueFakeStore struct {
	issues  map[int64]domain.Issue
	refs    map[int64][]domain.IssueRef
	creates int
	updates int
	inTx    int
	failAt  int
}

func newIssueFakeStore() *issueFakeStore {
	return &issueFakeStore{
		issues: make(map[int64]domain.Issue),
		refs:   make(map[int64][]domain.IssueRef),
	}
}

func issueTestOwnerKey(kind string, id int64) string {
	return fmt.Sprintf("%s:%d", kind, id)
}

// issueFakeRefValidator is the in-memory IssueRefValidator: allow registers the
// version each (kind, id) target belongs to, mimicking the cross-context
// execution/mindmap/version/codeintel adapters that T17 wires.
type issueFakeRefValidator struct {
	owners map[string]int64
	calls  int
}

func newIssueFakeRefValidator() *issueFakeRefValidator {
	return &issueFakeRefValidator{owners: make(map[string]int64)}
}

func (v *issueFakeRefValidator) allow(kind string, id, versionID int64) {
	v.owners[issueTestOwnerKey(kind, id)] = versionID
}

func (v *issueFakeRefValidator) ValidateRefs(_ context.Context, versionID int64, refs []domain.IssueRef) error {
	v.calls++
	for _, ref := range refs {
		if err := domain.ValidateIssueRef(ref.RefKind, ref.RefID); err != nil {
			return err
		}
		if ref.RefKind == domain.RefKindCommit {
			continue
		}
		id, err := strconv.ParseInt(ref.RefID, 10, 64)
		if err != nil {
			return domain.NewInvalid("refs", domain.ReasonIssueRefInvalid,
				fmt.Sprintf("issue ref %s:%s is not a decimal id", ref.RefKind, ref.RefID))
		}
		owner, ok := v.owners[issueTestOwnerKey(ref.RefKind, id)]
		if !ok {
			return domain.NewInvalid("refs", domain.ReasonIssueRefInvalid,
				fmt.Sprintf("issue ref %s:%s does not exist", ref.RefKind, ref.RefID))
		}
		if owner != versionID {
			return domain.NewInvalid("refs", domain.ReasonIssueRefInvalid,
				fmt.Sprintf("issue ref %s:%s belongs to version %d", ref.RefKind, ref.RefID, owner))
		}
	}
	return nil
}

func (s *issueFakeStore) InTx(_ context.Context, fn func(IssueStore) error) error {
	s.inTx++
	issues := make(map[int64]domain.Issue, len(s.issues))
	for id, issue := range s.issues {
		issues[id] = issue
	}
	refs := make(map[int64][]domain.IssueRef, len(s.refs))
	for id, list := range s.refs {
		refs[id] = append([]domain.IssueRef(nil), list...)
	}
	if err := fn(s); err != nil {
		s.issues = issues
		s.refs = refs
		return err
	}
	return nil
}

func (s *issueFakeStore) Create(_ context.Context, issue domain.Issue, refs []domain.IssueRef) (domain.Issue, error) {
	s.creates++
	if s.failAt > 0 && s.creates == s.failAt {
		return domain.Issue{}, errors.New("issue store: create failed")
	}
	if issue.DedupeKey != "" {
		for _, existing := range s.issues {
			if existing.VersionID == issue.VersionID && existing.DedupeKey == issue.DedupeKey {
				return domain.Issue{}, domain.NewConflict(domain.ReasonIssueStateConflict,
					"issue dedupe key already exists")
			}
		}
	}
	s.issues[issue.ID] = issue
	s.refs[issue.ID] = nil
	s.AddRefs(nil, issue.ID, refs) //nolint:errcheck // in-memory append cannot fail
	return issue, nil
}

func (s *issueFakeStore) Get(_ context.Context, versionID, issueID int64) (domain.Issue, bool, error) {
	issue, ok := s.issues[issueID]
	if !ok || issue.VersionID != versionID {
		return domain.Issue{}, false, nil
	}
	return issue, true, nil
}

func (s *issueFakeStore) FindByDedupeKey(_ context.Context, versionID int64, dedupeKey string) (domain.Issue, bool, error) {
	if dedupeKey == "" {
		return domain.Issue{}, false, nil
	}
	ids := make([]int64, 0, len(s.issues))
	for id := range s.issues {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		issue := s.issues[id]
		if issue.VersionID == versionID && issue.DedupeKey == dedupeKey {
			return issue, true, nil
		}
	}
	return domain.Issue{}, false, nil
}

func (s *issueFakeStore) Update(_ context.Context, issue domain.Issue, expectedRevision int) (domain.Issue, error) {
	current, ok := s.issues[issue.ID]
	if !ok || current.VersionID != issue.VersionID {
		return domain.Issue{}, domain.NewNotFound("issue", issue.ID)
	}
	if current.Revision != expectedRevision {
		return domain.Issue{}, domain.NewStateConflict(domain.ReasonIssueStateConflict,
			"issue revision changed")
	}
	s.updates++
	s.issues[issue.ID] = issue
	return issue, nil
}

func (s *issueFakeStore) AddRefs(_ context.Context, issueID int64, refs []domain.IssueRef) error {
	existing := s.refs[issueID]
	seen := make(map[string]struct{}, len(existing)+len(refs))
	for _, ref := range existing {
		seen[issueTestOwnerKey(ref.RefKind, 0)+":"+ref.RefID] = struct{}{}
	}
	for _, ref := range refs {
		key := issueTestOwnerKey(ref.RefKind, 0) + ":" + ref.RefID
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		ref.IssueID = issueID
		existing = append(existing, ref)
	}
	s.refs[issueID] = existing
	return nil
}

func (s *issueFakeStore) ListRefs(_ context.Context, issueID int64) ([]domain.IssueRef, error) {
	return append([]domain.IssueRef(nil), s.refs[issueID]...), nil
}

func (s *issueFakeStore) List(_ context.Context, versionID int64, filter IssueFilter) ([]domain.Issue, bool, error) {
	items := make([]domain.Issue, 0)
	for _, issue := range s.issues {
		if issue.VersionID != versionID {
			continue
		}
		if filter.Status != "" && issue.Status != filter.Status {
			continue
		}
		if filter.Severity != "" && issue.Severity != filter.Severity {
			continue
		}
		if filter.Category != "" && issue.Category != filter.Category {
			continue
		}
		if filter.BatchID != nil && (issue.BatchID == nil || *issue.BatchID != *filter.BatchID) {
			continue
		}
		if filter.CaseRunID != nil && (issue.CaseRunID == nil || *issue.CaseRunID != *filter.CaseRunID) {
			continue
		}
		if filter.CaseNodeID != nil && (issue.CaseNodeID == nil || *issue.CaseNodeID != *filter.CaseNodeID) {
			continue
		}
		if filter.Query != "" && !strings.Contains(strings.ToLower(issue.Title), strings.ToLower(filter.Query)) {
			continue
		}
		if filter.Cursor > 0 {
			// The keyset cursor is severity-rank aware: the page continues
			// after (rank, id) in the severity-weight then id-desc order. The
			// cursor row is resolved within the same version, matching the
			// version-scoped repository subquery.
			cursorIssue, ok := s.issues[filter.Cursor]
			if !ok || cursorIssue.VersionID != versionID {
				continue
			}
			cursorRank := domain.SeverityRank(cursorIssue.Severity)
			rank := domain.SeverityRank(issue.Severity)
			if rank < cursorRank || (rank == cursorRank && issue.ID >= filter.Cursor) {
				continue
			}
		}
		items = append(items, issue)
	}
	sort.Slice(items, func(i, j int) bool {
		left, right := domain.SeverityRank(items[i].Severity), domain.SeverityRank(items[j].Severity)
		if left != right {
			return left < right
		}
		return items[i].ID > items[j].ID
	})
	limit := filter.Limit
	if limit <= 0 {
		limit = 20
	}
	if len(items) > limit {
		return items[:limit], true, nil
	}
	return items, false, nil
}

// issueRaceStore models a concurrent transaction that commits the same dedupe
// key between the service's pre-read and its Create. InTx does not snapshot or
// restore: the concurrent row belongs to another transaction, so it must survive
// the failed transaction's rollback and be visible to the retry.
type issueRaceStore struct {
	*issueFakeStore
	raceDedupeKey string
	raced         bool
}

func (s *issueRaceStore) InTx(ctx context.Context, fn func(IssueStore) error) error {
	s.issueFakeStore.inTx++
	return fn(s)
}

func (s *issueRaceStore) Create(ctx context.Context, issue domain.Issue, refs []domain.IssueRef) (domain.Issue, error) {
	if !s.raced && issue.DedupeKey == s.raceDedupeKey {
		s.raced = true
		existing := issue
		existing.ID = 9001
		existing.Title = "并发落库的问题"
		existing.CaseRunID = nil
		existing.CaseNodeID = issueTestPtr(8000)
		existing.RecurrenceCount = 0
		existing.Revision = 3
		existing.CreatedAt = issueTestNow.Add(-time.Hour)
		existing.UpdatedAt = issueTestNow.Add(-time.Hour)
		s.issues[existing.ID] = existing
		s.refs[existing.ID] = []domain.IssueRef{
			{IssueID: existing.ID, RefKind: domain.RefKindCaseNode, RefID: "8000"},
		}
		return domain.Issue{}, ErrIssueDedupeConflict
	}
	return s.issueFakeStore.Create(ctx, issue, refs)
}

// --- test environment -------------------------------------------------------

type issueTestEnv struct {
	svc      *IssueManager
	store    *issueFakeStore
	refs     *issueFakeRefValidator
	versions *issueFakeVersionReader
	events   *issueFakeEvents
	audit    *issueFakeAudit
	ids      *issueTestIDs
	clock    *issueTestClock
}

func newIssueTestEnv(t *testing.T) *issueTestEnv {
	t.Helper()
	store := newIssueFakeStore()
	refs := newIssueFakeRefValidator()
	versions := newIssueFakeVersionReader()
	events := &issueFakeEvents{}
	audit := &issueFakeAudit{}
	ids := &issueTestIDs{next: 1000}
	clock := &issueTestClock{now: issueTestNow}
	svc := NewIssueService(IssueServiceDeps{
		Store:    store,
		Versions: versions,
		Refs:     refs,
		IDs:      ids,
		Clock:    clock,
		Events:   events,
		Audit:    audit,
	})
	return &issueTestEnv{svc: svc, store: store, refs: refs, versions: versions,
		events: events, audit: audit, ids: ids, clock: clock}
}

func (e *issueTestEnv) allow(kind string, id, versionID int64) {
	e.refs.allow(kind, id, versionID)
}

func (e *issueTestEnv) seed(issue domain.Issue, refs ...domain.IssueRef) {
	e.store.issues[issue.ID] = issue
	for _, ref := range refs {
		ref.IssueID = issue.ID
		e.store.refs[issue.ID] = append(e.store.refs[issue.ID], ref)
	}
}

// issueTestPtr returns a pointer to id.
func issueTestPtr(id int64) *int64 { return &id }

// issueTestDraftKey returns a deterministic 64-hex key for the service tests.
func issueTestDraftKey(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

func issueTestDraftKeyN(n int) string {
	return fmt.Sprintf("%064x", n+1)
}

// issueTestValidAIDraft is a complete AI defect draft.
func issueTestValidAIDraft() domain.IssueDraft {
	return domain.IssueDraft{
		DedupeKey:    issueTestDraftKey("defect"),
		Title:        "登录按钮无响应",
		Description:  "点击登录后页面停留在原处",
		ReproSteps:   "1. 打开登录页\n2. 点击登录",
		Expected:     "进入首页",
		Actual:       "页面无变化",
		Severity:     domain.SeverityMajor,
		Confidence:   domain.ConfidenceHigh,
		Category:     domain.CategoryDefect,
		FailureClass: domain.FailureClassApplication,
		CaseRunID:    issueTestPtr(777),
		CaseNodeID:   issueTestPtr(9001),
		EvidenceIDs:  []int64{31},
	}
}

// issueTestSeedOpenIssue stores one open issue with an existing occurrence ref.
func (e *issueTestEnv) seedOpenIssue(id int64, dedupeKey string) domain.Issue {
	caseNodeID := int64(8000)
	issue := domain.Issue{
		ID:              id,
		VersionID:       issueTestVersionID,
		BatchID:         issueTestPtr(600),
		CaseRunID:       issueTestPtr(700),
		CaseNodeID:      &caseNodeID,
		Source:          domain.SourceAI,
		Category:        domain.CategoryDefect,
		Severity:        domain.SeverityMajor,
		Status:          domain.StatusOpen,
		Confidence:      domain.ConfidenceLow,
		FailureClass:    domain.FailureClassApplication,
		DedupeKey:       dedupeKey,
		Title:           "登录按钮无响应",
		Description:     "旧描述",
		Actual:          "",
		RecurrenceCount: 0,
		Revision:        3,
		CreatedBy:       7,
		UpdatedBy:       7,
		CreatedAt:       issueTestNow.Add(-time.Hour),
		UpdatedAt:       issueTestNow.Add(-time.Hour),
	}
	e.seed(issue, domain.IssueRef{RefKind: domain.RefKindCaseNode, RefID: "8000"})
	return issue
}

// --- CreateHuman ------------------------------------------------------------

func TestCreateHumanIssue(t *testing.T) {
	env := newIssueTestEnv(t)
	env.allow(domain.RefKindCaseRun, 777, issueTestVersionID)
	env.allow(domain.RefKindCaseNode, 9001, issueTestVersionID)
	env.allow(domain.RefKindRequirement, 55, issueTestVersionID)

	cmd := CreateIssueCommand{
		Title:         "  登录按钮无响应  ",
		Description:   "点击登录后页面停留在原处",
		ReproSteps:    "打开登录页并点击登录",
		Expected:      "进入首页",
		Actual:        "页面无变化",
		Severity:      domain.SeverityMajor,
		CaseRunID:     issueTestPtr(777),
		CaseNodeID:    issueTestPtr(9001),
		RequirementID: issueTestPtr(55),
	}
	issue, err := env.svc.CreateHuman(context.Background(), issueTestVersionID, issueTestActor, cmd)
	if err != nil {
		t.Fatalf("CreateHuman = %v", err)
	}
	if issue.ID != 1001 {
		t.Errorf("id = %d, want the first generated id 1001", issue.ID)
	}
	if issue.Title != "登录按钮无响应" {
		t.Errorf("title = %q, want the trimmed value", issue.Title)
	}
	if issue.Source != domain.SourceHuman {
		t.Errorf("source = %q, want human", issue.Source)
	}
	if issue.DedupeKey != "" {
		t.Errorf("dedupe key = %q, want empty for a human issue", issue.DedupeKey)
	}
	if issue.Status != domain.StatusOpen || issue.Revision != 1 || issue.RecurrenceCount != 0 {
		t.Errorf("initial state = (%q, revision %d, recurrence %d)", issue.Status, issue.Revision, issue.RecurrenceCount)
	}
	if issue.Category != domain.CategoryDefect || issue.Confidence != domain.ConfidenceMedium {
		t.Errorf("defaults = (%q, %q), want (defect, medium)", issue.Category, issue.Confidence)
	}
	if issue.CreatedBy != issueTestActor || issue.UpdatedBy != issueTestActor {
		t.Errorf("actor = (%d, %d), want %d", issue.CreatedBy, issue.UpdatedBy, issueTestActor)
	}
	if !issue.CreatedAt.Equal(issueTestNow) || !issue.UpdatedAt.Equal(issueTestNow) {
		t.Errorf("timestamps = (%s, %s), want the injected clock", issue.CreatedAt, issue.UpdatedAt)
	}
	if issue.BatchID != nil || issue.StepResultID != nil {
		t.Errorf("human issue has unexpected batch/step links: %+v", issue)
	}

	refs := env.store.refs[issue.ID]
	if len(refs) != 3 {
		t.Fatalf("refs = %+v, want case_run, case_node and requirement", refs)
	}
	wantRefs := map[string]string{
		domain.RefKindCaseNode:    "9001",
		domain.RefKindCaseRun:     "777",
		domain.RefKindRequirement: "55",
	}
	for _, ref := range refs {
		if wantRefs[ref.RefKind] != ref.RefID {
			t.Errorf("unexpected ref %+v", ref)
		}
	}

	if len(env.events.published) != 1 {
		t.Fatalf("events = %+v, want one issue.created", env.events.published)
	}
	event := env.events.published[0]
	if event.Type != "issue.created" || event.VersionID != issueTestVersionID {
		t.Errorf("event = %+v", event)
	}
	if event.Detail["issueId"] != "1001" {
		t.Errorf("event issueId = %v (%T), want the decimal string", event.Detail["issueId"], event.Detail["issueId"])
	}
	if event.Detail["source"] != domain.SourceHuman || event.Detail["severity"] != domain.SeverityMajor ||
		event.Detail["status"] != domain.StatusOpen || event.Detail["confidence"] != domain.ConfidenceMedium {
		t.Errorf("event detail = %+v", event.Detail)
	}
	if _, leaked := event.Detail["title"]; leaked {
		t.Error("event detail leaks the issue title")
	}

	if len(env.audit.recorded) != 1 {
		t.Fatalf("audit = %+v, want one delivery.issue.created", env.audit.recorded)
	}
	entry := env.audit.recorded[0]
	if entry.Action != "delivery.issue.created" || entry.TargetType != "issue" || entry.TargetID != "1001" {
		t.Errorf("audit = %+v", entry)
	}
	if entry.ActorID != issueTestActor {
		t.Errorf("audit actor = %d, want %d", entry.ActorID, issueTestActor)
	}
	if entry.Metadata["version_id"] != issueTestVersionID || entry.Metadata["source"] != domain.SourceHuman ||
		entry.Metadata["severity"] != domain.SeverityMajor || entry.Metadata["category"] != domain.CategoryDefect {
		t.Errorf("audit metadata = %+v", entry.Metadata)
	}
	if _, leaked := entry.Metadata["title"]; leaked {
		t.Error("audit metadata leaks the issue title")
	}
}

// TestCreateHumanIssueChecksVersionExists pins the T2b version gate: a human
// issue is only created for an existing version, and a missing version is a
// 404 version_not_found with no row, event or audit entry.
func TestCreateHumanIssueChecksVersionExists(t *testing.T) {
	command := func() CreateIssueCommand {
		return CreateIssueCommand{Title: "login fails", Severity: domain.SeverityMajor}
	}

	t.Run("existing version passes", func(t *testing.T) {
		env := newIssueTestEnv(t)
		issue, err := env.svc.CreateHuman(context.Background(), issueTestVersionID, issueTestActor, command())
		if err != nil {
			t.Fatalf("CreateHuman = %v", err)
		}
		if issue.VersionID != issueTestVersionID {
			t.Errorf("version = %d, want %d", issue.VersionID, issueTestVersionID)
		}
		if env.store.creates != 1 || len(env.events.published) != 1 || len(env.audit.recorded) != 1 {
			t.Fatalf("create side effects = %d/%d/%d, want 1/1/1",
				env.store.creates, len(env.events.published), len(env.audit.recorded))
		}
		if len(env.versions.calls) != 1 || env.versions.calls[0] != issueTestVersionID {
			t.Errorf("version lookups = %v, want one lookup of %d", env.versions.calls, issueTestVersionID)
		}
	})

	t.Run("missing version is version_not_found", func(t *testing.T) {
		const missingVersionID = int64(4040)
		env := newIssueTestEnv(t)
		_, err := env.svc.CreateHuman(context.Background(), missingVersionID, issueTestActor, command())
		var domainErr *domain.Error
		if !errors.As(err, &domainErr) {
			t.Fatalf("error type = %T (%v)", err, err)
		}
		if domainErr.Kind != domain.KindNotFound || domainErr.Reason != "version_not_found" {
			t.Fatalf("error = %+v, want 404 version_not_found", domainErr)
		}
		if env.store.creates != 0 || len(env.store.issues) != 0 {
			t.Fatalf("missing version wrote %d creates and %d rows", env.store.creates, len(env.store.issues))
		}
		if len(env.events.published) != 0 || len(env.audit.recorded) != 0 {
			t.Fatalf("missing version published %d events and %d audit entries",
				len(env.events.published), len(env.audit.recorded))
		}
		if len(env.versions.calls) != 1 || env.versions.calls[0] != missingVersionID {
			t.Errorf("version lookups = %v, want one lookup of %d", env.versions.calls, missingVersionID)
		}
	})
}

// TestCreateHumanIssueFailsClosedWithoutVersionReader proves an unwired version
// port cannot silently skip the target-version check and create an orphan issue.
func TestCreateHumanIssueFailsClosedWithoutVersionReader(t *testing.T) {
	store := newIssueFakeStore()
	events := &issueFakeEvents{}
	audit := &issueFakeAudit{}
	svc := NewIssueService(IssueServiceDeps{
		Store:  store,
		IDs:    &issueTestIDs{next: 1000},
		Clock:  &issueTestClock{now: issueTestNow},
		Events: events,
		Audit:  audit,
	})
	_, err := svc.CreateHuman(context.Background(), issueTestVersionID, issueTestActor, CreateIssueCommand{
		Title:    "login fails",
		Severity: domain.SeverityMajor,
	})
	if err == nil {
		t.Fatal("CreateHuman accepted a version without a version reader")
	}
	if store.creates != 0 || len(store.issues) != 0 || len(events.published) != 0 || len(audit.recorded) != 0 {
		t.Fatalf("unconfigured version reader wrote state: creates=%d rows=%d events=%d audit=%d",
			store.creates, len(store.issues), len(events.published), len(audit.recorded))
	}
}

// TestCreateHumanIssuePropagatesVersionLookupFailure proves a version-reader
// error is not mistaken for a missing version and writes no issue.
func TestCreateHumanIssuePropagatesVersionLookupFailure(t *testing.T) {
	env := newIssueTestEnv(t)
	lookupErr := errors.New("version store unavailable")
	env.versions.err = lookupErr
	_, err := env.svc.CreateHuman(context.Background(), issueTestVersionID, issueTestActor, CreateIssueCommand{
		Title:    "login fails",
		Severity: domain.SeverityMajor,
	})
	if !errors.Is(err, lookupErr) {
		t.Fatalf("error = %v, want the wrapped version lookup failure", err)
	}
	if env.store.creates != 0 || len(env.store.issues) != 0 ||
		len(env.events.published) != 0 || len(env.audit.recorded) != 0 {
		t.Fatalf("failed version lookup wrote state: creates=%d rows=%d events=%d audit=%d",
			env.store.creates, len(env.store.issues), len(env.events.published), len(env.audit.recorded))
	}
}

// TestApplyDraftsChecksVersionExists proves the AI materialization path also
// refuses to write issues for a nonexistent version.
func TestApplyDraftsChecksVersionExists(t *testing.T) {
	const missingVersionID = int64(4040)
	env := newIssueTestEnv(t)
	env.allow(domain.RefKindCaseRun, 777, issueTestVersionID)
	env.allow(domain.RefKindCaseNode, 9001, issueTestVersionID)
	env.allow(domain.RefKindEvidence, 31, issueTestVersionID)

	_, err := env.svc.ApplyDrafts(context.Background(), missingVersionID, 700, issueTestActor,
		[]domain.IssueDraft{issueTestValidAIDraft()})
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("error type = %T (%v)", err, err)
	}
	if domainErr.Kind != domain.KindNotFound || domainErr.Reason != "version_not_found" {
		t.Fatalf("error = %+v, want 404 version_not_found", domainErr)
	}
	if env.store.creates != 0 || len(env.store.issues) != 0 || env.store.inTx != 0 {
		t.Fatalf("missing version wrote state: creates=%d rows=%d transactions=%d",
			env.store.creates, len(env.store.issues), env.store.inTx)
	}
	if len(env.events.published) != 0 || len(env.audit.recorded) != 0 {
		t.Fatalf("missing version published %d events and %d audit entries",
			len(env.events.published), len(env.audit.recorded))
	}
	if len(env.versions.calls) != 1 || env.versions.calls[0] != missingVersionID {
		t.Errorf("version lookups = %v, want one lookup of %d", env.versions.calls, missingVersionID)
	}
}

func TestCreateHumanIssueRejectsInvalidInput(t *testing.T) {
	foreign := int64(9999)
	cases := []struct {
		name   string
		mutate func(*CreateIssueCommand)
		reason string
		field  string
	}{
		{"empty title", func(c *CreateIssueCommand) { c.Title = "  " }, domain.ReasonInvalidArgument, "title"},
		{"long title", func(c *CreateIssueCommand) { c.Title = strings.Repeat("x", 201) }, domain.ReasonInvalidArgument, "title"},
		{"missing severity", func(c *CreateIssueCommand) { c.Severity = "" }, domain.ReasonInvalidArgument, "severity"},
		{"bad severity", func(c *CreateIssueCommand) { c.Severity = "urgent" }, domain.ReasonInvalidArgument, "severity"},
		{"bad category", func(c *CreateIssueCommand) { c.Category = "bug" }, domain.ReasonInvalidArgument, "category"},
		{"bad confidence", func(c *CreateIssueCommand) { c.Confidence = "certain" }, domain.ReasonInvalidArgument, "confidence"},
		{"foreign case node", func(c *CreateIssueCommand) { c.CaseNodeID = &foreign }, domain.ReasonIssueRefInvalid, "refs"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newIssueTestEnv(t)
			env.allow(domain.RefKindCaseNode, 9001, issueTestVersionID)
			cmd := CreateIssueCommand{Title: "登录失败", Severity: domain.SeverityMajor, CaseNodeID: issueTestPtr(9001)}
			tc.mutate(&cmd)
			issue, err := env.svc.CreateHuman(context.Background(), issueTestVersionID, issueTestActor, cmd)
			if err == nil {
				t.Fatalf("CreateHuman succeeded with %+v", issue)
			}
			var domainErr *domain.Error
			if !errors.As(err, &domainErr) {
				t.Fatalf("error type = %T (%v)", err, err)
			}
			if domainErr.Reason != tc.reason || domainErr.Field != tc.field {
				t.Fatalf("error = %+v, want field %q reason %q", domainErr, tc.field, tc.reason)
			}
			if env.store.creates != 0 || len(env.events.published) != 0 || len(env.audit.recorded) != 0 {
				t.Fatalf("invalid command wrote state: creates=%d events=%d audit=%d",
					env.store.creates, len(env.events.published), len(env.audit.recorded))
			}
		})
	}
}

// --- ApplyDrafts ------------------------------------------------------------

func TestApplyDraftsRejectsIncompleteAIDraft(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*domain.IssueDraft)
		field  string
	}{
		{"missing dedupe", func(d *domain.IssueDraft) { d.DedupeKey = "" }, "dedupeKey"},
		{"malformed dedupe", func(d *domain.IssueDraft) { d.DedupeKey = "abc" }, "dedupeKey"},
		{"missing failure class", func(d *domain.IssueDraft) { d.FailureClass = "" }, "failureClass"},
		{"non-application failure class", func(d *domain.IssueDraft) {
			d.FailureClass = domain.FailureClassEnvironment
		}, "failureClass"},
		{"missing case node", func(d *domain.IssueDraft) { d.CaseNodeID = nil }, "caseNodeId"},
		{"missing severity", func(d *domain.IssueDraft) { d.Severity = "" }, "severity"},
		{"missing confidence", func(d *domain.IssueDraft) { d.Confidence = "" }, "confidence"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newIssueTestEnv(t)
			draft := issueTestValidAIDraft()
			tc.mutate(&draft)
			issues, err := env.svc.ApplyDrafts(context.Background(), issueTestVersionID, 700, issueTestActor,
				[]domain.IssueDraft{draft})
			if err == nil {
				t.Fatalf("ApplyDrafts succeeded with %+v", issues)
			}
			var domainErr *domain.Error
			if !errors.As(err, &domainErr) {
				t.Fatalf("error type = %T (%v)", err, err)
			}
			if domainErr.Kind != domain.KindInvalid || domainErr.Field != tc.field {
				t.Fatalf("error = %+v, want invalid field %q", domainErr, tc.field)
			}
			if env.store.creates != 0 || env.store.inTx != 0 || len(env.events.published) != 0 {
				t.Fatalf("invalid draft touched the store: creates=%d tx=%d events=%d",
					env.store.creates, env.store.inTx, len(env.events.published))
			}
		})
	}
}

func TestApplyDraftsCreatesAIIssue(t *testing.T) {
	env := newIssueTestEnv(t)
	env.allow(domain.RefKindCaseRun, 777, issueTestVersionID)
	env.allow(domain.RefKindCaseNode, 9001, issueTestVersionID)
	env.allow(domain.RefKindEvidence, 31, issueTestVersionID)
	env.allow(domain.RefKindRequirement, 55, issueTestVersionID)

	draft := issueTestValidAIDraft()
	draft.RequirementIDs = []int64{55}
	issues, err := env.svc.ApplyDrafts(context.Background(), issueTestVersionID, 700, issueTestActor,
		[]domain.IssueDraft{draft})
	if err != nil {
		t.Fatalf("ApplyDrafts = %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("issues = %+v, want one materialized issue", issues)
	}
	issue := issues[0]
	if issue.Source != domain.SourceAI || issue.DedupeKey != draft.DedupeKey {
		t.Errorf("source/dedupe = (%q, %q)", issue.Source, issue.DedupeKey)
	}
	if issue.FailureClass != domain.FailureClassApplication || issue.Category != domain.CategoryDefect {
		t.Errorf("classification = (%q, %q)", issue.FailureClass, issue.Category)
	}
	if issue.BatchID == nil || *issue.BatchID != 700 {
		t.Errorf("batch = %v, want 700", issue.BatchID)
	}
	if issue.CaseRunID == nil || *issue.CaseRunID != 777 || issue.CaseNodeID == nil || *issue.CaseNodeID != 9001 {
		t.Errorf("case links = (%v, %v)", issue.CaseRunID, issue.CaseNodeID)
	}
	if issue.CreatedBy != issueTestActor || issue.UpdatedBy != issueTestActor {
		t.Errorf("created_by = %d", issue.CreatedBy)
	}
	if issue.Status != domain.StatusOpen || issue.Revision != 1 || issue.RecurrenceCount != 0 {
		t.Errorf("initial state = (%q, %d, %d)", issue.Status, issue.Revision, issue.RecurrenceCount)
	}

	refs := domain.NormalizeIssueRefs(env.store.refs[issue.ID])
	if len(refs) != 4 {
		t.Fatalf("refs = %+v, want case_run, case_node, evidence and requirement", refs)
	}
	want := []domain.IssueRef{
		{IssueID: issue.ID, RefKind: domain.RefKindCaseNode, RefID: "9001"},
		{IssueID: issue.ID, RefKind: domain.RefKindCaseRun, RefID: "777"},
		{IssueID: issue.ID, RefKind: domain.RefKindEvidence, RefID: "31"},
		{IssueID: issue.ID, RefKind: domain.RefKindRequirement, RefID: "55"},
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Errorf("ref[%d] = %+v, want %+v", i, refs[i], want[i])
		}
	}

	if len(env.events.published) != 1 {
		t.Fatalf("events = %+v, want one issue.created", env.events.published)
	}
	event := env.events.published[0]
	if event.Type != "issue.created" || event.Detail["issueId"] != fmt.Sprint(issue.ID) ||
		event.Detail["source"] != domain.SourceAI {
		t.Errorf("event = %+v", event)
	}
	if event.Detail["confidence"] != domain.ConfidenceHigh || event.Detail["severity"] != domain.SeverityMajor {
		t.Errorf("event detail = %+v", event.Detail)
	}
	if len(event.RefCaseIDs) != 1 || event.RefCaseIDs[0] != 9001 {
		t.Errorf("event case refs = %+v", event.RefCaseIDs)
	}
	if len(event.RefEvidenceIDs) != 1 || event.RefEvidenceIDs[0] != 31 {
		t.Errorf("event evidence refs = %+v", event.RefEvidenceIDs)
	}

	if len(env.audit.recorded) != 1 || env.audit.recorded[0].Action != "delivery.issue.created" {
		t.Fatalf("audit = %+v", env.audit.recorded)
	}
	if env.audit.recorded[0].Metadata["source"] != domain.SourceAI {
		t.Errorf("audit metadata = %+v", env.audit.recorded[0].Metadata)
	}
}

func TestApplyDraftsDedupesOpenIssue(t *testing.T) {
	env := newIssueTestEnv(t)
	existing := env.seedOpenIssue(500, issueTestDraftKey("defect"))
	for _, target := range []struct {
		kind string
		id   int64
	}{
		{domain.RefKindCaseRun, 777},
		{domain.RefKindCaseNode, 9001},
		{domain.RefKindEvidence, 31},
	} {
		env.allow(target.kind, target.id, issueTestVersionID)
	}
	draft := issueTestValidAIDraft()
	draft.Actual = "服务端返回 500"

	issues, err := env.svc.ApplyDrafts(context.Background(), issueTestVersionID, 700, issueTestActor,
		[]domain.IssueDraft{draft})
	if err != nil {
		t.Fatalf("ApplyDrafts = %v", err)
	}
	if len(issues) != 1 || issues[0].ID != existing.ID {
		t.Fatalf("issues = %+v, want the existing issue %d", issues, existing.ID)
	}
	merged := issues[0]
	if merged.Status != domain.StatusOpen {
		t.Errorf("status = %q, want open", merged.Status)
	}
	if merged.RecurrenceCount != 1 {
		t.Errorf("recurrence_count = %d, want 1", merged.RecurrenceCount)
	}
	if merged.Revision != 4 {
		t.Errorf("revision = %d, want 4", merged.Revision)
	}
	if merged.Confidence != domain.ConfidenceHigh {
		t.Errorf("confidence = %q, want high (never downgraded)", merged.Confidence)
	}
	if merged.Actual != "服务端返回 500" {
		t.Errorf("actual = %q, want the newest actual", merged.Actual)
	}
	if merged.UpdatedBy != issueTestActor {
		t.Errorf("updated_by = %d", merged.UpdatedBy)
	}
	if len(env.store.refs[existing.ID]) != 4 {
		t.Errorf("refs = %+v, want the old ref plus the new occurrence refs", env.store.refs[existing.ID])
	}
	if env.store.creates != 0 || env.store.updates != 1 {
		t.Errorf("store calls = creates %d updates %d, want 0/1", env.store.creates, env.store.updates)
	}
	if len(env.events.published) != 1 || env.events.published[0].Type != "issue.updated" {
		t.Fatalf("events = %+v, want one issue.updated", env.events.published)
	}
	event := env.events.published[0]
	if event.Detail["issueId"] != fmt.Sprint(existing.ID) {
		t.Errorf("event issueId = %v", event.Detail["issueId"])
	}
	if event.Detail["revision"] != 4 || event.Detail["recurrenceCount"] != 1 {
		t.Errorf("event detail = %+v", event.Detail)
	}
	if len(env.audit.recorded) != 0 {
		t.Errorf("recurrence must not record delivery.issue.created: %+v", env.audit.recorded)
	}
}

func TestApplyDraftsRecurrenceKeepsTerminalStatus(t *testing.T) {
	for _, terminal := range []string{domain.StatusConfirmed, domain.StatusDismissed} {
		t.Run(terminal, func(t *testing.T) {
			env := newIssueTestEnv(t)
			existing := env.seedOpenIssue(500, issueTestDraftKey("defect"))
			existing.Status = terminal
			if terminal == domain.StatusDismissed {
				existing.DismissedReason = "历史驳回"
			}
			env.store.issues[existing.ID] = existing
			env.allow(domain.RefKindCaseRun, 777, issueTestVersionID)
			env.allow(domain.RefKindCaseNode, 9001, issueTestVersionID)
			env.allow(domain.RefKindEvidence, 31, issueTestVersionID)

			issues, err := env.svc.ApplyDrafts(context.Background(), issueTestVersionID, 700, issueTestActor,
				[]domain.IssueDraft{issueTestValidAIDraft()})
			if err != nil {
				t.Fatalf("ApplyDrafts = %v", err)
			}
			if len(issues) != 1 || issues[0].ID != existing.ID {
				t.Fatalf("issues = %+v", issues)
			}
			if issues[0].Status != terminal {
				t.Errorf("status = %q, want the terminal status %q", issues[0].Status, terminal)
			}
			if issues[0].RecurrenceCount != 1 {
				t.Errorf("recurrence_count = %d, want 1", issues[0].RecurrenceCount)
			}
			if len(env.events.published) != 1 || env.events.published[0].Type != "issue.updated" {
				t.Fatalf("events = %+v, want issue.updated", env.events.published)
			}
		})
	}
}

func TestApplyDraftsRejectsCrossVersionRef(t *testing.T) {
	env := newIssueTestEnv(t)
	env.allow(domain.RefKindCaseNode, 9001, 999) // belongs to another version
	env.allow(domain.RefKindCaseRun, 777, issueTestVersionID)
	env.allow(domain.RefKindEvidence, 31, issueTestVersionID)

	_, err := env.svc.ApplyDrafts(context.Background(), issueTestVersionID, 700, issueTestActor,
		[]domain.IssueDraft{issueTestValidAIDraft()})
	if err == nil {
		t.Fatal("ApplyDrafts accepted a cross-version reference")
	}
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("error type = %T (%v)", err, err)
	}
	if domainErr.Kind != domain.KindInvalid || domainErr.Reason != domain.ReasonIssueRefInvalid {
		t.Fatalf("error = %+v, want issue_ref_invalid", domainErr)
	}
	if env.store.creates != 0 || len(env.store.issues) != 0 || len(env.events.published) != 0 {
		t.Fatalf("rejected draft wrote state: issues=%d creates=%d events=%d",
			len(env.store.issues), env.store.creates, len(env.events.published))
	}
}

func TestApplyDraftsRollsBackOnStoreFailure(t *testing.T) {
	env := newIssueTestEnv(t)
	env.allow(domain.RefKindCaseRun, 777, issueTestVersionID)
	env.allow(domain.RefKindCaseNode, 9001, issueTestVersionID)
	env.allow(domain.RefKindEvidence, 31, issueTestVersionID)
	env.store.failAt = 2

	first := issueTestValidAIDraft()
	second := issueTestValidAIDraft()
	second.DedupeKey = issueTestDraftKeyN(21)
	second.Title = "第二个问题"
	_, err := env.svc.ApplyDrafts(context.Background(), issueTestVersionID, 700, issueTestActor,
		[]domain.IssueDraft{first, second})
	if err == nil {
		t.Fatal("ApplyDrafts swallowed a store failure")
	}
	if len(env.store.issues) != 0 {
		t.Errorf("rollback left issues behind: %+v", env.store.issues)
	}
	if len(env.events.published) != 0 || len(env.audit.recorded) != 0 {
		t.Errorf("failed transaction published events/audit: %+v %+v", env.events.published, env.audit.recorded)
	}
}

func TestApplyDraftsMergesIntoExplicitIssue(t *testing.T) {
	env := newIssueTestEnv(t)
	existing := env.seedOpenIssue(500, issueTestDraftKeyN(7))
	env.allow(domain.RefKindCaseRun, 777, issueTestVersionID)
	env.allow(domain.RefKindCaseNode, 9001, issueTestVersionID)
	env.allow(domain.RefKindEvidence, 31, issueTestVersionID)

	draft := issueTestValidAIDraft()
	draft.MergeIntoIssueID = issueTestPtr(existing.ID)
	issues, err := env.svc.ApplyDrafts(context.Background(), issueTestVersionID, 700, issueTestActor,
		[]domain.IssueDraft{draft})
	if err != nil {
		t.Fatalf("ApplyDrafts = %v", err)
	}
	if len(issues) != 1 || issues[0].ID != existing.ID {
		t.Fatalf("issues = %+v, want the explicitly merged issue %d", issues, existing.ID)
	}
	if issues[0].RecurrenceCount != 1 || issues[0].Revision != 4 {
		t.Errorf("merged issue = recurrence %d revision %d, want 1/4",
			issues[0].RecurrenceCount, issues[0].Revision)
	}
	if len(env.store.issues) != 1 || env.store.creates != 0 {
		t.Errorf("explicit merge created a second issue: %+v", env.store.issues)
	}

	t.Run("foreign merge target", func(t *testing.T) {
		env := newIssueTestEnv(t)
		env.allow(domain.RefKindCaseRun, 777, issueTestVersionID)
		env.allow(domain.RefKindCaseNode, 9001, issueTestVersionID)
		env.allow(domain.RefKindEvidence, 31, issueTestVersionID)
		draft := issueTestValidAIDraft()
		draft.MergeIntoIssueID = issueTestPtr(4242) // no such issue in this version
		_, err := env.svc.ApplyDrafts(context.Background(), issueTestVersionID, 700, issueTestActor,
			[]domain.IssueDraft{draft})
		if err == nil {
			t.Fatal("ApplyDrafts merged into a missing issue")
		}
		var domainErr *domain.Error
		if !errors.As(err, &domainErr) || domainErr.Reason != domain.ReasonIssueRefInvalid {
			t.Fatalf("error = %v, want issue_ref_invalid", err)
		}
		if len(env.store.issues) != 0 || len(env.events.published) != 0 {
			t.Fatalf("failed merge wrote state")
		}
	})
}

func TestApplyDraftsReplayIsIdempotent(t *testing.T) {
	env := newIssueTestEnv(t)
	draft := issueTestValidAIDraft()
	env.allow(domain.RefKindCaseRun, 777, issueTestVersionID)
	env.allow(domain.RefKindCaseNode, 9001, issueTestVersionID)
	env.allow(domain.RefKindEvidence, 31, issueTestVersionID)

	first, err := env.svc.ApplyDrafts(context.Background(), issueTestVersionID, 700, issueTestActor,
		[]domain.IssueDraft{draft})
	if err != nil {
		t.Fatalf("first ApplyDrafts = %v", err)
	}
	env.events.published = nil

	second, err := env.svc.ApplyDrafts(context.Background(), issueTestVersionID, 700, issueTestActor,
		[]domain.IssueDraft{draft})
	if err != nil {
		t.Fatalf("replayed ApplyDrafts = %v", err)
	}
	if len(second) != 1 || second[0].ID != first[0].ID {
		t.Fatalf("replay returned %+v, want the same issue", second)
	}
	if second[0].RecurrenceCount != 0 {
		t.Errorf("replay counted a recurrence: %d", second[0].RecurrenceCount)
	}
	if second[0].Revision != 1 {
		t.Errorf("replay bumped the revision: %d", second[0].Revision)
	}
	if env.store.updates != 0 || env.store.creates != 1 {
		t.Errorf("replay store calls = updates %d creates %d, want 0/1", env.store.updates, env.store.creates)
	}
	if len(env.events.published) != 0 {
		t.Errorf("replay published events: %+v", env.events.published)
	}
}

// TestApplyDraftsNewRefIsRecurrence pins the Lead replay ruling: a draft whose
// refs are all already linked is a replay (no bump, no event), while a genuine
// recurrence carrying at least one new ref increments recurrence_count and emits
// issue.updated.
func TestApplyDraftsNewRefIsRecurrence(t *testing.T) {
	env := newIssueTestEnv(t)
	draft := issueTestValidAIDraft()
	env.allow(domain.RefKindCaseRun, 777, issueTestVersionID)
	env.allow(domain.RefKindCaseNode, 9001, issueTestVersionID)
	env.allow(domain.RefKindEvidence, 31, issueTestVersionID)

	first, err := env.svc.ApplyDrafts(context.Background(), issueTestVersionID, 700, issueTestActor,
		[]domain.IssueDraft{draft})
	if err != nil {
		t.Fatalf("first ApplyDrafts = %v", err)
	}
	env.events.published = nil

	draft.EvidenceIDs = append(draft.EvidenceIDs, 32)
	env.allow(domain.RefKindEvidence, 32, issueTestVersionID)
	second, err := env.svc.ApplyDrafts(context.Background(), issueTestVersionID, 700, issueTestActor,
		[]domain.IssueDraft{draft})
	if err != nil {
		t.Fatalf("recurring ApplyDrafts = %v", err)
	}
	if len(second) != 1 || second[0].ID != first[0].ID {
		t.Fatalf("recurrence returned %+v, want the same issue", second)
	}
	if second[0].RecurrenceCount != 1 {
		t.Errorf("recurrence_count = %d, want 1", second[0].RecurrenceCount)
	}
	if second[0].Revision != 2 {
		t.Errorf("revision = %d, want 2", second[0].Revision)
	}
	if len(env.events.published) != 1 || env.events.published[0].Type != "issue.updated" {
		t.Fatalf("events = %+v, want one issue.updated", env.events.published)
	}
	if got := len(env.events.published[0].RefEvidenceIDs); got != 2 {
		t.Errorf("event evidence refs = %v, want both occurrences", env.events.published[0].RefEvidenceIDs)
	}
}

// TestApplyDraftsRetriesConcurrentDedupeConflict pins spec §6.9: when Create
// loses the unique-key race to a concurrent transaction that has since committed,
// the service re-reads in one fresh transaction and merges instead of returning a
// 409. The next-visible issue already carries an earlier occurrence ref.
func TestApplyDraftsRetriesConcurrentDedupeConflict(t *testing.T) {
	store := &issueRaceStore{issueFakeStore: newIssueFakeStore()}
	refs := newIssueFakeRefValidator()
	refs.allow(domain.RefKindCaseRun, 777, issueTestVersionID)
	refs.allow(domain.RefKindCaseNode, 9001, issueTestVersionID)
	refs.allow(domain.RefKindEvidence, 31, issueTestVersionID)
	events := &issueFakeEvents{}
	audit := &issueFakeAudit{}
	draft := issueTestValidAIDraft()
	store.raceDedupeKey = draft.DedupeKey
	svc := NewIssueService(IssueServiceDeps{
		Store:    store,
		Versions: newIssueFakeVersionReader(),
		Refs:     refs,
		IDs:      &issueTestIDs{next: 1000},
		Clock:    &issueTestClock{now: issueTestNow},
		Events:   events,
		Audit:    audit,
	})

	issues, err := svc.ApplyDrafts(context.Background(), issueTestVersionID, 700, issueTestActor,
		[]domain.IssueDraft{draft})
	if err != nil {
		t.Fatalf("ApplyDrafts = %v", err)
	}
	if !store.raced {
		t.Fatal("the fake store never reported the concurrent dedupe conflict")
	}
	if len(issues) != 1 || issues[0].ID != 9001 {
		t.Fatalf("issues = %+v, want the concurrently created issue 9001", issues)
	}
	merged := issues[0]
	if merged.RecurrenceCount != 1 || merged.Revision != 4 {
		t.Errorf("merged = recurrence %d revision %d, want 1/4",
			merged.RecurrenceCount, merged.Revision)
	}
	if store.inTx != 2 {
		t.Errorf("transactions = %d, want the failed attempt plus one retry", store.inTx)
	}
	refsAfter := domain.NormalizeIssueRefs(store.refs[merged.ID])
	if len(refsAfter) != 4 {
		t.Errorf("refs = %+v, want the earlier ref plus the three occurrence refs", refsAfter)
	}
	if len(events.published) != 1 || events.published[0].Type != "issue.updated" {
		t.Fatalf("events = %+v, want exactly one issue.updated", events.published)
	}
	if events.published[0].Detail["recurrenceCount"] != 1 {
		t.Errorf("event detail = %+v", events.published[0].Detail)
	}
	if len(audit.recorded) != 0 {
		t.Errorf("recurrence recorded audit entries: %+v", audit.recorded)
	}
}

// TestApplyDraftsCopiesRerunSuggestion pins the additive IssueDraft field: the
// failure-analysis rerun suggestion is persisted on the materialized issue.
func TestApplyDraftsCopiesRerunSuggestion(t *testing.T) {
	env := newIssueTestEnv(t)
	env.allow(domain.RefKindCaseRun, 777, issueTestVersionID)
	env.allow(domain.RefKindCaseNode, 9001, issueTestVersionID)
	env.allow(domain.RefKindEvidence, 31, issueTestVersionID)
	draft := issueTestValidAIDraft()
	draft.RerunSuggestion = "重新执行并用 network 面板确认 500 的来源"

	issues, err := env.svc.ApplyDrafts(context.Background(), issueTestVersionID, 700, issueTestActor,
		[]domain.IssueDraft{draft})
	if err != nil {
		t.Fatalf("ApplyDrafts = %v", err)
	}
	if len(issues) != 1 || issues[0].RerunSuggestion != draft.RerunSuggestion {
		t.Fatalf("rerun suggestion = %q, want %q", issues[0].RerunSuggestion, draft.RerunSuggestion)
	}
	if got := env.store.issues[issues[0].ID].RerunSuggestion; got != draft.RerunSuggestion {
		t.Errorf("stored rerun suggestion = %q", got)
	}
}

func TestApplyDraftsEmptyIsNoOp(t *testing.T) {
	env := newIssueTestEnv(t)
	issues, err := env.svc.ApplyDrafts(context.Background(), issueTestVersionID, 700, issueTestActor, nil)
	if err != nil {
		t.Fatalf("ApplyDrafts(nil) = %v", err)
	}
	if len(issues) != 0 || env.store.inTx != 0 || len(env.events.published) != 0 {
		t.Fatalf("empty draft list wrote state: issues=%d tx=%d events=%d",
			len(issues), env.store.inTx, len(env.events.published))
	}
}

// --- Confirm / Dismiss ------------------------------------------------------

func TestConfirmIssue(t *testing.T) {
	env := newIssueTestEnv(t)
	env.seedOpenIssue(500, issueTestDraftKey("defect"))

	confirmed, err := env.svc.Confirm(context.Background(), issueTestVersionID, 500, issueTestActor, 3, "确认是缺陷")
	if err != nil {
		t.Fatalf("Confirm = %v", err)
	}
	if confirmed.Status != domain.StatusConfirmed {
		t.Errorf("status = %q, want confirmed", confirmed.Status)
	}
	if confirmed.Revision != 4 {
		t.Errorf("revision = %d, want 4", confirmed.Revision)
	}
	if confirmed.ConfirmedBy == nil || *confirmed.ConfirmedBy != issueTestActor {
		t.Errorf("confirmed_by = %v", confirmed.ConfirmedBy)
	}
	if confirmed.ConfirmedAt == nil || !confirmed.ConfirmedAt.Equal(issueTestNow) {
		t.Errorf("confirmed_at = %v", confirmed.ConfirmedAt)
	}
	if confirmed.UpdatedBy != issueTestActor || !confirmed.UpdatedAt.Equal(issueTestNow) {
		t.Errorf("updated = (%d, %s)", confirmed.UpdatedBy, confirmed.UpdatedAt)
	}
	if env.store.updates != 1 {
		t.Errorf("updates = %d, want 1 CAS write", env.store.updates)
	}
	if len(env.events.published) != 1 || env.events.published[0].Type != "issue.updated" {
		t.Fatalf("events = %+v", env.events.published)
	}
	if env.events.published[0].Detail["status"] != domain.StatusConfirmed ||
		env.events.published[0].Detail["revision"] != 4 {
		t.Errorf("event detail = %+v", env.events.published[0].Detail)
	}
	if len(env.events.published[0].RefCaseIDs) != 1 || env.events.published[0].RefCaseIDs[0] != 8000 {
		t.Errorf("event case refs = %+v, want the issue's case_node ref", env.events.published[0].RefCaseIDs)
	}
	if len(env.audit.recorded) != 1 || env.audit.recorded[0].Action != "delivery.issue.confirmed" {
		t.Fatalf("audit = %+v", env.audit.recorded)
	}
	if env.audit.recorded[0].TargetID != "500" || env.audit.recorded[0].ActorID != issueTestActor {
		t.Errorf("audit = %+v", env.audit.recorded[0])
	}
	// §11.2 allows a reason only for dismissals; a confirmation comment is not
	// an audit field and must not be stored as the free-text reason.
	if _, recorded := env.audit.recorded[0].Metadata["reason"]; recorded {
		t.Errorf("confirmation comment was recorded as the audit reason: %+v", env.audit.recorded[0].Metadata)
	}
	if fmt.Sprint(env.audit.recorded[0].Metadata) != "map[]" {
		t.Errorf("confirmation audit metadata = %+v, want no fields", env.audit.recorded[0].Metadata)
	}
}

func TestConfirmIssueRejectsInvalidState(t *testing.T) {
	terminal := []string{domain.StatusConfirmed, domain.StatusDismissed}
	for _, status := range terminal {
		t.Run(status, func(t *testing.T) {
			env := newIssueTestEnv(t)
			issue := env.seedOpenIssue(500, issueTestDraftKey("defect"))
			issue.Status = status
			env.seed(issue)
			_, err := env.svc.Confirm(context.Background(), issueTestVersionID, 500, issueTestActor, 3, "")
			issueTestStateConflict(t, err)
			if env.store.updates != 0 || len(env.events.published) != 0 {
				t.Errorf("terminal confirm wrote state")
			}
		})
	}
	t.Run("revision mismatch", func(t *testing.T) {
		env := newIssueTestEnv(t)
		env.seedOpenIssue(500, issueTestDraftKey("defect"))
		_, err := env.svc.Confirm(context.Background(), issueTestVersionID, 500, issueTestActor, 2, "")
		issueTestStateConflict(t, err)
		if env.store.updates != 0 || len(env.events.published) != 0 {
			t.Errorf("stale confirm wrote state")
		}
	})
	t.Run("missing issue", func(t *testing.T) {
		env := newIssueTestEnv(t)
		_, err := env.svc.Confirm(context.Background(), issueTestVersionID, 404, issueTestActor, 1, "")
		issueTestNotFound(t, err)
	})
}

func TestDismissIssue(t *testing.T) {
	env := newIssueTestEnv(t)
	env.seedOpenIssue(500, issueTestDraftKey("defect"))

	dismissed, err := env.svc.Dismiss(context.Background(), issueTestVersionID, 500, issueTestActor, 3, "  环境问题，非应用缺陷  ")
	if err != nil {
		t.Fatalf("Dismiss = %v", err)
	}
	if dismissed.Status != domain.StatusDismissed {
		t.Errorf("status = %q, want dismissed", dismissed.Status)
	}
	if dismissed.DismissedReason != "环境问题，非应用缺陷" {
		t.Errorf("dismissed_reason = %q", dismissed.DismissedReason)
	}
	if dismissed.Revision != 4 {
		t.Errorf("revision = %d, want 4", dismissed.Revision)
	}
	if dismissed.DismissedBy == nil || *dismissed.DismissedBy != issueTestActor {
		t.Errorf("dismissed_by = %v", dismissed.DismissedBy)
	}
	if dismissed.DismissedAt == nil || !dismissed.DismissedAt.Equal(issueTestNow) {
		t.Errorf("dismissed_at = %v", dismissed.DismissedAt)
	}
	if len(env.events.published) != 1 || env.events.published[0].Detail["status"] != domain.StatusDismissed {
		t.Fatalf("events = %+v", env.events.published)
	}
	if len(env.events.published[0].RefCaseIDs) != 1 || env.events.published[0].RefCaseIDs[0] != 8000 {
		t.Errorf("event case refs = %+v, want the issue's case_node ref", env.events.published[0].RefCaseIDs)
	}
	if len(env.audit.recorded) != 1 || env.audit.recorded[0].Action != "delivery.issue.dismissed" {
		t.Fatalf("audit = %+v", env.audit.recorded)
	}
	if env.audit.recorded[0].Metadata["reason"] != "环境问题，非应用缺陷" {
		t.Errorf("audit metadata = %+v", env.audit.recorded[0].Metadata)
	}
}

func TestDismissIssueRequiresReason(t *testing.T) {
	for _, reason := range []string{"", "   "} {
		env := newIssueTestEnv(t)
		env.seedOpenIssue(500, issueTestDraftKey("defect"))
		_, err := env.svc.Dismiss(context.Background(), issueTestVersionID, 500, issueTestActor, 3, reason)
		if err == nil {
			t.Fatalf("Dismiss(%q) succeeded without a reason", reason)
		}
		var domainErr *domain.Error
		if !errors.As(err, &domainErr) {
			t.Fatalf("error type = %T (%v)", err, err)
		}
		if domainErr.Kind != domain.KindInvalid || domainErr.Reason != domain.ReasonIssueDismissReasonRequired {
			t.Fatalf("error = %+v, want issue_dismiss_reason_required", domainErr)
		}
		if env.store.updates != 0 || len(env.events.published) != 0 {
			t.Errorf("reasonless dismiss wrote state")
		}
	}
}

func TestDismissIssueRejectsRevisionMismatch(t *testing.T) {
	env := newIssueTestEnv(t)
	env.seedOpenIssue(500, issueTestDraftKey("defect"))
	_, err := env.svc.Dismiss(context.Background(), issueTestVersionID, 500, issueTestActor, 9, "重复")
	issueTestStateConflict(t, err)
}

// --- Get / List / refs ------------------------------------------------------

func TestGetIssue(t *testing.T) {
	env := newIssueTestEnv(t)
	seeded := env.seedOpenIssue(500, issueTestDraftKey("defect"))

	issue, err := env.svc.Get(context.Background(), issueTestVersionID, 500)
	if err != nil {
		t.Fatalf("Get = %v", err)
	}
	if issue.ID != seeded.ID || issue.Status != domain.StatusOpen {
		t.Errorf("issue = %+v", issue)
	}
	if _, err := env.svc.Get(context.Background(), issueTestVersionID, 404); err == nil {
		t.Fatal("Get(missing) returned no error")
	} else {
		issueTestNotFound(t, err)
	}
	if _, err := env.svc.Get(context.Background(), 2, 500); err == nil {
		t.Fatal("Get(foreign version) returned no error")
	} else {
		issueTestNotFound(t, err)
	}
}

func TestListIssuesFilters(t *testing.T) {
	env := newIssueTestEnv(t)
	blockerID := int64(701)
	env.seed(domain.Issue{
		ID: 701, VersionID: issueTestVersionID, BatchID: issueTestPtr(601), CaseRunID: issueTestPtr(801),
		CaseNodeID: &blockerID, Source: domain.SourceAI, Category: domain.CategoryDefect,
		Severity: domain.SeverityBlocker, Status: domain.StatusOpen, Confidence: domain.ConfidenceHigh,
		Title: "登录页崩溃", Revision: 1,
	})
	env.seed(domain.Issue{
		ID: 702, VersionID: issueTestVersionID, BatchID: issueTestPtr(601),
		Source: domain.SourceAI, Category: domain.CategoryDefect, Severity: domain.SeverityMajor,
		Status: domain.StatusOpen, Confidence: domain.ConfidenceMedium, Title: "登录按钮无响应", Revision: 1,
	})
	env.seed(domain.Issue{
		ID: 703, VersionID: issueTestVersionID, Source: domain.SourceHuman,
		Category: domain.CategoryOther, Severity: domain.SeverityMinor,
		Status: domain.StatusConfirmed, Confidence: domain.ConfidenceMedium, Title: "文案问题", Revision: 2,
	})
	env.seed(domain.Issue{
		ID: 704, VersionID: issueTestVersionID, Source: domain.SourceAI, Category: domain.CategoryDefect,
		Severity: domain.SeverityMajor, Status: domain.StatusDismissed, Confidence: domain.ConfidenceLow,
		Title: "已驳回问题", Revision: 2,
	})
	env.seed(domain.Issue{
		ID: 705, VersionID: 2, Source: domain.SourceAI, Category: domain.CategoryDefect,
		Severity: domain.SeverityBlocker, Status: domain.StatusOpen, Confidence: domain.ConfidenceHigh,
		Title: "其他版本问题", Revision: 1,
	})

	cases := []struct {
		name   string
		filter IssueFilter
		want   []int64
	}{
		{"status", IssueFilter{Status: domain.StatusOpen, Limit: 10}, []int64{701, 702}},
		{"severity", IssueFilter{Severity: domain.SeverityMajor, Limit: 10}, []int64{704, 702}},
		{"category", IssueFilter{Category: domain.CategoryOther, Limit: 10}, []int64{703}},
		{"batch", IssueFilter{BatchID: issueTestPtr(601), Limit: 10}, []int64{701, 702}},
		{"case run", IssueFilter{CaseRunID: issueTestPtr(801), Limit: 10}, []int64{701}},
		{"query", IssueFilter{Query: "登录", Limit: 10}, []int64{701, 702}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issues, more, err := env.svc.List(context.Background(), issueTestVersionID, tc.filter)
			if err != nil {
				t.Fatalf("List = %v", err)
			}
			if more {
				t.Errorf("hasMore = true for %d results", len(issues))
			}
			got := make([]int64, len(issues))
			for i, issue := range issues {
				got[i] = issue.ID
			}
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Fatalf("ids = %v, want %v (severity weight then id desc)", got, tc.want)
			}
		})
	}
}

func TestListIssuesPagination(t *testing.T) {
	env := newIssueTestEnv(t)
	for id := int64(701); id <= 706; id++ {
		severity := domain.SeverityMajor
		if id == 701 || id == 705 {
			severity = domain.SeverityBlocker
		}
		env.seed(domain.Issue{
			ID: id, VersionID: issueTestVersionID, Source: domain.SourceAI,
			Category: domain.CategoryDefect, Severity: severity, Status: domain.StatusOpen,
			Confidence: domain.ConfidenceHigh, Title: fmt.Sprintf("问题 %d", id), Revision: 1,
		})
	}

	first, more, err := env.svc.List(context.Background(), issueTestVersionID, IssueFilter{Limit: 2})
	if err != nil {
		t.Fatalf("List first page = %v", err)
	}
	if !more || len(first) != 2 {
		t.Fatalf("first page = %d issues more=%v, want 2/true", len(first), more)
	}
	wantFirst := []int64{705, 701}
	for i, want := range wantFirst {
		if first[i].ID != want {
			t.Fatalf("first page ids = %d %d, want %v", first[0].ID, first[1].ID, wantFirst)
		}
	}

	second, more, err := env.svc.List(context.Background(), issueTestVersionID,
		IssueFilter{Limit: 2, Cursor: first[len(first)-1].ID})
	if err != nil {
		t.Fatalf("List second page = %v", err)
	}
	if !more || len(second) != 2 {
		t.Fatalf("second page = %d issues more=%v, want 2/true", len(second), more)
	}
	if second[0].ID != 706 || second[1].ID != 704 {
		t.Fatalf("second page ids = %d %d, want [706 704]", second[0].ID, second[1].ID)
	}

	last, more, err := env.svc.List(context.Background(), issueTestVersionID,
		IssueFilter{Limit: 2, Cursor: second[len(second)-1].ID})
	if err != nil {
		t.Fatalf("List last page = %v", err)
	}
	if more || len(last) != 2 || last[0].ID != 703 || last[1].ID != 702 {
		t.Fatalf("last page = %+v more=%v", last, more)
	}
}

func TestListIssuesRejectsInvalidFilter(t *testing.T) {
	env := newIssueTestEnv(t)
	cases := []struct {
		name   string
		filter IssueFilter
		field  string
	}{
		{"bad status", IssueFilter{Status: "bogus", Limit: 10}, "status"},
		{"bad severity", IssueFilter{Severity: "urgent", Limit: 10}, "severity"},
		{"bad category", IssueFilter{Category: "bug", Limit: 10}, "category"},
		{"long query", IssueFilter{Query: strings.Repeat("x", 101), Limit: 10}, "q"},
		{"negative cursor", IssueFilter{Cursor: -1, Limit: 10}, "cursor"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := env.svc.List(context.Background(), issueTestVersionID, tc.filter)
			var domainErr *domain.Error
			if !errors.As(err, &domainErr) || domainErr.Kind != domain.KindInvalid || domainErr.Field != tc.field {
				t.Fatalf("error = %v, want invalid field %q", err, tc.field)
			}
		})
	}
}

func TestListRefs(t *testing.T) {
	env := newIssueTestEnv(t)
	env.allow(domain.RefKindCaseNode, 9001, issueTestVersionID)
	env.allow(domain.RefKindEvidence, 31, issueTestVersionID)
	issue, err := env.svc.CreateHuman(context.Background(), issueTestVersionID, issueTestActor,
		CreateIssueCommand{
			Title:      "登录失败",
			Severity:   domain.SeverityMajor,
			CaseNodeID: issueTestPtr(9001),
		})
	if err != nil {
		t.Fatalf("CreateHuman = %v", err)
	}
	refs, err := env.svc.ListRefs(context.Background(), issueTestVersionID, issue.ID)
	if err != nil {
		t.Fatalf("ListRefs = %v", err)
	}
	if len(refs) != 1 || refs[0].RefKind != domain.RefKindCaseNode || refs[0].RefID != "9001" {
		t.Fatalf("refs = %+v", refs)
	}
	if _, err := env.svc.ListRefs(context.Background(), issueTestVersionID, 404); err == nil {
		t.Fatal("ListRefs(missing) returned no error")
	} else {
		issueTestNotFound(t, err)
	}
}

func TestIssueServiceSatisfiesFrozenInterface(t *testing.T) {
	var _ IssueService = (*IssueManager)(nil)
	var _ IssueRefReader = (*IssueManager)(nil)
}

func TestIssueServiceRequiresStore(t *testing.T) {
	svc := NewIssueService(IssueServiceDeps{})
	if _, err := svc.Get(context.Background(), 1, 2); err == nil {
		t.Fatal("Get without a store returned no error")
	}
}

func TestCreateHumanIssueRequiresRefValidator(t *testing.T) {
	store := newIssueFakeStore()
	svc := NewIssueService(IssueServiceDeps{
		Store:    store,
		Versions: newIssueFakeVersionReader(),
		IDs:      &issueTestIDs{next: 1000},
		Clock:    &issueTestClock{now: issueTestNow},
	})
	_, err := svc.CreateHuman(context.Background(), issueTestVersionID, issueTestActor, CreateIssueCommand{
		Title:      "登录失败",
		Severity:   domain.SeverityMajor,
		CaseNodeID: issueTestPtr(9001),
	})
	if err == nil {
		t.Fatal("CreateHuman accepted an owned ref without a ref validator")
	}
	if store.creates != 0 {
		t.Errorf("ref validation failure still created %d issues", store.creates)
	}

	// An issue without refs needs no cross-context validation.
	if _, err := svc.CreateHuman(context.Background(), issueTestVersionID, issueTestActor, CreateIssueCommand{
		Title:    "无引用问题",
		Severity: domain.SeverityMinor,
	}); err != nil {
		t.Fatalf("CreateHuman without refs = %v", err)
	}
}

func TestIssueServiceWithoutIDGeneratorFailsWrites(t *testing.T) {
	store := newIssueFakeStore()
	refs := newIssueFakeRefValidator()
	svc := NewIssueService(IssueServiceDeps{
		Store:    store,
		Versions: newIssueFakeVersionReader(),
		Refs:     refs,
		Clock:    &issueTestClock{now: issueTestNow},
	})
	_, err := svc.CreateHuman(context.Background(), issueTestVersionID, issueTestActor, CreateIssueCommand{
		Title:    "缺少 ID 生成器",
		Severity: domain.SeverityMajor,
	})
	if err == nil {
		t.Fatal("CreateHuman succeeded without an injected id generator")
	}
	if store.creates != 0 || len(store.issues) != 0 {
		t.Fatalf("write without ids left state behind: %+v", store.issues)
	}
}

// TestIssueEventRefsCapsTotal pins spec §8.2: the case and evidence refs of one
// event share a single total cap of 20, not a cap per kind.
func TestIssueEventRefsCapsTotal(t *testing.T) {
	refs := make([]domain.IssueRef, 0, 30)
	for id := int64(1); id <= 15; id++ {
		refs = append(refs, domain.IssueRef{RefKind: domain.RefKindCaseNode, RefID: strconv.FormatInt(id, 10)})
	}
	for id := int64(1); id <= 10; id++ {
		refs = append(refs, domain.IssueRef{RefKind: domain.RefKindEvidence, RefID: strconv.FormatInt(id, 10)})
	}
	caseIDs, evidenceIDs := issueEventRefs(domain.NormalizeIssueRefs(refs))
	if len(caseIDs)+len(evidenceIDs) != issueMaxEventRefs {
		t.Fatalf("event refs = %d case + %d evidence, want a total of %d",
			len(caseIDs), len(evidenceIDs), issueMaxEventRefs)
	}
	if len(caseIDs) != 15 || len(evidenceIDs) != 5 {
		t.Errorf("refs = %d case + %d evidence, want 15/5 (case_node sorts first)",
			len(caseIDs), len(evidenceIDs))
	}
}

// issueTestStateConflict asserts err is the 409 issue_state_conflict.
func issueTestStateConflict(t *testing.T, err error) {
	t.Helper()
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("error type = %T (%v)", err, err)
	}
	if domainErr.Kind != domain.KindStateConflict || domainErr.Reason != domain.ReasonIssueStateConflict {
		t.Fatalf("error = %+v, want issue_state_conflict", domainErr)
	}
}

// issueTestNotFound asserts err is the 404 issue_not_found.
func issueTestNotFound(t *testing.T, err error) {
	t.Helper()
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("error type = %T (%v)", err, err)
	}
	if domainErr.Kind != domain.KindNotFound || domainErr.Reason != "issue_not_found" {
		t.Fatalf("error = %+v, want issue_not_found", domainErr)
	}
}
