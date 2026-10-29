package domain

import "sort"

// BatchCounts is the derived status tally of a batch's case runs. It mirrors
// the run_batch count columns (spec §3.2) so the report layer can serve the
// batch summary without reading stale stored counters. Flaky is derived
// separately and is not a stored status.
type BatchCounts struct {
	CaseCount         int
	PassedCount       int
	FailedCount       int
	BlockedCount      int
	InconclusiveCount int
	SkippedCount      int
	InterruptedCount  int
}

// Aggregate counts case runs by terminal status. CaseCount is len(runs); a run
// in a non-terminal status (pending, running) contributes to CaseCount only,
// matching the DDL's sum-of-terminal-counts <= case_count invariant.
func Aggregate(runs []CaseRun) BatchCounts {
	counts := BatchCounts{CaseCount: len(runs)}
	for _, run := range runs {
		switch run.Status {
		case CaseRunStatusPassed:
			counts.PassedCount++
		case CaseRunStatusFailed:
			counts.FailedCount++
		case CaseRunStatusBlocked:
			counts.BlockedCount++
		case CaseRunStatusInconclusive:
			counts.InconclusiveCount++
		case CaseRunStatusSkipped:
			counts.SkippedCount++
		case CaseRunStatusInterrupted:
			counts.InterruptedCount++
		}
	}
	return counts
}

// FlakyCaseIDs returns the case_node ids that were flaky in the supplied
// attempt history: an earlier attempt failed(application) and a later attempt
// passed (spec §5.8). History is ordered by Attempt, never by slice order, and
// is not mutated; a failed(session) or failed(environment) attempt does not
// make a case flaky. The result is deduplicated and sorted ascending so report
// output is deterministic.
func FlakyCaseIDs(runs []CaseRun) []int64 {
	historyByCase := make(map[int64][]CaseRun)
	for _, run := range runs {
		historyByCase[run.CaseNodeID] = append(historyByCase[run.CaseNodeID], run)
	}

	flaky := make([]int64, 0)
	for caseNodeID, history := range historyByCase {
		sort.SliceStable(history, func(i, j int) bool {
			return history[i].Attempt < history[j].Attempt
		})
		failedApplication := false
		for _, run := range history {
			if run.Status == CaseRunStatusPassed {
				if failedApplication {
					flaky = append(flaky, caseNodeID)
					break
				}
				continue
			}
			if run.Status == CaseRunStatusFailed && run.FailureClass != nil &&
				*run.FailureClass == FailureClassApplication {
				failedApplication = true
			}
		}
	}
	sort.Slice(flaky, func(i, j int) bool { return flaky[i] < flaky[j] })
	return flaky
}
