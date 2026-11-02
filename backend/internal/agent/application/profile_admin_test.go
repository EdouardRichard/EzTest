package agentapp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

/* ------------------------------- fakes ---------------------------------- */

type fakeProfileAdminRepo struct {
	profiles map[string]agent.AgentProfile
	versions map[string]agent.PromptVersionInfo
	revisions map[string][]agent.PromptRevision

	listErr    error
	getErr     error
	updateErr  error
	saveErr    error
	getRevErr  error
	listRevErr error

	listCalls   int
	getCalls    int
	updateCalls int
	saveCalls   int
	revCalls    int

	lastUpdate      agent.ProfileUpdate
	lastWrite       agent.PromptWrite
	lastLimit       int
	lastBefore      *int
	lastVersionKey  string
	lastVersionNum  int
	updateChanged   bool
}

func newFakeProfileAdminRepo(t *testing.T) *fakeProfileAdminRepo {
	t.Helper()
	repo := &fakeProfileAdminRepo{
		profiles:  map[string]agent.AgentProfile{},
		versions:  map[string]agent.PromptVersionInfo{},
		revisions: map[string][]agent.PromptRevision{},
	}
	for i, seed := range agentapp.ProfileSeeds() {
		seed.ID = int64(100 + i)
		seed.CreatedAt = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
		seed.UpdatedAt = time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
		repo.profiles[seed.Key] = seed
		repo.versions[seed.Key] = agent.PromptVersionInfo{Version: 1, CreatedAt: seed.UpdatedAt}
		repo.revisions[seed.Key] = []agent.PromptRevision{{
			ID: int64(900 + i), ProfileKey: seed.Key, Version: 1,
			SystemPrompt: seed.SystemPrompt, Source: agent.PromptSourceSeed,
			CreatedAt: seed.UpdatedAt,
		}}
	}
	return repo
}

func (f *fakeProfileAdminRepo) ListProfiles(_ context.Context) ([]agent.AgentProfile, error) {
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	profiles := make([]agent.AgentProfile, 0, len(f.profiles))
	for _, profile := range f.profiles {
		profiles = append(profiles, profile)
	}
	return profiles, nil
}

func (f *fakeProfileAdminRepo) GetProfile(_ context.Context, key string) (agent.AgentProfile, error) {
	f.getCalls++
	if f.getErr != nil {
		return agent.AgentProfile{}, f.getErr
	}
	profile, ok := f.profiles[key]
	if !ok {
		return agent.AgentProfile{}, agent.ErrNotFound
	}
	return profile, nil
}

func (f *fakeProfileAdminRepo) UpdateProfile(_ context.Context, key string, update agent.ProfileUpdate) (agent.AgentProfile, bool, error) {
	f.updateCalls++
	f.lastUpdate = update
	if f.updateErr != nil {
		return agent.AgentProfile{}, false, f.updateErr
	}
	profile, ok := f.profiles[key]
	if !ok {
		return agent.AgentProfile{}, false, agent.ErrNotFound
	}
	changed := false
	if update.SetModel {
		var next *string
		if update.Model != nil && strings.TrimSpace(*update.Model) != "" {
			trimmed := strings.TrimSpace(*update.Model)
			next = &trimmed
		}
		if !sameStringPointer(profile.Model, next) {
			profile.Model = next
			changed = true
		}
	}
	if update.Budgets != nil && profile.Budgets != *update.Budgets {
		profile.Budgets = *update.Budgets
		changed = true
	}
	if update.AllowedTools != nil && !sameStrings(profile.AllowedTools, *update.AllowedTools) {
		profile.AllowedTools = append([]string(nil), (*update.AllowedTools)...)
		changed = true
	}
	if changed {
		profile.UpdatedAt = profile.UpdatedAt.Add(time.Hour)
		f.profiles[key] = profile
	}
	f.updateChanged = changed
	return profile, changed, nil
}

func (f *fakeProfileAdminRepo) LatestPromptVersions(_ context.Context) (map[string]agent.PromptVersionInfo, error) {
	versions := make(map[string]agent.PromptVersionInfo, len(f.versions))
	for key, info := range f.versions {
		versions[key] = info
	}
	return versions, nil
}

func (f *fakeProfileAdminRepo) SavePromptRevision(_ context.Context, write agent.PromptWrite) (agent.PromptWriteResult, error) {
	f.saveCalls++
	f.lastWrite = write
	if f.saveErr != nil {
		return agent.PromptWriteResult{}, f.saveErr
	}
	profile, ok := f.profiles[write.ProfileKey]
	if !ok {
		return agent.PromptWriteResult{}, agent.ErrNotFound
	}
	current := 0
	if info, ok := f.versions[write.ProfileKey]; ok {
		current = info.Version
	}
	if write.ExpectedVersion != nil && *write.ExpectedVersion != current {
		return agent.PromptWriteResult{}, agent.ErrPromptVersionConflict
	}
	if write.NoOpOnSame && profile.SystemPrompt == write.SystemPrompt {
		return agent.PromptWriteResult{Version: current, Changed: false}, nil
	}
	next := current + 1
	revision := agent.PromptRevision{
		ID: int64(1000 + next), ProfileKey: write.ProfileKey, Version: next,
		SystemPrompt: write.SystemPrompt, Note: write.Note, Source: write.Source,
		ActorID: write.ActorID, CreatedAt: profile.UpdatedAt,
	}
	f.revisions[write.ProfileKey] = append([]agent.PromptRevision{revision}, f.revisions[write.ProfileKey]...)
	f.versions[write.ProfileKey] = agent.PromptVersionInfo{Version: next, CreatedAt: revision.CreatedAt}
	profile.SystemPrompt = write.SystemPrompt
	f.profiles[write.ProfileKey] = profile
	return agent.PromptWriteResult{Version: next, Changed: true}, nil
}

func (f *fakeProfileAdminRepo) GetPromptRevision(_ context.Context, profileKey string, version int) (agent.PromptRevision, error) {
	f.lastVersionKey = profileKey
	f.lastVersionNum = version
	if f.getRevErr != nil {
		return agent.PromptRevision{}, f.getRevErr
	}
	for _, revision := range f.revisions[profileKey] {
		if revision.Version == version {
			return revision, nil
		}
	}
	return agent.PromptRevision{}, agent.ErrNotFound
}

func (f *fakeProfileAdminRepo) ListPromptRevisions(_ context.Context, profileKey string, limit int, beforeVersion *int) ([]agent.PromptRevision, bool, error) {
	f.revCalls++
	f.lastLimit = limit
	f.lastBefore = beforeVersion
	if f.listRevErr != nil {
		return nil, false, f.listRevErr
	}
	filtered := make([]agent.PromptRevision, 0)
	for _, revision := range f.revisions[profileKey] {
		if beforeVersion != nil && revision.Version >= *beforeVersion {
			continue
		}
		filtered = append(filtered, revision)
	}
	hasMore := len(filtered) > limit
	if hasMore {
		filtered = filtered[:limit]
	}
	return filtered, hasMore, nil
}

type fakeProfileAudit struct {
	events []auditapp.Event
	err    error
}

func (f *fakeProfileAudit) Record(_ context.Context, event auditapp.Event) error {
	f.events = append(f.events, event)
	return f.err
}

type fakeModelCatalog struct {
	models map[string]bool
	err    error
	calls  []string
}

func (f *fakeModelCatalog) ResolveModel(_ context.Context, model string) (llmapp.ModelInfo, error) {
	f.calls = append(f.calls, model)
	if f.err != nil {
		return llmapp.ModelInfo{}, f.err
	}
	if !f.models[model] {
		return llmapp.ModelInfo{}, errors.New("model not in catalog")
	}
	return llmapp.ModelInfo{Model: model, ContextWindow: 64000, MaxOutput: 8000}, nil
}

type stubProfileTool struct {
	name        string
	description string
	parameters  json.RawMessage
}

func (s stubProfileTool) Name() string                     { return s.name }
func (s stubProfileTool) Description() string              { return s.description }
func (s stubProfileTool) Parameters() json.RawMessage      { return s.parameters }
func (s stubProfileTool) Execute(context.Context, tools.Scope, json.RawMessage) (any, error) {
	return nil, errors.New("not executable in tests")
}

func profileToolRegistry(t *testing.T) *tools.Registry {
	t.Helper()
	return tools.NewRegistry(
		stubProfileTool{name: "requirement.read", description: "读取需求", parameters: json.RawMessage(`{"type":"object"}`)},
		stubProfileTool{name: "context.task_pack", description: "读取任务上下文", parameters: json.RawMessage(`{"type":"object"}`)},
	)
}

func newProfileAdminService(t *testing.T) (*agentapp.ProfileAdminService, *fakeProfileAdminRepo, *fakeProfileAudit, *fakeModelCatalog) {
	t.Helper()
	repo := newFakeProfileAdminRepo(t)
	audit := &fakeProfileAudit{}
	models := &fakeModelCatalog{models: map[string]bool{"deepseek-flash": true}}
	service := agentapp.NewProfileAdminService(repo, profileToolRegistry(t), models, audit)
	return service, repo, audit, models
}

func adminActor() platform.Actor { return platform.Actor{ID: 7, Role: "admin"} }

func findProfileView(t *testing.T, views []agentapp.ProfileView, key string) agentapp.ProfileView {
	t.Helper()
	for _, view := range views {
		if view.Key == key {
			return view
		}
	}
	t.Fatalf("profile %q not in views", key)
	return agentapp.ProfileView{}
}

/* ---------------------------------- list -------------------------------- */

func TestProfileAdminList(t *testing.T) {
	service, _, _, _ := newProfileAdminService(t)
	views, err := service.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(views) != 14 {
		t.Fatalf("views = %d, want 14", len(views))
	}
	host := findProfileView(t, views, agentapp.ProfileKeyClarificationHost)
	if host.ID == 0 || host.Key != agentapp.ProfileKeyClarificationHost {
		t.Fatalf("host = %#v", host)
	}
	if host.SystemPrompt == "" || len(host.AllowedTools) == 0 {
		t.Fatalf("admin profile view misses prompt/tools: %#v", host)
	}
	if host.PromptVersion != 1 {
		t.Fatalf("prompt version = %d", host.PromptVersion)
	}
	if !host.Enabled {
		t.Fatalf("enabled = false")
	}
}

func TestProfileAdminGet(t *testing.T) {
	service, _, _, _ := newProfileAdminService(t)
	detail, err := service.Get(context.Background(), agentapp.ProfileKeyClarificationHost)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if detail.Profile.Key != agentapp.ProfileKeyClarificationHost || detail.PromptVersion != 1 {
		t.Fatalf("detail = %#v", detail)
	}
	if detail.DefaultSystemPrompt == "" || !strings.Contains(detail.DefaultSystemPrompt, "[profile:clarification_host]") {
		t.Fatalf("default prompt = %q", detail.DefaultSystemPrompt)
	}
	if len(detail.Variables) != 8 || detail.Variables[0].Name != "Now" {
		t.Fatalf("variables = %#v", detail.Variables)
	}
}

func TestProfileAdminGetNotFound(t *testing.T) {
	service, _, _, _ := newProfileAdminService(t)
	if _, err := service.Get(context.Background(), "missing_profile"); platform.AsError(err).Status != 404 {
		t.Fatalf("err = %v", err)
	}
	if _, err := service.Get(context.Background(), "Bad Key"); platform.AsError(err).Status != 400 {
		t.Fatalf("invalid key err = %v", err)
	}
}

/* --------------------------------- patch -------------------------------- */

func TestProfileAdminPatchModelBudgetsTools(t *testing.T) {
	service, repo, audit, models := newProfileAdminService(t)
	budgets := agent.ProfileBudgets{MaxTurns: 9, SoftSeconds: 100, HardSeconds: 200}
	allowed := []string{"requirement.read", "context.task_pack", "requirement.read"}
	model := "deepseek-flash"

	view, changed, err := service.Patch(context.Background(), adminActor(), agentapp.ProfileKeyCaseExecutor, agentapp.PatchInput{
		SetModel:     true,
		Model:        &model,
		Budgets:      &budgets,
		AllowedTools: &allowed,
	})
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}
	if !changed {
		t.Fatal("changed = false")
	}
	if view.Model == nil || *view.Model != model {
		t.Fatalf("model = %v", view.Model)
	}
	if view.Budgets != budgets {
		t.Fatalf("budgets = %#v", view.Budgets)
	}
	if strings.Join(view.AllowedTools, ",") != "requirement.read,context.task_pack" {
		t.Fatalf("allowed tools = %v", view.AllowedTools)
	}
	if len(models.calls) != 1 || models.calls[0] != model {
		t.Fatalf("model catalog calls = %v", models.calls)
	}
	if repo.lastUpdate.Model == nil || *repo.lastUpdate.Model != model {
		t.Fatalf("repo update = %#v", repo.lastUpdate)
	}
	if len(audit.events) != 1 {
		t.Fatalf("audit events = %d", len(audit.events))
	}
	event := audit.events[0]
	if event.Action != agentapp.ActionProfileUpdated || event.TargetKind != "agent_profile" {
		t.Fatalf("audit event = %#v", event)
	}
	if event.ActorID == nil || *event.ActorID != 7 {
		t.Fatalf("audit actor = %v", event.ActorID)
	}
	for key := range event.Metadata {
		switch key {
		case "profile_key", "fields", "prompt_version", "prompt_sha256", "prompt_length":
		default:
			t.Fatalf("audit metadata key %q not allowed", key)
		}
	}
	if _, ok := event.Metadata["system_prompt"]; ok {
		t.Fatal("audit metadata carries the prompt body")
	}
	if event.Metadata["profile_key"] != agentapp.ProfileKeyCaseExecutor {
		t.Fatalf("metadata profile_key = %v", event.Metadata["profile_key"])
	}
}

func TestProfileAdminPatchClearsModel(t *testing.T) {
	service, repo, _, _ := newProfileAdminService(t)
	existing := "deepseek-flash"
	profile := repo.profiles[agentapp.ProfileKeyClarificationHost]
	profile.Model = &existing
	repo.profiles[agentapp.ProfileKeyClarificationHost] = profile
	empty := "  "
	view, changed, err := service.Patch(context.Background(), adminActor(), agentapp.ProfileKeyClarificationHost, agentapp.PatchInput{
		SetModel: true,
		Model:    &empty,
	})
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}
	if !changed || view.Model != nil {
		t.Fatalf("view = %#v changed=%v", view, changed)
	}
	if repo.lastUpdate.Model != nil {
		t.Fatalf("repo model = %v, want nil", repo.lastUpdate.Model)
	}
}

func TestProfileAdminPatchValidation(t *testing.T) {
	service, repo, _, _ := newProfileAdminService(t)
	unknownModel := "not-a-model"
	unknownTool := []string{"bogus.tool"}
	negative := agent.ProfileBudgets{MaxTurns: -1}
	softAfterHard := agent.ProfileBudgets{SoftSeconds: 10, HardSeconds: 5}

	cases := []struct {
		name  string
		input agentapp.PatchInput
		field string
	}{
		{name: "model", input: agentapp.PatchInput{SetModel: true, Model: &unknownModel}, field: "model"},
		{name: "tool", input: agentapp.PatchInput{AllowedTools: &unknownTool}, field: "allowed_tools"},
		{name: "negative budget", input: agentapp.PatchInput{Budgets: &negative}, field: "budgets.max_turns"},
		{name: "soft after hard", input: agentapp.PatchInput{Budgets: &softAfterHard}, field: "budgets"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := service.Patch(context.Background(), adminActor(), agentapp.ProfileKeyClarificationHost, tc.input)
			if err == nil {
				t.Fatal("expected validation error")
			}
			apiErr := platform.AsError(err)
			if apiErr.Status != 400 || apiErr.Details["field"] != tc.field {
				t.Fatalf("err = %#v", apiErr)
			}
			if repo.updateCalls != 0 {
				t.Fatalf("repo update calls = %d", repo.updateCalls)
			}
		})
	}
}

func TestProfileAdminPatchRequiresAdmin(t *testing.T) {
	service, repo, audit, _ := newProfileAdminService(t)
	_, _, err := service.Patch(context.Background(), platform.Actor{ID: 9, Role: "user"}, agentapp.ProfileKeyClarificationHost, agentapp.PatchInput{})
	if platform.AsError(err).Status != 403 {
		t.Fatalf("err = %v", err)
	}
	if repo.updateCalls != 0 || len(audit.events) != 0 {
		t.Fatal("non-admin patch reached the repository/audit")
	}
}

func TestProfileAdminPatchNoChange(t *testing.T) {
	serviceRepo := newFakeProfileAdminRepo(t)
	host := serviceRepo.profiles[agentapp.ProfileKeyClarificationHost]
	budgets := host.Budgets
	view, changed, err := agentapp.NewProfileAdminService(serviceRepo, profileToolRegistry(t),
		&fakeModelCatalog{models: map[string]bool{}}, nil).
		Patch(context.Background(), adminActor(), host.Key, agentapp.PatchInput{Budgets: &budgets})
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}
	if changed {
		t.Fatal("changed = true for identical budgets")
	}
	if view.Key != host.Key {
		t.Fatalf("view = %#v", view)
	}
}

func TestProfileAdminPatchNotFound(t *testing.T) {
	service, _, _, _ := newProfileAdminService(t)
	_, _, err := service.Patch(context.Background(), adminActor(), "missing_profile", agentapp.PatchInput{})
	if platform.AsError(err).Status != 404 {
		t.Fatalf("err = %v", err)
	}
}

/* ------------------------------- prompts -------------------------------- */

func TestProfileAdminSavePrompt(t *testing.T) {
	service, repo, audit, _ := newProfileAdminService(t)
	expected := 1
	nextPrompt := "[profile:clarification_host] 新提示词 {{.VersionTitle}}"
	view, version, changed, err := service.SavePrompt(context.Background(), adminActor(), agentapp.ProfileKeyClarificationHost, agentapp.SavePromptInput{
		SystemPrompt:    nextPrompt,
		Note:            "编辑器保存",
		ExpectedVersion: &expected,
	})
	if err != nil {
		t.Fatalf("SavePrompt: %v", err)
	}
	if !changed || version != 2 || view.PromptVersion != 2 {
		t.Fatalf("view=%#v version=%d changed=%v", view, version, changed)
	}
	if view.SystemPrompt != nextPrompt {
		t.Fatalf("view prompt = %q", view.SystemPrompt)
	}
	if repo.lastWrite.Source != agent.PromptSourceAdmin || repo.lastWrite.Note != "编辑器保存" {
		t.Fatalf("write = %#v", repo.lastWrite)
	}
	if repo.lastWrite.ExpectedVersion == nil || *repo.lastWrite.ExpectedVersion != 1 {
		t.Fatalf("expected version = %v", repo.lastWrite.ExpectedVersion)
	}
	if len(audit.events) != 1 {
		t.Fatalf("audit events = %d", len(audit.events))
	}
	metadata := audit.events[0].Metadata
	if metadata["prompt_version"] != 2 {
		t.Fatalf("metadata version = %v", metadata["prompt_version"])
	}
	if metadata["prompt_length"] != len([]rune(nextPrompt)) {
		t.Fatalf("metadata length = %v", metadata["prompt_length"])
	}
	if _, ok := metadata["prompt_sha256"].(string); !ok {
		t.Fatalf("metadata sha = %v", metadata["prompt_sha256"])
	}
	if _, ok := metadata["system_prompt"]; ok {
		t.Fatal("audit metadata carries prompt body")
	}
}

func TestProfileAdminSavePromptNoOp(t *testing.T) {
	service, _, _, _ := newProfileAdminService(t)
	hostPrompt, _ := agentapp.DefaultPrompt(agentapp.ProfileKeyClarificationHost)
	_, version, changed, err := service.SavePrompt(context.Background(), adminActor(), agentapp.ProfileKeyClarificationHost, agentapp.SavePromptInput{
		SystemPrompt: hostPrompt,
	})
	if err != nil {
		t.Fatalf("SavePrompt: %v", err)
	}
	if changed || version != 1 {
		t.Fatalf("changed=%v version=%d", changed, version)
	}
}

func TestProfileAdminSavePromptVersionConflict(t *testing.T) {
	service, _, _, _ := newProfileAdminService(t)
	expected := 9
	_, _, _, err := service.SavePrompt(context.Background(), adminActor(), agentapp.ProfileKeyClarificationHost, agentapp.SavePromptInput{
		SystemPrompt:    "[profile:clarification_host] 版本冲突",
		ExpectedVersion: &expected,
	})
	apiErr := platform.AsError(err)
	if apiErr.Status != 409 || apiErr.Code != "state_conflict" {
		t.Fatalf("err = %#v", apiErr)
	}
}

func TestProfileAdminSavePromptValidation(t *testing.T) {
	service, repo, audit, _ := newProfileAdminService(t)
	long := "[profile:clarification_host] " + strings.Repeat("x", agent.MaxPromptRunes)
	cases := []struct {
		name   string
		prompt string
		reason string
	}{
		{name: "missing marker", prompt: "没有标记", reason: "missing_profile_marker"},
		{name: "unknown variable", prompt: "[profile:clarification_host] {{.Bogus}}", reason: "unknown_variable"},
		{name: "too long", prompt: long},
		{name: "empty", prompt: "  "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := service.SavePrompt(context.Background(), adminActor(), agentapp.ProfileKeyClarificationHost, agentapp.SavePromptInput{
				SystemPrompt: tc.prompt,
			})
			apiErr := platform.AsError(err)
			if apiErr.Status != 400 || apiErr.Details["field"] != "system_prompt" {
				t.Fatalf("err = %#v", apiErr)
			}
			if tc.reason != "" && apiErr.Details["reason"] != tc.reason {
				t.Fatalf("reason = %#v, want %q", apiErr.Details["reason"], tc.reason)
			}
		})
	}
	if repo.saveCalls != 0 || len(audit.events) != 0 {
		t.Fatal("invalid prompt reached the repository")
	}
}

func TestProfileAdminSavePromptUnknownProfile(t *testing.T) {
	service, _, _, _ := newProfileAdminService(t)
	_, _, _, err := service.SavePrompt(context.Background(), adminActor(), "missing_profile", agentapp.SavePromptInput{
		SystemPrompt: "[profile:missing_profile] x",
	})
	if platform.AsError(err).Status != 404 {
		t.Fatalf("err = %v", err)
	}
}

func TestProfileAdminResetPrompt(t *testing.T) {
	service, repo, _, _ := newProfileAdminService(t)
	_, version, changed, err := service.ResetPrompt(context.Background(), adminActor(), agentapp.ProfileKeyClarificationHost)
	if err != nil {
		t.Fatalf("ResetPrompt: %v", err)
	}
	if !changed || version != 2 {
		t.Fatalf("version=%d changed=%v", version, changed)
	}
	defaultPrompt, _ := agentapp.DefaultPrompt(agentapp.ProfileKeyClarificationHost)
	if repo.lastWrite.SystemPrompt != defaultPrompt || repo.lastWrite.Source != agent.PromptSourceReset {
		t.Fatalf("write = %#v", repo.lastWrite)
	}
	if repo.lastWrite.NoOpOnSame {
		t.Fatal("reset must append even when the body equals current")
	}
}

func TestProfileAdminRestorePrompt(t *testing.T) {
	service, repo, _, _ := newProfileAdminService(t)
	view, version, changed, err := service.RestorePrompt(context.Background(), adminActor(), agentapp.ProfileKeyClarificationHost, 1)
	if err != nil {
		t.Fatalf("RestorePrompt: %v", err)
	}
	if !changed || version != 2 || view.PromptVersion != 2 {
		t.Fatalf("version=%d changed=%v view=%#v", version, changed, view)
	}
	if repo.lastWrite.Note != "restore v1" || repo.lastWrite.Source != agent.PromptSourceAdmin {
		t.Fatalf("write = %#v", repo.lastWrite)
	}
	if repo.lastVersionNum != 1 {
		t.Fatalf("requested version = %d", repo.lastVersionNum)
	}

	_, _, _, err = service.RestorePrompt(context.Background(), adminActor(), agentapp.ProfileKeyClarificationHost, 99)
	if platform.AsError(err).Status != 404 {
		t.Fatalf("missing version err = %v", err)
	}
	if _, _, _, err := service.RestorePrompt(context.Background(), adminActor(), agentapp.ProfileKeyClarificationHost, 0); platform.AsError(err).Status != 400 {
		t.Fatalf("zero version err = %v", err)
	}
}

func TestProfileAdminRevisionsPagination(t *testing.T) {
	service, repo, _, _ := newProfileAdminService(t)
	revisions := make([]agent.PromptRevision, 0, 120)
	for version := 120; version >= 1; version-- {
		revisions = append(revisions, agent.PromptRevision{
			Version: version, ProfileKey: agentapp.ProfileKeyClarificationHost,
			SystemPrompt: fmt.Sprintf("[profile:clarification_host] v%d", version),
			Source:       agent.PromptSourceAdmin,
		})
	}
	repo.revisions[agentapp.ProfileKeyClarificationHost] = revisions

	page, hasMore, err := service.Revisions(context.Background(), agentapp.ProfileKeyClarificationHost, 200, nil)
	if err != nil {
		t.Fatalf("Revisions: %v", err)
	}
	if !hasMore || len(page) != 100 || page[0].Version != 120 {
		t.Fatalf("page = %d rows hasMore=%v first=%d", len(page), hasMore, page[0].Version)
	}
	if repo.lastLimit != 100 {
		t.Fatalf("limit = %d, want cap 100", repo.lastLimit)
	}

	before := 3
	older, hasMore, err := service.Revisions(context.Background(), agentapp.ProfileKeyClarificationHost, 20, &before)
	if err != nil {
		t.Fatalf("Revisions older: %v", err)
	}
	if hasMore || len(older) != 2 || older[0].Version != 2 {
		t.Fatalf("older = %#v hasMore=%v", older, hasMore)
	}
	if repo.lastBefore == nil || *repo.lastBefore != 3 {
		t.Fatalf("before = %v", repo.lastBefore)
	}

	if _, _, err := service.Revisions(context.Background(), "missing_profile", 20, nil); platform.AsError(err).Status != 404 {
		t.Fatalf("missing profile err = %v", err)
	}
}

func TestProfileAdminPreview(t *testing.T) {
	service, _, _, _ := newProfileAdminService(t)
	rendered, err := service.Preview(context.Background(), agentapp.ProfileKeyClarificationHost,
		"[profile:clarification_host] {{.VersionTitle}}", map[string]string{"VersionTitle": "预览版本"})
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if rendered != "[profile:clarification_host] 预览版本" {
		t.Fatalf("rendered = %q", rendered)
	}

	current, _ := agentapp.DefaultPrompt(agentapp.ProfileKeyClarificationHost)
	rendered, err = service.Preview(context.Background(), agentapp.ProfileKeyClarificationHost, "", nil)
	if err != nil {
		t.Fatalf("Preview current: %v", err)
	}
	if rendered != current {
		t.Fatalf("current preview = %q", rendered)
	}

	if _, err := service.Preview(context.Background(), agentapp.ProfileKeyClarificationHost, "[profile:clarification_host] {{.Bogus}}", nil); platform.AsError(err).Status != 400 {
		t.Fatalf("unknown var err = %v", err)
	}
}

func TestProfileAdminTools(t *testing.T) {
	service, _, _, _ := newProfileAdminService(t)
	views, err := service.Tools()
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if len(views) != 2 || views[0].Name != "context.task_pack" {
		t.Fatalf("tools = %#v", views)
	}
	if views[0].Description == "" || len(views[0].Parameters) == 0 {
		t.Fatalf("tool view = %#v", views[0])
	}
}

func TestProfileAdminNilDependencies(t *testing.T) {
	service := agentapp.NewProfileAdminService(nil, nil, nil, nil)
	if _, err := service.List(context.Background()); err == nil {
		t.Fatal("nil repo List accepted")
	}
	if _, err := service.Tools(); err == nil {
		t.Fatal("nil registry accepted")
	}
	repo := newFakeProfileAdminRepo(t)
	service = agentapp.NewProfileAdminService(repo, nil, nil, nil)
	model := "deepseek-flash"
	if _, _, err := service.Patch(context.Background(), adminActor(), agentapp.ProfileKeyClarificationHost, agentapp.PatchInput{SetModel: true, Model: &model}); err == nil {
		t.Fatal("nil model catalog accepted")
	}
	if _, err := service.Get(context.Background(), agentapp.ProfileKeyClarificationHost); err != nil {
		t.Fatalf("Get without optional deps: %v", err)
	}
}

/* -------------------------------- helpers ------------------------------- */

func sameStringPointer(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
