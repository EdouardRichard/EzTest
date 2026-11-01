//go:build integration

package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// testRequirementVersionLookup resolves the owning version of a requirement for
// the P4 task ensurer (the agent port has no version).
type testRequirementVersionLookup struct {
	repo interface {
		GetRequirement(ctx context.Context, requirementID int64) (version.Requirement, error)
	}
}

func (l testRequirementVersionLookup) GetRequirementVersionID(ctx context.Context, requirementID int64) (int64, error) {
	requirement, err := l.repo.GetRequirement(ctx, requirementID)
	if err != nil {
		return 0, err
	}
	return requirement.VersionID, nil
}

// createP4Version provisions one SUT/environment/version through the public API
// and returns the string and int64 version IDs.
func (a *testAPI) createP4Version(title string) (string, int64) {
	a.t.Helper()
	sut := a.data(a.do(http.MethodPost, "/api/resources/suts", map[string]any{"name": title + " SUT", "intro": "p4"}, http.StatusCreated))
	environment := a.data(a.do(http.MethodPost, "/api/resources/suts/"+sut["id"].(string)+"/environments",
		map[string]any{"name": "dev", "base_url": "http://localhost:8080", "is_default": true}, http.StatusCreated))
	version := a.data(a.do(http.MethodPost, "/api/versions", map[string]any{
		"title": title,
		"bindings": []any{map[string]any{
			"sut_id": sut["id"], "environment_id": environment["id"],
			"page_ids": []string{}, "api_ids": []string{}, "database_ids": []string{}, "accounts": []any{},
		}},
	}, http.StatusCreated))
	versionID := version["id"].(string)
	parsed, err := strconv.ParseInt(versionID, 10, 64)
	if err != nil {
		a.t.Fatalf("parse version id: %v", err)
	}
	return versionID, parsed
}

// getRaw issues one request and returns the status/headers/body without
// assuming a JSON envelope (report HTML is raw bytes).
func (a *testAPI) getRaw(path string, authenticated bool) (int, http.Header, []byte) {
	a.t.Helper()
	request, err := http.NewRequest(http.MethodGet, a.baseURL+path, nil)
	if err != nil {
		a.t.Fatalf("new request: %v", err)
	}
	if authenticated && a.token != "" {
		request.Header.Set("Authorization", "Bearer "+a.token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		a.t.Fatalf("GET %s: %v", path, err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	return response.StatusCode, response.Header, raw
}

// TestP4RoutesAuthAndEmptyState pins the assembled P4 API: unauthenticated
// requests are 401, empty lists/chains are 200 (not 404) and unknown entities
// are 404.
func TestP4RoutesAuthAndEmptyState(t *testing.T) {
	api := newTestAPI(t)
	api.login("admin", "admin123")
	versionID, _ := api.createP4Version("P4 Routes")

	for _, path := range []string{
		"/api/versions/" + versionID + "/issues",
		"/api/versions/" + versionID + "/reports",
		"/api/versions/" + versionID + "/exploration-chain",
	} {
		if status, _, raw := api.getRaw(path, false); status != http.StatusUnauthorized {
			t.Fatalf("unauthenticated GET %s = %d, want 401: %s", path, status, raw)
		}
	}
	if status, _, raw := api.getRaw("/api/versions/"+versionID+"/reports/999999/revisions/1/html", false); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated report HTML = %d, want 401: %s", status, raw)
	}

	issues := api.do(http.MethodGet, "/api/versions/"+versionID+"/issues", nil, http.StatusOK)
	if len(issues["data"].([]any)) != 0 || issues["has_more"] != false {
		t.Fatalf("empty issues = %#v", issues)
	}
	reports := api.do(http.MethodGet, "/api/versions/"+versionID+"/reports", nil, http.StatusOK)
	if len(reports["data"].([]any)) != 0 {
		t.Fatalf("empty reports = %#v", reports)
	}
	chain := api.data(api.do(http.MethodGet, "/api/versions/"+versionID+"/exploration-chain", nil, http.StatusOK))
	if chain["version_id"] != versionID || len(chain["nodes"].([]any)) != 0 || len(chain["edges"].([]any)) != 0 {
		t.Fatalf("empty chain = %#v", chain)
	}

	api.do(http.MethodGet, "/api/versions/"+versionID+"/issues/999999", nil, http.StatusNotFound)
	api.do(http.MethodGet, "/api/versions/"+versionID+"/reports/999999", nil, http.StatusNotFound)
	api.do(http.MethodGet, "/api/versions/"+versionID+"/reports/999999/revisions/1", nil, http.StatusNotFound)
	api.do(http.MethodGet, "/api/versions/"+versionID+"/reports/999999/revisions/1/html", nil, http.StatusNotFound)

	// Version and requirement reopen are mounted behind auth.
	api.token = ""
	api.do(http.MethodPost, "/api/versions/"+versionID+"/reopen", map[string]any{"reason": "x"}, http.StatusUnauthorized)
	api.login("admin", "admin123")
}

// TestP4ReportPublishAndHTML exercises the real report pipeline end-to-end: the
// adapter reads the execution/design/version application surfaces, the renderer
// writes the deterministic HTML, the content ledger stores it and the HTTP
// endpoint serves the verified bytes.
func TestP4ReportPublishAndHTML(t *testing.T) {
	api := newTestAPI(t)
	api.login("admin", "admin123")
	versionID, versionIDInt := api.createP4Version("P4 Report")
	ctx := context.Background()

	waveID := int64(910001)
	if _, err := api.pool.Exec(ctx, `
		INSERT INTO run_wave (id, version_id, status, trigger, counts, started_at, finished_at)
		VALUES ($1, $2, 'done', 'manual', $3::jsonb, now(), now())`,
		waveID, versionIDInt, `{"total":0}`); err != nil {
		t.Fatalf("seed wave: %v", err)
	}

	report, err := api.reportSvc.EnsureWaveReport(ctx, versionIDInt, waveID)
	if err != nil {
		t.Fatalf("EnsureWaveReport: %v", err)
	}
	detail, err := api.reportSvc.Get(ctx, versionIDInt, report.ID)
	if err != nil {
		t.Fatalf("Get report: %v", err)
	}
	if len(detail.Revisions) != 1 {
		t.Fatalf("revisions = %+v", detail.Revisions)
	}
	revision := detail.Revisions[0].Revision
	published, err := api.reportSvc.Publish(ctx, report.ID, revision, "P4 集成报告叙述")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if published.Status != "published" || published.HTMLRef == nil {
		t.Fatalf("published revision = %+v", published)
	}

	list := api.listData(api.do(http.MethodGet, "/api/versions/"+versionID+"/reports", nil, http.StatusOK))
	if len(list) != 1 || list[0].(map[string]any)["id"] != strconv.FormatInt(report.ID, 10) {
		t.Fatalf("reports list = %#v", list)
	}
	reportPath := "/api/versions/" + versionID + "/reports/" + strconv.FormatInt(report.ID, 10)
	detailBody := api.data(api.do(http.MethodGet, reportPath, nil, http.StatusOK))
	if detailBody["status"] != "published" || len(detailBody["revisions"].([]any)) != 1 {
		t.Fatalf("report detail = %#v", detailBody)
	}
	api.do(http.MethodGet, reportPath+"/revisions/1", nil, http.StatusOK)

	status, headers, html := api.getRaw(reportPath+"/revisions/1/html", true)
	if status != http.StatusOK {
		t.Fatalf("report HTML status = %d: %s", status, html)
	}
	if contentType := headers.Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
		t.Fatalf("content type = %q", contentType)
	}
	if headers.Get("ETag") == "" || headers.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("report headers = %+v", headers)
	}
	if !strings.Contains(string(html), "P4 集成报告叙述") || !strings.Contains(string(html), `data-narrative="candidate"`) {
		t.Fatalf("report HTML does not contain the narrative: %s", html)
	}

	// Regenerate allocates the next revision; the old HTML stays readable.
	regenerated := api.do(http.MethodPost, reportPath+"/regenerate", map[string]any{"reason": "input changed"}, http.StatusAccepted)
	data := regenerated["data"].(map[string]any)
	if data["revision"] != float64(2) {
		t.Fatalf("regenerate = %#v", regenerated)
	}
	if status, _, _ := api.getRaw(reportPath+"/revisions/1/html", true); status != http.StatusOK {
		t.Fatalf("old revision HTML status = %d, want 200", status)
	}
}

// TestP4VersionAndRequirementReopen exercises the P4 reopen services through the
// real HTTP surface: completed version -> running, closed requirement -> draft
// with the closure reopen count, and the 409/404 matrix.
func TestP4VersionAndRequirementReopen(t *testing.T) {
	api := newTestAPI(t)
	api.login("admin", "admin123")
	versionID, versionIDInt := api.createP4Version("P4 Reopen")
	ctx := context.Background()

	// Requirement created through the P1/P4 API, then closed through SQL (the
	// closure predicate needs execution/design data that this test does not
	// seed; the reopen path is what is under test here).
	requirementBody := api.do(http.MethodPost, "/api/versions/"+versionID+"/requirements", map[string]any{
		"title": "Reopenable requirement", "description": "d", "acceptance_criteria": "a", "priority": "P1",
		"constraints": json.RawMessage(`{}`),
	}, http.StatusCreated)
	requirement := api.data(requirementBody)
	requirementIDString := requirement["id"].(string)
	requirementID, _ := strconv.ParseInt(requirementIDString, 10, 64)
	requirementVersionID, _ := strconv.ParseInt(requirement["current_version_id"].(string), 10, 64)
	if _, err := api.pool.Exec(ctx, `UPDATE requirement SET status = 'closed', closed_at = now() WHERE id = $1`, requirementID); err != nil {
		t.Fatalf("close requirement: %v", err)
	}
	if _, err := api.pool.Exec(ctx, `
		INSERT INTO requirement_closure (id, requirement_id, requirement_version_id, close_round, check_result, evidence_refs, closed_at)
		VALUES (980001, $1, $2, 1, '{}'::jsonb, '[]'::jsonb, now())`, requirementID, requirementVersionID); err != nil {
		t.Fatalf("seed closure: %v", err)
	}

	// A completed version reopens to running and clears the close projection.
	if _, err := api.pool.Exec(ctx, `UPDATE version SET status = 'completed', closed_at = now(), close_reason = 'shipped' WHERE id = $1`, versionIDInt); err != nil {
		t.Fatalf("complete version: %v", err)
	}
	reopened := api.data(api.do(http.MethodPost, "/api/versions/"+versionID+"/reopen",
		map[string]any{"reason": "new scope"}, http.StatusOK))
	if reopened["status"] != "running" || reopened["closed_at"] != nil || reopened["close_reason"] != nil {
		t.Fatalf("reopened version = %#v", reopened)
	}
	// Reopening a running version is a state conflict.
	api.do(http.MethodPost, "/api/versions/"+versionID+"/reopen", map[string]any{"reason": "again"}, http.StatusConflict)
	api.do(http.MethodPost, "/api/versions/999999999/reopen", map[string]any{"reason": "missing"}, http.StatusNotFound)

	// Requirement reopen: closed -> draft with the closure reopen count.
	requirementReopened := api.data(api.do(http.MethodPost,
		"/api/versions/"+versionID+"/requirements/"+requirementIDString+"/reopen",
		map[string]any{"reason": "more acceptance criteria"}, http.StatusOK))
	if requirementReopened["status"] != "draft" {
		t.Fatalf("reopened requirement = %#v", requirementReopened)
	}
	closure := requirementReopened["closure"].(map[string]any)
	if closure["close_round"] != float64(1) || closure["reopen_count"] != float64(1) {
		t.Fatalf("reopened closure = %#v", closure)
	}
	// A second reopen is a state conflict: the requirement is draft again.
	api.do(http.MethodPost, "/api/versions/"+versionID+"/requirements/"+requirementIDString+"/reopen",
		map[string]any{"reason": "again"}, http.StatusConflict)
	api.do(http.MethodPost, "/api/versions/"+versionID+"/requirements/999999999/reopen",
		map[string]any{"reason": "missing"}, http.StatusNotFound)
}
