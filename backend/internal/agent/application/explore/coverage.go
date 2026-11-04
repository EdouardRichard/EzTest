package explore

// This file owns spec §11 capability #5: coverage analysis. It builds the
// requirement / page / API / role coverage matrix of one version from the
// current cases and the discovered page/API/login facts.
//
// The matrix is a read model: it never mutates a requirement, a case or a fact.
// A case covers a page/API when the caller-supplied knowledge key (route
// template / `METHOD /path`) matches the discovered fact key, which keeps the
// agent layer free of resource-ID joins.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// Coverage bounds. The configured value may only lower the corresponding
// default so one large version cannot exhaust the read model.
const (
	DefaultMaxCoverageRequirements = 200
	DefaultMaxCoverageCases        = 1000
	DefaultMaxCoveragePages        = 500
	DefaultMaxCoverageAPIs         = 500
)

// Case snapshot statuses. Only draft and confirmed cases count as coverage; a
// stale/superseded case is historical and must never hide an uncovered target.
const (
	coverageCaseDraft     = "draft"
	coverageCaseConfirmed = "confirmed"
)

// CaseSnapshot is the deterministic case input of the coverage matrix. PageKeys
// and APIKeys hold the knowledge keys of the case targets (route template and
// `METHOD /path`). AccountRole is the primary role the case drives, when known.
type CaseSnapshot struct {
	CaseID        int64
	CaseVersionID int64
	RequirementID int64
	Title         string
	Status        string
	PageKeys      []string
	APIKeys       []string
	AccountRole   string
}

// CaseSource reads the current cases of one version.
type CaseSource interface {
	Cases(ctx context.Context, versionID int64) ([]CaseSnapshot, error)
}

// RoleSource reads the account roles bound to one version SUT. It is optional;
// roles that only appear in cases/login facts are still reported.
type RoleSource interface {
	Roles(ctx context.Context, versionID int64) ([]string, error)
}

// CoverageConfig bounds one matrix build.
type CoverageConfig struct {
	MaxRequirements int
	MaxCases        int
	MaxPages        int
	MaxAPIs         int
}

// CoverageDeps are the narrow read ports of the matrix.
type CoverageDeps struct {
	Requirements RequirementSource
	Cases        CaseSource
	Facts        FactReader
	Roles        RoleSource
}

// RequirementCoverage is one row of the requirement matrix.
type RequirementCoverage struct {
	RequirementID        int64
	RequirementVersionID int64
	Title                string
	Status               string
	Covered              bool
	CaseIDs              []int64
	MissingTargets       []string
}

// PageCoverage is one row of the page matrix.
type PageCoverage struct {
	PageKey       string
	RouteTemplate string
	URL           string
	FactID        int64
	Covered       bool
	CaseIDs       []int64
}

// APICoverage is one row of the API matrix. State carries the three-state
// discovery classification when the fact has one.
type APICoverage struct {
	APIKey       string
	Method       string
	PathTemplate string
	State        string
	FactID       int64
	Covered      bool
	CaseIDs      []int64
}

// RoleCoverage is one row of the role matrix. LoginVerified reports a
// successful `login_session` fact for the role; Covered additionally requires
// at least one case that drives the role.
type RoleCoverage struct {
	Role          string
	Covered       bool
	LoginVerified bool
	CaseCount     int
}

// CoverageTotals is the deterministic aggregate of the matrix.
type CoverageTotals struct {
	Requirements          int
	RequirementsCovered   int
	RequirementsUncovered int
	Cases                 int
	CasesCovering         int
	Pages                 int
	PagesCovered          int
	APIs                  int
	APIsCovered           int
	Roles                 int
	RolesCovered          int
}

// CoverageMatrix is the read-only coverage model of one version.
type CoverageMatrix struct {
	VersionID    int64
	Requirements []RequirementCoverage
	Pages        []PageCoverage
	APIs         []APICoverage
	Roles        []RoleCoverage
	Totals       CoverageTotals
	Truncated    bool
}

// Coverage builds the version coverage matrix.
type Coverage struct {
	cfg  CoverageConfig
	deps CoverageDeps
}

// NewCoverage wires one coverage analysis.
func NewCoverage(cfg CoverageConfig, deps CoverageDeps) *Coverage {
	return &Coverage{cfg: cfg, deps: deps}
}

// Build assembles the requirement/page/API/role matrix. Missing optional ports
// degrade that dimension instead of failing the whole matrix; a missing required
// port is a configuration error.
func (c *Coverage) Build(ctx context.Context, versionID int64) (CoverageMatrix, error) {
	matrix := CoverageMatrix{VersionID: versionID}
	if c == nil || c.deps.Requirements == nil {
		return matrix, errors.New("coverage analysis has no requirement source")
	}
	if versionID <= 0 {
		return matrix, errors.New("coverage analysis requires a positive version id")
	}
	if err := ctx.Err(); err != nil {
		return matrix, err
	}

	requirements, err := c.deps.Requirements.Requirements(ctx, versionID)
	if err != nil {
		return matrix, fmt.Errorf("read coverage requirements: %w", err)
	}
	requirements = sortedRequirements(requirements)
	maxRequirements := boundedConfig(c.cfg.MaxRequirements, DefaultMaxCoverageRequirements)
	if len(requirements) > maxRequirements {
		requirements = requirements[:maxRequirements]
		matrix.Truncated = true
	}

	cases, casesTruncated, err := c.readCases(ctx, versionID)
	if err != nil {
		return matrix, err
	}
	if casesTruncated {
		matrix.Truncated = true
	}
	pages, apis, loginFacts, err := c.readFacts(ctx, versionID)
	if err != nil {
		return matrix, err
	}
	sortFactsBy(pages, firstFactStringKey("page_key", "route_template"))
	sortFactsBy(apis, firstFactStringKey("api_key"))
	maxPages := boundedConfig(c.cfg.MaxPages, DefaultMaxCoveragePages)
	if len(pages) > maxPages {
		pages = pages[:maxPages]
		matrix.Truncated = true
	}
	maxAPIs := boundedConfig(c.cfg.MaxAPIs, DefaultMaxCoverageAPIs)
	if len(apis) > maxAPIs {
		apis = apis[:maxAPIs]
		matrix.Truncated = true
	}
	roles, err := c.readRoles(ctx, versionID)
	if err != nil {
		return matrix, err
	}

	caseIDsByRequirement := make(map[int64][]int64)
	caseIDsByPage := make(map[string][]int64)
	caseIDsByAPI := make(map[string][]int64)
	targetsByPage := make(map[string]bool)
	targetsByAPI := make(map[string]bool)
	rolesByCase := make(map[string]int)
	coveringCases := 0
	for _, item := range cases {
		if !coverageStatusCounts(item.Status) {
			continue
		}
		coveringCases++
		if item.RequirementID > 0 {
			caseIDsByRequirement[item.RequirementID] = append(caseIDsByRequirement[item.RequirementID], item.CaseID)
		}
		for _, key := range normalizeKeys(item.PageKeys) {
			caseIDsByPage[key] = append(caseIDsByPage[key], item.CaseID)
			targetsByPage[key] = true
		}
		for _, key := range normalizeKeys(item.APIKeys) {
			caseIDsByAPI[key] = append(caseIDsByAPI[key], item.CaseID)
			targetsByAPI[key] = true
		}
		if role := strings.TrimSpace(item.AccountRole); role != "" {
			rolesByCase[role]++
			roles[role] = true
		}
	}
	matrix.Totals.Cases = len(cases)
	matrix.Totals.CasesCovering = coveringCases

	for _, requirement := range requirements {
		caseIDs := sortedIDs(caseIDsByRequirement[requirement.ID])
		row := RequirementCoverage{
			RequirementID:        requirement.ID,
			RequirementVersionID: requirement.VersionID,
			Title:                requirement.Title,
			Status:               requirement.Status,
			Covered:              len(caseIDs) > 0,
			CaseIDs:              caseIDs,
			MissingTargets:       missingRequirementTargets(requirement, targetsByPage, targetsByAPI),
		}
		matrix.Requirements = append(matrix.Requirements, row)
		matrix.Totals.Requirements++
		if row.Covered {
			matrix.Totals.RequirementsCovered++
		} else {
			matrix.Totals.RequirementsUncovered++
		}
	}

	// A fact conflict (same key, different content hash) leaves two current rows;
	// the matrix keeps one row per knowledge key.
	seenPages := make(map[string]bool, len(pages))
	for _, fact := range pages {
		row := pageCoverageRow(fact, caseIDsByPage)
		identity := firstNonEmptyString(row.PageKey, row.RouteTemplate)
		if identity == "" {
			identity = fmt.Sprintf("fact:%d", row.FactID)
		}
		if seenPages[identity] {
			continue
		}
		seenPages[identity] = true
		matrix.Pages = append(matrix.Pages, row)
		matrix.Totals.Pages++
		if row.Covered {
			matrix.Totals.PagesCovered++
		}
	}

	seenAPIs := make(map[string]bool, len(apis))
	for _, fact := range apis {
		row := apiCoverageRow(fact, caseIDsByAPI)
		identity := row.APIKey
		if identity == "" {
			identity = fmt.Sprintf("fact:%d", row.FactID)
		}
		if seenAPIs[identity] {
			continue
		}
		seenAPIs[identity] = true
		matrix.APIs = append(matrix.APIs, row)
		matrix.Totals.APIs++
		if row.Covered {
			matrix.Totals.APIsCovered++
		}
	}

	loginVerified := loginRoles(loginFacts)
	for _, role := range sortedRoles(roles) {
		row := RoleCoverage{
			Role:          role,
			LoginVerified: loginVerified[role],
			CaseCount:     rolesByCase[role],
		}
		row.Covered = row.LoginVerified || row.CaseCount > 0
		matrix.Roles = append(matrix.Roles, row)
		matrix.Totals.Roles++
		if row.Covered {
			matrix.Totals.RolesCovered++
		}
	}
	return matrix, nil
}

/* -------------------------------- reading -------------------------------- */

func (c *Coverage) readCases(ctx context.Context, versionID int64) ([]CaseSnapshot, bool, error) {
	cases := []CaseSnapshot{}
	if c.deps.Cases != nil {
		read, err := c.deps.Cases.Cases(ctx, versionID)
		if err != nil {
			return nil, false, fmt.Errorf("read coverage cases: %w", err)
		}
		cases = read
	}
	sort.Slice(cases, func(i, j int) bool {
		if cases[i].CaseID != cases[j].CaseID {
			return cases[i].CaseID < cases[j].CaseID
		}
		return cases[i].CaseVersionID < cases[j].CaseVersionID
	})
	maxCases := boundedConfig(c.cfg.MaxCases, DefaultMaxCoverageCases)
	truncated := false
	if len(cases) > maxCases {
		cases = cases[:maxCases]
		truncated = true
	}
	return cases, truncated, nil
}

func (c *Coverage) readFacts(ctx context.Context, versionID int64) ([]version.Fact, []version.Fact, []version.Fact, error) {
	if c.deps.Facts == nil {
		return nil, nil, nil, nil
	}
	pages, err := c.deps.Facts.Current(ctx, versionID, KindPage)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read coverage page facts: %w", err)
	}
	apis, err := c.deps.Facts.Current(ctx, versionID, KindAPI)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read coverage api facts: %w", err)
	}
	logins, err := c.deps.Facts.Current(ctx, versionID, KindLoginSession)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read coverage login facts: %w", err)
	}
	return pages, apis, logins, nil
}

func (c *Coverage) readRoles(ctx context.Context, versionID int64) (map[string]bool, error) {
	roles := map[string]bool{}
	if c.deps.Roles == nil {
		return roles, nil
	}
	values, err := c.deps.Roles.Roles(ctx, versionID)
	if err != nil {
		return nil, fmt.Errorf("read coverage roles: %w", err)
	}
	for _, role := range values {
		if trimmed := strings.TrimSpace(role); trimmed != "" {
			roles[trimmed] = true
		}
	}
	return roles, nil
}

/* -------------------------------- rows ----------------------------------- */

func pageCoverageRow(fact version.Fact, caseIDsByPage map[string][]int64) PageCoverage {
	pageKey := factString(fact, "page_key")
	routeTemplate := factString(fact, "route_template")
	row := PageCoverage{
		PageKey:       pageKey,
		RouteTemplate: routeTemplate,
		URL:           factString(fact, "url"),
		FactID:        fact.ID,
	}
	caseIDs := append([]int64(nil), caseIDsByPage[pageKey]...)
	caseIDs = append(caseIDs, caseIDsByPage[routeTemplate]...)
	row.CaseIDs = sortedIDs(caseIDs)
	row.Covered = len(row.CaseIDs) > 0
	return row
}

func apiCoverageRow(fact version.Fact, caseIDsByAPI map[string][]int64) APICoverage {
	apiKey := factString(fact, "api_key")
	row := APICoverage{
		APIKey:       apiKey,
		Method:       factString(fact, "method"),
		PathTemplate: factString(fact, "path_template"),
		State:        factString(fact, "state"),
		FactID:       fact.ID,
		CaseIDs:      sortedIDs(caseIDsByAPI[apiKey]),
	}
	row.Covered = len(row.CaseIDs) > 0
	return row
}

func missingRequirementTargets(requirement RequirementSnapshot, pages, apis map[string]bool) []string {
	missing := make([]string, 0)
	for _, target := range normalizeKeys(requirement.TargetPages) {
		if !pages[target] {
			missing = append(missing, target)
		}
	}
	for _, target := range normalizeKeys(requirement.TargetAPIs) {
		if !apis[target] {
			missing = append(missing, target)
		}
	}
	sort.Strings(missing)
	return missing
}

func loginRoles(facts []version.Fact) map[string]bool {
	roles := make(map[string]bool, len(facts))
	for _, fact := range facts {
		if role := factString(fact, "account_role"); role != "" {
			roles[role] = true
		}
	}
	return roles
}

/* -------------------------------- helpers -------------------------------- */

func coverageStatusCounts(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case coverageCaseDraft, coverageCaseConfirmed:
		return true
	default:
		return false
	}
}

func sortedIDs(ids []int64) []int64 {
	seen := make(map[int64]bool, len(ids))
	unique := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		unique = append(unique, id)
	}
	sort.Slice(unique, func(i, j int) bool { return unique[i] < unique[j] })
	return unique
}

func sortedRoles(roles map[string]bool) []string {
	values := make([]string, 0, len(roles))
	for role := range roles {
		values = append(values, role)
	}
	sort.Strings(values)
	return values
}

func factString(fact version.Fact, key string) string {
	value, ok := fact.Payload[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

// firstFactStringKey returns a key extractor preferring the first non-empty
// payload key, used to order page/API facts deterministically.
func firstFactStringKey(keys ...string) func(version.Fact) string {
	return func(fact version.Fact) string {
		for _, key := range keys {
			if value := factString(fact, key); value != "" {
				return value
			}
		}
		return ""
	}
}

// sortFactsBy orders facts by their knowledge key (ID as the tie breaker), so
// the matrix order never depends on the fact read order.
func sortFactsBy(facts []version.Fact, key func(version.Fact) string) {
	sort.SliceStable(facts, func(i, j int) bool {
		left, right := key(facts[i]), key(facts[j])
		if left != right {
			return left < right
		}
		return facts[i].ID < facts[j].ID
	})
}
