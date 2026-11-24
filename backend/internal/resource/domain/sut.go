// Package resource is the resource-catalog domain: environments, the SUTs that
// belong to them, and the pages, APIs, target databases, test accounts and code
// repositories those SUTs own.
//
// v0.2.4-B inverted the hierarchy (SC-B1): an environment is a top-level entity
// that owns SUTs, a SUT owns its assets and carries the base_url that used to
// live on the environment.
package resource

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// Lifecycle statuses shared by environments, SUTs, target databases, test
// accounts and code repositories.
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
)

// SUT registration vocabulary (v0.2.4-B R-B4). This is the SUT's OWN source and
// status vocabulary, deliberately separate from the shared StatusActive /
// StatusDisabled lifecycle constants above: a SUT is registered by an admin, an
// AI agent or an import, and its registration is proposed, approved or rejected.
// Only an approved SUT is inside a version's effective scope (see
// resourceapp.SUTView.EffectiveAssets). The Agent-facing registration tool, the
// evidence gate and the parent-path heuristic that write source=ai/status=proposed
// belong to subproject F; subproject B reserves the schema and wires the admin
// path (source=admin, status=approved).
const (
	SUTSourceAdmin  = "admin"
	SUTSourceAI     = "ai"
	SUTSourceImport = "import"

	SUTStatusProposed = "proposed"
	SUTStatusApproved = "approved"
	SUTStatusRejected = "rejected"
)

// MaxFamilyRunes caps the optional SUT family slug.
const MaxFamilyRunes = 64

// SUT is one system under test inside an environment. Its name is unique per
// environment (uk_sut_name) and base_url is the entry point the tests drive.
type SUT struct {
	ID int64
	// EnvironmentID is the owning top-level environment. It is a real foreign
	// key inside the resource context (fk_sut_environment).
	EnvironmentID int64
	Name          string
	Intro         string
	BaseURL       string
	// Family is the optional SUT family (P7-D-01). Knowledge items with
	// scope=system are visible to versions bound to a SUT with the matching
	// family used as system_key. nil means the SUT has no family.
	Family *string
	// Source records who registered the SUT (admin/ai/import) and Status records
	// the registration outcome (proposed/approved/rejected) — the SUT's own R-B4
	// vocabulary, not the shared lifecycle statuses. The admin creation path
	// writes admin/approved; only approved SUTs enter a version's effective scope.
	Source    string
	Status    string
	CreatedBy int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Validate checks the editable SUT fields.
func (s SUT) Validate() error {
	if s.EnvironmentID <= 0 {
		return platform.Invalid("environment_id", "must be a positive decimal ID")
	}
	if err := validation.String("name", s.Name, 1, 100); err != nil {
		return err
	}
	if err := validation.String("intro", s.Intro, 0, 2000); err != nil {
		return err
	}
	if err := validation.URL("base_url", s.BaseURL); err != nil {
		return err
	}
	if _, err := NormalizeFamily(s.Family); err != nil {
		return err
	}
	switch s.Source {
	case SUTSourceAdmin, SUTSourceAI, SUTSourceImport:
	default:
		return invalidEnum("source", s.Source)
	}
	switch s.Status {
	case SUTStatusProposed, SUTStatusApproved, SUTStatusRejected:
	default:
		return invalidEnum("status", s.Status)
	}
	return nil
}

// SUTRepository associates one global code repository with a SUT (v0.22
// R-C2/DB-12). A SUT may associate two or more repositories; the code pool of an
// environment is the union of its SUTs' repositories (v0.2.4-B GD-03).
type SUTRepository struct {
	ID     int64
	SUTID  int64
	RepoID int64
	Role   string
	// CodeBaselineCommit is the resolved commit SHA this (SUT, repository) pair
	// is pinned at. Empty means "not resolved yet"; a present value is a
	// lowercase 40-hex Git SHA (chk_sut_repository_code_baseline_commit). It is
	// written by the version binding resolution and backfilled by the code index
	// after a successful run.
	CodeBaselineCommit string
	Enabled            bool
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// MaxRepositoryRoleRunes caps the optional repository role label.
const MaxRepositoryRoleRunes = 64

// Validate checks one SUT-repository association.
func (r SUTRepository) Validate() error {
	if r.SUTID <= 0 {
		return platform.Invalid("sut_id", "must be a positive decimal ID")
	}
	if r.RepoID <= 0 {
		return platform.Invalid("repo_id", "must be a positive decimal ID")
	}
	if utf8.RuneCountInString(r.Role) > MaxRepositoryRoleRunes {
		return platform.Invalid("role", "must be at most 64 characters")
	}
	return validation.GitCommitSHA("code_baseline_commit", r.CodeBaselineCommit)
}

// NormalizeFamily trims and validates the optional SUT family. nil means the
// SUT has no family. A present family must be lowercase, 1..64 runes and free
// of whitespace so it can be used as a knowledge system_key.
func NormalizeFamily(raw *string) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	value := strings.TrimSpace(*raw)
	if value == "" {
		return nil, platform.Invalid("family", "must not be empty")
	}
	if utf8.RuneCountInString(value) > MaxFamilyRunes {
		return nil, platform.Invalid("family", "must be at most 64 characters")
	}
	if value != strings.ToLower(value) {
		return nil, platform.Invalid("family", "must be lowercase")
	}
	if strings.ContainsFunc(value, unicode.IsSpace) {
		return nil, platform.Invalid("family", "must not contain whitespace")
	}
	return &value, nil
}
