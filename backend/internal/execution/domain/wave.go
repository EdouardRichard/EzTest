package execution

import "time"

// WaveStatus is the run_wave state machine status (spec §4.2).
type WaveStatus string

const (
	WavePlanned   WaveStatus = "planned"
	WaveRunning   WaveStatus = "running"
	WavePaused    WaveStatus = "paused"
	WaveDone      WaveStatus = "done"
	WaveFailed    WaveStatus = "failed"
	WaveCancelled WaveStatus = "cancelled"
)

// Wave trigger values (spec §3.1).
const (
	WaveTriggerAuto   = "auto"
	WaveTriggerManual = "manual"
	WaveTriggerRetest = "retest"
)

// Pause modes (spec §5.7).
const (
	PauseModeGraceful  = "graceful"
	PauseModeImmediate = "immediate"
)

// PauseReasonWaitingHuman marks a pause caused by an open human request; only
// this reason is auto-resumed after the request resolves (spec §5.7).
const PauseReasonWaitingHuman = "waiting_human"

// Valid reports whether status is one of the six persisted wave statuses.
func (s WaveStatus) Valid() bool {
	switch s {
	case WavePlanned, WaveRunning, WavePaused, WaveDone, WaveFailed, WaveCancelled:
		return true
	default:
		return false
	}
}

// Active reports whether the wave occupies the single active slot of a version.
// planned/running/paused are all active (P3-D-10).
func (s WaveStatus) Active() bool {
	switch s {
	case WavePlanned, WaveRunning, WavePaused:
		return true
	default:
		return false
	}
}

// Terminal reports whether the wave is finished.
func (s WaveStatus) Terminal() bool { return s.Valid() && !s.Active() }

// CanTransitionTo implements the §4.2 wave state machine.
func (s WaveStatus) CanTransitionTo(next WaveStatus) bool {
	switch s {
	case WavePlanned:
		return next == WaveRunning || next == WaveFailed || next == WaveCancelled
	case WaveRunning:
		return next == WavePaused || next == WaveDone || next == WaveFailed || next == WaveCancelled
	case WavePaused:
		return next == WaveRunning || next == WaveCancelled
	default:
		return false
	}
}

// ValidPauseMode reports whether mode is a supported pause mode. An empty mode
// is the graceful default.
func ValidPauseMode(mode string) bool {
	return mode == "" || mode == PauseModeGraceful || mode == PauseModeImmediate
}

// SUTCounts is the per-version-SUT projection of a wave (spec §5.6). The map
// key of ByVersionSUT is the decimal version_sut ID.
type SUTCounts struct {
	Total        int `json:"total"`
	Passed       int `json:"passed"`
	Failed       int `json:"failed"`
	Blocked      int `json:"blocked"`
	Inconclusive int `json:"inconclusive"`
}

// WaveCounts is the run_wave.counts snapshot. Total counts case_runs once;
// ByVersionSUT counts a cross-system case under each SUT it uses.
type WaveCounts struct {
	Total        int                  `json:"total"`
	Pending      int                  `json:"pending"`
	Running      int                  `json:"running"`
	Paused       int                  `json:"paused"`
	Passed       int                  `json:"passed"`
	Failed       int                  `json:"failed"`
	Blocked      int                  `json:"blocked"`
	Inconclusive int                  `json:"inconclusive"`
	Skipped      int                  `json:"skipped"`
	Interrupted  int                  `json:"interrupted"`
	Superseded   int                  `json:"superseded"`
	Evidence     int                  `json:"evidence"`
	ByVersionSUT map[string]SUTCounts `json:"by_version_sut"`
}

// Normalize returns counts with a non-nil ByVersionSUT map.
func (c WaveCounts) Normalize() WaveCounts {
	if c.ByVersionSUT == nil {
		c.ByVersionSUT = map[string]SUTCounts{}
	}
	return c
}

// RunWave is one execution wave for a version.
type RunWave struct {
	ID          int64
	VersionID   int64
	Status      WaveStatus
	Trigger     string
	Counts      WaveCounts
	StartedAt   *time.Time
	FinishedAt  *time.Time
	PausedAt    *time.Time
	PausedBy    int64
	PauseReason string
	PauseMode   string
	ReportID    *int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// PauseModeOrDefault returns the effective pause mode.
func (w RunWave) PauseModeOrDefault() string {
	if w.PauseMode == "" {
		return PauseModeGraceful
	}
	return w.PauseMode
}
