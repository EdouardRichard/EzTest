package versionadapter

// P9 explore composition wiring. The login flow needs three collaborators that
// the version adapter package already bridges to the agent runtime: the version
// binding read model (resolve the account bound to a version SUT + role), the
// version fact store (login_session facts) and the resource secret store (the
// password is read only inside the login flow). The executor and session cache
// are injected as narrow explore ports. Keeping the factory here means the
// composition root constructs one process-wide wiring and every explore caller
// shares the same login/cache-miss policy.

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	exploreapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application/explore"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// ExploreWiringDeps are the process-wide collaborators of the P9 explore
// composition. Every field is required: the wiring fails closed instead of
// producing a login flow that panics or silently skips fact recording.
type ExploreWiringDeps struct {
	// Executor is the HTTP explore-action client; it must also implement
	// explore.PageCloser so the throwaway reuse-verification page is closed by
	// page id (never by closing every page of the shared executor runtime).
	Executor exploreapp.Executor
	// Accounts is the real resource account-secret store (passwords are read
	// only by LoginFlow and never enter a fact or knowledge payload).
	Accounts exploreapp.AccountStore
	// Sessions is the bounded, version-scoped process cache of captured states.
	Sessions exploreapp.SessionStore
	// Facts records login_session version facts.
	Facts exploreapp.FactRecorder
	// Versions resolves version bindings to the account bound for a SUT role.
	Versions VersionService
	// Now is optional; it defaults to time.Now.
	Now func() time.Time
}

// ExploreWiring resolves accounts and constructs LoginFlow instances over the
// shared executor/session/fact collaborators. It is safe for concurrent use;
// each LoginFlow remains a single-flow stateful object.
type ExploreWiring struct {
	exec     exploreapp.Executor
	accounts exploreapp.AccountStore
	sessions exploreapp.SessionStore
	facts    exploreapp.FactRecorder
	versions VersionService
	now      func() time.Time
}

// NewExploreWiring validates and wires the P9 explore composition. A missing
// collaborator is a configuration error (never a nil dereference at runtime).
func NewExploreWiring(deps ExploreWiringDeps) (*ExploreWiring, error) {
	switch {
	case deps.Executor == nil:
		return nil, errors.New("versionadapter: explore wiring requires an executor")
	case deps.Accounts == nil:
		return nil, errors.New("versionadapter: explore wiring requires an account store")
	case deps.Sessions == nil:
		return nil, errors.New("versionadapter: explore wiring requires a session store")
	case deps.Facts == nil:
		return nil, errors.New("versionadapter: explore wiring requires a fact recorder")
	case deps.Versions == nil:
		return nil, errors.New("versionadapter: explore wiring requires a version binding reader")
	}
	if _, ok := deps.Executor.(exploreapp.PageCloser); !ok {
		return nil, errors.New("versionadapter: explore wiring executor must implement per-page cleanup (ClosePage)")
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	return &ExploreWiring{
		exec:     deps.Executor,
		accounts: deps.Accounts,
		sessions: deps.Sessions,
		facts:    deps.Facts,
		versions: deps.Versions,
		now:      now,
	}, nil
}

// LoginFlow builds one stateful flow over the shared collaborators. The caller
// resolves the account separately (or uses Login, which does both).
func (w *ExploreWiring) LoginFlow(cfg exploreapp.LoginFlowConfig) *exploreapp.LoginFlow {
	return exploreapp.NewLoginFlow(cfg, exploreapp.LoginFlowDeps{
		Executor: w.exec,
		Accounts: w.accounts,
		Sessions: w.sessions,
		Facts:    w.facts,
		Now:      w.now,
	})
}

// ResolveAccount selects the test account bound to a version SUT for one role.
// The password is not read here: LoginFlow resolves it through AccountStore only
// when it actually has to fill the form.
func (w *ExploreWiring) ResolveAccount(ctx context.Context, versionID, versionSUTID int64, role string) (exploreapp.Account, error) {
	if w == nil || w.versions == nil {
		return exploreapp.Account{}, errors.New("versionadapter: explore wiring has no version binding reader")
	}
	if versionID <= 0 {
		return exploreapp.Account{}, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if versionSUTID <= 0 {
		return exploreapp.Account{}, platform.Invalid("version_sut_id", "must be a positive decimal ID")
	}
	role = strings.TrimSpace(role)
	if role == "" {
		return exploreapp.Account{}, platform.Invalid("role", "must not be empty")
	}
	view, err := w.versions.Get(ctx, versionID)
	if err != nil {
		return exploreapp.Account{}, err
	}
	// Dedupe by account: one account may be listed with the same role more than
	// once (duplicate role entries or a repeated account row) without becoming a
	// state conflict. Only two *different* accounts sharing the role conflict.
	matched := make(map[string]struct{})
	for _, binding := range view.Bindings {
		if binding.ID != versionSUTID {
			continue
		}
		for _, account := range binding.Accounts {
			accountID := strings.TrimSpace(account.ID)
			for _, accountRole := range account.Roles {
				if strings.TrimSpace(accountRole) != role {
					continue
				}
				matched[accountID] = struct{}{}
				break
			}
		}
	}
	switch len(matched) {
	case 0:
		return exploreapp.Account{}, platform.NotFound("test_account")
	case 1:
	default:
		return exploreapp.Account{}, platform.StateConflict("multiple test accounts share the requested role")
	}
	accountID := ""
	for id := range matched {
		accountID = id
	}
	id, err := strconv.ParseInt(strings.TrimSpace(accountID), 10, 64)
	if err != nil || id <= 0 {
		return exploreapp.Account{}, platform.NewError(500, "internal", "test account has an invalid ID")
	}
	return exploreapp.Account{ID: id, Role: role}, nil
}

// Login resolves the bound account and runs the autonomous login/reuse flow.
func (w *ExploreWiring) Login(ctx context.Context, cfg exploreapp.LoginFlowConfig) (exploreapp.SessionFact, error) {
	flow, account, err := w.flow(ctx, cfg)
	if err != nil {
		return exploreapp.SessionFact{}, err
	}
	return flow.Execute(ctx, account)
}

// OpenWithSession resolves the bound account and opens url with the cached
// session. The direct-ref cache-miss fallback (unknown storage_state_ref ->
// fresh login) lives in LoginFlow.OpenWithSession so every caller shares it.
func (w *ExploreWiring) OpenWithSession(ctx context.Context, cfg exploreapp.LoginFlowConfig, url string) (exploreapp.Page, exploreapp.SessionFact, error) {
	flow, account, err := w.flow(ctx, cfg)
	if err != nil {
		return exploreapp.Page{}, exploreapp.SessionFact{}, err
	}
	return flow.OpenWithSession(ctx, account, url)
}

func (w *ExploreWiring) flow(ctx context.Context, cfg exploreapp.LoginFlowConfig) (*exploreapp.LoginFlow, exploreapp.Account, error) {
	if w == nil {
		return nil, exploreapp.Account{}, errors.New("versionadapter: explore wiring is not configured")
	}
	account, err := w.ResolveAccount(ctx, cfg.VersionID, cfg.VersionSUTID, cfg.Role)
	if err != nil {
		return nil, exploreapp.Account{}, err
	}
	return w.LoginFlow(cfg), account, nil
}
