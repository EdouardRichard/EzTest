package version

import (
	"context"
	"errors"
	"testing"

	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// issueVersionStub is the version application slice the adapter consumes.
type issueVersionStub struct {
	version *versionapp.Version
	err     error
	gotID   int64
	calls   int
}

func (s *issueVersionStub) Get(_ context.Context, id int64) (*versionapp.Version, error) {
	s.calls++
	s.gotID = id
	return s.version, s.err
}

// TestIssueVersionReaderExistingVersion proves an existing version is reported
// as present through the narrow VersionExists port.
func TestIssueVersionReaderExistingVersion(t *testing.T) {
	stub := &issueVersionStub{version: &versionapp.Version{ID: 101}}
	reader := NewIssueVersionReader(stub)

	exists, err := reader.VersionExists(context.Background(), 101)
	if err != nil {
		t.Fatalf("VersionExists = %v", err)
	}
	if !exists {
		t.Fatal("VersionExists = false, want true")
	}
	if stub.calls != 1 || stub.gotID != 101 {
		t.Fatalf("lookups = %d/%d, want one lookup of 101", stub.calls, stub.gotID)
	}
}

// TestIssueVersionReaderMissingVersion proves a version not-found domain error
// is the expected false result, not an internal failure.
func TestIssueVersionReaderMissingVersion(t *testing.T) {
	stub := &issueVersionStub{err: versiondomain.NewNotFound("version", 4040)}
	exists, err := NewIssueVersionReader(stub).VersionExists(context.Background(), 4040)
	if err != nil {
		t.Fatalf("VersionExists = %v, want the missing version to be false,nil", err)
	}
	if exists {
		t.Fatal("VersionExists = true, want false")
	}
}

// TestIssueVersionReaderNilVersion proves a nil version projection without an
// error is treated as missing rather than panicking.
func TestIssueVersionReaderNilVersion(t *testing.T) {
	exists, err := NewIssueVersionReader(&issueVersionStub{}).VersionExists(context.Background(), 101)
	if err != nil {
		t.Fatalf("VersionExists = %v", err)
	}
	if exists {
		t.Fatal("VersionExists = true, want false")
	}
}

// TestIssueVersionReaderPropagatesLookupFailure proves an infrastructure error
// is not mistaken for a missing version.
func TestIssueVersionReaderPropagatesLookupFailure(t *testing.T) {
	lookupErr := errors.New("version store unavailable")
	_, err := NewIssueVersionReader(&issueVersionStub{err: lookupErr}).
		VersionExists(context.Background(), 101)
	if !errors.Is(err, lookupErr) {
		t.Fatalf("VersionExists = %v, want the lookup error", err)
	}
}

// TestIssueVersionReaderUnconfigured proves an unwired service fails closed.
func TestIssueVersionReaderUnconfigured(t *testing.T) {
	readers := []*IssueVersionReader{nil, NewIssueVersionReader(nil)}
	for _, reader := range readers {
		exists, err := reader.VersionExists(context.Background(), 101)
		if err == nil {
			t.Fatal("VersionExists without a version service returned no error")
		}
		if exists {
			t.Fatal("VersionExists without a version service = true")
		}
	}
}
