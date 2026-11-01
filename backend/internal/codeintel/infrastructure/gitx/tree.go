package gitx

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// emptyTreeSHA is Git's canonical empty tree; it lets ListTree detect binary
// blobs with one whole-tree numstat command instead of one process per file.
const emptyTreeSHA = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// ListTree returns the regular blobs of commit, filtered by include/exclude
// globs, sorted by path and bounded by max. Symbolic links (120000) and
// submodules (160000) are returned with Unsupported=true so the application
// never feeds them to T1.
func (g *Git) ListTree(ctx context.Context, repoPath, commit string, include, exclude []string, max int) (codeintel.TreeResult, error) {
	if max <= 0 {
		max = codeintel.MaxTreeEntriesPerPage
	}
	sha, err := g.resolveVerified(ctx, repoPath, commit)
	if err != nil {
		return codeintel.TreeResult{}, err
	}
	stdout, stderr, err := g.execute(ctx, repoPath, "ls-tree", "-r", "-t", "-l", "-z", sha)
	if err != nil {
		return codeintel.TreeResult{}, g.mapExecutionError(stderr, err)
	}
	binaryPaths, err := g.binaryPaths(ctx, repoPath, sha)
	if err != nil {
		return codeintel.TreeResult{}, err
	}

	entries := make([]codeintel.TreeEntry, 0)
	for _, record := range splitNUL(stdout) {
		entry, ok, err := parseTreeRecord(record)
		if err != nil {
			return codeintel.TreeResult{}, apiError(500, CodeCommandFailed, "git returned a malformed tree record")
		}
		if !ok {
			continue
		}
		if len(include) > 0 && !matchesAnyGlob(include, entry.Path) {
			continue
		}
		if matchesAnyGlob(exclude, entry.Path) {
			continue
		}
		entry.Binary = binaryPaths[entry.Path]
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })

	total := len(entries)
	truncated := total > max
	if truncated {
		entries = entries[:max]
	}
	return codeintel.TreeResult{Entries: entries, Total: total, Truncated: truncated}, nil
}

// binaryPaths runs one empty-tree numstat diff and returns the set of paths Git
// classified as binary. The command reads objects only; it never touches the
// working tree.
func (g *Git) binaryPaths(ctx context.Context, repoPath, sha string) (map[string]bool, error) {
	stdout, stderr, err := g.execute(ctx, repoPath, "diff", "--numstat", "-z",
		"--no-ext-diff", emptyTreeSHA, sha)
	if err != nil {
		return nil, g.mapExecutionError(stderr, err)
	}
	binary := make(map[string]bool)
	for _, record := range splitNUL(stdout) {
		if record == "" {
			continue
		}
		parts := strings.SplitN(record, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		if parts[0] == "-" && parts[1] == "-" && parts[2] != "" {
			binary[parts[2]] = true
		}
	}
	return binary, nil
}

// parseTreeRecord parses one -z ls-tree record:
//
//	<mode> <type> <object> <size>\t<path>
//
// Directories are skipped (T0 keeps regular-file blobs only).
func parseTreeRecord(record string) (codeintel.TreeEntry, bool, error) {
	if len(record) == 0 {
		return codeintel.TreeEntry{}, false, nil
	}
	tab := strings.IndexByte(record, '\t')
	if tab < 0 {
		return codeintel.TreeEntry{}, false, fmt.Errorf("missing path separator")
	}
	meta := strings.Fields(record[:tab])
	filePath := record[tab+1:]
	if len(meta) != 4 {
		return codeintel.TreeEntry{}, false, fmt.Errorf("unexpected metadata field count %d", len(meta))
	}
	mode, objectType, objectSHA, rawSize := meta[0], meta[1], meta[2], meta[3]
	if objectType != "blob" {
		return codeintel.TreeEntry{}, false, nil
	}
	size := int64(0)
	if rawSize != "-" {
		parsed, err := strconv.ParseInt(rawSize, 10, 64)
		if err != nil || parsed < 0 {
			return codeintel.TreeEntry{}, false, fmt.Errorf("invalid blob size")
		}
		size = parsed
	}
	unsupported := mode == "120000" || mode == "160000"
	return codeintel.TreeEntry{
		Path:        filePath,
		Mode:        mode,
		Type:        objectType,
		BlobSHA:     objectSHA,
		Size:        size,
		Unsupported: unsupported,
	}, true, nil
}

// splitNUL splits NUL-separated -z output, dropping the trailing empty token.
func splitNUL(raw []byte) []string {
	parts := strings.Split(string(raw), "\x00")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

// matchesAnyGlob reports whether any pattern matches the slash-normalized path.
func matchesAnyGlob(patterns []string, filePath string) bool {
	for _, pattern := range patterns {
		if matchGlob(pattern, filePath) {
			return true
		}
	}
	return false
}

// matchGlob implements the gitignore-style subset used by the codeintel
// settings: "**" crosses path separators, "*" and "?" do not, and a pattern
// without "/" also matches a basename.
func matchGlob(pattern, filePath string) bool {
	pattern = strings.TrimSpace(strings.ReplaceAll(pattern, "\\", "/"))
	if pattern == "" {
		return false
	}
	pattern = strings.TrimPrefix(pattern, "./")
	if !strings.Contains(pattern, "/") {
		if matched, _ := path.Match(pattern, path.Base(filePath)); matched {
			return true
		}
	}
	expression, err := regexp.Compile(globToRegex(pattern))
	if err != nil {
		return false
	}
	return expression.MatchString(filePath)
}

// globToRegex translates a slash-normalized glob into an anchored regular
// expression.
func globToRegex(pattern string) string {
	var builder strings.Builder
	builder.WriteString("^")
	for index := 0; index < len(pattern); index++ {
		switch pattern[index] {
		case '*':
			if index+1 < len(pattern) && pattern[index+1] == '*' {
				index++
				if index+1 < len(pattern) && pattern[index+1] == '/' {
					index++
					builder.WriteString("(?:.*/)?")
				} else {
					builder.WriteString(".*")
				}
			} else {
				builder.WriteString("[^/]*")
			}
		case '?':
			builder.WriteString("[^/]")
		default:
			builder.WriteString(regexp.QuoteMeta(string(pattern[index])))
		}
	}
	builder.WriteString("$")
	return builder.String()
}
