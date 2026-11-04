package main

// The requirement cleanup writes (draft delete/merge/dedupe) are scoped by the
// version reader: the service asks it whether the version exists before it
// retires anything. The composition root must wire that reader; a nil reader
// would silently turn a missing version into an empty 200 dedupe response
// instead of the typed 404 the HTTP contract promises. These tests pin the
// composition-root wiring helper.

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// absentVersionReader is the production VersionReader shape for the test: the
// version does not exist, so every lookup is a typed 404.
type absentVersionReader struct{}

func (absentVersionReader) Get(context.Context, int64) (versionapp.VersionView, error) {
	return versionapp.VersionView{}, platform.NotFound("version")
}

func (absentVersionReader) MarkRunning(context.Context, int64) error { return nil }

func TestNewRequirementServiceRequiresVersionReader(t *testing.T) {
	service, err := newRequirementService(nil, nil, nil, nil, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("requirement service wired without a version reader")
	}
	if service != nil {
		t.Fatalf("service = %#v, want nil when the version reader is missing", service)
	}
}

// TestNewRequirementServiceChecksVersionExistence proves the composition-root
// helper passes the version reader through and that Dedupe consults it before
// touching the repository: the repository is nil here, so skipping the version
// check would panic instead of returning the typed 404.
func TestNewRequirementServiceChecksVersionExistence(t *testing.T) {
	service, err := newRequirementService(nil, absentVersionReader{}, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("wire requirement service: %v", err)
	}
	if service == nil {
		t.Fatal("requirement service is nil")
	}

	_, err = service.Dedupe(context.Background(), 999, 42)
	var apiError *platform.Error
	if !errors.As(err, &apiError) {
		t.Fatalf("Dedupe(unknown version) error = %v, want a typed platform error", err)
	}
	if apiError.Status != http.StatusNotFound {
		t.Fatalf("Dedupe(unknown version) status = %d, want 404 (%v)", apiError.Status, apiError)
	}
}
