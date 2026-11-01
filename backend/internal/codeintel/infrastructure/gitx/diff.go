package gitx

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// defaultMaxDiffBytes is the hard fallback for on-demand file patches; the
// application normally passes codeintel.max_diff_bytes.
const defaultMaxDiffBytes = 256 << 10

// rawDiffEntry is one `git diff --raw -z` record before augmentation by numstat.
type rawDiffEntry struct {
	oldPath string
	newPath string
	status  byte
	headSHA string
}

// numstatEntry is one `git diff --numstat -z` record.
type numstatEntry struct {
	additions int
	deletions int
	binary    bool
}

// Diff returns the bounded base..head change set. It parses the raw status
// stream (A/M/D/R/C/T, rename old path) and the numstat stream (additions,
// deletions, binary marker); both use -z so filenames with spaces or shell
// metacharacters are never re-tokenized.
func (g *Git) Diff(ctx context.Context, repoPath, base, head string, max int) (codeintel.DiffResult, error) {
	if max <= 0 {
		max = codeintel.MaxChangedFilesStored
	}
	baseSHA, err := g.resolveVerified(ctx, repoPath, base)
	if err != nil {
		return codeintel.DiffResult{}, err
	}
	headSHA, err := g.resolveVerified(ctx, repoPath, head)
	if err != nil {
		return codeintel.DiffResult{}, err
	}
	if baseSHA == headSHA {
		return codeintel.DiffResult{Files: []codeintel.ChangedFile{}}, nil
	}

	rawOutput, stderr, err := g.execute(ctx, repoPath, "diff", "--raw", "--no-abbrev", "-z", "--no-ext-diff",
		"--find-renames=50%", "--find-copies=50%", baseSHA, headSHA)
	if err != nil {
		return codeintel.DiffResult{}, g.mapExecutionError(stderr, err)
	}
	rawEntries, err := parseRawDiff(rawOutput)
	if err != nil {
		return codeintel.DiffResult{}, apiError(http.StatusInternalServerError, CodeCommandFailed, "git returned malformed raw diff output")
	}
	numstatOutput, stderr, err := g.execute(ctx, repoPath, "diff", "--numstat", "-z", "--no-ext-diff",
		"--find-renames=50%", "--find-copies=50%", baseSHA, headSHA)
	if err != nil {
		return codeintel.DiffResult{}, g.mapExecutionError(stderr, err)
	}
	numstats := parseNumstat(numstatOutput)

	files := make([]codeintel.ChangedFile, 0, len(rawEntries))
	for _, entry := range rawEntries {
		stats := numstats[entry.newPath]
		changed := codeintel.ChangedFile{
			Path:        entry.newPath,
			Status:      changedFileStatus(entry.status),
			Additions:   stats.additions,
			Deletions:   stats.deletions,
			Binary:      stats.binary,
			HeadBlobSHA: headBlobSHA(entry.status, entry.headSHA),
		}
		if entry.oldPath != entry.newPath {
			changed.OldPath = entry.oldPath
		}
		files = append(files, changed)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	total := len(files)
	truncated := total > max
	if truncated {
		files = files[:max]
	}
	return codeintel.DiffResult{Files: files, Truncated: truncated}, nil
}

// FileDiff returns one on-demand unified=3 patch. Binary, deleted and oversized
// files return an empty patch with the matching flag (spec §5.1).
func (g *Git) FileDiff(ctx context.Context, repoPath, base, head, filePath string, maxBytes int) (codeintel.FileDiff, error) {
	normalized, err := normalizeGitPath(filePath)
	if err != nil {
		return codeintel.FileDiff{}, err
	}
	if maxBytes <= 0 {
		maxBytes = defaultMaxDiffBytes
	}
	baseSHA, err := g.resolveVerified(ctx, repoPath, base)
	if err != nil {
		return codeintel.FileDiff{}, err
	}
	headSHA, err := g.resolveVerified(ctx, repoPath, head)
	if err != nil {
		return codeintel.FileDiff{}, err
	}
	filter := ":(literal)" + normalized

	rawOutput, stderr, err := g.execute(ctx, repoPath, "diff", "--raw", "--no-abbrev", "-z", "--no-ext-diff",
		"--find-renames=50%", "--find-copies=50%", baseSHA, headSHA, "--", filter)
	if err != nil {
		return codeintel.FileDiff{}, g.mapExecutionError(stderr, err)
	}
	rawEntries, err := parseRawDiff(rawOutput)
	if err != nil {
		return codeintel.FileDiff{}, apiError(http.StatusInternalServerError, CodeCommandFailed, "git returned malformed raw diff output")
	}
	if len(rawEntries) == 0 {
		return codeintel.FileDiff{Path: normalized}, nil
	}
	entry := rawEntries[0]
	if entry.status == 'D' {
		return codeintel.FileDiff{Path: normalized, Deleted: true}, nil
	}

	numstatOutput, stderr, err := g.execute(ctx, repoPath, "diff", "--numstat", "-z", "--no-ext-diff",
		"--find-renames=50%", "--find-copies=50%", baseSHA, headSHA, "--", filter)
	if err != nil {
		return codeintel.FileDiff{}, g.mapExecutionError(stderr, err)
	}
	if stats, ok := parseNumstat(numstatOutput)[normalized]; ok && stats.binary {
		return codeintel.FileDiff{Path: normalized, Binary: true}, nil
	}

	patchOutput, stderr, err := g.execute(ctx, repoPath, "diff", "--unified=3", "--no-ext-diff",
		baseSHA, headSHA, "--", filter)
	if err != nil {
		return codeintel.FileDiff{}, g.mapExecutionError(stderr, err)
	}
	if len(patchOutput) > maxBytes {
		return codeintel.FileDiff{Path: normalized, Truncated: true}, nil
	}
	if !utf8.Valid(patchOutput) {
		return codeintel.FileDiff{Path: normalized, Binary: true}, nil
	}
	return codeintel.FileDiff{Path: normalized, Patch: string(patchOutput)}, nil
}

// parseRawDiff parses `git diff --raw -z` records. Rename/copy records carry
// two NUL-separated path tokens, so filenames cannot inject extra entries.
func parseRawDiff(raw []byte) ([]rawDiffEntry, error) {
	tokens := splitNUL(raw)
	entries := make([]rawDiffEntry, 0, len(tokens))
	for index := 0; index < len(tokens); {
		record := tokens[index]
		if record == "" {
			index++
			continue
		}
		if !strings.HasPrefix(record, ":") {
			return nil, fmt.Errorf("raw diff record does not start with ':'")
		}
		fields := strings.Fields(record[1:])
		if len(fields) != 5 || len(fields[4]) == 0 {
			return nil, fmt.Errorf("raw diff record has %d fields", len(fields))
		}
		status := fields[4][0]
		index++
		entry := rawDiffEntry{status: status, headSHA: fields[3]}
		if status == 'R' || status == 'C' {
			if index+1 >= len(tokens) {
				return nil, fmt.Errorf("raw diff rename record is truncated")
			}
			entry.oldPath = tokens[index]
			entry.newPath = tokens[index+1]
			index += 2
		} else {
			if index >= len(tokens) {
				return nil, fmt.Errorf("raw diff record is truncated")
			}
			entry.newPath = tokens[index]
			entry.oldPath = entry.newPath
			index++
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// parseNumstat parses `git diff --numstat -z` records. Binary files carry "-"
// for both counts; rename/copy records put an empty path first and the old/new
// paths in the following two NUL tokens.
func parseNumstat(raw []byte) map[string]numstatEntry {
	tokens := splitNUL(raw)
	entries := make(map[string]numstatEntry, len(tokens))
	for index := 0; index < len(tokens); {
		record := tokens[index]
		index++
		if record == "" {
			continue
		}
		fields := strings.SplitN(record, "\t", 3)
		if len(fields) != 3 {
			continue
		}
		filePath := fields[2]
		if filePath == "" && index+1 < len(tokens) {
			filePath = tokens[index+1]
			index += 2
		}
		entry := numstatEntry{binary: fields[0] == "-" && fields[1] == "-"}
		if !entry.binary {
			entry.additions, _ = strconv.Atoi(fields[0])
			entry.deletions, _ = strconv.Atoi(fields[1])
		}
		entries[filePath] = entry
	}
	return entries
}

// changedFileStatus maps a raw status letter to the four API status values.
func changedFileStatus(status byte) string {
	switch status {
	case 'A':
		return "added"
	case 'D':
		return "deleted"
	case 'R':
		return "renamed"
	case 'C':
		// A copy is a new file on the head side; old_path is kept for context.
		return "added"
	default:
		return "modified"
	}
}

// headBlobSHA returns the head-side blob SHA, or nil for a deletion.
func headBlobSHA(status byte, sha string) *string {
	if status == 'D' || sha == "" || strings.Trim(sha, "0") == "" {
		return nil
	}
	value := sha
	return &value
}

// normalizeGitPath applies the shared repo-relative path rules and maps any
// rejection to the stable path_escape code.
func normalizeGitPath(raw string) (string, error) {
	normalized, err := codeintel.NormalizeRepoPath(raw)
	if err != nil {
		return "", apiError(http.StatusUnprocessableEntity, CodePathEscape, "path must be a normalized repo-relative path")
	}
	return normalized, nil
}
