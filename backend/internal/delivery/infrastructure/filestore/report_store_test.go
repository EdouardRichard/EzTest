package filestore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestReportStoreNewRejectsEmptyRoot pins the configuration guard.
func TestReportStoreNewRejectsEmptyRoot(t *testing.T) {
	if _, err := New("  "); err == nil {
		t.Fatal("empty root must be rejected")
	}
}

// TestReportStoreWriteAndOpen pins the happy path of spec §6.5: root-relative
// path, sha256/size return values, atomic file mode and exact read-back.
func TestReportStoreWriteAndOpen(t *testing.T) {
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	content := []byte("# 报告\n\n```mermaid\ngraph LR\n```\n")
	rel := "versions/10/reports/1000-r1.md"
	stored, digest, size, err := store.Write(context.Background(), rel, content)
	if err != nil {
		t.Fatalf("write report: %v", err)
	}
	if stored != rel {
		t.Fatalf("stored path = %q, want %q", stored, rel)
	}
	sum := sha256.Sum256(content)
	if want := hex.EncodeToString(sum[:]); digest != want {
		t.Fatalf("sha256 = %q, want %q", digest, want)
	}
	if size != int64(len(content)) {
		t.Fatalf("size = %d, want %d", size, len(content))
	}

	reader, err := store.Open(context.Background(), stored)
	if err != nil {
		t.Fatalf("open report: %v", err)
	}
	read, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close report: %v", err)
	}
	if string(read) != string(content) {
		t.Fatalf("read %q, want %q", read, content)
	}

	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("stat stored report: %v", err)
	}
	if runtime.GOOS != "windows" {
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("file mode = %o, want 600", mode)
		}
		dirInfo, err := os.Stat(filepath.Dir(filepath.Join(root, filepath.FromSlash(rel))))
		if err != nil {
			t.Fatalf("stat report dir: %v", err)
		}
		if mode := dirInfo.Mode().Perm(); mode != 0o700 {
			t.Fatalf("directory mode = %o, want 700", mode)
		}
	}

	// No temporary file may survive a successful atomic rename.
	leftovers, err := filepath.Glob(filepath.Join(root, "versions", "10", "reports", ".report-*"))
	if err != nil {
		t.Fatalf("glob temp files: %v", err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("temporary files left behind: %v", leftovers)
	}
}

// TestReportStoreRejectsUnsafePaths pins the root confinement checks of D30.
func TestReportStoreRejectsUnsafePaths(t *testing.T) {
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	tests := []string{
		"",
		"..",
		"../evil.md",
		"versions/../../evil.md",
		"/etc/evil.md",
		`C:\evil.md`,
		"versions//reports/r.md",
		"versions/./reports/r.md",
		"versions\\..\\reports\\r.md",
		"versions/10/reports/a:b.md",
		"versions/10/reports/a|b.md",
		strings.Repeat("a", 129) + "/r.md",
	}
	for _, rel := range tests {
		if _, _, _, err := store.Write(context.Background(), rel, []byte("x")); err == nil {
			t.Fatalf("unsafe path %q was accepted", rel)
		} else if !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("unsafe path %q error = %v, want ErrInvalidPath", rel, err)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read root: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("unsafe writes created entries: %v", entries)
	}
}

// TestReportStoreOpenMissingAndDirectory pins the not-found contract the export
// service maps to report_export_failed.
func TestReportStoreOpenMissingAndDirectory(t *testing.T) {
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, _, _, err := store.Write(context.Background(), "versions/10/reports/1000-r1.md", []byte("x")); err != nil {
		t.Fatalf("write report: %v", err)
	}
	if _, err := store.Open(context.Background(), "versions/10/reports/missing.md"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file error = %v, want fs.ErrNotExist", err)
	}
	if _, err := store.Open(context.Background(), "versions/10/reports"); err == nil {
		t.Fatal("opening a directory must be rejected")
	}
}

// TestReportStoreRejectsSymlinkEscape pins the symlink confinement of D30.
func TestReportStoreRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.md")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	dir := filepath.Join(root, "versions", "10", "reports")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create reports dir: %v", err)
	}
	link := filepath.Join(dir, "link.md")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	if _, err := store.Open(context.Background(), "versions/10/reports/link.md"); err == nil {
		t.Fatal("opening a symlink escaping the root must be rejected")
	}
	if _, _, _, err := store.Write(context.Background(), "versions/10/reports/link.md", []byte("x")); err == nil {
		t.Fatal("writing onto a symlink escaping the root must be rejected")
	}
}

// TestReportStoreHonoursContext pins the cancellation checks.
func TestReportStoreHonoursContext(t *testing.T) {
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, err := store.Write(ctx, "versions/10/reports/1000-r1.md", []byte("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("write error = %v, want context.Canceled", err)
	}
	if _, err := store.Open(ctx, "versions/10/reports/1000-r1.md"); !errors.Is(err, context.Canceled) {
		t.Fatalf("open error = %v, want context.Canceled", err)
	}
}
