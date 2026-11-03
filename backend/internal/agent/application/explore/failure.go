package explore

// This file owns spec §11 capability #7: deterministic failure attribution and
// self-healing handoff. It classifies one failed/blocked case run as
// application / automation / environment / data, decides whether a retry is
// safe, and drafts a problem report only for an application defect (D47: an AI
// issue is only raised for `failed(application)` or a requirement gap).
//
// The classification is intentionally deterministic: it is the server-side
// baseline the failure_analyst model may refine, never the other way around.
// Only an application failure produces a problem draft; the human handoff owns
// the confirm/reject decision. No case, requirement or issue is written here.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Failure classifications (spec §11 capability #7).
const (
	FailureClassificationApplication = "application"
	FailureClassificationAutomation  = "automation"
	FailureClassificationEnvironment = "environment"
	FailureClassificationData        = "data"
)

// Failure actions: retry the case autonomously, or hand the finding to a human.
const (
	FailureActionRetry = "retry"
	FailureActionHuman = "human"
)

// FailureSignal is the deterministic input of one attribution: the failed run,
// its candidate category/reason and the retry budget.
type FailureSignal struct {
	CaseRunID     int64
	CaseVersionID int64
	CaseTitle     string
	FailureClass  string
	Category      string
	Reason        string
	EvidenceIDs   []int64
	Attempts      int
	MaxAttempts   int
}

// ProblemDraft is the problem-report draft of an application failure. It is a
// draft only: creating/confirming the issue stays a human-authorized action.
type ProblemDraft struct {
	Title       string
	Summary     string
	Category    string
	Severity    string
	DedupeKey   string
	EvidenceIDs []int64
}

// FailureAttributionResult is the deterministic attribution of one run.
type FailureAttributionResult struct {
	Classification string
	Category       string
	Severity       string
	Retryable      bool
	Action         string
	Reason         string
	Problem        *ProblemDraft
	Confidence     float64
}

// failureKeywords are the deterministic evidence families of the failure
// classes. They are matched case-insensitively against the failure reason.
var failureKeywords = map[string][]string{
	FailureClassificationAutomation: {
		"target_not_found", "selector", "locator", "strict mode violation", "invalid selector",
		"stale element", "element not found", "invalid_arguments",
	},
	FailureClassificationEnvironment: {
		"timeout", "timed out", "connection", "network", "dns", "econnreset", "econnrefused",
		"502", "503", "504", "gateway", "browser closed", "executor unavailable", "page crashed",
		"net::err", "page_limit",
	},
	FailureClassificationData: {
		"account_secret", "account", "credential", "login_url_not_configured", "storage_state",
		"test data", "dataset", "fixture", "seed", "missing data", "401", "403",
	},
}

// FailureAttribution is the deterministic failure classifier.
type FailureAttribution struct{}

// NewFailureAttribution wires the classifier.
func NewFailureAttribution() *FailureAttribution { return &FailureAttribution{} }

// Classify returns the attribution of one failure signal. Only an application
// failure carries a problem-report draft; other classifications are recorded as
// an execution finding and, where safe, retried.
func (f *FailureAttribution) Classify(ctx context.Context, versionID int64, signal FailureSignal) (FailureAttributionResult, error) {
	if versionID <= 0 {
		return FailureAttributionResult{}, errors.New("failure attribution requires a positive version id")
	}
	if err := ctx.Err(); err != nil {
		return FailureAttributionResult{}, err
	}

	classification := classifyFailure(signal)
	category := strings.ToLower(strings.TrimSpace(signal.Category))
	if category == "" {
		category = defaultFailureCategory(classification)
	}
	severity := failureSeverity(classification, category)
	attempts := signal.Attempts
	if attempts < 0 {
		attempts = 0
	}
	maxAttempts := signal.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	retryable := classification == FailureClassificationEnvironment && attempts < maxAttempts
	action := FailureActionHuman
	if retryable {
		action = FailureActionRetry
	}

	result := FailureAttributionResult{
		Classification: classification,
		Category:       category,
		Severity:       severity,
		Retryable:      retryable,
		Action:         action,
		Reason:         attributionReason(classification, retryable),
		Confidence:     failureConfidence(classification),
	}
	if classification == FailureClassificationApplication {
		result.Problem = problemDraft(versionID, signal, category, severity)
	}
	return result, nil
}

func classifyFailure(signal FailureSignal) string {
	reason := strings.ToLower(strings.TrimSpace(signal.Reason))
	for _, classification := range []string{
		FailureClassificationAutomation,
		FailureClassificationEnvironment,
		FailureClassificationData,
	} {
		for _, keyword := range failureKeywords[classification] {
			if keyword != "" && strings.Contains(reason, keyword) {
				return classification
			}
		}
	}
	// The execution layer hands the nine-value P3 failure taxonomy to
	// execution.result_read; map it onto the four attribution classes when the
	// failure reason carries no keyword evidence of its own.
	if classification, ok := failureClassFromSignal(signal.FailureClass); ok {
		return classification
	}
	// An unrecognised class falls back to an application defect; the blocked
	// class is already mapped to environment above.
	return FailureClassificationApplication
}

// failureClassFromSignal maps the P3 nine-value failure class (and the
// four-value attribution class) onto the attribution enum. An unknown value
// falls through to the application default.
func failureClassFromSignal(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case FailureClassificationApplication, "failed":
		return FailureClassificationApplication, true
	case FailureClassificationAutomation, "model":
		return FailureClassificationAutomation, true
	case FailureClassificationEnvironment, "timeout", "executor_lost", "blocked":
		return FailureClassificationEnvironment, true
	case FailureClassificationData, "session":
		return FailureClassificationData, true
	default:
		return "", false
	}
}

func defaultFailureCategory(classification string) string {
	switch classification {
	case FailureClassificationAutomation:
		return "ui"
	case FailureClassificationEnvironment:
		return "environment"
	case FailureClassificationData:
		return "data"
	default:
		return "application"
	}
}

func failureSeverity(classification, category string) string {
	switch classification {
	case FailureClassificationAutomation, FailureClassificationData:
		return "minor"
	case FailureClassificationEnvironment:
		return "major"
	}
	switch category {
	case "db", "api", "data":
		return "major"
	default:
		return "minor"
	}
}

func attributionReason(classification string, retryable bool) string {
	switch classification {
	case FailureClassificationAutomation:
		return "用例自动化步骤不可执行（选择器/定位），需要修正用例"
	case FailureClassificationEnvironment:
		if retryable {
			return "环境/网络暂时不可用，可在预算内重试"
		}
		return "环境/网络故障已耗尽重试预算，需要人工处理"
	case FailureClassificationData:
		return "测试数据/账号/登录前置缺失，需要补充数据后重跑"
	default:
		return "应用行为与预期不符，已生成问题草稿等待人工确认"
	}
}

func failureConfidence(classification string) float64 {
	switch classification {
	case FailureClassificationEnvironment:
		return 0.9
	case FailureClassificationAutomation:
		return 0.85
	case FailureClassificationData:
		return 0.8
	default:
		return 0.7
	}
}

// problemDraft builds the deduplicated problem-report draft of an application
// failure (D47 dedupe key: version | case version | classification | reason).
func problemDraft(versionID int64, signal FailureSignal, category, severity string) *ProblemDraft {
	reason := strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(signal.Reason))), " ")
	title := strings.TrimSpace(signal.CaseTitle)
	if title == "" {
		title = fmt.Sprintf("case_run %d", signal.CaseRunID)
	}
	return &ProblemDraft{
		Title:       clipString("[缺陷] "+title, 200),
		Summary:     clipString(strings.TrimSpace(signal.Reason), 1000),
		Category:    category,
		Severity:    severity,
		DedupeKey:   failureDedupeKey(versionID, signal.CaseVersionID, reason),
		EvidenceIDs: append([]int64(nil), signal.EvidenceIDs...),
	}
}

func failureDedupeKey(versionID, caseVersionID int64, normalizedReason string) string {
	identity := fmt.Sprintf("%d|%d|%s|%s", versionID, caseVersionID, FailureClassificationApplication, normalizedReason)
	sum := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(sum[:])
}
