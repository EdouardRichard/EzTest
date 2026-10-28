package codeintel

import (
	"context"
	"errors"
	"fmt"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	codeinteldomain "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	deliveryagenttool "github.com/EdouardRichard/EzTest/backend/internal/delivery/application/agent_tool"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// codeReadActor is the system actor recorded on the autonomous analysis code
// reads (the P0 audit log keeps the actor optional).
const codeReadActor int64 = 0

// VersionReader is the version application slice the code reader needs.
// *version/application.VersionService satisfies it.
type VersionReader interface {
	Get(ctx context.Context, id int64) (*versionapp.Version, error)
}

// FileReader is the codeintel query slice the code reader needs.
// *codeintel/application.QueryService satisfies it.
type FileReader interface {
	File(ctx context.Context, actorID, repoID int64, commit, path string, startLine, endLine int) (codeinteldomain.FileWindow, error)
}

// CodeRead implements delivery/application/agent_tool.CodeReader: bounded,
// commit-pinned code windows of the version's bound repository (spec §9.3). It
// returns found=false for a path outside the commit; the caller only bounds the
// window, the adapter enforces the repository containment.
type CodeRead struct {
	versions VersionReader
	files    FileReader
}

// NewCodeRead wires the delivery agent tools' code reader.
func NewCodeRead(versions VersionReader, files FileReader) *CodeRead {
	return &CodeRead{versions: versions, files: files}
}

var _ deliveryagenttool.CodeReader = (*CodeRead)(nil)

// ReadCode implements delivery/application/agent_tool.CodeReader.
func (c *CodeRead) ReadCode(ctx context.Context, versionID int64, path string, startLine, endLine int) (deliveryagenttool.CodeWindow, bool, error) {
	if c == nil || c.versions == nil || c.files == nil {
		return deliveryagenttool.CodeWindow{}, false, errors.New("delivery: code reader is not configured")
	}
	version, err := c.versions.Get(ctx, versionID)
	if err != nil {
		if isVersionNotFound(err) {
			return deliveryagenttool.CodeWindow{}, false, nil
		}
		return deliveryagenttool.CodeWindow{}, false, fmt.Errorf("delivery: read version %d for code read: %w", versionID, err)
	}
	if version.CodeRepositoryID == nil || *version.CodeRepositoryID <= 0 ||
		version.CodeBaselineCommit == nil || *version.CodeBaselineCommit == "" {
		// A version without a bound repository has no commit-pinned code.
		return deliveryagenttool.CodeWindow{}, false, nil
	}
	window, err := c.files.File(ctx, codeReadActor, *version.CodeRepositoryID,
		*version.CodeBaselineCommit, path, startLine, endLine)
	if err != nil {
		if codeReadNotFound(err) {
			return deliveryagenttool.CodeWindow{}, false, nil
		}
		return deliveryagenttool.CodeWindow{}, false, fmt.Errorf("delivery: read code %q: %w", path, err)
	}
	if window.Binary || window.Unsupported {
		return deliveryagenttool.CodeWindow{}, false, nil
	}
	return deliveryagenttool.CodeWindow{
		Path:      window.Path,
		StartLine: window.StartLine,
		EndLine:   window.EndLine,
		Content:   window.Content,
		Truncated: window.Truncated,
	}, true, nil
}

// codeReadNotFound reports whether a read failure means "this path/commit is not
// in the repository" rather than an infrastructure failure.
func codeReadNotFound(err error) bool {
	switch codeinteldomain.Reason(err) {
	case "path_not_found", "path_not_file", "commit_not_found", "path_escape",
		"path_not_git_repo", "not_changed", "unsupported_content", "invalid_line_range":
		return true
	default:
		return false
	}
}

// isVersionNotFound reports whether a version read failed with not-found.
func isVersionNotFound(err error) bool {
	var domainErr *versiondomain.Error
	return errors.As(err, &domainErr) && domainErr != nil &&
		domainErr.Kind == versiondomain.KindNotFound
}

// Ensure the codeintel query service keeps satisfying the adapter contract; a
// signature drift fails the build here.
var _ FileReader = (*codeintelapp.QueryService)(nil)
