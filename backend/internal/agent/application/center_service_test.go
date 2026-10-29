package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// centerFakeProfiles is a programmable application.ProfileLister.
type centerFakeProfiles struct {
	profiles []domain.AgentProfile
	err      error
	calls    int
}

// List implements application.ProfileLister.
func (f *centerFakeProfiles) List(context.Context) ([]domain.AgentProfile, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.profiles, nil
}

// centerFakeReads is a programmable domain.CenterReadRepository.
type centerFakeReads struct {
	stats    []domain.ProfileRunStats
	statsErr error
	rows     []domain.CenterSessionRow
	rowsErr  error

	statsCalls   int
	sessionCalls int
	lastFilter   domain.CenterSessionFilter
}

// ProfileRunStats implements domain.CenterReadRepository.
func (f *centerFakeReads) ProfileRunStats(context.Context) ([]domain.ProfileRunStats, error) {
	f.statsCalls++
	if f.statsErr != nil {
		return nil, f.statsErr
	}
	return f.stats, nil
}

// CenterSessions implements domain.CenterReadRepository.
func (f *centerFakeReads) CenterSessions(_ context.Context, filter domain.CenterSessionFilter) ([]domain.CenterSessionRow, error) {
	f.sessionCalls++
	f.lastFilter = filter
	if f.rowsErr != nil {
		return nil, f.rowsErr
	}
	return f.rows, nil
}

// centerFakeQuality is a programmable application.QualityReader.
type centerFakeQuality struct {
	latest *application.CenterEval
	err    error
	calls  int
}

// LatestEvaluation implements application.QualityReader.
func (f *centerFakeQuality) LatestEvaluation(context.Context) (*application.CenterEval, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.latest, nil
}

// centerProfileKeys is the frozen role → profile-key order of spec §8.1: the 11
// configured profiles in catalog order, with report and knowledge last.
var centerProfileKeys = []string{
	"clarification-dialog",
	"requirement-analysis",
	"context-recon",
	"app-exploration",
	"case-design",
	"case-review",
	"case-data-plan",
	"execution-agent",
	"supplement-agent",
	"failure-analysis-agent",
	"issue-triage-agent",
}

// centerProfiles returns one stored profile per role, with a secret prompt so a
// leak test can prove the catalog never projects it.
func centerProfiles() []domain.AgentProfile {
	profiles := make([]domain.AgentProfile, 0, len(centerProfileKeys))
	for index, key := range centerProfileKeys {
		profiles = append(profiles, domain.AgentProfile{
			ID:           int64(index + 1),
			Key:          key,
			Name:         key + " name",
			Enabled:      true,
			SystemPrompt: "super-secret-prompt-" + key,
			Model:        "deepseek-chat",
			AllowedTools: []string{"tool.one"},
		})
	}
	return profiles
}

// newCenterService builds the service under test with the supplied fakes.
func newCenterService(profiles application.ProfileLister, reads domain.CenterReadRepository, quality application.QualityReader) *application.CenterService {
	return application.NewCenterService(application.CenterServiceDeps{
		Profiles: profiles,
		Reads:    reads,
		Quality:  quality,
	})
}

// TestCenterCatalogListsThirteenRolesInSpecOrder pins the D60 catalog: the 13
// logical roles in spec order, the 11 profile mappings and the two
// profile-less roles marked deterministic/manual rather than faked.
func TestCenterCatalogListsThirteenRolesInSpecOrder(t *testing.T) {
	service := newCenterService(&centerFakeProfiles{profiles: centerProfiles()}, &centerFakeReads{}, nil)

	catalog, err := service.Catalog(context.Background())
	if err != nil {
		t.Fatalf("Catalog() error = %v", err)
	}
	if got, want := len(catalog.Roles), 13; got != want {
		t.Fatalf("len(roles) = %d, want %d", got, want)
	}

	want := []struct {
		role    string
		name    string
		profile string
		mode    string
	}{
		{"clarification-dialog", "澄清对话", "clarification-dialog", application.CenterModeAgent},
		{"requirement-analysis", "需求分析", "requirement-analysis", application.CenterModeAgent},
		{"context-recon", "上下文侦察", "context-recon", application.CenterModeAgent},
		{"app-exploration", "应用探索", "app-exploration", application.CenterModeAgent},
		{"case-design", "测试设计", "case-design", application.CenterModeAgent},
		{"case-review", "设计评审", "case-review", application.CenterModeAgent},
		{"case-data-plan", "数据/前置规划", "case-data-plan", application.CenterModeAgent},
		{"execution-agent", "执行", "execution-agent", application.CenterModeAgent},
		{"supplement-agent", "补全", "supplement-agent", application.CenterModeAgent},
		{"failure-analysis-agent", "失败分析", "failure-analysis-agent", application.CenterModeAgent},
		{"issue-triage-agent", "问题整理", "issue-triage-agent", application.CenterModeAgent},
		{"report", "报告整理", "", application.CenterModeDeterministic},
		{"knowledge", "知识沉淀", "", application.CenterModeManualReview},
	}

	for index, expected := range want {
		view := catalog.Roles[index]
		if view.Order != index+1 {
			t.Errorf("role[%d].Order = %d, want %d", index, view.Order, index+1)
		}
		if view.Role != expected.role || view.Name != expected.name || view.Mode != expected.mode {
			t.Errorf("role[%d] = (%q, %q, %q), want (%q, %q, %q)",
				index, view.Role, view.Name, view.Mode, expected.role, expected.name, expected.mode)
		}
		if expected.profile == "" {
			if view.ProfileKey != nil || view.ProfileID != nil {
				t.Errorf("role[%q] must not fake a profile, got key=%v id=%v",
					expected.role, derefString(view.ProfileKey), derefInt64(view.ProfileID))
			}
			if view.Note == "" {
				t.Errorf("role[%q] must explain the profile-less mode", expected.role)
			}
			if len(view.AllowedTools) != 0 {
				t.Errorf("role[%q].AllowedTools = %v, want none", expected.role, view.AllowedTools)
			}
		} else {
			if view.ProfileKey == nil || *view.ProfileKey != expected.profile {
				t.Errorf("role[%q].ProfileKey = %v, want %q",
					expected.role, derefString(view.ProfileKey), expected.profile)
			}
			if view.Note != "" {
				t.Errorf("role[%q].Note = %q, want empty for a configured profile", expected.role, view.Note)
			}
		}
	}
}

// TestCenterCatalogReportAndKnowledgeNotes pins the exact “no independent
// profile” labels so the UI can render them without inventing data.
func TestCenterCatalogReportAndKnowledgeNotes(t *testing.T) {
	service := newCenterService(&centerFakeProfiles{profiles: centerProfiles()}, &centerFakeReads{}, nil)

	catalog, err := service.Catalog(context.Background())
	if err != nil {
		t.Fatalf("Catalog() error = %v", err)
	}
	if got := catalog.Roles[11].Note; !strings.Contains(got, "确定性") {
		t.Errorf("report note = %q, want it to mention 确定性", got)
	}
	if got := catalog.Roles[12].Note; !strings.Contains(got, "人工审核") {
		t.Errorf("knowledge note = %q, want it to mention 人工审核", got)
	}
}

// TestCenterCatalogZeroStatsWhenNoRuns proves an empty run history is reported
// as zeros, never as fabricated demo data.
func TestCenterCatalogZeroStatsWhenNoRuns(t *testing.T) {
	// The fake repository returns no stats rows: every profile has zero runs.
	service := newCenterService(&centerFakeProfiles{profiles: centerProfiles()}, &centerFakeReads{}, nil)

	catalog, err := service.Catalog(context.Background())
	if err != nil {
		t.Fatalf("Catalog() error = %v", err)
	}
	zero := application.CenterRoleStats{}
	for _, view := range catalog.Roles {
		if view.Stats != zero {
			t.Errorf("role[%q].Stats = %+v, want zero stats", view.Role, view.Stats)
		}
	}
}

// TestCenterCatalogMapsProfileFieldsAndStats pins the profile projection:
// enabled/model/tools/run statistics are copied, but the prompt never is.
func TestCenterCatalogMapsProfileFieldsAndStats(t *testing.T) {
	profiles := centerProfiles()
	profiles[0].Enabled = false
	profiles[0].Model = "deepseek-pro"
	profiles[0].AllowedTools = []string{"tool.b", "tool.a"}
	reads := &centerFakeReads{stats: []domain.ProfileRunStats{{
		ProfileID:      1,
		ProfileKey:     "clarification-dialog",
		RunCount:       4,
		SucceededCount: 3,
		FailedCount:    1,
	}}}
	service := newCenterService(&centerFakeProfiles{profiles: profiles}, reads, nil)

	catalog, err := service.Catalog(context.Background())
	if err != nil {
		t.Fatalf("Catalog() error = %v", err)
	}
	view := catalog.Roles[0]
	if view.Enabled {
		t.Error("role[clarification-dialog].Enabled = true, want the stored false value")
	}
	if view.Model != "deepseek-pro" {
		t.Errorf("Model = %q, want deepseek-pro", view.Model)
	}
	if got := strings.Join(view.AllowedTools, ","); got != "tool.b,tool.a" {
		t.Errorf("AllowedTools = %q, want tool.b,tool.a", got)
	}
	if view.ProfileID == nil || *view.ProfileID != 1 {
		t.Errorf("ProfileID = %v, want 1", derefInt64(view.ProfileID))
	}
	want := application.CenterRoleStats{RunCount: 4, SucceededCount: 3, FailedCount: 1, SuccessRate: 0.75}
	if view.Stats != want {
		t.Errorf("Stats = %+v, want %+v", view.Stats, want)
	}
	if view.PromptVersion != nil {
		t.Errorf("PromptVersion = %v, want nil (profiles carry no prompt version)", *view.PromptVersion)
	}
}

// TestCenterCatalogNeverLeaksPromptOrSecrets proves the catalog projection drops
// the stored system prompt and any secret-shaped field.
func TestCenterCatalogNeverLeaksPromptOrSecrets(t *testing.T) {
	profiles := centerProfiles()
	profiles[0].SystemPrompt = "super-secret-prompt"
	profiles[0].Description = "SSH key sk-live-do-not-leak"
	service := newCenterService(&centerFakeProfiles{profiles: profiles}, &centerFakeReads{}, nil)

	catalog, err := service.Catalog(context.Background())
	if err != nil {
		t.Fatalf("Catalog() error = %v", err)
	}
	encoded, err := json.Marshal(catalog)
	if err != nil {
		t.Fatalf("marshal catalog: %v", err)
	}
	for _, forbidden := range []string{
		"super-secret-prompt",
		"sk-live-do-not-leak",
		"system_prompt",
		"api_key",
		"Authorization",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Errorf("catalog JSON leaks %q: %s", forbidden, encoded)
		}
	}
}

// TestCenterCatalogStillListsUnseededProfile pins the fail-visible mapping: a
// role stays in the catalog when its profile row is missing, with enabled=false
// and no fabricated id.
func TestCenterCatalogStillListsUnseededProfile(t *testing.T) {
	profiles := centerProfiles()
	// Drop every profile except the first: all other roles are unseeded.
	service := newCenterService(&centerFakeProfiles{profiles: profiles[:1]}, &centerFakeReads{}, nil)

	catalog, err := service.Catalog(context.Background())
	if err != nil {
		t.Fatalf("Catalog() error = %v", err)
	}
	if len(catalog.Roles) != 13 {
		t.Fatalf("len(roles) = %d, want 13 even when profiles are missing", len(catalog.Roles))
	}
	view := catalog.Roles[4] // case-design
	if view.Role != "case-design" {
		t.Fatalf("role[4] = %q, want case-design", view.Role)
	}
	if view.ProfileKey == nil || *view.ProfileKey != "case-design" {
		t.Errorf("ProfileKey = %v, want the catalog mapping case-design", derefString(view.ProfileKey))
	}
	if view.ProfileID != nil || view.Enabled {
		t.Errorf("unseeded role = id %v enabled %v, want nil/false", derefInt64(view.ProfileID), view.Enabled)
	}
	if view.AllowedTools == nil {
		t.Error("AllowedTools = nil, want an empty array for JSON stability")
	}
}

// TestCenterCatalogDegradesWhenQualityFails proves a quality outage does not
// take the role catalog down: latest_eval degrades to null while the stats stay
// authoritative.
func TestCenterCatalogDegradesWhenQualityFails(t *testing.T) {
	quality := &centerFakeQuality{err: errors.New("quality db down")}
	service := newCenterService(&centerFakeProfiles{profiles: centerProfiles()}, &centerFakeReads{}, quality)

	catalog, err := service.Catalog(context.Background())
	if err != nil {
		t.Fatalf("Catalog() error = %v, want a degraded success", err)
	}
	if catalog.LatestEval != nil {
		t.Errorf("LatestEval = %+v, want nil when the quality read fails", catalog.LatestEval)
	}
	if len(catalog.Roles) != 13 {
		t.Errorf("len(roles) = %d, want 13", len(catalog.Roles))
	}
}

// TestCenterCatalogPropagatesProfileFailure proves the profile read is
// authoritative: a failure must not be rendered as a catalogue of zeros.
func TestCenterCatalogPropagatesProfileFailure(t *testing.T) {
	service := newCenterService(
		&centerFakeProfiles{err: errors.New("profiles db down")},
		&centerFakeReads{},
		nil,
	)

	if _, err := service.Catalog(context.Background()); err == nil {
		t.Fatal("Catalog() error = nil, want the profile failure")
	}
}

// TestCenterQualityReturnsLatestMetrics pins the optional quality port.
func TestCenterQualityReturnsLatestMetrics(t *testing.T) {
	created := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	latest := &application.CenterEval{
		RunID:      "7",
		Passed:     true,
		Metrics:    map[string]float64{"design_coverage": 0.95},
		Thresholds: map[string]float64{"design_coverage": 0.9},
		CreatedAt:  created,
	}
	service := newCenterService(
		&centerFakeProfiles{profiles: centerProfiles()},
		&centerFakeReads{},
		&centerFakeQuality{latest: latest},
	)

	quality, err := service.Quality(context.Background())
	if err != nil {
		t.Fatalf("Quality() error = %v", err)
	}
	if quality.Latest == nil || quality.Latest.RunID != "7" || !quality.Latest.Passed {
		t.Fatalf("Quality().Latest = %+v, want the wired evaluation", quality.Latest)
	}

	// Without a provider (Task 23 not wired yet) the response is an explicit
	// null, not fabricated metrics.
	empty := newCenterService(&centerFakeProfiles{profiles: centerProfiles()}, &centerFakeReads{}, nil)
	quality, err = empty.Quality(context.Background())
	if err != nil {
		t.Fatalf("Quality() without provider error = %v", err)
	}
	if quality.Latest != nil {
		t.Errorf("Quality().Latest = %+v, want nil", quality.Latest)
	}
}

// TestCenterSessionsPassesFiltersAndLimit pins the run-observation query.
func TestCenterSessionsPassesFiltersAndLimit(t *testing.T) {
	updated := time.Date(2026, 10, 29, 11, 0, 0, 0, time.UTC)
	reads := &centerFakeReads{rows: []domain.CenterSessionRow{{
		ID:             42,
		ProfileKey:     "case-design",
		Title:          "版本 7 设计",
		Status:         "done",
		Running:        false,
		CreatedAt:      updated.Add(-time.Hour),
		UpdatedAt:      updated,
		LastActivityAt: updated,
	}}}
	service := newCenterService(&centerFakeProfiles{profiles: centerProfiles()}, reads, nil)

	page, err := service.Sessions(context.Background(), application.CenterSessionQuery{
		Role:   "case-design",
		Status: "done",
		Limit:  5,
	})
	if err != nil {
		t.Fatalf("Sessions() error = %v", err)
	}
	wantFilter := domain.CenterSessionFilter{RoleKey: "case-design", Status: "done", Limit: 5}
	if reads.lastFilter != wantFilter {
		t.Errorf("repository filter = %+v, want %+v", reads.lastFilter, wantFilter)
	}
	if len(page.Items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(page.Items))
	}
	item := page.Items[0]
	if item.ID != 42 || item.Role != "case-design" || item.Title != "版本 7 设计" ||
		item.Status != "done" || item.Running {
		t.Errorf("item = %+v, want the repository row mapped", item)
	}
	if !item.LastActivityAt.Equal(updated) {
		t.Errorf("LastActivityAt = %v, want %v", item.LastActivityAt, updated)
	}
}

// TestCenterSessionsDefaultsAndCapsLimit pins the page bounds for direct callers.
func TestCenterSessionsDefaultsAndCapsLimit(t *testing.T) {
	reads := &centerFakeReads{}
	service := newCenterService(&centerFakeProfiles{profiles: centerProfiles()}, reads, nil)

	if _, err := service.Sessions(context.Background(), application.CenterSessionQuery{}); err != nil {
		t.Fatalf("Sessions() error = %v", err)
	}
	if want := application.CenterDefaultSessionLimit; reads.lastFilter.Limit != want {
		t.Errorf("default limit = %d, want %d", reads.lastFilter.Limit, want)
	}

	if _, err := service.Sessions(context.Background(), application.CenterSessionQuery{Limit: 500}); err != nil {
		t.Fatalf("Sessions() error = %v", err)
	}
	if want := application.CenterMaxSessionLimit; reads.lastFilter.Limit != want {
		t.Errorf("capped limit = %d, want %d", reads.lastFilter.Limit, want)
	}
}

// TestCenterSessionsProfileLessRoleIsEmpty proves report/knowledge have no
// sessions by construction: the read repository is never asked to fake them.
func TestCenterSessionsProfileLessRoleIsEmpty(t *testing.T) {
	reads := &centerFakeReads{}
	service := newCenterService(&centerFakeProfiles{profiles: centerProfiles()}, reads, nil)

	page, err := service.Sessions(context.Background(), application.CenterSessionQuery{Role: "report"})
	if err != nil {
		t.Fatalf("Sessions(report) error = %v", err)
	}
	if page.Items == nil || len(page.Items) != 0 {
		t.Errorf("items = %#v, want a non-nil empty slice", page.Items)
	}
	if reads.sessionCalls != 0 {
		t.Errorf("repository session calls = %d, want 0 for a profile-less role", reads.sessionCalls)
	}
}

// TestCenterSessionsRejectsUnknownFilters pins the validation sentinels the HTTP
// layer maps to 422.
func TestCenterSessionsRejectsUnknownFilters(t *testing.T) {
	service := newCenterService(&centerFakeProfiles{profiles: centerProfiles()}, &centerFakeReads{}, nil)

	if _, err := service.Sessions(context.Background(), application.CenterSessionQuery{Role: "not-a-role"}); !errors.Is(err, application.ErrUnknownCenterRole) {
		t.Errorf("unknown role error = %v, want ErrUnknownCenterRole", err)
	}
	if _, err := service.Sessions(context.Background(), application.CenterSessionQuery{Status: "exploded"}); !errors.Is(err, application.ErrUnknownCenterStatus) {
		t.Errorf("unknown status error = %v, want ErrUnknownCenterStatus", err)
	}
}

// TestCenterRolesReturnsFreshSlices proves every call returns an independent
// catalog slice (callers may sort/filter it without mutating the next response).
func TestCenterRolesReturnsFreshSlices(t *testing.T) {
	first := application.CenterRoles()
	first[0].Name = "mutated"
	second := application.CenterRoles()
	if second[0].Name != "澄清对话" {
		t.Fatalf("CenterRoles()[0].Name = %q after mutation, want 澄清对话", second[0].Name)
	}
}

// derefString returns *value or "" for a nil pointer.
func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// derefInt64 returns *value or 0 for a nil pointer.
func derefInt64(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
