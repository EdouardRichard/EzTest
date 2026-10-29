package version

import (
	"context"
	"errors"
	"reflect"
	"testing"

	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// fakeVersionReader is the version application slice the adapter consumes.
type fakeVersionReader struct {
	versions map[int64]*versionapp.Version
	err      error
	gotIDs   []int64
}

func (f *fakeVersionReader) Get(_ context.Context, id int64) (*versionapp.Version, error) {
	f.gotIDs = append(f.gotIDs, id)
	if f.err != nil {
		return nil, f.err
	}
	version, ok := f.versions[id]
	if !ok {
		return nil, errors.New("version not found")
	}
	return version, nil
}

// TestVersionNamesResolvesDistinctIDs proves the adapter maps every requested
// version to its display name and never resolves the same id twice.
func TestVersionNamesResolvesDistinctIDs(t *testing.T) {
	reader := &fakeVersionReader{versions: map[int64]*versionapp.Version{
		101: {ID: 101, Name: "checkout v1"},
		202: {ID: 202, Name: "reports v2"},
	}}
	names, err := NewVersionNamesReader(reader).VersionNames(context.Background(),
		[]int64{101, 202, 101, 0, -1})
	if err != nil {
		t.Fatalf("VersionNames = %v", err)
	}
	want := map[int64]string{101: "checkout v1", 202: "reports v2"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("names=%v, want %v", names, want)
	}
	if !reflect.DeepEqual(reader.gotIDs, []int64{101, 202}) {
		t.Fatalf("lookups=%v, want each positive id once", reader.gotIDs)
	}
}

// TestVersionNamesEmptySkipsLookup proves an empty page issues no version read.
func TestVersionNamesEmptySkipsLookup(t *testing.T) {
	reader := &fakeVersionReader{err: errors.New("must not be called")}
	names, err := NewVersionNamesReader(reader).VersionNames(context.Background(), nil)
	if err != nil {
		t.Fatalf("VersionNames = %v", err)
	}
	if len(names) != 0 || len(reader.gotIDs) != 0 {
		t.Fatalf("names=%v lookups=%v", names, reader.gotIDs)
	}
}

// TestVersionNamesPropagatesError proves a version-context failure is returned
// so the HTTP layer maps it to a 500 instead of rendering a blank versionName.
func TestVersionNamesPropagatesError(t *testing.T) {
	reader := &fakeVersionReader{err: errors.New("version service down")}
	if _, err := NewVersionNamesReader(reader).VersionNames(context.Background(),
		[]int64{101}); err == nil {
		t.Fatal("VersionNames must return the version reader error")
	}
}

// TestVersionNamesNilReaderFailsClosed proves a mis-wired adapter fails instead
// of panicking.
func TestVersionNamesNilReaderFailsClosed(t *testing.T) {
	if _, err := NewVersionNamesReader(nil).VersionNames(context.Background(), []int64{101}); err == nil {
		t.Fatal("a nil version reader must fail")
	}
}
