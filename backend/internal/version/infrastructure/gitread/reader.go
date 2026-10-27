// Package gitread implements the P3 read-only local Git reader (design spec
// §3.5/§6.8): it resolves a branch to a commit, lists a commit-pinned tree and
// reads a commit-pinned blob, and never mutates the checkout. Every git process
// runs with GIT_OPTIONAL_LOCKS=0 so it cannot refresh the index or take an
// optional lock, and every public call applies the reader's timeout.
//
// The port contract lives in version/application: commit resolution reports
// branch_not_found / path_not_found / path_not_git_repo; a pinned commit that no
// longer resolves is resource_unavailable; a missing code path is
// path_not_found. Operational failures (git missing, timeout, canceled context)
// are returned as ordinary errors so callers can tell them apart from a verdict.
package gitread

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

const (
	// defaultTimeout bounds one whole Git read, including every git process.
	defaultTimeout = 5 * time.Second
	// maxTreeEntries is the hard bound of one tree listing (spec §6.8: at most
	// 200 entries per tree).
	maxTreeEntries = 200
	// maxReadBytes is the hard bound of one blob read (spec §6.8: at most
	// 64 KiB of text per read).
	maxReadBytes = 64 << 10
	// maxBranchBytes mirrors the P2 branch format bound.
	maxBranchBytes = 255
)

// commitRE is the 40 lowercase hexadecimal commit id form the version baseline
// column stores.
var commitRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

// branchRE is the documented branch format. It rejects option-like branches so
// a "branch" can never reach git as a command line flag.
var branchRE = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,255}$`)

// GitReader is the read-only local Git adapter. It implements
// application.GitReader and additionally exposes ReadRange, which reports the
// blob size so the code tools can flag truncation.
type GitReader struct {
	timeout time.Duration
}

// NewGitReader returns a reader that gives one whole call timeout. A
// non-positive timeout selects the documented 5 s default.
func NewGitReader(timeout time.Duration) *GitReader {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &GitReader{timeout: timeout}
}

var _ application.GitReader = (*GitReader)(nil)

// ResolveCommit resolves branch in repoPath to its 40 hex commit id.
func (r *GitReader) ResolveCommit(ctx context.Context, repoPath, branch string) (string, error) {
	if !validBranch(branch) {
		return "", gitError("branch_not_found",
			fmt.Sprintf("branch %q is not a valid branch name", branch))
	}
	ctx, cancel := r.withTimeout(ctx)
	defer cancel()

	dir, err := r.resolveRepo(ctx, repoPath)
	if err != nil {
		return "", err
	}
	if _, _, err := r.run(ctx, dir, "rev-parse", "--verify", "--quiet", branch+"^{commit}"); err != nil {
		if operational(ctx, err) {
			return "", err
		}
		return "", gitError("branch_not_found",
			fmt.Sprintf("branch %q does not resolve to a commit", branch))
	}
	output, _, err := r.run(ctx, dir, "rev-parse", branch+"^{commit}")
	if err != nil {
		if operational(ctx, err) {
			return "", err
		}
		return "", fmt.Errorf("gitread: resolve branch %q: %w", branch, err)
	}
	commit := strings.TrimSpace(output)
	if !commitRE.MatchString(commit) {
		return "", fmt.Errorf("gitread: branch %q resolved to unexpected commit id %q", branch, commit)
	}
	return commit, nil
}

// Tree lists the entries of a commit-pinned tree, applying the repository's
// include/exclude path globs and returning at most max entries (hard cap 200).
func (r *GitReader) Tree(ctx context.Context, repoPath, commit string, include, exclude []string, max int) ([]application.CodeEntry, error) {
	ctx, cancel := r.withTimeout(ctx)
	defer cancel()

	dir, err := r.resolveRepo(ctx, repoPath)
	if err != nil {
		return nil, err
	}
	if err := r.verifyCommit(ctx, dir, commit); err != nil {
		return nil, err
	}
	limit := max
	if limit <= 0 || limit > maxTreeEntries {
		limit = maxTreeEntries
	}

	output, _, err := r.run(ctx, dir, "ls-tree", "-r", "-t", "-l", "-z", commit)
	if err != nil {
		if operational(ctx, err) {
			return nil, err
		}
		return nil, fmt.Errorf("gitread: list tree %s: %w", commit, err)
	}

	entries := make([]application.CodeEntry, 0, limit)
	for _, record := range strings.Split(output, "\x00") {
		if record == "" {
			continue
		}
		tab := strings.IndexByte(record, '\t')
		if tab < 0 {
			continue
		}
		fields := strings.Fields(record[:tab])
		if len(fields) < 4 {
			continue
		}
		entry := application.CodeEntry{
			Path: record[tab+1:],
			Type: fields[1],
		}
		if fields[3] != "-" {
			size, err := strconv.ParseInt(fields[3], 10, 64)
			if err != nil {
				continue
			}
			entry.Size = size
		}
		if !treePathIncluded(entry.Path, include, exclude) {
			continue
		}
		entries = append(entries, entry)
		if len(entries) >= limit {
			break
		}
	}
	return entries, nil
}

// Read returns at most maxBytes of the commit-pinned blob at path.
func (r *GitReader) Read(ctx context.Context, repoPath, commit, path string, maxBytes int) (string, error) {
	content, _, err := r.readContent(ctx, repoPath, commit, path, 0, maxBytes)
	return content, err
}

// ReadRange returns the content window [offset, offset+maxBytes) of the
// commit-pinned blob plus the blob's total size. The code tools use the size to
// report truncation; it is an extension over application.GitReader.
func (r *GitReader) ReadRange(ctx context.Context, repoPath, commit, path string, offset, maxBytes int) (string, int64, error) {
	if offset < 0 {
		return "", 0, fmt.Errorf("gitread: offset must not be negative")
	}
	return r.readContent(ctx, repoPath, commit, path, offset, maxBytes)
}

// readContent validates the repository, commit and path, then streams the
// bounded window of the blob at path.
func (r *GitReader) readContent(ctx context.Context, repoPath, commit, codePath string, offset, maxBytes int) (string, int64, error) {
	ctx, cancel := r.withTimeout(ctx)
	defer cancel()

	dir, err := r.resolveRepo(ctx, repoPath)
	if err != nil {
		return "", 0, err
	}
	if err := r.verifyCommit(ctx, dir, commit); err != nil {
		return "", 0, err
	}
	if err := validateCodePath(codePath); err != nil {
		return "", 0, err
	}
	limit := maxBytes
	if limit <= 0 || limit > maxReadBytes {
		limit = maxReadBytes
	}

	revision := commit + ":" + codePath
	kind, _, err := r.run(ctx, dir, "cat-file", "-t", revision)
	if err != nil {
		if operational(ctx, err) {
			return "", 0, err
		}
		return "", 0, gitError("path_not_found",
			fmt.Sprintf("code path %q does not exist at commit %s", codePath, commit))
	}
	if strings.TrimSpace(kind) != "blob" {
		return "", 0, gitError("path_not_found",
			fmt.Sprintf("code path %q is not a file at commit %s", codePath, commit))
	}
	sizeOutput, _, err := r.run(ctx, dir, "cat-file", "-s", revision)
	if err != nil {
		if operational(ctx, err) {
			return "", 0, err
		}
		return "", 0, fmt.Errorf("gitread: read blob size %q: %w", revision, err)
	}
	size, err := strconv.ParseInt(strings.TrimSpace(sizeOutput), 10, 64)
	if err != nil {
		return "", 0, fmt.Errorf("gitread: parse blob size %q: %w", sizeOutput, err)
	}

	content, err := r.streamBlob(ctx, dir, revision, offset, limit)
	if err != nil {
		return "", 0, err
	}
	return content, size, nil
}

// streamBlob runs `git show <revision>` and reads the bounded window directly
// from its pipe, draining the rest so git can exit cleanly even for a large
// blob.
func (r *GitReader) streamBlob(ctx context.Context, dir, revision string, offset, limit int) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "show", revision)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("gitread: pipe git show: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("gitread: start git show: %w", err)
	}

	var content []byte
	readErr := func() error {
		if offset > 0 {
			if _, err := io.CopyN(io.Discard, stdout, int64(offset)); err != nil && !errors.Is(err, io.EOF) {
				return err
			}
		}
		content, err = io.ReadAll(io.LimitReader(stdout, int64(limit)))
		if err != nil {
			return err
		}
		// Drain the remaining output so git is not blocked on a full pipe when
		// Wait runs.
		_, _ = io.Copy(io.Discard, stdout)
		return nil
	}()
	waitErr := cmd.Wait()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", fmt.Errorf("gitread: git show: %w", ctxErr)
	}
	if readErr != nil {
		return "", fmt.Errorf("gitread: read blob %q: %w", revision, readErr)
	}
	if waitErr != nil {
		return "", fmt.Errorf("gitread: git show %q: %w: %s", revision, waitErr, strings.TrimSpace(stderr.String()))
	}
	return string(content), nil
}

// verifyCommit proves a pinned commit still resolves. A missing commit is a
// resource_unavailable verdict rather than an operational failure.
func (r *GitReader) verifyCommit(ctx context.Context, dir, commit string) error {
	if !commitRE.MatchString(commit) {
		return fmt.Errorf("gitread: commit %q must be 40 lowercase hexadecimal characters", commit)
	}
	if _, _, err := r.run(ctx, dir, "rev-parse", "--verify", "--quiet", commit+"^{commit}"); err != nil {
		if operational(ctx, err) {
			return err
		}
		return gitError("resource_unavailable",
			fmt.Sprintf("commit %s does not resolve in this repository", commit))
	}
	return nil
}

// resolveRepo validates repoPath and proves it is a Git work tree.
func (r *GitReader) resolveRepo(ctx context.Context, repoPath string) (string, error) {
	if strings.TrimSpace(repoPath) == "" {
		return "", gitError("path_not_found", "repository path is required")
	}
	absolute, err := filepath.Abs(repoPath)
	if err != nil {
		return "", fmt.Errorf("gitread: resolve repository path %q: %w", repoPath, err)
	}
	absolute = filepath.Clean(absolute)
	info, err := os.Stat(absolute)
	if err != nil || !info.IsDir() {
		return "", gitError("path_not_found",
			fmt.Sprintf("repository path %q does not exist or is not a directory", repoPath))
	}
	inside, _, err := r.run(ctx, absolute, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		if operational(ctx, err) {
			return "", err
		}
		return "", gitError("path_not_git_repo",
			fmt.Sprintf("repository path %q is not a Git work tree", repoPath))
	}
	if strings.TrimSpace(inside) != "true" {
		return "", gitError("path_not_git_repo",
			fmt.Sprintf("repository path %q is not a Git work tree", repoPath))
	}
	return absolute, nil
}

// withTimeout applies the reader's timeout to one public call.
func (r *GitReader) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if r.timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, r.timeout)
}

// run executes one fixed read-only git command in dir and returns its trimmed
// standard output plus the raw standard error.
func (r *GitReader) run(ctx context.Context, dir string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", stderr.String(), fmt.Errorf("gitread: git %s: %w", args[0], ctxErr)
		}
		return "", stderr.String(), fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), stderr.String(), nil
}

// operational reports whether a git failure was caused by cancellation, the
// reader deadline or a git executable that cannot be started. A false result is
// an ordinary negative verdict the caller translates into a typed reason.
func operational(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx.Err() != nil {
		return true
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var execErr *exec.Error
	return errors.As(err, &execErr)
}

// validBranch enforces the branch format and rejects option-like branches.
func validBranch(branch string) bool {
	return len(branch) <= maxBranchBytes && !strings.HasPrefix(branch, "-") && branchRE.MatchString(branch)
}

// validateCodePath rejects absolute paths, Windows separators, NUL bytes and any
// ".." component so git can never read outside the commit tree.
func validateCodePath(codePath string) error {
	invalid := gitError("path_not_found",
		fmt.Sprintf("code path %q is not a safe repository-relative path", codePath))
	if codePath == "" || strings.ContainsRune(codePath, '\x00') || strings.ContainsRune(codePath, '\\') {
		return invalid
	}
	if strings.HasPrefix(codePath, "/") || filepath.IsAbs(codePath) || filepath.VolumeName(codePath) != "" {
		return invalid
	}
	for _, component := range strings.Split(codePath, "/") {
		if component == ".." {
			return invalid
		}
	}
	cleaned := path.Clean(codePath)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return invalid
	}
	return nil
}

// treePathIncluded applies the simple include/exclude path filters of spec
// §6.8: an entry must match at least one include glob (when any) and no exclude
// glob.
func treePathIncluded(entryPath string, include, exclude []string) bool {
	if len(include) > 0 && !matchesAnyGlob(entryPath, include) {
		return false
	}
	return !matchesAnyGlob(entryPath, exclude)
}

// matchesAnyGlob reports whether p matches at least one glob.
func matchesAnyGlob(p string, globs []string) bool {
	for _, glob := range globs {
		if matchGlob(glob, p) {
			return true
		}
	}
	return false
}

// matchGlob implements the P3 subset of glob matching: exact names match the
// entry or any descendant, and a compiled wildcard pattern matches the path.
// "**" matches across separators, "*" and "?" stay inside one path segment.
func matchGlob(glob, p string) bool {
	glob = strings.TrimSpace(strings.ReplaceAll(glob, `\`, "/"))
	if glob == "" {
		return false
	}
	if !strings.ContainsAny(glob, "*?[") {
		return p == glob || strings.HasPrefix(p, glob+"/")
	}
	glob = strings.TrimSuffix(glob, "/")
	pattern, err := globPattern(glob)
	if err != nil {
		return false
	}
	return pattern.MatchString(p)
}

// globPattern compiles a simple glob into an anchored regular expression.
func globPattern(glob string) (*regexp.Regexp, error) {
	var builder strings.Builder
	builder.WriteString("^")
	for i := 0; i < len(glob); i++ {
		switch glob[i] {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				builder.WriteString(".*")
				i++
				continue
			}
			builder.WriteString("[^/]*")
		case '?':
			builder.WriteString("[^/]")
		default:
			builder.WriteString(regexp.QuoteMeta(string(glob[i])))
		}
	}
	builder.WriteString("$")
	return regexp.Compile(builder.String())
}

// gitError builds a typed Git verdict.
func gitError(reason, message string) *application.GitError {
	return &application.GitError{Reason: reason, Message: message}
}
