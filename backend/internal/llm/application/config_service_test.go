package application

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"

	audit "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	auditdomain "github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
	llm "github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
)

var ctx = context.Background()

// capturingRecorder is an audit.Recorder that keeps entries in memory so a test
// can assert what the service recorded.
type capturingRecorder struct {
	entries []auditdomain.Entry
}

func (r *capturingRecorder) Record(_ context.Context, entry auditdomain.Entry) error {
	r.entries = append(r.entries, entry)
	return nil
}

func (r *capturingRecorder) List(context.Context, int, int64) ([]auditdomain.Entry, bool, error) {
	return nil, false, nil
}

func (r *capturingRecorder) ListFiltered(context.Context, auditdomain.ListFilter, int, int64) ([]auditdomain.Entry, bool, error) {
	return nil, false, nil
}

// fakeConfigRepository is an in-memory llm.Repository. Default switching is
// emulated so unit tests can observe the service's calls; the real transaction
// is covered by the PostgreSQL integration test.
type fakeConfigRepository struct {
	configs map[int64]llm.Config
	nextID  int64
}

func newFakeConfigRepository() *fakeConfigRepository {
	return &fakeConfigRepository{configs: make(map[int64]llm.Config)}
}

func (r *fakeConfigRepository) Create(_ context.Context, params llm.CreateParams) (llm.Config, error) {
	for _, existing := range r.configs {
		if existing.Name == params.Name {
			return llm.Config{}, llm.ErrNameTaken
		}
	}
	if params.IsDefault {
		r.clearDefaults()
	}
	r.nextID++
	cfg := llm.Config{
		ID:                r.nextID,
		Name:              params.Name,
		Protocol:          params.Protocol,
		BaseURL:           params.BaseURL,
		Model:             params.Model,
		APIKey:            params.APIKey,
		Headers:           params.Headers,
		RetryCount:        params.RetryCount,
		RetryIntervalMS:   params.RetryIntervalMS,
		TimeoutMS:         params.TimeoutMS,
		StreamEnabled:     params.StreamEnabled,
		MultimodalEnabled: params.MultimodalEnabled,
		IsDefault:         params.IsDefault,
		KeyVersion:        1,
	}
	r.configs[cfg.ID] = cfg
	return cfg, nil
}

func (r *fakeConfigRepository) Update(_ context.Context, id int64, params llm.UpdateParams) (llm.Config, error) {
	cfg, ok := r.configs[id]
	if !ok {
		return llm.Config{}, llm.ErrConfigNotFound
	}
	if params.Name != nil {
		for otherID, other := range r.configs {
			if otherID != id && other.Name == *params.Name {
				return llm.Config{}, llm.ErrNameTaken
			}
		}
		cfg.Name = *params.Name
	}
	if params.Protocol != nil {
		cfg.Protocol = *params.Protocol
	}
	if params.BaseURL != nil {
		cfg.BaseURL = *params.BaseURL
	}
	if params.Model != nil {
		cfg.Model = *params.Model
	}
	if params.APIKey != nil {
		cfg.APIKey = *params.APIKey
	}
	if params.Headers != nil {
		cfg.Headers = *params.Headers
	}
	if params.RetryCount != nil {
		cfg.RetryCount = *params.RetryCount
	}
	if params.RetryIntervalMS != nil {
		cfg.RetryIntervalMS = *params.RetryIntervalMS
	}
	if params.TimeoutMS != nil {
		cfg.TimeoutMS = *params.TimeoutMS
	}
	if params.StreamEnabled != nil {
		cfg.StreamEnabled = *params.StreamEnabled
	}
	if params.MultimodalEnabled != nil {
		cfg.MultimodalEnabled = *params.MultimodalEnabled
	}
	if params.IsDefault != nil {
		if *params.IsDefault {
			r.clearDefaults()
		}
		cfg.IsDefault = *params.IsDefault
	}
	if params.ModelCatalog != nil {
		cfg.ModelCatalog = *params.ModelCatalog
	}
	if params.CatalogFetchedAt != nil {
		cfg.CatalogFetchedAt = params.CatalogFetchedAt
	}
	r.configs[id] = cfg
	return cfg, nil
}

func (r *fakeConfigRepository) clearDefaults() {
	for id, cfg := range r.configs {
		cfg.IsDefault = false
		r.configs[id] = cfg
	}
}

func (r *fakeConfigRepository) Delete(_ context.Context, id int64) error {
	if _, ok := r.configs[id]; !ok {
		return llm.ErrConfigNotFound
	}
	delete(r.configs, id)
	return nil
}

func (r *fakeConfigRepository) Get(_ context.Context, id int64) (llm.Config, error) {
	cfg, ok := r.configs[id]
	if !ok {
		return llm.Config{}, llm.ErrConfigNotFound
	}
	return cfg, nil
}

func (r *fakeConfigRepository) List(context.Context) ([]llm.Config, error) {
	ids := make([]int64, 0, len(r.configs))
	for id := range r.configs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	configs := make([]llm.Config, 0, len(ids))
	for _, id := range ids {
		configs = append(configs, r.configs[id])
	}
	return configs, nil
}

func (r *fakeConfigRepository) GetDefault(context.Context) (llm.Config, error) {
	for _, cfg := range r.configs {
		if cfg.IsDefault {
			return cfg, nil
		}
	}
	return llm.Config{}, llm.ErrConfigNotFound
}

func ptr[T any](value T) *T {
	return &value
}

func validCreateParams() llm.CreateParams {
	return llm.CreateParams{
		Name:              "default",
		Protocol:          llm.ProtocolChat,
		BaseURL:           "https://api.deepseek.com",
		Model:             "deepseek-flash",
		APIKey:            "sk-test",
		Headers:           map[string]string{"X-Test": "1"},
		RetryCount:        2,
		RetryIntervalMS:   500,
		TimeoutMS:         60000,
		StreamEnabled:     true,
		MultimodalEnabled: true,
		IsDefault:         true,
	}
}

func TestConfigServiceCreateValidates(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*llm.CreateParams)
		want   error
	}{
		{name: "name required", mutate: func(p *llm.CreateParams) { p.Name = " " }, want: llm.ErrInvalidName},
		{name: "model required", mutate: func(p *llm.CreateParams) { p.Model = "" }, want: llm.ErrInvalidModel},
		{name: "unknown protocol", mutate: func(p *llm.CreateParams) { p.Protocol = "grpc" }, want: llm.ErrInvalidProtocol},
		{name: "base url scheme", mutate: func(p *llm.CreateParams) { p.BaseURL = "ftp://api.deepseek.com" }, want: llm.ErrInvalidBaseURL},
		{name: "base url without scheme", mutate: func(p *llm.CreateParams) { p.BaseURL = "api.deepseek.com" }, want: llm.ErrInvalidBaseURL},
		{name: "base url without host", mutate: func(p *llm.CreateParams) { p.BaseURL = "https://" }, want: llm.ErrInvalidBaseURL},
		{name: "retry below range", mutate: func(p *llm.CreateParams) { p.RetryCount = -1 }, want: llm.ErrInvalidRetryCount},
		{name: "retry above range", mutate: func(p *llm.CreateParams) { p.RetryCount = 6 }, want: llm.ErrInvalidRetryCount},
		{name: "interval below range", mutate: func(p *llm.CreateParams) { p.RetryIntervalMS = 99 }, want: llm.ErrInvalidRetryInterval},
		{name: "interval above range", mutate: func(p *llm.CreateParams) { p.RetryIntervalMS = 30001 }, want: llm.ErrInvalidRetryInterval},
		{name: "timeout below range", mutate: func(p *llm.CreateParams) { p.TimeoutMS = 999 }, want: llm.ErrInvalidTimeout},
		{name: "timeout above range", mutate: func(p *llm.CreateParams) { p.TimeoutMS = 300001 }, want: llm.ErrInvalidTimeout},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeConfigRepository()
			svc := NewConfigService(repo, audit.NopRecorder{})
			params := validCreateParams()
			tc.mutate(&params)
			if _, err := svc.Create(ctx, params); !errors.Is(err, tc.want) {
				t.Fatalf("err=%v, want %v", err, tc.want)
			}
			if len(repo.configs) != 0 {
				t.Fatalf("invalid config was persisted: %+v", repo.configs)
			}
		})
	}
}

func TestConfigServiceCreateAcceptsValidConfig(t *testing.T) {
	repo := newFakeConfigRepository()
	svc := NewConfigService(repo, audit.NopRecorder{})
	cfg, err := svc.Create(ctx, validCreateParams())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ID == 0 || cfg.Name != "default" || cfg.Protocol != llm.ProtocolChat || !cfg.IsDefault {
		t.Fatalf("cfg=%+v", cfg)
	}
	if cfg.APIKey != "sk-test" || cfg.Headers["X-Test"] != "1" {
		t.Fatalf("secrets not passed through to the repository: %+v", cfg)
	}
}

func TestConfigServiceCreateAppliesTimingDefaults(t *testing.T) {
	repo := newFakeConfigRepository()
	svc := NewConfigService(repo, audit.NopRecorder{})
	params := validCreateParams()
	params.RetryIntervalMS = 0
	params.TimeoutMS = 0
	cfg, err := svc.Create(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RetryIntervalMS != 500 || cfg.TimeoutMS != 60000 {
		t.Fatalf("timing defaults=%d/%d want 500/60000", cfg.RetryIntervalMS, cfg.TimeoutMS)
	}
}

func TestConfigServiceRejectsReservedHeaders(t *testing.T) {
	for _, name := range []string{"Authorization", "authorization", "AUTHORIZATION", "Content-Type", "content-type"} {
		t.Run(name, func(t *testing.T) {
			repo := newFakeConfigRepository()
			svc := NewConfigService(repo, audit.NopRecorder{})
			params := validCreateParams()
			params.Headers = map[string]string{name: "value"}
			if _, err := svc.Create(ctx, params); !errors.Is(err, llm.ErrReservedHeader) {
				t.Fatalf("err=%v, want ErrReservedHeader", err)
			}
			if len(repo.configs) != 0 {
				t.Fatal("config with reserved header was persisted")
			}
		})
	}
}

func TestConfigServiceCreateRecordsSanitizedAudit(t *testing.T) {
	repo := newFakeConfigRepository()
	recorder := &capturingRecorder{}
	svc := NewConfigService(repo, recorder)
	params := validCreateParams()
	params.Headers = map[string]string{"X-Test": "header-secret"}
	cfg, err := svc.Create(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if len(recorder.entries) != 1 {
		t.Fatalf("audit entries=%d want 1", len(recorder.entries))
	}
	entry := recorder.entries[0]
	if entry.Action != audit.ActionLLMConfigCreated || entry.TargetType != "llm_config" {
		t.Fatalf("entry=%+v", entry)
	}
	if entry.TargetID != strconv.FormatInt(cfg.ID, 10) {
		t.Fatalf("target=%q want %d", entry.TargetID, cfg.ID)
	}
	if entry.Metadata["name"] != "default" || entry.Metadata["model"] != "deepseek-flash" {
		t.Fatalf("metadata=%v", entry.Metadata)
	}
	rendered := fmt.Sprint(entry.Metadata)
	if strings.Contains(rendered, "sk-test") || strings.Contains(rendered, "X-Test") || strings.Contains(rendered, "header-secret") {
		t.Fatalf("audit metadata leaks secrets: %v", entry.Metadata)
	}
}

func TestConfigServiceAuditStampsRequestMetaAndActor(t *testing.T) {
	repo := newFakeConfigRepository()
	recorder := &capturingRecorder{}
	svc := NewConfigService(repo, recorder)
	reqCtx := audit.WithMeta(context.Background(), audit.RequestMeta{
		RequestID: "req-9",
		IP:        "127.0.0.1",
		UserAgent: "go-test",
	})
	reqCtx = audit.WithActorID(reqCtx, 7)
	if _, err := svc.Create(reqCtx, validCreateParams()); err != nil {
		t.Fatal(err)
	}
	entry := recorder.entries[0]
	if entry.RequestID != "req-9" || entry.IP != "127.0.0.1" || entry.UserAgent != "go-test" {
		t.Fatalf("request metadata missing: %+v", entry)
	}
	if entry.ActorUserID == nil || *entry.ActorUserID != 7 {
		t.Fatalf("actor=%v want 7", entry.ActorUserID)
	}
}

func TestConfigServiceUpdateValidatesAndRecordsAudit(t *testing.T) {
	repo := newFakeConfigRepository()
	recorder := &capturingRecorder{}
	svc := NewConfigService(repo, recorder)
	cfg, err := svc.Create(ctx, validCreateParams())
	if err != nil {
		t.Fatal(err)
	}

	newName := "renamed"
	updated, err := svc.Update(ctx, cfg.ID, llm.UpdateParams{Name: &newName})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "renamed" {
		t.Fatalf("updated=%+v", updated)
	}
	if len(recorder.entries) != 2 || recorder.entries[1].Action != audit.ActionLLMConfigUpdated {
		t.Fatalf("audit=%+v", recorder.entries)
	}

	invalid := []struct {
		name   string
		params llm.UpdateParams
		want   error
	}{
		{name: "empty name", params: llm.UpdateParams{Name: ptr(" ")}, want: llm.ErrInvalidName},
		{name: "empty model", params: llm.UpdateParams{Model: ptr("")}, want: llm.ErrInvalidModel},
		{name: "invalid protocol", params: llm.UpdateParams{Protocol: ptr(llm.Protocol("grpc"))}, want: llm.ErrInvalidProtocol},
		{name: "invalid base url", params: llm.UpdateParams{BaseURL: ptr("ws://api.deepseek.com")}, want: llm.ErrInvalidBaseURL},
		{name: "invalid retry", params: llm.UpdateParams{RetryCount: ptr(9)}, want: llm.ErrInvalidRetryCount},
		{name: "invalid interval", params: llm.UpdateParams{RetryIntervalMS: ptr(50)}, want: llm.ErrInvalidRetryInterval},
		{name: "invalid timeout", params: llm.UpdateParams{TimeoutMS: ptr(500)}, want: llm.ErrInvalidTimeout},
		{name: "reserved header", params: llm.UpdateParams{Headers: &map[string]string{"Content-Type": "text/plain"}}, want: llm.ErrReservedHeader},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.Update(ctx, cfg.ID, tc.params); !errors.Is(err, tc.want) {
				t.Fatalf("err=%v, want %v", err, tc.want)
			}
		})
	}
	current, err := repo.Get(ctx, cfg.ID)
	if err != nil || current.Name != "renamed" || current.Model != "deepseek-flash" {
		t.Fatalf("invalid update changed the config: %+v err=%v", current, err)
	}
}

func TestConfigServiceDeleteRecordsAudit(t *testing.T) {
	repo := newFakeConfigRepository()
	recorder := &capturingRecorder{}
	svc := NewConfigService(repo, recorder)
	cfg, err := svc.Create(ctx, validCreateParams())
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, cfg.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get(ctx, cfg.ID); !errors.Is(err, llm.ErrConfigNotFound) {
		t.Fatalf("err=%v, want ErrConfigNotFound", err)
	}
	if len(recorder.entries) != 2 || recorder.entries[1].Action != audit.ActionLLMConfigDeleted {
		t.Fatalf("audit=%+v", recorder.entries)
	}
	if err := svc.Delete(ctx, cfg.ID); !errors.Is(err, llm.ErrConfigNotFound) {
		t.Fatalf("second delete err=%v, want ErrConfigNotFound", err)
	}
}

func TestConfigServiceSeedFromEnv(t *testing.T) {
	t.Run("missing values do not seed", func(t *testing.T) {
		for _, args := range [][3]string{
			{"", "sk-seed", "deepseek-flash"},
			{"https://api.deepseek.com", "", "deepseek-flash"},
			{"https://api.deepseek.com", "sk-seed", ""},
		} {
			repo := newFakeConfigRepository()
			recorder := &capturingRecorder{}
			svc := NewConfigService(repo, recorder)
			seeded, err := svc.SeedFromEnv(ctx, args[0], args[1], args[2])
			if err != nil || seeded {
				t.Fatalf("seeded=%v err=%v args=%q", seeded, err, args)
			}
			if len(repo.configs) != 0 || len(recorder.entries) != 0 {
				t.Fatalf("missing seed values created data: configs=%d audit=%d", len(repo.configs), len(recorder.entries))
			}
		}
	})

	t.Run("complete values seed documented defaults once", func(t *testing.T) {
		repo := newFakeConfigRepository()
		recorder := &capturingRecorder{}
		svc := NewConfigService(repo, recorder)

		seeded, err := svc.SeedFromEnv(ctx, "https://api.deepseek.com", "sk-seed", "deepseek-flash")
		if err != nil || !seeded {
			t.Fatalf("seeded=%v err=%v", seeded, err)
		}
		cfg, err := svc.GetDefault(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Name != "default" || cfg.Protocol != llm.ProtocolChat || cfg.BaseURL != "https://api.deepseek.com" || cfg.Model != "deepseek-flash" {
			t.Fatalf("seeded config=%+v", cfg)
		}
		if cfg.RetryCount != 2 || cfg.RetryIntervalMS != 500 || cfg.TimeoutMS != 60000 {
			t.Fatalf("seed timing=%d/%d/%d", cfg.RetryCount, cfg.RetryIntervalMS, cfg.TimeoutMS)
		}
		if !cfg.StreamEnabled || !cfg.MultimodalEnabled || !cfg.IsDefault {
			t.Fatalf("seed flags=%+v", cfg)
		}
		if cfg.APIKey != "sk-seed" {
			t.Fatalf("seed api key=%q", cfg.APIKey)
		}
		if len(recorder.entries) != 1 || recorder.entries[0].Action != audit.ActionLLMConfigCreated {
			t.Fatalf("seed audit=%+v", recorder.entries)
		}

		seeded, err = svc.SeedFromEnv(ctx, "https://api.deepseek.com", "sk-other", "other")
		if err != nil || seeded {
			t.Fatalf("second seed seeded=%v err=%v", seeded, err)
		}
		configs, err := svc.List(ctx)
		if err != nil || len(configs) != 1 || len(recorder.entries) != 1 {
			t.Fatalf("second seed configs=%d audit=%d err=%v", len(configs), len(recorder.entries), err)
		}
	})

	t.Run("existing default wins", func(t *testing.T) {
		repo := newFakeConfigRepository()
		svc := NewConfigService(repo, audit.NopRecorder{})
		params := validCreateParams()
		params.Name = "custom"
		params.IsDefault = true
		if _, err := svc.Create(ctx, params); err != nil {
			t.Fatal(err)
		}
		seeded, err := svc.SeedFromEnv(ctx, "https://api.deepseek.com", "sk-seed", "deepseek-flash")
		if err != nil || seeded {
			t.Fatalf("seeded=%v err=%v", seeded, err)
		}
		configs, err := svc.List(ctx)
		if err != nil || len(configs) != 1 || configs[0].Name != "custom" {
			t.Fatalf("configs=%+v err=%v", configs, err)
		}
	})

	t.Run("invalid seed values fail", func(t *testing.T) {
		repo := newFakeConfigRepository()
		svc := NewConfigService(repo, audit.NopRecorder{})
		if _, err := svc.SeedFromEnv(ctx, "not-a-url", "sk-seed", "deepseek-flash"); !errors.Is(err, llm.ErrInvalidBaseURL) {
			t.Fatalf("err=%v, want ErrInvalidBaseURL", err)
		}
		if len(repo.configs) != 0 {
			t.Fatal("invalid seed config was persisted")
		}
	})
}

func TestConfigServiceGetListAndDefault(t *testing.T) {
	repo := newFakeConfigRepository()
	svc := NewConfigService(repo, audit.NopRecorder{})
	if _, err := svc.Get(ctx, 42); !errors.Is(err, llm.ErrConfigNotFound) {
		t.Fatalf("err=%v, want ErrConfigNotFound", err)
	}
	if _, err := svc.GetDefault(ctx); !errors.Is(err, llm.ErrConfigNotFound) {
		t.Fatalf("err=%v, want ErrConfigNotFound", err)
	}
	configs, err := svc.List(ctx)
	if err != nil || len(configs) != 0 {
		t.Fatalf("configs=%v err=%v", configs, err)
	}
	if _, err := svc.Create(ctx, validCreateParams()); err != nil {
		t.Fatal(err)
	}
	got, err := svc.GetDefault(ctx)
	if err != nil || got.Name != "default" {
		t.Fatalf("default=%+v err=%v", got, err)
	}
}

// TestDefaultCreateParams pins the explicit create default policy used by the
// admin API: omitted fields get the database column defaults instead of the Go
// zero value, while an explicitly provided zero (retry_count=0) stays zero.
func TestDefaultCreateParams(t *testing.T) {
	defaults := DefaultCreateParams()
	if defaults.RetryCount != 2 {
		t.Fatalf("RetryCount=%d want 2", defaults.RetryCount)
	}
	if defaults.RetryIntervalMS != 500 {
		t.Fatalf("RetryIntervalMS=%d want 500", defaults.RetryIntervalMS)
	}
	if defaults.TimeoutMS != 60000 {
		t.Fatalf("TimeoutMS=%d want 60000", defaults.TimeoutMS)
	}
	if !defaults.StreamEnabled || !defaults.MultimodalEnabled {
		t.Fatalf("feature defaults=%+v want both true", defaults)
	}
	if defaults.IsDefault {
		t.Fatal("a created configuration must not become the default implicitly")
	}
}

// TestConfigServiceTrimsPersistedValues covers the Task 9 review minor: the
// persisted name, base_url, model and header names must be trimmed.
func TestConfigServiceTrimsPersistedValues(t *testing.T) {
	repo := newFakeConfigRepository()
	svc := NewConfigService(repo, audit.NopRecorder{})
	params := validCreateParams()
	params.Name = "  default  "
	params.BaseURL = "  https://api.deepseek.com  "
	params.Model = "  deepseek-flash  "
	params.Headers = map[string]string{"  X-Test  ": " value "}
	cfg, err := svc.Create(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "default" || cfg.BaseURL != "https://api.deepseek.com" || cfg.Model != "deepseek-flash" {
		t.Fatalf("create did not trim persisted values: %+v", cfg)
	}
	if len(cfg.Headers) != 1 || cfg.Headers["X-Test"] != " value " {
		t.Fatalf("create did not trim header names only: %+v", cfg.Headers)
	}

	name := "  renamed  "
	baseURL := "  https://api.deepseek.com/v1  "
	model := "  deepseek-chat  "
	headers := map[string]string{"  X-New  ": "v"}
	updated, err := svc.Update(ctx, cfg.ID, llm.UpdateParams{
		Name:    &name,
		BaseURL: &baseURL,
		Model:   &model,
		Headers: &headers,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "renamed" || updated.BaseURL != "https://api.deepseek.com/v1" || updated.Model != "deepseek-chat" {
		t.Fatalf("update did not trim persisted values: %+v", updated)
	}
	if len(updated.Headers) != 1 || updated.Headers["X-New"] != "v" {
		t.Fatalf("update did not trim header names: %+v", updated.Headers)
	}
}

// TestConfigServiceSeedExistingNameWithoutDefault covers the Task 9 review
// minor: a pre-existing non-default configuration named "default" must not abort
// startup seeding. The seed is a logged no-op and the existing row is untouched.
func TestConfigServiceSeedExistingNameWithoutDefault(t *testing.T) {
	repo := newFakeConfigRepository()
	recorder := &capturingRecorder{}
	svc := NewConfigService(repo, recorder)

	params := validCreateParams()
	params.Name = llm.SeedConfigName
	params.IsDefault = false
	if _, err := svc.Create(ctx, params); err != nil {
		t.Fatal(err)
	}

	seeded, err := svc.SeedFromEnv(ctx, "https://api.deepseek.com", "sk-seed", "deepseek-flash")
	if err != nil {
		t.Fatalf("seed aborted startup: %v", err)
	}
	if seeded {
		t.Fatal("seed reported a created configuration")
	}
	configs, err := svc.List(ctx)
	if err != nil || len(configs) != 1 {
		t.Fatalf("configs=%d err=%v", len(configs), err)
	}
	if configs[0].APIKey != "sk-test" || configs[0].Model != "deepseek-flash" || configs[0].IsDefault {
		t.Fatalf("existing config was modified: %+v", configs[0])
	}
	if len(recorder.entries) != 1 {
		t.Fatalf("seed recorded an unexpected audit entry: %+v", recorder.entries)
	}
}
