// Package gitx is the read-only Git adapter for the codeintel context (spec
// §5.1). Every command runs through an argv slice without a shell, with
// GIT_OPTIONAL_LOCKS=0, cwd set to the repository root and a per-command
// timeout. It never checks out, switches, stashes or writes a working tree; the
// only writes it can cause are Git's own object reads.
package gitx

import (
	"context"
	"errors"
	"net/http"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// Stable git error codes (spec §5.1). They are also the API error codes because
// the HTTP layer maps them without adding context (never a filesystem path).
const (
	CodeRepoNotFound       = "repo_not_found"
	CodePathNotRepo        = "path_not_repo"
	CodeGitUnavailable     = "git_unavailable"
	CodeCommitNotFound     = "commit_not_found"
	CodeInvalidHead        = "invalid_head"
	CodePathNotFound       = "path_not_found"
	CodePathNotFile        = "path_not_file"
	CodePathEscape         = "path_escape"
	CodeUnsupportedContent = "unsupported_content"
	CodeBinaryFile         = "binary_file"
	CodeFileTooLarge       = "file_too_large"
	CodeInvalidRange       = "invalid_range"
	CodeInvalidArgument    = "invalid_argument"
	CodeCommandFailed      = "git_command_failed"
)

func apiError(status int, code, message string) *platform.Error {
	return &platform.Error{Status: status, Code: code, Message: message}
}

func unavailableError() *platform.Error {
	error := apiError(http.StatusServiceUnavailable, CodeGitUnavailable, "git is temporarily unavailable")
	error.Retryable = true
	return error
}

// causalError keeps the stable platform error visible to errors.As while also
// wrapping the underlying cause so callers can use errors.Is (for example to
// detect context.DeadlineExceeded).
type causalError struct {
	api   *platform.Error
	cause error
}

func (e *causalError) Error() string { return e.api.Error() }

func (e *causalError) Unwrap() error { return e.cause }

// As lets platform.AsError and errors.As discover the embedded API error.
func (e *causalError) As(target any) bool {
	switch value := target.(type) {
	case **platform.Error:
		*value = e.api
		return true
	default:
		return false
	}
}

func (e *causalError) Is(target error) bool {
	return errors.Is(e.cause, target)
}

func withCause(api *platform.Error, cause error) error {
	return &causalError{api: api, cause: cause}
}

// exitCode returns the process exit code when the runner error exposes one.
func exitCode(err error) (int, bool) {
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) {
		return coded.ExitCode(), true
	}
	return 0, false
}

// isContextError reports whether err is a parent-cancellation error that must
// propagate unchanged instead of being remapped to a Git failure.
func isContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
