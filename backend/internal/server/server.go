package server

import (
	"context"
	"errors"
	"net/http"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentapi "github.com/EdouardRichard/EzTest/backend/internal/agent/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	"github.com/EdouardRichard/EzTest/backend/internal/audit/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/config"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	designapi "github.com/EdouardRichard/EzTest/backend/internal/design/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/eventing/interfaces/httpapi"
	executionhttp "github.com/EdouardRichard/EzTest/backend/internal/execution/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/identity/application"
	"github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	"github.com/EdouardRichard/EzTest/backend/internal/llm/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/settings/application"
	"github.com/EdouardRichard/EzTest/backend/internal/settings/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/storage/application"
	"github.com/EdouardRichard/EzTest/backend/internal/storage/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/interfaces/httpapi"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Deps is the fully constructed application surface the HTTP layer exposes.
type Deps struct {
	Config        config.Config
	Pool          *pgxpool.Pool
	Version       string
	Settings      *settingsapp.Service
	Identity      *identityapp.Service
	Audit         *auditapp.Service
	Events        *eventingapp.Service
	Storage       *storageapp.Service
	LLM           *llmapp.Service
	Resource      *resourceapp.Service
	Versions      *versionapp.Service
	Requirements  *versionapp.RequirementService
	Conversations *versionapp.ConversationService
	TeamRoom      *agentapp.TeamRoom
	DesignGraph   *designapp.GraphService
	DesignExports *designapp.ExportService
	DesignStarter versionapp.DesignStarter
	Execution     *executionhttp.Handler
}

// New assembles the HTTP middleware chain and every P0 route.
func New(deps Deps) http.Handler {
	mux := http.NewServeMux()

	executorURL, _ := deps.Settings.String(context.Background(), "execution.executor_url")
	health := &Health{
		Version:        deps.Version,
		DB:             deps.Pool,
		SettingsLoaded: true,
		ExecutorURL:    executorURL,
	}
	health.Register(mux)

	authn := identityAuthenticator{svc: deps.Identity}
	authenticated := RequireAuth(authn)
	admin := RequireAdmin(authn)

	identityapi.New(deps.Identity).Register(mux, authenticated, admin)
	registerSettingsRoutes(mux, admin, settingsapi.New(deps.Settings))
	registerAuditRoutes(mux, admin, auditapi.New(deps.Audit))
	registerLLMRoutes(mux, admin, llmapi.New(deps.LLM))
	registerContentRoutes(mux, authenticated, storageapi.New(deps.Storage))
	registerEventRoutes(mux, authenticated, eventingapi.New(deps.Events))
	registerResourceRoutes(mux, authenticated, admin, resourceapi.New(deps.Resource))
	registerVersionRoutes(mux, authenticated, versionapi.New(deps.Versions))
	registerRequirementRoutes(mux, authenticated, versionapi.NewRequirementHandler(deps.Requirements))
	registerConversationRoutes(mux, authenticated, versionapi.NewConversationHandler(deps.Conversations, deps.DesignStarter))
	registerTeamRoomRoutes(mux, authenticated, agentapi.NewTeamRoom(deps.TeamRoom, versionExistence{deps.Versions}))
	registerDesignRoutes(mux, authenticated, designapi.New(deps.DesignGraph, deps.DesignExports))
	registerExecutionRoutes(mux, authenticated, admin, deps.Execution)

	return RequestID(Recover(Logging(CORS(deps.Config)(mux))))
}

type identityAuthenticator struct {
	svc *identityapp.Service
}

// versionExistence adapts the version application service to the agent team
// room handler's local Exists port; missing versions become 404, other errors
// propagate.
type versionExistence struct {
	svc *versionapp.Service
}

func (v versionExistence) Exists(ctx context.Context, id int64) (bool, error) {
	if v.svc == nil {
		return false, platform.NewError(http.StatusInternalServerError, "internal", "version service is not configured")
	}
	_, err := v.svc.Get(ctx, id)
	if err == nil {
		return true, nil
	}
	var apiErr *platform.Error
	if errors.As(err, &apiErr) && apiErr.Code == "not_found" {
		return false, nil
	}
	return false, err
}

func (a identityAuthenticator) Authenticate(ctx context.Context, token string) (platform.Actor, error) {
	user, err := a.svc.Authenticate(ctx, token)
	if err != nil {
		return platform.Actor{}, err
	}
	return platform.Actor{ID: user.ID, Role: string(user.Role)}, nil
}

func registerSettingsRoutes(mux *http.ServeMux, admin func(http.Handler) http.Handler, h *settingsapi.Handler) {
	mux.Handle("GET /api/admin/settings", admin(http.HandlerFunc(h.List)))
	mux.Handle("GET /api/admin/settings/revisions", admin(http.HandlerFunc(h.Revisions)))
	mux.Handle("GET /api/admin/settings/{key}", admin(http.HandlerFunc(h.Get)))
	mux.Handle("PATCH /api/admin/settings/{key}", admin(http.HandlerFunc(h.Update)))
}

func registerAuditRoutes(mux *http.ServeMux, admin func(http.Handler) http.Handler, h *auditapi.Handler) {
	mux.Handle("GET /api/admin/audit-logs", admin(http.HandlerFunc(h.List)))
}

func registerLLMRoutes(mux *http.ServeMux, admin func(http.Handler) http.Handler, h *llmapi.Handler) {
	mux.Handle("GET /api/admin/llm/configs", admin(http.HandlerFunc(h.ListConfigs)))
	mux.Handle("POST /api/admin/llm/configs", admin(http.HandlerFunc(h.CreateConfig)))
	mux.Handle("GET /api/admin/llm/configs/{id}", admin(http.HandlerFunc(h.GetConfig)))
	mux.Handle("PATCH /api/admin/llm/configs/{id}", admin(http.HandlerFunc(h.UpdateConfig)))
	mux.Handle("DELETE /api/admin/llm/configs/{id}", admin(http.HandlerFunc(h.DeleteConfig)))
	mux.Handle("POST /api/admin/llm/configs/{id}/test", admin(http.HandlerFunc(h.TestConfig)))
	mux.Handle("POST /api/admin/llm/configs/{id}/refresh-models", admin(http.HandlerFunc(h.RefreshModels)))
	mux.Handle("POST /api/admin/llm/configs/{id}/models", admin(http.HandlerFunc(h.AddModel)))
	mux.Handle("DELETE /api/admin/llm/configs/{id}/models/{modelId}", admin(http.HandlerFunc(h.DeleteModel)))
	mux.Handle("GET /api/admin/llm/models", admin(http.HandlerFunc(h.ListModels)))
}

func registerContentRoutes(mux *http.ServeMux, authenticated func(http.Handler) http.Handler, h *storageapi.Handler) {
	mux.Handle("POST /api/content", authenticated(http.HandlerFunc(h.Upload)))
	mux.Handle("GET /api/content/{sha256}", authenticated(http.HandlerFunc(h.Download)))
}

func registerEventRoutes(mux *http.ServeMux, authenticated func(http.Handler) http.Handler, h *eventingapi.Handler) {
	mux.Handle("GET /api/events", authenticated(http.HandlerFunc(h.Snapshot)))
	mux.Handle("GET /api/stream", authenticated(http.HandlerFunc(h.Stream)))
}

func registerVersionRoutes(mux *http.ServeMux, authenticated func(http.Handler) http.Handler, h *versionapi.Handler) {
	mux.Handle("GET /api/versions", authenticated(http.HandlerFunc(h.List)))
	mux.Handle("POST /api/versions", authenticated(http.HandlerFunc(h.Create)))
	mux.Handle("GET /api/versions/{id}", authenticated(http.HandlerFunc(h.Get)))
	mux.Handle("PATCH /api/versions/{id}", authenticated(http.HandlerFunc(h.Update)))
}

func registerRequirementRoutes(mux *http.ServeMux, authenticated func(http.Handler) http.Handler, h *versionapi.RequirementHandler) {
	mux.Handle("GET /api/versions/{id}/requirements", authenticated(http.HandlerFunc(h.List)))
	mux.Handle("POST /api/versions/{id}/requirements", authenticated(http.HandlerFunc(h.Create)))
	mux.Handle("POST /api/versions/{id}/requirements/confirmations", authenticated(http.HandlerFunc(h.BatchConfirm)))
	mux.Handle("GET /api/versions/{id}/requirements/{rid}", authenticated(http.HandlerFunc(h.Get)))
	mux.Handle("PATCH /api/versions/{id}/requirements/{rid}", authenticated(http.HandlerFunc(h.Update)))
	mux.Handle("POST /api/versions/{id}/requirements/{rid}/confirmations", authenticated(http.HandlerFunc(h.Confirm)))
	mux.Handle("POST /api/versions/{id}/requirements/{rid}/rollback", authenticated(http.HandlerFunc(h.Rollback)))
	mux.Handle("GET /api/versions/{id}/requirements/{rid}/relations", authenticated(http.HandlerFunc(h.ListRelations)))
}

func registerConversationRoutes(mux *http.ServeMux, authenticated func(http.Handler) http.Handler, h *versionapi.ConversationHandler) {
	mux.Handle("GET /api/versions/{id}/conversations", authenticated(http.HandlerFunc(h.List)))
	mux.Handle("POST /api/versions/{id}/conversations", authenticated(http.HandlerFunc(h.Create)))
	mux.Handle("GET /api/versions/{id}/conversations/{cid}", authenticated(http.HandlerFunc(h.Get)))
	mux.Handle("GET /api/versions/{id}/conversations/{cid}/messages", authenticated(http.HandlerFunc(h.Messages)))
	mux.Handle("POST /api/versions/{id}/conversations/{cid}/messages", authenticated(http.HandlerFunc(h.PostMessage)))
}

func registerTeamRoomRoutes(mux *http.ServeMux, authenticated func(http.Handler) http.Handler, h *agentapi.TeamRoomHandler) {
	mux.Handle("GET /api/versions/{id}/team-rooms/{team}", authenticated(http.HandlerFunc(h.Read)))
}

// registerDesignRoutes mounts the P2 design graph/confirmation/export API.
func registerDesignRoutes(mux *http.ServeMux, authenticated func(http.Handler) http.Handler, h *designapi.Handler) {
	mux.Handle("GET /api/versions/{id}/design/graph", authenticated(http.HandlerFunc(h.ReadGraph)))
	mux.Handle("GET /api/versions/{id}/design/overview", authenticated(http.HandlerFunc(h.Overview)))
	mux.Handle("GET /api/versions/{id}/design/cases/{caseId}", authenticated(http.HandlerFunc(h.GetCase)))
	mux.Handle("POST /api/versions/{id}/design/confirmations", authenticated(http.HandlerFunc(h.Confirm)))
	mux.Handle("POST /api/versions/{id}/exports", authenticated(http.HandlerFunc(h.CreateExport)))
	mux.Handle("GET /api/versions/{id}/exports", authenticated(http.HandlerFunc(h.ListExports)))
	mux.Handle("GET /api/exports/{exportId}", authenticated(http.HandlerFunc(h.GetExport)))
	mux.Handle("GET /api/exports/{exportId}/download", authenticated(http.HandlerFunc(h.DownloadExport)))
}

func registerResourceRoutes(mux *http.ServeMux, authenticated, admin func(http.Handler) http.Handler, h *resourceapi.Handler) {
	// Catalog and SUT roots.
	mux.Handle("GET /api/resources/catalog", authenticated(http.HandlerFunc(h.Catalog)))
	mux.Handle("GET /api/resources/suts", authenticated(http.HandlerFunc(h.ListSUTs)))
	mux.Handle("POST /api/resources/suts", admin(http.HandlerFunc(h.CreateSUT)))
	mux.Handle("GET /api/resources/suts/{id}", authenticated(http.HandlerFunc(h.GetSUT)))
	mux.Handle("PATCH /api/resources/suts/{id}", admin(http.HandlerFunc(h.UpdateSUT)))
	mux.Handle("DELETE /api/resources/suts/{id}", admin(http.HandlerFunc(h.DeleteSUT)))

	// Environments.
	mux.Handle("GET /api/resources/suts/{id}/environments", authenticated(http.HandlerFunc(h.ListEnvironments)))
	mux.Handle("POST /api/resources/suts/{id}/environments", admin(http.HandlerFunc(h.CreateEnvironment)))
	mux.Handle("PATCH /api/resources/environments/{id}", admin(http.HandlerFunc(h.UpdateEnvironment)))
	mux.Handle("DELETE /api/resources/environments/{id}", admin(http.HandlerFunc(h.DeleteEnvironment)))

	// Pages.
	mux.Handle("GET /api/resources/environments/{id}/pages", authenticated(http.HandlerFunc(h.ListPages)))
	mux.Handle("POST /api/resources/environments/{id}/pages", admin(http.HandlerFunc(h.CreatePage)))
	mux.Handle("POST /api/resources/environments/{id}/pages/import", admin(http.HandlerFunc(h.ImportPages)))
	mux.Handle("PATCH /api/resources/pages/{id}", admin(http.HandlerFunc(h.UpdatePage)))
	mux.Handle("DELETE /api/resources/pages/{id}", admin(http.HandlerFunc(h.DeletePage)))

	// APIs.
	mux.Handle("GET /api/resources/environments/{id}/apis", authenticated(http.HandlerFunc(h.ListAPIs)))
	mux.Handle("POST /api/resources/environments/{id}/apis", admin(http.HandlerFunc(h.CreateAPI)))
	mux.Handle("POST /api/resources/environments/{id}/apis/import", admin(http.HandlerFunc(h.ImportAPIs)))
	mux.Handle("PATCH /api/resources/apis/{id}", admin(http.HandlerFunc(h.UpdateAPI)))
	mux.Handle("DELETE /api/resources/apis/{id}", admin(http.HandlerFunc(h.DeleteAPI)))

	// Target databases.
	mux.Handle("GET /api/resources/suts/{id}/databases", authenticated(http.HandlerFunc(h.ListDatabases)))
	mux.Handle("POST /api/resources/suts/{id}/databases", admin(http.HandlerFunc(h.CreateDatabase)))
	mux.Handle("PATCH /api/resources/databases/{id}", admin(http.HandlerFunc(h.UpdateDatabase)))
	mux.Handle("DELETE /api/resources/databases/{id}", admin(http.HandlerFunc(h.DeleteDatabase)))

	// Test accounts.
	mux.Handle("GET /api/resources/suts/{id}/accounts", authenticated(http.HandlerFunc(h.ListAccounts)))
	mux.Handle("POST /api/resources/suts/{id}/accounts", admin(http.HandlerFunc(h.CreateAccount)))
	mux.Handle("PATCH /api/resources/accounts/{id}", admin(http.HandlerFunc(h.UpdateAccount)))
	mux.Handle("DELETE /api/resources/accounts/{id}", admin(http.HandlerFunc(h.DeleteAccount)))

	// Code repositories: any authenticated user, per D09/architecture.
	mux.Handle("GET /api/resources/code-repos", authenticated(http.HandlerFunc(h.ListRepositories)))
	mux.Handle("POST /api/resources/code-repos", authenticated(http.HandlerFunc(h.CreateRepository)))
	mux.Handle("GET /api/resources/code-repos/{id}", authenticated(http.HandlerFunc(h.GetRepository)))
	mux.Handle("PATCH /api/resources/code-repos/{id}", authenticated(http.HandlerFunc(h.UpdateRepository)))
	mux.Handle("DELETE /api/resources/code-repos/{id}", authenticated(http.HandlerFunc(h.DeleteRepository)))
}

// registerExecutionRoutes mounts the P3 execution API (21 path items / 22
// operations, spec §9.2). A nil handler keeps P0–P2 deployments unchanged.
func registerExecutionRoutes(mux *http.ServeMux, authenticated, admin func(http.Handler) http.Handler, h *executionhttp.Handler) {
	if h == nil {
		return
	}
	h.Register(mux, authenticated, admin)
}
