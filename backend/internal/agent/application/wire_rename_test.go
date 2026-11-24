package agentapp

import (
	"strings"
	"testing"
)

// wireRenameTask builds a two-surface execution snapshot: the SUT catalog the
// model reads its sut_id from, plus a prose-target step and an assertion that
// names a SUT. v0.2.4-C T14 (R-C1) renames the rendered labels and the task_pack
// result keys from version_sut/version_sut_id/target_version_sut_id to sut/
// sut_id/target_sut_id. v0.2.4-F T12 (SC-F1) removes the design-supplied STEP
// target id (the design column is gone; the step names its target in prose);
// the assertion keeps target_sut_id (the live design case_assertion.sut_id).
// The DTO field names below are updated in lockstep with execution_task_context.go.
func wireRenameTask() ExecutionTask {
	return ExecutionTask{
		CaseRunID:     9101,
		WaveID:        9102,
		IntentID:      9106,
		CaseVersionID: 9104,
		CaseTitle:     "登录后创建订单",
		SUTs: []ExecutionSUT{{
			SUTID:           7,
			SUTName:         "orders",
			EnvironmentName: "test",
			BaseURL:         "http://127.0.0.1:13335",
			AccountRoles:    []string{"admin"},
		}},
		Steps: []ExecutionStep{{
			Seq:               1,
			Action:            "打开订单页",
			Expected:          "订单表单可见",
			AccountRole:       "admin",
			TargetDescription: "订单列表页",
		}},
		Assertions: []ExecutionAssertion{{
			StepSeq:      1,
			AssertionSeq: 1,
			Kind:         "ui",
			Expected:     "订单表单可见",
			TargetSUTID:  7,
		}},
	}
}

// TestWireRenameExecutionBlockRendering pins surface ⑤ (the prompt rendering
// segment): the execution task block labels the SUT selector sut=, never the
// stale version_sut= label that no longer matches any tool argument.
func TestWireRenameExecutionBlockRendering(t *testing.T) {
	block := executionBlock(wireRenameTask())
	if strings.Contains(block, "version_sut=") {
		t.Fatalf("executionBlock still renders version_sut= labels:\n%s", block)
	}
	if !strings.Contains(block, "sut=7") {
		t.Fatalf("executionBlock does not render the sut= selector:\n%s", block)
	}
}

// TestWireRenameTaskPackCarriesSUTID pins surfaces ③ and ⑥ (the task_pack result
// keys AND the value-source carry): the execution snapshot the model reads its
// sut_id from must actually CONTAIN sut_id on every SUT entry and target_sut_id
// on every assertion (the live design case_assertion.sut_id) — not merely exist
// as a tool. A source that exists but drops the value is the B "错而绿" channel.
// Steps carry the prose target_description instead of a design target id
// (v0.2.4-F T12, SC-F1): the old step target_sut_id key must NOT come back.
func TestWireRenameTaskPackCarriesSUTID(t *testing.T) {
	result := executionTaskResult(wireRenameTask())
	if _, stale := result["version_suts"]; stale {
		t.Fatalf("task_pack execution still carries version_suts key")
	}
	suts, ok := result["suts"].([]map[string]any)
	if !ok || len(suts) != 1 {
		t.Fatalf("task_pack execution suts = %#v, want one entry", result["suts"])
	}
	if got, _ := suts[0]["sut_id"].(string); got != "7" {
		t.Fatalf("task_pack SUT entry does not carry sut_id=7: %#v", suts[0])
	}
	if _, stale := suts[0]["version_sut_id"]; stale {
		t.Fatalf("task_pack SUT entry still carries version_sut_id: %#v", suts[0])
	}
	assertions, ok := result["assertions"].([]map[string]any)
	if !ok || len(assertions) != 1 {
		t.Fatalf("task_pack assertions = %#v, want one entry", result["assertions"])
	}
	if got, _ := assertions[0]["target_sut_id"].(string); got != "7" {
		t.Fatalf("task_pack assertion entry does not carry target_sut_id=7: %#v", assertions[0])
	}
	if _, stale := assertions[0]["target_version_sut_id"]; stale {
		t.Fatalf("task_pack assertion entry still carries target_version_sut_id: %#v", assertions[0])
	}
	steps, ok := result["steps"].([]map[string]any)
	if !ok || len(steps) != 1 {
		t.Fatalf("task_pack steps = %#v, want one entry", result["steps"])
	}
	if got, _ := steps[0]["target_description"].(string); got != "订单列表页" {
		t.Fatalf("task_pack step does not carry the prose target_description: %#v", steps[0])
	}
	for _, gone := range []string{"target_sut_id", "target_version_sut_id", "page_id", "api_id"} {
		if _, stale := steps[0][gone]; stale {
			t.Fatalf("task_pack step still carries the design-target key %q: %#v", gone, steps[0])
		}
	}
}

// TestWireRenameTaskPackResultCarriesSUTID pins surface ⑥ end-to-end through the
// context.task_pack tool result: the value the prompt tells the model to read
// (execution.suts[].sut_id) is present in the actual tool payload.
func TestWireRenameTaskPackResultCarriesSUTID(t *testing.T) {
	task := wireRenameTask()
	pack := taskPackResult(9103, 0, 9105, TaskContext{Type: "execute.case"}, &task, nil)
	execution, ok := pack["execution"].(map[string]any)
	if !ok {
		t.Fatalf("task_pack result has no execution block: %#v", pack)
	}
	suts, ok := execution["suts"].([]map[string]any)
	if !ok || len(suts) != 1 {
		t.Fatalf("task_pack execution.suts = %#v", execution["suts"])
	}
	if got, _ := suts[0]["sut_id"].(string); got != "7" {
		t.Fatalf("context.task_pack does not carry the sut_id value the prompt names: %#v", suts[0])
	}
}
