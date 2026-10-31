package resource

import (
	"net/url"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// Page statuses and sources. P0 only writes approved + admin/import.
const (
	PageStatusApproved = "approved"
	PageStatusProposed = "proposed"
	PageStatusRejected = "rejected"

	PageSourceAdmin  = "admin"
	PageSourceAI     = "ai"
	PageSourceImport = "import"
)

// Page is one UI page of a SUT environment.
type Page struct {
	ID            int64
	SUTID         int64
	EnvironmentID int64
	Name          string
	URL           string
	RouteTemplate string
	PageType      string
	AuthRole      string
	Module        string
	Status        string
	Source        string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Validate checks page fields. RouteTemplate is required; callers derive it
// from the URL when the caller omitted it.
func (p Page) Validate() error {
	if err := validation.String("name", p.Name, 1, 200); err != nil {
		return err
	}
	if err := validation.URL("url", p.URL); err != nil {
		return err
	}
	if err := validation.String("route_template", p.RouteTemplate, 1, 500); err != nil {
		return err
	}
	if !strings.HasPrefix(p.RouteTemplate, "/") {
		return platform.Invalid("route_template", "must start with /")
	}
	if err := validation.String("page_type", p.PageType, 1, 50); err != nil {
		return err
	}
	if err := validation.String("auth_role", p.AuthRole, 0, 100); err != nil {
		return err
	}
	if err := validation.String("module", p.Module, 0, 100); err != nil {
		return err
	}
	switch p.Status {
	case PageStatusApproved, PageStatusProposed, PageStatusRejected:
	default:
		return invalidEnum("status", p.Status)
	}
	switch p.Source {
	case PageSourceAdmin, PageSourceAI, PageSourceImport:
	default:
		return invalidEnum("source", p.Source)
	}
	return nil
}

// DeriveRouteTemplate returns the URL path used as the route template default.
func DeriveRouteTemplate(rawURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", platform.Invalid("url", "must be an absolute http(s) URL")
	}
	if parsed.Path == "" {
		return "/", nil
	}
	return parsed.Path, nil
}

func invalidEnum(field, value string) error {
	return platform.Invalid(field, "unsupported value "+value)
}
