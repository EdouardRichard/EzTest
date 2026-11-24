package tools

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// fakeFixtureLibrary is the FixtureLibrary stub: it records the parsed inputs
// and the injected scope so a test can pin the tool->service contract surface.
type fakeFixtureLibrary struct {
	uploads     []FixtureUploadInput
	downloads   []FixtureDownloadInput
	scopes      []Scope
	uploadRes   any
	downloadRes any
	uploadErr   error
	downloadErr error
}

func (f *fakeFixtureLibrary) UploadFixture(_ context.Context, scope Scope, in FixtureUploadInput) (any, error) {
	f.uploads = append(f.uploads, in)
	f.scopes = append(f.scopes, scope)
	if f.uploadErr != nil {
		return nil, f.uploadErr
	}
	return f.uploadRes, nil
}

func (f *fakeFixtureLibrary) DownloadFixture(_ context.Context, scope Scope, in FixtureDownloadInput) (any, error) {
	f.downloads = append(f.downloads, in)
	f.scopes = append(f.scopes, scope)
	if f.downloadErr != nil {
		return nil, f.downloadErr
	}
	return f.downloadRes, nil
}

// TestFixtureToolsRegistered pins the additive registration contract: the
// fixture upload/download tools register through the P4 tool-group mechanism
// only when the fixture library port is wired, and the admin catalog always
// lists them (capability-list completeness gate, TestCapabilityListContract).
func TestFixtureToolsRegistered(t *testing.T) {
	library := &fakeFixtureLibrary{}
	registry := NewKnowledgeRegistry(Deps{SessionFixtures: SessionFixtureDeps{Library: library}})
	for _, name := range []string{ToolFixtureUpload, ToolFixtureDownload} {
		if _, ok := registry.Get(name); !ok {
			t.Fatalf("NewKnowledgeRegistry is missing %q when the fixture library is wired", name)
		}
	}
	if _, ok := NewKnowledgeRegistry(Deps{}).Get(ToolFixtureUpload); ok {
		t.Fatal("fixture.upload must stay unregistered without the fixture library port")
	}
	if _, ok := NewKnowledgeRegistry(Deps{}).Get(ToolFixtureDownload); ok {
		t.Fatal("fixture.download must stay unregistered without the fixture library port")
	}
	catalog := AdminCatalogRegistry()
	for _, name := range []string{ToolFixtureUpload, ToolFixtureDownload} {
		if _, ok := catalog.Get(name); !ok {
			t.Fatalf("AdminCatalogRegistry is missing %q", name)
		}
	}
}

// TestFixtureToolDescriptionsRuntimeClean: the model-facing descriptions teach
// the CLI state-save/load chain and carry no development-stage codenames
// (runtime cleanliness, AGENTS §2.2).
func TestFixtureToolDescriptionsRuntimeClean(t *testing.T) {
	catalog := AdminCatalogRegistry()
	upload, _ := catalog.Get(ToolFixtureUpload)
	download, _ := catalog.Get(ToolFixtureDownload)
	if !strings.Contains(upload.Description(), "state-save") {
		t.Fatalf("fixture.upload description must name the CLI state-save chain: %s", upload.Description())
	}
	if !strings.Contains(download.Description(), "state-load") {
		t.Fatalf("fixture.download description must name the CLI state-load injection: %s", download.Description())
	}
	stageCodename := regexp.MustCompile(`\bP[0-9]+\b|\bT1[0-9]\b|SC-F[0-9]`)
	for _, tool := range []Tool{upload, download} {
		if stageCodename.MatchString(tool.Description()) {
			t.Fatalf("%s description leaks a stage codename: %s", tool.Name(), tool.Description())
		}
	}
}

// TestFixtureUploadToolContract pins the six model-facing surfaces of
// fixture.upload: schema required fields, argument decode, scope passthrough,
// result keys, and the retryable field error for a malformed sut_id.
func TestFixtureUploadToolContract(t *testing.T) {
	library := &fakeFixtureLibrary{uploadRes: map[string]any{
		"fixture_id": "1", "storage_state_ref": strings.Repeat("a", 64), "role": "admin",
	}}
	registry := NewKnowledgeRegistry(Deps{SessionFixtures: SessionFixtureDeps{Library: library}})

	tool, _ := registry.Get(ToolFixtureUpload)
	var schema struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(tool.Parameters(), &schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if len(schema.Required) != 1 || schema.Required[0] != "sut_id" {
		t.Fatalf("required = %v, want [sut_id]", schema.Required)
	}
	for _, field := range []string{"sut_id", "account_role", "path"} {
		if _, ok := schema.Properties[field]; !ok {
			t.Fatalf("schema missing property %q", field)
		}
	}

	scope := Scope{VersionID: 100, CaseRunID: 500, AgentRunID: 42}
	result, err := registry.Execute(context.Background(), scope, ToolFixtureUpload,
		json.RawMessage(`{"sut_id":"9001","account_role":"admin","path":"state.json"}`))
	if err != nil {
		t.Fatalf("fixture.upload: %v", err)
	}
	if len(library.uploads) != 1 {
		t.Fatalf("uploads = %d, want 1", len(library.uploads))
	}
	in := library.uploads[0]
	if in.SUTID != 9001 || in.AccountRole != "admin" || in.Path != "state.json" {
		t.Fatalf("parsed input = %+v", in)
	}
	if library.scopes[0].VersionID != 100 || library.scopes[0].CaseRunID != 500 {
		t.Fatalf("scope passthrough = %+v", library.scopes[0])
	}
	body := resultMap(t, result)
	if body["fixture_id"] != "1" {
		t.Fatalf("result fixture_id = %#v", body["fixture_id"])
	}

	for _, payload := range []string{`{"sut_id":"abc"}`, `{"sut_id":""}`, `{}`} {
		t.Run("malformed "+payload, func(t *testing.T) {
			fresh := &fakeFixtureLibrary{}
			reg := NewKnowledgeRegistry(Deps{SessionFixtures: SessionFixtureDeps{Library: fresh}})
			_, err := reg.Execute(context.Background(), scope, ToolFixtureUpload, json.RawMessage(payload))
			toolErr := asToolError(t, err)
			if toolErr.Code != CodeInvalidArgument || !toolErr.Retryable || toolErr.Field != "sut_id" {
				t.Fatalf("error = %+v, want retryable invalid_argument on sut_id", toolErr)
			}
			if len(fresh.uploads) != 0 {
				t.Fatal("malformed argument reached the fixture library")
			}
		})
	}

	// Unknown fields are rejected (the model cannot inject scope ids).
	if _, err := registry.Execute(context.Background(), scope, ToolFixtureUpload,
		json.RawMessage(`{"sut_id":"9001","version_id":"999"}`)); err == nil {
		t.Fatal("unknown field version_id must be rejected")
	}
}

// TestFixtureDownloadToolContract pins fixture.download's argument decode,
// result keys and the typed failure mapping (a failed state-load surfaces as
// state_conflict, never a silent success).
func TestFixtureDownloadToolContract(t *testing.T) {
	library := &fakeFixtureLibrary{downloadRes: map[string]any{
		"fixture_id": "2", "loaded": true, "storage_state_ref": strings.Repeat("b", 64),
	}}
	registry := NewKnowledgeRegistry(Deps{SessionFixtures: SessionFixtureDeps{Library: library}})
	scope := Scope{VersionID: 100, CaseRunID: 501, AgentRunID: 42}

	result, err := registry.Execute(context.Background(), scope, ToolFixtureDownload,
		json.RawMessage(`{"sut_id":"9001","account_role":"admin"}`))
	if err != nil {
		t.Fatalf("fixture.download: %v", err)
	}
	body := resultMap(t, result)
	if body["loaded"] != true || body["fixture_id"] != "2" {
		t.Fatalf("result = %+v", body)
	}
	if len(library.downloads) != 1 || library.downloads[0].SUTID != 9001 || library.downloads[0].AccountRole != "admin" {
		t.Fatalf("parsed input = %+v", library.downloads)
	}

	t.Run("load failure maps to state_conflict", func(t *testing.T) {
		failing := &fakeFixtureLibrary{downloadErr: platform.NewError(409, "fixture_load_failed",
			"the saved login state could not be loaded")}
		reg := NewKnowledgeRegistry(Deps{SessionFixtures: SessionFixtureDeps{Library: failing}})
		_, err := reg.Execute(context.Background(), scope, ToolFixtureDownload, json.RawMessage(`{"sut_id":"9001"}`))
		toolErr := asToolError(t, err)
		if toolErr.Code != CodeStateConflict {
			t.Fatalf("error = %+v, want state_conflict (observable, not a silent stale success)", toolErr)
		}
	})

	t.Run("no fixture maps to not_found", func(t *testing.T) {
		empty := &fakeFixtureLibrary{downloadErr: platform.NotFound("session fixture")}
		reg := NewKnowledgeRegistry(Deps{SessionFixtures: SessionFixtureDeps{Library: empty}})
		_, err := reg.Execute(context.Background(), scope, ToolFixtureDownload, json.RawMessage(`{"sut_id":"9001"}`))
		toolErr := asToolError(t, err)
		if toolErr.Code != CodeNotFound {
			t.Fatalf("error = %+v, want not_found", toolErr)
		}
	})
}
