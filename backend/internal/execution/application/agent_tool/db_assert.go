package agent_tool

import (
	"context"
	"encoding/json"

	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/application"
)

// DefaultMaxDBResultBytes and DefaultMaxDBSQLBytes are the spec §16.1 defaults
// of EZTEST_DB_RESULT_MAX_BYTES / EZTEST_DB_SQL_MAX_BYTES. The composition root
// overrides them through Limits.
const (
	DefaultMaxDBResultBytes int64 = application.DefaultDBResultMaxBytes
	DefaultMaxDBSQLBytes    int64 = application.DefaultDBSQLMaxBytes
)

// schemaDBAssert is the one-argument object of execution.db_assert (spec §9.5):
// the agent supplies only the assertion id; SQL, expected value and target are
// read server-side from the pinned snapshot. additionalProperties=false rejects
// any agent-supplied SQL/expected/connection argument.
const schemaDBAssert = `{"type":"object","properties":{"assertionId":{"type":"string","pattern":"^[0-9]+$"}},"required":["assertionId"],"additionalProperties":false}`

// dbAssertDefinitions returns the P7 execution.db_assert tool. It is appended
// after the P6 submission tools so their frozen order is unchanged.
func dbAssertDefinitions() []toolDefinition {
	return []toolDefinition{
		{
			name:        "execution.db_assert",
			description: "Execute one kind=db assertion of this case snapshot with the server-side SQL, expected value and environment target, and record its db_result evidence and assertion_result. Completed by execution.step_record/case_finish; a connection/target failure finishes blocked(environment), SQL/read-only/truncation/expected errors finish inconclusive(case).",
			schema:      schemaDBAssert,
			run:         runDBAssert,
		},
	}
}

// dbAssertInput is the validated argument shape of execution.db_assert. The
// spec §9.5 argument is assertionId; every other field is rejected.
type dbAssertInput struct {
	AssertionID string `json:"assertionId"`
}

// dbAssertValue is the model-facing result of spec §9.5. It carries the verdict
// and a bounded preview only: never the SQL, target credentials or full rows.
type dbAssertValue struct {
	AssertionID   string `json:"assertionId"`
	Verdict       string `json:"verdict"`
	RowCount      int    `json:"rowCount"`
	Truncated     bool   `json:"truncated"`
	ElapsedMS     int64  `json:"elapsedMs"`
	ActualPreview string `json:"actualPreview"`
	Reason        string `json:"reason,omitempty"`
}

// runDBAssert executes execution.db_assert.
func runDBAssert(ctx context.Context, set *toolSet, args json.RawMessage) (agentdomain.ToolResult, error) {
	invocation, err := caseScope(ctx)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	input, err := decodeArguments[dbAssertInput](args)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	assertionID, err := parseDecimalID(input.AssertionID)
	if err != nil {
		return agentdomain.ToolResult{}, invalidArguments("assertionId must be a positive decimal string")
	}
	if set.recorder == nil {
		return agentdomain.ToolResult{}, internalError("the execution recorder is not configured")
	}
	outcome, err := set.recorder.DBAssert(ctx, invocation.CaseRunID, assertionID, application.DBAssertLimits{
		MaxResultBytes: set.limits.MaxDBResultBytes,
		MaxSQLBytes:    set.limits.MaxDBSQLBytes,
	})
	if err != nil {
		return agentdomain.ToolResult{}, mapExecutionError(err)
	}
	return jsonResult(dbAssertValue{
		AssertionID:   decimalID(assertionID),
		Verdict:       outcome.Verdict,
		RowCount:      outcome.RowCount,
		Truncated:     outcome.Truncated,
		ElapsedMS:     outcome.ElapsedMS,
		ActualPreview: outcome.ActualPreview,
		Reason:        outcome.FailureReason,
	})
}
