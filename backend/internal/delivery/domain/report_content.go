package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ReportContent is the deterministic, secret-free snapshot of one batch report
// (spec §5.5). The field set matches the `eztest.report.v1` JSON contract; ids
// are decimal strings, and every slice is encoded as an array (never null) so
// the snapshot bytes are stable.
type ReportContent struct {
	Schema       string              `json:"schema"`
	Version      ReportVersion       `json:"version"`
	Batch        ReportBatch         `json:"batch"`
	Summary      ReportSummary       `json:"summary"`
	Cases        []ReportCase        `json:"cases"`
	Requirements []ReportRequirement `json:"requirements"`
	Issues       []ReportIssue       `json:"issues"`
	Risks        []string            `json:"risks"`
	Degraded     bool                `json:"degraded"`
	Appendix     ReportAppendix      `json:"appendix"`
}

// ReportVersion is the version header of the snapshot.
type ReportVersion struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Label              string `json:"label"`
	Status             string `json:"status"`
	CodeBaselineCommit string `json:"codeBaselineCommit"`
}

// ReportBatch is the batch header of the snapshot.
type ReportBatch struct {
	ID             string `json:"id"`
	BatchNo        int    `json:"batchNo"`
	Kind           string `json:"kind"`
	Status         string `json:"status"`
	BaselineHash   string `json:"baselineHash"`
	GraphHash      string `json:"graphHash"`
	CodeBaseCommit string `json:"codeBaseCommit"`
	HeadCommit     string `json:"headCommit"`
	StartedAt      string `json:"startedAt"`
	FinishedAt     string `json:"finishedAt"`
}

// ReportSummary is the batch tally of the snapshot.
type ReportSummary struct {
	CaseCount    int `json:"caseCount"`
	Passed       int `json:"passed"`
	Failed       int `json:"failed"`
	Blocked      int `json:"blocked"`
	Inconclusive int `json:"inconclusive"`
	Skipped      int `json:"skipped"`
	Interrupted  int `json:"interrupted"`
	FlakyCount   int `json:"flakyCount"`
}

// ReportCase is one latest case run of the batch.
type ReportCase struct {
	CaseRunID      string `json:"caseRunId"`
	CaseNodeID     string `json:"caseNodeId"`
	Title          string `json:"title"`
	Source         string `json:"source"`
	Status         string `json:"status"`
	FailureClass   string `json:"failureClass"`
	Attempt        int    `json:"attempt"`
	StepCount      int    `json:"stepCount"`
	AssertionCount int    `json:"assertionCount"`
	EvidenceCount  int    `json:"evidenceCount"`
}

// ReportRequirement is one requirement coverage row.
type ReportRequirement struct {
	RequirementID string `json:"requirementId"`
	Title         string `json:"title"`
	Status        string `json:"status"`
	CaseCount     int    `json:"caseCount"`
	Passed        int    `json:"passed"`
	Failed        int    `json:"failed"`
}

// ReportIssue is one issue row. It carries ids/counts/classification only: the
// issue body, evidence content and code snippets never enter the snapshot.
type ReportIssue struct {
	IssueID    string          `json:"issueId"`
	Title      string          `json:"title"`
	Severity   string          `json:"severity"`
	Status     string          `json:"status"`
	Confidence string          `json:"confidence"`
	Category   string          `json:"category"`
	CaseNodeID string          `json:"caseNodeId"`
	CodeRefs   []ReportCodeRef `json:"codeRefs"`
}

// ReportCodeRef is one code location of an issue.
type ReportCodeRef struct {
	Path string `json:"path"`
	Line int    `json:"line"`
}

// ReportAppendix is the context footer of the snapshot.
type ReportAppendix struct {
	Environment string `json:"environment"`
	System      string `json:"system"`
	Commit      string `json:"commit"`
	GeneratedAt string `json:"generatedAt"`
}

// ReportFingerprintInput is the canonical input of the report fingerprint of
// spec §5.5. IDs are decimal strings; timestamps are RFC3339 UTC.
type ReportFingerprintInput struct {
	Batch          ReportFingerprintBatch     `json:"batch"`
	LatestCaseRuns []ReportFingerprintCaseRun `json:"latestCaseRunsPerCaseNode"`
	Issues         []ReportFingerprintIssue   `json:"issuesLinkedToBatchOrOpen"`
}

// ReportFingerprintBatch is the batch part of the fingerprint input.
type ReportFingerprintBatch struct {
	ID         string                  `json:"id"`
	Status     string                  `json:"status"`
	FinishedAt string                  `json:"finishedAt"`
	Counts     ReportFingerprintCounts `json:"counts"`
}

// ReportFingerprintCounts are the persisted batch counters.
type ReportFingerprintCounts struct {
	CaseCount    int `json:"caseCount"`
	Passed       int `json:"passed"`
	Failed       int `json:"failed"`
	Blocked      int `json:"blocked"`
	Inconclusive int `json:"inconclusive"`
	Skipped      int `json:"skipped"`
	Interrupted  int `json:"interrupted"`
}

// ReportFingerprintCaseRun is one latest case run of the fingerprint input.
type ReportFingerprintCaseRun struct {
	CaseRunID    string `json:"caseRunId"`
	Attempt      int    `json:"attempt"`
	Status       string `json:"status"`
	FailureClass string `json:"failureClass"`
	UpdatedAt    string `json:"updatedAt"`
}

// ReportFingerprintIssue is one linked/open issue of the fingerprint input.
type ReportFingerprintIssue struct {
	IssueID   string `json:"issueId"`
	Status    string `json:"status"`
	Revision  int    `json:"revision"`
	UpdatedAt string `json:"updatedAt"`
}

// CanonicalJSON normalizes the ordering rules of spec §5.5 and marshals the
// snapshot deterministically. Two snapshots of the same inputs therefore
// produce byte-identical JSON.
func (c ReportContent) CanonicalJSON() ([]byte, error) {
	normalized := c.Normalized()
	return json.Marshal(normalized)
}

// Normalized returns a copy of the snapshot with the §5.5 ordering applied:
// cases by case_node_id, requirements by requirement id, issues by severity
// weight then issue id descending, code refs by path then line, and every empty
// slice as a non-nil empty slice.
func (c ReportContent) Normalized() ReportContent {
	normalized := c
	normalized.Cases = append([]ReportCase(nil), c.Cases...)
	sort.SliceStable(normalized.Cases, func(i, j int) bool {
		left, right := normalized.Cases[i], normalized.Cases[j]
		if left.CaseNodeID != right.CaseNodeID {
			return reportNumericIDLess(left.CaseNodeID, right.CaseNodeID)
		}
		return reportNumericIDLess(left.CaseRunID, right.CaseRunID)
	})
	if normalized.Cases == nil {
		normalized.Cases = []ReportCase{}
	}

	normalized.Requirements = append([]ReportRequirement(nil), c.Requirements...)
	sort.SliceStable(normalized.Requirements, func(i, j int) bool {
		return reportNumericIDLess(normalized.Requirements[i].RequirementID, normalized.Requirements[j].RequirementID)
	})
	if normalized.Requirements == nil {
		normalized.Requirements = []ReportRequirement{}
	}

	normalized.Issues = append([]ReportIssue(nil), c.Issues...)
	for index := range normalized.Issues {
		refs := append([]ReportCodeRef(nil), normalized.Issues[index].CodeRefs...)
		sort.SliceStable(refs, func(i, j int) bool {
			if refs[i].Path != refs[j].Path {
				return refs[i].Path < refs[j].Path
			}
			return refs[i].Line < refs[j].Line
		})
		if refs == nil {
			refs = []ReportCodeRef{}
		}
		normalized.Issues[index].CodeRefs = refs
	}
	sort.SliceStable(normalized.Issues, func(i, j int) bool {
		left, right := normalized.Issues[i], normalized.Issues[j]
		leftRank, rightRank := reportSeverityRank(left.Severity), reportSeverityRank(right.Severity)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		return reportNumericIDLess(right.IssueID, left.IssueID)
	})
	if normalized.Issues == nil {
		normalized.Issues = []ReportIssue{}
	}

	normalized.Risks = append([]string(nil), c.Risks...)
	if normalized.Risks == nil {
		normalized.Risks = []string{}
	}
	return normalized
}

// Validate checks the structural invariants of an `eztest.report.v1` snapshot.
func (c ReportContent) Validate() error {
	if c.Schema != ReportSchemaV1 {
		return fmt.Errorf("content schema %q is not %q", c.Schema, ReportSchemaV1)
	}
	if !reportPositiveDecimalID(c.Version.ID) {
		return fmt.Errorf("content version id %q is invalid", c.Version.ID)
	}
	if !reportPositiveDecimalID(c.Batch.ID) {
		return fmt.Errorf("content batch id %q is invalid", c.Batch.ID)
	}
	if c.Batch.BatchNo < 1 {
		return fmt.Errorf("content batch number must be positive")
	}
	switch c.Batch.Kind {
	case "baseline", "incremental", "supplement":
	default:
		return fmt.Errorf("content batch kind %q is invalid", c.Batch.Kind)
	}
	switch c.Batch.Status {
	case "planned", "running", "done", "failed", "cancelled":
	default:
		return fmt.Errorf("content batch status %q is invalid", c.Batch.Status)
	}
	if c.Summary.CaseCount < 0 || c.Summary.Passed < 0 || c.Summary.Failed < 0 ||
		c.Summary.Blocked < 0 || c.Summary.Inconclusive < 0 || c.Summary.Skipped < 0 ||
		c.Summary.Interrupted < 0 || c.Summary.FlakyCount < 0 {
		return fmt.Errorf("content summary counts must not be negative")
	}
	terminal := c.Summary.Passed + c.Summary.Failed + c.Summary.Blocked +
		c.Summary.Inconclusive + c.Summary.Skipped + c.Summary.Interrupted
	if terminal > c.Summary.CaseCount {
		return fmt.Errorf("content summary terminal counts exceed the case count")
	}
	for index, item := range c.Cases {
		if err := item.validate(); err != nil {
			return fmt.Errorf("content cases[%d]: %w", index, err)
		}
	}
	for index, item := range c.Requirements {
		if !reportPositiveDecimalID(item.RequirementID) {
			return fmt.Errorf("content requirements[%d].requirementId %q is invalid", index, item.RequirementID)
		}
		if item.CaseCount < 0 || item.Passed < 0 || item.Failed < 0 {
			return fmt.Errorf("content requirements[%d] counts must not be negative", index)
		}
		if item.Passed+item.Failed > item.CaseCount {
			return fmt.Errorf("content requirements[%d] passed+failed exceeds the case count", index)
		}
	}
	for index, item := range c.Issues {
		if err := item.validate(); err != nil {
			return fmt.Errorf("content issues[%d]: %w", index, err)
		}
	}
	for index, risk := range c.Risks {
		if strings.TrimSpace(risk) == "" {
			return fmt.Errorf("content risks[%d] must not be empty", index)
		}
	}
	if _, err := time.Parse(time.RFC3339, c.Appendix.GeneratedAt); err != nil {
		return fmt.Errorf("content appendix generatedAt %q is not RFC3339", c.Appendix.GeneratedAt)
	}
	return nil
}

// validate checks one case row of the snapshot.
func (c ReportCase) validate() error {
	if !reportPositiveDecimalID(c.CaseNodeID) {
		return fmt.Errorf("caseNodeId %q is invalid", c.CaseNodeID)
	}
	if !reportPositiveDecimalID(c.CaseRunID) {
		return fmt.Errorf("caseRunId %q is invalid", c.CaseRunID)
	}
	switch c.Status {
	case "pending", "running", "passed", "failed", "blocked", "inconclusive", "skipped", "interrupted":
	default:
		return fmt.Errorf("status %q is invalid", c.Status)
	}
	if c.Attempt < 1 {
		return fmt.Errorf("attempt must be positive")
	}
	if c.StepCount < 0 || c.AssertionCount < 0 || c.EvidenceCount < 0 {
		return fmt.Errorf("counts must not be negative")
	}
	if c.FailureClass != "" {
		switch c.FailureClass {
		case "application", "environment", "session", "model", "case":
		default:
			return fmt.Errorf("failureClass %q is invalid", c.FailureClass)
		}
	}
	return nil
}

// validate checks one issue row of the snapshot.
func (i ReportIssue) validate() error {
	if !reportPositiveDecimalID(i.IssueID) {
		return fmt.Errorf("issueId %q is invalid", i.IssueID)
	}
	switch i.Severity {
	case "blocker", "major", "minor", "info":
	default:
		return fmt.Errorf("severity %q is invalid", i.Severity)
	}
	switch i.Status {
	case "open", "confirmed", "dismissed":
	default:
		return fmt.Errorf("status %q is invalid", i.Status)
	}
	switch i.Confidence {
	case "high", "medium", "low":
	default:
		return fmt.Errorf("confidence %q is invalid", i.Confidence)
	}
	switch i.Category {
	case "defect", "requirement_gap", "other":
	default:
		return fmt.Errorf("category %q is invalid", i.Category)
	}
	if i.CaseNodeID != "" && !reportPositiveDecimalID(i.CaseNodeID) {
		return fmt.Errorf("caseNodeId %q is invalid", i.CaseNodeID)
	}
	for index, ref := range i.CodeRefs {
		if strings.TrimSpace(ref.Path) == "" {
			return fmt.Errorf("codeRefs[%d].path must not be empty", index)
		}
		if ref.Line < 0 {
			return fmt.Errorf("codeRefs[%d].line must not be negative", index)
		}
	}
	return nil
}

// Compute returns the lowercase hex sha256 of the canonical fingerprint input.
// The input slices are sorted first, so the digest only depends on content, not
// on port iteration order.
func (in ReportFingerprintInput) Compute() (string, error) {
	normalized := in
	normalized.LatestCaseRuns = append([]ReportFingerprintCaseRun(nil), in.LatestCaseRuns...)
	sort.SliceStable(normalized.LatestCaseRuns, func(i, j int) bool {
		return reportNumericIDLess(normalized.LatestCaseRuns[i].CaseRunID, normalized.LatestCaseRuns[j].CaseRunID)
	})
	if normalized.LatestCaseRuns == nil {
		normalized.LatestCaseRuns = []ReportFingerprintCaseRun{}
	}
	normalized.Issues = append([]ReportFingerprintIssue(nil), in.Issues...)
	sort.SliceStable(normalized.Issues, func(i, j int) bool {
		return reportNumericIDLess(normalized.Issues[i].IssueID, normalized.Issues[j].IssueID)
	})
	if normalized.Issues == nil {
		normalized.Issues = []ReportFingerprintIssue{}
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("delivery: marshal report fingerprint: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// reportSeverityRank sorts blocker→major→minor→info; unknown severities sort
// last so malformed input is still deterministic.
func reportSeverityRank(severity string) int {
	switch severity {
	case "blocker":
		return 0
	case "major":
		return 1
	case "minor":
		return 2
	case "info":
		return 3
	default:
		return 4
	}
}

// reportNumericIDLess orders decimal-string ids numerically, falling back to
// lexical order when a value is not an id so sorting is still total.
func reportNumericIDLess(left, right string) bool {
	leftID, leftErr := strconv.ParseInt(left, 10, 64)
	rightID, rightErr := strconv.ParseInt(right, 10, 64)
	if leftErr == nil && rightErr == nil {
		return leftID < rightID
	}
	return left < right
}

// reportPositiveDecimalID reports whether value is a positive decimal int64.
func reportPositiveDecimalID(value string) bool {
	id, err := strconv.ParseInt(value, 10, 64)
	return err == nil && id > 0
}
