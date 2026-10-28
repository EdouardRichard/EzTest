package domain

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Version statuses, mirrored from the version.status CHECK constraint.
const (
	VersionDraft      = "draft"
	VersionClarifying = "clarifying"
	VersionDesigning  = "designing"
	VersionReviewing  = "reviewing"
	VersionExecuting  = "executing"
	VersionContinuing = "continuing"
	VersionCompleted  = "completed"
	VersionArchived   = "archived"
)

// commitRE mirrors the code_baseline_commit CHECK constraint: exactly 40
// lowercase hexadecimal characters.
var commitRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Version is the coarse-grained container for a requirement pipeline. P3 only
// drives it through draft -> clarifying -> designing; requirements never change
// its status.
type Version struct {
	ID                 int64
	Name               string
	Label              string
	Description        string
	Status             string
	SystemID           *int64
	EnvironmentID      *int64
	CodeRepositoryID   *int64
	CodeBranch         *string
	CodeBaselineCommit *string
	Revision           int
	CreatedBy          int64
	UpdatedBy          int64
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// VersionFilter selects a page of versions ordered by updated_at DESC, id DESC.
type VersionFilter struct {
	Status string
	Page   Page
}

// VersionBindings is the optional resource binding of a version. It is replaced
// as a whole by PUT /versions/{id}/bindings.
type VersionBindings struct {
	SystemID           *int64
	EnvironmentID      *int64
	CodeRepositoryID   *int64
	CodeBranch         *string
	CodeBaselineCommit *string
}

// VersionResource is the minimal P2 resource projection rendered in a version
// detail response. Available is false when the referenced P2 resource is gone.
type VersionResource struct {
	ID        int64
	Name      string
	Available bool
}

// VersionBindingsView is the resolved binding view of a version.
type VersionBindingsView struct {
	System         *VersionResource
	Environment    *VersionResource
	CodeRepository *VersionResource
}

// VersionCounts are the aggregate counters of a version detail/list item.
type VersionCounts struct {
	RequirementCount  int
	ClarifiedCount    int
	OpenQuestionCount int
}

// Warnings is the soft gate for enter-design: unclarified requirement ids plus
// the number of version-level open questions.
type Warnings struct {
	UnclarifiedRequirementIDs []int64
	OpenQuestionCount         int
}

// Validate enforces the version bounds of spec §6.1/§7.2 and the binding
// CHECK constraints of spec §5.1.
func (v *Version) Validate() error {
	if v == nil {
		return NewInvalid("", "invalid_version", "version is required")
	}
	if strings.TrimSpace(v.Name) == "" {
		return NewInvalid("name", "invalid_name", "name is required")
	}
	if utf8.RuneCountInString(v.Name) > MaxVersionNameChars {
		return NewInvalid("name", "invalid_name",
			fmt.Sprintf("name must be at most %d characters", MaxVersionNameChars))
	}
	if utf8.RuneCountInString(v.Label) > MaxVersionLabelChars {
		return NewInvalid("label", "invalid_label",
			fmt.Sprintf("label must be at most %d characters", MaxVersionLabelChars))
	}
	if utf8.RuneCountInString(v.Description) > MaxDescriptionChars {
		return NewInvalid("description", "invalid_description",
			fmt.Sprintf("description must be at most %d characters", MaxDescriptionChars))
	}
	if !validVersionStatus(v.Status) {
		return NewInvalid("status", "invalid_status",
			"status must be one of draft, clarifying, designing, reviewing, executing, continuing, completed or archived")
	}
	return ValidateBindings(v.SystemID, v.EnvironmentID, v.CodeRepositoryID, v.CodeBranch, v.CodeBaselineCommit)
}

// ValidateBindings enforces the resource binding rules shared by version
// creation and PUT /versions/{id}/bindings: an environment requires a system,
// and the repository id, branch and baseline commit are a snapshot that must be
// set or cleared together.
func ValidateBindings(systemID, environmentID, codeRepositoryID *int64, branch, baselineCommit *string) error {
	if environmentID != nil && systemID == nil {
		return NewInvalid("environmentId", "environment_system_mismatch",
			"an environment requires a system binding")
	}
	hasRepository := codeRepositoryID != nil
	if hasRepository != (branch != nil) || hasRepository != (baselineCommit != nil) {
		return NewInvalid("codeRepositoryId", "invalid_bindings",
			"codeRepositoryId, codeBranch and codeBaselineCommit must be set or cleared together")
	}
	if branch != nil {
		if strings.TrimSpace(*branch) == "" {
			return NewInvalid("codeBranch", "invalid_branch", "codeBranch must not be empty")
		}
		if utf8.RuneCountInString(*branch) > MaxCodeBranchChars {
			return NewInvalid("codeBranch", "invalid_branch",
				fmt.Sprintf("codeBranch must be at most %d characters", MaxCodeBranchChars))
		}
	}
	if baselineCommit != nil && !commitRE.MatchString(*baselineCommit) {
		return NewInvalid("codeBaselineCommit", "invalid_commit",
			"codeBaselineCommit must be 40 lowercase hexadecimal characters")
	}
	return nil
}

// validVersionStatus reports whether status is one of the DDL's frozen values.
func validVersionStatus(status string) bool {
	switch status {
	case VersionDraft, VersionClarifying, VersionDesigning, VersionReviewing,
		VersionExecuting, VersionContinuing, VersionCompleted, VersionArchived:
		return true
	default:
		return false
	}
}
