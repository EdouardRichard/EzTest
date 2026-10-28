package domain

// Checkpoint derives the resume point of a case run from its recorded step
// results. Completed seqs are the contiguous run 1..N of terminal (passed or
// failed) steps; interrupted, pending and running steps are not completed and
// stop the checkpoint. The returned nextSeq is the first step the resumed run
// must execute, so a run whose step 2 was interrupted resumes at step 2 without
// re-running completed steps (spec §5.4, §5.12 invariant 5).
//
// The completed slice is always non-nil, which keeps JSON checkpoint payloads
// stable as [] instead of null.
func Checkpoint(steps []StepResult) (completed []int, nextSeq int) {
	completed = []int{}
	statusBySeq := make(map[int]StepStatus, len(steps))
	for _, step := range steps {
		statusBySeq[step.Seq] = step.Status
	}
	for seq := 1; ; seq++ {
		status, ok := statusBySeq[seq]
		if !ok {
			return completed, seq
		}
		if status != StepStatusPassed && status != StepStatusFailed {
			return completed, seq
		}
		completed = append(completed, seq)
	}
}
