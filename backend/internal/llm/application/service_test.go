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
		existing.Source = incoming.Source
		existing.UpdatedAt = incoming.UpdatedAt
		f.models[existingID] = existing
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

	model, err := svc.AddModel(ctx, cfg.ID, "gpt-4o", 128000, 16384, true, false, 7)
	if err != nil {
		t.Fatalf("AddModel: %v", err)
	}
	if model.ID <= 0 || model.ConfigID != cfg.ID || model.Model != "gpt-4o" || model.DisplayName != "gpt-4o" {
		t.Fatalf("model = %#v", model)
	}
	if model.Source != "manual" || model.Status != "active" || !model.SupportsTools || model.SupportsVision {
		t.Fatalf("model flags = %#v", model)
	}

	if _, err := svc.AddModel(ctx, cfg.ID, "gpt-4o", 1, 1, false, false, 7); err == nil {
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
			if _, err := svc.AddModel(ctx, tc.configID, tc.model, tc.contextWindow, tc.maxOutput, false, false, 1); err == nil {
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
	modelA, err := svc.AddModel(ctx, first.ID, "gpt-4o", 128000, 16384, true, true, 1)
	if err != nil {
		t.Fatalf("AddModel: %v", err)
	}
	if _, err := svc.AddModel(ctx, second.ID, "claude", 200000, 8192, false, true, 1); err != nil {
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

func formatID(value int64) string {
	return strconv.FormatInt(value, 10)
}
