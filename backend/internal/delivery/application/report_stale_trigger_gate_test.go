package deliveryapp

// v0.2.4-G T11 (fix round 1 minor #2): the causal stale trigger vocabulary is a
// declared routing contract. This gate pins every member against a REAL backend
// PRODUCER so a renamed/removed producer event fails the gate. It is
// discriminative: it strips comments and only accepts the quoted literal on a
// publish call site, an event `Type:` field, or a producer const declaration —
// NOT on a consumer routing table or a comment, so a deleted producer whose
// literal survives only in a consumer/comment cannot keep the gate green.
//
// Limitation (disclosed, R-G10): this is a closed routing table — it cannot
// auto-enroll a NEW conclusion-input event added later. The periodic complete
// sweep is the safety net that heals any uncovered or lost path.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	blockCommentRE = regexp.MustCompile(`(?s)/\*.*?\*/`)
	lineCommentRE  = regexp.MustCompile(`//[^\n]*`)
	constAssignRE  = regexp.MustCompile(`=\s*"`)
)

// producerLines returns the comment-stripped source lines of every non-test Go
// file under backend (internal contexts AND the cmd composition root, which
// produces design.judge_exhausted), excluding the trigger definition itself.
func producerLines(t *testing.T) []string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve backend root: %v", err)
	}
	var lines []string
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if filepath.Base(path) == "report_stale_trigger.go" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		body := blockCommentRE.ReplaceAll(data, nil)
		body = lineCommentRE.ReplaceAll(body, nil)
		lines = append(lines, strings.Split(string(body), "\n")...)
		return nil
	})
	if err != nil {
		t.Fatalf("walk sources: %v", err)
	}
	return lines
}

// isProducerLine reports whether a comment-stripped line carrying the literal is
// a producer site: a publish call, an event Type field, or a const declaration.
func isProducerLine(line string) bool {
	lower := strings.ToLower(line)
	return strings.Contains(lower, "publish") ||
		strings.Contains(line, "Type:") ||
		constAssignRE.MatchString(line)
}

func TestStaleTriggerProducersExist(t *testing.T) {
	lines := producerLines(t)
	for eventType := range reportStaleTriggerEvents {
		needle := `"` + eventType + `"`
		found := false
		for _, line := range lines {
			if strings.Contains(line, needle) && isProducerLine(line) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("stale trigger %q has no backend producer publish site (renamed/removed event?)", eventType)
		}
	}
}

// TestStaleTriggerGateIgnoresConsumersAndComments proves the gate is
// discriminative (fix round 1 minor #2): a literal that appears ONLY in a
// consumer routing table or a comment is NOT a producer.
func TestStaleTriggerGateIgnoresConsumersAndComments(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{`publishEvent(ctx, versionID, "design.case_confirmed", map[string]any{`, true},
		{`Type:    "design.judge_exhausted",`, true},
		{`EventEvidenceCreated   = "evidence.created"`, true},
		{`case "issue.updated": // consumer switch`, false},
		{`"issue.updated": handler, // consumer routing table`, false},
		{`// the producer emits "version.waived" here`, false},
	}
	for _, tc := range cases {
		body := lineCommentRE.ReplaceAllString(blockCommentRE.ReplaceAllString(tc.line, ""), "")
		got := strings.Contains(body, `"`) && isProducerLine(body)
		if got != tc.want {
			t.Errorf("isProducerLine(%q) = %v, want %v", tc.line, got, tc.want)
		}
	}
}
