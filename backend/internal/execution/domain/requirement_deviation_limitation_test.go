package execution

import "testing"

// v0.2.3 X-02: an executor capability limitation is a structured step marker
// (executor_limitation.code), never a product deviation. These tests pin the
// domain reader and prove it wins over the legacy text heuristics.

func TestStepExecutorLimitationMarker(t *testing.T) {
	cases := []struct {
		name     string
		step     StepResult
		wantOK   bool
		wantCode string
	}{
		{
			name: "structured map with code",
			step: StepResult{Observed: map[string]any{
				ExecutorLimitationKey: map[string]any{"code": "file_chooser_unavailable"},
			}},
			wantOK: true, wantCode: "file_chooser_unavailable",
		},
		{
			name: "structured map without code keeps the marker",
			step: StepResult{Observed: map[string]any{
				ExecutorLimitationKey: map[string]any{},
			}},
			wantOK: true, wantCode: "",
		},
		{
			name:   "structured bool marker",
			step:   StepResult{Observed: map[string]any{ExecutorLimitationKey: true}},
			wantOK: true, wantCode: "",
		},
		{
			name: "string code marker",
			step: StepResult{Observed: map[string]any{
				ExecutorLimitationKey: "hover_unavailable",
			}},
			wantOK: true, wantCode: "hover_unavailable",
		},
		{
			name: "absent marker",
			step: StepResult{Observed: map[string]any{"summary": "ok"}},
		},
		{
			name: "false marker is not a limitation",
			step: StepResult{Observed: map[string]any{ExecutorLimitationKey: false}},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			code, ok := StepExecutorLimitation(test.step)
			if ok != test.wantOK || code != test.wantCode {
				t.Fatalf("StepExecutorLimitation = %q/%v, want %q/%v", code, ok, test.wantCode, test.wantOK)
			}
		})
	}
}

// TestExecutorLimitationSuppressesApplicationDeviation: the observation text
// would match the missing-control heuristic, but the structured marker proves
// the executor could not perform the action, so the step is not product
// evidence and cannot become a failed(application) requirement deviation.
func TestExecutorLimitationSuppressesApplicationDeviation(t *testing.T) {
	step := StepResult{
		Status: StepFailed,
		Observed: map[string]any{
			"summary":             "页面上没有上传按钮，无法选择本地文件",
			ExecutorLimitationKey: map[string]any{"code": "file_chooser_unavailable"},
		},
	}
	if StepObservesMissingFeature(step) {
		t.Fatal("limitation-marked step was read as a missing product control")
	}
	if FailedStepShowsRequirementDeviation(step) {
		t.Fatal("limitation-marked failed step was read as a requirement deviation")
	}
	if StepShowsApplicationDeviation(step) {
		t.Fatal("limitation-marked step was read as application evidence")
	}

	blocked := step
	blocked.Status = StepBlocked
	if StepObservesMissingFeature(blocked) || StepShowsApplicationDeviation(blocked) {
		t.Fatal("limitation-marked blocked step was read as application evidence")
	}
}

func TestExecutorLimitationCodeFromToolError(t *testing.T) {
	cases := []struct {
		name   string
		text   string
		want   string
		wantOK bool
	}{
		{name: "code prefix", text: "file_chooser_unavailable: the executor cannot drive the native file chooser", want: "file_chooser_unavailable", wantOK: true},
		{name: "code inside probe error", text: "browser.upload_file failed (hover_unavailable)", want: "hover_unavailable", wantOK: true},
		{name: "generic unsupported suffix", text: "download_unavailable", want: "download_unavailable", wantOK: true},
		{name: "transport executor unavailable is not a capability limit", text: "executor_unavailable: executor returned HTTP 503"},
		{name: "executor timeout is not a capability limit", text: "executor_timeout: context creation exceeded its deadline"},
		{name: "prose only", text: "工具不支持文件选择与 hover，上传动作未能发起"},
		{name: "empty", text: ""},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			code, ok := ExecutorLimitationCodeFromError(test.text)
			if ok != test.wantOK || code != test.want {
				t.Fatalf("ExecutorLimitationCodeFromError(%q) = %q/%v, want %q/%v", test.text, code, ok, test.want, test.wantOK)
			}
		})
	}
}

func TestExecutorLimitationStepFindsFirstMarkedStep(t *testing.T) {
	steps := []StepResult{
		{Seq: 1, Status: StepPassed},
		{Seq: 2, Status: StepFailed, Observed: map[string]any{
			ExecutorLimitationKey: map[string]any{"code": "hover_unavailable"},
		}},
		{Seq: 3, Status: StepFailed},
	}
	step, code, ok := ExecutorLimitationStep(steps)
	if !ok || step.Seq != 2 || code != "hover_unavailable" {
		t.Fatalf("ExecutorLimitationStep = %+v/%q/%v, want step 2 hover_unavailable", step, code, ok)
	}
}
