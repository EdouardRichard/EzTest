package versionapp

// The handoff resources block assembly (v0.2.4-H H-B1).
//
// The block describes the resource scope of the environment a version binds.
// That scope belongs to the RESOURCE context, so it is read through
// resourceapp.HandoffResourceReader — the port that applies the single
// effective-scope authority (SUTView.IsApproved / EffectiveAssets and the
// environment code pool). The version adapter contributes only what the version
// context owns: the bound environment_id and the enabled version_account
// associations (HandoffResourceScope).
//
// Before this, the version PostgreSQL adapter assembled the whole block with its
// own queries over eight resource-owned tables, re-stating the effective-scope
// rule as SQL status predicates. That was both a cross-context boundary break
// (architecture §3.2 / AGENTS §2.1) and a second implementation of a rule whose
// single authority is the resource projection layer.
//
// The mapping below is a pure projection onto the PERSISTED handoff document
// (clarification_handoff.content): the json-tagged Handoff* types of handoff.go
// stay the version context's own contract, unchanged by this refactor. Bounding
// (block caps, the shared asset budget and the Truncated flag) stays in
// normalizeHandoffResources — one place, applied after the read.

import (
	"context"

	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
)

// HandoffResourceScope is the version-owned input to the resources block: the
// environment the version binds and the accounts it enabled. It carries no
// resource catalog data — that is read through the resource port.
type HandoffResourceScope struct {
	EnvironmentID int64
	// AccountIDs are the enabled version_account associations, in account order.
	// The association is version-owned, so the resource context cannot resolve it
	// from an environment id.
	AccountIDs []int64
}

// handoffResources reads the resources block: the version-owned scope from the
// handoff read port, the resource-owned projection from the resource port.
func (s *HandoffService) handoffResources(ctx context.Context, versionID int64) (HandoffResources, error) {
	scope, err := s.reader.ResourceScope(ctx, versionID)
	if err != nil {
		return HandoffResources{}, err
	}
	bundle, err := s.resources.HandoffResources(ctx, scope.EnvironmentID)
	if err != nil {
		return HandoffResources{}, err
	}
	accounts, err := s.resources.HandoffAccounts(ctx, scope.AccountIDs)
	if err != nil {
		return HandoffResources{}, err
	}
	bundle.Accounts = accounts
	return handoffResourcesFromBundle(bundle), nil
}

// handoffResourcesFromBundle projects the resource read model onto the persisted
// handoff document. Every repository row is SUT-inherited (the version repository
// overrides were removed with the version_sut chain), so Source is the single
// HandoffRepositorySourceSUT constant.
func handoffResourcesFromBundle(bundle resourceapp.HandoffResourceBundle) HandoffResources {
	resources := HandoffResources{
		Environment: HandoffEnvironment{
			ID:     bundle.Environment.ID,
			Name:   bundle.Environment.Name,
			Status: bundle.Environment.Status,
		},
		SUTs:         make([]HandoffSUT, 0, len(bundle.SUTs)),
		Accounts:     make([]HandoffAccount, 0, len(bundle.Accounts)),
		Repositories: make([]HandoffRepository, 0, len(bundle.Repositories)),
	}
	for _, sut := range bundle.SUTs {
		projected := HandoffSUT{
			SUTID:     sut.ID,
			Name:      sut.Name,
			BaseURL:   sut.BaseURL,
			Pages:     make([]HandoffPage, 0, len(sut.Pages)),
			APIs:      make([]HandoffAPI, 0, len(sut.APIs)),
			Databases: make([]HandoffDatabase, 0, len(sut.Databases)),
		}
		for _, page := range sut.Pages {
			projected.Pages = append(projected.Pages, HandoffPage{
				ID: page.ID, Name: page.Name, URL: page.URL, RouteTemplate: page.RouteTemplate,
			})
		}
		for _, api := range sut.APIs {
			projected.APIs = append(projected.APIs, HandoffAPI{
				ID: api.ID, Name: api.Name, Method: api.Method, Path: api.Path,
			})
		}
		for _, database := range sut.Databases {
			projected.Databases = append(projected.Databases, HandoffDatabase{
				ID: database.ID, SUTID: database.SUTID, Name: database.Name,
				Kind: database.Kind, ReadOnly: database.ReadOnly,
			})
		}
		resources.SUTs = append(resources.SUTs, projected)
	}
	for _, account := range bundle.Accounts {
		resources.Accounts = append(resources.Accounts, HandoffAccount{
			ID: account.ID, Username: account.Username, Roles: account.Roles,
		})
	}
	for _, repository := range bundle.Repositories {
		resources.Repositories = append(resources.Repositories, HandoffRepository{
			ID: repository.ID, Name: repository.Name, Role: repository.Role,
			Branch: repository.Branch, Source: HandoffRepositorySourceSUT,
		})
	}
	return resources
}
