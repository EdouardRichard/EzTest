package resource

import (
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// API statuses and sources. P0 only writes approved + manual/import.
const (
	APIStatusApproved = "approved"
	APIStatusProposed = "proposed"
	APIStatusRejected = "rejected"

	APISourceManual  = "manual"
	APISourceImport  = "import"
	APISourceOpenAPI = "openapi"
	APISourceCode    = "code"
	APISourceAI      = "ai"
)

// API is one HTTP endpoint of a SUT environment.
type API struct {
	ID            int64
	SUTID         int64
	EnvironmentID int64
	Method        string
	PathTemplate  string
	Name          string
	AuthRole      string
	Source        string
	Status        string
	SchemaRef     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Validate checks API fields.
func (a API) Validate() error {
	if err := validation.HTTPMethod(a.Method); err != nil {
		return err
	}
	if err := validation.String("path_template", a.PathTemplate, 1, 500); err != nil {
		return err
	}
	if !strings.HasPrefix(a.PathTemplate, "/") {
		return platform.Invalid("path_template", "must start with /")
	}
	if err := validation.String("name", a.Name, 1, 200); err != nil {
		return err
	}
	if err := validation.String("auth_role", a.AuthRole, 0, 100); err != nil {
		return err
	}
	if err := validation.String("schema_ref", a.SchemaRef, 0, 500); err != nil {
		return err
	}
	switch a.Status {
	case APIStatusApproved, APIStatusProposed, APIStatusRejected:
	default:
		return invalidEnum("status", a.Status)
	}
	switch a.Source {
	case APISourceManual, APISourceImport, APISourceOpenAPI, APISourceCode, APISourceAI:
	default:
		return invalidEnum("source", a.Source)
	}
	return nil
}
