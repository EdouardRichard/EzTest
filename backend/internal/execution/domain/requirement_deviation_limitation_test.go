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

// TestExecutorLimitationCodeClosedAllowlist: only known executor capability
// codes qualify. A generic *_unavailable/*_unsupported suffix (service_unavailable,
// endpoint_unavailable, ...) can describe a real SUT/environment failure and must
// never become an executor capability block.
func TestExecutorLimitationCodeClosedAllowlist(t *testing.T) {
	for _, code := range []string{
		"file_chooser_unavailable", "hover_unavailable", "download_unavailable",
		"upload_unavailable", "dialog_unavailable", "clipboard_unavailable", "drag_unavailable",
	} {
		if !IsExecutorLimitationCode(code) {
			t.Fatalf("%q should be a known executor capability code", code)
		}
	}
	for _, code := range []string{
		"service_unavailable", "endpoint_unavailable", "database_unavailable",
		"selector_unsupported", "feature_not_supported", "executor_unavailable",
		"executor_timeout", "executor_context_create_failed", "executor_lost",
		"executor_capability", "",
	} {
		if IsExecutorLimitationCode(code) {
			t.Fatalf("%q must not be treated as an executor capability code", code)
		}
	}
	if _, ok := ExecutorLimitationCodeFromError("service_unavailable: the SUT returned 503"); ok {
		t.Fatal("free-text service_unavailable was accepted as an executor limitation")
	}
}

// TestStepExecutorLimitationRejectsUnknownCode: a legacy/hand-written marker
// carrying an unknown code is ignored instead of blocking a real defect.
func TestStepExecutorLimitationRejectsUnknownCode(t *testing.T) {
	step := StepResult{
		Status: StepFailed,
		Observed: map[string]any{
			"summary":             "服务不可用",
			ExecutorLimitationKey: map[string]any{"code": "service_unavailable"},
		},
	}
	if code, ok := StepExecutorLimitation(step); ok {
		t.Fatalf("unknown marker code %q was read as a limitation", code)
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
