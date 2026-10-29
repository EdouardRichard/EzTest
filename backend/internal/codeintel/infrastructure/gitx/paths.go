package gitx

import (
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// commitRE is the full SHA-1 commit id form pinned by design spec §6.1/§15.
var commitRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

// The error constructors below keep every git verdict a typed domain error
// with a stable details.reason. Most reasons are the §7.1 table values;
// path_not_git_repo and invalid_line_range are the two documented extensions
// (P2 repo validation and the file-window bounds) and are also returned as
// domain.ErrInvalid so handlers answer 422. Environment failures (timeout,
// output overflow, exec failure, unreadable localPath) deliberately do NOT use
// these constructors: they stay non-domain so handlers map them to 503
// git_unavailable (ruling R19).

// invalidPath denies a path that could escape the commit tree.
func invalidPath() error {
	return domain.NewInvalid("path", "path_escape",
		"codeintel: path must be a repository-relative slash path inside the repository root")
}

// invalidLineRange denies a malformed 1-based line window.
func invalidLineRange() error {
	return domain.NewInvalid("startLine", "invalid_line_range",
		"codeintel: line window must satisfy 1 <= startLine <= endLine")
}

// pathNotFound reports a path that does not exist at the pinned commit.
func pathNotFound() error {
	return domain.NewInvalid("path", "path_not_found",
		"codeintel: path does not exist at the requested commit")
}

// pathNotFile reports a directory, symlink or submodule where a file is needed.
func pathNotFile() error {
	return domain.NewInvalid("path", "path_not_file",
		"codeintel: path is not a regular file at the requested commit")
}

// commitNotFound reports an unresolvable commit id.
func commitNotFound() error {
	return domain.NewInvalid("commit", "commit_not_found",
		"codeintel: commit does not resolve in this repository")
}

// invalidHead reports a branch or head that cannot be resolved.
func invalidHead() error {
	return domain.NewInvalid("head", "invalid_head",
		"codeintel: fixed branch or commit does not resolve to a commit")
}

// unsupportedContent reports a binary, non-UTF-8 or oversized file read.
func unsupportedContent() error {
	return domain.NewInvalid("path", "unsupported_content",
		"codeintel: content is binary, not valid UTF-8 or above the read limit")
}

// notChanged reports a path that is not part of the change set.
func notChanged() error {
	return domain.NewInvalid("path", "not_changed",
		"codeintel: path is not part of the requested change set")
}

// repoUnavailable reports a local repository path that is missing or not a
// readable directory. It is an operational failure rather than a domain
// verdict so handlers map it to 503 git_unavailable (ruling R19).
func repoUnavailable() error {
	return fmt.Errorf("%w: local path does not exist or is not a directory", errRepoUnavailable)
}

// notGitWorkTree reports a local path that is not a Git work tree.
func notGitWorkTree() error {
	return domain.NewInvalid("localPath", "path_not_git_repo",
		"codeintel: local path is not a Git work tree")
}

// validCommit reports whether commit is a full lowercase SHA-1 id.
func validCommit(commit string) bool {
	return commitRE.MatchString(commit)
}

// validBranch reports whether branch is a safe fixed branch name. It accepts
// the names git accepts for refs/heads, including "+", "=" and UTF-8, so a
// valid configured branch is never rejected. It still rejects option-like
// names and the revision operators ~ ^ : ? * [ \ and "@{" so a branch can
// never reach git as a command line flag or a revision expression.
func validBranch(branch string) bool {
	if branch == "" || branch == "@" || len(branch) > 255 {
		return false
	}
	if strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") {
		return false
	}
	if strings.Contains(branch, "..") || strings.Contains(branch, "@{") {
		return false
	}
	for _, component := range strings.Split(branch, "/") {
		if component == "" ||
			strings.HasPrefix(component, ".") ||
			strings.HasSuffix(component, ".") ||
			strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	for index := 0; index < len(branch); index++ {
		char := branch[index]
		if char <= ' ' || char == 0x7f {
			return false
		}
		switch char {
		case '~', '^', ':', '?', '*', '[', '\\':
			return false
		}
	}
	return true
}

// normalizeRepoPath slash-normalizes a repository-relative path and rejects
// absolute paths, Windows volume/UNC forms, backslashes and any ".." component.
// The cleaned path is what every git command receives.
func normalizeRepoPath(raw string) (string, error) {
	if raw == "" || strings.ContainsRune(raw, '\x00') || strings.ContainsRune(raw, '\\') {
		return "", invalidPath()
	}
	if strings.HasPrefix(raw, "/") || filepath.IsAbs(raw) || filepath.VolumeName(raw) != "" {
		return "", invalidPath()
	}
	// A Windows drive prefix stays a plausible relative path on a Linux host,
	// so reject the shape explicitly on both platforms.
	if len(raw) >= 2 && raw[1] == ':' && isASCIIAlpha(raw[0]) {
		return "", invalidPath()
	}
	for _, component := range strings.Split(raw, "/") {
		if component == ".." {
			return "", invalidPath()
		}
	}
	cleaned := path.Clean(raw)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		return "", invalidPath()
	}
	if len(cleaned) > domain.MaxPathChars {
		return "", invalidPath()
	}
	return cleaned, nil
}

// normalizeTreePrefix trims the conventional separators of a tree listing
// prefix. The empty prefix means the repository root.
func normalizeTreePrefix(prefix string) (string, error) {
	trimmed := strings.Trim(prefix, "/")
	if trimmed == "" {
		return "", nil
	}
	return normalizeRepoPath(trimmed)
}

// isASCIIAlpha reports whether b is an ASCII letter.
func isASCIIAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// literalPathspec renders a repository-relative path as a literal git pathspec
// so wildcard characters in a real file name cannot expand to other files.
func literalPathspec(repoPath string) string {
	return ":(literal)" + repoPath
}

// compiledGlob is one include/exclude pattern compiled once per tree listing.
// A pattern without wildcards keeps its literal form, trailing slash included,
// because that is the P3 gitread semantics design spec §6.1 requires: a plain
// name also matches every descendant, while a trailing slash matches no file.
type compiledGlob struct {
	literal string
	pattern *regexp.Regexp
}

// pathFilter is the compiled include/exclude pair of one tree listing.
type pathFilter struct {
	include []compiledGlob
	exclude []compiledGlob
}

// compilePathFilter compiles the repository globs once so a tree listing with
// many entries does not recompile every pattern per entry.
func compilePathFilter(include, exclude []string) pathFilter {
	return pathFilter{include: compileGlobs(include), exclude: compileGlobs(exclude)}
}

// compileGlobs compiles the non-blank globs; an invalid wildcard pattern is
// dropped and therefore matches nothing.
func compileGlobs(globs []string) []compiledGlob {
	compiled := make([]compiledGlob, 0, len(globs))
	for _, glob := range globs {
		glob = strings.TrimSpace(strings.ReplaceAll(glob, `\`, "/"))
		if glob == "" {
			continue
		}
		if !strings.ContainsAny(glob, "*?[") {
			compiled = append(compiled, compiledGlob{literal: glob})
			continue
		}
		pattern, err := globPattern(strings.TrimSuffix(glob, "/"))
		if err != nil {
			continue
		}
		compiled = append(compiled, compiledGlob{pattern: pattern})
	}
	return compiled
}

// includes applies the P3 gitread filter semantics: an entry must match at
// least one include glob (when any) and no exclude glob; exclude wins.
func (f pathFilter) includes(entryPath string) bool {
	if len(f.include) > 0 && !matchesAnyCompiledGlob(f.include, entryPath) {
		return false
	}
	return !matchesAnyCompiledGlob(f.exclude, entryPath)
}

// matchesAnyCompiledGlob reports whether p matches at least one compiled glob.
func matchesAnyCompiledGlob(globs []compiledGlob, p string) bool {
	for _, glob := range globs {
		if glob.matches(p) {
			return true
		}
	}
	return false
}

// matches applies one compiled glob. A literal matches itself and descendants;
// a wildcard pattern must match the whole path.
func (g compiledGlob) matches(p string) bool {
	if g.pattern != nil {
		return g.pattern.MatchString(p)
	}
	return p == g.literal || strings.HasPrefix(p, g.literal+"/")
}

// globPattern compiles a simple shell-style glob into an anchored expression.
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
