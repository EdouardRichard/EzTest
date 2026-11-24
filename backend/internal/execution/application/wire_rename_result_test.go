package executionapp_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// TestWireRenameToolResultKeysCarrySutID pins surface ③ (the tool RESULT json
// keys) on the execution side: account.credentials, database.schema_read and
// evidence.read results carry sut_id (the value), never the stale version_sut_id
// key. A result key that drifts from the argument schema is the B "错而绿"
// channel — the model reads a field name it cannot correlate with its arguments.
func TestWireRenameToolResultKeysCarrySutID(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)

	creds, err := fixture.runner.AccountCredentials(context.Background(), executionScope(), tools.AccountCredentialsInput{
		VersionSUTID: int64ptr(7), Role: "admin",
	})
	if err != nil {
		t.Fatalf("AccountCredentials: %v", err)
	}
	credsPayload := creds.(map[string]any)
	if got, _ := credsPayload["sut_id"].(string); got != "7" {
		t.Fatalf("account.credentials result sut_id = %#v, want \"7\"", credsPayload["sut_id"])
	}
	if _, stale := credsPayload["version_sut_id"]; stale {
		t.Fatalf("account.credentials result still carries version_sut_id: %#v", credsPayload)
	}

	schema, err := fixture.runner.SchemaRead(context.Background(), executionScope(), json.RawMessage(`{"sut_id":"7"}`))
	if err != nil {
		t.Fatalf("SchemaRead: %v", err)
	}
	schemaPayload := schema.(map[string]any)
	if got, _ := schemaPayload["sut_id"].(string); got != "7" {
		t.Fatalf("database.schema_read result sut_id = %#v, want \"7\"", schemaPayload["sut_id"])
	}
	if _, stale := schemaPayload["version_sut_id"]; stale {
		t.Fatalf("database.schema_read result still carries version_sut_id: %#v", schemaPayload)
	}

	fixture.content.open = []byte("evidence body")
	stored, err := fixture.runner.AddEvidence(context.Background(), executionScope(), executionapp.EvidenceInput{
		Kind: execution.EvidenceText, Data: []byte("evidence body"), SUTID: int64ptr(7),
	})
	if err != nil {
		t.Fatalf("AddEvidence: %v", err)
	}
	read, err := fixture.runner.EvidenceRead(context.Background(), executionScope(), tools.EvidenceReadInput{
		EvidenceID: stored.ID,
	})
	if err != nil {
		t.Fatalf("EvidenceRead: %v", err)
	}
	readPayload := read.(map[string]any)
	if got, _ := readPayload["sut_id"].(string); got != "7" {
		t.Fatalf("evidence.read result sut_id = %#v, want \"7\"", readPayload["sut_id"])
	}
	if _, stale := readPayload["version_sut_id"]; stale {
		t.Fatalf("evidence.read result still carries version_sut_id: %#v", readPayload)
	}
}
