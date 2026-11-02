package agentapp_test

import (
	"encoding/json"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
)

func TestP5CodeintelWorkTypes(t *testing.T) {
	if agentapp.WorkTypeIndexRepo != "index.repo" {
		t.Fatalf("WorkTypeIndexRepo = %q", agentapp.WorkTypeIndexRepo)
	}
	if agentapp.WorkTypeCodeAnalyze != "code.analyze" {
		t.Fatalf("WorkTypeCodeAnalyze = %q", agentapp.WorkTypeCodeAnalyze)
	}
}

func TestParseWorkItemPayloadP5CodeintelFields(t *testing.T) {
	raw := json.RawMessage(`{
		"repo_id": "7001",
		"index_run_id": 7002,
		"analysis_id": "7003",
		"base_commit_sha": "1111111111111111111111111111111111111111",
		"head_commit_sha": "2222222222222222222222222222222222222222"
	}`)
	payload, err := agentapp.ParseWorkItemPayload(raw)
	if err != nil {
		t.Fatalf("ParseWorkItemPayload: %v", err)
	}
	if payload.RepoID != 7001 || payload.IndexRunID != 7002 || payload.AnalysisID != 7003 {
		t.Fatalf("codeintel IDs = %d/%d/%d", payload.RepoID, payload.IndexRunID, payload.AnalysisID)
	}
	if payload.BaseCommitSHA != "1111111111111111111111111111111111111111" ||
		payload.HeadCommitSHA != "2222222222222222222222222222222222222222" {
		t.Fatalf("commit SHAs = %q/%q", payload.BaseCommitSHA, payload.HeadCommitSHA)
	}
}

func TestParseWorkItemPayloadRejectsNonStringCommitSHA(t *testing.T) {
	if _, err := agentapp.ParseWorkItemPayload(json.RawMessage(`{"base_commit_sha": 12}`)); err == nil {
		t.Fatal("non-string base_commit_sha accepted")
	}
	if _, err := agentapp.ParseWorkItemPayload(json.RawMessage(`{"head_commit_sha": true}`)); err == nil {
		t.Fatal("non-string head_commit_sha accepted")
	}
}
