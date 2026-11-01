package orchestrationinfra

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type stubRootReader struct {
	root string
	err  error
}

func (s stubRootReader) String(context.Context, string) (string, error) {
	return s.root, s.err
}

func TestStorageProbeWritesAndRemovesProbeFile(t *testing.T) {
	root := t.TempDir()
	probe := NewStorageProbe(stubRootReader{root: root}, "host-a:42")
	if err := probe.Probe(context.Background()); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("probe left %d entries in the data root", len(entries))
	}

	// The probe must also create a missing root.
	nested := filepath.Join(root, "nested", "data")
	probe = NewStorageProbe(stubRootReader{root: nested}, "host-a:42")
	if err := probe.Probe(context.Background()); err != nil {
		t.Fatalf("Probe missing root: %v", err)
	}
	if _, err := os.Stat(nested); err != nil {
		t.Fatalf("data root was not created: %v", err)
	}
}

func TestStorageProbePropagatesConfigurationAndWriteErrors(t *testing.T) {
	probe := NewStorageProbe(stubRootReader{err: errors.New("setting missing")}, "host-a:42")
	if err := probe.Probe(context.Background()); err == nil {
		t.Fatal("Probe succeeded without a data root setting")
	}

	file := filepath.Join(t.TempDir(), "data-root")
	if err := os.WriteFile(file, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	probe = NewStorageProbe(stubRootReader{root: file}, "host-a:42")
	if err := probe.Probe(context.Background()); err == nil {
		t.Fatal("Probe succeeded when the data root is a regular file")
	}
}

func TestStorageProbeRequiresInstanceKey(t *testing.T) {
	probe := NewStorageProbe(stubRootReader{root: t.TempDir()}, " ")
	if err := probe.Probe(context.Background()); err == nil {
		t.Fatal("Probe succeeded without an instance key")
	}
}
