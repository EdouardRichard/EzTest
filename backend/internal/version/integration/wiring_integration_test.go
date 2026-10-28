//go:build integration

package integration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	agentpostgres "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/postgres"
	agenttools "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/tools"
	"github.com/EdouardRichard/EzTest/backend/internal/config"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	eventinginfra "github.com/EdouardRichard/EzTest/backend/internal/eventing/infrastructure"
	eventpostgres "github.com/EdouardRichard/EzTest/backend/internal/eventing/infrastructure/postgres"
	identityapp "github.com/EdouardRichard/EzTest/backend/internal/identity/application"
	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	resourcedomain "github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/infrastructure/gitexec"
	resourcepostgres "github.com/EdouardRichard/EzTest/backend/internal/resource/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/server"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versionagent "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/agent"
	versioneventing "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/eventing"
	versionfilestore "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/filestore"
	versiongitread "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/gitread"
	versionpostgres "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/postgres"
	versionresource "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/resource"
	versionhttp "github.com/EdouardRichard/EzTest/backend/internal/version/interfaces/httpapi"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Wiring test constants. The node ID is deliberately not the default (1) so the
// shared-generator assertion cannot pass by accident.
const (
	wiringToken           = "ezt_wiring"
	wiringSnowflakeNodeID = int64(7)
	wiringUserID          = int64(1)
)

// wiringClock is the P2 application.Clock.
type wiringClock struct{}

// Now returns the current time.
func (wiringClock) Now() time.Time { return time.Now() }

// wiringAuth is the identity AuthService double: it authenticates one fixed
// token and has no login/logout surface (the wiring test logs in directly).
type wiringAuth struct {
	user identity.User
}

// Authenticate resolves the fixed wiring token.
func (a wiringAuth) Authenticate(_ context.Context, rawToken string) (identity.User, error) {
	if rawToken == wiringToken {
		return a.user, nil
	}
	return identity.User{}, identity.ErrUnauthenticated
}

// Login is not part of the wiring test.
func (wiringAuth) Login(context.Context, string, string) (identityapp.LoginResult, error) {
	return identityapp.LoginResult{}, identity.ErrUnauthenticated
}

// Logout is a no-op.
func (wiringAuth) Logout(context.Context, string) error { return nil }

// p1AgentServices breaks the construction cycle between the P1 run service (it
// needs the tool registry, which carries the P3 clarification tools) and the P3
// clarification service (it needs the P1 session and run services). The
// composition root passes this holder to versionagent.NewAgentSessions and binds
// the final P1 services right after they exist, before the HTTP server starts.
type p1AgentServices struct {
	sessions *agentapp.SessionService
	runs     *agentapp.RunService
}

// bind fills the late-bound services.
func (s *p1AgentServices) bind(sessions *agentapp.SessionService, runs *agentapp.RunService) {
	s.sessions = sessions
	s.runs = runs
}

// unavailable is the error returned before bind.
func (s *p1AgentServices) unavailable(service string) error {
	return fmt.Errorf("version wiring: P1 %s service is not bound", service)
}

// CreateWithOptions delegates to the P1 session service.
func (s *p1AgentServices) CreateWithOptions(ctx context.Context, profileID int64, title string, actor *int64, opts agentapp.SessionOptions) (agentdomain.Session, error) {
	if s.sessions == nil {
		return agentdomain.Session{}, s.unavailable("session")
	}
	return s.sessions.CreateWithOptions(ctx, profileID, title, actor, opts)
}

// ListMessages delegates to the P1 session service.
func (s *p1AgentServices) ListMessages(ctx context.Context, sessionID, beforeID int64, limit int) ([]agentdomain.Message, bool, error) {
	if s.sessions == nil {
		return nil, false, s.unavailable("session")
	}
	return s.sessions.ListMessages(ctx, sessionID, beforeID, limit)
}

// Start delegates to the P1 run service.
func (s *p1AgentServices) Start(ctx context.Context, sessionID int64, message string, actor *int64) (agentdomain.StartResult, error) {
	if s.runs == nil {
		return agentdomain.StartResult{}, s.unavailable("run")
	}
	return s.runs.Start(ctx, sessionID, message, actor)
}

// StartWithParts delegates to the P1 run service.
func (s *p1AgentServices) StartWithParts(ctx context.Context, sessionID int64, parts []agentdomain.Part, actor *int64) (agentdomain.StartResult, error) {
	if s.runs == nil {
		return agentdomain.StartResult{}, s.unavailable("run")
	}
	return s.runs.StartWithParts(ctx, sessionID, parts, actor)
}

// HasRunningByChannel delegates to the P1 run service.
func (s *p1AgentServices) HasRunningByChannel(ctx context.Context, channel string) (bool, error) {
	if s.runs == nil {
		return false, s.unavailable("run")
	}
	return s.runs.HasRunningByChannel(ctx, channel)
}

// p3Wiring is the assembled P3 graph the test drives over HTTP.
type p3Wiring struct {
	handler     http.Handler
	registry    *agentapp.ToolRegistry
	profileRepo *agentpostgres.ProfileRepository
	systemID    int64
}

// seedP3Profiles mirrors the production startup seed: it first proves every
// allowed tool of the three P3 profiles exists in the real registry, then seeds
// the profiles idempotently.
func seedP3Profiles(ctx context.Context, profiles *agentpostgres.ProfileRepository, registry *agentapp.ToolRegistry) error {
	seeds := agentdomain.ClarificationProfiles()
	for _, profile := range seeds {
		if err := profile.Validate(); err != nil {
			return fmt.Errorf("validate P3 agent profile %q: %w", profile.Key, err)
		}
		for _, name := range profile.AllowedTools {
			if tool, ok := registry.Get(name); !ok || tool == nil {
				return fmt.Errorf("P3 agent profile %q allows unregistered tool %q", profile.Key, name)
			}
		}
	}
	for _, profile := range seeds {
		if _, err := profiles.EnsureBuiltin(ctx, profile); err != nil {
			return fmt.Errorf("seed P3 agent profile %q: %w", profile.Key, err)
		}
	}
	return nil
}

// newP3Wiring assembles the P3 version context exactly the way the production
// composition root does: one shared Snowflake generator, the real PostgreSQL
// repositories, the T13 adapters, the 11 clarification tools, the three seeded
// profiles and the server dependency group.
func newP3Wiring(t *testing.T, ctx context.Context, pool *pgxpool.Pool, attachmentDir string) *p3Wiring {
	t.Helper()

	// P2 resource stack, so the catalog adapter and the version binding
	// resolution are the production implementations.
	catalogRepo := resourcepostgres.NewCatalogRepository(pool)
	codeRepoRepo := resourcepostgres.NewCodeRepository(pool)
	catalogService := resourceapp.NewCatalogService(
		catalogRepo, catalogRepo, catalogRepo, catalogRepo, wiringClock{})
	codeRepoService := resourceapp.NewCodeRepoService(
		codeRepoRepo, gitexec.NewValidator(), wiringClock{})
	system, err := catalogService.CreateSystem(ctx, resourceapp.CreateSystemInput{
		Key:     "wiring-system",
		Name:    "Wiring system",
		Kind:    resourcedomain.SystemKindWeb,
		BaseURL: "http://127.0.0.1:13335",
	})
	if err != nil {
		t.Fatalf("seed P2 system: %v", err)
	}
	versionCatalog := versionresource.NewCatalog(catalogService, catalogService, codeRepoService)

	// P0 eventing: the version events are persisted and fanned out to the
	// in-process hub, which backs the version SSE stream.
	eventStore := eventpostgres.NewEventStore(pool)
	eventHub := eventinginfra.NewHub()
	eventService := eventingapp.NewService(eventStore, eventHub)
	versionEvents := versioneventing.NewEventPublisher(eventService)

	// P3 repositories, adapters and services.
	versionRepo := versionpostgres.NewVersionRepository(pool)
	requirementRepo := versionpostgres.NewRequirementRepository(pool)
	clarificationRepo := versionpostgres.NewClarificationRepository(pool)
	attachmentStore := versionfilestore.New(attachmentDir)
	git := versiongitread.NewGitReader(0)
	ids, err := id.NewSnowflake(wiringSnowflakeNodeID)
	if err != nil {
		t.Fatalf("create shared snowflake generator: %v", err)
	}

	versionService := versionapp.NewVersionService(
		versionRepo, versionCatalog, git, versionEvents,
		versionapp.WithIDGenerator(ids))
	requirementService := versionapp.NewRequirementService(
		requirementRepo, versionRepo, versionEvents,
		versionapp.WithRequirementIDGenerator(ids))
	relationService := versionapp.NewRelationService(
		requirementRepo,
		versionapp.WithRelationIDGenerator(ids),
		versionapp.WithRelationEventPublisher(versionEvents))
	attachmentService := versionapp.NewAttachmentService(
		requirementRepo, attachmentStore, versionapp.DefaultLimits(), versionEvents,
		versionapp.WithAttachmentIDGenerator(ids))

	// P1 agent stack. The run service has no LLM caller because the wiring test
	// never starts a run; it only creates the P1 clarification session.
	profileRepo := agentpostgres.NewProfileRepository(pool)
	sessionRepo := agentpostgres.NewSessionRepository(pool)
	runRepo := agentpostgres.NewRunRepository(pool)
	messageRepo := agentpostgres.NewMessageRepository(pool)
	profileService := agentapp.NewProfileService(profileRepo, nil)

	late := &p1AgentServices{}
	agentSessionsAdapter := versionagent.NewAgentSessions(late, late, profileService)

	clarificationService := versionapp.NewClarificationService(
		clarificationRepo, requirementRepo, versionRepo, agentSessionsAdapter, versionEvents,
		versionapp.WithClarificationIDGenerator(ids))
	toolService := versionapp.NewAgentToolService(versionapp.AgentToolServiceDeps{
		Versions:             versionRepo,
		Requirements:         requirementRepo,
		Clarifications:       clarificationRepo,
		RequirementService:   requirementService,
		RelationService:      relationService,
		ClarificationService: clarificationService,
		AttachmentService:    attachmentService,
		Resources:            versionCatalog,
		Git:                  git,
	})

	toolList := []agentdomain.Tool{
		agenttools.NewTestEcho(),
		agenttools.NewTestFail(),
		agenttools.NewHTTPGet([]string{"127.0.0.1"}, 0),
	}
	toolList = append(toolList, agenttools.NewClarificationTools(toolService)...)
	registry := agentapp.NewToolRegistry(toolList...)
	if err := seedP3Profiles(ctx, profileRepo, registry); err != nil {
		t.Fatalf("seed P3 agent profiles: %v", err)
	}
	executor := agentapp.NewToolExecutor(registry, nil, 0)
	runService := agentapp.NewRunService(agentapp.RunServiceDeps{
		Profiles:          profileRepo,
		Sessions:          sessionRepo,
		Runs:              runRepo,
		Messages:          messageRepo,
		Tools:             registry,
		Executor:          executor,
		MaxConcurrentRuns: 4,
		HardTimeout:       time.Minute,
	})
	sessionService := agentapp.NewSessionService(
		profileRepo, sessionRepo, runRepo, messageRepo, runService, nil)
	late.bind(sessionService, runService)

	versionHandlers := versionhttp.NewHandlers(versionhttp.HandlersDeps{
		Versions:              versionService,
		VersionCounts:         versionRepo,
		ResourceCatalog:       versionCatalog,
		Requirements:          requirementService,
		RequirementStats:      requirementRepo,
		Relations:             relationService,
		Attachments:           attachmentService,
		Clarification:         clarificationService,
		ClarificationMessages: agentSessionsAdapter,
		Events:                eventService,
		EventReader:           eventStore,
		EventSubscriber:       eventHub,
	})

	handler := server.New(config.Config{Env: "test"}, slog.New(slog.NewTextHandler(io.Discard, nil)), server.Dependencies{
		Auth:    wiringAuth{user: identity.User{ID: wiringUserID, Username: "wiring-admin", Role: identity.RoleAdmin, Status: identity.StatusActive}},
		Version: versionHandlers,
	})
	return &p3Wiring{
		handler:     handler,
		registry:    registry,
		profileRepo: profileRepo,
		systemID:    system.ID,
	}
}

// wiringDo sends one HTTP request and returns the status and body.
func wiringDo(t *testing.T, ctx context.Context, srv *httptest.Server, method, path, token, body string) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, srv.URL+path, reader)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s %s: %v", method, path, err)
	}
	return resp.StatusCode, payload
}

// wiringJSON asserts a JSON response status and decodes the body.
func wiringJSON(t *testing.T, ctx context.Context, srv *httptest.Server, method, path, body string, status int, out any) {
	t.Helper()
	got, payload := wiringDo(t, ctx, srv, method, path, wiringToken, body)
	if got != status {
		t.Fatalf("%s %s=%d want %d body=%s", method, path, got, status, payload)
	}
	if out != nil {
		if err := json.Unmarshal(payload, out); err != nil {
			t.Fatalf("decode %s %s body=%s: %v", method, path, payload, err)
		}
	}
}

// wiringID decodes a P3 id and proves its JSON representation is the decimal
// string the contract requires.
func wiringID(t *testing.T, raw json.RawMessage, field string) int64 {
	t.Helper()
	text := string(raw)
	if len(text) < 3 || text[0] != '"' || text[len(text)-1] != '"' {
		t.Fatalf("%s=%s want a decimal JSON string", field, text)
	}
	value, err := strconv.ParseInt(text[1:len(text)-1], 10, 64)
	if err != nil || value <= 0 {
		t.Fatalf("%s=%q is not a positive decimal id", field, text)
	}
	return value
}

// wiringAssertSnowflake proves the id was minted by the shared generator with
// the configured node id (12 sequence bits, then 10 node bits).
func wiringAssertSnowflake(t *testing.T, value int64, field string) {
	t.Helper()
	if got := (value >> 12) & 0x3ff; got != wiringSnowflakeNodeID {
		t.Fatalf("%s=%d has snowflake node %d, want %d (shared generator not injected)", field, value, got, wiringSnowflakeNodeID)
	}
}

// wiringUploadAttachment posts a small text attachment and returns its metadata.
func wiringUploadAttachment(t *testing.T, ctx context.Context, srv *httptest.Server, versionID, requirementID int64) (int64, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "wiring-notes.txt")
	if err != nil {
		t.Fatalf("create multipart file: %v", err)
	}
	if _, err := io.WriteString(part, "wiring attachment content\n"); err != nil {
		t.Fatalf("write multipart file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/api/versions/%d/requirements/%d/attachments", srv.URL, versionID, requirementID),
		bytes.NewReader(body.Bytes()))
	if err != nil {
		t.Fatalf("build attachment request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+wiringToken)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("upload attachment: %v", err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read attachment response: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload attachment=%d body=%s want 201", resp.StatusCode, payload)
	}
	var created struct {
		Data struct {
			ID       json.RawMessage `json:"id"`
			Filename string          `json:"filename"`
			SHA256   string          `json:"sha256"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &created); err != nil {
		t.Fatalf("decode attachment body=%s: %v", payload, err)
	}
	return wiringID(t, created.Data.ID, "attachment.id"), created.Data.Filename
}

// wiringStreamEvents opens the version SSE stream from the beginning and waits
// until every wanted event type arrived.
func wiringStreamEvents(t *testing.T, ctx context.Context, srv *httptest.Server, versionID int64, want ...string) map[string]bool {
	t.Helper()
	streamCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(streamCtx, http.MethodGet,
		fmt.Sprintf("%s/api/versions/%d/stream?since=0", srv.URL, versionID), nil)
	if err != nil {
		t.Fatalf("build stream request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+wiringToken)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("open version stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("version stream=%d want 200", resp.StatusCode)
	}
	if contentType := resp.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		t.Fatalf("version stream Content-Type=%q want text/event-stream", contentType)
	}

	wanted := make(map[string]bool, len(want))
	for _, name := range want {
		wanted[name] = false
	}
	seen := make(map[string]bool, len(want))
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "event: ") {
			continue
		}
		name := strings.TrimPrefix(line, "event: ")
		seen[name] = true
		if _, ok := wanted[name]; ok {
			wanted[name] = true
		}
		pending := false
		for _, done := range wanted {
			if !done {
				pending = true
				break
			}
		}
		if !pending {
			return seen
		}
	}
	t.Fatalf("version stream ended before %v arrived: seen=%v err=%v", want, seen, scanner.Err())
	return seen
}

// TestP3WiringEndToEnd drives the assembled P3 stack over HTTP against the real
// PostgreSQL repositories: the P3 routes require a login, a version and a
// requirement are created through the handlers, the attachment is written below
// EZTEST_VERSION_ATTACHMENT_DIR, the clarification session spawns a real P1
// session on the clarification-dialog profile and version channel, the three P3
// profiles are seeded with registered tools, the shared Snowflake generator mints
// every P3 id, and the version stream replays version.created/requirement.added.
func TestP3WiringEndToEnd(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)

	// The test database is truncated after migration: restore the seeded user
	// (agent_session.created_by is an FK) and the default P2 company (the
	// catalog services resolve it for every write).
	if _, err := pool.Exec(ctx, `INSERT INTO user_account (id, username, password_hash, role)
		VALUES ($1, 'wiring-admin', 'not-used', 'admin')`, wiringUserID); err != nil {
		t.Fatalf("seed wiring user: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO company (key, name) VALUES ('default', 'Default')`); err != nil {
		t.Fatalf("seed default company: %v", err)
	}

	attachmentDir := filepath.Join(t.TempDir(), "version-attachments")
	wiring := newP3Wiring(t, ctx, pool, attachmentDir)
	srv := httptest.NewServer(wiring.handler)
	defer srv.Close()

	// The P3 surface sits behind the global bearer auth with no admin guard.
	if status, body := wiringDo(t, ctx, srv, http.MethodGet, "/api/versions", "", ""); status != http.StatusUnauthorized {
		t.Fatalf("anonymous GET versions=%d body=%s want 401", status, body)
	}
	if status, body := wiringDo(t, ctx, srv, http.MethodPost, "/api/versions", "", `{"name":"x"}`); status != http.StatusUnauthorized {
		t.Fatalf("anonymous POST versions=%d body=%s want 401", status, body)
	}

	// Create a version bound to the seeded P2 system: the handler renders the
	// resolved binding through the real catalog adapter.
	var createdVersion struct {
		Data struct {
			ID       json.RawMessage `json:"id"`
			Status   string          `json:"status"`
			Revision int             `json:"revision"`
			Bindings struct {
				System *struct {
					ID        int64  `json:"id"`
					Name      string `json:"name"`
					Available bool   `json:"available"`
				} `json:"system"`
			} `json:"bindings"`
		} `json:"data"`
	}
	wiringJSON(t, ctx, srv, http.MethodPost, "/api/versions",
		fmt.Sprintf(`{"name":"Wiring version","systemId":%d}`, wiring.systemID), http.StatusCreated, &createdVersion)
	versionID := wiringID(t, createdVersion.Data.ID, "version.id")
	wiringAssertSnowflake(t, versionID, "version.id")
	if createdVersion.Data.Status != "draft" || createdVersion.Data.Revision != 1 {
		t.Fatalf("created version status/revision=%q/%d want draft/1", createdVersion.Data.Status, createdVersion.Data.Revision)
	}
	if createdVersion.Data.Bindings.System == nil ||
		createdVersion.Data.Bindings.System.ID != wiring.systemID ||
		createdVersion.Data.Bindings.System.Name != "Wiring system" ||
		!createdVersion.Data.Bindings.System.Available {
		t.Fatalf("version bindings=%+v want the resolved Wiring system", createdVersion.Data.Bindings)
	}

	// The version is listed and readable through the handlers.
	var listed struct {
		Data []struct {
			ID json.RawMessage `json:"id"`
		} `json:"data"`
	}
	wiringJSON(t, ctx, srv, http.MethodGet, "/api/versions", "", http.StatusOK, &listed)
	if len(listed.Data) != 1 || wiringID(t, listed.Data[0].ID, "list.id") != versionID {
		t.Fatalf("version list=%+v want the created version", listed.Data)
	}

	// Create a draft requirement; it must be uploaded to before confirmation
	// because attachments are only accepted on draft requirements.
	var createdRequirement struct {
		Data struct {
			ID       json.RawMessage `json:"id"`
			Status   string          `json:"status"`
			Version  json.RawMessage `json:"versionId"`
			Revision int             `json:"revision"`
		} `json:"data"`
	}
	wiringJSON(t, ctx, srv, http.MethodPost, fmt.Sprintf("/api/versions/%d/requirements", versionID),
		`{"title":"Wiring requirement","description":"assembled through the composition root","priority":"must"}`,
		http.StatusCreated, &createdRequirement)
	requirementID := wiringID(t, createdRequirement.Data.ID, "requirement.id")
	wiringAssertSnowflake(t, requirementID, "requirement.id")
	if createdRequirement.Data.Status != "draft" {
		t.Fatalf("requirement status=%q want draft", createdRequirement.Data.Status)
	}
	if got := string(createdRequirement.Data.Version); got != strconv.Quote(strconv.FormatInt(versionID, 10)) {
		t.Fatalf("requirement versionId=%s want %q", got, strconv.FormatInt(versionID, 10))
	}

	// Upload an attachment through the handler and prove the bytes landed below
	// the configured attachment root.
	attachmentID, filename := wiringUploadAttachment(t, ctx, srv, versionID, requirementID)
	wiringAssertSnowflake(t, attachmentID, "attachment.id")
	if filename != "wiring-notes.txt" {
		t.Fatalf("attachment filename=%q", filename)
	}
	var storagePath string
	if err := pool.QueryRow(ctx,
		`SELECT storage_path FROM requirement_attachment WHERE id = $1`, attachmentID).Scan(&storagePath); err != nil {
		t.Fatalf("load attachment storage path: %v", err)
	}
	if !strings.HasPrefix(storagePath, strconv.FormatInt(versionID, 10)+"/") {
		t.Fatalf("storage_path=%q want the version directory prefix", storagePath)
	}
	content, err := os.ReadFile(filepath.Join(attachmentDir, filepath.FromSlash(storagePath)))
	if err != nil {
		t.Fatalf("read stored attachment: %v", err)
	}
	if string(content) != "wiring attachment content\n" {
		t.Fatalf("stored attachment content=%q", content)
	}
	if info, err := os.Stat(attachmentDir); err != nil {
		t.Fatalf("stat configured attachment dir: %v", err)
	} else if runtime.GOOS != "windows" {
		// Windows cannot represent the Unix permission bits; the filestore
		// still enforces 0700 on Unix hosts.
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			t.Fatalf("attachment dir mode=%#o want 0700 (no group/other bits)", perm)
		}
	}

	// Confirm the requirement, move the version through start-clarification and
	// enter-design; each action runs through the real services.
	var confirmed struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	wiringJSON(t, ctx, srv, http.MethodPost,
		fmt.Sprintf("/api/versions/%d/requirements/%d/confirm", versionID, requirementID),
		"", http.StatusOK, &confirmed)
	if confirmed.Data.Status != "clarified" {
		t.Fatalf("confirmed requirement status=%q want clarified", confirmed.Data.Status)
	}

	var started struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	wiringJSON(t, ctx, srv, http.MethodPost, fmt.Sprintf("/api/versions/%d/start-clarification", versionID),
		"", http.StatusOK, &started)
	if started.Data.Status != "clarifying" {
		t.Fatalf("start-clarification status=%q want clarifying", started.Data.Status)
	}

	// Starting the clarification session must create a real P1 session on the
	// clarification-dialog profile and stamp the version channel.
	var sessionStarted struct {
		Data struct {
			ID             json.RawMessage `json:"id"`
			Status         string          `json:"status"`
			AgentSessionID int64           `json:"agentSessionId"`
		} `json:"data"`
	}
	wiringJSON(t, ctx, srv, http.MethodPost,
		fmt.Sprintf("/api/versions/%d/clarification/sessions", versionID), "",
		http.StatusCreated, &sessionStarted)
	clarificationSessionID := wiringID(t, sessionStarted.Data.ID, "clarificationSession.id")
	wiringAssertSnowflake(t, clarificationSessionID, "clarificationSession.id")
	if sessionStarted.Data.Status != "active" || sessionStarted.Data.AgentSessionID <= 0 {
		t.Fatalf("clarification session=%+v want active with a P1 agent session", sessionStarted.Data)
	}

	var profileKey, activityChannel string
	var activityVersionID *string
	if err := pool.QueryRow(ctx, `
		SELECT p.key, s.activity_channel, s.activity_version_id
		FROM agent_session s JOIN agent_profile p ON p.id = s.profile_id
		WHERE s.id = $1`, sessionStarted.Data.AgentSessionID).
		Scan(&profileKey, &activityChannel, &activityVersionID); err != nil {
		t.Fatalf("load P1 clarification session: %v", err)
	}
	if profileKey != "clarification-dialog" {
		t.Fatalf("P1 session profile=%q want clarification-dialog", profileKey)
	}
	if want := fmt.Sprintf("version:%d", versionID); activityChannel != want {
		t.Fatalf("P1 session channel=%q want %q", activityChannel, want)
	}
	if activityVersionID == nil || *activityVersionID != strconv.FormatInt(versionID, 10) {
		t.Fatalf("P1 session activity_version_id=%v want %d", activityVersionID, versionID)
	}

	// The clarification history route reads through the same T13 adapter
	// (ClarificationMessages). An empty page proves the adapter is wired; a nil
	// reader would surface as a 500.
	var messagePage struct {
		Data    []json.RawMessage `json:"data"`
		HasMore bool              `json:"has_more"`
	}
	wiringJSON(t, ctx, srv, http.MethodGet,
		fmt.Sprintf("/api/versions/%d/clarification/messages", versionID), "",
		http.StatusOK, &messagePage)
	if len(messagePage.Data) != 0 || messagePage.HasMore {
		t.Fatalf("clarification message page=%+v want an empty page", messagePage)
	}

	var enteredDesign struct {
		Data struct {
			Version struct {
				Status string `json:"status"`
			} `json:"version"`
			Warnings []string `json:"warnings"`
		} `json:"data"`
	}
	wiringJSON(t, ctx, srv, http.MethodPost, fmt.Sprintf("/api/versions/%d/enter-design", versionID),
		`{}`, http.StatusOK, &enteredDesign)
	if enteredDesign.Data.Version.Status != "designing" {
		t.Fatalf("enter-design status=%q want designing", enteredDesign.Data.Version.Status)
	}
	if len(enteredDesign.Data.Warnings) != 0 {
		t.Fatalf("enter-design warnings=%v want none after confirmation", enteredDesign.Data.Warnings)
	}

	// The version SSE stream replays the persisted version.created and
	// requirement.added events published by the assembled eventing adapter.
	seen := wiringStreamEvents(t, ctx, srv, versionID, "version.created", "requirement.added")
	if !seen["version.created"] || !seen["requirement.added"] {
		t.Fatalf("stream events=%v want version.created and requirement.added", seen)
	}

	// The flat P3 event snapshot reads through the eventing service wired into
	// the handler group.
	var snapshot struct {
		Data []struct {
			Type string `json:"type"`
		} `json:"data"`
		SnapshotCursor int64 `json:"snapshot_cursor"`
	}
	wiringJSON(t, ctx, srv, http.MethodGet, fmt.Sprintf("/api/versions/%d/events", versionID),
		"", http.StatusOK, &snapshot)
	snapshotTypes := make(map[string]bool, len(snapshot.Data))
	for _, event := range snapshot.Data {
		snapshotTypes[event.Type] = true
	}
	if !snapshotTypes["version.created"] || !snapshotTypes["requirement.added"] {
		t.Fatalf("event snapshot types=%v want version.created and requirement.added", snapshotTypes)
	}
	if snapshot.SnapshotCursor == 0 {
		t.Fatal("event snapshot cursor=0 want the newest persisted seq")
	}

	// The three P3 profiles are seeded and every allowed tool comes from the
	// real registry, not just from the seed literals.
	rows, err := pool.Query(ctx, `SELECT key, allowed_tools FROM agent_profile ORDER BY id`)
	if err != nil {
		t.Fatalf("list seeded profiles: %v", err)
	}
	defer rows.Close()
	seeded := make(map[string]bool, 3)
	for rows.Next() {
		var key string
		var allowedTools []string
		if err := rows.Scan(&key, &allowedTools); err != nil {
			t.Fatalf("scan seeded profile: %v", err)
		}
		seeded[key] = true
		if len(allowedTools) == 0 {
			t.Fatalf("profile %q has no allowed tools", key)
		}
		for _, name := range allowedTools {
			if tool, ok := wiring.registry.Get(name); !ok || tool == nil {
				t.Fatalf("profile %q allows unregistered tool %q", key, name)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("list seeded profiles: %v", err)
	}
	for _, key := range []string{"clarification-dialog", "requirement-analysis", "context-recon"} {
		if !seeded[key] {
			t.Fatalf("profile %q was not seeded: %v", key, seeded)
		}
	}
	if len(seeded) != 3 {
		t.Fatalf("seeded profiles=%v want exactly the three P3 profiles", seeded)
	}

	// Seeding is idempotent: a second startup pass updates nothing.
	if err := seedP3Profiles(ctx, wiring.profileRepo, wiring.registry); err != nil {
		t.Fatalf("second profile seed: %v", err)
	}
	for _, profile := range agentdomain.ClarificationProfiles() {
		created, err := wiring.profileRepo.EnsureBuiltin(ctx, profile)
		if err != nil {
			t.Fatalf("re-seed %q: %v", profile.Key, err)
		}
		if created {
			t.Fatalf("re-seed recreated profile %q", profile.Key)
		}
	}

	// Every P3 id came from the one shared generator: unique, positive and in
	// creation order.
	allIDs := []int64{versionID, requirementID, attachmentID, clarificationSessionID}
	seenIDs := make(map[int64]bool, len(allIDs))
	for i, value := range allIDs {
		if seenIDs[value] {
			t.Fatalf("duplicate P3 id %d in %v (per-service generators?)", value, allIDs)
		}
		seenIDs[value] = true
		if i > 0 && value <= allIDs[i-1] {
			t.Fatalf("P3 ids %v are not strictly increasing; services do not share the generator", allIDs)
		}
	}
}
