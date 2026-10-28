// Package delivery adapts the delivery application service to the codeintel
// anchor use case's IssueCatalog port (design spec §7.9): an issue anchor may
// only be created when the issue exists in the addressed version. Codeintel only
// sees the delivery application interface; it never reads the issue table.
package delivery

import (
	"context"
	"errors"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// IssueReader is the delivery issue read slice the catalog needs.
// *delivery/application.IssueManager satisfies it.
type IssueReader interface {
	Get(ctx context.Context, versionID, issueID int64) (domain.Issue, error)
}

// IssueCatalog implements codeintel/application.IssueCatalog over the delivery
// issue service.
type IssueCatalog struct {
	issues IssueReader
}

// NewIssueCatalog wires the codeintel issue catalog. The delivery issue service
// may be bound later with Bind (the composition root builds codeintel before P7
// delivery); a call before the bind fails closed.
func NewIssueCatalog(issues IssueReader) *IssueCatalog {
	return &IssueCatalog{issues: issues}
}

// Bind fills the late-bound delivery issue service. It is called once by the
// composition root before the HTTP server starts.
func (c *IssueCatalog) Bind(issues IssueReader) {
	if c != nil {
		c.issues = issues
	}
}

var _ codeintelapp.IssueCatalog = (*IssueCatalog)(nil)

// GetIssue implements codeintel/application.IssueCatalog. A missing or foreign
// issue is found=false (never an error); every other failure is returned so the
// anchor call fails loudly instead of silently accepting a dangling subject.
func (c *IssueCatalog) GetIssue(ctx context.Context, versionID, issueID int64) (codeintelapp.IssueInfo, bool, error) {
	if c == nil || c.issues == nil {
		return codeintelapp.IssueInfo{}, false, errors.New("codeintel: issue catalog is not configured")
	}
	issue, err := c.issues.Get(ctx, versionID, issueID)
	if err != nil {
		if isIssueNotFound(err) {
			return codeintelapp.IssueInfo{}, false, nil
		}
		return codeintelapp.IssueInfo{}, false, err
	}
	return codeintelapp.IssueInfo{ID: issue.ID, VersionID: issue.VersionID}, true, nil
}

// isIssueNotFound reports a delivery not-found domain error.
func isIssueNotFound(err error) bool {
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr == nil {
		return false
	}
	return domainErr.Kind == domain.KindNotFound
}
