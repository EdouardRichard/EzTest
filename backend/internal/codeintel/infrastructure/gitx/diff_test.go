package gitx_test

import (
	"context"
	"testing"
	"time"

	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/gitx"
)

func diffByPath(t *testing.T, files []codeintel.ChangedFile) map[string]codeintel.ChangedFile {
	t.Helper()
	byPath := make(map[string]codeintel.ChangedFile, len(files))
	for _, file := range files {
		byPath[file.Path] = file
	}
	return byPath
}

func TestDiffParsesRawAndNumstat(t *testing.T) {
	fixture := newGitFixture(t)
	client := newClient(t)
	ctx := context.Background()

	result, err := client.Diff(ctx, fixture.dir, fixture.c1, fixture.c2, 100)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if result.Truncated {
		t.Fatal("Diff unexpectedly truncated")
	}
	byPath := diffByPath(t, result.Files)

	renamed, ok := byPath["src/renamed a.ts"]
	if !ok {
		t.Fatalf("renamed file missing from %v", result.Files)
	}
	if renamed.Status != "renamed" || renamed.OldPath != "src/a.ts" ||
		renamed.Additions != 0 || renamed.HeadBlobSHA == nil || len(*renamed.HeadBlobSHA) != 40 {
		t.Fatalf("renamed entry = %+v", renamed)
	}

	added, ok := byPath["src/new.ts"]
	if !ok || added.Status != "added" || added.Additions != 1 || added.Deletions != 0 {
		t.Fatalf("added entry = %+v (ok=%v)", added, ok)
	}

	deleted, ok := byPath["src/old.ts"]
	if !ok || deleted.Status != "deleted" || deleted.HeadBlobSHA != nil {
		t.Fatalf("deleted entry = %+v (ok=%v)", deleted, ok)
	}

	binary, ok := byPath["src/assets/logo.bin"]
	if !ok || !binary.Binary || binary.Status != "modified" {
		t.Fatalf("binary entry = %+v (ok=%v)", binary, ok)
	}

	weird, ok := byPath["src/weird name$().ts"]
	if !ok || weird.Status != "modified" || weird.Additions != 1 {
		t.Fatalf("shell-metacharacter path entry = %+v (ok=%v)", weird, ok)
	}

	sorted := true
	for index := 1; index < len(result.Files); index++ {
		if result.Files[index-1].Path > result.Files[index].Path {
			sorted = false
			break
		}
	}
	if !sorted {
		t.Fatalf("diff entries are not sorted by path: %+v", result.Files)
	}
}

func TestDiffTruncatesAtMax(t *testing.T) {
	fixture := newGitFixture(t)
	client := newClient(t)
	result, err := client.Diff(context.Background(), fixture.dir, fixture.c1, fixture.c2, 1)
	if err != nil {
		t.Fatalf("Diff truncated: %v", err)
	}
	if len(result.Files) != 1 || !result.Truncated {
		t.Fatalf("truncated diff = %d files, truncated=%v", len(result.Files), result.Truncated)
	}
}

func TestDiffRejectsUnknownCommits(t *testing.T) {
	fixture := newGitFixture(t)
	client := newClient(t)
	const missing = "0000000000000000000000000000000000000000"
	if _, err := client.Diff(context.Background(), fixture.dir, missing, fixture.c2, 10); errorCode(t, err) != "commit_not_found" {
		t.Fatalf("bad base code = %q", errorCode(t, err))
	}
	if _, err := client.Diff(context.Background(), fixture.dir, fixture.c1, missing, 10); errorCode(t, err) != "commit_not_found" {
		t.Fatalf("bad head code = %q", errorCode(t, err))
	}
}

func TestGitTimeoutIsPerCommand(t *testing.T) {
	fixture := newGitFixture(t)
	client := gitx.New(nil, time.Nanosecond)
	// A nanosecond timeout is enough to prove the timeout is enforced on every
	// command rather than only at construction time.
	_, err := client.Diff(context.Background(), fixture.dir, fixture.c1, fixture.c2, 10)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if code := errorCode(t, err); code != "git_unavailable" {
		t.Fatalf("timeout code = %q", code)
	}
}
