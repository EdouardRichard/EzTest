package executionapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// --- fakes -----------------------------------------------------------------

// fakeFixtureStore is the append-only session_fixture fact store stub. It
// mirrors the production contract: Append only inserts, List returns one reuse
// key's facts in append order (id ascending), and there is no Update/Delete.
type fakeFixtureStore struct {
	appended  []execution.SessionFixture
	appendErr error
	listErr   error
}

func (f *fakeFixtureStore) AppendSessionFixture(_ context.Context, fixture execution.SessionFixture) (execution.SessionFixture, error) {
	if f.appendErr != nil {
		return execution.SessionFixture{}, f.appendErr
	}
	if err := fixture.Validate(); err != nil {
		return execution.SessionFixture{}, err
	}
	fixture.ID = int64(len(f.appended) + 1)
	f.appended = append(f.appended, fixture)
	return fixture, nil
}

func (f *fakeFixtureStore) ListSessionFixtures(_ context.Context, versionID, sutID, environmentID int64, role string) ([]execution.SessionFixture, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]execution.SessionFixture, 0)
	for _, fixture := range f.appended {
		if fixture.VersionID == versionID && fixture.SUTID == sutID &&
			fixture.EnvironmentID == environmentID && fixture.Role == role {
			out = append(out, fixture)
		}
	}
	return out, nil
}

// fakeFixtureContent is the content-ledger stub: PutFixture stores bytes under
// their sha256 (content addressing), Open reads them back. openErr injects a
// missing/corrupt blob so a test can prove the download path fails observably.
type fakeFixtureContent struct {
	blobs     map[string][]byte
	puts      []fixtureContentPut
	putErr    error
	openErr   error
	openCalls []string
}

type fixtureContentPut struct {
	fixtureID int64
	data      []byte
}

func (f *fakeFixtureContent) PutFixture(_ context.Context, fixtureID int64, _ string, data []byte) (ContentRef, error) {
	if f.putErr != nil {
		return ContentRef{}, f.putErr
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	if f.blobs == nil {
		f.blobs = map[string][]byte{}
	}
	f.blobs[sha] = append([]byte(nil), data...)
	f.puts = append(f.puts, fixtureContentPut{fixtureID: fixtureID, data: append([]byte(nil), data...)})
	return ContentRef{SHA256: sha, SizeBytes: int64(len(data))}, nil
}

func (f *fakeFixtureContent) Open(_ context.Context, sha string) ([]byte, error) {
	f.openCalls = append(f.openCalls, sha)
	if f.openErr != nil {
		return nil, f.openErr
	}
	data, ok := f.blobs[sha]
	if !ok {
		return nil, execution.ErrNotFound
	}
	return data, nil
}

// fakeFixtureSession is the per-(SUT,role) session port stub. Every method the
// tested path must NOT call panics with a named message (method discipline:
// a stub must refuse to answer a question it was never asked), so a silent
// fallback to a stale state or a skipped CLI load is a loud test failure.
type fakeFixtureSession struct {
	readFn  func(scope tools.Scope, sutID int64, role, path string) ([]byte, error)
	writeFn func(scope tools.Scope, sutID int64, role, path string, data []byte) error
	loadFn  func(scope tools.Scope, sutID int64, role, path string) error
	linkFn  func(scope tools.Scope, sutID int64, role string, fixtureID int64) error

	reads  []fixtureSessionCall
	writes []fixtureSessionWrite
	loads  []fixtureSessionCall
	links  []fixtureSessionLink
}

type fixtureSessionCall struct {
	caseRunID int64
	sutID     int64
	role      string
	path      string
}

type fixtureSessionWrite struct {
	fixtureSessionCall
	data []byte
}

type fixtureSessionLink struct {
	caseRunID int64
	sutID     int64
	role      string
	fixtureID int64
}

func (f *fakeFixtureSession) ReadSessionFile(_ context.Context, scope tools.Scope, sutID int64, role, path string) ([]byte, error) {
	f.reads = append(f.reads, fixtureSessionCall{caseRunID: scope.CaseRunID, sutID: sutID, role: role, path: path})
	if f.readFn == nil {
		panic("fakeFixtureSession: unexpected ReadSessionFile call")
	}
	return f.readFn(scope, sutID, role, path)
}

func (f *fakeFixtureSession) WriteSessionFile(_ context.Context, scope tools.Scope, sutID int64, role, path string, data []byte) error {
	f.writes = append(f.writes, fixtureSessionWrite{
		fixtureSessionCall: fixtureSessionCall{caseRunID: scope.CaseRunID, sutID: sutID, role: role, path: path},
		data:               append([]byte(nil), data...),
	})
	if f.writeFn == nil {
		panic("fakeFixtureSession: unexpected WriteSessionFile call")
	}
	return f.writeFn(scope, sutID, role, path, data)
}

func (f *fakeFixtureSession) LoadBrowserState(_ context.Context, scope tools.Scope, sutID int64, role, path string) error {
	f.loads = append(f.loads, fixtureSessionCall{caseRunID: scope.CaseRunID, sutID: sutID, role: role, path: path})
	if f.loadFn == nil {
		panic("fakeFixtureSession: unexpected LoadBrowserState call")
	}
	return f.loadFn(scope, sutID, role, path)
}

func (f *fakeFixtureSession) LinkSessionFixture(_ context.Context, scope tools.Scope, sutID int64, role string, fixtureID int64) error {
	f.links = append(f.links, fixtureSessionLink{caseRunID: scope.CaseRunID, sutID: sutID, role: role, fixtureID: fixtureID})
	if f.linkFn == nil {
		panic("fakeFixtureSession: unexpected LinkSessionFixture call")
	}
	return f.linkFn(scope, sutID, role, fixtureID)
}

func fixtureTestScope(caseRunID int64) tools.Scope {
	return tools.Scope{
		VersionID: 100, WaveID: 50, CaseRunID: caseRunID, IntentID: 1,
		DispatchEpoch: 1, DispatchToken: "token-1", AgentRunID: 42,
	}
}

func fixtureTestCatalog() *fakeSUTCatalog {
	return &fakeSUTCatalog{suts: []ResolvedSUT{
		{SUTID: 9001, EnvironmentID: 7001, Name: "订单系统", BaseURL: "https://orders.example.com", Status: "approved", PageIDs: []int64{101}},
		{SUTID: 9002, EnvironmentID: 7001, Name: "库存系统", BaseURL: "https://stock.example.com", Status: "approved", PageIDs: []int64{102}},
	}}
}

func newFixtureService(store *fakeFixtureStore, content *fakeFixtureContent, session *fakeFixtureSession, pub *corePublisher) *SessionFixtureService {
	return NewSessionFixtureService(SessionFixtureDeps{
		Fixtures: store,
		Catalog:  fixtureTestCatalog(),
		Content:  content,
		Session:  session,
		Events:   pub,
		Clock:    fakeResolutionClock{at: time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)},
	})
}

func fixtureShaOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// --- tests -----------------------------------------------------------------

// TestFixtureCrossCaseReuse pins SC-F5: the agent saves a login state in one
// case_run (CLI state-save -> fixture.upload), and a DIFFERENT case_run of the
// same version reuses it (fixture.download -> CLI state-load). The reuse key is
// (version, sut, environment, role); the fact is append-only; the storage state
// is content-addressed; case_run_sut_context records the fixture reference; and
// session.saved is published on save.
func TestFixtureCrossCaseReuse(t *testing.T) {
	ctx := context.Background()
	store := &fakeFixtureStore{}
	content := &fakeFixtureContent{}
	session := &fakeFixtureSession{}
	pub := &corePublisher{}
	svc := newFixtureService(store, content, session, pub)

	stateA := []byte(`{"cookies":[{"name":"sid","value":"a"}],"origins":[]}`)
	session.readFn = func(_ tools.Scope, sutID int64, role, path string) ([]byte, error) {
		if sutID != 9001 || role != "admin" || path != "state.json" {
			t.Fatalf("read selector = (%d,%q,%q), want (9001,admin,state.json)", sutID, role, path)
		}
		return stateA, nil
	}
	session.linkFn = func(tools.Scope, int64, string, int64) error { return nil }

	saved, err := svc.UploadFixture(ctx, fixtureTestScope(500), tools.FixtureUploadInput{
		SUTID: 9001, AccountRole: "admin", Path: "state.json",
	})
	if err != nil {
		t.Fatalf("UploadFixture: %v", err)
	}
	savedMap, ok := saved.(map[string]any)
	if !ok {
		t.Fatalf("upload result type %T, want map", saved)
	}
	if len(store.appended) != 1 {
		t.Fatalf("appended facts = %d, want 1", len(store.appended))
	}
	fact := store.appended[0]
	if fact.VersionID != 100 || fact.SUTID != 9001 || fact.EnvironmentID != 7001 || fact.Role != "admin" {
		t.Fatalf("reuse key = (%d,%d,%d,%q), want (100,9001,7001,admin)", fact.VersionID, fact.SUTID, fact.EnvironmentID, fact.Role)
	}
	if fact.StorageStateRef != fixtureShaOf(stateA) {
		t.Fatalf("storage_state_ref = %q, want sha256 of the saved state", fact.StorageStateRef)
	}
	if fact.CreatedBy != 42 {
		t.Fatalf("created_by = %d, want the agent run id 42", fact.CreatedBy)
	}
	if len(content.puts) != 1 || content.puts[0].fixtureID != fact.ID || string(content.puts[0].data) != string(stateA) {
		t.Fatalf("content puts = %+v, want the state bytes under the fixture id", content.puts)
	}
	if len(session.links) != 1 || session.links[0].caseRunID != 500 || session.links[0].fixtureID != fact.ID {
		t.Fatalf("reuse links = %+v, want case_run 500 -> fixture %d", session.links, fact.ID)
	}
	if savedMap["fixture_id"] != "1" || savedMap["storage_state_ref"] != fixtureShaOf(stateA) ||
		savedMap["sut_id"] != "9001" || savedMap["environment_id"] != "7001" || savedMap["role"] != "admin" {
		t.Fatalf("upload result = %+v", savedMap)
	}
	if pub.count(EventSessionSaved) != 1 {
		t.Fatalf("session.saved events = %d, want 1", pub.count(EventSessionSaved))
	}
	payload := pub.payloads(EventSessionSaved)[0]
	if payload["fixture_id"] != "1" || payload["sut_id"] != "9001" || payload["role"] != "admin" ||
		payload["storage_state_ref"] != fixtureShaOf(stateA) || payload["case_run_id"] != "500" {
		t.Fatalf("session.saved payload = %+v", payload)
	}

	// The NEXT case_run (different run id, same version/sut/environment/role)
	// downloads the fixture and injects it via CLI state-load.
	session.writeFn = func(_ tools.Scope, sutID int64, role, path string, data []byte) error {
		if sutID != 9001 || role != "admin" {
			t.Fatalf("write selector = (%d,%q), want (9001,admin)", sutID, role)
		}
		if path != DefaultFixtureDownloadPath {
			t.Fatalf("download path = %q, want the default %q (must not clobber the state-save file)", path, DefaultFixtureDownloadPath)
		}
		if string(data) != string(stateA) {
			t.Fatalf("injected bytes = %q, want the saved state", data)
		}
		return nil
	}
	session.loadFn = func(tools.Scope, int64, string, string) error { return nil }

	loaded, err := svc.DownloadFixture(ctx, fixtureTestScope(501), tools.FixtureDownloadInput{
		SUTID: 9001, AccountRole: "admin",
	})
	if err != nil {
		t.Fatalf("DownloadFixture: %v", err)
	}
	loadedMap, ok := loaded.(map[string]any)
	if !ok {
		t.Fatalf("download result type %T, want map", loaded)
	}
	if loadedMap["loaded"] != true || loadedMap["fixture_id"] != "1" || loadedMap["storage_state_ref"] != fixtureShaOf(stateA) {
		t.Fatalf("download result = %+v", loadedMap)
	}
	if len(session.loads) != 1 || session.loads[0].caseRunID != 501 {
		t.Fatalf("CLI state-load calls = %+v, want one in case_run 501", session.loads)
	}
	if len(session.links) != 2 || session.links[1].caseRunID != 501 || session.links[1].fixtureID != fact.ID {
		t.Fatalf("reuse links = %+v, want case_run 501 -> fixture %d", session.links, fact.ID)
	}
	// Download must not publish session.saved (only a save does).
	if pub.count(EventSessionSaved) != 1 {
		t.Fatalf("session.saved events after download = %d, want still 1", pub.count(EventSessionSaved))
	}

	// Reuse-key isolation: a different role or a different SUT has no fixture.
	if _, err := svc.DownloadFixture(ctx, fixtureTestScope(502), tools.FixtureDownloadInput{
		SUTID: 9001, AccountRole: "viewer",
	}); err == nil {
		t.Fatal("download with a different role must be not_found, got success")
	} else {
		requireAPIError(t, err, "not_found")
	}
	if _, err := svc.DownloadFixture(ctx, fixtureTestScope(502), tools.FixtureDownloadInput{
		SUTID: 9002, AccountRole: "admin",
	}); err == nil {
		t.Fatal("download with a different SUT must be not_found, got success")
	} else {
		requireAPIError(t, err, "not_found")
	}
}

// TestFixtureExpiredReloadPath pins Review Focus 5: an expired/failed fixture
// load is OBSERVABLE (typed error, no success, no reuse link) and the recovery
// is re-login + re-save -> the next download uses the FRESH state, never the
// stale one. Mutation: silently swallowing the load failure (returning success
// with the stale state) fails this test.
func TestFixtureExpiredReloadPath(t *testing.T) {
	ctx := context.Background()
	store := &fakeFixtureStore{}
	content := &fakeFixtureContent{}
	session := &fakeFixtureSession{}
	pub := &corePublisher{}
	svc := newFixtureService(store, content, session, pub)

	staleState := []byte(`{"cookies":[{"name":"sid","value":"stale"}],"origins":[]}`)
	freshState := []byte(`{"cookies":[{"name":"sid","value":"fresh"}],"origins":[]}`)
	current := staleState
	session.readFn = func(tools.Scope, int64, string, string) ([]byte, error) { return current, nil }
	session.writeFn = func(tools.Scope, int64, string, string, []byte) error { return nil }
	session.linkFn = func(tools.Scope, int64, string, int64) error { return nil }
	loadFails := true
	session.loadFn = func(tools.Scope, int64, string, string) error {
		if loadFails {
			return fixtureLoadFailedError("state-load exited 1: storage state rejected")
		}
		return nil
	}

	if _, err := svc.UploadFixture(ctx, fixtureTestScope(500), tools.FixtureUploadInput{
		SUTID: 9001, AccountRole: "admin", Path: "state.json",
	}); err != nil {
		t.Fatalf("UploadFixture: %v", err)
	}

	// The stale fixture fails to load: observable typed conflict, no success.
	_, err := svc.DownloadFixture(ctx, fixtureTestScope(501), tools.FixtureDownloadInput{
		SUTID: 9001, AccountRole: "admin",
	})
	if err == nil {
		t.Fatal("a failed CLI state-load must surface an error, never a silent success with stale state")
	}
	requireAPIError(t, err, "fixture_load_failed")
	if len(session.links) != 1 {
		t.Fatalf("links = %+v, a failed load must not record a reuse link", session.links)
	}

	// Re-login: the agent saves a FRESH state (append-only second fact).
	current = freshState
	loadFails = false
	saved, err := svc.UploadFixture(ctx, fixtureTestScope(501), tools.FixtureUploadInput{
		SUTID: 9001, AccountRole: "admin", Path: "state.json",
	})
	if err != nil {
		t.Fatalf("re-save UploadFixture: %v", err)
	}
	if len(store.appended) != 2 {
		t.Fatalf("appended facts = %d, want 2 (append-only history)", len(store.appended))
	}
	freshFact := store.appended[1]
	if freshFact.StorageStateRef != fixtureShaOf(freshState) {
		t.Fatalf("fresh ref = %q, want sha256 of the fresh state", freshFact.StorageStateRef)
	}
	if saved.(map[string]any)["fixture_id"] != "2" {
		t.Fatalf("re-save result = %+v", saved)
	}

	// The next download must inject the FRESH state (latest wins), not the stale one.
	loaded, err := svc.DownloadFixture(ctx, fixtureTestScope(502), tools.FixtureDownloadInput{
		SUTID: 9001, AccountRole: "admin",
	})
	if err != nil {
		t.Fatalf("DownloadFixture after re-save: %v", err)
	}
	loadedMap := loaded.(map[string]any)
	if loadedMap["storage_state_ref"] != fixtureShaOf(freshState) || loadedMap["fixture_id"] != "2" {
		t.Fatalf("download after re-save = %+v, want the FRESH fixture (stale state reused)", loadedMap)
	}
	lastWrite := session.writes[len(session.writes)-1]
	if string(lastWrite.data) != string(freshState) {
		t.Fatalf("injected bytes = %q, want the fresh state", lastWrite.data)
	}
}

// TestFixtureContentUnavailableObservable: a fixture whose content blob is
// missing/corrupt in the ledger fails observably (typed conflict) and never
// writes/loads anything — the agent must re-login and re-save.
func TestFixtureContentUnavailableObservable(t *testing.T) {
	ctx := context.Background()
	store := &fakeFixtureStore{}
	content := &fakeFixtureContent{}
	session := &fakeFixtureSession{}
	svc := newFixtureService(store, content, session, &corePublisher{})

	state := []byte(`{"cookies":[],"origins":[]}`)
	session.readFn = func(tools.Scope, int64, string, string) ([]byte, error) { return state, nil }
	session.linkFn = func(tools.Scope, int64, string, int64) error { return nil }
	if _, err := svc.UploadFixture(ctx, fixtureTestScope(500), tools.FixtureUploadInput{
		SUTID: 9001, AccountRole: "admin",
	}); err != nil {
		t.Fatalf("UploadFixture: %v", err)
	}

	content.openErr = errors.New("content ledger: object missing")
	// writeFn/loadFn stay nil: the panic stub proves no injection is attempted.
	_, err := svc.DownloadFixture(ctx, fixtureTestScope(501), tools.FixtureDownloadInput{
		SUTID: 9001, AccountRole: "admin",
	})
	if err == nil {
		t.Fatal("missing fixture content must surface an error, never a silent success")
	}
	requireAPIError(t, err, "fixture_content_unavailable")
}

// TestFixtureUploadValidatesInput: the upload gate rejects a non-state file,
// an empty file, an out-of-scope SUT and an escaping workspace path; the
// default role normalizes to "default" symmetrically on upload and download.
func TestFixtureUploadValidatesInput(t *testing.T) {
	ctx := context.Background()

	t.Run("non-JSON state rejected", func(t *testing.T) {
		session := &fakeFixtureSession{readFn: func(tools.Scope, int64, string, string) ([]byte, error) {
			return []byte("not a storage state"), nil
		}}
		svc := newFixtureService(&fakeFixtureStore{}, &fakeFixtureContent{}, session, &corePublisher{})
		_, err := svc.UploadFixture(ctx, fixtureTestScope(500), tools.FixtureUploadInput{SUTID: 9001, AccountRole: "admin"})
		if err == nil {
			t.Fatal("non-JSON state must be rejected")
		}
		requireAPIError(t, err, "invalid_argument")
	})

	t.Run("empty state rejected", func(t *testing.T) {
		session := &fakeFixtureSession{readFn: func(tools.Scope, int64, string, string) ([]byte, error) {
			return nil, nil
		}}
		svc := newFixtureService(&fakeFixtureStore{}, &fakeFixtureContent{}, session, &corePublisher{})
		_, err := svc.UploadFixture(ctx, fixtureTestScope(500), tools.FixtureUploadInput{SUTID: 9001, AccountRole: "admin"})
		if err == nil {
			t.Fatal("empty state must be rejected")
		}
		requireAPIError(t, err, "invalid_argument")
	})

	t.Run("SUT outside the effective catalog rejected", func(t *testing.T) {
		session := &fakeFixtureSession{}
		svc := newFixtureService(&fakeFixtureStore{}, &fakeFixtureContent{}, session, &corePublisher{})
		_, err := svc.UploadFixture(ctx, fixtureTestScope(500), tools.FixtureUploadInput{SUTID: 9999, AccountRole: "admin"})
		if err == nil {
			t.Fatal("a SUT outside the effective catalog must be not_found")
		}
		requireAPIError(t, err, "not_found")
	})

	t.Run("escaping path rejected", func(t *testing.T) {
		for _, path := range []string{"../evil.json", "/abs/state.json", "a/../../evil.json"} {
			session := &fakeFixtureSession{}
			svc := newFixtureService(&fakeFixtureStore{}, &fakeFixtureContent{}, session, &corePublisher{})
			_, err := svc.UploadFixture(ctx, fixtureTestScope(500), tools.FixtureUploadInput{SUTID: 9001, AccountRole: "admin", Path: path})
			if err == nil {
				t.Fatalf("path %q must be rejected", path)
			}
			requireAPIError(t, err, "invalid_argument")
		}
	})

	t.Run("default role symmetric", func(t *testing.T) {
		store := &fakeFixtureStore{}
		content := &fakeFixtureContent{}
		state := []byte(`{"cookies":[],"origins":[]}`)
		session := &fakeFixtureSession{
			readFn:  func(tools.Scope, int64, string, string) ([]byte, error) { return state, nil },
			writeFn: func(tools.Scope, int64, string, string, []byte) error { return nil },
			loadFn:  func(tools.Scope, int64, string, string) error { return nil },
			linkFn:  func(tools.Scope, int64, string, int64) error { return nil },
		}
		svc := newFixtureService(store, content, session, &corePublisher{})
		if _, err := svc.UploadFixture(ctx, fixtureTestScope(500), tools.FixtureUploadInput{SUTID: 9001}); err != nil {
			t.Fatalf("UploadFixture with empty role: %v", err)
		}
		if store.appended[0].Role != "default" {
			t.Fatalf("fact role = %q, want the normalized default role", store.appended[0].Role)
		}
		if _, err := svc.DownloadFixture(ctx, fixtureTestScope(501), tools.FixtureDownloadInput{SUTID: 9001}); err != nil {
			t.Fatalf("DownloadFixture with empty role must find the default-role fixture: %v", err)
		}
	})

	t.Run("missing case_run scope rejected", func(t *testing.T) {
		svc := newFixtureService(&fakeFixtureStore{}, &fakeFixtureContent{}, &fakeFixtureSession{}, &corePublisher{})
		scope := fixtureTestScope(500)
		scope.CaseRunID = 0
		if _, err := svc.UploadFixture(ctx, scope, tools.FixtureUploadInput{SUTID: 9001, AccountRole: "admin"}); err == nil {
			t.Fatal("upload without a case_run scope must be rejected")
		}
	})
}

// TestFixtureInfraErrorsPropagate: a fact-write, content-write, fact-list or
// session-file-write failure is returned (observable, retryable), never a silent
// success. The session-file-write case also proves state-load is NOT attempted
// after a failed write (the panic stub), so a retry cannot load a stale
// previously-written fixture-state.json.
func TestFixtureInfraErrorsPropagate(t *testing.T) {
	ctx := context.Background()
	state := []byte(`{"cookies":[],"origins":[]}`)
	session := &fakeFixtureSession{
		readFn: func(tools.Scope, int64, string, string) ([]byte, error) { return state, nil },
	}

	t.Run("fact write failure", func(t *testing.T) {
		store := &fakeFixtureStore{appendErr: errors.New("db down")}
		svc := newFixtureService(store, &fakeFixtureContent{}, session, &corePublisher{})
		if _, err := svc.UploadFixture(ctx, fixtureTestScope(500), tools.FixtureUploadInput{SUTID: 9001, AccountRole: "admin"}); err == nil {
			t.Fatal("fact write failure must propagate")
		}
	})

	t.Run("content write failure", func(t *testing.T) {
		content := &fakeFixtureContent{putErr: errors.New("ledger down")}
		svc := newFixtureService(&fakeFixtureStore{}, content, session, &corePublisher{})
		if _, err := svc.UploadFixture(ctx, fixtureTestScope(500), tools.FixtureUploadInput{SUTID: 9001, AccountRole: "admin"}); err == nil {
			t.Fatal("content write failure must propagate")
		}
	})

	t.Run("fact list failure", func(t *testing.T) {
		store := &fakeFixtureStore{listErr: errors.New("db down")}
		svc := newFixtureService(store, &fakeFixtureContent{}, &fakeFixtureSession{}, &corePublisher{})
		if _, err := svc.DownloadFixture(ctx, fixtureTestScope(501), tools.FixtureDownloadInput{SUTID: 9001, AccountRole: "admin"}); err == nil {
			t.Fatal("fact list failure must propagate")
		}
	})

	t.Run("session file write failure does not load", func(t *testing.T) {
		store := &fakeFixtureStore{}
		content := &fakeFixtureContent{}
		// Seed one fixture (upload needs read + link).
		seed := &fakeFixtureSession{
			readFn: func(tools.Scope, int64, string, string) ([]byte, error) { return state, nil },
			linkFn: func(tools.Scope, int64, string, int64) error { return nil },
		}
		svc := newFixtureService(store, content, seed, &corePublisher{})
		if _, err := svc.UploadFixture(ctx, fixtureTestScope(500), tools.FixtureUploadInput{SUTID: 9001, AccountRole: "admin"}); err != nil {
			t.Fatalf("seed upload: %v", err)
		}
		// Download: the workspace write fails. loadFn stays nil — the panic stub
		// proves state-load is NEVER attempted after a failed write (a swallowed
		// write error would let a retry load a stale fixture-state.json).
		download := &fakeFixtureSession{
			writeFn: func(tools.Scope, int64, string, string, []byte) error { return errors.New("workspace write failed") },
		}
		svcDownload := newFixtureService(store, content, download, &corePublisher{})
		if _, err := svcDownload.DownloadFixture(ctx, fixtureTestScope(501), tools.FixtureDownloadInput{SUTID: 9001, AccountRole: "admin"}); err == nil {
			t.Fatal("session file write failure must propagate, never a silent success")
		}
		if len(download.loads) != 0 {
			t.Fatalf("state-load called %d times after a failed write, want 0", len(download.loads))
		}
		if len(download.links) != 0 {
			t.Fatalf("reuse link recorded %d times after a failed write, want 0", len(download.links))
		}
	})
}

// TestLoginTakeoverPauseResume pins SC-F5's GD-02 human judgment point: a 2FA
// challenge opens a login_takeover request that PAUSES the case_run and wave;
// the human answer RESUMES the run (re-dispatch); the takeover expiry reads the
// execution.human_request_timeout_seconds setting (default 1800s, configurable).
func TestLoginTakeoverPauseResume(t *testing.T) {
	ctx := context.Background()
	f := newTask9Fixture()
	f.seedWave(execution.WaveRunning)
	f.seedRun(500, 1, execution.CaseRunRunning)

	settings := &fakeSettings{values: map[string]any{
		"execution.human_request_timeout_seconds": int64(1800),
	}}
	human := NewHumanService(HumanServiceDeps{
		Humans: f.humans, CaseRuns: f.runs, Evidence: f.evidence, Waves: f.waves,
		Canceller: f.canceller, WorkQueue: f.queue, IDs: f.ids, Clock: f.clock,
		Events: f.events, Settings: settings,
	})

	request, err := human.Create(ctx, HumanCreateInput{
		CaseRunID: 500, WaveID: 50, Kind: "login_takeover",
		Reason: "2FA verification required", ChallengeKind: "2fa", AccountRole: "admin",
		DispatchEpoch: 1, DispatchToken: "token-1", ActorID: 42,
	})
	if err != nil {
		t.Fatalf("Create login_takeover: %v", err)
	}
	if request.Kind != execution.HumanLoginTakeover || request.Status != execution.HumanPending {
		t.Fatalf("request = %+v", request)
	}
	if want := f.clock.Now().Add(1800 * time.Second); !request.ExpiresAt.Equal(want) {
		t.Fatalf("expires_at = %s, want now+1800s (%s)", request.ExpiresAt, want)
	}
	run, _ := f.runs.Get(ctx, 500)
	if run.Status != execution.CaseRunPaused || run.PauseReason != execution.PauseReasonWaitingHuman {
		t.Fatalf("paused run = %+v", run)
	}
	wave, _ := f.waves.Get(ctx, 50)
	if wave.Status != execution.WavePaused {
		t.Fatalf("wave status = %s, want paused", wave.Status)
	}
	if !f.canceller.cancelled(500) {
		t.Fatal("the in-flight agent run must be cancelled on takeover")
	}

	// The human completes the 2FA challenge and answers; the run resumes.
	resolved, err := human.Resolve(ctx, request.ID, 42, map[string]any{"code": "246810"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.Status != execution.HumanAnswered {
		t.Fatalf("resolved status = %s", resolved.Status)
	}
	run, _ = f.runs.Get(ctx, 500)
	if run.Status != execution.CaseRunPending {
		t.Fatalf("run after human answer = %s, want pending (re-dispatch)", run.Status)
	}
	if _, err := f.pause.ResumeWave(ctx, 100, 50, 42); err != nil {
		t.Fatalf("ResumeWave after answer: %v", err)
	}
	wave, _ = f.waves.Get(ctx, 50)
	if wave.Status != execution.WaveRunning {
		t.Fatalf("wave after resume = %s, want running", wave.Status)
	}

	t.Run("timeout configurable", func(t *testing.T) {
		f2 := newTask9Fixture()
		f2.seedWave(execution.WaveRunning)
		f2.seedRun(500, 1, execution.CaseRunRunning)
		custom := NewHumanService(HumanServiceDeps{
			Humans: f2.humans, CaseRuns: f2.runs, Evidence: f2.evidence, Waves: f2.waves,
			Canceller: f2.canceller, WorkQueue: f2.queue, IDs: f2.ids, Clock: f2.clock,
			Events: f2.events,
			Settings: &fakeSettings{values: map[string]any{
				"execution.human_request_timeout_seconds": int64(60),
			}},
		})
		req, err := custom.Create(ctx, HumanCreateInput{
			CaseRunID: 500, WaveID: 50, Kind: "login_takeover", Reason: "2fa",
			DispatchEpoch: 1, DispatchToken: "token-1", ActorID: 42,
		})
		if err != nil {
			t.Fatalf("Create with custom timeout: %v", err)
		}
		if want := f2.clock.Now().Add(60 * time.Second); !req.ExpiresAt.Equal(want) {
			t.Fatalf("expires_at = %s, want now+60s from the setting (hardcoded override?)", req.ExpiresAt)
		}
	})

	t.Run("timeout default 1800 without setting", func(t *testing.T) {
		f3 := newTask9Fixture()
		f3.seedWave(execution.WaveRunning)
		f3.seedRun(500, 1, execution.CaseRunRunning)
		bare := NewHumanService(HumanServiceDeps{
			Humans: f3.humans, CaseRuns: f3.runs, Evidence: f3.evidence, Waves: f3.waves,
			Canceller: f3.canceller, WorkQueue: f3.queue, IDs: f3.ids, Clock: f3.clock,
			Events: f3.events, Settings: &fakeSettings{values: map[string]any{}},
		})
		req, err := bare.Create(ctx, HumanCreateInput{
			CaseRunID: 500, WaveID: 50, Kind: "login_takeover", Reason: "2fa",
			DispatchEpoch: 1, DispatchToken: "token-1", ActorID: 42,
		})
		if err != nil {
			t.Fatalf("Create without setting: %v", err)
		}
		if want := f3.clock.Now().Add(1800 * time.Second); !req.ExpiresAt.Equal(want) {
			t.Fatalf("expires_at = %s, want the 1800s default", req.ExpiresAt)
		}
	})
}

// requireFixtureLoadFailedShape pins the typed failure the model can act on.
func TestFixtureLoadFailedErrorShape(t *testing.T) {
	err := fixtureLoadFailedError("state-load exited 1")
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error type %T, want *platform.Error", err)
	}
	if apiErr.Status != 409 || apiErr.Code != "fixture_load_failed" {
		t.Fatalf("error = %+v", apiErr)
	}
	hint, _ := apiErr.Details["hint"].(string)
	if !strings.Contains(hint, "fixture.upload") {
		t.Fatalf("hint = %q, must tell the model to re-login and re-save via fixture.upload", hint)
	}
}
