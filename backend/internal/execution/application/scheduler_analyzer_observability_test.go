package executionapp

// V-0022 / SC-F9: the scheduler's wave-analysis trigger must not silently drop
// an AnalyzeWave error (constitution art.7.6/7.7: no silent degradation,
// observability is a feature). The wave stays terminal — analysis failure never
// rolls a completed wave back — but the failure is logged with the wave/version
// attribution and the cause so an incomplete classification/coverage pass is
// diagnosable instead of invisible.

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

func TestAnalyzerErrorObservable(t *testing.T) {
	f := newTask6Fixture(t)
	f.analysis.err = errors.New("analysis store unavailable")
	logs := captureSchedulerSlog(t)

	versionID, waveID := finishTask6Wave(t, f)
	if err := f.scheduler.ReconcileVersion(context.Background(), versionID); err != nil {
		t.Fatalf("terminal ReconcileVersion: %v", err)
	}

	// Conservative handling: the wave stays done (analysis never rolls back a
	// terminal wave), but the analyzer failure is observable with attribution.
	wave, err := f.waves.Get(context.Background(), waveID)
	if err != nil || wave.Status != execution.WaveDone {
		t.Fatalf("wave = %+v err=%v, want done (analysis failure must not roll back the wave)", wave, err)
	}
	logged := logs.String()
	for _, want := range []string{
		"analysis",
		"wave_id=" + strconv.FormatInt(waveID, 10),
		"version_id=" + strconv.FormatInt(versionID, 10),
		"analysis store unavailable",
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("analyzer failure log is missing %q: %s", want, logged)
		}
	}
}
