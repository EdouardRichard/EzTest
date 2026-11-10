package llmapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

type fixedClock struct{ at time.Time }

func (f fixedClock) Now() time.Time { return f.at }

type seqIDs struct {
	mu   sync.Mutex
	next int64
}

func (s *seqIDs) Next() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	return s.next
}

type fakeRepo struct {
	mu          sync.Mutex
	configs     map[int64]llm.Config
	models      map[int64]llm.Model
	upsertCalls int
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{configs: map[int64]llm.Config{}, models: map[int64]llm.Model{}}
}

func (f *fakeRepo) ListConfigs(context.Context) ([]llm.Config, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := make([]llm.Config, 0, len(f.configs))
	for _, cfg := range f.configs {
		list = append(list, cfg)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list, nil
}

func (f *fakeRepo) GetConfig(_ context.Context, id int64) (llm.Config, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cfg, ok := f.configs[id]
	if !ok {
		return llm.Config{}, llm.ErrConfigNotFound
	}
	return cfg, nil
}

func (f *fakeRepo) nameTaken(name string, exceptID int64) bool {
	for _, cfg := range f.configs {
		if cfg.ID != exceptID && strings.EqualFold(cfg.Name, name) {
			return true
		}
	}
	return false
}

func (f *fakeRepo) clearDefault(exceptID int64) {
	for id, cfg := range f.configs {
		if id == exceptID {
			continue
		}
		if cfg.IsDefault {
			cfg.IsDefault = false
			f.configs[id] = cfg
		}
	}
}

func (f *fakeRepo) CreateConfig(_ context.Context, cfg llm.Config) (llm.Config, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.nameTaken(cfg.Name, 0) {
		return llm.Config{}, llm.ErrNameTaken
	}
	if cfg.IsDefault {
		f.clearDefault(0)
	}
	f.configs[cfg.ID] = cfg
	return cfg, nil
}

func (f *fakeRepo) UpdateConfig(_ context.Context, cfg llm.Config) (llm.Config, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.configs[cfg.ID]; !ok {
		return llm.Config{}, llm.ErrConfigNotFound
	}
	if f.nameTaken(cfg.Name, cfg.ID) {
		return llm.Config{}, llm.ErrNameTaken
	}
	if cfg.IsDefault {
		f.clearDefault(cfg.ID)
	}
	f.configs[cfg.ID] = cfg
	return cfg, nil
}

func (f *fakeRepo) DeleteConfig(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.configs[id]; !ok {
		return llm.ErrConfigNotFound
	}
	delete(f.configs, id)
	for modelID, model := range f.models {
		if model.ConfigID == id {
			delete(f.models, modelID)
		}
	}
	return nil
}

func (f *fakeRepo) ListModels(context.Context) ([]llm.Model, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := make([]llm.Model, 0, len(f.models))
	for _, model := range f.models {
		list = append(list, model)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list, nil
}

func (f *fakeRepo) ListModelsByConfig(_ context.Context, configID int64) ([]llm.Model, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := make([]llm.Model, 0)
	for _, model := range f.models {
		if model.ConfigID == configID {
			list = append(list, model)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list, nil
}

func (f *fakeRepo) UpsertModels(_ context.Context, configID int64, models []llm.Model, at time.Time) ([]llm.Model, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upsertCalls++
	for _, incoming := range models {
		var existingID int64
		for id, existing := range f.models {
			if existing.ConfigID == configID && existing.Model == incoming.Model {
				existingID = id
				break
			}
		}
		if existingID == 0 {
			incoming.ConfigID = configID
			f.models[incoming.ID] = incoming
			continue
		}
		existing := f.models[existingID]
		existing.ContextWindow = incoming.ContextWindow
		existing.MaxOutput = incoming.MaxOutput
		existing.SupportsTools = incoming.SupportsTools
		existing.SupportsVision = incoming.SupportsVision
		existing.Capabilities = incoming.Capabilities
		existing.Deprecated = incoming.Deprecated
		existing.Source = incoming.Source
		existing.UpdatedAt = incoming.UpdatedAt
		f.models[existingID] = existing
	}
	// Mirror the repository contract: catalog rows absent from the refresh are
	// kept and marked deprecated instead of being deleted.
	present := make(map[string]bool, len(models))
	for _, incoming := range models {
		present[incoming.Model] = true
	}
	for id, existing := range f.models {
		if existing.ConfigID != configID || existing.Source != llm.SourceCatalog || present[existing.Model] {
			continue
		}
		existing.Deprecated = true
		existing.UpdatedAt = at
		f.models[id] = existing
	}
	list := make([]llm.Model, 0)
	for _, model := range f.models {
		if model.ConfigID == configID {
			list = append(list, model)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list, nil
}

func (f *fakeRepo) InsertModel(_ context.Context, model llm.Model) (llm.Model, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, existing := range f.models {
		if existing.ConfigID == model.ConfigID && existing.Model == model.Model {
			return llm.Model{}, llm.ErrModelTaken
		}
	}
	f.models[model.ID] = model
	return model, nil
}

func (f *fakeRepo) DeleteModel(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.models[id]; !ok {
		return llm.ErrModelNotFound
	}
	delete(f.models, id)
	return nil
}

func (f *fakeRepo) UpdateModel(_ context.Context, model llm.Model) (llm.Model, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	existing, ok := f.models[model.ID]
	if !ok || existing.ConfigID != model.ConfigID {
		return llm.Model{}, llm.ErrModelNotFound
	}
	f.models[model.ID] = model
	return model, nil
}

type fakeClient struct {
	models     []RemoteModel
	listErr    error
	testResult TestResult
	testErr    error
	listCalls  int
	testCalls  int
	lastConfig llm.Config
}

func (f *fakeClient) ListModels(_ context.Context, cfg llm.Config) ([]RemoteModel, error) {
	f.listCalls++
	f.lastConfig = cfg
	return f.models, f.listErr
}

func (f *fakeClient) Test(_ context.Context, cfg llm.Config) (TestResult, error) {
	f.testCalls++
	f.lastConfig = cfg
	return f.testResult, f.testErr
}

type fakeAudit struct{ events []auditapp.Event }

func (f *fakeAudit) Record(_ context.Context, event auditapp.Event) error {
	f.events = append(f.events, event)
	return nil
}

type fakePublisher struct{ events []eventingapp.Event }

func (f *fakePublisher) Publish(_ context.Context, event eventingapp.Event) error {
	f.events = append(f.events, event)
	return nil
}

func newTestService(t *testing.T) (*Service, *fakeRepo, *fakeClient, *fakeAudit, *fakePublisher) {
	t.Helper()
	repo := newFakeRepo()
	client := &fakeClient{}
	audit := &fakeAudit{}
	publisher := &fakePublisher{}
	svc := New(
		repo,
		client,
		&seqIDs{},
		fixedClock{at: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)},
		audit,
		publisher,
	)
	return svc, repo, client, audit, publisher
}

func strPtr(value string) *string { return &value }

func intPtr(value int) *int { return &value }

func boolPtr(value bool) *bool { return &value }

func mapPtr(value map[string]string) *map[string]string { return &value }

func validCreate() ConfigInput {
	return ConfigInput{
		Name:    strPtr("Primary"),
		BaseURL: strPtr("http://127.0.0.1:13334/v1"),
	}
}

func apiError(t *testing.T, err error) *platform.Error {
	t.Helper()
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %v is not a platform error", err)
	}
	return apiErr
}

func assertNoSecret(t *testing.T, value any, secret string) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatalf("secret %q leaked in %s", secret, raw)
	}
}

func TestCreateConfigAppliesDefaultsAndRecordsAuditAndEvent(t *testing.T) {
	svc, repo, _, audit, publisher := newTestService(t)
	ctx := context.Background()

	input := validCreate()
	input.Name = strPtr("  Primary  ")
	input.BaseURL = strPtr("  http://127.0.0.1:13334/v1  ")
	input.APIKey = strPtr("sk-super-secret")
	input.IsDefault = boolPtr(true)

	cfg, err := svc.CreateConfig(ctx, input, 42)
	if err != nil {
		t.Fatalf("CreateConfig: %v", err)
	}
	if cfg.ID <= 0 {
		t.Fatalf("id = %d", cfg.ID)
	}
	if cfg.Name != "Primary" || cfg.BaseURL != "http://127.0.0.1:13334/v1" {
		t.Fatalf("normalized config = %#v", cfg)
	}
	if cfg.Provider != "openai" || cfg.Protocol != "openai" {
		t.Fatalf("provider/protocol = %q/%q", cfg.Provider, cfg.Protocol)
	}
	if cfg.TimeoutSeconds != 60 || cfg.MaxRetries != 2 || cfg.Status != "active" {
		t.Fatalf("defaults = %#v", cfg)
	}
	if !cfg.IsDefault || cfg.CreatedBy == nil || *cfg.CreatedBy != 42 {
		t.Fatalf("default/created_by = %#v", cfg)
	}
	if cfg.APIKey != "sk-super-secret" {
		t.Fatalf("application config must retain the raw key for internal use")
	}
	if cfg.CreatedAt.IsZero() || cfg.UpdatedAt.IsZero() {
		t.Fatalf("timestamps = %v/%v", cfg.CreatedAt, cfg.UpdatedAt)
	}
	if _, err := repo.GetConfig(ctx, cfg.ID); err != nil {
		t.Fatalf("config not persisted: %v", err)
	}

	if len(audit.events) != 1 {
		t.Fatalf("audit events = %d", len(audit.events))
	}
	entry := audit.events[0]
	if entry.Action != "llm.config_created" || entry.TargetKind != "llm_config" {
		t.Fatalf("audit = %#v", entry)
	}
	if entry.TargetID != formatID(cfg.ID) {
		t.Fatalf("audit target id = %q", entry.TargetID)
	}
	if entry.ActorID == nil || *entry.ActorID != 42 {
		t.Fatalf("audit actor = %v", entry.ActorID)
	}
	assertNoSecret(t, entry.Metadata, "sk-super-secret")

	if len(publisher.events) != 1 {
		t.Fatalf("events = %d", len(publisher.events))
	}
	event := publisher.events[0]
	if event.Channel != "system" || event.Type != "llm.config_created" {
		t.Fatalf("event = %#v", event)
	}
	if event.ActorID == nil || *event.ActorID != 42 {
		t.Fatalf("event actor = %v", event.ActorID)
	}
	if id, ok := event.Payload["id"].(string); !ok || id != formatID(cfg.ID) {
		t.Fatalf("event payload id = %#v", event.Payload["id"])
	}
	assertNoSecret(t, event.Payload, "sk-super-secret")
	assertNoSecret(t, event.Scope, "sk-super-secret")
}

func TestCreateConfigValidatesInput(t *testing.T) {
	longName := strings.Repeat("a", 101)
	cases := []struct {
		name   string
		mutate func(*ConfigInput)
	}{
		{"missing name", func(in *ConfigInput) { in.Name = nil }},
		{"blank name", func(in *ConfigInput) { in.Name = strPtr("   ") }},
		{"long name", func(in *ConfigInput) { in.Name = strPtr(longName) }},
		{"missing base url", func(in *ConfigInput) { in.BaseURL = nil }},
		{"relative base url", func(in *ConfigInput) { in.BaseURL = strPtr("not-a-url") }},
		{"unsupported scheme", func(in *ConfigInput) { in.BaseURL = strPtr("ftp://example.com/v1") }},
		{"bad protocol", func(in *ConfigInput) { in.Protocol = strPtr("anthropic") }},
		{"bad status", func(in *ConfigInput) { in.Status = strPtr("paused") }},
		{"timeout zero", func(in *ConfigInput) { in.TimeoutSeconds = intPtr(0) }},
		{"timeout too large", func(in *ConfigInput) { in.TimeoutSeconds = intPtr(601) }},
		{"retries negative", func(in *ConfigInput) { in.MaxRetries = intPtr(-1) }},
		{"retries too large", func(in *ConfigInput) { in.MaxRetries = intPtr(11) }},
		{"authorization header", func(in *ConfigInput) {
			in.Headers = mapPtr(map[string]string{"Authorization": "Bearer other"})
		}},
		{"blank header name", func(in *ConfigInput) {
			in.Headers = mapPtr(map[string]string{" ": "value"})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo, _, audit, publisher := newTestService(t)
			input := validCreate()
			tc.mutate(&input)
			if _, err := svc.CreateConfig(context.Background(), input, 1); err == nil {
				t.Fatal("CreateConfig accepted invalid input")
			} else if got := apiError(t, err).Status; got != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", got)
			}
			if len(repo.configs) != 0 || len(audit.events) != 0 || len(publisher.events) != 0 {
				t.Fatal("invalid input reached persistence or side effects")
			}
		})
	}
}

func TestCreateConfigRejectsDuplicateName(t *testing.T) {
	svc, repo, _, _, _ := newTestService(t)
	ctx := context.Background()
	if _, err := svc.CreateConfig(ctx, validCreate(), 1); err != nil {
		t.Fatalf("first CreateConfig: %v", err)
	}
	duplicate := validCreate()
	duplicate.Name = strPtr("primary")
	if _, err := svc.CreateConfig(ctx, duplicate, 1); err == nil {
		t.Fatal("duplicate name accepted")
	} else if got := apiError(t, err).Status; got != http.StatusConflict {
		t.Fatalf("status = %d, want 409", got)
	}
	if len(repo.configs) != 1 {
		t.Fatalf("configs = %d", len(repo.configs))
	}
}

func TestUpdateConfigKeepsUnsetFieldsAndClearsExplicitOnes(t *testing.T) {
	svc, repo, _, audit, publisher := newTestService(t)
	ctx := context.Background()

	create := validCreate()
	create.APIKey = strPtr("sk-old")
	create.Headers = mapPtr(map[string]string{"X-Org": "team-a"})
	create.TimeoutSeconds = intPtr(30)
	create.MaxRetries = intPtr(1)
	create.IsDefault = boolPtr(true)
	cfg, err := svc.CreateConfig(ctx, create, 42)
	if err != nil {
		t.Fatalf("CreateConfig: %v", err)
	}

	updated, err := svc.UpdateConfig(ctx, cfg.ID, ConfigInput{Name: strPtr("Renamed")}, 42)
	if err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}
	if updated.Name != "Renamed" {
		t.Fatalf("name = %q", updated.Name)
	}
	if updated.APIKey != "sk-old" {
		t.Fatalf("api key was not preserved: %q", updated.APIKey)
	}
	if updated.Headers["X-Org"] != "team-a" {
		t.Fatalf("headers were not preserved: %#v", updated.Headers)
	}
	if updated.TimeoutSeconds != 30 || updated.MaxRetries != 1 || !updated.IsDefault {
		t.Fatalf("scalar fields were not preserved: %#v", updated)
	}
	if len(audit.events) != 2 || audit.events[1].Action != "llm.config_updated" {
		t.Fatalf("audit = %#v", audit.events)
	}
	if len(publisher.events) != 2 || publisher.events[1].Type != "llm.config_updated" {
		t.Fatalf("events = %#v", publisher.events)
	}
	assertNoSecret(t, publisher.events[1].Payload, "sk-old")

	cleared, err := svc.UpdateConfig(ctx, cfg.ID, ConfigInput{
		APIKey:         strPtr(""),
		Headers:        mapPtr(map[string]string{}),
		TimeoutSeconds: intPtr(90),
		MaxRetries:     intPtr(0),
		IsDefault:      boolPtr(false),
		Status:         strPtr("disabled"),
	}, 42)
	if err != nil {
		t.Fatalf("clearing UpdateConfig: %v", err)
	}
	if cleared.APIKey != "" || len(cleared.Headers) != 0 {
		t.Fatalf("secrets were not cleared: %#v", cleared)
	}
	if cleared.TimeoutSeconds != 90 || cleared.MaxRetries != 0 || cleared.IsDefault || cleared.Status != "disabled" {
		t.Fatalf("explicit fields not applied: %#v", cleared)
	}
	stored, err := repo.GetConfig(ctx, cfg.ID)
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if stored.APIKey != "" || stored.MaxRetries != 0 {
		t.Fatalf("stored = %#v", stored)
	}
}

func TestUpdateConfigRejectsDuplicateNameAndMissingConfig(t *testing.T) {
	svc, _, _, _, _ := newTestService(t)
	ctx := context.Background()
	first, err := svc.CreateConfig(ctx, validCreate(), 1)
	if err != nil {
		t.Fatalf("CreateConfig: %v", err)
	}
	secondInput := validCreate()
	secondInput.Name = strPtr("Secondary")
	second, err := svc.CreateConfig(ctx, secondInput, 1)
	if err != nil {
		t.Fatalf("CreateConfig: %v", err)
	}
	if _, err := svc.UpdateConfig(ctx, second.ID, ConfigInput{Name: strPtr(first.Name)}, 1); err == nil {
		t.Fatal("duplicate name accepted")
	} else if got := apiError(t, err).Status; got != http.StatusConflict {
		t.Fatalf("status = %d, want 409", got)
	}
	if _, err := svc.UpdateConfig(ctx, 999, ConfigInput{Name: strPtr("Nope")}, 1); err == nil {
		t.Fatal("missing config accepted")
	} else if got := apiError(t, err).Status; got != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", got)
	}
}

func TestDeleteConfigRecordsAuditAndEvent(t *testing.T) {
	svc, repo, _, audit, publisher := newTestService(t)
	ctx := context.Background()
	cfg, err := svc.CreateConfig(ctx, validCreate(), 42)
	if err != nil {
		t.Fatalf("CreateConfig: %v", err)
	}
	if err := svc.DeleteConfig(ctx, cfg.ID, 42); err != nil {
		t.Fatalf("DeleteConfig: %v", err)
	}
	if _, err := repo.GetConfig(ctx, cfg.ID); !errors.Is(err, llm.ErrConfigNotFound) {
		t.Fatalf("config still present: %v", err)
	}
	if len(audit.events) != 2 || audit.events[1].Action != "llm.config_deleted" {
		t.Fatalf("audit = %#v", audit.events)
	}
	if audit.events[1].TargetID != formatID(cfg.ID) {
		t.Fatalf("audit target = %q", audit.events[1].TargetID)
	}
	if len(publisher.events) != 2 || publisher.events[1].Type != "llm.config_deleted" {
		t.Fatalf("events = %#v", publisher.events)
	}
	if err := svc.DeleteConfig(ctx, cfg.ID, 42); err == nil {
		t.Fatal("second delete succeeded")
	} else if got := apiError(t, err).Status; got != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", got)
	}
}

func TestListAndGetConfigs(t *testing.T) {
	svc, _, _, _, _ := newTestService(t)
	ctx := context.Background()
	first, err := svc.CreateConfig(ctx, validCreate(), 1)
	if err != nil {
		t.Fatalf("CreateConfig: %v", err)
	}
	secondInput := validCreate()
	secondInput.Name = strPtr("Secondary")
	second, err := svc.CreateConfig(ctx, secondInput, 1)
	if err != nil {
		t.Fatalf("CreateConfig: %v", err)
	}

	list, err := svc.ListConfigs(ctx)
	if err != nil {
		t.Fatalf("ListConfigs: %v", err)
	}
	if len(list) != 2 || list[0].ID != first.ID || list[1].ID != second.ID {
		t.Fatalf("list = %#v", list)
	}

	got, err := svc.GetConfig(ctx, second.ID)
	if err != nil || got.Name != "Secondary" {
		t.Fatalf("GetConfig = %#v, %v", got, err)
	}
	if _, err := svc.GetConfig(ctx, 999); err == nil {
		t.Fatal("missing config returned")
	} else if apiError(t, err).Status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", apiError(t, err).Status)
	}
}

func TestTestConfigUsesStoredConfigAndReturnsResult(t *testing.T) {
	svc, _, client, _, _ := newTestService(t)
	ctx := context.Background()
	input := validCreate()
	input.APIKey = strPtr("sk-test")
	cfg, err := svc.CreateConfig(ctx, input, 1)
	if err != nil {
		t.Fatalf("CreateConfig: %v", err)
	}
	client.testResult = TestResult{
		OK:        true,
		LatencyMS: 42,
		Models:    []RemoteModel{{ID: "gpt-4o", ContextWindow: 128000, MaxOutput: 16384, SupportsTools: true}},
	}

	result, err := svc.TestConfig(ctx, cfg.ID)
	if err != nil {
		t.Fatalf("TestConfig: %v", err)
	}
	if !result.OK || result.LatencyMS != 42 || len(result.Models) != 1 || result.Models[0].ID != "gpt-4o" {
		t.Fatalf("result = %#v", result)
	}
	if client.testCalls != 1 || client.lastConfig.ID != cfg.ID || client.lastConfig.APIKey != "sk-test" {
		t.Fatalf("client call = %#v", client)
	}
	if _, err := svc.TestConfig(ctx, 999); err == nil {
		t.Fatal("missing config tested")
	} else if apiError(t, err).Status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", apiError(t, err).Status)
	}
	if client.testCalls != 1 {
		t.Fatal("client was called for a missing config")
	}
}

func TestTestConfigPropagatesClientErrors(t *testing.T) {
	svc, _, client, _, _ := newTestService(t)
	cfg, err := svc.CreateConfig(context.Background(), validCreate(), 1)
	if err != nil {
		t.Fatalf("CreateConfig: %v", err)
	}
	client.testErr = errors.New("dial tcp: connection refused")
	if _, err := svc.TestConfig(context.Background(), cfg.ID); err == nil {
		t.Fatal("client error was swallowed")
	}
}

func TestRefreshModelsUpsertsCatalogModels(t *testing.T) {
	svc, repo, client, _, _ := newTestService(t)
	ctx := context.Background()
	cfg, err := svc.CreateConfig(ctx, validCreate(), 1)
	if err != nil {
		t.Fatalf("CreateConfig: %v", err)
	}

	client.models = []RemoteModel{
		{ID: "gpt-4o", ContextWindow: 128000, MaxOutput: 16384, SupportsTools: true, SupportsVision: true},
		{ID: "gpt-4o-mini"},
	}
	first, err := svc.RefreshModels(ctx, cfg.ID, 7)
	if err != nil {
		t.Fatalf("RefreshModels: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("models = %#v", first)
	}
	for _, model := range first {
		if model.ConfigID != cfg.ID || model.Source != "catalog" || model.Status != "active" {
			t.Fatalf("model = %#v", model)
		}
		if model.ID <= 0 || model.UpdatedAt.IsZero() {
			t.Fatalf("model id/timestamps = %#v", model)
		}
	}
	if first[0].Model != "gpt-4o" || first[0].ContextWindow != 128000 || !first[0].SupportsTools {
		t.Fatalf("first model = %#v", first[0])
	}
	if repo.upsertCalls != 1 {
		t.Fatalf("upsert calls = %d", repo.upsertCalls)
	}

	client.models = []RemoteModel{
		{ID: "gpt-4o", ContextWindow: 200000, SupportsTools: true},
		{ID: "o3"},
	}
	second, err := svc.RefreshModels(ctx, cfg.ID, 7)
	if err != nil {
		t.Fatalf("second RefreshModels: %v", err)
	}
	if len(second) != 3 {
		t.Fatalf("models after refresh = %#v", second)
	}
	if second[0].ID != first[0].ID || second[0].Model != "gpt-4o" || second[0].ContextWindow != 200000 {
		t.Fatalf("upsert did not update in place: %#v", second[0])
	}
	if repo.upsertCalls != 2 {
		t.Fatalf("upsert calls = %d", repo.upsertCalls)
	}
}

func TestRefreshModelsMapsUpstreamFailure(t *testing.T) {
	svc, repo, client, _, _ := newTestService(t)
	cfg, err := svc.CreateConfig(context.Background(), validCreate(), 1)
	if err != nil {
		t.Fatalf("CreateConfig: %v", err)
	}
	client.listErr = errors.New("llm provider returned HTTP 500")
	if _, err := svc.RefreshModels(context.Background(), cfg.ID, 1); err == nil {
		t.Fatal("refresh failure was swallowed")
	} else {
		apiErr := apiError(t, err)
		if apiErr.Status != http.StatusServiceUnavailable || !strings.Contains(apiErr.Message, "HTTP 500") {
			t.Fatalf("error = %#v", apiErr)
		}
	}
	if repo.upsertCalls != 0 {
		t.Fatalf("failed refresh persisted models: %d", repo.upsertCalls)
	}
	if _, err := svc.RefreshModels(context.Background(), 999, 1); err == nil {
		t.Fatal("refresh of missing config succeeded")
	} else if apiError(t, err).Status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", apiError(t, err).Status)
	}
}

func TestAddModelValidatesAndRejectsDuplicates(t *testing.T) {
	svc, _, _, _, _ := newTestService(t)
	ctx := context.Background()
	cfg, err := svc.CreateConfig(ctx, validCreate(), 1)
	if err != nil {
		t.Fatalf("CreateConfig: %v", err)
	}

	model, err := svc.AddModel(ctx, cfg.ID, ModelInput{
		Model: "gpt-4o", ContextWindow: 128000, MaxOutput: 16384,
		SupportsTools: true, SupportsReasoning: true, SupportsPromptCache: true,
	}, 7)
	if err != nil {
		t.Fatalf("AddModel: %v", err)
	}
	if model.ID <= 0 || model.ConfigID != cfg.ID || model.Model != "gpt-4o" || model.DisplayName != "gpt-4o" {
		t.Fatalf("model = %#v", model)
	}
	if model.Source != "manual" || model.Status != "active" || !model.SupportsTools || model.SupportsVision {
		t.Fatalf("model flags = %#v", model)
	}
	if !model.Capabilities.SupportsReasoning || !model.Capabilities.SupportsPromptCache ||
		model.Capabilities.ContextWindow != 128000 {
		t.Fatalf("capabilities = %#v", model.Capabilities)
	}

	if _, err := svc.AddModel(ctx, cfg.ID, ModelInput{Model: "gpt-4o", ContextWindow: 1, MaxOutput: 1}, 7); err == nil {
		t.Fatal("duplicate model accepted")
	} else if apiError(t, err).Status != http.StatusConflict {
		t.Fatalf("status = %d, want 409", apiError(t, err).Status)
	}

	cases := []struct {
		name          string
		configID      int64
		model         string
		contextWindow int
		maxOutput     int
		wantStatus    int
	}{
		{"missing config", 999, "m", 1, 1, http.StatusNotFound},
		{"blank model", cfg.ID, "   ", 1, 1, http.StatusBadRequest},
		{"long model", cfg.ID, strings.Repeat("m", 201), 1, 1, http.StatusBadRequest},
		{"negative context", cfg.ID, "m", -1, 1, http.StatusBadRequest},
		{"negative max output", cfg.ID, "m", 1, -1, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := ModelInput{Model: tc.model, ContextWindow: tc.contextWindow, MaxOutput: tc.maxOutput}
			if _, err := svc.AddModel(ctx, tc.configID, input, 1); err == nil {
				t.Fatal("invalid model accepted")
			} else if got := apiError(t, err).Status; got != tc.wantStatus {
				t.Fatalf("status = %d, want %d", got, tc.wantStatus)
			}
		})
	}
}

func TestDeleteModelAndListModels(t *testing.T) {
	svc, _, _, _, _ := newTestService(t)
	ctx := context.Background()
	first, err := svc.CreateConfig(ctx, validCreate(), 1)
	if err != nil {
		t.Fatalf("CreateConfig: %v", err)
	}
	secondInput := validCreate()
	secondInput.Name = strPtr("Secondary")
	second, err := svc.CreateConfig(ctx, secondInput, 1)
	if err != nil {
		t.Fatalf("CreateConfig: %v", err)
	}
	modelA, err := svc.AddModel(ctx, first.ID, ModelInput{Model: "gpt-4o", ContextWindow: 128000, MaxOutput: 16384, SupportsTools: true, SupportsVision: true}, 1)
	if err != nil {
		t.Fatalf("AddModel: %v", err)
	}
	if _, err := svc.AddModel(ctx, second.ID, ModelInput{Model: "claude", ContextWindow: 200000, MaxOutput: 8192, SupportsVision: true}, 1); err != nil {
		t.Fatalf("AddModel: %v", err)
	}

	all, err := svc.ListModels(ctx)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(all) != 2 || all[0].ID != modelA.ID || all[1].ConfigID != second.ID {
		t.Fatalf("models = %#v", all)
	}

	if err := svc.DeleteModel(ctx, modelA.ID, 1); err != nil {
		t.Fatalf("DeleteModel: %v", err)
	}
	remaining, err := svc.ListModels(ctx)
	if err != nil || len(remaining) != 1 || remaining[0].ConfigID != second.ID {
		t.Fatalf("remaining = %#v, %v", remaining, err)
	}
	if err := svc.DeleteModel(context.Background(), modelA.ID, 1); err == nil {
		t.Fatal("second delete succeeded")
	} else if apiError(t, err).Status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", apiError(t, err).Status)
	}
}

func TestDefaultConfigStaysUnique(t *testing.T) {
	svc, repo, _, _, _ := newTestService(t)
	ctx := context.Background()

	firstInput := validCreate()
	firstInput.IsDefault = boolPtr(true)
	first, err := svc.CreateConfig(ctx, firstInput, 1)
	if err != nil {
		t.Fatalf("CreateConfig: %v", err)
	}
	secondInput := validCreate()
	secondInput.Name = strPtr("Secondary")
	secondInput.IsDefault = boolPtr(true)
	second, err := svc.CreateConfig(ctx, secondInput, 1)
	if err != nil {
		t.Fatalf("CreateConfig: %v", err)
	}

	storedFirst, err := repo.GetConfig(ctx, first.ID)
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if storedFirst.IsDefault {
		t.Fatal("first config is still the default after promoting the second")
	}
	storedSecond, err := repo.GetConfig(ctx, second.ID)
	if err != nil || !storedSecond.IsDefault {
		t.Fatalf("second config default = %#v, %v", storedSecond, err)
	}

	if _, err := svc.UpdateConfig(ctx, first.ID, ConfigInput{IsDefault: boolPtr(true)}, 1); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}
	storedSecond, err = repo.GetConfig(ctx, second.ID)
	if err != nil || storedSecond.IsDefault {
		t.Fatalf("demoted config = %#v, %v", storedSecond, err)
	}
	dflt := 0
	list, _ := repo.ListConfigs(ctx)
	for _, cfg := range list {
		if cfg.IsDefault {
			dflt++
		}
	}
	if dflt != 1 {
		t.Fatalf("default configs = %d, want 1", dflt)
	}
}

// TestConfigRequiresDefaultWhenModelsExist pins the dispatch-ready invariant:
// once a configuration owns a selectable model, create/update must name a
// default_model_id, otherwise the config is persistable but the agent runtime
// cannot resolve a model. A config without models may still be created before
// its catalog is refreshed (Task 17 carve-out), and a catalog whose models are
// all deprecated does not require a default either.
func TestConfigRequiresDefaultWhenModelsExist(t *testing.T) {
	ctx := context.Background()
	svc, repo, _, _, _ := newTestService(t)

	// Task 17 carve-out: a config with no models may be created (and updated)
	// before its catalog is refreshed.
	cfg, err := svc.CreateConfig(ctx, validCreate(), 1)
	if err != nil {
		t.Fatalf("CreateConfig without models: %v", err)
	}
	if _, err := svc.UpdateConfig(ctx, cfg.ID, ConfigInput{Name: strPtr("Primary")}, 1); err != nil {
		t.Fatalf("UpdateConfig without models: %v", err)
	}

	model, err := svc.AddModel(ctx, cfg.ID, ModelInput{Model: "gpt-4o", ContextWindow: 128000, MaxOutput: 8192, SupportsTools: true, SupportsVision: true}, 1)
	if err != nil {
		t.Fatalf("AddModel: %v", err)
	}

	assertDefaultRequired := func(t *testing.T, err error) {
		t.Helper()
		apiErr := apiError(t, err)
		if apiErr.Status != http.StatusBadRequest || apiErr.Code != CodeLLMConfigInvalid || apiErr.Retryable {
			t.Fatalf("error = %#v, want non-retryable %s", apiErr, CodeLLMConfigInvalid)
		}
		if apiErr.Details["field"] != "default_model_id" {
			t.Fatalf("error details = %#v, want field=default_model_id", apiErr.Details)
		}
	}

	// With a selectable model but no default, a partial update must be rejected.
	if _, err := svc.UpdateConfig(ctx, cfg.ID, ConfigInput{Name: strPtr("Renamed")}, 1); err == nil {
		t.Fatal("UpdateConfig accepted a config with selectable models and no default")
	} else {
		assertDefaultRequired(t, err)
	}
	stored, err := repo.GetConfig(ctx, cfg.ID)
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if stored.Name != "Primary" {
		t.Fatalf("rejected update was persisted: name = %q", stored.Name)
	}

	// An explicit clear is the same violation.
	zero := int64(0)
	if _, err := svc.UpdateConfig(ctx, cfg.ID, ConfigInput{DefaultModelID: &zero}, 1); err == nil {
		t.Fatal("UpdateConfig accepted an explicit default_model_id clear")
	} else {
		assertDefaultRequired(t, err)
	}

	// Naming the model satisfies the rule.
	updated, err := svc.UpdateConfig(ctx, cfg.ID, ConfigInput{
		Name:           strPtr("Renamed"),
		DefaultModelID: &model.ID,
	}, 1)
	if err != nil {
		t.Fatalf("UpdateConfig with default model: %v", err)
	}
	if updated.DefaultModelID != model.ID {
		t.Fatalf("default_model_id = %d, want %d", updated.DefaultModelID, model.ID)
	}

	// A catalog whose models are all deprecated has nothing selectable, so the
	// admin may clear the default until the catalog is refreshed.
	model.Deprecated = true
	repo.models[model.ID] = model
	cleared, err := svc.UpdateConfig(ctx, cfg.ID, ConfigInput{DefaultModelID: &zero}, 1)
	if err != nil {
		t.Fatalf("UpdateConfig deprecated-only catalog: %v", err)
	}
	if cleared.DefaultModelID != 0 {
		t.Fatalf("default_model_id = %d, want cleared", cleared.DefaultModelID)
	}
}

func formatID(value int64) string {
	return strconv.FormatInt(value, 10)
}

// TestVisionModelCannotBeDefault covers R-E2-2 (spec §4.25): a model with
// supports_vision=false can never be the default model, because document
// images and standalone image drafts would then be accepted by the catalog and
// fail only at dispatch. A vision-capable model is accepted.
func TestVisionModelCannotBeDefault(t *testing.T) {
	ctx := context.Background()
	svc, repo, _, _, _ := newTestService(t)

	cfg, err := svc.CreateConfig(ctx, validCreate(), 1)
	if err != nil {
		t.Fatalf("CreateConfig: %v", err)
	}
	textOnly, err := svc.AddModel(ctx, cfg.ID, ModelInput{
		Model: "text-only", ContextWindow: 128000, MaxOutput: 8192, SupportsTools: true,
	}, 1)
	if err != nil {
		t.Fatalf("AddModel(text-only): %v", err)
	}
	visionModel, err := svc.AddModel(ctx, cfg.ID, ModelInput{
		Model: "vision-llm", ContextWindow: 128000, MaxOutput: 8192, SupportsTools: true, SupportsVision: true,
	}, 1)
	if err != nil {
		t.Fatalf("AddModel(vision): %v", err)
	}

	rejected, err := svc.UpdateConfig(ctx, cfg.ID, ConfigInput{DefaultModelID: &textOnly.ID}, 1)
	if err == nil {
		t.Fatalf("non-vision default accepted: %#v", rejected)
	}
	apiErr := apiError(t, err)
	if apiErr.Status != http.StatusBadRequest || apiErr.Code != "invalid_argument" || apiErr.Retryable {
		t.Fatalf("error = %#v, want non-retryable 400 invalid_argument", apiErr)
	}
	if field, _ := apiErr.Details["field"].(string); field != "default_model_id" {
		t.Fatalf("details = %#v, want field=default_model_id", apiErr.Details)
	}
	if stored, getErr := repo.GetConfig(ctx, cfg.ID); getErr != nil || stored.DefaultModelID != 0 {
		t.Fatalf("rejected default persisted: %#v, %v", stored, getErr)
	}

	updated, err := svc.UpdateConfig(ctx, cfg.ID, ConfigInput{DefaultModelID: &visionModel.ID}, 1)
	if err != nil {
		t.Fatalf("vision default rejected: %v", err)
	}
	if updated.DefaultModelID != visionModel.ID {
		t.Fatalf("default_model_id = %d, want %d", updated.DefaultModelID, visionModel.ID)
	}
}

func int64Ptr(value int64) *int64 { return &value }

// TestPatchModelSetsManualSource pins R-A1-2/R-A1-8: writing context_window or
// max_output through the model-parameter patch stores the value, marks that
// field's provenance as manual and derives max_input = W - O for the response.
func TestPatchModelSetsManualSource(t *testing.T) {
	svc, repo, _, _, _ := newTestService(t)
	ctx := context.Background()
	repo.models[2] = llm.Model{
		ID: 2, ConfigID: 1, Model: "gpt-4o", DisplayName: "gpt-4o",
		ContextWindow: 64000, MaxOutput: 4096,
		Source: llm.SourceCatalog, Status: llm.ModelStatusActive,
		WindowSource: llm.SourceCatalog, OutputSource: llm.SourceCatalog,
		Capabilities: llm.Capabilities{
			ContextWindow:        64000,
			CatalogContextWindow: 128000,
			CatalogMaxOutput:     16384,
		},
	}

	window, output := int64(128000), int64(8192)
	got, err := svc.UpdateModelParams(ctx, 1, 2, UpdateModelParams{ContextWindow: &window, MaxOutput: &output})
	if err != nil {
		t.Fatalf("UpdateModelParams: %v", err)
	}
	if got.WindowSource != llm.SourceManual || got.OutputSource != llm.SourceManual {
		t.Fatalf("sources = %q/%q, want manual/manual", got.WindowSource, got.OutputSource)
	}
	if got.MaxInput != 119808 {
		t.Fatalf("max_input = %d, want 119808", got.MaxInput)
	}

	stored := repo.models[2]
	t.Logf("stored after patch: %+v", stored)
	if stored.ContextWindow != 128000 || stored.MaxOutput != 8192 {
		t.Fatalf("stored window/output = %d/%d, want 128000/8192", stored.ContextWindow, stored.MaxOutput)
	}
	if stored.WindowSource != llm.SourceManual || stored.OutputSource != llm.SourceManual {
		t.Fatalf("stored sources = %q/%q, want manual/manual", stored.WindowSource, stored.OutputSource)
	}
	// The legacy capabilities.context_window mirror stays in sync (A §4.2).
	if stored.Capabilities.ContextWindow != 128000 {
		t.Fatalf("capabilities.context_window = %d, want 128000", stored.Capabilities.ContextWindow)
	}
	// Patching does not discard the last upstream snapshot.
	if stored.Capabilities.CatalogContextWindow != 128000 || stored.Capabilities.CatalogMaxOutput != 16384 {
		t.Fatalf("upstream snapshot = %#v", stored.Capabilities)
	}
}

// TestPatchOneSideKeepsOtherCatalogSource pins the one-sided patch success
// path: changing only max_output marks that field manual and must leave the
// catalog-sourced context_window (and its provenance) untouched.
func TestPatchOneSideKeepsOtherCatalogSource(t *testing.T) {
	svc, repo, _, _, _ := newTestService(t)
	ctx := context.Background()
	repo.models[2] = llm.Model{
		ID: 2, ConfigID: 1, Model: "gpt-4o", DisplayName: "gpt-4o",
		ContextWindow: 128000, MaxOutput: 8192,
		Source: llm.SourceCatalog, Status: llm.ModelStatusActive,
		WindowSource: llm.SourceCatalog, OutputSource: llm.SourceCatalog,
		Capabilities: llm.Capabilities{
			ContextWindow:        128000,
			CatalogContextWindow: 128000,
			CatalogMaxOutput:     16384,
		},
	}

	got, err := svc.UpdateModelParams(ctx, 1, 2, UpdateModelParams{MaxOutput: int64Ptr(4096)})
	if err != nil {
		t.Fatalf("UpdateModelParams: %v", err)
	}
	if got.WindowSource != llm.SourceCatalog || got.OutputSource != llm.SourceManual {
		t.Fatalf("sources = %q/%q, want catalog/manual", got.WindowSource, got.OutputSource)
	}
	if got.ContextWindow != 128000 || got.MaxOutput != 4096 {
		t.Fatalf("params = %d/%d, want 128000/4096", got.ContextWindow, got.MaxOutput)
	}
	if got.MaxInput != 123904 {
		t.Fatalf("max_input = %d, want 123904 (128000-4096)", got.MaxInput)
	}

	stored := repo.models[2]
	if stored.ContextWindow != 128000 || stored.MaxOutput != 4096 {
		t.Fatalf("stored params = %d/%d, want 128000/4096", stored.ContextWindow, stored.MaxOutput)
	}
	if stored.WindowSource != llm.SourceCatalog || stored.OutputSource != llm.SourceManual {
		t.Fatalf("stored sources = %q/%q, want catalog/manual", stored.WindowSource, stored.OutputSource)
	}
}

// TestRefreshPreservesManualCapabilities pins S-02: a capability an
// administrator corrected by hand survives an upstream refresh, while every
// catalog-sourced capability (and the numeric parameters) still follows the
// upstream. It also proves the refreshed-default vision guard evaluates the
// merged capability set instead of the raw upstream one.
func TestRefreshPreservesManualCapabilities(t *testing.T) {
	svc, repo, client, audit, _ := newTestService(t)
	ctx := context.Background()
	input := validCreate()
	input.APIKey = strPtr("sk-refresh-secret")
	cfg, err := svc.CreateConfig(ctx, input, 1)
	if err != nil {
		t.Fatalf("CreateConfig: %v", err)
	}

	client.models = []RemoteModel{{
		ID: "gpt-4o", ContextWindow: 128000, MaxOutput: 8192,
	}}
	refreshed, err := svc.RefreshModels(ctx, cfg.ID, 7)
	if err != nil {
		t.Fatalf("first RefreshModels: %v", err)
	}
	modelID := refreshed[0].ID
	if refreshed[0].EffectiveCapabilities().SupportsVisionSource != llm.SourceCatalog {
		t.Fatalf("new catalog capability source = %#v", refreshed[0].Capabilities)
	}

	// The gateway did not advertise vision; the administrator corrects it.
	updated, err := svc.UpdateModel(ctx, cfg.ID, modelID, ModelPatch{SupportsVision: boolPtr(true)}, 7)
	if err != nil {
		t.Fatalf("UpdateModel: %v", err)
	}
	if !updated.SupportsVision || updated.EffectiveCapabilities().SupportsVisionSource != llm.SourceManual {
		t.Fatalf("manual vision = %#v", updated)
	}
	if _, err := svc.UpdateConfig(ctx, cfg.ID, ConfigInput{DefaultModelID: &modelID}, 7); err != nil {
		t.Fatalf("set default model: %v", err)
	}

	// The next refresh still reports no vision (the raw upstream value), but
	// updates tools/reasoning/window. Merging must keep the manual vision, and
	// the default-model guard must accept the merged value.
	client.models = []RemoteModel{{
		ID: "gpt-4o", ContextWindow: 200000, MaxOutput: 16384,
		SupportsTools: true, SupportsReasoning: true, SupportsPromptCache: true,
	}}
	merged, err := svc.RefreshModels(ctx, cfg.ID, 7)
	if err != nil {
		t.Fatalf("second RefreshModels rejected merged manual vision: %v", err)
	}
	model := merged[0]
	capabilities := model.EffectiveCapabilities()
	if !capabilities.SupportsVision || capabilities.SupportsVisionSource != llm.SourceManual {
		t.Fatalf("manual vision was overwritten: %#v", capabilities)
	}
	if !capabilities.SupportsTools || capabilities.SupportsToolsSource != llm.SourceCatalog {
		t.Fatalf("catalog tools not refreshed: %#v", capabilities)
	}
	if !capabilities.SupportsReasoning || capabilities.SupportsReasoningSource != llm.SourceCatalog {
		t.Fatalf("catalog reasoning not refreshed: %#v", capabilities)
	}
	if !capabilities.SupportsPromptCache || capabilities.SupportsPromptCacheSource != llm.SourceCatalog {
		t.Fatalf("catalog prompt cache not refreshed: %#v", capabilities)
	}
	if model.ContextWindow != 200000 || model.MaxOutput != 16384 {
		t.Fatalf("numeric parameters were not refreshed: %d/%d", model.ContextWindow, model.MaxOutput)
	}

	stored := repo.models[modelID]
	if !stored.SupportsVision || stored.EffectiveCapabilities().SupportsVisionSource != llm.SourceManual {
		t.Fatalf("stored manual vision = %#v", stored)
	}

	actions := make(map[string]bool, len(audit.events))
	for _, event := range audit.events {
		actions[event.Action] = true
	}
	if !actions["llm.model_updated"] || !actions["llm.config_refreshed"] {
		t.Fatalf("audit actions = %v, want model_updated and config_refreshed", actions)
	}
	assertNoSecret(t, audit.events, "sk-refresh-secret")
}

// TestAddModelMarksCapabilitiesManual pins S-02 for manual rows: every
// capability supplied through AddModel is administrator input, so it keeps its
// value when an upstream refresh later reports the same model name.
func TestAddModelMarksCapabilitiesManual(t *testing.T) {
	svc, _, client, audit, _ := newTestService(t)
	ctx := context.Background()
	cfg, err := svc.CreateConfig(ctx, validCreate(), 1)
	if err != nil {
		t.Fatalf("CreateConfig: %v", err)
	}

	model, err := svc.AddModel(ctx, cfg.ID, ModelInput{
		Model: "gpt-4o", ContextWindow: 128000, MaxOutput: 8192,
		SupportsTools: true, SupportsVision: false,
	}, 7)
	if err != nil {
		t.Fatalf("AddModel: %v", err)
	}
	capabilities := model.EffectiveCapabilities()
	if capabilities.SupportsToolsSource != llm.SourceManual ||
		capabilities.SupportsVisionSource != llm.SourceManual ||
		capabilities.SupportsReasoningSource != llm.SourceManual ||
		capabilities.SupportsPromptCacheSource != llm.SourceManual {
		t.Fatalf("manual model capability sources = %#v", capabilities)
	}

	client.models = []RemoteModel{{
		ID: "gpt-4o", ContextWindow: 200000, MaxOutput: 16384,
		SupportsTools: false, SupportsVision: true, SupportsReasoning: true,
	}}
	refreshed, err := svc.RefreshModels(ctx, cfg.ID, 7)
	if err != nil {
		t.Fatalf("RefreshModels: %v", err)
	}
	after := refreshed[0].EffectiveCapabilities()
	if !after.SupportsTools || after.SupportsVision {
		t.Fatalf("manual capability values were overwritten: %#v", after)
	}
	if after.SupportsToolsSource != llm.SourceManual || after.SupportsVisionSource != llm.SourceManual {
		t.Fatalf("manual capability sources were reset: %#v", after)
	}

	var added *auditapp.Event
	for i := range audit.events {
		if audit.events[i].Action == "llm.model_added" {
			added = &audit.events[i]
		}
	}
	if added == nil {
		t.Fatalf("audit actions = %#v, want llm.model_added", audit.events)
	}
	if added.TargetKind != "llm_model" || added.TargetID != formatID(model.ID) {
		t.Fatalf("model audit target = %#v", added)
	}
	if added.ActorID == nil || *added.ActorID != 7 {
		t.Fatalf("model audit actor = %v", added.ActorID)
	}
	if added.Metadata["model"] != "gpt-4o" || added.Metadata["config_id"] != formatID(cfg.ID) {
		t.Fatalf("model audit metadata = %#v", added.Metadata)
	}
}

// TestUpdateModelMarksSuppliedCapabilitiesManual pins the per-field provenance
// of the capability editor and the changed-fields audit trail (S-02/S-05): only
// the supplied capability becomes manual, the others stay catalog, and the
// audit entry records the old/new values and resulting sources.
func TestUpdateModelMarksSuppliedCapabilitiesManual(t *testing.T) {
	svc, repo, _, audit, _ := newTestService(t)
	ctx := context.Background()
	cfg, err := svc.CreateConfig(ctx, validCreate(), 1)
	if err != nil {
		t.Fatalf("CreateConfig: %v", err)
	}
	repo.models[50] = llm.Model{
		ID: 50, ConfigID: cfg.ID, Model: "gpt-4o", DisplayName: "gpt-4o",
		ContextWindow: 128000, MaxOutput: 8192,
		Source: llm.SourceCatalog, Status: llm.ModelStatusActive,
		WindowSource: llm.SourceCatalog, OutputSource: llm.SourceCatalog,
		Capabilities: llm.Capabilities{
			SupportsTools:             true,
			SupportsVisionSource:      llm.SourceCatalog,
			SupportsToolsSource:       llm.SourceCatalog,
			SupportsReasoningSource:   llm.SourceCatalog,
			SupportsPromptCacheSource: llm.SourceCatalog,
			ContextWindow:             128000,
		},
	}

	updated, err := svc.UpdateModel(ctx, cfg.ID, 50, ModelPatch{SupportsVision: boolPtr(true)}, 7)
	if err != nil {
		t.Fatalf("UpdateModel: %v", err)
	}
	capabilities := updated.EffectiveCapabilities()
	if !capabilities.SupportsVision || capabilities.SupportsVisionSource != llm.SourceManual {
		t.Fatalf("vision = %#v, want manual true", capabilities)
	}
	if capabilities.SupportsToolsSource != llm.SourceCatalog ||
		capabilities.SupportsReasoningSource != llm.SourceCatalog ||
		capabilities.SupportsPromptCacheSource != llm.SourceCatalog {
		t.Fatalf("unsupplied capability sources = %#v", capabilities)
	}

	var entry *auditapp.Event
	for i := range audit.events {
		if audit.events[i].Action == "llm.model_updated" {
			entry = &audit.events[i]
		}
	}
	if entry == nil {
		t.Fatalf("audit actions = %#v, want llm.model_updated", audit.events)
	}
	if entry.TargetKind != "llm_model" || entry.TargetID != "50" {
		t.Fatalf("audit target = %#v", entry)
	}
	if entry.ActorID == nil || *entry.ActorID != 7 {
		t.Fatalf("audit actor = %v", entry.ActorID)
	}
	changed, _ := entry.Metadata["changed_fields"].([]string)
	if !containsString(changed, "supports_vision") || !containsString(changed, "supports_vision_source") {
		t.Fatalf("changed fields = %#v", entry.Metadata["changed_fields"])
	}
	oldValues, _ := entry.Metadata["old_values"].(map[string]any)
	newValues, _ := entry.Metadata["new_values"].(map[string]any)
	if oldValues["supports_vision"] != false || newValues["supports_vision"] != true {
		t.Fatalf("old/new values = %#v/%#v", oldValues, newValues)
	}
	sources, _ := entry.Metadata["capability_sources"].(map[string]any)
	if sources["supports_vision"] != llm.SourceManual || sources["supports_tools"] != llm.SourceCatalog {
		t.Fatalf("capability sources = %#v", sources)
	}
	assertNoSecret(t, entry.Metadata, "sk-")
}

// TestDeleteModelRejectsConfiguredDefault pins the M-07 guard: deleting the
// model that is the configuration's default bypasses default-model validation,
// so it is a typed 409 that tells the administrator to reassign first. A
// successful delete records llm.model_deleted with the model metadata.
func TestDeleteModelRejectsConfiguredDefault(t *testing.T) {
	svc, repo, _, audit, _ := newTestService(t)
	ctx := context.Background()
	cfg, err := svc.CreateConfig(ctx, validCreate(), 1)
	if err != nil {
		t.Fatalf("CreateConfig: %v", err)
	}
	defaultModel, err := svc.AddModel(ctx, cfg.ID, ModelInput{
		Model: "vision-llm", ContextWindow: 128000, MaxOutput: 8192, SupportsVision: true,
	}, 1)
	if err != nil {
		t.Fatalf("AddModel default: %v", err)
	}
	other, err := svc.AddModel(ctx, cfg.ID, ModelInput{
		Model: "other-llm", ContextWindow: 128000, MaxOutput: 8192, SupportsVision: true,
	}, 1)
	if err != nil {
		t.Fatalf("AddModel other: %v", err)
	}
	if _, err := svc.UpdateConfig(ctx, cfg.ID, ConfigInput{DefaultModelID: &defaultModel.ID}, 1); err != nil {
		t.Fatalf("set default: %v", err)
	}

	err = svc.DeleteModel(ctx, defaultModel.ID, 7)
	if err == nil {
		t.Fatal("deleting the configured default model succeeded")
	}
	apiErr := apiError(t, err)
	if apiErr.Status != http.StatusConflict || apiErr.Code != "default_model_in_use" {
		t.Fatalf("error = %#v, want 409 default_model_in_use", apiErr)
	}
	if apiErr.Details["config_id"] != formatID(cfg.ID) ||
		apiErr.Details["config_name"] != cfg.Name ||
		apiErr.Details["default_model_id"] != formatID(defaultModel.ID) {
		t.Fatalf("details = %#v", apiErr.Details)
	}
	if next, _ := apiErr.Details["next_action"].(string); next == "" {
		t.Fatalf("details = %#v, want a reassign-first next_action", apiErr.Details)
	}
	if _, ok := repo.models[defaultModel.ID]; !ok {
		t.Fatal("rejected delete removed the default model")
	}

	// Reassigning the default unlocks the delete.
	if _, err := svc.UpdateConfig(ctx, cfg.ID, ConfigInput{DefaultModelID: &other.ID}, 1); err != nil {
		t.Fatalf("reassign default: %v", err)
	}
	if err := svc.DeleteModel(ctx, defaultModel.ID, 7); err != nil {
		t.Fatalf("DeleteModel after reassign: %v", err)
	}
	var entry *auditapp.Event
	for i := range audit.events {
		if audit.events[i].Action == "llm.model_deleted" {
			entry = &audit.events[i]
		}
	}
	if entry == nil {
		t.Fatalf("audit actions = %#v, want llm.model_deleted", audit.events)
	}
	if entry.TargetID != formatID(defaultModel.ID) || entry.Metadata["model"] != "vision-llm" {
		t.Fatalf("delete audit = %#v", entry)
	}
	if entry.ActorID == nil || *entry.ActorID != 7 {
		t.Fatalf("delete audit actor = %v", entry.ActorID)
	}
}

// TestRefreshPreservesManual pins R-A1-3: an upstream refresh only fills fields
// whose source is catalog (or whose stored value is 0) and never overwrites an
// administrator's value. The upstream snapshot is still refreshed so
// reset-params can restore the latest catalog value.
func TestRefreshPreservesManual(t *testing.T) {
	svc, repo, client, _, _ := newTestService(t)
	ctx := context.Background()
	cfg, err := svc.CreateConfig(ctx, validCreate(), 1)
	if err != nil {
		t.Fatalf("CreateConfig: %v", err)
	}

	repo.models[10] = llm.Model{
		ID: 10, ConfigID: cfg.ID, Model: "manual-model", DisplayName: "manual-model",
		ContextWindow: 128000, MaxOutput: 8192,
		Source: llm.SourceManual, Status: llm.ModelStatusActive,
		WindowSource: llm.SourceManual, OutputSource: llm.SourceManual,
		Capabilities: llm.Capabilities{ContextWindow: 128000},
	}
	repo.models[11] = llm.Model{
		ID: 11, ConfigID: cfg.ID, Model: "catalog-model", DisplayName: "catalog-model",
		ContextWindow: 64000, MaxOutput: 4096,
		Source: llm.SourceCatalog, Status: llm.ModelStatusActive,
		WindowSource: llm.SourceCatalog, OutputSource: llm.SourceCatalog,
		Capabilities: llm.Capabilities{ContextWindow: 64000},
	}
	client.models = []RemoteModel{
		{ID: "manual-model", ContextWindow: 1000000, MaxOutput: 393216},
		{ID: "catalog-model", ContextWindow: 200000, MaxOutput: 32000},
	}

	models, err := svc.RefreshModels(ctx, cfg.ID, 1)
	if err != nil {
		t.Fatalf("RefreshModels: %v", err)
	}
	byName := make(map[string]llm.Model, len(models))
	for _, model := range models {
		byName[model.Model] = model
		t.Logf("refreshed model: %+v", model)
	}

	manual := byName["manual-model"]
	if manual.ContextWindow != 128000 || manual.MaxOutput != 8192 {
		t.Fatalf("manual params overwritten: %d/%d", manual.ContextWindow, manual.MaxOutput)
	}
	if manual.WindowSource != llm.SourceManual || manual.OutputSource != llm.SourceManual {
		t.Fatalf("manual sources = %q/%q", manual.WindowSource, manual.OutputSource)
	}
	if manual.Capabilities.ContextWindow != 128000 {
		t.Fatalf("manual capabilities.context_window = %d, want 128000", manual.Capabilities.ContextWindow)
	}
	if manual.Capabilities.CatalogContextWindow != 1000000 || manual.Capabilities.CatalogMaxOutput != 393216 {
		t.Fatalf("upstream snapshot = %#v, want the remote 1M/393216", manual.Capabilities)
	}
	if manual.MaxInput != 119808 {
		t.Fatalf("manual max_input = %d, want 119808", manual.MaxInput)
	}

	catalog := byName["catalog-model"]
	if catalog.ContextWindow != 200000 || catalog.MaxOutput != 32000 {
		t.Fatalf("catalog params not updated: %d/%d", catalog.ContextWindow, catalog.MaxOutput)
	}
	if catalog.WindowSource != llm.SourceCatalog || catalog.OutputSource != llm.SourceCatalog {
		t.Fatalf("catalog sources = %q/%q", catalog.WindowSource, catalog.OutputSource)
	}
	if catalog.Capabilities.ContextWindow != 200000 {
		t.Fatalf("catalog capabilities.context_window = %d", catalog.Capabilities.ContextWindow)
	}
	// Display max_input uses the raw upstream output: 200000 - 32000.
	if catalog.MaxInput != 168000 {
		t.Fatalf("catalog max_input = %d, want 168000 (200000-32000)", catalog.MaxInput)
	}
}

// TestResetParams pins R-A1-4: reset restores the last upstream window/output
// snapshot and flips both field sources back to catalog; a model without a
// snapshot resets to 0 (unknown/auto).
func TestResetParams(t *testing.T) {
	svc, repo, _, audit, _ := newTestService(t)
	ctx := context.Background()
	repo.configs[1] = chatTestConfig(1, false)
	repo.models[2] = llm.Model{
		ID: 2, ConfigID: 1, Model: "edited-model", DisplayName: "edited-model",
		ContextWindow: 128000, MaxOutput: 8192,
		Source: llm.SourceManual, Status: llm.ModelStatusActive,
		WindowSource: llm.SourceManual, OutputSource: llm.SourceManual,
		Capabilities: llm.Capabilities{
			ContextWindow:        128000,
			CatalogContextWindow: 1000000,
			CatalogMaxOutput:     393216,
		},
	}
	repo.models[3] = llm.Model{
		ID: 3, ConfigID: 1, Model: "no-upstream-model", DisplayName: "no-upstream-model",
		ContextWindow: 64000, MaxOutput: 4096,
		Source: llm.SourceManual, Status: llm.ModelStatusActive,
		WindowSource: llm.SourceManual, OutputSource: llm.SourceManual,
		Capabilities: llm.Capabilities{ContextWindow: 64000},
	}

	got, err := svc.ResetModelParams(ctx, 1, 2, 7)
	if err != nil {
		t.Fatalf("ResetModelParams: %v", err)
	}
	if got.ContextWindow != 1000000 || got.MaxOutput != 393216 {
		t.Fatalf("reset params = %d/%d, want the 1M/393216 snapshot", got.ContextWindow, got.MaxOutput)
	}
	if got.WindowSource != llm.SourceCatalog || got.OutputSource != llm.SourceCatalog {
		t.Fatalf("reset sources = %q/%q, want catalog/catalog", got.WindowSource, got.OutputSource)
	}
	if got.MaxInput != 606784 {
		t.Fatalf("max_input = %d, want 606784 (1000000-393216 raw output)", got.MaxInput)
	}
	stored := repo.models[2]
	t.Logf("stored after reset: %+v", stored)
	if stored.ContextWindow != 1000000 || stored.MaxOutput != 393216 ||
		stored.WindowSource != llm.SourceCatalog || stored.OutputSource != llm.SourceCatalog {
		t.Fatalf("stored after reset = %+v", stored)
	}
	if stored.Capabilities.ContextWindow != 1000000 {
		t.Fatalf("capabilities.context_window = %d, want 1000000", stored.Capabilities.ContextWindow)
	}

	empty, err := svc.ResetModelParams(ctx, 1, 3, 7)
	if err != nil {
		t.Fatalf("ResetModelParams without snapshot: %v", err)
	}
	if empty.ContextWindow != 0 || empty.MaxOutput != 0 {
		t.Fatalf("reset without snapshot = %d/%d, want 0/0", empty.ContextWindow, empty.MaxOutput)
	}
	if empty.WindowSource != llm.SourceCatalog || empty.OutputSource != llm.SourceCatalog {
		t.Fatalf("sources without snapshot = %q/%q", empty.WindowSource, empty.OutputSource)
	}
	if empty.MaxInput != 123904 {
		t.Fatalf("max_input without snapshot = %d, want 123904 (128000-4096)", empty.MaxInput)
	}

	if _, err := svc.ResetModelParams(ctx, 1, 999, 7); err == nil {
		t.Fatal("reset of an unknown model succeeded")
	} else if apiErr := apiError(t, err); apiErr.Status != http.StatusNotFound {
		t.Fatalf("unknown model status = %d, want 404", apiErr.Status)
	}

	var entry *auditapp.Event
	for i := range audit.events {
		if audit.events[i].Action == "llm.model_params_reset" && audit.events[i].TargetID == "2" {
			entry = &audit.events[i]
			break
		}
	}
	if entry == nil {
		t.Fatalf("audit actions = %#v, want llm.model_params_reset for model 2", audit.events)
	}
	if entry.TargetKind != "llm_model" {
		t.Fatalf("reset audit target = %#v", entry)
	}
	if entry.ActorID == nil || *entry.ActorID != 7 {
		t.Fatalf("reset audit actor = %v", entry.ActorID)
	}
	changed, _ := entry.Metadata["changed_fields"].([]string)
	if !containsString(changed, "context_window") || !containsString(changed, "max_output") {
		t.Fatalf("reset changed fields = %#v", entry.Metadata["changed_fields"])
	}
}

// TestPatchRejectsMaxOutputAboveWindow pins R-A1-6: max_output must stay within
// the context window, including when only one side is patched and the other is
// the stored value. Equal values are allowed (the UI warns instead).
func TestPatchRejectsMaxOutputAboveWindow(t *testing.T) {
	svc, repo, _, _, _ := newTestService(t)
	ctx := context.Background()
	repo.models[2] = llm.Model{
		ID: 2, ConfigID: 1, Model: "gpt-4o", DisplayName: "gpt-4o",
		ContextWindow: 128000, MaxOutput: 8192,
		Source: llm.SourceCatalog, Status: llm.ModelStatusActive,
		WindowSource: llm.SourceCatalog, OutputSource: llm.SourceCatalog,
		Capabilities: llm.Capabilities{ContextWindow: 128000},
	}

	assertInvalid := func(t *testing.T, params UpdateModelParams) {
		t.Helper()
		if _, err := svc.UpdateModelParams(ctx, 1, 2, params); err == nil {
			t.Fatalf("params %#v accepted", params)
		} else {
			apiErr := apiError(t, err)
			if apiErr.Status != http.StatusBadRequest || apiErr.Code != "invalid_argument" {
				t.Fatalf("error = %#v, want 400 invalid_argument", apiErr)
			}
		}
	}

	// max_output above the stored window.
	assertInvalid(t, UpdateModelParams{MaxOutput: int64Ptr(128001)})
	// A window lowered below the stored max_output is the same violation.
	assertInvalid(t, UpdateModelParams{ContextWindow: int64Ptr(4096)})
	// Out-of-range window is rejected even when max_output stays valid.
	assertInvalid(t, UpdateModelParams{ContextWindow: int64Ptr(llm.MaxContextWindow + 1)})
	assertInvalid(t, UpdateModelParams{MaxOutput: int64Ptr(-1)})

	if repo.models[2].ContextWindow != 128000 || repo.models[2].MaxOutput != 8192 {
		t.Fatalf("rejected patch was persisted: %+v", repo.models[2])
	}

	// Equal raw window/output is allowed; the derived max_input is the raw
	// difference, so it is 0 rather than an artefact of the request-side cap.
	equal, err := svc.UpdateModelParams(ctx, 1, 2, UpdateModelParams{MaxOutput: int64Ptr(128000)})
	if err != nil {
		t.Fatalf("equal window/output rejected: %v", err)
	}
	if equal.MaxInput != 0 {
		t.Fatalf("max_input = %d, want 0 (128000-128000 raw output)", equal.MaxInput)
	}
}
