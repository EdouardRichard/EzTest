package gitx_test

import (
	"context"
	"strings"
	"testing"
)

func TestReadFileWindowAndEscape(t *testing.T) {
	fixture := newGitFixture(t)
	client := newClient(t)
	ctx := context.Background()

	window, err := client.ReadFile(ctx, fixture.dir, fixture.c1, "src/a.ts", 2, 3, 0)
	if err != nil {
		t.Fatalf("ReadFile window: %v", err)
	}
	if window.Path != "src/a.ts" || window.Commit != fixture.c1 {
		t.Fatalf("window identity = %+v", window)
	}
	if strings.Join(window.Lines, "|") != "line2|line3" {
		t.Fatalf("window lines = %v", window.Lines)
	}
	if window.StartLine != 2 || window.EndLine != 3 || window.TotalLines != 5 || window.Truncated {
		t.Fatalf("window bounds = %+v", window)
	}

	whole, err := client.ReadFile(ctx, fixture.dir, fixture.c1, "src/a.ts", 0, 0, 0)
	if err != nil {
		t.Fatalf("ReadFile whole: %v", err)
	}
	if len(whole.Lines) != 5 || whole.StartLine != 1 || whole.EndLine != 5 {
		t.Fatalf("whole window = %+v", whole)
	}

	if _, err := client.ReadFile(ctx, fixture.dir, fixture.c1, "../secret.txt", 1, 0, 0); errorCode(t, err) != "path_escape" {
		t.Fatalf("traversal code = %q", errorCode(t, err))
	}
	if _, err := client.ReadFile(ctx, fixture.dir, fixture.c1, "C:/secret.txt", 1, 0, 0); errorCode(t, err) != "path_escape" {
		t.Fatalf("absolute code = %q", errorCode(t, err))
	}
	if _, err := client.ReadFile(ctx, fixture.dir, fixture.c1, "src\\a.ts", 1, 0, 0); errorCode(t, err) != "path_escape" {
		t.Fatalf("backslash code = %q", errorCode(t, err))
	}
	if _, err := client.ReadFile(ctx, fixture.dir, fixture.c1, "src", 1, 0, 0); errorCode(t, err) != "path_not_file" {
		t.Fatalf("directory code = %q", errorCode(t, err))
	}
	if _, err := client.ReadFile(ctx, fixture.dir, fixture.c1, "src/missing.ts", 1, 0, 0); errorCode(t, err) != "path_not_found" {
		t.Fatalf("missing path code = %q", errorCode(t, err))
	}
	if _, err := client.ReadFile(ctx, fixture.dir, fixture.c1, "src/assets/logo.bin", 1, 0, 0); errorCode(t, err) != "binary_file" {
		t.Fatalf("binary code = %q", errorCode(t, err))
	}
	if _, err := client.ReadFile(ctx, fixture.dir, fixture.c1, "src/link.ts", 1, 0, 0); errorCode(t, err) != "unsupported_content" {
		t.Fatalf("symlink code = %q", errorCode(t, err))
	}

	// The 400-line hard cap applies to the API window.
	big, err := client.ReadFile(ctx, fixture.dir, fixture.c1, "src/big.txt", 0, 0, 0)
	if err != nil {
		t.Fatalf("ReadFile big: %v", err)
	}
	if len(big.Lines) != 400 || !big.Truncated || big.TotalLines != 450 {
		t.Fatalf("big window = %d lines, truncated=%v, total=%d", len(big.Lines), big.Truncated, big.TotalLines)
	}

	// A tiny byte budget truncates on a valid UTF-8 boundary.
	longFixture := newGitFixture(t)
	writeFile(t, longFixture.dir, "src/long.txt", "abcdefghijklmno\n")
	commitAll(t, longFixture.dir, "long file")
	longHead, err := client.HeadCommit(ctx, longFixture.dir, "")
	if err != nil {
		t.Fatalf("HeadCommit long: %v", err)
	}
	long, err := client.ReadFile(ctx, longFixture.dir, longHead, "src/long.txt", 1, 0, 5)
	if err != nil {
		t.Fatalf("ReadFile long: %v", err)
	}
	if !long.Truncated || len(strings.Join(long.Lines, "\n")) > 5 {
		t.Fatalf("byte-capped window = %+v", long)
	}
}

func TestReadFileWindowBulkForAnalysis(t *testing.T) {
	fixture := newGitFixture(t)
	client := newClient(t)
	ctx := context.Background()

	window, err := client.ReadFileWindow(ctx, fixture.dir, fixture.c1, "src/big.txt", 1<<20, 0)
	if err != nil {
		t.Fatalf("ReadFileWindow: %v", err)
	}
	if len(window.Lines) != 450 || window.Truncated {
		t.Fatalf("bulk window = %d lines, truncated=%v", len(window.Lines), window.Truncated)
	}

	if _, err := client.ReadFileWindow(ctx, fixture.dir, fixture.c1, "src/big.txt", 16, 0); errorCode(t, err) != "file_too_large" {
		t.Fatalf("large file code = %q", errorCode(t, err))
	}
	if _, err := client.ReadFileWindow(ctx, fixture.dir, fixture.c1, "src/assets/logo.bin", 1<<20, 0); errorCode(t, err) != "binary_file" {
		t.Fatalf("bulk binary code = %q", errorCode(t, err))
	}
}

func TestFileDiffLimits(t *testing.T) {
	fixture := newGitFixture(t)
	client := newClient(t)
	ctx := context.Background()

	modified, err := client.FileDiff(ctx, fixture.dir, fixture.c1, fixture.c2, "src/renamed a.ts", 1<<20)
	if err != nil {
		t.Fatalf("FileDiff renamed file: %v", err)
	}
	if !strings.Contains(modified.Patch, "diff --git") || modified.Truncated || modified.Binary || modified.Deleted {
		t.Fatalf("renamed diff = %+v", modified)
	}

	added, err := client.FileDiff(ctx, fixture.dir, fixture.c1, fixture.c2, "src/new.ts", 1<<20)
	if err != nil {
		t.Fatalf("FileDiff added: %v", err)
	}
	if !strings.Contains(added.Patch, "export const added") {
		t.Fatalf("added patch = %q", added.Patch)
	}

	deleted, err := client.FileDiff(ctx, fixture.dir, fixture.c1, fixture.c2, "src/old.ts", 1<<20)
	if err != nil {
		t.Fatalf("FileDiff deleted: %v", err)
	}
	if !deleted.Deleted || deleted.Patch != "" {
		t.Fatalf("deleted diff = %+v", deleted)
	}

	binary, err := client.FileDiff(ctx, fixture.dir, fixture.c1, fixture.c2, "src/assets/logo.bin", 1<<20)
	if err != nil {
		t.Fatalf("FileDiff binary: %v", err)
	}
	if !binary.Binary || binary.Patch != "" {
		t.Fatalf("binary diff = %+v", binary)
	}

	truncated, err := client.FileDiff(ctx, fixture.dir, fixture.c1, fixture.c2, "src/new.ts", 8)
	if err != nil {
		t.Fatalf("FileDiff truncated: %v", err)
	}
	if !truncated.Truncated || truncated.Patch != "" {
		t.Fatalf("truncated diff = %+v", truncated)
	}

	if _, err := client.FileDiff(ctx, fixture.dir, fixture.c1, fixture.c2, "../escape", 1<<20); errorCode(t, err) != "path_escape" {
		t.Fatalf("FileDiff escape code = %q", errorCode(t, err))
	}
}
