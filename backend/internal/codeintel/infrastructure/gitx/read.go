package gitx

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// ReadFile returns one bounded 1-based line window of a regular UTF-8 file at
// commit (spec §5.1). The API hard caps are 400 lines and 64 KiB; binary files
// map to binary_file so the HTTP layer can return an empty binary view, while
// symlinks/submodules map to unsupported_content.
func (g *Git) ReadFile(ctx context.Context, repoPath, commit, filePath string, start, end, maxBytes int) (codeintel.FileWindow, error) {
	return g.readWindow(ctx, repoPath, commit, filePath, start, end, maxBytes, codeintel.MaxReadLines)
}

// ReadFileWindow is the bulk T1 read: no API line cap (maxLines<=0), the file is
// rejected when its blob exceeds maxBytes, and ordinary UTF-8 text is returned
// in full.
func (g *Git) ReadFileWindow(ctx context.Context, repoPath, commit, filePath string, maxBytes, maxLines int) (codeintel.FileWindow, error) {
	normalized, err := normalizeGitPath(filePath)
	if err != nil {
		return codeintel.FileWindow{}, err
	}
	if maxBytes <= 0 {
		maxBytes = codeintel.MaxFileReadBytes
	}
	sha, entry, err := g.blobEntry(ctx, repoPath, commit, normalized)
	if err != nil {
		return codeintel.FileWindow{}, err
	}
	if entry.size > int64(maxBytes) {
		return codeintel.FileWindow{}, apiError(http.StatusUnprocessableEntity, CodeFileTooLarge, "file exceeds the readable size limit")
	}
	lines, err := g.readTextBlob(ctx, repoPath, sha, normalized)
	if err != nil {
		return codeintel.FileWindow{}, err
	}
	window := codeintel.FileWindow{
		Path:       normalized,
		Commit:     sha,
		Lines:      lines,
		StartLine:  1,
		EndLine:    len(lines),
		TotalLines: len(lines),
	}
	if maxLines > 0 && len(window.Lines) > maxLines {
		window.Lines = window.Lines[:maxLines]
		window.EndLine = maxLines
		window.Truncated = true
	}
	return window, nil
}

// readWindow is the shared read implementation.
func (g *Git) readWindow(ctx context.Context, repoPath, commit, filePath string, start, end, maxBytes, maxLines int) (codeintel.FileWindow, error) {
	normalized, err := normalizeGitPath(filePath)
	if err != nil {
		return codeintel.FileWindow{}, err
	}
	if maxBytes <= 0 {
		maxBytes = codeintel.MaxFileReadBytes
	}
	sha, _, err := g.blobEntry(ctx, repoPath, commit, normalized)
	if err != nil {
		return codeintel.FileWindow{}, err
	}
	lines, err := g.readTextBlob(ctx, repoPath, sha, normalized)
	if err != nil {
		return codeintel.FileWindow{}, err
	}
	return buildWindow(normalized, sha, lines, start, end, maxLines, maxBytes)
}

// blobEntry resolves commit, normalizes the path, verifies the path is a
// regular file at that commit and returns the resolved SHA plus the ls-tree
// size record.
func (g *Git) blobEntry(ctx context.Context, repoPath, commit, normalizedPath string) (string, treeEntry, error) {
	sha, err := g.resolveVerified(ctx, repoPath, commit)
	if err != nil {
		return "", treeEntry{}, err
	}
	entry, found, err := g.lsTreeEntry(ctx, repoPath, sha, normalizedPath)
	if err != nil {
		return "", treeEntry{}, err
	}
	if !found {
		return "", treeEntry{}, apiError(http.StatusNotFound, CodePathNotFound, "path was not found in the commit")
	}
	switch {
	case entry.objectType == "tree":
		return "", treeEntry{}, apiError(http.StatusUnprocessableEntity, CodePathNotFile, "path is a directory, not a file")
	case entry.objectType != "blob":
		return "", treeEntry{}, apiError(http.StatusUnprocessableEntity, CodeUnsupportedContent, "path is not a regular file")
	case entry.mode == "120000" || entry.mode == "160000":
		return "", treeEntry{}, apiError(http.StatusUnprocessableEntity, CodeUnsupportedContent, "symbolic links and submodules are not readable")
	case entry.mode != "100644" && entry.mode != "100755":
		return "", treeEntry{}, apiError(http.StatusUnprocessableEntity, CodeUnsupportedContent, "file mode is not supported")
	}
	return sha, entry, nil
}

// treeEntry is the minimal ls-tree metadata needed by file reads.
type treeEntry struct {
	mode       string
	objectType string
	objectSHA  string
	size       int64
}

// lsTreeEntry reads the exact (literal) path entry at a resolved commit.
func (g *Git) lsTreeEntry(ctx context.Context, repoPath, sha, filePath string) (treeEntry, bool, error) {
	stdout, stderr, err := g.execute(ctx, repoPath, "ls-tree", "-l", "-z", sha, "--", ":(literal)"+filePath)
	if err != nil {
		return treeEntry{}, false, g.mapExecutionError(stderr, err)
	}
	records := splitNUL(stdout)
	for _, record := range records {
		if record == "" {
			continue
		}
		tab := strings.IndexByte(record, '\t')
		if tab < 0 {
			return treeEntry{}, false, apiError(http.StatusInternalServerError, CodeCommandFailed, "git returned a malformed tree record")
		}
		meta := strings.Fields(record[:tab])
		if len(meta) != 4 {
			return treeEntry{}, false, apiError(http.StatusInternalServerError, CodeCommandFailed, "git returned a malformed tree record")
		}
		if record[tab+1:] != filePath {
			continue
		}
		size := int64(0)
		if meta[3] != "-" {
			parsed, parseErr := strconv.ParseInt(meta[3], 10, 64)
			if parseErr != nil || parsed < 0 {
				return treeEntry{}, false, apiError(http.StatusInternalServerError, CodeCommandFailed, "git returned a malformed blob size")
			}
			size = parsed
		}
		return treeEntry{mode: meta[0], objectType: meta[1], objectSHA: meta[2], size: size}, true, nil
	}
	return treeEntry{}, false, nil
}

// readTextBlob runs `git show <sha>:<path>` and applies the strict text rules:
// NUL bytes are binary, invalid UTF-8 is unsupported.
func (g *Git) readTextBlob(ctx context.Context, repoPath, sha, filePath string) ([]string, error) {
	stdout, stderr, err := g.execute(ctx, repoPath, "show", sha+":"+filePath)
	if err != nil {
		return nil, g.mapExecutionError(stderr, err)
	}
	if strings.IndexByte(string(stdout), 0) >= 0 {
		return nil, apiError(http.StatusUnprocessableEntity, CodeBinaryFile, "file is binary and has no text window")
	}
	if !utf8.Valid(stdout) {
		return nil, apiError(http.StatusUnprocessableEntity, CodeUnsupportedContent, "file is not valid UTF-8 text")
	}
	return splitTextLines(string(stdout)), nil
}

// splitTextLines splits blob text on LF, drops the artificial trailing empty
// line and normalizes CRLF to LF.
func splitTextLines(content string) []string {
	if content == "" {
		return []string{}
	}
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for index, line := range lines {
		lines[index] = strings.TrimSuffix(line, "\r")
	}
	return lines
}

// buildWindow applies the 1-based closed range, the line cap and the byte
// budget (never splitting a UTF-8 rune) to one file's lines.
func buildWindow(filePath, sha string, lines []string, start, end, maxLines, maxBytes int) (codeintel.FileWindow, error) {
	total := len(lines)
	if total == 0 {
		return codeintel.FileWindow{
			Path: filePath, Commit: sha, Lines: []string{},
			StartLine: 1, EndLine: 0, TotalLines: 0,
		}, nil
	}
	if start <= 0 {
		start = 1
	}
	if end <= 0 {
		end = total
	}
	if start < 1 || start > total {
		return codeintel.FileWindow{}, apiError(http.StatusUnprocessableEntity, CodeInvalidRange, "start line is outside the file")
	}
	if end > total {
		end = total
	}
	if end < start {
		return codeintel.FileWindow{}, apiError(http.StatusUnprocessableEntity, CodeInvalidRange, "end line is before start line")
	}

	truncated := false
	if maxLines > 0 && end-start+1 > maxLines {
		end = start + maxLines - 1
		truncated = true
	}
	selected := lines[start-1 : end]
	window := make([]string, 0, len(selected))
	used := 0
	for _, line := range selected {
		needed := len(line)
		if len(window) > 0 {
			needed++
		}
		if used+needed > maxBytes {
			if len(window) == 0 {
				window = append(window, truncateUTF8Bytes(line, maxBytes))
			}
			truncated = true
			break
		}
		window = append(window, line)
		used += needed
	}
	endLine := start + len(window) - 1
	if endLine < start {
		endLine = start
	}
	return codeintel.FileWindow{
		Path:       filePath,
		Commit:     sha,
		Lines:      window,
		StartLine:  start,
		EndLine:    endLine,
		TotalLines: total,
		Truncated:  truncated,
	}, nil
}

// truncateUTF8Bytes cuts a string to at most limit bytes on a rune boundary.
func truncateUTF8Bytes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}

// truncateRunes bounds a string by rune count for commit messages.
func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
