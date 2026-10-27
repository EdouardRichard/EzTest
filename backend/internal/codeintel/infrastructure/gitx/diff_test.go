package gitx_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/gitx"
)

// TestDiffStatuses pins the two-command file-level diff: statuses, rename old
// paths, binary detection, head blob hashes, -z special file names and ordering.
func TestDiffStatuses(t *testing.T) {
	fx := newTestRepo(t)
	r := gitx.NewGitReader(gitx.GitConfig{CommandTimeout: 5 * time.Second})
	ctx := context.Background()

	changes, err := r.Diff(ctx, fx.dir, fx.commit1, fx.commit2)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if changes.Truncated {
		t.Fatal("Diff.Truncated=true want false")
	}
	if changes.ChangeSetID != nil {
		t.Fatalf("Diff.ChangeSetID=%v want nil for a preview read", *changes.ChangeSetID)
	}
	if len(changes.Files) == 0 {
		t.Fatal("Diff returned no changed files")
	}

	index := make(map[string]domain.ChangedFile, len(changes.Files))
	for _, file := range changes.Files {
		if _, duplicate := index[file.Path]; duplicate {
			t.Fatalf("Diff returned duplicate path %q", file.Path)
		}
		index[file.Path] = file
	}
	for i := 1; i < len(changes.Files); i++ {
		if changes.Files[i-1].Path >= changes.Files[i].Path {
			t.Fatalf("Diff files are not path sorted: %q then %q", changes.Files[i-1].Path, changes.Files[i].Path)
		}
	}

	refund, ok := index["src/api/refund.ts"]
	if !ok {
		t.Fatalf("Diff missing modified src/api/refund.ts (got %v)", keys(index))
	}
	wantHash := "git-sha1:" + runGit(t, fx.dir, "rev-parse", fx.commit2+":src/api/refund.ts")
	if refund.Status != "M" || refund.Additions == 0 || refund.Deletions == 0 || refund.Binary {
		t.Fatalf("refund change=%+v want modified non-binary with stats", refund)
	}
	if refund.HeadBlobHash != wantHash {
		t.Fatalf("refund HeadBlobHash=%q want %q", refund.HeadBlobHash, wantHash)
	}

	added, ok := index["src/api/refund.test.ts"]
	if !ok || added.Status != "A" || added.OldPath != nil {
		t.Fatalf("added change=%+v present=%v want status A without old path", added, ok)
	}

	deleted, ok := index["docs/guide.md"]
	if !ok {
		t.Fatalf("Diff missing deleted docs/guide.md (got %v)", keys(index))
	}
	if deleted.Status != "D" || deleted.HeadBlobHash != "" || deleted.Additions != 0 || deleted.Deletions != 3 {
		t.Fatalf("deleted change=%+v want status D, empty head hash, 3 deletions", deleted)
	}

	renamed, ok := index["src/api/refund-v2.ts"]
	if !ok {
		t.Fatalf("Diff missing renamed src/api/refund-v2.ts (got %v)", keys(index))
	}
	if renamed.Status != "R100" || renamed.OldPath == nil || *renamed.OldPath != "src/api/legacy.ts" {
		t.Fatalf("renamed change=%+v want status R100 oldPath src/api/legacy.ts", renamed)
	}
	if !strings.HasPrefix(renamed.HeadBlobHash, "git-sha1:") {
		t.Fatalf("renamed HeadBlobHash=%q want a git-sha1 hash", renamed.HeadBlobHash)
	}

	logo, ok := index["assets/logo.png"]
	if !ok || !logo.Binary || logo.Status != "M" || logo.Additions != 0 || logo.Deletions != 0 {
		t.Fatalf("binary change=%+v present=%v want status M binary with no line stats", logo, ok)
	}

	notes, ok := index["src/notes/说明 文档.md"]
	if !ok || notes.Status != "M" {
		t.Fatalf("special-name change=%+v present=%v want status M", notes, ok)
	}

	var stats struct {
		Additions    int  `json:"additions"`
		ChangedFiles int  `json:"changedFiles"`
		Deletions    int  `json:"deletions"`
		Truncated    bool `json:"truncated"`
	}
	if len(changes.Stats) == 0 {
		t.Fatal("Diff.Stats is empty")
	}
	if err := json.Unmarshal(changes.Stats, &stats); err != nil {
		t.Fatalf("Diff.Stats is not JSON: %v (%s)", err, changes.Stats)
	}
	if stats.ChangedFiles != len(changes.Files) || stats.Truncated {
		t.Fatalf("Diff.Stats=%+v want changedFiles=%d truncated=false", stats, len(changes.Files))
	}

	same, err := r.Diff(ctx, fx.dir, fx.commit2, fx.commit2)
	if err != nil {
		t.Fatalf("Diff(same commit): %v", err)
	}
	if len(same.Files) != 0 || same.Truncated {
		t.Fatalf("Diff(same commit)=%+v want no changes", same)
	}

	_, err = r.Diff(ctx, fx.dir, strings.Repeat("0", 40), fx.commit2)
	requireDomainInvalid(t, err, "commit_not_found")
}

// TestDiffTruncatesAt2000 pins the stored change-set bound: the listing is
// sorted by path and cut at domain.MaxChangedFilesStored with truncated=true.
func TestDiffTruncatesAt2000(t *testing.T) {
	if testing.Short() {
		t.Skip("creates 2000+ files")
	}
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.email", testEmail)
	runGit(t, dir, "config", "user.name", testAuthor)
	writeRepoFile(t, dir, "base.txt", "base\n")
	base := commitRepo(t, dir, "base", commit1Date)

	const added = domain.MaxChangedFilesStored + 1
	for i := 0; i < added; i++ {
		writeRepoFile(t, dir, fmt.Sprintf("bulk/file-%04d.txt", i), "x\n")
	}
	head := commitRepo(t, dir, "bulk", commit2Date)

	r := gitx.NewGitReader(gitx.GitConfig{CommandTimeout: 10 * time.Second})
	changes, err := r.Diff(context.Background(), dir, base, head)
	if err != nil {
		t.Fatalf("Diff(bulk): %v", err)
	}
	if !changes.Truncated {
		t.Fatalf("Diff(bulk).Truncated=false want true for %d files", added)
	}
	if len(changes.Files) != domain.MaxChangedFilesStored {
		t.Fatalf("Diff(bulk) files=%d want %d", len(changes.Files), domain.MaxChangedFilesStored)
	}
	if changes.Files[0].Path != "bulk/file-0000.txt" {
		t.Fatalf("Diff(bulk) first path=%q want bulk/file-0000.txt", changes.Files[0].Path)
	}
	if last := changes.Files[len(changes.Files)-1].Path; last != "bulk/file-1999.txt" {
		t.Fatalf("Diff(bulk) last path=%q want bulk/file-1999.txt", last)
	}
}

// TestFileDiffStatusBinaryAndTruncation pins the on-demand unified diff:
// change-set status, binary/deleted empty patches, byte truncation and the
// not_changed rejection.
func TestFileDiffStatusBinaryAndTruncation(t *testing.T) {
	fx := newTestRepo(t)
	r := gitx.NewGitReader(gitx.GitConfig{CommandTimeout: 5 * time.Second})
	ctx := context.Background()

	text, err := r.FileDiff(ctx, fx.dir, fx.commit1, fx.commit2, "src/api/refund.ts", 1<<20)
	if err != nil {
		t.Fatalf("FileDiff(text): %v", err)
	}
	if text.BaseCommit != fx.commit1 || text.HeadCommit != fx.commit2 || text.Path != "src/api/refund.ts" {
		t.Fatalf("FileDiff(text)=%+v want pinned commits and path", text)
	}
	if text.Status != "M" || text.Binary || text.Truncated {
		t.Fatalf("FileDiff(text) status=%q binary=%v truncated=%v want M/false/false", text.Status, text.Binary, text.Truncated)
	}
	if !strings.Contains(text.Patch, "@@") || !strings.Contains(text.Patch, "export function refund") {
		t.Fatalf("FileDiff(text) patch=%q want a unified diff", text.Patch)
	}

	binary, err := r.FileDiff(ctx, fx.dir, fx.commit1, fx.commit2, "assets/logo.png", 1<<20)
	if err != nil {
		t.Fatalf("FileDiff(binary): %v", err)
	}
	if !binary.Binary || binary.Status != "M" || binary.Patch != "" || binary.Truncated {
		t.Fatalf("FileDiff(binary)=%+v want binary M with an empty un-truncated patch", binary)
	}

	deleted, err := r.FileDiff(ctx, fx.dir, fx.commit1, fx.commit2, "docs/guide.md", 1<<20)
	if err != nil {
		t.Fatalf("FileDiff(deleted): %v", err)
	}
	if deleted.Status != "D" || deleted.Patch != "" || deleted.Binary {
		t.Fatalf("FileDiff(deleted)=%+v want status D with an empty patch", deleted)
	}

	truncated, err := r.FileDiff(ctx, fx.dir, fx.commit1, fx.commit2, "src/api/refund.ts", 20)
	if err != nil {
		t.Fatalf("FileDiff(truncated): %v", err)
	}
	if !truncated.Truncated || len(truncated.Patch) != 20 {
		t.Fatalf("FileDiff(truncated) len=%d truncated=%v want exactly 20 bytes truncated", len(truncated.Patch), truncated.Truncated)
	}

	renamed, err := r.FileDiff(ctx, fx.dir, fx.commit1, fx.commit2, "src/api/refund-v2.ts", 1<<20)
	if err != nil {
		t.Fatalf("FileDiff(renamed): %v", err)
	}
	if renamed.Status != "R100" {
		t.Fatalf("FileDiff(renamed).Status=%q want R100 from the change set", renamed.Status)
	}

	_, err = r.FileDiff(ctx, fx.dir, fx.commit1, fx.commit2, "src/data/lines.txt", 1<<20)
	requireDomainInvalid(t, err, "not_changed")
	_, err = r.FileDiff(ctx, fx.dir, fx.commit1, fx.commit2, "../secret", 1<<20)
	requireDomainInvalid(t, err, "path_escape")
	_, err = r.FileDiff(ctx, fx.dir, fx.commit1, strings.Repeat("0", 40), "src/api/refund.ts", 1<<20)
	requireDomainInvalid(t, err, "commit_not_found")
}
