package resource

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	executiondomain "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	resourcedomain "github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

func ptr[T any](value T) *T { return &value }

// fakeSessions is the resource session service slice the adapter consumes.
type fakeSessions struct {
	acquired    resourceapp.AcquiredSession
	acquireErr  error
	acquireArgs []acquireCall

	releaseCalls []releaseCall
	releaseErr   error

	invalidCalls []invalidCall
	invalidErr   error

	getTakeover *resourcedomain.Takeover
	getErr      error

	takeovers     []resourcedomain.Takeover
	takeoversMore bool
	listErr       error
	listFilters   []resourcedomain.TakeoverFilter
	listFn        func(resourcedomain.TakeoverFilter) ([]resourcedomain.Takeover, bool, error)
}

type acquireCall struct {
	environmentID int64
	systemID      int64
	holder        string
	ttl           time.Duration
}

type releaseCall struct {
	sessionID int64
	holder    string
	reason    string
}

type invalidCall struct {
	sessionID int64
	reason    string
}

func (f *fakeSessions) AcquireForExecution(_ context.Context, environmentID, systemID int64, holder string, ttl time.Duration) (resourceapp.AcquiredSession, error) {
	f.acquireArgs = append(f.acquireArgs, acquireCall{environmentID, systemID, holder, ttl})
	return f.acquired, f.acquireErr
}

func (f *fakeSessions) ReleaseLease(_ context.Context, sessionID int64, holder, reason string) error {
	f.releaseCalls = append(f.releaseCalls, releaseCall{sessionID, holder, reason})
	return f.releaseErr
}

func (f *fakeSessions) MarkInvalid(_ context.Context, sessionID int64, reason string) error {
	f.invalidCalls = append(f.invalidCalls, invalidCall{sessionID, reason})
	return f.invalidErr
}

func (f *fakeSessions) Get(context.Context, int64) (*resourcedomain.Session, *resourcedomain.Snapshot, *resourcedomain.Lease, *resourcedomain.Takeover, error) {
	if f.getErr != nil {
		return nil, nil, nil, nil, f.getErr
	}
	return nil, nil, nil, f.getTakeover, nil
}

func (f *fakeSessions) ListTakeovers(_ context.Context, filter resourcedomain.TakeoverFilter) ([]resourcedomain.Takeover, bool, error) {
	f.listFilters = append(f.listFilters, filter)
	if f.listFn != nil {
		return f.listFn(filter)
	}
	return f.takeovers, f.takeoversMore, f.listErr
}

// fakeCatalog is the resource catalog slice the adapter consumes.
type fakeCatalog struct {
	environment *resourcedomain.Environment
	envErr      error
	system      *resourcedomain.System
	systemErr   error
}

func (f *fakeCatalog) GetEnvironment(context.Context, int64) (*resourcedomain.Environment, error) {
	return f.environment, f.envErr
}

func (f *fakeCatalog) GetSystem(context.Context, int64) (*resourcedomain.System, error) {
	return f.system, f.systemErr
}

func TestAcquireMapsBrowserSession(t *testing.T) {
	expires := time.Now().Add(time.Hour).UTC()
	service := &fakeSessions{acquired: resourceapp.AcquiredSession{
		SessionID:        55,
		SnapshotID:       56,
		StorageState:     json.RawMessage(`{"cookies":[{"name":"sid","value":"v"}],"origins":[]}`),
		SessionStorage:   json.RawMessage(`{"https://app.example":[]}`),
		StorageStateKind: resourcedomain.StorageStateKindStorageState,
		ExpiresAt:        &expires,
	}}
	adapter := New(service, &fakeCatalog{})

	ref, err := adapter.Acquire(context.Background(), 12, 11, "case_run:1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if ref.SessionID != 55 || ref.SnapshotID != 56 || ref.ExpiresAt == nil || !ref.ExpiresAt.Equal(expires) {
		t.Fatalf("ref = %+v", ref)
	}
	if ref.StorageStateKind != resourcedomain.StorageStateKindStorageState {
		t.Fatalf("storageStateKind = %q", ref.StorageStateKind)
	}
	if string(ref.StorageState) != `{"cookies":[{"name":"sid","value":"v"}],"origins":[]}` ||
		string(ref.SessionStorage) != `{"https://app.example":[]}` {
		t.Fatalf("state = %s / %s", ref.StorageState, ref.SessionStorage)
	}
	if len(ref.Headers) != 0 {
		t.Fatalf("browser session headers = %v", ref.Headers)
	}
	if got := service.acquireArgs; len(got) != 1 ||
		got[0] != (acquireCall{12, 11, "case_run:1", time.Minute}) {
		t.Fatalf("acquire args = %+v", got)
	}

	// Returned state is a copy: mutating the acquisition cannot change the ref.
	service.acquired.StorageState[0] = 'X'
	if ref.StorageState[0] != '{' {
		t.Fatalf("ref aliases the acquired state: %s", ref.StorageState)
	}
}

func TestAcquireMapsTokenSessionToHeadersOnly(t *testing.T) {
	service := &fakeSessions{acquired: resourceapp.AcquiredSession{
		SessionID:        55,
		SnapshotID:       56,
		StorageState:     json.RawMessage(`{"headers":{"Authorization":"Bearer t"}}`),
		StorageStateKind: resourcedomain.StorageStateKindToken,
		Headers:          map[string]string{"Authorization": "Bearer t"},
	}}
	ref, err := New(service, &fakeCatalog{}).Acquire(context.Background(), 12, 11, "case_run:1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ref.Headers, map[string]string{"Authorization": "Bearer t"}) {
		t.Fatalf("headers = %v", ref.Headers)
	}
	// The kind is projected verbatim so downstream can skip creating a browser
	// context for an HTTP-only session.
	if ref.StorageStateKind != resourcedomain.StorageStateKindToken {
		t.Fatalf("storageStateKind = %q", ref.StorageStateKind)
	}
	// A token session is HTTP-only (spec §5.9): its token document must never be
	// injected as browser storage state.
	if len(ref.StorageState) != 0 || len(ref.SessionStorage) != 0 {
		t.Fatalf("token session browser state = %s / %s", ref.StorageState, ref.SessionStorage)
	}
	service.acquired.Headers["Authorization"] = "changed"
	if ref.Headers["Authorization"] != "Bearer t" {
		t.Fatalf("ref aliases the acquired headers: %v", ref.Headers)
	}
}

func TestAcquireMapsNeedsManualToTakeover(t *testing.T) {
	expires := time.Now().Add(time.Minute).UTC()
	service := &fakeSessions{
		acquireErr: &resourceapp.ErrNeedsManual{TakeoverID: 99, Reason: resourcedomain.TakeoverReasonMFA},
		takeovers: []resourcedomain.Takeover{{
			ID:           99,
			SessionID:    ptr[int64](55),
			Reason:       resourcedomain.TakeoverReasonMFA,
			Status:       resourcedomain.TakeoverStatusWaiting,
			Instructions: json.RawMessage(`{"step":"open authenticator"}`),
			ExpiresAt:    &expires,
		}},
	}
	ref, err := New(service, &fakeCatalog{}).Acquire(context.Background(), 12, 11, "case_run:1", time.Minute)

	if !errors.Is(err, executionapp.ErrSessionNeedsManual) {
		t.Fatalf("error = %v, want ErrSessionNeedsManual", err)
	}
	var needsManual *NeedsManualError
	if !errors.As(err, &needsManual) {
		t.Fatalf("error %v is not a *NeedsManualError", err)
	}
	if needsManual.Reason != resourcedomain.TakeoverReasonMFA {
		t.Fatalf("reason = %q", needsManual.Reason)
	}
	if needsManual.Takeover == nil || needsManual.Takeover.ID != 99 ||
		needsManual.Takeover.SessionID != 55 ||
		needsManual.Takeover.Reason != resourcedomain.TakeoverReasonMFA ||
		needsManual.Takeover.ExpiresAt == nil {
		t.Fatalf("takeover = %+v", needsManual.Takeover)
	}
	if string(needsManual.Takeover.Instructions) != `{"step":"open authenticator"}` {
		t.Fatalf("instructions = %s", needsManual.Takeover.Instructions)
	}
	// The waiting session id is surfaced so the scheduler can poll the gate.
	if ref.SessionID != 55 {
		t.Fatalf("session ref = %+v", ref)
	}
	if len(service.listFilters) == 0 ||
		service.listFilters[0].Status != resourcedomain.TakeoverStatusWaiting {
		t.Fatalf("takeover lookup filters = %+v", service.listFilters)
	}
}

func TestAcquireTakeoverLookupPagesUntilFound(t *testing.T) {
	pages := map[int64][]resourcedomain.Takeover{
		0:  {{ID: 10}},
		10: {{ID: 250, SessionID: ptr[int64](77), Status: resourcedomain.TakeoverStatusWaiting}},
	}
	service := &fakeSessions{
		acquireErr: &resourceapp.ErrNeedsManual{TakeoverID: 250, Reason: resourcedomain.TakeoverReasonCaptcha},
		listFn: func(filter resourcedomain.TakeoverFilter) ([]resourcedomain.Takeover, bool, error) {
			return pages[filter.Page.AfterID], true, nil
		},
	}
	ref, err := New(service, &fakeCatalog{}).Acquire(context.Background(), 12, 11, "case_run:1", time.Minute)
	if !errors.Is(err, executionapp.ErrSessionNeedsManual) {
		t.Fatalf("error = %v, want ErrSessionNeedsManual", err)
	}
	var needsManual *NeedsManualError
	if !errors.As(err, &needsManual) || needsManual.Takeover == nil ||
		needsManual.Takeover.ID != 250 || needsManual.Takeover.SessionID != 77 {
		t.Fatalf("takeover = %+v, error = %v", needsManual, err)
	}
	if ref.SessionID != 77 {
		t.Fatalf("session ref = %+v", ref)
	}
	if len(service.listFilters) != 2 || service.listFilters[1].Page.AfterID != 10 ||
		service.listFilters[0].Page.Limit != takeoverPageSize {
		t.Fatalf("takeover lookup filters = %+v", service.listFilters)
	}
}

func TestAcquireUnresolvedTakeoverIsSessionUnavailable(t *testing.T) {
	service := &fakeSessions{
		acquireErr: &resourceapp.ErrNeedsManual{TakeoverID: 99, Reason: resourcedomain.TakeoverReasonMFA},
	}
	ref, err := New(service, &fakeCatalog{}).Acquire(context.Background(), 12, 11, "case_run:1", time.Minute)

	// Without a resolvable waiting takeover there is no session id to poll, so
	// the scheduler must classify blocked(session) without a fake gate ref.
	if !errors.Is(err, executionapp.ErrSessionUnavailable) {
		t.Fatalf("error = %v, want ErrSessionUnavailable", err)
	}
	if errors.Is(err, executionapp.ErrSessionNeedsManual) {
		t.Fatalf("error %v must not claim a manual takeover without a ref", err)
	}
	var unavailable *ErrTakeoverUnavailable
	if !errors.As(err, &unavailable) {
		t.Fatalf("error %v is not an *ErrTakeoverUnavailable", err)
	}
	if unavailable.TakeoverID != 99 || unavailable.Reason != resourcedomain.TakeoverReasonMFA {
		t.Fatalf("unavailable = %+v", unavailable)
	}
	if ref.SessionID != 0 {
		t.Fatalf("session ref = %+v, want the zero ref", ref)
	}
}

func TestAcquireTakeoverLookupStopsAfterPassingTargetID(t *testing.T) {
	lists := 0
	service := &fakeSessions{
		acquireErr: &resourceapp.ErrNeedsManual{TakeoverID: 99, Reason: resourcedomain.TakeoverReasonMFA},
		listFn: func(resourcedomain.TakeoverFilter) ([]resourcedomain.Takeover, bool, error) {
			lists++
			// Ordered by increasing id: once a row exceeds the target the lookup
			// can stop instead of scanning every remaining page.
			return []resourcedomain.Takeover{{ID: 10}, {ID: 20}, {ID: 300}}, true, nil
		},
	}
	_, err := New(service, &fakeCatalog{}).Acquire(context.Background(), 12, 11, "case_run:1", time.Minute)

	var unavailable *ErrTakeoverUnavailable
	if !errors.As(err, &unavailable) {
		t.Fatalf("error = %v, want *ErrTakeoverUnavailable", err)
	}
	if lists != 1 {
		t.Fatalf("takeover lookup pages = %d, want 1", lists)
	}
}

func TestAcquireTakeoverLookupErrorIsSessionUnavailable(t *testing.T) {
	boom := errors.New("takeover list failed")
	service := &fakeSessions{
		acquireErr: &resourceapp.ErrNeedsManual{TakeoverID: 99, Reason: resourcedomain.TakeoverReasonMFA},
		listErr:    boom,
	}
	_, err := New(service, &fakeCatalog{}).Acquire(context.Background(), 12, 11, "case_run:1", time.Minute)

	if !errors.Is(err, executionapp.ErrSessionUnavailable) {
		t.Fatalf("error = %v, want ErrSessionUnavailable", err)
	}
	var unavailable *ErrTakeoverUnavailable
	if !errors.As(err, &unavailable) || !errors.Is(err, boom) {
		t.Fatalf("error = %v, want *ErrTakeoverUnavailable carrying the lookup error", err)
	}
}

func TestAcquireMapsNoSessionReason(t *testing.T) {
	service := &fakeSessions{
		acquireErr: &resourceapp.NoSessionError{Reason: resourceapp.NoSessionReasonNoValidSession},
	}
	_, err := New(service, &fakeCatalog{}).Acquire(context.Background(), 12, 11, "case_run:1", time.Minute)

	if !errors.Is(err, executionapp.ErrSessionUnavailable) {
		t.Fatalf("error = %v, want ErrSessionUnavailable", err)
	}
	var unavailable *SessionUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("error %v is not a *SessionUnavailableError", err)
	}
	if unavailable.Reason != resourceapp.NoSessionReasonNoValidSession {
		t.Fatalf("reason = %q", unavailable.Reason)
	}
}

func TestAcquireMapsErrSessionUnavailableReason(t *testing.T) {
	service := &fakeSessions{
		acquireErr: &resourceapp.ErrSessionUnavailable{Reason: "login_check_failed"},
	}
	_, err := New(service, &fakeCatalog{}).Acquire(context.Background(), 12, 11, "case_run:1", time.Minute)

	if !errors.Is(err, executionapp.ErrSessionUnavailable) {
		t.Fatalf("error = %v, want ErrSessionUnavailable", err)
	}
	var unavailable *SessionUnavailableError
	if !errors.As(err, &unavailable) || unavailable.Reason != "login_check_failed" {
		t.Fatalf("error = %v", err)
	}
}

func TestAcquireMapsResourceDomainErrorToSessionUnavailable(t *testing.T) {
	// The acquisition contract reports resource-domain failures; they must not
	// leak untranslated or the scheduler would treat them as infra failures.
	service := &fakeSessions{acquireErr: resourcedomain.NewStateConflict("session state conflict")}
	_, err := New(service, &fakeCatalog{}).Acquire(context.Background(), 12, 11, "case_run:1", time.Minute)

	if !errors.Is(err, executionapp.ErrSessionUnavailable) {
		t.Fatalf("error = %v, want ErrSessionUnavailable", err)
	}
	var unavailable *SessionUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("error %v is not a *SessionUnavailableError", err)
	}
	if unavailable.Reason != "session_state_conflict" {
		t.Fatalf("reason = %q, want session_state_conflict", unavailable.Reason)
	}
}

func TestAcquireMapsUnknownErrorToInternalSessionUnavailable(t *testing.T) {
	boom := errors.New("boom")
	_, err := New(&fakeSessions{acquireErr: boom}, &fakeCatalog{}).
		Acquire(context.Background(), 12, 11, "case_run:1", time.Minute)

	if !errors.Is(err, executionapp.ErrSessionUnavailable) {
		t.Fatalf("error = %v, want ErrSessionUnavailable", err)
	}
	var unavailable *SessionUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("error %v is not a *SessionUnavailableError", err)
	}
	if unavailable.Reason != "internal" {
		t.Fatalf("reason = %q, want internal", unavailable.Reason)
	}
}

func TestReleaseAndMarkInvalidForward(t *testing.T) {
	service := &fakeSessions{}
	adapter := New(service, &fakeCatalog{})
	ctx := context.Background()

	if err := adapter.Release(ctx, 55, "case_run:1", "done"); err != nil {
		t.Fatal(err)
	}
	if len(service.releaseCalls) != 1 || service.releaseCalls[0] != (releaseCall{55, "case_run:1", "done"}) {
		t.Fatalf("release calls = %+v", service.releaseCalls)
	}
	if err := adapter.MarkInvalid(ctx, 55, "session_unauthorized"); err != nil {
		t.Fatal(err)
	}
	if len(service.invalidCalls) != 1 || service.invalidCalls[0] != (invalidCall{55, "session_unauthorized"}) {
		t.Fatalf("invalid calls = %+v", service.invalidCalls)
	}

	service.releaseErr = resourcedomain.NewStateConflict("session state conflict")
	err := adapter.Release(ctx, 55, "case_run:1", "done")
	var reasonErr *executiondomain.ReasonError
	if !errors.As(err, &reasonErr) || reasonErr.Code != "session_state_conflict" {
		t.Fatalf("release error = %v", err)
	}
}

func TestPendingTakeoverReturnsWaitingTakeover(t *testing.T) {
	expires := time.Now().Add(time.Minute).UTC()
	service := &fakeSessions{getTakeover: &resourcedomain.Takeover{
		ID:           99,
		SessionID:    ptr[int64](55),
		Reason:       resourcedomain.TakeoverReasonCaptcha,
		Status:       resourcedomain.TakeoverStatusWaiting,
		Instructions: json.RawMessage(`{}`),
		ExpiresAt:    &expires,
	}}
	takeover, err := New(service, &fakeCatalog{}).PendingTakeover(context.Background(), 55)
	if err != nil {
		t.Fatal(err)
	}
	if takeover == nil || takeover.ID != 99 || takeover.SessionID != 55 ||
		takeover.Reason != resourcedomain.TakeoverReasonCaptcha ||
		takeover.ExpiresAt == nil || !takeover.ExpiresAt.Equal(expires) {
		t.Fatalf("takeover = %+v", takeover)
	}
}

func TestPendingTakeoverIgnoresFinishedTakeover(t *testing.T) {
	service := &fakeSessions{getTakeover: &resourcedomain.Takeover{
		ID:     99,
		Status: resourcedomain.TakeoverStatusCompleted,
	}}
	takeover, err := New(service, &fakeCatalog{}).PendingTakeover(context.Background(), 55)
	if err != nil || takeover != nil {
		t.Fatalf("takeover = %+v, err = %v", takeover, err)
	}
}

func TestPendingTakeoverMissingSessionIsNoTakeover(t *testing.T) {
	service := &fakeSessions{getErr: resourcedomain.NewNotFound("session", 55)}
	takeover, err := New(service, &fakeCatalog{}).PendingTakeover(context.Background(), 55)
	if err != nil || takeover != nil {
		t.Fatalf("takeover = %+v, err = %v", takeover, err)
	}
}

func TestEnvironmentMapsProjection(t *testing.T) {
	catalog := &fakeCatalog{environment: &resourcedomain.Environment{
		ID:           12,
		SystemID:     11,
		Name:         "staging",
		BaseURL:      "https://staging.example",
		AllowedHosts: []string{"staging.example", "api.staging.example"},
	}}
	ref, err := New(&fakeSessions{}, catalog).Environment(context.Background(), 12)
	if err != nil {
		t.Fatal(err)
	}
	if ref.ID != 12 || ref.SystemID != 11 || ref.Name != "staging" || ref.BaseURL != "https://staging.example" {
		t.Fatalf("ref = %+v", ref)
	}
	if !reflect.DeepEqual(ref.AllowedHosts, []string{"staging.example", "api.staging.example"}) {
		t.Fatalf("hosts = %v", ref.AllowedHosts)
	}
	catalog.environment.AllowedHosts[0] = "changed"
	if ref.AllowedHosts[0] != "staging.example" {
		t.Fatalf("ref aliases the environment hosts: %v", ref.AllowedHosts)
	}
}

func TestEnvironmentMapsNotFound(t *testing.T) {
	catalog := &fakeCatalog{envErr: resourcedomain.NewNotFound("environment", 12)}
	_, err := New(&fakeSessions{}, catalog).Environment(context.Background(), 12)
	var reasonErr *executiondomain.ReasonError
	if !errors.As(err, &reasonErr) || reasonErr.Code != "not_found" {
		t.Fatalf("error = %v", err)
	}
}

// fakeVersionRefs is the execution version port slice the report adapter needs.
type fakeVersionRefs struct {
	ref executionapp.VersionRef
	err error
}

func (f *fakeVersionRefs) GetVersion(context.Context, int64) (executionapp.VersionRef, error) {
	return f.ref, f.err
}

func TestReportEnvironmentSourceResolvesNames(t *testing.T) {
	versions := &fakeVersionRefs{ref: executionapp.VersionRef{
		ID:            7,
		SystemID:      ptr[int64](11),
		EnvironmentID: ptr[int64](12),
	}}
	catalog := &fakeCatalog{
		environment: &resourcedomain.Environment{ID: 12, SystemID: 11, Name: "staging", BaseURL: "https://staging.example"},
		system:      &resourcedomain.System{ID: 11, Name: "Order"},
	}
	environment, err := NewReportEnvironmentSource(versions, catalog).
		ReportEnvironment(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if environment.System != "Order" || environment.Environment != "staging" ||
		environment.BaseURL != "https://staging.example" {
		t.Fatalf("environment = %+v", environment)
	}
}

func TestReportEnvironmentSourceUnboundVersionIsEmpty(t *testing.T) {
	versions := &fakeVersionRefs{ref: executionapp.VersionRef{ID: 7}}
	environment, err := NewReportEnvironmentSource(versions, &fakeCatalog{}).
		ReportEnvironment(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if (environment != executionapp.ReportEnvironment{}) {
		t.Fatalf("environment = %+v", environment)
	}
}

func TestReportEnvironmentSourceMissingEnvironmentFails(t *testing.T) {
	versions := &fakeVersionRefs{ref: executionapp.VersionRef{
		ID:            7,
		EnvironmentID: ptr[int64](12),
	}}
	catalog := &fakeCatalog{envErr: resourcedomain.NewNotFound("environment", 12)}
	_, err := NewReportEnvironmentSource(versions, catalog).ReportEnvironment(context.Background(), 7)
	var reasonErr *executiondomain.ReasonError
	if !errors.As(err, &reasonErr) || reasonErr.Code != "not_found" {
		t.Fatalf("error = %v", err)
	}
}

func TestReportEnvironmentSourceMissingSystemLeavesNameEmpty(t *testing.T) {
	versions := &fakeVersionRefs{ref: executionapp.VersionRef{
		ID:            7,
		SystemID:      ptr[int64](11),
		EnvironmentID: ptr[int64](12),
	}}
	catalog := &fakeCatalog{
		environment: &resourcedomain.Environment{ID: 12, SystemID: 11, Name: "staging", BaseURL: "https://staging.example"},
		systemErr:   resourcedomain.NewNotFound("system", 11),
	}
	environment, err := NewReportEnvironmentSource(versions, catalog).ReportEnvironment(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if environment.System != "" || environment.Environment != "staging" {
		t.Fatalf("environment = %+v", environment)
	}
}

func TestAdapterRejectsNilServices(t *testing.T) {
	if _, err := New(nil, nil).Acquire(context.Background(), 12, 11, "h", time.Minute); err == nil {
		t.Fatal("Acquire with nil service = nil error")
	}
	if _, err := New(nil, nil).Environment(context.Background(), 12); err == nil {
		t.Fatal("Environment with nil catalog = nil error")
	}
	if _, err := NewReportEnvironmentSource(nil, nil).ReportEnvironment(context.Background(), 7); err == nil {
		t.Fatal("ReportEnvironment with nil version port = nil error")
	}
}

var (
	_ executionapp.SessionPort             = (*Adapter)(nil)
	_ executionapp.ReportEnvironmentSource = (*ReportEnvironmentSource)(nil)
)
