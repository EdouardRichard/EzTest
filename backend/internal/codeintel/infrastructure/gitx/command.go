package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	// defaultCommandTimeout is the per-command bound of design spec §6.1. The
	// adapter can lower it through GitConfig.CommandTimeout.
	defaultCommandTimeout = 10 * time.Second
	// maxCommandOutput bounds one git command's stdout and stderr. Git output
	// is parsed in memory (tree listings, -z diffs, blobs), so an oversized
	// stream is rejected instead of buffered without limit.
	maxCommandOutput = 4 << 20
)

// errCommandOutputTooLarge marks a git command whose stdout or stderr exceeded
// maxCommandOutput.
var errCommandOutputTooLarge = errors.New("gitx: git command output exceeds the 4 MiB bound")

// errMalformedOutput marks git output that does not match the fixed machine
// readable formats this adapter parses (-z and NUL-separated records).
var errMalformedOutput = errors.New("gitx: unexpected git output format")

// errRepoUnavailable marks a localPath that is missing or unreadable. It is
// deliberately not a domain.ErrInvalid: it is an environment failure, so the
// HTTP layer maps it to 503 git_unavailable instead of a 422 reason (ruling
// R19).
var errRepoUnavailable = errors.New("gitx: local repository path is unavailable")

// gitEnv returns the child environment with the repository-redirecting
// variables removed. An inherited GIT_DIR or GIT_WORK_TREE would otherwise let
// git resolve the validated localPath against a different repository, and
// GIT_OPTIONAL_LOCKS=0 keeps git from refreshing the index or taking any other
// optional lock. Names are compared case-insensitively because Windows
// environment names are.
func gitEnv() []string {
	environ := os.Environ()
	env := make([]string, 0, len(environ)+1)
	for _, entry := range environ {
		name, _, ok := strings.Cut(entry, "=")
		if ok && (strings.EqualFold(name, "GIT_DIR") || strings.EqualFold(name, "GIT_WORK_TREE")) {
			continue
		}
		env = append(env, entry)
	}
	return append(env, "GIT_OPTIONAL_LOCKS=0")
}

// boundedBuffer keeps at most maxCommandOutput bytes and records an overflow
// instead of allocating for the rest of the stream.
type boundedBuffer struct {
	buf      bytes.Buffer
	overflow bool
}

// Write implements io.Writer.
func (b *boundedBuffer) Write(p []byte) (int, error) {
	remaining := maxCommandOutput - b.buf.Len()
	if remaining <= 0 {
		b.overflow = true
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = b.buf.Write(p[:remaining])
		b.overflow = true
		return len(p), nil
	}
	_, _ = b.buf.Write(p)
	return len(p), nil
}

// commandError is one failed git process. Standard error is captured only to
// detect overflow; Error never echoes it so repository paths and git advice
// cannot leak through API error messages or logs.
type commandError struct {
	command string
	code    int
	cause   error
}

// Error implements error without exposing standard error.
func (e *commandError) Error() string {
	if e.code >= 0 {
		return fmt.Sprintf("gitx: git %s failed with exit code %d", e.command, e.code)
	}
	return fmt.Sprintf("gitx: git %s failed: %v", e.command, e.cause)
}

// Unwrap exposes the underlying process error.
func (e *commandError) Unwrap() error {
	return e.cause
}

// run executes one read-only git command in dir and returns its raw standard
// output. The command never goes through a shell, runs with GIT_OPTIONAL_LOCKS=0
// so it cannot refresh the index or take optional locks, and is bounded by
// GitConfig.CommandTimeout.
func (r *GitReader) run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	commandCtx, cancel := context.WithTimeout(ctx, r.cfg.CommandTimeout)
	defer cancel()

	cmd := exec.CommandContext(commandCtx, "git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	var stdout, stderr boundedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	if ctxErr := commandCtx.Err(); ctxErr != nil {
		return nil, fmt.Errorf("gitx: git %s: %w", args[0], ctxErr)
	}
	if stdout.overflow || stderr.overflow {
		return nil, fmt.Errorf("%w (git %s)", errCommandOutputTooLarge, args[0])
	}
	if runErr != nil {
		code := -1
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			code = exitErr.ExitCode()
		}
		return nil, &commandError{
			command: args[0],
			code:    code,
			cause:   runErr,
		}
	}
	return stdout.buf.Bytes(), nil
}

// operationalError reports whether err is an environment or cancellation
// failure rather than a git verdict about the repository. Handlers map these
// errors to 503 git_unavailable (ruling R19).
func operationalError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if errors.Is(err, errCommandOutputTooLarge) || errors.Is(err, errRepoUnavailable) {
		return true
	}
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return true
	}
	var pathErr *os.PathError
	return errors.As(err, &pathErr)
}

// exitCode returns the process exit code carried by err.
func exitCode(err error) (int, bool) {
	var commandErr *commandError
	if errors.As(err, &commandErr) {
		return commandErr.code, true
	}
	return 0, false
}

// missingObject reports whether a git command failed because a commit or
// object could not be resolved.
func missingObject(err error) bool {
	code, ok := exitCode(err)
	return ok && code == 128
}
