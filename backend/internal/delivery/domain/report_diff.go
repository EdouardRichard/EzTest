package domain

import (
	"sort"
	"strconv"
)

// ReportDiffSummary is the classified tally of a report diff (spec §5.5/§7.4).
type ReportDiffSummary struct {
	AddedIssues  int `json:"addedIssues"`
	FixedIssues  int `json:"fixedIssues"`
	Regressed    int `json:"regressed"`
	Recovered    int `json:"recovered"`
	NewCases     int `json:"newCases"`
	RemovedCases int `json:"removedCases"`
	Changed      int `json:"changed"`
}

// ReportDiffIssue is one added or fixed issue of the diff.
type ReportDiffIssue struct {
	IssueID  int64  `json:"issueId"`
	Title    string `json:"title"`
	Severity string `json:"severity"`
}

// ReportDiffCase is one regressed, recovered or changed case of the diff.
type ReportDiffCase struct {
	CaseNodeID int64  `json:"caseNodeId"`
	Title      string `json:"title"`
	From       string `json:"from"`
	To         string `json:"to"`
}

// ReportDiffNewCase is one case that only exists in the base (removed) or only
// in the target (new) report.
type ReportDiffNewCase struct {
	CaseNodeID int64  `json:"caseNodeId"`
	Title      string `json:"title"`
	Source     string `json:"source"`
}

// ReportDiff is the deterministic comparison of two report snapshots of the same
// version. All detail slices are ordered by case node id or issue id.
type ReportDiff struct {
	BaseReportID   int64
	TargetReportID int64
	BaseBatchNo    int
	TargetBatchNo  int

	Summary        ReportDiffSummary
	AddedIssues    []ReportDiffIssue
	FixedIssues    []ReportDiffIssue
	RegressedCases []ReportDiffCase
	RecoveredCases []ReportDiffCase
	ChangedCases   []ReportDiffCase
	NewCases       []ReportDiffNewCase
	RemovedCases   []ReportDiffNewCase
}

// BuildReportDiff compares two report snapshots with the classification rules of
// spec §5.5:
//
//   - case only in target = new, only in base = removed;
//   - passed→failed/blocked/inconclusive = regressed;
//   - failed/blocked/inconclusive→passed = recovered;
//   - any other status change = changed;
//   - issue only in target = added;
//   - base open/confirmed that is dismissed or absent in the target = fixed;
//   - a dismissed base issue never reappears.
func BuildReportDiff(base, target ReportContent) ReportDiff {
	diff := ReportDiff{
		BaseBatchNo:    base.Batch.BatchNo,
		TargetBatchNo:  target.Batch.BatchNo,
		AddedIssues:    []ReportDiffIssue{},
		FixedIssues:    []ReportDiffIssue{},
		RegressedCases: []ReportDiffCase{},
		RecoveredCases: []ReportDiffCase{},
		ChangedCases:   []ReportDiffCase{},
		NewCases:       []ReportDiffNewCase{},
		RemovedCases:   []ReportDiffNewCase{},
	}

	baseCases := make(map[int64]ReportCase, len(base.Cases))
	for _, item := range base.Cases {
		if id, ok := reportParseID(item.CaseNodeID); ok {
			baseCases[id] = item
		}
	}
	targetCases := make(map[int64]ReportCase, len(target.Cases))
	for _, item := range target.Cases {
		if id, ok := reportParseID(item.CaseNodeID); ok {
			targetCases[id] = item
		}
	}
	for id, targetCase := range targetCases {
		baseCase, existed := baseCases[id]
		if !existed {
			diff.NewCases = append(diff.NewCases, ReportDiffNewCase{
				CaseNodeID: id, Title: targetCase.Title, Source: targetCase.Source,
			})
			continue
		}
		if baseCase.Status == targetCase.Status {
			continue
		}
		switch {
		case targetCase.Status == "passed" && reportFailedStatus(baseCase.Status):
			diff.RecoveredCases = append(diff.RecoveredCases, ReportDiffCase{
				CaseNodeID: id, Title: targetCase.Title, From: baseCase.Status, To: targetCase.Status,
			})
		case baseCase.Status == "passed" && reportFailedStatus(targetCase.Status):
			diff.RegressedCases = append(diff.RegressedCases, ReportDiffCase{
				CaseNodeID: id, Title: targetCase.Title, From: baseCase.Status, To: targetCase.Status,
			})
		default:
			diff.ChangedCases = append(diff.ChangedCases, ReportDiffCase{
				CaseNodeID: id, Title: targetCase.Title, From: baseCase.Status, To: targetCase.Status,
			})
		}
	}
	for id, baseCase := range baseCases {
		if _, existed := targetCases[id]; existed {
			continue
		}
		diff.RemovedCases = append(diff.RemovedCases, ReportDiffNewCase{
			CaseNodeID: id, Title: baseCase.Title, Source: baseCase.Source,
		})
	}

	baseIssues := make(map[int64]ReportIssue, len(base.Issues))
	for _, item := range base.Issues {
		if id, ok := reportParseID(item.IssueID); ok {
			baseIssues[id] = item
		}
	}
	targetIssues := make(map[int64]ReportIssue, len(target.Issues))
	for _, item := range target.Issues {
		if id, ok := reportParseID(item.IssueID); ok {
			targetIssues[id] = item
		}
	}
	for id, targetIssue := range targetIssues {
		if _, existed := baseIssues[id]; existed {
			continue
		}
		diff.AddedIssues = append(diff.AddedIssues, ReportDiffIssue{
			IssueID: id, Title: targetIssue.Title, Severity: targetIssue.Severity,
		})
	}
	for id, baseIssue := range baseIssues {
		if !reportIssueActive(baseIssue.Status) {
			continue
		}
		targetIssue, existed := targetIssues[id]
		if !existed {
			diff.FixedIssues = append(diff.FixedIssues, ReportDiffIssue{
				IssueID: id, Title: baseIssue.Title, Severity: baseIssue.Severity,
			})
			continue
		}
		if targetIssue.Status == "dismissed" {
			title := targetIssue.Title
			if title == "" {
				title = baseIssue.Title
			}
			diff.FixedIssues = append(diff.FixedIssues, ReportDiffIssue{
				IssueID: id, Title: title, Severity: baseIssue.Severity,
			})
		}
	}

	sort.SliceStable(diff.NewCases, func(i, j int) bool { return diff.NewCases[i].CaseNodeID < diff.NewCases[j].CaseNodeID })
	sort.SliceStable(diff.RemovedCases, func(i, j int) bool { return diff.RemovedCases[i].CaseNodeID < diff.RemovedCases[j].CaseNodeID })
	sort.SliceStable(diff.RegressedCases, func(i, j int) bool { return diff.RegressedCases[i].CaseNodeID < diff.RegressedCases[j].CaseNodeID })
	sort.SliceStable(diff.RecoveredCases, func(i, j int) bool { return diff.RecoveredCases[i].CaseNodeID < diff.RecoveredCases[j].CaseNodeID })
	sort.SliceStable(diff.ChangedCases, func(i, j int) bool { return diff.ChangedCases[i].CaseNodeID < diff.ChangedCases[j].CaseNodeID })
	sort.SliceStable(diff.AddedIssues, func(i, j int) bool { return diff.AddedIssues[i].IssueID < diff.AddedIssues[j].IssueID })
	sort.SliceStable(diff.FixedIssues, func(i, j int) bool { return diff.FixedIssues[i].IssueID < diff.FixedIssues[j].IssueID })

	diff.Summary = ReportDiffSummary{
		AddedIssues:  len(diff.AddedIssues),
		FixedIssues:  len(diff.FixedIssues),
		Regressed:    len(diff.RegressedCases),
		Recovered:    len(diff.RecoveredCases),
		NewCases:     len(diff.NewCases),
		RemovedCases: len(diff.RemovedCases),
		Changed:      len(diff.ChangedCases),
	}
	return diff
}

// reportFailedStatus reports whether status is one of the failure buckets
// treated as regressed/recovered by spec §5.5.
func reportFailedStatus(status string) bool {
	switch status {
	case "failed", "blocked", "inconclusive":
		return true
	default:
		return false
	}
}

// reportIssueActive reports whether an issue status counts as open work.
func reportIssueActive(status string) bool {
	return status == "open" || status == "confirmed"
}

// reportParseID parses a decimal-string id from a snapshot.
func reportParseID(value string) (int64, bool) {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}
