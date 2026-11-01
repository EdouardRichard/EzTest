package version_test

import (
	"errors"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

const validCodeBaselineCommit = "0123456789abcdef0123456789abcdef01234567"

func baselineBinding(commit string) version.Binding {
	return version.Binding{
		SUTID:              1001,
		EnvironmentID:      1101,
		CodeBaselineCommit: commit,
	}
}

func TestBindingValidateAcceptsEmptyOrFullCommit(t *testing.T) {
	for _, commit := range []string{"", validCodeBaselineCommit} {
		if err := baselineBinding(commit).Validate(); err != nil {
			t.Fatalf("Validate(%q) = %v, want nil", commit, err)
		}
	}
}

func TestBindingValidateRejectsMalformedCommit(t *testing.T) {
	cases := map[string]string{
		"short":     "0123456789abcdef",
		"long":      validCodeBaselineCommit + "0",
		"uppercase": "0123456789ABCDEF0123456789ABCDEF01234567",
		"non-hex":   "0123456789abcdef0123456789abcdef0123456g",
	}
	for name, commit := range cases {
		t.Run(name, func(t *testing.T) {
			err := baselineBinding(commit).Validate()
			if err == nil {
				t.Fatalf("Validate(%q) accepted a malformed commit", commit)
			}
			var apiErr *platform.Error
			if !errors.As(err, &apiErr) {
				t.Fatalf("err = %v, want a platform error", err)
			}
			if field, _ := apiErr.Details["field"].(string); field != "code_baseline_commit" {
				t.Fatalf("field = %q, want code_baseline_commit", field)
			}
		})
	}
}
