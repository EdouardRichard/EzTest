package agentapp

// Clarification convergence edge. Spec §5.3 makes clarifying exit on
// convergence and spec §5.5 defines the assistant reply types (question /
// confirm_request / progress / done). 0.2.1 does not yet render a structured
// reply part, so the clarification host's explicit "requirement confirmation
// request" is the convergence declaration the production turn can observe. The
// version stage machine owns the actual clarifying→requirement_review edge; this
// file only detects the host's declaration and hands the version to the narrow
// converger port.

import (
	"context"
	"regexp"
	"strings"
)

// ClarificationConverger applies the production clarifying→requirement_review
// edge. The version context implements it; the runner never writes a stage
// itself. An implementation must be idempotent and must refuse to converge a
// version that is not clarifying.
type ClarificationConverger interface {
	Converge(ctx context.Context, versionID int64) error
}

// requirementConfirmationRequest matches the host's explicit convergence
// declaration. It deliberately does not match a turn that merely *mentions* the
// requirement confirmation gate while still asking clarification questions
// ("停在需求确认门等你确认"), which is what keeps the edge from advancing after
// the first turn.
var requirementConfirmationRequest = regexp.MustCompile(
	`(?i)需求确认请求|需求确认卡|confirm_request|confirm\s+(the\s+)?requirements?`,
)

// requestsRequirementConfirmation reports whether one assistant turn explicitly
// asks the user to confirm the requirement drafts.
func requestsRequirementConfirmation(text string) bool {
	return requirementConfirmationRequest.MatchString(strings.TrimSpace(text))
}
