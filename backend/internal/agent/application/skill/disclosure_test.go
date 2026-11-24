package skill_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	skillapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application/skill"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

/* ------------------------- progressive disclosure ------------------------- */

// TestMetadataLayerStripsBody pins the resident layer contract: Metadata
// returns name/description only (enabled skills, sorted), and the rendered
// section never carries a body — a 30k-rune body must not bloat the context.
func TestMetadataLayerStripsBody(t *testing.T) {
	repo := newFakeRepo()
	hugeBody := "BODY-MARKER-7f3a " + strings.Repeat("x", 30000)
	repo.skills["zeta-skill"] = agent.Skill{
		Name: "zeta-skill", Description: "zeta description", Body: hugeBody, Enabled: true,
	}
	repo.skills["alpha-skill"] = agent.Skill{
		Name: "alpha-skill", Description: "alpha description", Body: "alpha body", Enabled: true,
	}
	repo.skills["disabled-skill"] = agent.Skill{
		Name: "disabled-skill", Description: "hidden", Body: "hidden body", Enabled: false,
	}
	svc := skillapp.NewService(repo, nil)

	metas, err := svc.Metadata(context.Background())
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	if len(metas) != 2 || metas[0].Name != "alpha-skill" || metas[1].Name != "zeta-skill" {
		t.Fatalf("metas = %#v, want enabled skills sorted [alpha-skill, zeta-skill]", metas)
	}
	if metas[0].Description != "alpha description" || metas[1].Description != "zeta description" {
		t.Fatalf("descriptions lost: %#v", metas)
	}

	rendered := skillapp.RenderMetadata(metas)
	if !strings.Contains(rendered, "alpha-skill") || !strings.Contains(rendered, "zeta description") {
		t.Fatalf("rendered metadata layer missing name/description:\n%s", rendered)
	}
	if !strings.Contains(rendered, "skill.read") {
		t.Fatalf("rendered layer must point at the on-demand body tool skill.read:\n%s", rendered)
	}
	if strings.Contains(rendered, "BODY-MARKER-7f3a") || strings.Contains(rendered, "alpha body") {
		t.Fatal("skill body leaked into the resident metadata layer")
	}
	if len(rendered) >= len(hugeBody) {
		t.Fatalf("metadata layer bloated: %d bytes vs body %d", len(rendered), len(hugeBody))
	}
}

func TestRenderMetadataEmptyRendersNothing(t *testing.T) {
	if got := skillapp.RenderMetadata(nil); got != "" {
		t.Fatalf("empty metadata rendered %q, want empty section", got)
	}
}

func TestMetadataUnavailableWithoutRepo(t *testing.T) {
	var svc *skillapp.Service
	if _, err := svc.Metadata(context.Background()); err == nil {
		t.Fatal("nil service Metadata succeeded")
	}
}

// TestReadForAgentServesOnlyEnabled pins the on-demand body path: enabled
// packages are served, disabled packages are a miss (the metadata layer
// excludes them, so their bodies must not be readable either), unknown names
// keep the not_found error the tool adapter maps to a miss.
func TestReadForAgentServesOnlyEnabled(t *testing.T) {
	repo := newFakeRepo()
	repo.skills["live-skill"] = agent.Skill{Name: "live-skill", Description: "d", Body: "live body", Enabled: true}
	repo.skills["off-skill"] = agent.Skill{Name: "off-skill", Description: "d", Body: "off body", Enabled: false}
	svc := skillapp.NewService(repo, nil)
	ctx := context.Background()

	skill, found, err := svc.ReadForAgent(ctx, "live-skill")
	if err != nil || !found || skill.Body != "live body" {
		t.Fatalf("enabled read = %#v, %v, %v", skill, found, err)
	}
	skill, found, err = svc.ReadForAgent(ctx, "off-skill")
	if err != nil || found || skill.Body != "" {
		t.Fatalf("disabled read = %#v, %v, %v; want miss without error", skill, found, err)
	}
	if _, _, err := svc.ReadForAgent(ctx, "ghost-skill"); err == nil {
		t.Fatal("unknown name did not surface not_found")
	}
}

/* ----------------------------- skill_usage ledger ------------------------- */

// fakeUsageRepo extends fakeRepo with the append-only usage ledger surface.
type fakeUsageRepo struct {
	*fakeRepo
	usage    []agent.SkillUsage
	usageErr error
	listErr  error
}

func (r *fakeUsageRepo) RecordSkillUsage(_ context.Context, runID int64, name string, found bool) (int, error) {
	if r.usageErr != nil {
		return 0, r.usageErr
	}
	seq := len(r.usage) + 1
	r.usage = append(r.usage, agent.SkillUsage{
		RunID: runID, Seq: seq, SkillName: name, Found: found,
	})
	return seq, nil
}

func (r *fakeUsageRepo) ListSkillUsage(_ context.Context, runID int64) ([]agent.SkillUsage, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	out := make([]agent.SkillUsage, 0)
	for _, row := range r.usage {
		if row.RunID == runID {
			out = append(out, row)
		}
	}
	return out, nil
}

// TestRecordUsageFoundAndMiss pins the fact contract: every skill.read outcome
// (hit and miss) appends one skill_usage row with the run id, name and found
// flag; the seq is assigned per run by the repository.
func TestRecordUsageFoundAndMiss(t *testing.T) {
	repo := &fakeUsageRepo{fakeRepo: newFakeRepo()}
	svc := skillapp.NewService(repo, nil)
	ctx := context.Background()

	if err := svc.RecordUsage(ctx, 42, "playwright-cli", true); err != nil {
		t.Fatalf("RecordUsage found: %v", err)
	}
	if err := svc.RecordUsage(ctx, 42, "no-such-skill", false); err != nil {
		t.Fatalf("RecordUsage miss: %v", err)
	}
	if len(repo.usage) != 2 {
		t.Fatalf("usage rows = %d, want 2", len(repo.usage))
	}
	hit, miss := repo.usage[0], repo.usage[1]
	if hit.RunID != 42 || hit.SkillName != "playwright-cli" || !hit.Found || hit.Seq != 1 {
		t.Fatalf("hit row = %#v", hit)
	}
	if miss.RunID != 42 || miss.SkillName != "no-such-skill" || miss.Found || miss.Seq != 2 {
		t.Fatalf("miss row = %#v", miss)
	}

	rows, err := svc.Usage(ctx, 42)
	if err != nil || len(rows) != 2 {
		t.Fatalf("Usage = %#v, %v", rows, err)
	}
}

func TestRecordUsageValidatesInputs(t *testing.T) {
	repo := &fakeUsageRepo{fakeRepo: newFakeRepo()}
	svc := skillapp.NewService(repo, nil)
	ctx := context.Background()
	if err := svc.RecordUsage(ctx, 0, "playwright-cli", true); err == nil {
		t.Fatal("zero run id accepted")
	}
	if err := svc.RecordUsage(ctx, 42, "Bad Name!", true); err == nil {
		t.Fatal("invalid skill name accepted")
	}
	if len(repo.usage) != 0 {
		t.Fatalf("invalid input wrote rows: %#v", repo.usage)
	}
}

func TestUsageUnavailableWithoutLedgerRepo(t *testing.T) {
	// A Repository without the usage surface fails closed (never half-wired).
	svc := skillapp.NewService(newFakeRepo(), nil)
	ctx := context.Background()
	err := svc.RecordUsage(ctx, 42, "playwright-cli", true)
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "unavailable" {
		t.Fatalf("RecordUsage without ledger repo = %v, want unavailable", err)
	}
	if _, err := svc.Usage(ctx, 42); err == nil {
		t.Fatal("Usage without ledger repo succeeded")
	}
}

func TestUsageRejectsNonPositiveRunID(t *testing.T) {
	repo := &fakeUsageRepo{fakeRepo: newFakeRepo()}
	svc := skillapp.NewService(repo, nil)
	if _, err := svc.Usage(context.Background(), -1); err == nil {
		t.Fatal("negative run id accepted")
	}
}
