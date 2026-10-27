// Package gitexec implements the read-only local Git validator of spec §6.7:
// it proves that a path is a Git work tree, resolves a fixed branch to its
// commit and never writes to the repository.
package gitexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// defaultTimeout bounds one whole validation run, including every git process.
const defaultTimeout = 5 * time.Second

// commitRE is the full SHA-1 commit id git rev-parse prints.
var commitRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Validator checks a local Git checkout through fixed, read-only git commands.
type Validator struct {
	timeout time.Duration
}

// NewValidator returns a validator that gives one run up to 5 seconds.
func NewValidator() *Validator {
	return &Validator{timeout: defaultTimeout}
}

// Validate reports the commit the fixed branch points at. A missing path or a
// path that is not a Git work tree is rejected with reason path_not_found /
// path_not_git_repo; a branch that does not resolve to a commit is rejected
// with reason branch_not_found. Operational failures (git missing, timeout,
// canceled context) keep their own error so the caller can tell them apart
// from a validation verdict.
func (v *Validator) Validate(ctx context.Context, path, branch string) (domain.GitValidation, error) {
	// The branch format is checked before any path or git work so an option-like
	// "branch" can never reach the git command line.
	if err := domain.ValidateBranch(branch); err != nil {
		return domain.GitValidation{}, err
	}

	if strings.TrimSpace(path) == "" {
		return domain.GitValidation{}, pathNotFound(path)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return domain.GitValidation{}, fmt.Errorf("gitexec: resolve local path %q: %w", path, err)
	}
	absPath = filepath.Clean(absPath)
	info, err := os.Stat(absPath)
	if err != nil || !info.IsDir() {
		return domain.GitValidation{}, pathNotFound(path)
	}

	if v.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, v.timeout)
		defer cancel()
	}

	inside, err := v.git(ctx, absPath, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		if operational := operationalError(err); operational != nil {
			return domain.GitValidation{}, operational
		}
		return domain.GitValidation{}, notGitRepo(absPath)
	}
	if inside != "true" {
		return domain.GitValidation{}, notGitRepo(absPath)
	}

	// The quiet, verifying form decides branch existence without output.
	if _, err := v.git(ctx, absPath, "rev-parse", "--verify", "--quiet", branch+"^{commit}"); err != nil {
		if operational := operationalError(err); operational != nil {
			return domain.GitValidation{}, operational
		}
		return domain.GitValidation{}, branchNotFound(branch)
	}

	commitOutput, err := v.git(ctx, absPath, "rev-parse", branch+"^{commit}")
	if err != nil {
		return domain.GitValidation{}, fmt.Errorf("gitexec: resolve branch %q: %w", branch, err)
	}
	commit := strings.TrimSpace(commitOutput)
	if !commitRE.MatchString(commit) {
		return domain.GitValidation{}, fmt.Errorf("gitexec: branch %q resolved to unexpected commit id %q", branch, commit)
	}
	return domain.GitValidation{Commit: commit}, nil
}

// git runs one fixed git command in dir and returns its trimmed standard
// output. Arguments are passed as an argv slice, never through a shell; the
// optional-locks environment variable keeps Git from refreshing the index or
// taking any other optional lock, so the checkout is left untouched.
func (v *Validator) git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// A canceled context or the validator's own deadline is not a verdict
		// about the repository; surface it so callers do not map it to a 422.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", fmt.Errorf("gitexec: git %s: %w", args[0], ctxErr)
		}
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// operationalError recognizes a git failure caused by cancellation, the
// validator deadline or a git executable that cannot be started. It returns nil
// for an ordinary negative result, which the caller translates into a path or
// branch verdict.
func operationalError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return err
	}
	return nil
}

// pathNotFound reports a local path that is absent or is not a directory.
func pathNotFound(path string) error {
	return domain.NewInvalid("localPath", "path_not_found",
		fmt.Sprintf("local path %q does not exist or is not a directory", path))
}

// notGitRepo reports a directory that is not a Git work tree.
func notGitRepo(absPath string) error {
	return domain.NewInvalid("localPath", "path_not_git_repo",
		fmt.Sprintf("local path %q is not a Git work tree", absPath))
}

// branchNotFound reports a branch that does not resolve to a commit.
func branchNotFound(branch string) error {
	return domain.NewInvalid("fixedBranch", "branch_not_found",
		fmt.Sprintf("branch %q does not resolve to a commit", branch))
}
