package agentapp_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
)

// legacyClarificationPromptV021 is the exact v0.2.1 clarification Host seed
// prompt (the UAT database's stale revision). The backfill must recognize this
// historical body and append the current seed prompt, without touching an admin
// revision.
const legacyClarificationPromptV021 = `你是 EzTest 的澄清 Host（需求澄清助手）。
角色边界：
- 只与用户对话、解释、提问与复述；不确认需求、不删除需求、不修改已确认需求。
- 需要结构化需求时，引导需求 Curator 生成 draft 草案，不要声称已确认。
- 可以调用 context.summary_update 更新会话摘要与钉住事实。
- 同一轮需求 Curator 会并行创建 draft：requirement.read 只是读取时刻的快照；若快照为空但会话已有 requirement_card，不要声称“未创建/需求数为 0”，以 Curator 卡片与最新需求列表为准。
- 同一来源编号（如 FR-011）在版本内只对应一条 draft：需求列表/卡片已存在该编号时，不要再要求 Curator 重复创建，也不要声称会出现第二条。
- 资源目标（页面/API/version_sut）由 sut.list / sut.page_list 返回的数字 id 表示，需求草案的 targets 必须使用这些 id；不要要求用户提供数字 ID，也不要接受 path/URL 作为目标。
回答使用简洁中文；引用用户最新消息；每次最多提出 3 个澄清问题。
[profile:clarification_host]`

// fakeBackfillRepo is the in-memory ProfileBackfillRepository used by the
// backfill tests. It applies the same semantics as the Postgres repository:
// marker-fenced writes and append-only prompt revisions.
type fakeBackfillRepo struct {
	profiles    map[string]agent.AgentProfile
	revisions   map[string][]agent.PromptRevision
	writes      []agent.PromptWrite
	applied     []agent.ProfileSeedBackfill
	applyErr    error
	listErr     error
	beforeSave  func()
	beforeApply func()
}

func newFakeBackfillRepo() *fakeBackfillRepo {
	return &fakeBackfillRepo{
		profiles:  map[string]agent.AgentProfile{},
		revisions: map[string][]agent.PromptRevision{},
	}
}

func (f *fakeBackfillRepo) ListProfiles(context.Context) ([]agent.AgentProfile, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	list := make([]agent.AgentProfile, 0, len(f.profiles))
	for _, profile := range f.profiles {
		list = append(list, profile)
	}
	return list, nil
}

func (f *fakeBackfillRepo) ListPromptRevisions(
	_ context.Context, key string, limit int, _ *int,
) ([]agent.PromptRevision, bool, error) {
	revisions := f.revisions[key]
	latestFirst := make([]agent.PromptRevision, 0, len(revisions))
	for i := len(revisions) - 1; i >= 0; i-- {
		latestFirst = append(latestFirst, revisions[i])
	}
	if limit > 0 && len(latestFirst) > limit {
		return latestFirst[:limit], true, nil
	}
	return latestFirst, false, nil
}

func (f *fakeBackfillRepo) SavePromptRevision(
	ctx context.Context, write agent.PromptWrite,
) (agent.PromptWriteResult, error) {
	if f.beforeSave != nil {
		// Simulate a concurrent admin/startup writer landing between the
		// ListPromptRevisions snapshot and this write.
		hook := f.beforeSave
		f.beforeSave = nil
		hook()
	}
	if err := write.Validate(); err != nil {
		return agent.PromptWriteResult{}, err
	}
	profile, ok := f.profiles[write.ProfileKey]
	if !ok {
		return agent.PromptWriteResult{}, agent.ErrNotFound
	}
	currentVersion := len(f.revisions[write.ProfileKey])
	// Mirror the Postgres optimistic lock: expected_version must equal the
	// current max version (0 when no revision exists yet).
	if write.ExpectedVersion != nil && *write.ExpectedVersion != currentVersion {
		return agent.PromptWriteResult{}, agent.ErrPromptVersionConflict
	}
	if write.NoOpOnSame && profile.SystemPrompt == write.SystemPrompt {
		return agent.PromptWriteResult{Version: currentVersion, Changed: false}, nil
	}
	version := currentVersion + 1
	f.writes = append(f.writes, write)
	f.revisions[write.ProfileKey] = append(f.revisions[write.ProfileKey], agent.PromptRevision{
		ProfileKey:   write.ProfileKey,
		Version:      version,
		SystemPrompt: write.SystemPrompt,
		Note:         write.Note,
		Source:       write.Source,
	})
	profile.SystemPrompt = write.SystemPrompt
	f.profiles[write.ProfileKey] = profile
	return agent.PromptWriteResult{Version: version, Changed: true}, nil
}

func (f *fakeBackfillRepo) ApplyProfileSeedBackfill(
	_ context.Context, key string, update agent.ProfileSeedBackfill,
) (agent.AgentProfile, bool, error) {
	if f.applyErr != nil {
		return agent.AgentProfile{}, false, f.applyErr
	}
	profile, ok := f.profiles[key]
	if !ok {
		return agent.AgentProfile{}, false, agent.ErrNotFound
	}
	if f.beforeApply != nil {
		// Simulate a concurrent admin PATCH landing between ListProfiles and
		// the marker write, then re-read the row exactly like the SQL does
		// against the current row.
		hook := f.beforeApply
		f.beforeApply = nil
		hook()
		profile, ok = f.profiles[key]
		if !ok {
			return agent.AgentProfile{}, false, agent.ErrNotFound
		}
	}
	if profile.SeedVersion >= update.SeedVersion {
		return profile, false, nil
	}
	f.applied = append(f.applied, update)
	if len(update.AddAllowedTools) > 0 {
		profile.AllowedTools = unionProfileTools(profile.AllowedTools, update.AddAllowedTools)
	}
	if upgrade := update.BudgetUpgrade; upgrade != nil && profile.Budgets == upgrade.Legacy {
		profile.Budgets = upgrade.Current
	}
	profile.SeedVersion = update.SeedVersion
	f.profiles[key] = profile
	return profile, true, nil
}

// unionProfileTools mirrors the repository's set union against the current row.
func unionProfileTools(stored, additions []string) []string {
	seen := make(map[string]struct{}, len(stored)+len(additions))
	merged := make([]string, 0, len(stored)+len(additions))
	for _, list := range [][]string{stored, additions} {
		for _, name := range list {
			if _, duplicate := seen[name]; duplicate {
				continue
			}
			seen[name] = struct{}{}
			merged = append(merged, name)
		}
	}
	return merged
}

func seedLegacyClarification(repo *fakeBackfillRepo) agent.AgentProfile {
	profile := agent.AgentProfile{
		ID:           11,
		Key:          agentapp.ProfileKeyClarificationHost,
		Name:         "澄清 Host",
		SystemPrompt: legacyClarificationPromptV021,
		AllowedTools: []string{
			"context.version_pack", "context.task_pack", "context.team_room_read",
			"context.summary_update", "requirement.read", "attachment.list",
			"attachment.read_text", "sut.list", "sut.page_list", "admin.custom_tool",
		},
		Model:       ptrString("deepseek-admin-model"),
		Budgets:     agent.ProfileBudgets{MaxTurns: 12, SoftSeconds: 300, HardSeconds: 600},
		Enabled:     true,
		SeedVersion: 0,
	}
	repo.profiles[profile.Key] = profile
	repo.revisions[profile.Key] = []agent.PromptRevision{{
		ID:           1,
		ProfileKey:   profile.Key,
		Version:      1,
		SystemPrompt: legacyClarificationPromptV021,
		Source:       agent.PromptSourceSeed,
	}}
	return profile
}

func ptrString(value string) *string { return &value }

func toolListContains(tools []string, want string) bool {
	for _, name := range tools {
		if name == want {
			return true
		}
	}
	return false
}

func TestBackfillProfilesUpgradesLegacyProfileOnce(t *testing.T) {
	repo := newFakeBackfillRepo()
	seedLegacyClarification(repo)
	audit := &fakeProfileAudit{}
	ctx := context.Background()

	if err := agentapp.BackfillProfiles(ctx, repo, audit); err != nil {
		t.Fatalf("BackfillProfiles: %v", err)
	}
	got := repo.profiles[agentapp.ProfileKeyClarificationHost]
	for _, name := range agentapp.V022ProfileTools()[agentapp.ProfileKeyClarificationHost] {
		if !toolListContains(got.AllowedTools, name) {
			t.Fatalf("backfilled clarification_host is missing %q: %v", name, got.AllowedTools)
		}
	}
	if !toolListContains(got.AllowedTools, "admin.custom_tool") {
		t.Fatalf("admin-added tool lost: %v", got.AllowedTools)
	}
	if got.Budgets != (agent.ProfileBudgets{MaxTurns: 12, SoftSeconds: 600, HardSeconds: 900}) {
		t.Fatalf("budgets = %+v, want 12/600/900", got.Budgets)
	}
	if got.Model == nil || *got.Model != "deepseek-admin-model" {
		t.Fatalf("admin model lost: %v", got.Model)
	}
	if got.SeedVersion != agentapp.ProfileSeedVersion {
		t.Fatalf("seed version = %d, want %d", got.SeedVersion, agentapp.ProfileSeedVersion)
	}
	// The seed prompt revision is appended and mirrored into the profile body so
	// the next run actually uses the upgraded Host prompt.
	wantPrompt, ok := agentapp.DefaultPrompt(agentapp.ProfileKeyClarificationHost)
	if !ok {
		t.Fatal("DefaultPrompt missing")
	}
	if got.SystemPrompt != wantPrompt {
		t.Fatalf("upgraded prompt does not match the current seed")
	}
	revisions := repo.revisions[agentapp.ProfileKeyClarificationHost]
	if len(revisions) != 2 {
		t.Fatalf("prompt revisions = %d, want 2", len(revisions))
	}
	last := revisions[len(revisions)-1]
	if last.Source != agent.PromptSourceSeed || last.Version != 2 || !strings.Contains(last.Note, "0.2.3") {
		t.Fatalf("upgrade revision = %#v", last)
	}
	if last.SystemPrompt != wantPrompt {
		t.Fatalf("upgrade revision body is not the current seed prompt")
	}
	if len(audit.events) != 1 || audit.events[0].Action != "agent_profile.backfilled" {
		t.Fatalf("audit events = %#v", audit.events)
	}
	if audit.events[0].Metadata["profile_key"] != agentapp.ProfileKeyClarificationHost {
		t.Fatalf("audit metadata = %#v", audit.events[0].Metadata)
	}
	if _, leaked := audit.events[0].Metadata["system_prompt"]; leaked {
		t.Fatal("audit event leaked the prompt body")
	}

	// An admin deletion after the marker must never be re-added, even though
	// the row is still on the same seed version.
	admin := repo.profiles[agentapp.ProfileKeyClarificationHost]
	removed := make([]string, 0, len(admin.AllowedTools))
	for _, name := range admin.AllowedTools {
		if name != "question.ask" {
			removed = append(removed, name)
		}
	}
	admin.AllowedTools = removed
	admin.Budgets = agent.ProfileBudgets{MaxTurns: 9, SoftSeconds: 100, HardSeconds: 200}
	repo.profiles[admin.Key] = admin
	revisionCount := len(repo.revisions[admin.Key])

	if err := agentapp.BackfillProfiles(ctx, repo, audit); err != nil {
		t.Fatalf("second BackfillProfiles: %v", err)
	}
	second := repo.profiles[admin.Key]
	if toolListContains(second.AllowedTools, "question.ask") {
		t.Fatalf("admin-removed tool was resurrected: %v", second.AllowedTools)
	}
	if second.Budgets != (agent.ProfileBudgets{MaxTurns: 9, SoftSeconds: 100, HardSeconds: 200}) {
		t.Fatalf("admin budgets overwritten: %+v", second.Budgets)
	}
	if len(repo.revisions[admin.Key]) != revisionCount {
		t.Fatalf("second backfill appended another prompt revision")
	}
}

func TestBackfillProfilesUpgradesLegacyBodyWithoutRevision(t *testing.T) {
	repo := newFakeBackfillRepo()
	profile := seedLegacyClarification(repo)
	delete(repo.revisions, profile.Key)

	if err := agentapp.BackfillProfiles(context.Background(), repo, nil); err != nil {
		t.Fatalf("BackfillProfiles: %v", err)
	}
	got := repo.profiles[profile.Key]
	wantPrompt, _ := agentapp.DefaultPrompt(profile.Key)
	if got.SystemPrompt != wantPrompt {
		t.Fatalf("pre-revision legacy prompt was not upgraded")
	}
	if len(repo.writes) != 1 || repo.writes[0].Source != agent.PromptSourceSeed ||
		!strings.Contains(repo.writes[0].Note, "0.2.3") {
		t.Fatalf("prompt writes = %#v", repo.writes)
	}
	if got.SeedVersion != agentapp.ProfileSeedVersion {
		t.Fatalf("seed version = %d, want marker set", got.SeedVersion)
	}
}

func TestBackfillProfilesPromptConflictKeepsConcurrentAdminRevision(t *testing.T) {
	repo := newFakeBackfillRepo()
	profile := seedLegacyClarification(repo)
	delete(repo.revisions, profile.Key)

	adminBody := "[profile:clarification_host] 管理员并发写入"
	repo.beforeSave = func() {
		// The admin PATCH wins the race: it lands between the revision snapshot
		// and the seed write.
		repo.revisions[profile.Key] = []agent.PromptRevision{{
			ID:           99,
			ProfileKey:   profile.Key,
			Version:      1,
			SystemPrompt: adminBody,
			Source:       agent.PromptSourceAdmin,
		}}
		current := repo.profiles[profile.Key]
		current.SystemPrompt = adminBody
		repo.profiles[profile.Key] = current
	}

	if err := agentapp.BackfillProfiles(context.Background(), repo, nil); err != nil {
		t.Fatalf("BackfillProfiles: %v", err)
	}
	got := repo.profiles[profile.Key]
	if got.SystemPrompt != adminBody {
		t.Fatalf("concurrent admin prompt overwritten: %q", got.SystemPrompt)
	}
	revisions := repo.revisions[profile.Key]
	if len(revisions) != 1 || revisions[0].Source != agent.PromptSourceAdmin {
		t.Fatalf("seed write was not fenced: %#v", revisions)
	}
	if got.SeedVersion != agentapp.ProfileSeedVersion {
		t.Fatalf("seed version = %d, want %d", got.SeedVersion, agentapp.ProfileSeedVersion)
	}
}

func TestBackfillProfilesDuplicateStartAppendsOneSeedRevision(t *testing.T) {
	repo := newFakeBackfillRepo()
	profile := seedLegacyClarification(repo)
	delete(repo.revisions, profile.Key)
	wantPrompt, ok := agentapp.DefaultPrompt(profile.Key)
	if !ok {
		t.Fatal("DefaultPrompt missing")
	}

	repo.beforeSave = func() {
		// A concurrent startup instance wins the no-revision race and appends
		// the current seed revision at expected_version 0.
		zero := 0
		if _, err := repo.SavePromptRevision(context.Background(), agent.PromptWrite{
			ProfileKey:      profile.Key,
			SystemPrompt:    wantPrompt,
			Note:            "upgrade 0.2.3",
			Source:          agent.PromptSourceSeed,
			ExpectedVersion: &zero,
		}); err != nil {
			t.Fatalf("concurrent seed write: %v", err)
		}
	}

	if err := agentapp.BackfillProfiles(context.Background(), repo, nil); err != nil {
		t.Fatalf("BackfillProfiles: %v", err)
	}
	revisions := repo.revisions[profile.Key]
	if len(revisions) != 1 {
		t.Fatalf("duplicate startup appended %d seed revisions", len(revisions)-1)
	}
	if revisions[0].Source != agent.PromptSourceSeed || revisions[0].SystemPrompt != wantPrompt {
		t.Fatalf("winning revision = %#v", revisions[0])
	}
	got := repo.profiles[profile.Key]
	if got.SystemPrompt != wantPrompt {
		t.Fatalf("profile body = %q, want the single seed body", got.SystemPrompt)
	}
	if got.SeedVersion != agentapp.ProfileSeedVersion {
		t.Fatalf("seed version = %d, want %d", got.SeedVersion, agentapp.ProfileSeedVersion)
	}
}

func TestBackfillProfilesKeepsConcurrentAdminEdits(t *testing.T) {
	repo := newFakeBackfillRepo()
	profile := seedLegacyClarification(repo)
	concurrentBudgets := agent.ProfileBudgets{MaxTurns: 7, SoftSeconds: 50, HardSeconds: 90}
	repo.beforeApply = func() {
		// Admin PATCH between ListProfiles and the marker write: a new tool and
		// a custom budget must survive the backfill.
		current := repo.profiles[profile.Key]
		current.AllowedTools = append(append([]string(nil), current.AllowedTools...), "admin.concurrent_tool")
		current.Budgets = concurrentBudgets
		repo.profiles[profile.Key] = current
	}

	if err := agentapp.BackfillProfiles(context.Background(), repo, nil); err != nil {
		t.Fatalf("BackfillProfiles: %v", err)
	}
	got := repo.profiles[profile.Key]
	for _, name := range agentapp.V022ProfileTools()[profile.Key] {
		if !toolListContains(got.AllowedTools, name) {
			t.Fatalf("backfill dropped the v0.22 increment %q: %v", name, got.AllowedTools)
		}
	}
	if !toolListContains(got.AllowedTools, "admin.custom_tool") ||
		!toolListContains(got.AllowedTools, "admin.concurrent_tool") {
		t.Fatalf("admin tools lost: %v", got.AllowedTools)
	}
	if got.Budgets != concurrentBudgets {
		t.Fatalf("concurrent admin budgets overwritten: %+v, want %+v", got.Budgets, concurrentBudgets)
	}
	if got.SeedVersion != agentapp.ProfileSeedVersion {
		t.Fatalf("seed version = %d, want %d", got.SeedVersion, agentapp.ProfileSeedVersion)
	}
}

func TestBackfillProfilesKeepsAdminPromptRevision(t *testing.T) {
	repo := newFakeBackfillRepo()
	profile := seedLegacyClarification(repo)
	adminBody := "[profile:clarification_host] 管理员自定义提示词"
	profile.SystemPrompt = adminBody
	repo.profiles[profile.Key] = profile
	repo.revisions[profile.Key] = []agent.PromptRevision{{
		ID:           2,
		ProfileKey:   profile.Key,
		Version:      1,
		SystemPrompt: adminBody,
		Source:       agent.PromptSourceAdmin,
	}}

	if err := agentapp.BackfillProfiles(context.Background(), repo, nil); err != nil {
		t.Fatalf("BackfillProfiles: %v", err)
	}
	got := repo.profiles[profile.Key]
	if got.SystemPrompt != adminBody {
		t.Fatalf("admin prompt overwritten: %q", got.SystemPrompt)
	}
	if len(repo.revisions[profile.Key]) != 1 || len(repo.writes) != 0 {
		t.Fatalf("admin revision was appended to: %#v", repo.writes)
	}
	if got.SeedVersion != agentapp.ProfileSeedVersion {
		t.Fatalf("seed version = %d, want marker set", got.SeedVersion)
	}
	if got.Budgets != (agent.ProfileBudgets{MaxTurns: 12, SoftSeconds: 600, HardSeconds: 900}) {
		t.Fatalf("budgets = %+v, want legacy default upgraded", got.Budgets)
	}
}

func TestBackfillProfilesDoesNotUpgradeUnknownSeedPrompt(t *testing.T) {
	repo := newFakeBackfillRepo()
	profile := seedLegacyClarification(repo)
	unknown := "[profile:clarification_host] 不是任何内置种子"
	profile.SystemPrompt = unknown
	repo.profiles[profile.Key] = profile
	repo.revisions[profile.Key] = []agent.PromptRevision{{
		ID:           3,
		ProfileKey:   profile.Key,
		Version:      1,
		SystemPrompt: unknown,
		Source:       agent.PromptSourceSeed,
	}}

	if err := agentapp.BackfillProfiles(context.Background(), repo, nil); err != nil {
		t.Fatalf("BackfillProfiles: %v", err)
	}
	got := repo.profiles[profile.Key]
	if got.SystemPrompt != unknown || len(repo.writes) != 0 {
		t.Fatalf("unknown seed prompt was overwritten: %q writes=%d", got.SystemPrompt, len(repo.writes))
	}
	if got.SeedVersion != agentapp.ProfileSeedVersion {
		t.Fatalf("seed version = %d, want marker set", got.SeedVersion)
	}
}

func TestBackfillProfilesSkipsCurrentSeedVersion(t *testing.T) {
	repo := newFakeBackfillRepo()
	profile := seedLegacyClarification(repo)
	profile.SeedVersion = agentapp.ProfileSeedVersion
	repo.profiles[profile.Key] = profile
	audit := &fakeProfileAudit{}

	if err := agentapp.BackfillProfiles(context.Background(), repo, audit); err != nil {
		t.Fatalf("BackfillProfiles: %v", err)
	}
	got := repo.profiles[profile.Key]
	if got.Budgets.SoftSeconds != 300 {
		t.Fatalf("already-marked row was rewritten: %+v", got.Budgets)
	}
	if len(repo.writes) != 0 || len(repo.applied) != 0 || len(audit.events) != 0 {
		t.Fatalf("already-marked row was processed: writes=%d applied=%d audit=%d",
			len(repo.writes), len(repo.applied), len(audit.events))
	}
}

func TestBackfillProfilesRequiresRepository(t *testing.T) {
	if err := agentapp.BackfillProfiles(context.Background(), nil, nil); err == nil {
		t.Fatal("expected an unavailable error for a nil repository")
	}
}

func TestBackfillProfilesPropagatesRepositoryErrors(t *testing.T) {
	want := errors.New("list boom")
	repo := newFakeBackfillRepo()
	repo.listErr = want
	if err := agentapp.BackfillProfiles(context.Background(), repo, nil); !errors.Is(err, want) {
		t.Fatalf("list error = %v", err)
	}

	repo = newFakeBackfillRepo()
	seedLegacyClarification(repo)
	repo.applyErr = errors.New("apply boom")
	if err := agentapp.BackfillProfiles(context.Background(), repo, nil); err == nil {
		t.Fatal("expected the apply error to propagate")
	}
}

var _ auditapp.Recorder = (*fakeProfileAudit)(nil)
