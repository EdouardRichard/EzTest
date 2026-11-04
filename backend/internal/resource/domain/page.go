package resource

import (
	"fmt"
	"net/url"
	"regexp"
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

// Validate checks page fields. RouteTemplate is optional (a blank value is
// derived from the URL by the application service); when present it must be a
// path starting with "/".
func (p Page) Validate() error {
	if err := validation.String("name", p.Name, 1, 200); err != nil {
		return err
	}
	if err := validation.URL("url", p.URL); err != nil {
		return err
	}
	if p.RouteTemplate != "" {
		if err := validation.String("route_template", p.RouteTemplate, 1, 500); err != nil {
			return err
		}
		if !strings.HasPrefix(p.RouteTemplate, "/") {
			return platform.Invalid("route_template", "must start with /")
		}
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

// ErrPageURLDuplicate reports a collision on (environment_id, normalized_url).
var ErrPageURLDuplicate = fmt.Errorf("page url already exists: %w", ErrDuplicate)

var (
	// pageURLPattern mirrors the eztest_normalize_url regular expression.
	pageURLPattern = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9+.-]*)://([^/?#]*)([^?#]*)(?:\?[^#]*)?(#.*)?$`)
	// pageHostPortPattern splits the last ":" followed by digits, as the SQL
	// function does for the default-port check.
	pageHostPortPattern = regexp.MustCompile(`^(.*):([0-9]+)$`)
	pageTrailingSlashes = regexp.MustCompile(`/+$`)
)

// NormalizePageURL returns the canonical form of a page URL. It implements the
// same rules as the eztest_normalize_url SQL function backing the generated
// sut_page.normalized_url column, so application-level checks and the database
// unique index cannot disagree:
//
//   - lowercase the scheme and host
//   - strip default ports (:80 for http, :443 for https)
//   - keep the path case-sensitive; drop trailing slashes except for root "/"
//   - keep the fragment (SPA routes live there)
//   - drop the query string
//   - preserve the userinfo component
//
// Invalid (non absolute http(s)) URLs are rejected before normalization.
func NormalizePageURL(raw string) (string, error) {
	if err := validation.URL("url", raw); err != nil {
		return "", err
	}
	trimmed := strings.Trim(raw, " ")
	matched := pageURLPattern.FindStringSubmatch(trimmed)
	if matched == nil {
		return trimmed, nil
	}

	scheme := strings.ToLower(matched[1])
	originalAuthority := matched[2]
	path := matched[3]
	fragment := matched[4]

	userinfo := ""
	hostPort := originalAuthority
	if at := strings.Index(originalAuthority, "@"); at >= 0 {
		userinfo = originalAuthority[:at+1]
		hostPort = originalAuthority[at+1:]
	}
	host, port := hostPort, ""
	if hostMatched := pageHostPortPattern.FindStringSubmatch(hostPort); hostMatched != nil {
		host, port = hostMatched[1], hostMatched[2]
	}
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	authority := userinfo + strings.ToLower(host)
	if port != "" {
		authority += ":" + port
	}

	if path == "" {
		path = "/"
	} else if trimmedPath := pageTrailingSlashes.ReplaceAllString(path, ""); trimmedPath != "" {
		path = trimmedPath
	} else {
		path = "/"
	}

	return scheme + "://" + authority + path + fragment, nil
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
