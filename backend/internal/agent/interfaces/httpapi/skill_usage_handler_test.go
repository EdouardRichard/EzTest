package agentapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	agentapi "github.com/EdouardRichard/EzTest/backend/internal/agent/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// usageSkillStub extends the CRUD stub with the skill_usage read surface.
type usageSkillStub struct {
	*stubSkillService
	usage     []agent.SkillUsage
	usageErr  error
	usageCall int
	lastRunID int64
}

func (s *usageSkillStub) Usage(_ context.Context, runID int64) ([]agent.SkillUsage, error) {
	s.usageCall++
	s.lastRunID = runID
	return s.usage, s.usageErr
}

var _ agentapi.SkillUsageReader = (*usageSkillStub)(nil)

func sampleUsage() []agent.SkillUsage {
	return []agent.SkillUsage{
		{ID: 1, RunID: 9001, Seq: 1, SkillName: "playwright-cli", Found: true,
			CreatedAt: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)},
		{ID: 2, RunID: 9001, Seq: 2, SkillName: "no-such-skill", Found: false,
			CreatedAt: time.Date(2026, 11, 17, 10, 1, 0, 0, time.UTC)},
	}
}

// TestSkillUsageReadDTOShape pins the production read side of the ledger:
// GET /api/skill-usage?run_id= returns the run's rows in seq order with the
// model-independent DTO shape (ids as decimal strings).
func TestSkillUsageReadDTOShape(t *testing.T) {
	stub := &usageSkillStub{stubSkillService: &stubSkillService{}, usage: sampleUsage()}
	r := httptest.NewRequest(http.MethodGet, "/api/skill-usage?run_id=9001", nil)
	r = r.WithContext(platform.WithActor(r.Context(), platform.Actor{ID: 5, Role: "admin"}))
	rec := serveSkill(t, stub, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if stub.usageCall != 1 || stub.lastRunID != 9001 {
		t.Fatalf("service call = %d runID %d", stub.usageCall, stub.lastRunID)
	}
	var envelope struct {
		Data []struct {
			RunID     string `json:"run_id"`
			Seq       int    `json:"seq"`
			SkillName string `json:"skill_name"`
			Found     bool   `json:"found"`
			CreatedAt string `json:"created_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, rec.Body.String())
	}
	if len(envelope.Data) != 2 {
		t.Fatalf("data = %#v", envelope.Data)
	}
	first, second := envelope.Data[0], envelope.Data[1]
	if first.RunID != "9001" || first.Seq != 1 || first.SkillName != "playwright-cli" || !first.Found || first.CreatedAt == "" {
		t.Fatalf("first row = %#v", first)
	}
	if second.Seq != 2 || second.SkillName != "no-such-skill" || second.Found {
		t.Fatalf("second row = %#v", second)
	}
}

func TestSkillUsageReadValidatesRunID(t *testing.T) {
	stub := &usageSkillStub{stubSkillService: &stubSkillService{}}
	for _, query := range []string{"", "?run_id=abc", "?run_id=0", "?run_id=-3"} {
		r := httptest.NewRequest(http.MethodGet, "/api/skill-usage"+query, nil)
		r = r.WithContext(platform.WithActor(r.Context(), platform.Actor{ID: 5, Role: "admin"}))
		rec := serveSkill(t, stub, r)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("query %q: status = %d, want 400", query, rec.Code)
		}
	}
	if stub.usageCall != 0 {
		t.Fatal("invalid run_id reached the service")
	}
}

func TestSkillUsageReadRejectsNonAdmin(t *testing.T) {
	stub := &usageSkillStub{stubSkillService: &stubSkillService{}, usage: sampleUsage()}
	for _, role := range []string{"", "user"} {
		r := httptest.NewRequest(http.MethodGet, "/api/skill-usage?run_id=9001", nil)
		if role != "" {
			r = r.WithContext(platform.WithActor(r.Context(), platform.Actor{ID: 5, Role: role}))
		}
		rec := serveSkill(t, stub, r)
		want := http.StatusUnauthorized
		if role == "user" {
			want = http.StatusForbidden
		}
		if rec.Code != want {
			t.Fatalf("role %q: status = %d, want %d", role, rec.Code, want)
		}
	}
	if stub.usageCall != 0 {
		t.Fatal("rejected request reached the service")
	}
}
