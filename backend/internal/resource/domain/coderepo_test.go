package domain

import (
	"strings"
	"testing"
)

func TestValidateBranchRejectsLeadingDash(t *testing.T) {
	if err := ValidateBranch("-evil"); err == nil {
		t.Fatal("want error")
	}
}

func TestValidateBranchAcceptsFeatureBranches(t *testing.T) {
	for _, branch := range []string{"main", "master", "feat/p2-resources-identity", "release/1.2.3", "a_b.c"} {
		if err := ValidateBranch(branch); err != nil {
			t.Fatalf("branch=%q: %v", branch, err)
		}
	}
}

func TestValidateBranchRejectsInvalidBranches(t *testing.T) {
	for _, branch := range []string{"", "-evil", "feature branch", "release;rm", strings.Repeat("a", 256)} {
		if err := ValidateBranch(branch); err == nil {
			t.Fatalf("branch=%q: want error", branch)
		}
	}
}

func TestValidateGlobs(t *testing.T) {
	if err := ValidateGlobs(nil); err != nil {
		t.Fatalf("empty globs: %v", err)
	}
	if err := ValidateGlobs([]string{"src/**", "*.go"}); err != nil {
		t.Fatalf("valid globs: %v", err)
	}
	for _, globs := range [][]string{
		{""},
		{"src/**", ""},
		{strings.Repeat("a", 257)},
		make([]string, 201),
	} {
		if err := ValidateGlobs(globs); err == nil {
			t.Fatalf("globs=%v: want error", globs)
		}
	}
}
