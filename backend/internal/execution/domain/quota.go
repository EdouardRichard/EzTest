package execution

// Quota is the frozen concurrency budget (P3-D-11): global running case_runs,
// per version_sut running case_runs, and global cross-system running case_runs.
type Quota struct {
	Global      int
	PerSUT      int
	CrossSystem int
}

// RunningCase is one currently running case_run as seen by the quota check.
// CrossSystem is set when the case uses more than one version_sut.
type RunningCase struct {
	CaseRunID     int64
	VersionSUTIDs []int64
	CrossSystem   bool
}

// Dispatch rejection reasons. They are stable strings so tests and logs can
// assert on them.
const (
	ReasonGlobalConcurrency      = "global_concurrency"
	ReasonPerSUTConcurrency      = "per_sut_concurrency"
	ReasonCrossSystemConcurrency = "cross_system_concurrency"
	ReasonExecutorCapacity       = "executor_capacity"
)

// CanDispatch applies the one-shot quota reservation rules of spec §5.3. The
// candidate must fit every dimension at once: global slot, all of its
// version_sut slots, the cross-system slot, and the executor context capacity.
// A zero/negative quota dimension is treated as unlimited; a zero executor
// capacity means capacity is unknown and does not block.
func CanDispatch(q Quota, running []RunningCase, candidate RunningCase, execCapacity, execActive, candidateContexts int) (bool, string) {
	globalRunning := 0
	crossRunning := 0
	perSUT := make(map[int64]int)
	for _, item := range running {
		globalRunning++
		if item.CrossSystem {
			crossRunning++
		}
		for _, versionSUTID := range item.VersionSUTIDs {
			perSUT[versionSUTID]++
		}
	}

	if q.Global > 0 && globalRunning+1 > q.Global {
		return false, ReasonGlobalConcurrency
	}
	if q.PerSUT > 0 {
		for _, versionSUTID := range candidate.VersionSUTIDs {
			if perSUT[versionSUTID]+1 > q.PerSUT {
				return false, ReasonPerSUTConcurrency
			}
		}
	}
	crossCandidate := candidate.CrossSystem || len(candidate.VersionSUTIDs) > 1
	if q.CrossSystem > 0 && crossCandidate && crossRunning+1 > q.CrossSystem {
		return false, ReasonCrossSystemConcurrency
	}

	contexts := candidateContexts
	if contexts <= 0 {
		contexts = len(candidate.VersionSUTIDs)
	}
	if contexts <= 0 {
		contexts = 1
	}
	if execCapacity > 0 && execActive+contexts > execCapacity {
		return false, ReasonExecutorCapacity
	}
	return true, ""
}

// QuotaFromSettings builds a Quota from the three frozen settings defaults. A
// non-positive value falls back to the P3 default.
func QuotaFromSettings(global, perSUT, crossSystem int) Quota {
	if global <= 0 {
		global = 3
	}
	if perSUT <= 0 {
		perSUT = 2
	}
	if crossSystem <= 0 {
		crossSystem = 1
	}
	return Quota{Global: global, PerSUT: perSUT, CrossSystem: crossSystem}
}
