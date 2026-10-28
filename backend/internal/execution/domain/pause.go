package domain

// CanPause reports whether a batch in the given status may be paused. Terminal
// batches cannot be paused (the API answers batch_finished); both active
// statuses, planned and running, may be paused. A paused batch keeps its
// running status so it still occupies the version's single active-batch slot
// (spec §5.4, §5.12 invariant 1).
func CanPause(status BatchStatus) bool {
	return status == BatchStatusPlanned || status == BatchStatusRunning
}

// PauseCaseOutcome decides what happens to one running case run when a pause is
// requested. A graceful pause parks the run as pending once the in-flight step
// has been recorded, so it never produces an interrupted run and needs no
// cleanup on resume. An immediate pause always interrupts the run and requires
// cleanup/seed before the same-attempt resume (spec §5.4, §5.12 invariant 4).
//
// stepInFlight is accepted so the engine can call this at any step boundary.
// Both modes deliberately make the same status decision regardless of it:
// graceful waits for the current step and immediate always interrupts.
func PauseCaseOutcome(mode PauseMode, stepInFlight bool) (CaseRunStatus, bool) {
	if mode == PauseModeImmediate {
		return CaseRunStatusInterrupted, true
	}
	return CaseRunStatusPending, false
}
