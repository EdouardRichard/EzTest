package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// fakeVersionPort records the 11 P3 tool primitive calls and returns canned
// outcomes.
type fakeVersionPort struct {
	calls    []string
	content  string
	business bool
	err      error

	contextVersionID int64

	updateVersionID     int64
	updateRequirementID int64
	updateRevision      int
	updatePatch         string
	updateActor         int64

	questionVersionID int64
	questionID        *int64
	requirementID     *int64
	questionText      string
	questionActor     int64

	answerVersionID  int64
	answerQuestionID int64
	answerText       string
	answerActor      int64

	decisionVersionID int64
	decisionReqID     *int64
	decisionQuestID   *int64
	decisionTopic     string
	decisionText      string
	decisionRationale string
	decisionActor     int64

	relationVersionID int64
	relationFrom      int64
	relationTo        int64
	relationType      string
	relationDesc      string
	relationActor     int64

	listVersionID     int64
	listRequirementID *int64

	readVersionID    int64
	readAttachmentID int64
	readMaxBytes     int

	summaryVersionID int64
	summaryText      string
	summaryActor     int64

	treeVersionID int64
	treePath      string
	treeMax       int

	codeReadVersionID int64
	codeReadPath      string
	codeReadOffset    int
	codeReadMax       int
}

// respond records one call and returns the configured outcome.
func (f *fakeVersionPort) respond(call string) (string, bool, error) {
	f.calls = append(f.calls, call)
	if f.err != nil {
		return "", false, f.err
	}
	if f.business {
		return "business failure", true, nil
	}
	return f.content, false, nil
}

// ContextGet records the version id.
func (f *fakeVersionPort) ContextGet(_ context.Context, versionID int64) (string, bool, error) {
	f.contextVersionID = versionID
	return f.respond(fmt.Sprintf("contextGet:%d", versionID))
}

// UpdateDraft records the AI draft edit.
func (f *fakeVersionPort) UpdateDraft(_ context.Context, versionID, requirementID int64, revision int, patch json.RawMessage, actor int64) (string, bool, error) {
	f.updateVersionID, f.updateRequirementID, f.updateRevision = versionID, requirementID, revision
	f.updatePatch = string(patch)
	f.updateActor = actor
	return f.respond(fmt.Sprintf("updateDraft:%d:%d:%d", versionID, requirementID, revision))
}

// QuestionUpsert records the question write.
func (f *fakeVersionPort) QuestionUpsert(_ context.Context, versionID int64, questionID, requirementID *int64, question string, actor int64) (string, bool, error) {
	f.questionVersionID, f.questionID, f.requirementID = versionID, questionID, requirementID
	f.questionText, f.questionActor = question, actor
	return f.respond(fmt.Sprintf("questionUpsert:%d", versionID))
}

// QuestionAnswer records the question answer.
func (f *fakeVersionPort) QuestionAnswer(_ context.Context, versionID, questionID int64, answer string, actor int64) (string, bool, error) {
	f.answerVersionID, f.answerQuestionID = versionID, questionID
	f.answerText, f.answerActor = answer, actor
	return f.respond(fmt.Sprintf("questionAnswer:%d:%d", versionID, questionID))
}

// DecisionPropose records the decision proposal.
func (f *fakeVersionPort) DecisionPropose(_ context.Context, versionID int64, requirementID, questionID *int64, topic, decision, rationale string, actor int64) (string, bool, error) {
	f.decisionVersionID = versionID
	f.decisionReqID, f.decisionQuestID = requirementID, questionID
	f.decisionTopic, f.decisionText, f.decisionRationale = topic, decision, rationale
	f.decisionActor = actor
	return f.respond(fmt.Sprintf("decisionPropose:%d", versionID))
}

// RelationPropose records the relation suggestion.
func (f *fakeVersionPort) RelationPropose(_ context.Context, versionID, fromRequirementID, toRequirementID int64, relationType, description string, actor int64) (string, bool, error) {
	f.relationVersionID, f.relationFrom, f.relationTo = versionID, fromRequirementID, toRequirementID
	f.relationType, f.relationDesc, f.relationActor = relationType, description, actor
	return f.respond(fmt.Sprintf("relationPropose:%d", versionID))
}

// AttachmentList records the attachment listing request.
func (f *fakeVersionPort) AttachmentList(_ context.Context, versionID int64, requirementID *int64) (string, bool, error) {
	f.listVersionID, f.listRequirementID = versionID, requirementID
	return f.respond(fmt.Sprintf("attachmentList:%d", versionID))
}

// AttachmentReadText records the attachment text read.
func (f *fakeVersionPort) AttachmentReadText(_ context.Context, versionID, attachmentID int64, maxBytes int) (string, bool, error) {
	f.readVersionID, f.readAttachmentID, f.readMaxBytes = versionID, attachmentID, maxBytes
	return f.respond(fmt.Sprintf("attachmentReadText:%d:%d", versionID, attachmentID))
}

// ContextSummaryUpdate records the summary write.
func (f *fakeVersionPort) ContextSummaryUpdate(_ context.Context, versionID int64, summary string, actor int64) (string, bool, error) {
	f.summaryVersionID, f.summaryText, f.summaryActor = versionID, summary, actor
	return f.respond(fmt.Sprintf("contextSummaryUpdate:%d", versionID))
}

// CodeTree records the code tree request.
func (f *fakeVersionPort) CodeTree(_ context.Context, versionID int64, path string, maxEntries int) (string, bool, error) {
	f.treeVersionID, f.treePath, f.treeMax = versionID, path, maxEntries
	return f.respond(fmt.Sprintf("codeTree:%d", versionID))
}

// CodeRead records the code read request.
func (f *fakeVersionPort) CodeRead(_ context.Context, versionID int64, path string, offset, maxBytes int) (string, bool, error) {
	f.codeReadVersionID, f.codeReadPath = versionID, path
	f.codeReadOffset, f.codeReadMax = offset, maxBytes
	return f.respond(fmt.Sprintf("codeRead:%d:%s", versionID, path))
}

// expectedToolNames is the exact spec §8.3 tool set, in registration order.
var expectedToolNames = []string{
	"version.context_get",
	"requirement.update_draft",
	"clarification.question_upsert",
	"clarification.question_answer",
	"decision.propose",
	"relation.propose",
	"attachment.list",
	"attachment.read_text",
	"context.summary_update",
	"code.tree",
	"code.read",
}

// invocationContext returns a context carrying the run-scoped actor.
func invocationContext(actor int64) context.Context {
	return agentapp.WithToolInvocation(context.Background(), agentapp.Invocation{
		SessionID: 11, RunID: 22, ActorUserID: &actor, ProfileKey: "clarification-dialog",
	})
}

// schemaObject decodes one tool schema as a generic JSON object.
func schemaObject(t *testing.T, tool domain.Tool) map[string]any {
	t.Helper()
	var schema map[string]any
	if err := json.Unmarshal(tool.InputSchema(), &schema); err != nil {
		t.Fatalf("%s schema is not valid JSON: %v", tool.Name(), err)
	}
	if schema["type"] != "object" {
		t.Fatalf("%s schema type=%v want object", tool.Name(), schema["type"])
	}
	return schema
}

// requiredSet returns the schema's required field set.
func requiredSet(t *testing.T, schema map[string]any) map[string]bool {
	t.Helper()
	raw, ok := schema["required"].([]any)
	if !ok {
		t.Fatalf("schema required=%v want an array", schema["required"])
	}
	required := make(map[string]bool, len(raw))
	for _, field := range raw {
		name, ok := field.(string)
		if !ok {
			t.Fatalf("schema required contains a non-string: %v", field)
		}
		required[name] = true
	}
	return required
}

// propertyMap returns the schema's properties object.
func propertyMap(t *testing.T, schema map[string]any) map[string]any {
	t.Helper()
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties=%v want an object", schema["properties"])
	}
	return properties
}

// TestClarificationToolsSpecs pins the 11 tool names, their JSON schemas and
// the requirement that no schema ever exposes an actor parameter.
func TestClarificationToolsSpecs(t *testing.T) {
	port := &fakeVersionPort{content: "{}"}
	tools := NewClarificationTools(port)
	if len(tools) != len(expectedToolNames) {
		t.Fatalf("NewClarificationTools returned %d tools, want %d", len(tools), len(expectedToolNames))
	}
	seen := map[string]bool{}
	for i, tool := range tools {
		if tool.Name() != expectedToolNames[i] {
			t.Fatalf("tool[%d]=%q want %q", i, tool.Name(), expectedToolNames[i])
		}
		if seen[tool.Name()] {
			t.Fatalf("duplicate tool name %q", tool.Name())
		}
		seen[tool.Name()] = true
		if strings.TrimSpace(tool.Description()) == "" {
			t.Fatalf("%s has an empty description", tool.Name())
		}
		schema := schemaObject(t, tool)
		properties := propertyMap(t, schema)
		if _, hasActor := properties["actor"]; hasActor {
			t.Fatalf("%s schema exposes an actor parameter", tool.Name())
		}
		if _, hasActorID := properties["actorId"]; hasActorID {
			t.Fatalf("%s schema exposes an actorId parameter", tool.Name())
		}
		required := requiredSet(t, schema)
		for _, idField := range []string{"versionId", "requirementId", "fromRequirementId", "toRequirementId", "questionId", "attachmentId"} {
			if _, present := properties[idField]; present {
				property, ok := properties[idField].(map[string]any)
				if !ok || property["type"] != "string" {
					t.Fatalf("%s.%s must be a string-typed property", tool.Name(), idField)
				}
				if needed := required[idField]; needed || idField == "versionId" {
					if !needed {
						t.Fatalf("%s must require %s", tool.Name(), idField)
					}
				}
			}
		}
		if !required["versionId"] {
			t.Fatalf("%s does not require versionId", tool.Name())
		}
	}

	t.Run("required fields follow the tool contract", func(t *testing.T) {
		want := map[string][]string{
			"version.context_get":           {"versionId"},
			"requirement.update_draft":      {"versionId", "requirementId", "revision", "patch"},
			"clarification.question_upsert": {"versionId", "question"},
			"clarification.question_answer": {"versionId", "questionId", "answer"},
			"decision.propose":              {"versionId", "topic", "decision"},
			"relation.propose":              {"versionId", "fromRequirementId", "toRequirementId", "relationType"},
			"attachment.list":               {"versionId"},
			"attachment.read_text":          {"versionId", "attachmentId"},
			"context.summary_update":        {"versionId", "summary"},
			"code.tree":                     {"versionId"},
			"code.read":                     {"versionId", "path"},
		}
		for _, tool := range tools {
			schema := schemaObject(t, tool)
			required := requiredSet(t, schema)
			expected := want[tool.Name()]
			if len(required) != len(expected) {
				t.Fatalf("%s required=%v want %v", tool.Name(), required, expected)
			}
			for _, field := range expected {
				if !required[field] {
					t.Fatalf("%s required=%v missing %q", tool.Name(), required, field)
				}
			}
		}
	})

	t.Run("update_draft patch only allows the whitelisted fields", func(t *testing.T) {
		schema := schemaObject(t, tools[1])
		patch, ok := propertyMap(t, schema)["patch"].(map[string]any)
		if !ok {
			t.Fatalf("update_draft patch schema missing")
		}
		properties := propertyMap(t, patch)
		for _, field := range []string{"title", "description", "acceptanceCriteria", "priority"} {
			if _, ok := properties[field]; !ok {
				t.Fatalf("update_draft patch is missing %q", field)
			}
		}
		if len(properties) != 4 {
			t.Fatalf("update_draft patch allows extra fields: %v", properties)
		}
		if _, hasStatus := properties["status"]; hasStatus {
			t.Fatal("update_draft patch allows status")
		}
	})

	t.Run("relation.propose enumerates the four relation types", func(t *testing.T) {
		schema := schemaObject(t, tools[5])
		relationType, ok := propertyMap(t, schema)["relationType"].(map[string]any)
		if !ok {
			t.Fatal("relation.propose relationType schema missing")
		}
		values, ok := relationType["enum"].([]any)
		if !ok || len(values) != 4 {
			t.Fatalf("relationType enum=%v want 4 values", relationType["enum"])
		}
	})

	if NewClarificationTools(nil) != nil {
		t.Fatal("NewClarificationTools(nil) must return nil")
	}
}

// TestClarificationToolsExecution pins the argument mapping of every tool.
func TestClarificationToolsExecution(t *testing.T) {
	port := &fakeVersionPort{content: "{}"}
	tools := NewClarificationTools(port)
	byName := map[string]domain.Tool{}
	for _, tool := range tools {
		byName[tool.Name()] = tool
	}
	ctx := invocationContext(42)

	cases := []struct {
		name  string
		args  string
		check func(t *testing.T)
	}{
		{
			name: "version.context_get",
			args: `{"versionId":"77"}`,
			check: func(t *testing.T) {
				if port.contextVersionID != 77 {
					t.Fatalf("context version=%d want 77", port.contextVersionID)
				}
			},
		},
		{
			name: "requirement.update_draft",
			args: `{"versionId":"77","requirementId":"88","revision":3,"patch":{"title":"new title"}}`,
			check: func(t *testing.T) {
				if port.updateVersionID != 77 || port.updateRequirementID != 88 ||
					port.updateRevision != 3 || port.updateActor != 42 {
					t.Fatalf("update captured=%d/%d/%d/%d", port.updateVersionID, port.updateRequirementID, port.updateRevision, port.updateActor)
				}
				if !strings.Contains(port.updatePatch, `"title":"new title"`) {
					t.Fatalf("update patch=%s", port.updatePatch)
				}
			},
		},
		{
			name: "clarification.question_upsert",
			args: `{"versionId":"77","requirementId":"88","question":"why?"}`,
			check: func(t *testing.T) {
				if port.questionVersionID != 77 || port.questionText != "why?" || port.questionActor != 42 {
					t.Fatalf("question captured=%d/%q/%d", port.questionVersionID, port.questionText, port.questionActor)
				}
				if port.questionID != nil || port.requirementID == nil || *port.requirementID != 88 {
					t.Fatalf("question ids=%v/%v", port.questionID, port.requirementID)
				}
			},
		},
		{
			name: "clarification.question_answer",
			args: `{"versionId":"77","questionId":"99","answer":"because"}`,
			check: func(t *testing.T) {
				if port.answerVersionID != 77 || port.answerQuestionID != 99 ||
					port.answerText != "because" || port.answerActor != 42 {
					t.Fatalf("answer captured=%d/%d/%q/%d", port.answerVersionID, port.answerQuestionID, port.answerText, port.answerActor)
				}
			},
		},
		{
			name: "decision.propose",
			args: `{"versionId":"77","topic":"topic","decision":"decision"}`,
			check: func(t *testing.T) {
				if port.decisionVersionID != 77 || port.decisionTopic != "topic" ||
					port.decisionText != "decision" || port.decisionActor != 42 {
					t.Fatalf("decision captured=%+v", port)
				}
				if port.decisionReqID != nil || port.decisionQuestID != nil {
					t.Fatalf("decision optional ids=%v/%v want nil", port.decisionReqID, port.decisionQuestID)
				}
			},
		},
		{
			name: "relation.propose",
			args: `{"versionId":"77","fromRequirementId":"1","toRequirementId":"2","relationType":"depends_on","description":"needs"}`,
			check: func(t *testing.T) {
				if port.relationVersionID != 77 || port.relationFrom != 1 || port.relationTo != 2 ||
					port.relationType != "depends_on" || port.relationDesc != "needs" || port.relationActor != 42 {
					t.Fatalf("relation captured=%+v", port)
				}
			},
		},
		{
			name: "attachment.list",
			args: `{"versionId":"77"}`,
			check: func(t *testing.T) {
				if port.listVersionID != 77 || port.listRequirementID != nil {
					t.Fatalf("list captured=%d/%v", port.listVersionID, port.listRequirementID)
				}
			},
		},
		{
			name: "attachment.read_text",
			args: `{"versionId":"77","attachmentId":"5","maxBytes":10}`,
			check: func(t *testing.T) {
				if port.readVersionID != 77 || port.readAttachmentID != 5 || port.readMaxBytes != 10 {
					t.Fatalf("read captured=%d/%d/%d", port.readVersionID, port.readAttachmentID, port.readMaxBytes)
				}
			},
		},
		{
			name: "context.summary_update",
			args: `{"versionId":"77","summary":"summary"}`,
			check: func(t *testing.T) {
				if port.summaryVersionID != 77 || port.summaryText != "summary" || port.summaryActor != 42 {
					t.Fatalf("summary captured=%d/%q/%d", port.summaryVersionID, port.summaryText, port.summaryActor)
				}
			},
		},
		{
			name: "code.tree",
			args: `{"versionId":"77","path":"src","maxEntries":5}`,
			check: func(t *testing.T) {
				if port.treeVersionID != 77 || port.treePath != "src" || port.treeMax != 5 {
					t.Fatalf("tree captured=%d/%q/%d", port.treeVersionID, port.treePath, port.treeMax)
				}
			},
		},
		{
			name: "code.read",
			args: `{"versionId":"77","path":"src/a.go","offset":2,"maxBytes":10}`,
			check: func(t *testing.T) {
				if port.codeReadVersionID != 77 || port.codeReadPath != "src/a.go" ||
					port.codeReadOffset != 2 || port.codeReadMax != 10 {
					t.Fatalf("code read captured=%d/%q/%d/%d", port.codeReadVersionID, port.codeReadPath, port.codeReadOffset, port.codeReadMax)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			port.calls = nil
			result, err := byName[tc.name].Execute(ctx, json.RawMessage(tc.args))
			if err != nil {
				t.Fatalf("%s returned infrastructure error: %v", tc.name, err)
			}
			if result.IsError {
				t.Fatalf("%s returned a business error: %s", tc.name, result.Content)
			}
			if result.Content != "{}" {
				t.Fatalf("%s content=%q want the port payload", tc.name, result.Content)
			}
			if len(port.calls) != 1 || port.calls[0] == "" {
				t.Fatalf("%s port calls=%v want exactly one", tc.name, port.calls)
			}
			tc.check(t)
		})
	}
}

// TestClarificationToolsActorIsNotModelSupplied pins the security rule: the
// actor comes from the invocation context, never from the model arguments.
func TestClarificationToolsActorIsNotModelSupplied(t *testing.T) {
	port := &fakeVersionPort{content: "{}"}
	tools := NewClarificationTools(port)
	byName := map[string]domain.Tool{}
	for _, tool := range tools {
		byName[tool.Name()] = tool
	}

	result, err := byName["requirement.update_draft"].Execute(
		invocationContext(42),
		json.RawMessage(`{"versionId":"77","requirementId":"88","revision":1,"patch":{"title":"x"},"actor":999,"actorId":999}`),
	)
	if err != nil || result.IsError {
		t.Fatalf("update_draft=%q/%v want success", result.Content, err)
	}
	if port.updateActor != 42 {
		t.Fatalf("update_draft actor=%d want the invocation actor 42", port.updateActor)
	}

	// A write outside a run has no actor and must fail closed.
	port.calls = nil
	for _, name := range []string{"requirement.update_draft", "clarification.question_upsert", "context.summary_update"} {
		var args string
		switch name {
		case "requirement.update_draft":
			args = `{"versionId":"77","requirementId":"88","revision":1,"patch":{"title":"x"}}`
		case "clarification.question_upsert":
			args = `{"versionId":"77","question":"why?"}`
		default:
			args = `{"versionId":"77","summary":"s"}`
		}
		result, err := byName[name].Execute(context.Background(), json.RawMessage(args))
		if err != nil {
			t.Fatalf("%s without invocation returned error: %v", name, err)
		}
		if !result.IsError {
			t.Fatalf("%s without invocation succeeded, want a fail-closed tool error", name)
		}
	}
	if len(port.calls) != 0 {
		t.Fatalf("write tools without an invocation reached the port: %v", port.calls)
	}
}

// TestClarificationToolsErrorMapping pins the business/infrastructure split and
// the invalid-argument handling.
func TestClarificationToolsErrorMapping(t *testing.T) {
	ctx := invocationContext(42)

	t.Run("business failures become ToolResult.IsError", func(t *testing.T) {
		port := &fakeVersionPort{business: true}
		tools := NewClarificationTools(port)
		for _, tool := range tools {
			args := minimalArgsFor(t, tool.Name())
			result, err := tool.Execute(ctx, args)
			if err != nil {
				t.Fatalf("%s business failure returned an infrastructure error: %v", tool.Name(), err)
			}
			if !result.IsError || result.Content != "business failure" {
				t.Fatalf("%s result=%+v want IsError with the port content", tool.Name(), result)
			}
		}
	})

	t.Run("infrastructure failures are returned as errors", func(t *testing.T) {
		port := &fakeVersionPort{err: errors.New("database down")}
		tools := NewClarificationTools(port)
		result, err := tools[0].Execute(ctx, json.RawMessage(`{"versionId":"77"}`))
		if err == nil || !strings.Contains(err.Error(), "database down") {
			t.Fatalf("Execute error=%v want the infrastructure failure", err)
		}
		if result.IsError {
			t.Fatalf("infrastructure failure reported a tool-level error: %+v", result)
		}
	})

	t.Run("invalid arguments are correctable", func(t *testing.T) {
		port := &fakeVersionPort{content: "{}"}
		tools := NewClarificationTools(port)
		for _, args := range []string{
			`{}`,
			`{"versionId":"abc"}`,
			`{"versionId":"0"}`,
			`{"versionId":"-3"}`,
		} {
			result, err := tools[0].Execute(ctx, json.RawMessage(args))
			if err != nil {
				t.Fatalf("invalid arguments returned an infrastructure error: %v", err)
			}
			if !result.IsError || !strings.Contains(result.Content, "invalid_arguments") {
				t.Fatalf("invalid arguments %s result=%+v", args, result)
			}
		}
		if len(port.calls) != 0 {
			t.Fatalf("invalid arguments reached the port: %v", port.calls)
		}
	})
}

// minimalArgsFor returns one valid argument object per tool.
func minimalArgsFor(t *testing.T, name string) json.RawMessage {
	t.Helper()
	switch name {
	case "version.context_get":
		return json.RawMessage(`{"versionId":"77"}`)
	case "requirement.update_draft":
		return json.RawMessage(`{"versionId":"77","requirementId":"88","revision":1,"patch":{"title":"x"}}`)
	case "clarification.question_upsert":
		return json.RawMessage(`{"versionId":"77","question":"why?"}`)
	case "clarification.question_answer":
		return json.RawMessage(`{"versionId":"77","questionId":"99","answer":"because"}`)
	case "decision.propose":
		return json.RawMessage(`{"versionId":"77","topic":"t","decision":"d"}`)
	case "relation.propose":
		return json.RawMessage(`{"versionId":"77","fromRequirementId":"1","toRequirementId":"2","relationType":"depends_on"}`)
	case "attachment.list":
		return json.RawMessage(`{"versionId":"77"}`)
	case "attachment.read_text":
		return json.RawMessage(`{"versionId":"77","attachmentId":"5"}`)
	case "context.summary_update":
		return json.RawMessage(`{"versionId":"77","summary":"s"}`)
	case "code.tree":
		return json.RawMessage(`{"versionId":"77"}`)
	case "code.read":
		return json.RawMessage(`{"versionId":"77","path":"src/a.go"}`)
	default:
		t.Fatalf("unexpected tool %q", name)
		return nil
	}
}
