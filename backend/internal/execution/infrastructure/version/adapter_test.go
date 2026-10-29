package version

import (
	"context"
	"errors"
	"testing"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	executiondomain "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

func ptr[T any](value T) *T { return &value }

// fakeVersions is the version application service slice the adapter consumes.
type fakeVersions struct {
	version *versionapp.Version
	getErr  error

	getCalls     int
	pauseCalls   []pauseCall
	pauseErr     error
	batchNoCalls []batchNoCall
	batchNoErr   error
	completeArgs []int64
	completeErr  error
	phaseCalls   []phaseCall
	phaseErr     error
}

type pauseCall struct {
	versionID int64
	paused    bool
	mode      string
	reason    string
	actor     int64
}

type batchNoCall struct {
	versionID int64
	batchNo   int
	actor     int64
}

type phaseCall struct {
	versionID int64
	phase     string
}

func (f *fakeVersions) Get(context.Context, int64) (*versionapp.Version, error) {
	f.getCalls++
	return f.version, f.getErr
}

func (f *fakeVersions) MarkPaused(_ context.Context, versionID int64, paused bool, mode, reason string, actor int64) (*versionapp.Version, error) {
	f.pauseCalls = append(f.pauseCalls, pauseCall{versionID, paused, mode, reason, actor})
	return f.version, f.pauseErr
}

func (f *fakeVersions) SetBatchNo(_ context.Context, versionID int64, batchNo int, actor int64) (*versionapp.Version, error) {
	f.batchNoCalls = append(f.batchNoCalls, batchNoCall{versionID, batchNo, actor})
	return f.version, f.batchNoErr
}

func (f *fakeVersions) CompleteExecution(_ context.Context, versionID, actor int64) (*versionapp.Version, error) {
	f.completeArgs = append(f.completeArgs, versionID, actor)
	return f.version, f.completeErr
}

func (f *fakeVersions) SetContinuePhase(_ context.Context, versionID int64, phase string) error {
	f.phaseCalls = append(f.phaseCalls, phaseCall{versionID, phase})
	return f.phaseErr
}

// atomicVersions additionally provides the native allocate/release surface a
// future version service can expose; the adapter must prefer it.
type atomicVersions struct {
	*fakeVersions
	allocated   int
	allocateErr error
	released    []int
	releaseErr  error
}

func (f *atomicVersions) AllocateBatchNo(context.Context, int64) (int, error) {
	return f.allocated, f.allocateErr
}

func (f *atomicVersions) ReleaseBatchNo(_ context.Context, _ int64, allocated int) error {
	f.released = append(f.released, allocated)
	return f.releaseErr
}

func seededVersion() *versionapp.Version {
	return &versionapp.Version{
		ID:                 7,
		Status:             versiondomain.VersionExecuting,
		Paused:             true,
		PauseMode:          versiondomain.PauseModeImmediate,
		ContinuePhase:      versiondomain.ContinuePhaseDesigning,
		BatchNo:            3,
		SystemID:           ptr[int64](11),
		EnvironmentID:      ptr[int64](12),
		CodeBaselineCommit: ptr("0123456789abcdef0123456789abcdef01234567"),
	}
}

func TestGetVersionMapsExecutionState(t *testing.T) {
	service := &fakeVersions{version: seededVersion()}
	adapter := New(service)

	ref, err := adapter.GetVersion(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if ref.ID != 7 || ref.Status != versiondomain.VersionExecuting || !ref.Paused ||
		ref.PauseMode != versiondomain.PauseModeImmediate || ref.BatchNo != 3 {
		t.Fatalf("ref = %+v", ref)
	}
	// The P7 incremental gate reads the live continue phase from the projection.
	if ref.ContinuePhase != versiondomain.ContinuePhaseDesigning {
		t.Fatalf("continue phase = %q, want %q", ref.ContinuePhase, versiondomain.ContinuePhaseDesigning)
	}
	if ref.SystemID == nil || *ref.SystemID != 11 ||
		ref.EnvironmentID == nil || *ref.EnvironmentID != 12 ||
		ref.CodeBaselineCommit == nil || *ref.CodeBaselineCommit != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("bindings = %+v", ref)
	}

	// The projection owns copies: mutating the source cannot change it.
	*service.version.SystemID = 99
	*service.version.CodeBaselineCommit = "changed"
	if *ref.SystemID != 11 || *ref.CodeBaselineCommit != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("ref aliases the source version: %+v", ref)
	}
}

func TestGetVersionMapsNotFound(t *testing.T) {
	service := &fakeVersions{getErr: versiondomain.NewNotFound("version", 7)}
	_, err := New(service).GetVersion(context.Background(), 7)

	var reasonErr *executiondomain.ReasonError
	if !errors.As(err, &reasonErr) {
		t.Fatalf("error %v is not a *domain.ReasonError", err)
	}
	if reasonErr.Code != "not_found" || reasonErr.Reason != "not_found" {
		t.Fatalf("code/reason = %q/%q, want not_found/not_found", reasonErr.Code, reasonErr.Reason)
	}
}

func TestVersionActionsForwardArgumentsAndMapErrors(t *testing.T) {
	service := &fakeVersions{version: seededVersion()}
	adapter := New(service)
	ctx := context.Background()

	if err := adapter.MarkPaused(ctx, 7, true, "immediate", "restart", 42); err != nil {
		t.Fatal(err)
	}
	if len(service.pauseCalls) != 1 {
		t.Fatalf("MarkPaused calls = %+v", service.pauseCalls)
	}
	if got := service.pauseCalls[0]; got != (pauseCall{7, true, "immediate", "restart", 42}) {
		t.Fatalf("MarkPaused args = %+v", got)
	}

	if err := adapter.SetBatchNo(ctx, 7, 4, 42); err != nil {
		t.Fatal(err)
	}
	if len(service.batchNoCalls) != 1 || service.batchNoCalls[0] != (batchNoCall{7, 4, 42}) {
		t.Fatalf("SetBatchNo args = %+v", service.batchNoCalls)
	}

	if err := adapter.CompleteExecution(ctx, 7, 42); err != nil {
		t.Fatal(err)
	}
	if len(service.completeArgs) != 2 || service.completeArgs[0] != 7 || service.completeArgs[1] != 42 {
		t.Fatalf("CompleteExecution args = %v", service.completeArgs)
	}

	service.pauseErr = versiondomain.NewStateConflict("version_state_conflict", "version is paused")
	err := adapter.MarkPaused(ctx, 7, false, "", "", 42)
	var reasonErr *executiondomain.ReasonError
	if !errors.As(err, &reasonErr) || reasonErr.Code != "version_state_conflict" {
		t.Fatalf("pause error = %v", err)
	}

	service.batchNoErr = versiondomain.NewInvalid("batchNo", "invalid_batch_no", "negative")
	err = adapter.SetBatchNo(ctx, 7, -1, 42)
	if !errors.As(err, &reasonErr) || reasonErr.Code != "invalid_argument" || reasonErr.Reason != "invalid_batch_no" {
		t.Fatalf("batch error = %v", err)
	}

	service.completeErr = errors.New("boom")
	if err := adapter.CompleteExecution(ctx, 7, 42); !errors.Is(err, service.completeErr) {
		t.Fatalf("complete error = %v, want the underlying error", err)
	}
}

// TestSetContinuePhaseForwardsAndMapsErrors covers the optional P7
// ContinuePhasePort the batch service asserts after an incremental batch
// commits (spec §5.1/§5.3).
func TestSetContinuePhaseForwardsAndMapsErrors(t *testing.T) {
	ctx := context.Background()
	service := &fakeVersions{version: seededVersion()}
	adapter := New(service)

	if err := adapter.SetContinuePhase(ctx, 7, versiondomain.ContinuePhaseExecuting); err != nil {
		t.Fatal(err)
	}
	if len(service.phaseCalls) != 1 ||
		service.phaseCalls[0] != (phaseCall{7, versiondomain.ContinuePhaseExecuting}) {
		t.Fatalf("SetContinuePhase args = %+v", service.phaseCalls)
	}

	service.phaseErr = versiondomain.NewStateConflict("version_state_conflict", "version is completed")
	err := adapter.SetContinuePhase(ctx, 7, versiondomain.ContinuePhaseExecuting)
	var reasonErr *executiondomain.ReasonError
	if !errors.As(err, &reasonErr) || reasonErr.Code != "version_state_conflict" {
		t.Fatalf("error = %v", err)
	}
}

func TestAllocateBatchNoPrefersNativeAllocator(t *testing.T) {
	service := &atomicVersions{fakeVersions: &fakeVersions{version: seededVersion()}, allocated: 9}
	allocated, err := New(service).AllocateBatchNo(context.Background(), 7)
	if err != nil || allocated != 9 {
		t.Fatalf("allocated = %d, err = %v", allocated, err)
	}
	if service.getCalls != 0 || len(service.batchNoCalls) != 0 {
		t.Fatalf("fallback was used: get=%d set=%+v", service.getCalls, service.batchNoCalls)
	}
}

func TestAllocateBatchNoFallsBackToVersionService(t *testing.T) {
	service := &fakeVersions{version: seededVersion()}
	allocated, err := New(service).AllocateBatchNo(context.Background(), 7)
	if err != nil || allocated != 4 {
		t.Fatalf("allocated = %d, err = %v", allocated, err)
	}
	if service.getCalls != 1 || len(service.batchNoCalls) != 1 {
		t.Fatalf("calls get=%d set=%+v", service.getCalls, service.batchNoCalls)
	}
	if got := service.batchNoCalls[0]; got.versionID != 7 || got.batchNo != 4 {
		t.Fatalf("SetBatchNo args = %+v", got)
	}
}

func TestAllocateBatchNoMapsVersionError(t *testing.T) {
	service := &fakeVersions{getErr: versiondomain.NewStateConflict("version_state_conflict", "not executing")}
	_, err := New(service).AllocateBatchNo(context.Background(), 7)
	var reasonErr *executiondomain.ReasonError
	if !errors.As(err, &reasonErr) || reasonErr.Code != "version_state_conflict" {
		t.Fatalf("error = %v", err)
	}
}

func TestReleaseBatchNoDelegatesToNativeAllocator(t *testing.T) {
	service := &atomicVersions{fakeVersions: &fakeVersions{}, releaseErr: errors.New("boom")}
	err := New(service).ReleaseBatchNo(context.Background(), 7, 4)
	if !errors.Is(err, service.releaseErr) {
		t.Fatalf("error = %v", err)
	}
	if len(service.released) != 1 || service.released[0] != 4 {
		t.Fatalf("released = %v", service.released)
	}
}

// The version application surface has no decrement, so the fallback release is
// a tolerated no-op: batch numbers are monotonic rather than gapless (T12 port
// contract). A future native allocator is preferred by the test above.
func TestReleaseBatchNoWithoutNativeAllocatorIsNoOp(t *testing.T) {
	if err := New(&fakeVersions{}).ReleaseBatchNo(context.Background(), 7, 4); err != nil {
		t.Fatalf("release = %v, want nil", err)
	}
}

func TestAdapterRejectsNilService(t *testing.T) {
	adapter := New(nil)
	if _, err := adapter.GetVersion(context.Background(), 7); err == nil {
		t.Fatal("GetVersion with a nil service = nil error")
	}
	if err := adapter.MarkPaused(context.Background(), 7, true, "graceful", "", 1); err == nil {
		t.Fatal("MarkPaused with a nil service = nil error")
	}
	if _, err := adapter.AllocateBatchNo(context.Background(), 7); err == nil {
		t.Fatal("AllocateBatchNo with a nil service = nil error")
	}
	if err := adapter.SetContinuePhase(context.Background(), 7, versiondomain.ContinuePhaseExecuting); err == nil {
		t.Fatal("SetContinuePhase with a nil service = nil error")
	}
}

var _ executionapp.VersionPort = (*Adapter)(nil)
var _ executionapp.ContinuePhasePort = (*Adapter)(nil)
