package agentapp

import (
	"context"
	"errors"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// FailureKind is the contract §1 failure classification for work_item runs:
// model_error / tool_failure / budget / aborted / interrupted /
// context_overflow_unrecoverable. It is intentionally narrower than the
// execution failure_class taxonomy: work_item failures describe why the agent
// run stopped, while execution failures classify a test result.
type FailureKind string

const (
	FailureKindModelError                   FailureKind = "model_error"
	FailureKindToolFailure                  FailureKind = "tool_failure"
	FailureKindBudget                       FailureKind = "budget"
	FailureKindAborted                      FailureKind = "aborted"
	FailureKindInterrupted                  FailureKind = "interrupted"
	FailureKindContextOverflowUnrecoverable FailureKind = "context_overflow_unrecoverable"
)

// Recovery actions emitted with a Failure. They are stable, user-actionable
// strings (spec §5.6: the user must learn what happened, the impact and the
// next step). They are not phase labels and never contain model secrets.
const (
	FailureActionRetry            = "自动重试"
	FailureActionSwitchModel      = "更换模型或调整模型配置"
	FailureActionSettlement       = "写回部分产物并生成摘要"
	FailureActionToolRetry        = "重试、更换策略或转人工"
	FailureActionExecutorRecovery = "等待执行器恢复后重排或更换执行器"
	FailureActionAbort            = "无需处理（已取消）"
	FailureActionRequeue          = "按新的 lease epoch 重排"
	FailureActionCompactContext   = "压缩上下文或重建运行"
	FailureActionRetryOrEscalate  = "重试；持续失败时查看错误详情"
)

// Failure is the classified outcome of a failed agent run.
//
// UserVisible distinguishes transient problems that the runtime retries on its
// own (not shown) from failures the user must act on. Action is the next step.
// The zero Failure (empty Kind) means "no failure" (Classify(nil)).
type Failure struct {
	Kind        FailureKind
	Retryable   bool
	UserVisible bool
	Action      string
}

// Classify maps one agent run error onto the contract failure taxonomy and its
// retry/user-visible/action semantics (spec §5.6). It never inspects the
// work_item status: retryability of an item is decided by attempts/max_attempts
// (R41), not by this classification.
func Classify(err error) Failure {
	if err == nil {
		return Failure{}
	}
	if errors.Is(err, context.Canceled) {
		return Failure{Kind: FailureKindAborted, Retryable: false, UserVisible: false, Action: FailureActionAbort}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Failure{Kind: FailureKindBudget, Retryable: false, UserVisible: true, Action: FailureActionSettlement}
	}

	var runErr *RunError
	if errors.As(err, &runErr) {
		switch runErr.Reason {
		case "budget":
			return Failure{Kind: FailureKindBudget, Retryable: false, UserVisible: true, Action: FailureActionSettlement}
		case "aborted":
			return Failure{Kind: FailureKindAborted, Retryable: false, UserVisible: false, Action: FailureActionAbort}
		case "interrupted", "lease_lost":
			return Failure{Kind: FailureKindInterrupted, Retryable: runErr.Retryable, UserVisible: false, Action: FailureActionRequeue}
		case "context_overflow_unrecoverable":
			return Failure{Kind: FailureKindContextOverflowUnrecoverable, Retryable: false, UserVisible: true, Action: FailureActionCompactContext}
		case "tool_failure":
			var apiErr *platform.Error
			if errors.As(err, &apiErr) && apiErr.Code == "executor_unavailable" {
				return Failure{Kind: FailureKindToolFailure, Retryable: true, UserVisible: true, Action: FailureActionExecutorRecovery}
			}
			return Failure{Kind: FailureKindToolFailure, Retryable: runErr.Retryable, UserVisible: true, Action: FailureActionToolRetry}
		case "model_error", "ai_unavailable", "invalid_payload", "profile_not_found":
			if runErr.Retryable {
				// Transient LLM failures are retried with backoff and not shown.
				return Failure{Kind: FailureKindModelError, Retryable: true, UserVisible: false, Action: FailureActionRetry}
			}
			return Failure{Kind: FailureKindModelError, Retryable: false, UserVisible: true, Action: FailureActionSwitchModel}
		default:
			return Failure{Kind: FailureKindToolFailure, Retryable: runErr.Retryable, UserVisible: true, Action: FailureActionRetryOrEscalate}
		}
	}

	var apiErr *platform.Error
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case "lease_lost":
			return Failure{Kind: FailureKindInterrupted, Retryable: false, UserVisible: false, Action: FailureActionRequeue}
		case "context_overflow":
			return Failure{Kind: FailureKindContextOverflowUnrecoverable, Retryable: apiErr.Retryable, UserVisible: true, Action: FailureActionCompactContext}
		case "executor_unavailable":
			return Failure{Kind: FailureKindToolFailure, Retryable: true, UserVisible: true, Action: FailureActionExecutorRecovery}
		case "ai_unavailable", "rate_limited", "provider_unavailable":
			return Failure{Kind: FailureKindModelError, Retryable: true, UserVisible: false, Action: FailureActionRetry}
		case "unavailable":
			return Failure{Kind: FailureKindToolFailure, Retryable: true, UserVisible: true, Action: FailureActionToolRetry}
		}
		if apiErr.Status >= 500 {
			return Failure{Kind: FailureKindToolFailure, Retryable: true, UserVisible: true, Action: FailureActionToolRetry}
		}
		// 4xx: a model/tool configuration or capability problem. Do not retry;
		// tell the user which configuration to change.
		return Failure{Kind: FailureKindModelError, Retryable: false, UserVisible: true, Action: FailureActionSwitchModel}
	}

	if IsLeaseLost(err) {
		return Failure{Kind: FailureKindInterrupted, Retryable: false, UserVisible: false, Action: FailureActionRequeue}
	}
	return Failure{Kind: FailureKindToolFailure, Retryable: true, UserVisible: true, Action: FailureActionRetryOrEscalate}
}
