package agentapp

import (
	"errors"
	"fmt"
	"time"

	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

// NewLLMSummarizer is the test-only constructor kept after W1-1 removed the
// unused production factory.
func NewLLMSummarizer(chat llmapp.ChatGateway) *LLMSummarizer {
	return &LLMSummarizer{chat: chat}
}

// NewCoveredMessageID applies the R-A5-12 monotonic watermark rule. It is kept
// as a test-only helper after W1-1 removed the unused production constructor.
func NewCoveredMessageID(previous, candidate int64) (int64, error) {
	if previous < 0 || candidate < 0 {
		return 0, errors.New("agentapp: summary covered_message_id must not be negative")
	}
	if candidate == 0 {
		return previous, nil
	}
	if candidate < previous {
		return 0, fmt.Errorf("%w: previous=%d candidate=%d", ErrSummaryWatermarkStale, previous, candidate)
	}
	return candidate, nil
}

// NewSummaryLedger returns an empty in-process ledger for tests. The production
// reference implementation was unused and removed by W1-1.
func NewSummaryLedger() *SummaryLedger {
	return &SummaryLedger{now: time.Now, latest: make(map[int64]SummaryRecord)}
}
