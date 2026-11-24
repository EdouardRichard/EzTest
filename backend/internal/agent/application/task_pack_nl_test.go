package agentapp

import (
	"strings"
	"testing"
)

// v0.2.4-F T12 (SC-F1): the execution input chain is natural language end to
// end. The design contract (v0.2.4-B) deleted the design-supplied target
// SUT/page/API ids from case steps; the case names its system in prose
// (sut_description), lists the acceptance criteria it covers (covered_acs) and
// every step names its target in prose (target_description). These tests pin
// the model-facing surfaces of that contract:
//
//   - surface ③: the context.task_pack execution result keys;
//   - surface ⑤: the executionBlock prompt rendering segment;
//   - surface ⑥: the value-source carry — context.task_pack must CONTAIN the
//     values the prompt names, not merely exist as a tool.
//
// Prior-step/assertion target_sut_id is FACTUAL attribution (the SUT a recorded
// result actually ran against, needed for resume) and is deliberately kept.

func nlExecutionTask() ExecutionTask {
	return ExecutionTask{
		CaseRunID:      9101,
		WaveID:         9102,
		IntentID:       9106,
		CaseVersionID:  9104,
		EnvironmentID:  3,
		CaseTitle:      "登录后创建订单",
		Description:    "用户登录后创建一笔订单",
		SutDescription: "订单中心 Web 端，面向内部运营人员",
		Preconditions:  "数据库中已存在运营账号 seed_user",
		CoveredACs:     []string{"AC-1", "AC-2"},
		Priority:       "P1",
		SUTs: []ExecutionSUT{{
			SUTID:           7,
			SUTName:         "orders",
			Intro:           "订单中心：下单、查询与审核",
			EnvironmentID:   3,
			EnvironmentName: "test",
			BaseURL:         "http://127.0.0.1:13335",
			AccountRoles:    []string{"admin"},
		}},
		Steps: []ExecutionStep{{
			Seq:               1,
			Action:            "打开订单页并新建订单",
			Expected:          "订单表单可见",
			AccountRole:       "admin",
			TargetDescription: "订单列表页右上角的新建订单入口",
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

// TestTaskPackRendersNLFields pins surfaces ③/⑤/⑥ for the natural-language
// execution input: sut_description / covered_acs / per-step target_description
// and the SUT catalog intro / environment_id must reach the model through both
// the tool result and the prompt block.
func TestTaskPackRendersNLFields(t *testing.T) {
	task := nlExecutionTask()

	// --- surface ③: the execution result keys ---
	result := executionTaskResult(task)
	if got, _ := result["sut_description"].(string); got != task.SutDescription {
		t.Fatalf("task_pack execution sut_description = %#v, want %q", result["sut_description"], task.SutDescription)
	}
	acs, ok := result["covered_acs"].([]string)
	if !ok || len(acs) != 2 || acs[0] != "AC-1" || acs[1] != "AC-2" {
		t.Fatalf("task_pack execution covered_acs = %#v, want [AC-1 AC-2]", result["covered_acs"])
	}
	if got, _ := result["preconditions"].(string); got != task.Preconditions {
		t.Fatalf("task_pack execution preconditions = %#v, want %q", result["preconditions"], task.Preconditions)
	}
	if got, _ := result["environment_id"].(string); got != "3" {
		t.Fatalf("task_pack execution environment_id = %#v, want the decimal string \"3\"", result["environment_id"])
	}
	suts, ok := result["suts"].([]map[string]any)
	if !ok || len(suts) != 1 {
		t.Fatalf("task_pack execution suts = %#v, want one entry", result["suts"])
	}
	if got, _ := suts[0]["intro"].(string); got != task.SUTs[0].Intro {
		t.Fatalf("task_pack SUT entry intro = %#v, want %q", suts[0]["intro"], task.SUTs[0].Intro)
	}
	if got, _ := suts[0]["environment_id"].(string); got != "3" {
		t.Fatalf("task_pack SUT entry environment_id = %#v, want \"3\"", suts[0]["environment_id"])
	}
	steps, ok := result["steps"].([]map[string]any)
	if !ok || len(steps) != 1 {
		t.Fatalf("task_pack execution steps = %#v, want one entry", result["steps"])
	}
	if got, _ := steps[0]["target_description"].(string); got != task.Steps[0].TargetDescription {
		t.Fatalf("task_pack step target_description = %#v, want %q", steps[0]["target_description"], task.Steps[0].TargetDescription)
	}
	// The design-supplied target ids are gone from the STEP surface: rendering
	// them would teach the model a contract the design gate no longer accepts.
	for _, gone := range []string{"target_sut_id", "page_id", "api_id"} {
		if _, stale := steps[0][gone]; stale {
			t.Fatalf("task_pack step still renders the design-target key %q: %#v", gone, steps[0])
		}
	}

	// --- surface ⑤: the prompt rendering segment ---
	block := executionBlock(task)
	for _, want := range []string{
		task.SutDescription, "AC-1", "AC-2", task.Preconditions,
		task.Steps[0].TargetDescription, task.SUTs[0].Intro, "environment_id=3",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("executionBlock misses NL material %q:\n%s", want, block)
		}
	}

	// --- surface ⑥: end-to-end through the context.task_pack tool result ---
	pack := taskPackResult(9103, 0, 9105, TaskContext{Type: "execute.case"}, &task, nil)
	execution, ok := pack["execution"].(map[string]any)
	if !ok {
		t.Fatalf("task_pack result has no execution block: %#v", pack)
	}
	if got, _ := execution["sut_description"].(string); got != task.SutDescription {
		t.Fatalf("context.task_pack does not carry the sut_description the prompt names: %#v", execution["sut_description"])
	}
	packSteps, ok := execution["steps"].([]map[string]any)
	if !ok || len(packSteps) != 1 {
		t.Fatalf("context.task_pack execution.steps = %#v", execution["steps"])
	}
	if got, _ := packSteps[0]["target_description"].(string); got != task.Steps[0].TargetDescription {
		t.Fatalf("context.task_pack does not carry the step target_description the prompt names: %#v", packSteps[0])
	}
}

// TestPriorResultAttributionKept pins the SC-F1 carve-out: prior steps and
// assertions keep their FACTUAL target_sut_id (which SUT the recorded result
// actually ran against — resume needs it), while the design-target step ids
// stay removed. The grep-zero rule targets design-target rendering only.
func TestPriorResultAttributionKept(t *testing.T) {
	task := nlExecutionTask()
	task.PriorSteps = []ExecutionPriorStep{{
		Seq: 1, Status: "passed", Observed: "订单表单可见", TargetSUTID: 7,
	}}
	task.PriorAssertions = []ExecutionPriorAssertion{{
		StepSeq: 1, AssertionSeq: 1, Kind: "ui", Verdict: "passed",
		Expected: "订单表单可见", Actual: "订单表单可见", TargetSUTID: 7,
	}}
	result := executionTaskResult(task)
	priorSteps, ok := result["prior_steps"].([]map[string]any)
	if !ok || len(priorSteps) != 1 {
		t.Fatalf("prior_steps = %#v, want one entry", result["prior_steps"])
	}
	if got, _ := priorSteps[0]["target_sut_id"].(string); got != "7" {
		t.Fatalf("prior step lost its factual target_sut_id: %#v", priorSteps[0])
	}
	priorAssertions, ok := result["prior_assertions"].([]map[string]any)
	if !ok || len(priorAssertions) != 1 {
		t.Fatalf("prior_assertions = %#v, want one entry", result["prior_assertions"])
	}
	if got, _ := priorAssertions[0]["target_sut_id"].(string); got != "7" {
		t.Fatalf("prior assertion lost its factual target_sut_id: %#v", priorAssertions[0])
	}
	// The live design contract still names a SUT on db assertions
	// (case_assertion.sut_id): that is a CURRENT design field, not a removed
	// target id, and stays rendered.
	assertions, ok := result["assertions"].([]map[string]any)
	if !ok || len(assertions) != 1 {
		t.Fatalf("assertions = %#v, want one entry", result["assertions"])
	}
	if got, _ := assertions[0]["target_sut_id"].(string); got != "7" {
		t.Fatalf("assertion lost its design sut_id rendering: %#v", assertions[0])
	}
}

// TestCaseExecutorPromptTeachesNLAndDataPrep pins surface ⑤'s teaching side
// (SC-F1 + SC-F13): the case executor prompt must teach the natural-language
// fields, name the value source that actually carries them, and teach the
// data-preparation loop (seed Given preconditions via general SQL/exec, clean
// up afterwards, and an observable failure path — never a silent skip).
func TestCaseExecutorPromptTeachesNLAndDataPrep(t *testing.T) {
	profiles := ExecutionProfiles()
	var prompt string
	for _, profile := range profiles {
		if profile.Key == ProfileKeyCaseExecutor {
			prompt = profile.SystemPrompt
			break
		}
	}
	if prompt == "" {
		t.Fatal("case_executor profile is not registered")
	}
	for _, want := range []string{
		// NL contract fields (the exact key names the pack renders).
		"sut_description", "covered_acs", "target_description", "preconditions",
		// The value source that carries them.
		"context.task_pack",
		// Factual attribution: the sut_id the record tools take.
		"sut_id",
		// Data prep: seed the Given preconditions, clean up, observable failure.
		"前置条件", "清理", "不得静默跳过",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("case_executor prompt is missing the NL/data-prep contract %q", want)
		}
	}
}
