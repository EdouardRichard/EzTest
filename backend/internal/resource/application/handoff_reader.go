package resourceapp

// HandoffResourceReader is the resource context's read port for the version
// context's clarification -> design handoff pack (v0.2.4-H H-B1).
//
// WHY THIS PORT EXISTS: the handoff resources block used to be assembled by the
// version PostgreSQL adapter with its own queries over sut / sut_page / sut_api /
// target_database / test_account / sut_repository / code_repository /
// environment. That broke the cross-context boundary (architecture §3.2 / AGENTS
// §2.1: a context reaches another context through its application ports, never by
// reading its tables) AND put the effective-scope RULE in a second place: the SQL
// status predicates re-stated SUTView.IsApproved / EffectiveAssets, so the rule
// had one authority but two implementations that had to be kept in lockstep by
// hand.
//
// The port returns the resource-owned half of that block, resolved through the
// ONE authority (SUTView.IsApproved / SUTView.EffectiveAssets and the environment
// code pool of ListEnvironmentRepositories). The version application service maps
// it onto its own persisted handoff document; the version adapter reads only
// version-owned rows (the bound environment_id and the enabled version_account
// associations).
//
// The read models are deliberately narrow functional summaries: no password, no
// DSN, no account notes. That is scope correctness, not credential scrubbing
// (GD-01: credentials are plaintext by design; this port simply does not carry
// columns the design pack has no use for).

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// HandoffResourceReader is implemented by the resource application Service.
type HandoffResourceReader interface {
	// HandoffResources returns the environment summary, its APPROVED SUTs with
	// their EFFECTIVE assets (approved pages/APIs, active target databases) and
	// the environment code pool. environmentID <= 0 (a version with no binding)
	// and a missing environment row both yield an empty bundle rather than an
	// error: the handoff still describes the bound id it was asked about.
	HandoffResources(ctx context.Context, environmentID int64) (HandoffResourceBundle, error)
	// HandoffAccounts returns the ACTIVE test accounts of the given ids, in id
	// order. The ids come from the version context's own enabled version_account
	// associations — the association is version-owned, so it cannot be resolved
	// from an environment id. Missing ids are skipped (the association may point
	// at a deleted account); a store failure is returned.
	HandoffAccounts(ctx context.Context, accountIDs []int64) ([]HandoffAccount, error)
}

// HandoffResourceBundle is the resource-owned half of the handoff resources
// block. Accounts is filled by the version application service from
// HandoffAccounts, because only the version context knows which accounts it
// enabled.
type HandoffResourceBundle struct {
	Environment  HandoffEnvironment
	SUTs         []HandoffSUT
	Accounts     []HandoffAccount
	Repositories []HandoffRepo
}

// HandoffEnvironment is the bound environment's summary.
type HandoffEnvironment struct {
	ID     int64
	Name   string
	Status string
}

// HandoffSUT is one approved SUT with its effective asset scope.
type HandoffSUT struct {
	ID        int64
	Name      string
	BaseURL   string
	Pages     []HandoffPage
	APIs      []HandoffAPI
	Databases []HandoffDatabase
}

// HandoffPage is one approved page of a SUT.
type HandoffPage struct {
	ID            int64
	Name          string
	URL           string
	RouteTemplate string
}

// HandoffAPI is one approved API of a SUT.
type HandoffAPI struct {
	ID     int64
	Name   string
	Method string
	Path   string
}

// HandoffDatabase is one active target database of a SUT. SUTID is carried per
// row because a design db assertion takes sut_id, not the database id. Kind is
// the engine label; no DSN/username/password is ever carried.
type HandoffDatabase struct {
	ID       int64
	SUTID    int64
	Name     string
	Kind     string
	ReadOnly bool
}

// HandoffAccount is one active test account. No password or notes.
type HandoffAccount struct {
	ID       int64
	Username string
	Roles    []string
}

// HandoffRepo is one repository of the environment code pool, with the role of
// the association the pool resolved (lowest sut_id wins, see
// ListEnvironmentRepositories).
type HandoffRepo struct {
	ID     int64
	Name   string
	Branch string
	Role   string
}

var _ HandoffResourceReader = (*Service)(nil)

// HandoffResources implements HandoffResourceReader.
//
// The effective-scope decision is NOT re-implemented here: every SUT passes
// through SUTView.IsApproved and its assets through SUTView.EffectiveAssets, and
// the code pool comes from ListEnvironmentRepositories (which already gates on
// approved SUTs, enabled associations and active repositories, and de-duplicates
// first-wins by repo_id, sut_id, id). A proposed/rejected SUT therefore
// contributes nothing — not its pages, not its databases, not its repository
// associations (which would otherwise decide pool[0], the code baseline).
//
// Reads are bounded by the environment's own catalog, exactly like the version
// binding overview (Service.GetEnvironment). The handoff's own block caps and
// shared asset budget are applied by the version application service, which sets
// the document's Truncated flag; there is no second, silent SQL LIMIT here.
func (s *Service) HandoffResources(ctx context.Context, environmentID int64) (HandoffResourceBundle, error) {
	bundle := HandoffResourceBundle{
		Environment:  HandoffEnvironment{ID: environmentID},
		SUTs:         []HandoffSUT{},
		Accounts:     []HandoffAccount{},
		Repositories: []HandoffRepo{},
	}
	if environmentID <= 0 {
		return bundle, nil
	}
	graph, err := s.repo.GetEnvironmentGraph(ctx, environmentID)
	if err != nil {
		if errors.Is(err, resource.ErrNotFound) {
			// The binding points at an environment row that no longer exists.
			// The handoff keeps the bound id with an empty name/status and an
			// empty scope (the LEFT JOIN behaviour of the former adapter).
			return bundle, nil
		}
		return HandoffResourceBundle{}, mapRepoError(err, "environment", "")
	}
	bundle.Environment = HandoffEnvironment{
		ID:     graph.Environment.ID,
		Name:   graph.Environment.Name,
		Status: graph.Environment.Status,
	}
	// DatabaseView carries no engine label, so take Kind from the graph's own
	// rows (already loaded, no extra read).
	kinds := make(map[int64]string, len(graph.Databases))
	for _, database := range graph.Databases {
		kinds[database.ID] = database.Kind
	}
	for _, view := range environmentSUTViews(graph) {
		if !view.IsApproved() {
			continue
		}
		scope := view.EffectiveAssets()
		sutID, err := handoffCatalogID(view.ID)
		if err != nil {
			return HandoffResourceBundle{}, err
		}
		sut := HandoffSUT{
			ID:        sutID,
			Name:      view.Name,
			BaseURL:   view.BaseURL,
			Pages:     make([]HandoffPage, 0, len(scope.Pages)),
			APIs:      make([]HandoffAPI, 0, len(scope.APIs)),
			Databases: make([]HandoffDatabase, 0, len(scope.Databases)),
		}
		for _, page := range scope.Pages {
			id, err := handoffCatalogID(page.ID)
			if err != nil {
				return HandoffResourceBundle{}, err
			}
			sut.Pages = append(sut.Pages, HandoffPage{
				ID: id, Name: page.Name, URL: page.URL, RouteTemplate: page.RouteTemplate,
			})
		}
		for _, api := range scope.APIs {
			id, err := handoffCatalogID(api.ID)
			if err != nil {
				return HandoffResourceBundle{}, err
			}
			sut.APIs = append(sut.APIs, HandoffAPI{
				ID: id, Name: api.Name, Method: api.Method, Path: api.PathTemplate,
			})
		}
		for _, database := range scope.Databases {
			id, err := handoffCatalogID(database.ID)
			if err != nil {
				return HandoffResourceBundle{}, err
			}
			sut.Databases = append(sut.Databases, HandoffDatabase{
				ID: id, SUTID: sutID, Name: database.Name,
				Kind: kinds[id], ReadOnly: database.ReadOnly,
			})
		}
		bundle.SUTs = append(bundle.SUTs, sut)
	}
	pool, err := s.ListEnvironmentRepositories(ctx, environmentID)
	if err != nil {
		return HandoffResourceBundle{}, err
	}
	for _, association := range pool {
		id, err := handoffCatalogID(association.Repository.ID)
		if err != nil {
			return HandoffResourceBundle{}, err
		}
		bundle.Repositories = append(bundle.Repositories, HandoffRepo{
			ID: id, Name: association.Repository.Name,
			Branch: association.Repository.Branch, Role: association.Role,
		})
	}
	return bundle, nil
}

// HandoffAccounts implements HandoffResourceReader.
func (s *Service) HandoffAccounts(ctx context.Context, accountIDs []int64) ([]HandoffAccount, error) {
	accounts := make([]HandoffAccount, 0, len(accountIDs))
	seen := make(map[int64]struct{}, len(accountIDs))
	ordered := make([]int64, 0, len(accountIDs))
	for _, id := range accountIDs {
		if id <= 0 {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		ordered = append(ordered, id)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	for _, id := range ordered {
		account, err := s.repo.GetAccount(ctx, id)
		if err != nil {
			if errors.Is(err, resource.ErrNotFound) {
				continue
			}
			return nil, mapRepoError(err, "account", "")
		}
		if account.Status != resource.StatusActive {
			continue
		}
		accounts = append(accounts, HandoffAccount{
			ID: account.ID, Username: account.Username, Roles: account.Roles,
		})
	}
	return accounts, nil
}

// handoffCatalogID parses a catalog view's decimal id. The views are produced
// from int64 ids in this same package, so a failure is a programming error and is
// reported rather than silently turned into 0 (which would drop the row).
func handoffCatalogID(raw string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id <= 0 {
		// Typed field error (AGENTS §2.2), not a bare fmt.Errorf (T7 M6): a
		// malformed catalog id is a 400 invalid_argument the caller can classify.
		return 0, platform.Invalid("catalog_id", "handoff resource read: malformed catalog id "+strconv.Quote(raw))
	}
	return id, nil
}
