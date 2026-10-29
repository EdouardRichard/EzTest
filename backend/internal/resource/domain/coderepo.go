package domain

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Code repository validation bounds (spec §6.7).
const (
	maxBranchLength = 255
	maxGlobLength   = 256
	maxGlobCount    = 200
)

// branchRE is the documented branch format (spec §6.7).
var branchRE = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,255}$`)

// CodeRepository is a local Git checkout used by later phases for indexing.
// P2 only validates and stores it; LastIndexedAt stays nil ("not indexed").
type CodeRepository struct {
	ID                  int64
	Key                 string
	Name                string
	LocalPath           string
	FixedBranch         string
	IncludeGlobs        []string
	ExcludeGlobs        []string
	Status              string
	LastCommit          string
	LastValidatedAt     *time.Time
	LastValidationError string
	LastIndexedAt       *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// GitValidation is the successful result of a read-only Git check.
type GitValidation struct {
	Commit string
}

// CodeRepoFilter selects a page of code repositories ordered by ID ascending.
type CodeRepoFilter struct {
	Status string
	Page   Page
}

// ValidateBranch enforces the branch format of spec §6.7: at most 255
// characters of [A-Za-z0-9._/-], never starting with a dash so a branch cannot
// be mistaken for a git option.
func ValidateBranch(branch string) error {
	if branch == "" || strings.HasPrefix(branch, "-") || !branchRE.MatchString(branch) {
		return NewInvalid("fixedBranch", "invalid_branch",
			"branch must be 1-255 characters of letters, digits, '.', '_', '/' or '-' and must not start with '-'")
	}
	return nil
}

// ValidateGlobs enforces the include/exclude glob bounds of spec §6.7. P2 only
// stores globs; it never matches them.
func ValidateGlobs(globs []string) error {
	if len(globs) > maxGlobCount {
		return NewInvalid("globs", "invalid_glob", fmt.Sprintf("at most %d globs are allowed", maxGlobCount))
	}
	for i, glob := range globs {
		if strings.TrimSpace(glob) == "" {
			return NewInvalid("globs", "invalid_glob", fmt.Sprintf("globs[%d] must not be empty", i))
		}
		if len(glob) > maxGlobLength {
			return NewInvalid("globs", "invalid_glob",
				fmt.Sprintf("globs[%d] must be at most %d bytes", i, maxGlobLength))
		}
	}
	return nil
}
