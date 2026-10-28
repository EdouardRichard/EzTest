package version_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	mindmapapp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	mindmapdomain "github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
	mindmapversion "github.com/EdouardRichard/EzTest/backend/internal/mindmap/infrastructure/version"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

const baselineCommit = "0123456789abcdef0123456789abcdef01234567"

// fakeVersionService records the calls of the mindmap lifecycle adapter and
// returns the configured result. It is the narrow version-service slice the
// adapter consumes.
type fakeVersionService struct {
	version   *versionapp.Version
	err       error
	calls     []string
	versionID int64
	actor     int64
}

// Get implements the version read.
func (f *fakeVersionService) Get(_ context.Context, id int64) (*versionapp.Version, error) {
	f.calls = append(f.calls, "Get")
	f.versionID = id
	if f.err != nil {
		return nil, f.err
	}
	return f.version, nil
}

// result records one transition and returns the configured result.
func (f *fakeVersionService) result(name string, id, actor int64) (*versionapp.Version, error) {
	f.calls = append(f.calls, name)
	f.versionID = id
	f.actor = actor
	if f.err != nil {
		return nil, f.err
	}
	return f.version, nil
}

// EnterReview implements the version state transition.
func (f *fakeVersionService) EnterReview(_ context.Context, id, actor int64) (*versionapp.Version, error) {
	return f.result("EnterReview", id, actor)
}

// RejectReview implements the version state transition.
func (f *fakeVersionService) RejectReview(_ context.Context, id, actor int64) (*versionapp.Version, error) {
	return f.result("RejectReview", id, actor)
}

// ApproveBaseline implements the version state transition.
func (f *fakeVersionService) ApproveBaseline(_ context.Context, id, actor int64) (*versionapp.Version, error) {
	return f.result("ApproveBaseline", id, actor)
}

// TestLifecycleAdapterReadsContinuePhase covers the optional P7
// ContinuePhaseReader the incremental_design gate and the incremental plan
// assert: the adapter returns version.continue_phase (empty when the version
// has none) and maps version errors.
func TestLifecycleAdapterReadsContinuePhase(t *testing.T) {
	ctx := context.Background()
	service := &fakeVersionService{version: &versionapp.Version{
		ID:            7,
		Status:        versiondomain.VersionContinuing,
		ContinuePhase: versiondomain.ContinuePhaseDesigning,
	}}
	adapter := mindmapversion.NewLifecycleAdapter(service)
	var _ mindmapapp.ContinuePhaseReader = adapter

	phase, err := adapter.ContinuePhase(ctx, 7)
	if err != nil {
		t.Fatalf("ContinuePhase: %v", err)
	}
	if phase != versiondomain.ContinuePhaseDesigning {
		t.Fatalf("phase = %q, want %q", phase, versiondomain.ContinuePhaseDesigning)
	}
	if service.versionID != 7 {
		t.Fatalf("version service received id %d want 7", service.versionID)
	}

	// A version without a continue phase (executing/completed) has an empty
	// phase rather than an error.
	service.version = &versionapp.Version{ID: 7, Status: versiondomain.VersionExecuting}
	phase, err = adapter.ContinuePhase(ctx, 7)
	if err != nil || phase != "" {
		t.Fatalf("phase/error = %q/%v, want empty/nil", phase, err)
	}

	service.err = versiondomain.NewNotFound("version", 7)
	_, err = adapter.ContinuePhase(ctx, 7)
	var mapped *mindmapdomain.Error
	if !errors.As(err, &mapped) || mapped.Kind != mindmapdomain.KindNotFound {
		t.Fatalf("ContinuePhase error=%v want a not_found mindmap error", err)
	}
}

// TestLifecycleAdapterMapsVersionSnapshot pins every field of the mindmap
// VersionSnapshot projection, including the optional resource bindings and the
// code baseline commit.
func TestLifecycleAdapterMapsVersionSnapshot(t *testing.T) {
	ctx := context.Background()
	systemID, environmentID, repositoryID := int64(11), int64(22), int64(33)
	service := &fakeVersionService{version: &versionapp.Version{
		ID:                 7,
		Name:               "登录需求",
		Status:             versiondomain.VersionDesigning,
		SystemID:           &systemID,
		EnvironmentID:      &environmentID,
		CodeRepositoryID:   &repositoryID,
		CodeBaselineCommit: stringPtr(baselineCommit),
	}}
	adapter := mindmapversion.NewLifecycleAdapter(service)
	var _ mindmapapp.VersionLifecycle = adapter

	snapshot, err := adapter.GetVersion(ctx, 7)
	if err != nil {
		t.Fatalf("GetVersion: %v", err)
	}
	if snapshot.ID != 7 || snapshot.Name != "登录需求" || snapshot.Status != versiondomain.VersionDesigning {
		t.Fatalf("snapshot=%+v want id/name/status of version 7", snapshot)
	}
	if snapshot.SystemID == nil || *snapshot.SystemID != systemID {
		t.Fatalf("SystemID=%v want %d", snapshot.SystemID, systemID)
	}
	if snapshot.EnvironmentID == nil || *snapshot.EnvironmentID != environmentID {
		t.Fatalf("EnvironmentID=%v want %d", snapshot.EnvironmentID, environmentID)
	}
	if snapshot.CodeRepositoryID == nil || *snapshot.CodeRepositoryID != repositoryID {
		t.Fatalf("CodeRepositoryID=%v want %d", snapshot.CodeRepositoryID, repositoryID)
	}
	if snapshot.CodeBaselineCommit == nil || *snapshot.CodeBaselineCommit != baselineCommit {
		t.Fatalf("CodeBaselineCommit=%v want %q", snapshot.CodeBaselineCommit, baselineCommit)
	}
	if service.versionID != 7 {
		t.Fatalf("version service received id %d want 7", service.versionID)
	}
}

// TestLifecycleAdapterTransitionsDriveTheVersionService pins that each lifecycle
// entry point delegates to the matching version action with the caller actor and
// maps the returned version.
func TestLifecycleAdapterTransitionsDriveTheVersionService(t *testing.T) {
	ctx := context.Background()
	service := &fakeVersionService{version: &versionapp.Version{ID: 9, Status: versiondomain.VersionReviewing}}
	adapter := mindmapversion.NewLifecycleAdapter(service)

	snapshot, err := adapter.EnterReview(ctx, 9, 42)
	if err != nil {
		t.Fatalf("EnterReview: %v", err)
	}
	if snapshot.Status != versiondomain.VersionReviewing {
		t.Fatalf("EnterReview status=%q", snapshot.Status)
	}

	if _, err := adapter.RejectReview(ctx, 9, 43); err != nil {
		t.Fatalf("RejectReview: %v", err)
	}
	if _, err := adapter.ApproveBaseline(ctx, 9, 44); err != nil {
		t.Fatalf("ApproveBaseline: %v", err)
	}

	want := []string{"EnterReview", "RejectReview", "ApproveBaseline"}
	if len(service.calls) != len(want) {
		t.Fatalf("calls=%v want %v", service.calls, want)
	}
	for i, name := range want {
		if service.calls[i] != name {
			t.Fatalf("calls=%v want %v", service.calls, want)
		}
	}
	if service.actor != 44 {
		t.Fatalf("last transition actor=%d want 44", service.actor)
	}
	if service.versionID != 9 {
		t.Fatalf("last transition version id=%d want 9", service.versionID)
	}
}

// TestLifecycleAdapterMapsVersionDomainErrors pins the version -> mindmap error
// translation: not_found stays not_found and a state conflict keeps the
// version_state_conflict reason and the original message.
func TestLifecycleAdapterMapsVersionDomainErrors(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name       string
		input      error
		wantKind   string
		wantReason string
	}{
		{
			name:       "not found",
			input:      versiondomain.NewNotFound("version", 7),
			wantKind:   mindmapdomain.KindNotFound,
			wantReason: "not_found",
		},
		{
			name:       "state conflict",
			input:      versiondomain.NewStateConflict("version_state_conflict", "version 7 is executing, approve-baseline requires reviewing"),
			wantKind:   mindmapdomain.KindStateConflict,
			wantReason: "version_state_conflict",
		},
		{
			name:       "invalid",
			input:      versiondomain.NewInvalid("versionId", "invalid_bindings", "bindings are invalid"),
			wantKind:   mindmapdomain.KindInvalid,
			wantReason: "invalid_bindings",
		},
		{
			name:       "conflict",
			input:      versiondomain.NewConflict("duplicate_relation", "an equivalent relation already exists"),
			wantKind:   mindmapdomain.KindConflict,
			wantReason: "duplicate_relation",
		},
		{
			name:       "dependency",
			input:      versiondomain.NewDependency("has_dependencies", "version still has requirements"),
			wantKind:   mindmapdomain.KindConflict,
			wantReason: "has_dependencies",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adapter := mindmapversion.NewLifecycleAdapter(&fakeVersionService{err: tc.input})
			_, err := adapter.ApproveBaseline(ctx, 7, 1)
			if err == nil {
				t.Fatal("ApproveBaseline error=nil want the mapped version error")
			}
			var mapped *mindmapdomain.Error
			if !errors.As(err, &mapped) {
				t.Fatalf("error %T is not a mindmap domain error", err)
			}
			if mapped.Kind != tc.wantKind || mapped.Reason != tc.wantReason {
				t.Fatalf("kind/reason=%q/%q want %q/%q", mapped.Kind, mapped.Reason, tc.wantKind, tc.wantReason)
			}
			if !strings.Contains(mapped.Error(), tc.input.Error()) {
				t.Fatalf("mapped message %q does not preserve %q", mapped.Error(), tc.input.Error())
			}
		})
	}
}

// TestLifecycleAdapterKeepsOperationalErrors pins that a non-domain failure is
// returned unchanged so it surfaces as a 500 rather than a 4xx.
func TestLifecycleAdapterKeepsOperationalErrors(t *testing.T) {
	ctx := context.Background()
	operational := errors.New("database is down")
	adapter := mindmapversion.NewLifecycleAdapter(&fakeVersionService{err: operational})
	_, err := adapter.GetVersion(ctx, 7)
	if !errors.Is(err, operational) {
		t.Fatalf("GetVersion error=%v want the operational error", err)
	}
}

// TestLifecycleAdapterNilVersionIsNotFound pins that a nil version without an
// error is still reported as not_found instead of a zero snapshot.
func TestLifecycleAdapterNilVersionIsNotFound(t *testing.T) {
	ctx := context.Background()
	adapter := mindmapversion.NewLifecycleAdapter(&fakeVersionService{})
	_, err := adapter.GetVersion(ctx, 7)
	var mapped *mindmapdomain.Error
	if !errors.As(err, &mapped) || mapped.Kind != mindmapdomain.KindNotFound {
		t.Fatalf("GetVersion error=%v want a not_found mindmap error", err)
	}
}

// stringPtr returns a pointer to value.
func stringPtr(value string) *string { return &value }
