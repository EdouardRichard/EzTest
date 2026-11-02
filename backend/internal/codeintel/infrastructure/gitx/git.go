package gitx

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// DefaultCommandTimeout is the read-only git command budget (spec §5.1).
const DefaultCommandTimeout = 10 * time.Second

// Runner executes one git argv command inside dir. It must not use a shell.
// Implementations return stdout, stderr and the process error.
type Runner interface {
	Run(ctx context.Context, dir string, args ...string) (stdout, stderr []byte, err error)
}

// ExecRunner is the production Runner backed by os/exec.
type ExecRunner struct{}

// Run executes git with a locked-down environment: optional locks disabled,
// prompts disabled and a C locale so error sniffing is deterministic.
func (ExecRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, []byte, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = dir
	command.Env = gitEnvironment()
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// gitEnvironment builds the child environment without duplicate Git variables.
func gitEnvironment() []string {
	blocked := map[string]bool{
		"GIT_OPTIONAL_LOCKS":  true,
		"GIT_TERMINAL_PROMPT": true,
		"LC_ALL":              true,
		"LANG":                true,
	}
	environment := make([]string, 0, len(os.Environ())+4)
	for _, entry := range os.Environ() {
		name := entry
		if index := strings.IndexByte(entry, '='); index >= 0 {
			name = entry[:index]
		}
		if blocked[strings.ToUpper(name)] {
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment,
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0",
		"LC_ALL=C",
		"LANG=C",
	)
}

// Git is the read-only GitHub-facing client. All methods resolve refs to a
// commit SHA before reading objects, so a caller-supplied ref can never select
// a non-commit object.
type Git struct {
	runner  Runner
	timeout time.Duration
}

// New builds the Git client. A nil runner uses ExecRunner; a non-positive
// timeout falls back to DefaultCommandTimeout.
func New(runner Runner, timeout time.Duration) *Git {
	if runner == nil {
		runner = ExecRunner{}
	}
	if timeout <= 0 {
		timeout = DefaultCommandTimeout
	}
	return &Git{runner: runner, timeout: timeout}
}

var (
	commitSHAPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	// Ref names accept branch/tag shapes but never an option-like leading dash,
	// whitespace or control characters.
	refPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._/@{}~^+-]*$`)
)

// ResolveCommit resolves a ref (branch, tag, short ref) or 40-hex SHA to the
// full commit SHA. Nonexistent refs map to commit_not_found.
func (g *Git) ResolveCommit(ctx context.Context, repoPath, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" || !refPattern.MatchString(ref) || strings.HasPrefix(ref, "-") {
		return "", apiError(http.StatusUnprocessableEntity, CodeCommitNotFound, "commit reference is not valid")
	}
	return g.resolveVerified(ctx, repoPath, ref)
}

// HeadCommit resolves the current head of branch (empty branch = default HEAD).
// A branch that cannot be resolved maps to invalid_head.
func (g *Git) HeadCommit(ctx context.Context, repoPath, branch string) (string, error) {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return g.resolveVerified(ctx, repoPath, "HEAD")
	}
	if !refPattern.MatchString(branch) || strings.HasPrefix(branch, "-") {
		return "", apiError(http.StatusUnprocessableEntity, CodeInvalidHead, "head branch is not valid")
	}
	sha, err := g.resolveVerified(ctx, repoPath, "refs/heads/"+branch)
	if err != nil {
		if apiErr := platform.AsError(err); apiErr.Code == CodeCommitNotFound {
			return "", apiError(http.StatusUnprocessableEntity, CodeInvalidHead, "head branch was not found")
		}
		return "", err
	}
	return sha, nil
}

// IsAncestor reports whether base is an ancestor of head (merge-base
// --is-ancestor). It returns false with no error when the commits are unrelated.
func (g *Git) IsAncestor(ctx context.Context, repoPath, base, head string) (bool, error) {
	if !commitSHAPattern.MatchString(base) || !commitSHAPattern.MatchString(head) {
		return false, apiError(http.StatusUnprocessableEntity, CodeCommitNotFound, "ancestor check requires 40-hex commit SHAs")
	}
	_, stderr, err := g.execute(ctx, repoPath, "merge-base", "--is-ancestor", base, head)
	if err == nil {
		return true, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return false, ctxErr
	}
	if code, ok := exitCode(err); ok && code == 1 {
		return false, nil
	}
	return false, g.mapExecutionError(stderr, err)
}

// Commit loads commit metadata (never file content) for CodeCommitView.
func (g *Git) Commit(ctx context.Context, repoPath, sha string, maxMessage int) (codeintel.CommitInfo, error) {
	sha = strings.TrimSpace(sha)
	if !commitSHAPattern.MatchString(sha) {
		return codeintel.CommitInfo{}, apiError(http.StatusUnprocessableEntity, CodeCommitNotFound, "commit SHA must be 40 lowercase hex characters")
	}
	resolved, err := g.resolveVerified(ctx, repoPath, sha)
	if err != nil {
		return codeintel.CommitInfo{}, err
	}
	format := "--format=%H%x00%an%x00%aI%x00%cI%x00%P%x00%B"
	stdout, stderr, err := g.execute(ctx, repoPath, "show", "-s", format, resolved)
	if err != nil {
		return codeintel.CommitInfo{}, g.mapExecutionError(stderr, err)
	}
	fields := strings.SplitN(string(stdout), "\x00", 6)
	if len(fields) != 6 {
		return codeintel.CommitInfo{}, apiError(http.StatusInternalServerError, CodeCommandFailed, "git returned malformed commit metadata")
	}
	authoredAt, err := time.Parse(time.RFC3339, strings.TrimSpace(fields[2]))
	if err != nil {
		return codeintel.CommitInfo{}, apiError(http.StatusInternalServerError, CodeCommandFailed, "git returned a malformed author timestamp")
	}
	committedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(fields[3]))
	if err != nil {
		return codeintel.CommitInfo{}, apiError(http.StatusInternalServerError, CodeCommandFailed, "git returned a malformed commit timestamp")
	}
	if maxMessage <= 0 || maxMessage > 4000 {
		maxMessage = 4000
	}
	message := strings.TrimRight(fields[5], "\n")
	message = truncateRunes(message, maxMessage)
	return codeintel.CommitInfo{
		SHA:         strings.TrimSpace(fields[0]),
		Message:     message,
		AuthorName:  strings.TrimSpace(fields[1]),
		AuthoredAt:  authoredAt,
		CommittedAt: committedAt,
		Parents:     strings.Fields(fields[4]),
	}, nil
}

// resolveVerified runs `git rev-parse --verify <ref>^{commit}` and validates the
// returned SHA shape. Callers must never trust a DB/model supplied ref.
func (g *Git) resolveVerified(ctx context.Context, repoPath, ref string) (string, error) {
	stdout, stderr, err := g.execute(ctx, repoPath, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", g.mapExecutionError(stderr, err)
	}
	sha := strings.TrimSpace(string(stdout))
	if !commitSHAPattern.MatchString(sha) {
		return "", apiError(http.StatusUnprocessableEntity, CodeCommitNotFound, "git did not resolve the reference to a commit")
	}
	return sha, nil
}

// execute validates the repository and runs one bounded git command.
func (g *Git) execute(ctx context.Context, repoPath string, args ...string) ([]byte, []byte, error) {
	if err := validateRepository(repoPath); err != nil {
		return nil, nil, err
	}
	runCtx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	stdout, stderr, err := g.runner.Run(runCtx, repoPath, args...)
	if err == nil {
		return stdout, stderr, nil
	}
	if parentErr := ctx.Err(); parentErr != nil {
		return nil, stderr, parentErr
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return nil, stderr, withCause(unavailableError(), context.DeadlineExceeded)
	}
	return stdout, stderr, err
}

// validateRepository rejects a missing path and a non-directory without ever
// including the path in the returned message.
func validateRepository(repoPath string) error {
	info, err := os.Stat(repoPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return apiError(http.StatusNotFound, CodeRepoNotFound, "code repository path was not found")
		}
		return apiError(http.StatusUnprocessableEntity, CodeRepoNotFound, "code repository path is not readable")
	}
	if !info.IsDir() {
		return apiError(http.StatusUnprocessableEntity, CodePathNotRepo, "code repository path is not a directory")
	}
	return nil
}

// mapExecutionError classifies a failed git command into stable codes. The
// stderr text is inspected but never returned to a caller.
func (g *Git) mapExecutionError(stderr []byte, err error) error {
	// Errors returned by execute itself (repository validation) are already
	// classified; never wrap them into a generic command failure.
	var classified *platform.Error
	if errors.As(err, &classified) {
		return err
	}
	if isContextError(err) {
		return withCause(unavailableError(), err)
	}
	if errors.Is(err, exec.ErrNotFound) {
		return withCause(unavailableError(), err)
	}
	message := strings.ToLower(string(stderr))
	switch {
	case strings.Contains(message, "not a git repository"):
		return apiError(http.StatusUnprocessableEntity, CodePathNotRepo, "code repository path is not a git working tree")
	case strings.Contains(message, "unknown revision"),
		strings.Contains(message, "bad revision"),
		strings.Contains(message, "needed a single revision"),
		strings.Contains(message, "bad object"),
		strings.Contains(message, "ambiguous argument"),
		strings.Contains(message, "invalid object name"):
		return apiError(http.StatusUnprocessableEntity, CodeCommitNotFound, "commit was not found")
	case strings.Contains(message, "does not exist in"),
		strings.Contains(message, "exists on disk, but not in"):
		return apiError(http.StatusNotFound, CodePathNotFound, "path was not found in the commit")
	}
	return apiError(http.StatusInternalServerError, CodeCommandFailed, "git command failed")
}
