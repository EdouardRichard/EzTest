package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// analysisTitleMaxRunes is the issue.title length bound of the DDL. A fallback
// title derived from a description is truncated to it so the insert succeeds.
const analysisTitleMaxRunes = 200

// analysisDefaultFailureSeverity is the conservative severity of a fallback
// issue whose candidate lost its severity during validation.
const analysisDefaultFailureSeverity = SeverityMajor

// ValidateFailureCandidates enforces the failure-analysis contract of spec
// §5.6/§9.3. allowedCaseRuns maps every failed(application) case run id of the
// batch to its case node id. allowedEvidenceIDs, when non-nil, is the set of
// evidence ids of those failed cases: an evidence id outside it is removed with
// a warning (a nil set means the caller cannot prove ownership and only checks
// positivity). Candidates that reference another case, claim another failure
// class, carry an invalid enum or lack both an evidence and a code reference are
// dropped; a malformed code reference or evidence id is removed. The returned
// warnings are short field-level messages and never fail the run.
func ValidateFailureCandidates(allowedCaseRuns map[int64]int64, allowedEvidenceIDs map[int64]struct{}, candidates []FailureCandidate) ([]FailureCandidate, []string) {
	valid := make([]FailureCandidate, 0, len(candidates))
	warnings := make([]string, 0)
	seen := make(map[int64]struct{}, len(candidates))

	for index, candidate := range candidates {
		label := fmt.Sprintf("candidate %d", index+1)
		if candidate.FailureClass != FailureClassApplication {
			warnings = append(warnings, fmt.Sprintf("%s: failure_class must be %q", label, FailureClassApplication))
			continue
		}
		expectedNode, ok := allowedCaseRuns[candidate.CaseRunID]
		if !ok {
			warnings = append(warnings, fmt.Sprintf("%s: case_run %d is not a failed application case run of the batch", label, candidate.CaseRunID))
			continue
		}
		if candidate.CaseNodeID != expectedNode {
			warnings = append(warnings, fmt.Sprintf("%s: case_node %d does not belong to case_run %d", label, candidate.CaseNodeID, candidate.CaseRunID))
			continue
		}
		candidate.Title = strings.TrimSpace(candidate.Title)
		if candidate.Title == "" {
			warnings = append(warnings, label+": title must not be empty")
			continue
		}
		if err := ValidateSeverity(candidate.Severity); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: invalid severity %q", label, candidate.Severity))
			continue
		}
		if err := ValidateConfidence(candidate.Confidence); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: invalid confidence %q", label, candidate.Confidence))
			continue
		}
		if _, duplicate := seen[candidate.CaseRunID]; duplicate {
			warnings = append(warnings, fmt.Sprintf("%s: duplicate candidate for case_run %d", label, candidate.CaseRunID))
			continue
		}

		sanitizedRefs, refWarnings := sanitizeAnalysisCodeRefs(candidate.CodeRefs, label)
		warnings = append(warnings, refWarnings...)
		candidate.CodeRefs = sanitizedRefs

		evidenceIDs, evidenceWarnings := filterAnalysisEvidenceIDs(candidate.EvidenceIDs, allowedEvidenceIDs, label)
		warnings = append(warnings, evidenceWarnings...)
		candidate.EvidenceIDs = evidenceIDs

		if len(candidate.EvidenceIDs) == 0 && len(candidate.CodeRefs) == 0 {
			warnings = append(warnings, label+": candidate needs evidence or a code reference")
			continue
		}
		if len(valid) >= AnalysisMaxFailureCandidates {
			warnings = append(warnings, fmt.Sprintf("%s: candidate limit %d reached", label, AnalysisMaxFailureCandidates))
			break
		}
		seen[candidate.CaseRunID] = struct{}{}
		valid = append(valid, candidate)
	}
	return valid, warnings
}

// ValidateIssueDrafts enforces the issue-triage draft contract of spec
// §5.4/§9.4 on top of ValidateIssueDraft: duplicates are dropped, malformed
// code references and evidence ids outside allowedEvidenceIDs are removed with a
// warning, the cap is applied, and invalid drafts become warnings instead of
// failing the run. A nil allowedEvidenceIDs set means the caller cannot prove
// evidence ownership and only positivity is checked. The returned drafts are
// safe to hand to IssueService.ApplyDrafts.
func ValidateIssueDrafts(drafts []IssueDraft, allowedEvidenceIDs map[int64]struct{}) ([]IssueDraft, []string) {
	valid := make([]IssueDraft, 0, len(drafts))
	warnings := make([]string, 0)
	seen := make(map[string]struct{}, len(drafts))

	for index, draft := range drafts {
		label := fmt.Sprintf("draft %d", index+1)
		if draft.DedupeKey != "" {
			if _, duplicate := seen[draft.DedupeKey]; duplicate {
				warnings = append(warnings, fmt.Sprintf("%s: duplicate dedupe key", label))
				continue
			}
		}
		sanitizedRefs, refWarnings := sanitizeAnalysisCodeRefs(draft.CodeRefs, label)
		warnings = append(warnings, refWarnings...)
		draft.CodeRefs = sanitizedRefs
		evidenceIDs, evidenceWarnings := filterAnalysisEvidenceIDs(draft.EvidenceIDs, allowedEvidenceIDs, label)
		warnings = append(warnings, evidenceWarnings...)
		draft.EvidenceIDs = evidenceIDs
		if err := ValidateIssueDraft(draft); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %s", label, analysisDraftValidationMessage(err)))
			continue
		}
		if len(valid) >= AnalysisMaxIssueDrafts {
			warnings = append(warnings, fmt.Sprintf("%s: issue draft limit %d reached", label, AnalysisMaxIssueDrafts))
			break
		}
		draft.Title = strings.TrimSpace(draft.Title)
		seen[draft.DedupeKey] = struct{}{}
		valid = append(valid, draft)
	}
	return valid, warnings
}

// analysisDraftValidationMessage renders a short non-sensitive validation
// message naming the offending field.
func analysisDraftValidationMessage(err error) string {
	var domainErr *Error
	if errors.As(err, &domainErr) {
		if domainErr.Field != "" {
			return fmt.Sprintf("invalid %s (%s)", domainErr.Field, domainErr.Reason)
		}
		return fmt.Sprintf("invalid (%s)", domainErr.Reason)
	}
	return err.Error()
}

// sanitizeAnalysisCodeRefs drops malformed code references and returns one
// warning per removed reference. No file content is read or recorded.
func sanitizeAnalysisCodeRefs(refs []CodeRef, label string) ([]CodeRef, []string) {
	if len(refs) == 0 {
		return nil, nil
	}
	valid := make([]CodeRef, 0, len(refs))
	warnings := make([]string, 0)
	for index, ref := range refs {
		ref.Path = strings.TrimSpace(ref.Path)
		ref.Reason = strings.TrimSpace(ref.Reason)
		if ref.Path == "" || ref.Line <= 0 || ref.Reason == "" {
			warnings = append(warnings, fmt.Sprintf("%s: code_ref %d needs path, positive line and reason", label, index+1))
			continue
		}
		valid = append(valid, ref)
	}
	return valid, warnings
}

// filterAnalysisEvidenceIDs keeps the positive evidence ids, and when
// allowedEvidenceIDs is non-nil only the ids of that set (the evidence of the
// failed application case runs of the batch). Every removed id yields a short
// warning; the caller never fails the run for a hallucinated or foreign id.
func filterAnalysisEvidenceIDs(ids []int64, allowedEvidenceIDs map[int64]struct{}, label string) ([]int64, []string) {
	if len(ids) == 0 {
		return nil, nil
	}
	filtered := make([]int64, 0, len(ids))
	warnings := make([]string, 0)
	for _, evidenceID := range ids {
		if evidenceID <= 0 {
			warnings = append(warnings, fmt.Sprintf("%s: invalid evidence id %d", label, evidenceID))
			continue
		}
		if allowedEvidenceIDs != nil {
			if _, allowed := allowedEvidenceIDs[evidenceID]; !allowed {
				warnings = append(warnings, fmt.Sprintf("%s: evidence id %d does not belong to a failed application case of the batch", label, evidenceID))
				continue
			}
		}
		filtered = append(filtered, evidenceID)
	}
	return filtered, warnings
}

// FallbackIssueDrafts materializes failure candidates as issue drafts when the
// issue-triage run failed or degraded (spec §5.6). The title is the failure
// summary, the confidence is lowered one step and the dedupe key is the
// deterministic §5.4 failure key.
func FallbackIssueDrafts(versionID int64, candidates []FailureCandidate) []IssueDraft {
	if len(candidates) == 0 {
		return nil
	}
	if versionID <= 0 {
		return nil
	}
	drafts := make([]IssueDraft, 0, len(candidates))
	for _, candidate := range candidates {
		caseNodeID := candidate.CaseNodeID
		caseRunID := candidate.CaseRunID
		if caseNodeID <= 0 || caseRunID <= 0 {
			continue
		}
		failureClass := candidate.FailureClass
		if failureClass == "" {
			failureClass = FailureClassApplication
		}
		reason := strings.TrimSpace(candidate.Title)
		if reason == "" {
			reason = strings.TrimSpace(candidate.Description)
		}
		title := analysisTitleFromFailure(candidate)
		severity := candidate.Severity
		if severity == "" {
			severity = analysisDefaultFailureSeverity
		}
		draft := IssueDraft{
			DedupeKey:    FailureDedupeKey(versionID, caseNodeID, failureClass, reason),
			Title:        title,
			Description:  candidate.Description,
			Severity:     severity,
			Confidence:   DowngradeConfidence(candidate.Confidence),
			Category:     CategoryDefect,
			FailureClass: failureClass,
			CaseRunID:    &caseRunID,
			CaseNodeID:   &caseNodeID,
			EvidenceIDs:  append([]int64(nil), candidate.EvidenceIDs...),
			CodeRefs:     append([]CodeRef(nil), candidate.CodeRefs...),
		}
		drafts = append(drafts, draft)
	}
	if len(drafts) > AnalysisMaxIssueDrafts {
		drafts = drafts[:AnalysisMaxIssueDrafts]
	}
	return drafts
}

// analysisTitleFromFailure builds the §5.6 fallback title: the failure
// assertion summary, else a description excerpt, else a stable placeholder.
func analysisTitleFromFailure(candidate FailureCandidate) string {
	title := strings.TrimSpace(candidate.Title)
	if title == "" {
		title = strings.TrimSpace(candidate.Description)
	}
	if title == "" {
		title = "application failure"
	}
	return analysisTruncateRunes(title, analysisTitleMaxRunes)
}

// DowngradeConfidence lowers one confidence step; an unknown confidence
// degrades to low. It is used by the triage-failure fallback.
func DowngradeConfidence(confidence string) string {
	switch confidence {
	case ConfidenceHigh:
		return ConfidenceMedium
	case ConfidenceMedium:
		return ConfidenceLow
	default:
		return ConfidenceLow
	}
}

// FailureDedupeKey computes the §5.4 deterministic failure dedupe key:
// sha256(version_id | case_node_id | failure_class | normalized_failure_reason).
func FailureDedupeKey(versionID, caseNodeID int64, failureClass, reason string) string {
	normalized := strings.Join(strings.Fields(strings.ToLower(reason)), " ")
	if failureClass == "" {
		failureClass = FailureClassApplication
	}
	payload := fmt.Sprintf("%d|%d|%s|%s", versionID, caseNodeID, failureClass, normalized)
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

// analysisTruncateRunes cuts value to at most max runes on a UTF-8 boundary.
func analysisTruncateRunes(value string, max int) string {
	if max <= 0 {
		return ""
	}
	if utf8.RuneCountInString(value) <= max {
		return value
	}
	runes := []rune(value)
	return string(runes[:max])
}
