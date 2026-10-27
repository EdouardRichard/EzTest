package domain

import (
	"errors"
	"strings"
	"testing"
)

// ptr returns a pointer to v; it keeps table-driven fixtures readable.
func ptr[T any](v T) *T { return &v }

// assertDomainError asserts that err is a *Error with the given field and
// reason. field may be empty when the error is not tied to a request field.
func assertDomainError(t *testing.T, err error, field, reason string) {
	t.Helper()
	if err == nil {
		t.Fatal("want a domain error, got nil")
	}
	var domainErr *Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("want *domain.Error, got %T (%v)", err, err)
	}
	if domainErr.Field != field {
		t.Fatalf("field = %q, want %q (%v)", domainErr.Field, field, err)
	}
	if domainErr.Reason != reason {
		t.Fatalf("reason = %q, want %q (%v)", domainErr.Reason, reason, err)
	}
}

// validVersion is the smallest valid aggregate; each case changes one aspect.
func validVersion() Version {
	return Version{Name: "Order system v2", Status: VersionDraft}
}

func TestVersionValidate(t *testing.T) {
	commit := strings.Repeat("a", 40)

	tests := []struct {
		name   string
		build  func(*Version)
		valid  bool
		field  string
		reason string
	}{
		{name: "minimal valid version", valid: true},
		{
			name: "valid resources and repository snapshot",
			build: func(v *Version) {
				v.SystemID = ptr(int64(7))
				v.EnvironmentID = ptr(int64(8))
				v.CodeRepositoryID = ptr(int64(9))
				v.CodeBranch = ptr("main")
				v.CodeBaselineCommit = ptr(commit)
			},
			valid: true,
		},
		{
			name:   "name required",
			build:  func(v *Version) { v.Name = "" },
			field:  "name",
			reason: "invalid_name",
		},
		{
			name:  "name at limit",
			build: func(v *Version) { v.Name = strings.Repeat("n", MaxVersionNameChars) },
			valid: true,
		},
		{
			name:   "name too long",
			build:  func(v *Version) { v.Name = strings.Repeat("n", MaxVersionNameChars+1) },
			field:  "name",
			reason: "invalid_name",
		},
		{
			name:  "label at limit",
			build: func(v *Version) { v.Label = strings.Repeat("l", MaxVersionLabelChars) },
			valid: true,
		},
		{
			name:   "label too long",
			build:  func(v *Version) { v.Label = strings.Repeat("l", MaxVersionLabelChars+1) },
			field:  "label",
			reason: "invalid_label",
		},
		{
			name:  "description at limit",
			build: func(v *Version) { v.Description = strings.Repeat("d", MaxDescriptionChars) },
			valid: true,
		},
		{
			name:   "description too long",
			build:  func(v *Version) { v.Description = strings.Repeat("d", MaxDescriptionChars+1) },
			field:  "description",
			reason: "invalid_description",
		},
		{
			name:   "status must be a known enum value",
			build:  func(v *Version) { v.Status = "shipped" },
			field:  "status",
			reason: "invalid_status",
		},
		{
			name: "environment requires system",
			build: func(v *Version) {
				v.EnvironmentID = ptr(int64(8))
			},
			field:  "environmentId",
			reason: "environment_system_mismatch",
		},
		{
			name: "system without environment is allowed",
			build: func(v *Version) {
				v.SystemID = ptr(int64(7))
			},
			valid: true,
		},
		{
			name: "repository requires branch and commit snapshot",
			build: func(v *Version) {
				v.CodeRepositoryID = ptr(int64(9))
			},
			field:  "codeRepositoryId",
			reason: "invalid_bindings",
		},
		{
			name: "branch requires repository",
			build: func(v *Version) {
				v.CodeBranch = ptr("main")
			},
			field:  "codeRepositoryId",
			reason: "invalid_bindings",
		},
		{
			name: "commit requires repository",
			build: func(v *Version) {
				v.CodeBaselineCommit = ptr(commit)
			},
			field:  "codeRepositoryId",
			reason: "invalid_bindings",
		},
		{
			name: "repository requires branch",
			build: func(v *Version) {
				v.CodeRepositoryID = ptr(int64(9))
				v.CodeBaselineCommit = ptr(commit)
			},
			field:  "codeRepositoryId",
			reason: "invalid_bindings",
		},
		{
			name: "baseline commit must be 40 hex characters",
			build: func(v *Version) {
				v.CodeRepositoryID = ptr(int64(9))
				v.CodeBranch = ptr("main")
				v.CodeBaselineCommit = ptr(strings.Repeat("a", 39))
			},
			field:  "codeBaselineCommit",
			reason: "invalid_commit",
		},
		{
			name: "baseline commit must be lowercase hex",
			build: func(v *Version) {
				v.CodeRepositoryID = ptr(int64(9))
				v.CodeBranch = ptr("main")
				v.CodeBaselineCommit = ptr(strings.Repeat("A", 40))
			},
			field:  "codeBaselineCommit",
			reason: "invalid_commit",
		},
		{
			name: "branch at limit",
			build: func(v *Version) {
				v.CodeRepositoryID = ptr(int64(9))
				v.CodeBranch = ptr(strings.Repeat("b", MaxCodeBranchChars))
				v.CodeBaselineCommit = ptr(commit)
			},
			valid: true,
		},
		{
			name: "branch too long",
			build: func(v *Version) {
				v.CodeRepositoryID = ptr(int64(9))
				v.CodeBranch = ptr(strings.Repeat("b", MaxCodeBranchChars+1))
				v.CodeBaselineCommit = ptr(commit)
			},
			field:  "codeBranch",
			reason: "invalid_branch",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			version := validVersion()
			if tc.build != nil {
				tc.build(&version)
			}
			err := version.Validate()
			if tc.valid {
				if err != nil {
					t.Fatalf("want valid version, got %v", err)
				}
				return
			}
			assertDomainError(t, err, tc.field, tc.reason)
		})
	}
}
