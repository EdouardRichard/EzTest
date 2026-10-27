package gitx_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/gitx"
)

const (
	testAuthor = "Gitx Test"
	testEmail  = "gitx@example.com"

	commit1Date = "2026-01-01T00:00:00+0000"
	commit2Date = "2026-01-02T00:00:00+0000"
)

// repoFixture is a deterministic two-commit repository shared by the gitx
// tests. commit2 contains a modify, an add, a delete and a 100% rename on top
// of commit1, plus a symlink and a submodule entry in both commits.
type repoFixture struct {
	dir     string
	commit1 string
	commit2 string
}

// newTestRepo initialises a fixed two-commit repository. Every author,
// committer and timestamp is pinned so git object ids are reproducible.
func newTestRepo(t *testing.T) repoFixture {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.email", testEmail)
	runGit(t, dir, "config", "user.name", testAuthor)
	runGit(t, dir, "config", "core.autocrlf", "false")
	runGit(t, dir, "config", "core.safecrlf", "false")
	runGit(t, dir, "config", "commit.gpgsign", "false")

	writeRepoFile(t, dir, "src/api/refund.ts", "export function refund(): number {\n  return 1;\n}\n")
	writeRepoFile(t, dir, "src/api/legacy.ts", "export const legacy = 'old';\n")
	writeRepoFile(t, dir, "src/utils/format.ts", "export function format(value: string) {\n  return value.trim();\n}\n")
	writeRepoFile(t, dir, "src/notes/说明 文档.md", "# 说明\n\n第一版\n")
	writeRepoFile(t, dir, "docs/guide.md", "guide\nline two\nline three\n")
	writeRepoFile(t, dir, "src/styles/site.css", "first\r\nsecond\r\nthird\r\n")
	writeRepoFile(t, dir, "src/utf8/你好.txt", "第一行\n第二行\n第三行\n")
	writeRepoFile(t, dir, "src/data/lines.txt", "l1\nl2\nl3\nl4\n")
	writeRepoFile(t, dir, "src/data/leading-blank.txt", "\nhello\n")
	writeRepoFile(t, dir, "src/data/internal-blank.txt", "a\n\nb\n")
	writeRepoFile(t, dir, "src/data/blank-wide.txt", "\n"+strings.Repeat("a", domain.MaxFileReadBytes+1024)+"\n")
	writeRepoFile(t, dir, "src/data/wide.txt", strings.Repeat("a", 70000)+"\n")
	writeRepoFile(t, dir, "src/data/wide-utf8.txt", strings.Repeat("你", 30000)+"\n")
	writeRepoFile(t, dir, "src/data/empty.txt", "")
	writeRepoFile(t, dir, "src/notes/binary-note.txt", "note\n")
	writeRepoBytes(t, dir, "assets/logo.png", []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x01, 0x02, 0x03})
	writeRepoBytes(t, dir, "src/data/binary.bin", []byte{0x00, 0xff, 0xfe, 0x80, 0x81})
	writeRepoBytes(t, dir, "src/data/invalid-utf8.txt", []byte("ok\n\xff\n"))
	commit1 := commitRepo(t, dir, "first commit", commit1Date)

	writeRepoFile(t, dir, "src/api/refund.ts", "export function refund(): number {\n  return 2;\n}\n\nexport const fee = 3;\n")
	writeRepoFile(t, dir, "src/api/refund.test.ts", "import { refund } from './refund';\n")
	writeRepoFile(t, dir, "src/utils/format.ts", "export function format(value: string) {\n  return value.trim().toUpperCase();\n}\n")
	writeRepoFile(t, dir, "src/notes/说明 文档.md", "# 说明\n\n第二版\n")
	writeRepoFile(t, dir, "src/notes/binary-note.txt", "note\nBinary files a/logo.png and b/logo.png differ\n")
	writeRepoBytes(t, dir, "assets/logo.png", []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x09, 0x08, 0x07})
	if err := os.Remove(filepath.Join(dir, "docs", "guide.md")); err != nil {
		t.Fatalf("remove docs/guide.md: %v", err)
	}
	runGit(t, dir, "mv", "src/api/legacy.ts", "src/api/refund-v2.ts")
	commit2 := commitRepo(t, dir, "second commit", commit2Date)

	return repoFixture{dir: dir, commit1: commit1, commit2: commit2}
}

// runGit runs one git command with a fixed environment and returns its trimmed
// standard output.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return runGitCommand(t, dir, os.Environ(), "", args...)
}

// runGitEnv runs one git command with pinned author and committer dates.
func runGitEnv(t *testing.T, dir, date string, args ...string) string {
	t.Helper()
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME="+testAuthor,
		"GIT_AUTHOR_EMAIL="+testEmail,
		"GIT_COMMITTER_NAME="+testAuthor,
		"GIT_COMMITTER_EMAIL="+testEmail,
		"GIT_AUTHOR_DATE="+date,
		"GIT_COMMITTER_DATE="+date,
	)
	return runGitCommand(t, dir, env, "", args...)
}

// runGitInput runs one git command with stdin and returns its trimmed output.
func runGitInput(t *testing.T, dir, input string, args ...string) string {
	t.Helper()
	return runGitCommand(t, dir, os.Environ(), input, args...)
}

// runGitCommand is the shared git runner of the test helpers.
func runGitCommand(t *testing.T, dir string, env []string, input string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(env, "GIT_OPTIONAL_LOCKS=0")
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

// writeRepoFile writes one repository-relative text file.
func writeRepoFile(t *testing.T, dir, relative, content string) {
	t.Helper()
	writeRepoBytes(t, dir, relative, []byte(content))
}

// writeRepoBytes writes one repository-relative file verbatim.
func writeRepoBytes(t *testing.T, dir, relative string, content []byte) {
	t.Helper()
	absolute := filepath.Join(dir, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", relative, err)
	}
	if err := os.WriteFile(absolute, content, 0o644); err != nil {
		t.Fatalf("write %s: %v", relative, err)
	}
}

// commitRepo stages the work tree plus the synthetic symlink/submodule entries
// and commits with a pinned date. The synthetic entries keep the unsupported
// modes deterministic across platforms (no OS symlink privilege needed).
func commitRepo(t *testing.T, dir, message, date string) string {
	t.Helper()
	runGit(t, dir, "add", "-A")
	target := runGitInput(t, dir, "src/api/refund.ts\n", "hash-object", "-w", "--stdin")
	runGit(t, dir, "update-index", "--add", "--cacheinfo", "120000,"+target+",link.txt")
	runGit(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+strings.Repeat("1", 40)+",vendor/sub")
	runGitEnv(t, dir, date, "commit", "-q", "-m", message)
	return runGit(t, dir, "rev-parse", "HEAD")
}

// requireDomainInvalid asserts that err wraps domain.ErrInvalid and carries the
// expected stable reason.
func requireDomainInvalid(t *testing.T, err error, wantReason string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want domain.ErrInvalid reason %q, got nil", wantReason)
	}
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("error %v does not wrap domain.ErrInvalid", err)
	}
	if got := domain.Reason(err); got != wantReason {
		t.Fatalf("reason=%q want %q (error: %v)", got, wantReason, err)
	}
}

// requireOperationalError asserts that err is a non-domain operational git
// failure. Handlers map such errors to 503 git_unavailable (R19), so the error
// must not wrap domain.ErrInvalid or carry a domain reason.
func requireOperationalError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("want an operational git error, got nil")
	}
	if errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("error %v wraps domain.ErrInvalid; handlers would map it to 422 instead of 503 git_unavailable", err)
	}
	if reason := domain.Reason(err); reason != "" {
		t.Fatalf("operational error carries domain reason %q (error: %v)", reason, err)
	}
}

// entryIndex maps tree paths to entries.
func entryIndex(entries []domain.TreeEntry) map[string]domain.TreeEntry {
	index := make(map[string]domain.TreeEntry, len(entries))
	for _, entry := range entries {
		index[entry.Path] = entry
	}
	return index
}

// TestHeadAncestorAndCommit pins branch resolution, reachability and commit
// metadata.
func TestHeadAncestorAndCommit(t *testing.T) {
	fx := newTestRepo(t)
	r := gitx.NewGitReader(gitx.GitConfig{CommandTimeout: 5 * time.Second})
	ctx := context.Background()

	head, err := r.Head(ctx, fx.dir, "main")
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if head != fx.commit2 {
		t.Fatalf("Head=%q want %q", head, fx.commit2)
	}

	ancestor, err := r.IsAncestor(ctx, fx.dir, fx.commit1, fx.commit2)
	if err != nil {
		t.Fatalf("IsAncestor(base, head): %v", err)
	}
	if !ancestor {
		t.Fatal("IsAncestor(base, head)=false want true")
	}
	reverse, err := r.IsAncestor(ctx, fx.dir, fx.commit2, fx.commit1)
	if err != nil {
		t.Fatalf("IsAncestor(head, base): %v", err)
	}
	if reverse {
		t.Fatal("IsAncestor(head, base)=true want false")
	}
	same, err := r.IsAncestor(ctx, fx.dir, fx.commit1, fx.commit1)
	if err != nil || !same {
		t.Fatalf("IsAncestor(commit, commit)=%v, %v want true, nil", same, err)
	}

	info, err := r.Commit(ctx, fx.dir, fx.commit2)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if info.SHA != fx.commit2 || info.Message != "second commit" || info.Author != testAuthor {
		t.Fatalf("Commit=%+v want sha=%s message=%q author=%q", info, fx.commit2, "second commit", testAuthor)
	}
	wantAt := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	if !info.CommittedAt.Equal(wantAt) {
		t.Fatalf("CommittedAt=%s want %s", info.CommittedAt, wantAt)
	}

	_, err = r.Head(ctx, fx.dir, "no-such-branch")
	requireDomainInvalid(t, err, "invalid_head")
	_, err = r.Head(ctx, fx.dir, "--upload-pack=evil")
	requireDomainInvalid(t, err, "invalid_head")
	_, err = r.IsAncestor(ctx, fx.dir, "short", fx.commit2)
	requireDomainInvalid(t, err, "invalid_head")
	_, err = r.Commit(ctx, fx.dir, strings.Repeat("a", 40))
	requireDomainInvalid(t, err, "commit_not_found")
}

// TestHeadAcceptsGitValidBranchNames pins branch validation: names git accepts
// (for example the "+" separator) must resolve, while option-like names and
// revision operators must never reach git as flags or revision expressions.
func TestHeadAcceptsGitValidBranchNames(t *testing.T) {
	fx := newTestRepo(t)
	r := gitx.NewGitReader(gitx.GitConfig{CommandTimeout: 5 * time.Second})
	ctx := context.Background()

	const extended = "release+2026"
	runGit(t, fx.dir, "branch", extended, fx.commit1)
	head, err := r.Head(ctx, fx.dir, extended)
	if err != nil {
		t.Fatalf("Head(%q): %v", extended, err)
	}
	if head != fx.commit1 {
		t.Fatalf("Head(%q)=%q want %q", extended, head, fx.commit1)
	}

	rejected := []string{
		"main~1",
		"main^",
		"main:refs/heads/main",
		"main?",
		"main*",
		"main[",
		`main\`,
		"main..x",
		"release@{1}",
		"/main",
		"main/",
		"@",
		"main branch",
	}
	for _, branch := range rejected {
		_, err := r.Head(ctx, fx.dir, branch)
		requireDomainInvalid(t, err, "invalid_head")
	}
}

// TestReaderIgnoresInheritedGitRedirects pins the child environment: an
// inherited GIT_DIR/GIT_WORK_TREE must not redirect the adapter away from the
// validated localPath.
func TestReaderIgnoresInheritedGitRedirects(t *testing.T) {
	fx := newTestRepo(t)
	other := t.TempDir()
	runGit(t, other, "init", "-q", "-b", "main")
	runGit(t, other, "config", "user.email", testEmail)
	runGit(t, other, "config", "user.name", testAuthor)
	writeRepoFile(t, other, "other.txt", "other\n")
	otherHead := commitRepo(t, other, "other", commit1Date)
	if otherHead == fx.commit2 {
		t.Fatal("fixture repositories unexpectedly share a head commit")
	}
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)

	r := gitx.NewGitReader(gitx.GitConfig{CommandTimeout: 5 * time.Second})
	head, err := r.Head(context.Background(), fx.dir, "main")
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if head != fx.commit2 {
		t.Fatalf("Head=%q want %q (inherited GIT_DIR/GIT_WORK_TREE pointed at %q)", head, fx.commit2, otherHead)
	}
}

// TestCommitOutputOverflowIsOperational pins the 4 MiB output bound: overflow
// is a non-domain operational failure (503 git_unavailable), never a 422.
func TestCommitOutputOverflowIsOperational(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.email", testEmail)
	runGit(t, dir, "config", "user.name", testAuthor)
	writeRepoFile(t, dir, "base.txt", "base\n")
	runGit(t, dir, "add", "-A")
	runGitInput(t, dir, strings.Repeat("m", 4<<20+1024), "commit", "-q", "-F", "-")
	sha := runGit(t, dir, "rev-parse", "HEAD")

	r := gitx.NewGitReader(gitx.GitConfig{CommandTimeout: 10 * time.Second})
	_, err := r.Commit(context.Background(), dir, sha)
	requireOperationalError(t, err)
}

// TestTreeFiltersAndHashes pins the commit-pinned recursive listing: path
// globs, deterministic ordering, blob hashes/sizes, unsupported modes,
// prefixing, cursor pagination and limits.
func TestTreeFiltersAndHashes(t *testing.T) {
	fx := newTestRepo(t)
	r := gitx.NewGitReader(gitx.GitConfig{CommandTimeout: 5 * time.Second})
	ctx := context.Background()

	page, err := r.Tree(ctx, fx.dir, fx.commit1, "", []string{"src/**"}, []string{"assets/**"}, "", 100)
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	if page.Commit != fx.commit1 {
		t.Fatalf("Tree.Commit=%q want %q", page.Commit, fx.commit1)
	}
	if page.HasMore {
		t.Fatalf("Tree.HasMore=true want false (entries=%d)", len(page.Entries))
	}
	if len(page.Entries) == 0 {
		t.Fatal("Tree returned no entries")
	}
	previous := ""
	for _, entry := range page.Entries {
		if !strings.HasPrefix(entry.Path, "src/") {
			t.Fatalf("Tree(include src/**) leaked %q", entry.Path)
		}
		if previous != "" && entry.Path <= previous {
			t.Fatalf("Tree entries are not path sorted: %q then %q", previous, entry.Path)
		}
		previous = entry.Path
		if entry.Type == "blob" {
			if len(entry.GitObjectID) != 40 {
				t.Fatalf("blob %q GitObjectID=%q want 40 hex", entry.Path, entry.GitObjectID)
			}
			if entry.Unsupported {
				t.Fatalf("regular blob %q marked unsupported", entry.Path)
			}
			if entry.Size < 0 {
				t.Fatalf("regular blob %q size=%d want >=0", entry.Path, entry.Size)
			}
		}
	}

	index := entryIndex(page.Entries)
	refund, ok := index["src/api/refund.ts"]
	if !ok {
		t.Fatalf("Tree missing src/api/refund.ts (got %v)", keys(index))
	}
	wantHash := runGit(t, fx.dir, "rev-parse", fx.commit1+":src/api/refund.ts")
	wantSize := int64(len("export function refund(): number {\n  return 1;\n}\n"))
	if refund.GitObjectID != wantHash || refund.Type != "blob" || refund.Mode != "100644" || refund.Size != wantSize {
		t.Fatalf("refund entry=%+v want hash=%s size=%d mode=100644 blob", refund, wantHash, wantSize)
	}
	if _, ok := index["src/api/refund.test.ts"]; ok {
		t.Fatal("Tree(commit1) leaked a commit2 file")
	}

	excluded, err := r.Tree(ctx, fx.dir, fx.commit2, "", nil, []string{"assets/**"}, "", 200)
	if err != nil {
		t.Fatalf("Tree(exclude): %v", err)
	}
	for _, entry := range excluded.Entries {
		if entry.Path == "assets/logo.png" {
			t.Fatal("Tree(exclude assets/**) returned assets/logo.png")
		}
	}

	full, err := r.Tree(ctx, fx.dir, fx.commit2, "", nil, nil, "", 200)
	if err != nil {
		t.Fatalf("Tree(all): %v", err)
	}
	all := entryIndex(full.Entries)
	link, ok := all["link.txt"]
	if !ok || !link.Unsupported || link.Mode != "120000" {
		t.Fatalf("symlink entry=%+v present=%v want unsupported mode 120000", link, ok)
	}
	sub, ok := all["vendor/sub"]
	if !ok || !sub.Unsupported || sub.Type != "commit" || sub.Mode != "160000" {
		t.Fatalf("submodule entry=%+v present=%v want unsupported type=commit mode=160000", sub, ok)
	}

	prefixed, err := r.Tree(ctx, fx.dir, fx.commit2, "src/api", nil, nil, "", 200)
	if err != nil {
		t.Fatalf("Tree(prefix): %v", err)
	}
	prefixIndex := entryIndex(prefixed.Entries)
	if _, ok := prefixIndex["src/api/refund.ts"]; !ok {
		t.Fatalf("Tree(prefix src/api) missing refund.ts (got %v)", keys(prefixIndex))
	}
	for _, entry := range prefixed.Entries {
		if entry.Path != "src/api" && !strings.HasPrefix(entry.Path, "src/api/") {
			t.Fatalf("Tree(prefix src/api) leaked %q", entry.Path)
		}
	}

	first, err := r.Tree(ctx, fx.dir, fx.commit2, "", nil, nil, "", 2)
	if err != nil {
		t.Fatalf("Tree(page 1): %v", err)
	}
	if !first.HasMore || first.NextCursor == "" || len(first.Entries) != 2 {
		t.Fatalf("Tree(page 1) hasMore=%v cursor=%q entries=%d want more/cursor/2", first.HasMore, first.NextCursor, len(first.Entries))
	}
	second, err := r.Tree(ctx, fx.dir, fx.commit2, "", nil, nil, first.NextCursor, 2)
	if err != nil {
		t.Fatalf("Tree(page 2): %v", err)
	}
	if len(second.Entries) == 0 {
		t.Fatal("Tree(page 2) returned no entries")
	}
	if second.Entries[0].Path <= first.NextCursor {
		t.Fatalf("Tree(page 2) first path %q is not after cursor %q", second.Entries[0].Path, first.NextCursor)
	}

	largeLimit, err := r.Tree(ctx, fx.dir, fx.commit2, "", nil, nil, "", 1000)
	if err != nil {
		t.Fatalf("Tree(limit 1000): %v", err)
	}
	if len(largeLimit.Entries) > domain.MaxTreeEntriesPerPage {
		t.Fatalf("Tree(limit 1000) returned %d entries, want <= %d", len(largeLimit.Entries), domain.MaxTreeEntriesPerPage)
	}

	_, err = r.Tree(ctx, fx.dir, fx.commit2, "../escape", nil, nil, "", 10)
	requireDomainInvalid(t, err, "path_escape")
	_, err = r.Tree(ctx, fx.dir, fx.commit2, "", nil, nil, "../escape", 10)
	requireDomainInvalid(t, err, "path_escape")
	_, err = r.Tree(ctx, fx.dir, strings.Repeat("0", 40), "", nil, nil, "", 10)
	requireDomainInvalid(t, err, "commit_not_found")
}

// TestFileWindowCRLFAndUTF8 pins the on-demand line window: CRLF normalisation,
// strict UTF-8, 1-based inclusive windows, commit pinning, defaults, caps and
// truncation reporting.
func TestFileWindowCRLFAndUTF8(t *testing.T) {
	fx := newTestRepo(t)
	r := gitx.NewGitReader(gitx.GitConfig{CommandTimeout: 5 * time.Second})
	ctx := context.Background()

	window, err := r.File(ctx, fx.dir, fx.commit2, "src/styles/site.css", 2, 3)
	if err != nil {
		t.Fatalf("File(site.css 2-3): %v", err)
	}
	if window.Commit != fx.commit2 || window.Path != "src/styles/site.css" {
		t.Fatalf("File window=%+v want commit/path pinned", window)
	}
	if window.StartLine != 2 || window.EndLine != 3 || window.TotalLines != 3 {
		t.Fatalf("File window lines start=%d end=%d total=%d want 2/3/3", window.StartLine, window.EndLine, window.TotalLines)
	}
	if window.Content != "second\nthird" {
		t.Fatalf("File content=%q want %q", window.Content, "second\nthird")
	}
	if strings.ContainsRune(window.Content, '\r') {
		t.Fatalf("File content still contains CR: %q", window.Content)
	}
	if window.Truncated || window.Binary || window.Unsupported {
		t.Fatalf("File flags=%+v want all false", window)
	}

	whole, err := r.File(ctx, fx.dir, fx.commit2, "src/styles/site.css", 0, 0)
	if err != nil {
		t.Fatalf("File(site.css all): %v", err)
	}
	if whole.StartLine != 1 || whole.EndLine != 3 || whole.Content != "first\nsecond\nthird" {
		t.Fatalf("File(all)=%+v want 1..3 CRLF-normalised content", whole)
	}

	chinese, err := r.File(ctx, fx.dir, fx.commit2, "src/utf8/你好.txt", 2, 3)
	if err != nil {
		t.Fatalf("File(utf8 2-3): %v", err)
	}
	if chinese.Content != "第二行\n第三行" || chinese.TotalLines != 3 {
		t.Fatalf("File(utf8)=%+v", chinese)
	}

	old, err := r.File(ctx, fx.dir, fx.commit1, "src/api/refund.ts", 1, 0)
	if err != nil {
		t.Fatalf("File(commit1): %v", err)
	}
	if old.TotalLines != 3 || !strings.Contains(old.Content, "return 1;") || strings.Contains(old.Content, "return 2;") {
		t.Fatalf("File(commit1)=%+v want first-commit content", old)
	}

	capped := gitx.NewGitReader(gitx.GitConfig{CommandTimeout: 5 * time.Second, MaxReadLines: 2})
	firstTwo, err := capped.File(ctx, fx.dir, fx.commit2, "src/data/lines.txt", 1, 0)
	if err != nil {
		t.Fatalf("File(capped): %v", err)
	}
	if firstTwo.Content != "l1\nl2" || firstTwo.EndLine != 2 || firstTwo.TotalLines != 4 || !firstTwo.Truncated {
		t.Fatalf("File(capped)=%+v want 2 lines of 4 truncated", firstTwo)
	}
	lastTwo, err := capped.File(ctx, fx.dir, fx.commit2, "src/data/lines.txt", 3, 0)
	if err != nil {
		t.Fatalf("File(capped 3..): %v", err)
	}
	if lastTwo.Content != "l3\nl4" || lastTwo.Truncated {
		t.Fatalf("File(capped 3..)=%+v want the final two lines untruncated", lastTwo)
	}

	clamped, err := r.File(ctx, fx.dir, fx.commit2, "src/data/lines.txt", 1, 999)
	if err != nil {
		t.Fatalf("File(clamped): %v", err)
	}
	if clamped.EndLine != 4 || clamped.Truncated || clamped.Content != "l1\nl2\nl3\nl4" {
		t.Fatalf("File(clamped)=%+v want the whole 4-line file", clamped)
	}

	wide, err := r.File(ctx, fx.dir, fx.commit2, "src/data/wide.txt", 1, 1)
	if err != nil {
		t.Fatalf("File(wide): %v", err)
	}
	if len(wide.Content) != domain.MaxFileReadBytes || !wide.Truncated || wide.EndLine != 1 || wide.TotalLines != 1 {
		t.Fatalf("File(wide) len=%d truncated=%v end=%d total=%d want %d bytes truncated", len(wide.Content), wide.Truncated, wide.EndLine, wide.TotalLines, domain.MaxFileReadBytes)
	}

	wideUTF8, err := r.File(ctx, fx.dir, fx.commit2, "src/data/wide-utf8.txt", 1, 1)
	if err != nil {
		t.Fatalf("File(wide-utf8): %v", err)
	}
	if !wideUTF8.Truncated || wideUTF8.EndLine != 1 || wideUTF8.TotalLines != 1 {
		t.Fatalf("File(wide-utf8) truncated=%v end=%d total=%d want a truncated single-line window", wideUTF8.Truncated, wideUTF8.EndLine, wideUTF8.TotalLines)
	}
	if len(wideUTF8.Content) > domain.MaxFileReadBytes {
		t.Fatalf("File(wide-utf8) len=%d want <= %d", len(wideUTF8.Content), domain.MaxFileReadBytes)
	}
	if len(wideUTF8.Content) < domain.MaxFileReadBytes-utf8.UTFMax {
		t.Fatalf("File(wide-utf8) len=%d want within one rune of %d", len(wideUTF8.Content), domain.MaxFileReadBytes)
	}
	if !utf8.ValidString(wideUTF8.Content) {
		t.Fatalf("File(wide-utf8) content is not valid UTF-8 at the 64 KiB cut")
	}
	if wideUTF8.Content != strings.Repeat("你", len(wideUTF8.Content)/len("你")) {
		t.Fatalf("File(wide-utf8) content is not a whole-rune prefix of the file")
	}

	empty, err := r.File(ctx, fx.dir, fx.commit2, "src/data/empty.txt", 1, 0)
	if err != nil {
		t.Fatalf("File(empty): %v", err)
	}
	if empty.Content != "" || empty.TotalLines != 0 {
		t.Fatalf("File(empty)=%+v want empty content and 0 lines", empty)
	}

	_, err = r.File(ctx, fx.dir, fx.commit2, "src/data/lines.txt", 3, 2)
	requireDomainInvalid(t, err, "invalid_line_range")
	_, err = r.File(ctx, fx.dir, fx.commit2, "src/data/lines.txt", 5, 0)
	requireDomainInvalid(t, err, "invalid_line_range")
	_, err = r.File(ctx, fx.dir, fx.commit2, "src/missing.ts", 0, 0)
	requireDomainInvalid(t, err, "path_not_found")
}

// TestFileWindowPreservesBlankWindowLines is the regression test for the line
// joiner: a window that starts on an empty line must still emit the separator
// before every following line, so Content always describes exactly
// StartLine..EndLine.
func TestFileWindowPreservesBlankWindowLines(t *testing.T) {
	fx := newTestRepo(t)
	r := gitx.NewGitReader(gitx.GitConfig{CommandTimeout: 5 * time.Second})
	ctx := context.Background()

	cases := []struct {
		name     string
		path     string
		start    int
		end      int
		total    int
		wantLast int
		want     string
	}{
		{
			name:     "leading blank line",
			path:     "src/data/leading-blank.txt",
			start:    1,
			end:      2,
			total:    2,
			wantLast: 2,
			want:     "\nhello",
		},
		{
			name:     "internal blank line",
			path:     "src/data/internal-blank.txt",
			start:    2,
			end:      3,
			total:    3,
			wantLast: 3,
			want:     "\nb",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			window, err := r.File(ctx, fx.dir, fx.commit2, tc.path, tc.start, tc.end)
			if err != nil {
				t.Fatalf("File(%s %d-%d): %v", tc.path, tc.start, tc.end, err)
			}
			if window.StartLine != tc.start || window.EndLine != tc.wantLast || window.TotalLines != tc.total {
				t.Fatalf("File(%s) lines start=%d end=%d total=%d want %d/%d/%d",
					tc.path, window.StartLine, window.EndLine, window.TotalLines, tc.start, tc.wantLast, tc.total)
			}
			if window.Content != tc.want {
				t.Fatalf("File(%s %d-%d).Content=%q want %q", tc.path, tc.start, tc.end, window.Content, tc.want)
			}
			if window.Truncated {
				t.Fatalf("File(%s %d-%d).Truncated=true want false", tc.path, tc.start, tc.end)
			}
		})
	}
}

// TestFileWindowByteCapKeepsLeadingBlankLine pins the 64 KiB byte-cap path of
// the line joiner: the separator that represents a leading empty line counts
// toward the byte budget, so the content stays line-accurate at the cut.
func TestFileWindowByteCapKeepsLeadingBlankLine(t *testing.T) {
	fx := newTestRepo(t)
	r := gitx.NewGitReader(gitx.GitConfig{CommandTimeout: 5 * time.Second})

	window, err := r.File(context.Background(), fx.dir, fx.commit2, "src/data/blank-wide.txt", 1, 2)
	if err != nil {
		t.Fatalf("File(blank-wide 1-2): %v", err)
	}
	want := "\n" + strings.Repeat("a", domain.MaxFileReadBytes-1)
	if window.Content != want {
		prefix := window.Content
		if len(prefix) > 8 {
			prefix = prefix[:8]
		}
		t.Fatalf("File(blank-wide).Content len=%d prefix=%q want len=%d starting with a newline",
			len(window.Content), prefix, len(want))
	}
	if len(window.Content) != domain.MaxFileReadBytes || !window.Truncated {
		t.Fatalf("File(blank-wide) len=%d truncated=%v want len=%d truncated=true",
			len(window.Content), window.Truncated, domain.MaxFileReadBytes)
	}
	if window.StartLine != 1 || window.EndLine != 2 || window.TotalLines != 2 {
		t.Fatalf("File(blank-wide) lines start=%d end=%d total=%d want 1/2/2",
			window.StartLine, window.EndLine, window.TotalLines)
	}
}

// TestReadAllCapsBytes pins commit-pinned byte reads, the maxBytes cap and its
// truncated flag.
func TestReadAllCapsBytes(t *testing.T) {
	fx := newTestRepo(t)
	r := gitx.NewGitReader(gitx.GitConfig{CommandTimeout: 5 * time.Second})
	ctx := context.Background()

	content := "export function format(value: string) {\n  return value.trim().toUpperCase();\n}\n"
	full, truncated, err := r.ReadAll(ctx, fx.dir, fx.commit2, "src/utils/format.ts", 0)
	if err != nil {
		t.Fatalf("ReadAll(full): %v", err)
	}
	if truncated || string(full) != content {
		t.Fatalf("ReadAll(full)=%q truncated=%v want the whole file", full, truncated)
	}

	bounded, truncated, err := r.ReadAll(ctx, fx.dir, fx.commit2, "src/utils/format.ts", 10)
	if err != nil {
		t.Fatalf("ReadAll(bounded): %v", err)
	}
	if !truncated || len(bounded) != 10 || string(bounded) != content[:10] {
		t.Fatalf("ReadAll(bounded)=%q truncated=%v want 10 bytes truncated", bounded, truncated)
	}

	exact, truncated, err := r.ReadAll(ctx, fx.dir, fx.commit2, "src/utils/format.ts", len(content))
	if err != nil {
		t.Fatalf("ReadAll(exact): %v", err)
	}
	if truncated || string(exact) != content {
		t.Fatalf("ReadAll(exact)=%q truncated=%v want not truncated", exact, truncated)
	}

	old, _, err := r.ReadAll(ctx, fx.dir, fx.commit1, "src/utils/format.ts", 0)
	if err != nil {
		t.Fatalf("ReadAll(commit1): %v", err)
	}
	if strings.Contains(string(old), "toUpperCase") {
		t.Fatalf("ReadAll(commit1)=%q leaked commit2 content", old)
	}

	binary, _, err := r.ReadAll(ctx, fx.dir, fx.commit2, "src/data/binary.bin", 0)
	if err != nil {
		t.Fatalf("ReadAll(binary): %v", err)
	}
	if string(binary) != string([]byte{0x00, 0xff, 0xfe, 0x80, 0x81}) {
		t.Fatalf("ReadAll(binary)=%v want verbatim bytes", binary)
	}

	_, _, err = r.ReadAll(ctx, fx.dir, fx.commit2, "src/missing.ts", 0)
	requireDomainInvalid(t, err, "path_not_found")
}

// keys returns the map keys for readable failure messages.
func keys[V any](values map[string]V) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	return out
}
