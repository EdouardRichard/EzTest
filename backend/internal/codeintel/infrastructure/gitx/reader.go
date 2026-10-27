// Package gitx implements the read-only Git adapter of the codeintel T0 Git
// layer (design spec §6.1): it resolves a fixed branch to a commit, proves
// reachability, lists a commit-pinned tree with glob filtering and cursor
// pagination, reads commit-pinned line windows and raw bytes, parses commit
// metadata and builds the deterministic file-level change set.
//
// Every git process is argv-only (never a shell), runs with
// GIT_OPTIONAL_LOCKS=0 in the repository root and is bounded by the reader's
// per-command timeout and a 4 MiB stdout/stderr cap. The adapter never writes
// to the checkout. The port shape lives in codeintel/application (design spec
// §6.1); the methods here implement it structurally without importing the
// application package.
package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// GitConfig bounds one GitReader. CommandTimeout applies to each individual
// git process; MaxTreeEntries and MaxReadLines default to the design spec
// §4.2 constants and can only be lowered, never raised.
type GitConfig struct {
	CommandTimeout time.Duration
	MaxTreeEntries int
	MaxReadLines   int
}

// GitReader is the read-only local Git adapter of design spec §6.1. A zero
// value is not usable; construct it with NewGitReader.
type GitReader struct {
	cfg GitConfig
}

// NewGitReader returns a reader whose per-command timeout and page/window
// bounds fall back to the documented defaults when unset or out of range.
func NewGitReader(cfg GitConfig) *GitReader {
	if cfg.CommandTimeout <= 0 {
		cfg.CommandTimeout = defaultCommandTimeout
	}
	if cfg.MaxTreeEntries <= 0 || cfg.MaxTreeEntries > domain.MaxTreeEntriesPerPage {
		cfg.MaxTreeEntries = domain.MaxTreeEntriesPerPage
	}
	if cfg.MaxReadLines <= 0 || cfg.MaxReadLines > domain.MaxReadLines {
		cfg.MaxReadLines = domain.MaxReadLines
	}
	return &GitReader{cfg: cfg}
}

// Head resolves branch to its current 40 hex commit id.
func (r *GitReader) Head(ctx context.Context, localPath, branch string) (string, error) {
	if !validBranch(branch) {
		return "", invalidHead()
	}
	dir, err := r.openRepo(ctx, localPath)
	if err != nil {
		return "", err
	}
	output, err := r.run(ctx, dir, "rev-parse", "--verify", "--quiet", branch+"^{commit}")
	if err != nil {
		if operationalError(err) {
			return "", err
		}
		return "", invalidHead()
	}
	commit := strings.TrimSpace(string(output))
	if !validCommit(commit) {
		return "", invalidHead()
	}
	return commit, nil
}

// IsAncestor reports whether base is an ancestor of head. An unreachable pair
// is (false, nil); an unreadable repository or Git failure is an error.
func (r *GitReader) IsAncestor(ctx context.Context, localPath, base, head string) (bool, error) {
	if !validCommit(base) || !validCommit(head) {
		return false, invalidHead()
	}
	dir, err := r.openRepo(ctx, localPath)
	if err != nil {
		return false, err
	}
	if _, err := r.run(ctx, dir, "merge-base", "--is-ancestor", base, head); err != nil {
		if operationalError(err) {
			return false, err
		}
		if code, ok := exitCode(err); ok && code == 1 {
			return false, nil
		}
		return false, commitNotFound()
	}
	return true, nil
}

// Tree lists one page of the commit-pinned recursive file tree. Entries are
// filtered by prefix and the repository include/exclude shell-style globs,
// sorted by path ascending and paged with cursor == last path. Symlinks and
// submodules are returned with Unsupported=true.
func (r *GitReader) Tree(ctx context.Context, localPath, commit, prefix string, include, exclude []string, cursor string, limit int) (domain.TreePage, error) {
	if !validCommit(commit) {
		return domain.TreePage{}, commitNotFound()
	}
	normalizedPrefix, err := normalizeTreePrefix(prefix)
	if err != nil {
		return domain.TreePage{}, err
	}
	cursorPath := ""
	if cursor != "" {
		cursorPath, err = normalizeRepoPath(cursor)
		if err != nil {
			return domain.TreePage{}, err
		}
	}
	dir, err := r.openRepo(ctx, localPath)
	if err != nil {
		return domain.TreePage{}, err
	}
	entries, err := r.listTree(ctx, dir, commit)
	if err != nil {
		return domain.TreePage{}, err
	}
	effectiveLimit := limit
	if effectiveLimit <= 0 || effectiveLimit > r.cfg.MaxTreeEntries {
		effectiveLimit = r.cfg.MaxTreeEntries
	}
	filter := compilePathFilter(include, exclude)
	selected := make([]domain.TreeEntry, 0, len(entries))
	for _, entry := range entries {
		if normalizedPrefix != "" &&
			entry.Path != normalizedPrefix &&
			!strings.HasPrefix(entry.Path, normalizedPrefix+"/") {
			continue
		}
		if !filter.includes(entry.Path) {
			continue
		}
		if cursorPath != "" && entry.Path <= cursorPath {
			continue
		}
		selected = append(selected, entry)
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Path < selected[j].Path })

	page := domain.TreePage{Commit: commit, Entries: selected}
	if len(selected) > effectiveLimit {
		page.Entries = selected[:effectiveLimit]
		page.HasMore = true
		page.NextCursor = page.Entries[len(page.Entries)-1].Path
	}
	return page, nil
}

// File returns one 1-based inclusive line window of a commit-pinned text file.
// Content is CRLF-normalised (a trailing CR is stripped from every line) and
// bounded by MaxReadLines and 64 KiB; TotalLines always describes the whole
// file so a truncated window is unambiguous. Binary, non-UTF-8, symlink and
// submodule content is rejected.
func (r *GitReader) File(ctx context.Context, localPath, commit, path string, startLine, endLine int) (domain.FileWindow, error) {
	if !validCommit(commit) {
		return domain.FileWindow{}, commitNotFound()
	}
	normalized, err := normalizeRepoPath(path)
	if err != nil {
		return domain.FileWindow{}, err
	}
	if startLine <= 0 {
		startLine = 1
	}
	if endLine != 0 && endLine < startLine {
		return domain.FileWindow{}, invalidLineRange()
	}
	dir, err := r.openRepo(ctx, localPath)
	if err != nil {
		return domain.FileWindow{}, err
	}
	entry, err := r.lookupEntry(ctx, dir, commit, normalized)
	if err != nil {
		return domain.FileWindow{}, err
	}
	if !isRegularBlob(entry) {
		return domain.FileWindow{}, pathNotFile()
	}
	content, err := r.readBlob(ctx, dir, commit, normalized)
	if err != nil {
		return domain.FileWindow{}, err
	}
	lines, err := normalizeTextLines(content)
	if err != nil {
		return domain.FileWindow{}, err
	}

	window := domain.FileWindow{Commit: commit, Path: normalized, TotalLines: len(lines)}
	if len(lines) == 0 {
		return window, nil
	}
	if endLine <= 0 {
		endLine = len(lines)
	}
	if startLine > len(lines) {
		return domain.FileWindow{}, invalidLineRange()
	}
	if endLine > len(lines) {
		endLine = len(lines)
	}
	if endLine < startLine {
		return domain.FileWindow{}, invalidLineRange()
	}
	lineTruncated := false
	if endLine-startLine+1 > r.cfg.MaxReadLines {
		endLine = startLine + r.cfg.MaxReadLines - 1
		lineTruncated = true
	}
	windowContent, lastLine, byteTruncated := joinLineWindow(lines, startLine, endLine)
	window.StartLine = startLine
	window.EndLine = lastLine
	window.Content = windowContent
	window.Truncated = lineTruncated || byteTruncated
	return window, nil
}

// ReadAll returns the commit-pinned raw bytes of a regular file, cut at
// maxBytes. truncated reports that the file is longer than the returned slice.
func (r *GitReader) ReadAll(ctx context.Context, localPath, commit, path string, maxBytes int) ([]byte, bool, error) {
	if !validCommit(commit) {
		return nil, false, commitNotFound()
	}
	normalized, err := normalizeRepoPath(path)
	if err != nil {
		return nil, false, err
	}
	dir, err := r.openRepo(ctx, localPath)
	if err != nil {
		return nil, false, err
	}
	entry, err := r.lookupEntry(ctx, dir, commit, normalized)
	if err != nil {
		return nil, false, err
	}
	if !isRegularBlob(entry) {
		return nil, false, pathNotFile()
	}
	content, err := r.readBlob(ctx, dir, commit, normalized)
	if err != nil {
		return nil, false, err
	}
	limit := maxBytes
	if limit <= 0 {
		limit = domain.MaxFileReadBytes
	}
	if len(content) > limit {
		return content[:limit], true, nil
	}
	return content, false, nil
}

// Commit returns commit-pinned metadata for sha.
func (r *GitReader) Commit(ctx context.Context, localPath, sha string) (domain.CommitInfo, error) {
	if !validCommit(sha) {
		return domain.CommitInfo{}, commitNotFound()
	}
	dir, err := r.openRepo(ctx, localPath)
	if err != nil {
		return domain.CommitInfo{}, err
	}
	output, err := r.run(ctx, dir, "show", "-s", "--format=%H%x00%ct%x00%an%x00%s", sha)
	if err != nil {
		if operationalError(err) {
			return domain.CommitInfo{}, err
		}
		if missingObject(err) {
			return domain.CommitInfo{}, commitNotFound()
		}
		return domain.CommitInfo{}, fmt.Errorf("gitx: read commit: %w", err)
	}
	record := strings.TrimSuffix(string(output), "\n")
	record = strings.TrimSuffix(record, "\r")
	fields := strings.Split(record, "\x00")
	if len(fields) != 4 || !validCommit(fields[0]) {
		return domain.CommitInfo{}, commitNotFound()
	}
	seconds, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return domain.CommitInfo{}, commitNotFound()
	}
	return domain.CommitInfo{
		SHA:         fields[0],
		Message:     fields[3],
		Author:      fields[2],
		CommittedAt: time.Unix(seconds, 0).UTC(),
	}, nil
}

// openRepo validates localPath, proves it is a Git work tree and returns the
// repository root, which is the working directory of every git command.
func (r *GitReader) openRepo(ctx context.Context, localPath string) (string, error) {
	if strings.TrimSpace(localPath) == "" {
		return "", pathNotFoundAtRepo()
	}
	absolute, err := filepath.Abs(localPath)
	if err != nil {
		return "", fmt.Errorf("gitx: resolve local path: %w", err)
	}
	absolute = filepath.Clean(absolute)
	info, err := os.Stat(absolute)
	if err != nil || !info.IsDir() {
		return "", pathNotFoundAtRepo()
	}
	inside, err := r.run(ctx, absolute, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		if operationalError(err) {
			return "", err
		}
		return "", notGitWorkTree()
	}
	if strings.TrimSpace(string(inside)) != "true" {
		return "", notGitWorkTree()
	}
	rootOutput, err := r.run(ctx, absolute, "rev-parse", "--show-toplevel")
	if err != nil {
		if operationalError(err) {
			return "", err
		}
		return "", notGitWorkTree()
	}
	root := strings.TrimSpace(string(rootOutput))
	if root == "" {
		return "", notGitWorkTree()
	}
	return filepath.Clean(root), nil
}

// listTree runs the fixed §6.1 tree listing command and parses its -z records.
func (r *GitReader) listTree(ctx context.Context, dir, commit string) ([]domain.TreeEntry, error) {
	output, err := r.run(ctx, dir, "ls-tree", "-r", "-t", "-l", "-z", commit)
	if err != nil {
		if operationalError(err) {
			return nil, err
		}
		if missingObject(err) {
			return nil, commitNotFound()
		}
		return nil, fmt.Errorf("gitx: list tree: %w", err)
	}
	return parseTreeEntries(output)
}

// lookupEntry resolves the exact tree entry for repoPath. The literal pathspec
// keeps wildcard characters in a real file name from matching other paths.
func (r *GitReader) lookupEntry(ctx context.Context, dir, commit, repoPath string) (domain.TreeEntry, error) {
	output, err := r.run(ctx, dir, "ls-tree", "-r", "-t", "-l", "-z", commit, "--", literalPathspec(repoPath))
	if err != nil {
		if operationalError(err) {
			return domain.TreeEntry{}, err
		}
		if missingObject(err) {
			return domain.TreeEntry{}, commitNotFound()
		}
		return domain.TreeEntry{}, fmt.Errorf("gitx: look up tree entry: %w", err)
	}
	entries, err := parseTreeEntries(output)
	if err != nil {
		return domain.TreeEntry{}, err
	}
	for _, entry := range entries {
		if entry.Path == repoPath {
			return entry, nil
		}
	}
	return domain.TreeEntry{}, pathNotFound()
}

// readBlob returns the raw bytes of commit:repoPath. An oversized stream is
// reported as unsupported content rather than an opaque operational failure.
func (r *GitReader) readBlob(ctx context.Context, dir, commit, repoPath string) ([]byte, error) {
	output, err := r.run(ctx, dir, "show", commit+":"+repoPath)
	if err != nil {
		if operationalError(err) {
			if errors.Is(err, errCommandOutputTooLarge) {
				return nil, unsupportedContent()
			}
			return nil, err
		}
		if missingObject(err) {
			return nil, pathNotFound()
		}
		return nil, fmt.Errorf("gitx: read file: %w", err)
	}
	return output, nil
}

// parseTreeEntries parses git ls-tree -r -t -l -z records:
// "<mode> <type> <object> <size>\t<path>\x00".
func parseTreeEntries(output []byte) ([]domain.TreeEntry, error) {
	records := strings.Split(string(output), "\x00")
	entries := make([]domain.TreeEntry, 0, len(records))
	for _, record := range records {
		if record == "" {
			continue
		}
		tab := strings.IndexByte(record, '\t')
		if tab < 0 {
			return nil, fmt.Errorf("%w: ls-tree record without a path", errMalformedOutput)
		}
		fields := strings.Fields(record[:tab])
		if len(fields) != 4 {
			return nil, fmt.Errorf("%w: ls-tree record with %d fields", errMalformedOutput, len(fields))
		}
		entry := domain.TreeEntry{
			Path:        record[tab+1:],
			Type:        fields[1],
			GitObjectID: fields[2],
			Mode:        fields[0],
		}
		if fields[3] != "-" {
			size, err := strconv.ParseInt(fields[3], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("%w: ls-tree size %q", errMalformedOutput, fields[3])
			}
			entry.Size = size
		}
		entry.Unsupported = isUnsupportedMode(entry.Mode, entry.Type)
		entries = append(entries, entry)
	}
	return entries, nil
}

// isUnsupportedMode reports symlink and submodule entries, which are never
// followed or analysed (design spec §6.1).
func isUnsupportedMode(mode, entryType string) bool {
	return mode == "120000" || mode == "160000" || entryType == "commit"
}

// isRegularBlob reports whether entry is an ordinary file blob.
func isRegularBlob(entry domain.TreeEntry) bool {
	return entry.Type == "blob" && entry.Mode != "120000" && entry.Mode != "160000"
}

// normalizeTextLines splits raw file bytes into LF-normalised lines following
// design spec §6.3: split on "\n", drop one trailing newline, strip a trailing
// "\r" from each line. Binary and non-UTF-8 content is rejected.
func normalizeTextLines(content []byte) ([]string, error) {
	if !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
		return nil, unsupportedContent()
	}
	if len(content) == 0 {
		return []string{}, nil
	}
	if content[len(content)-1] == '\n' {
		content = content[:len(content)-1]
	}
	rawLines := strings.Split(string(content), "\n")
	lines := make([]string, len(rawLines))
	for i, line := range rawLines {
		lines[i] = strings.TrimSuffix(line, "\r")
	}
	return lines, nil
}

// joinLineWindow joins lines[start-1:end] with "\n" while staying within the
// 64 KiB content bound. When a single line crosses the bound the line is cut
// and the reported last line is the cut line. The returned bool reports that
// the window content was truncated by the byte bound.
func joinLineWindow(lines []string, start, end int) (string, int, bool) {
	var builder strings.Builder
	lastLine := start
	for index := start; index <= end; index++ {
		line := lines[index-1]
		separator := 0
		if builder.Len() > 0 {
			separator = 1
		}
		available := domain.MaxFileReadBytes - builder.Len()
		if available <= separator {
			return builder.String(), lastLine, true
		}
		if separator+len(line) > available {
			if separator == 1 {
				builder.WriteByte('\n')
			}
			builder.WriteString(line[:available-separator])
			return builder.String(), index, true
		}
		if separator == 1 {
			builder.WriteByte('\n')
		}
		builder.WriteString(line)
		lastLine = index
	}
	return builder.String(), lastLine, false
}
