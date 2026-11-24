package agentapp

import (
	"context"
	"reflect"
	"testing"
)

func TestDeliveryReportTaskContextWave(t *testing.T) {
	value, ok := ReportTaskContext(TaskRef{
		VersionID: 7,
		Type:      WorkTypeReportWave,
		Payload: map[string]any{
			"report_id": "910",
			"wave_id":   "12",
			"revision":  float64(2),
		},
	})
	if !ok {
		t.Fatal("ReportTaskContext did not claim a report.wave task")
	}
	want := map[string]any{
		"version_id": "7",
		"report_id":  "910",
		"wave_id":    "12",
		"revision":   int32(2),
	}
	if !reflect.DeepEqual(value, want) {
		t.Fatalf("ReportTaskContext(wave) = %#v, want %#v", value, want)
	}
}

func TestDeliveryReportTaskContextVersion(t *testing.T) {
	value, ok := ReportTaskContext(TaskRef{
		VersionID: 7,
		Type:      WorkTypeReportVersion,
		Payload: map[string]any{
			"report_id":  "920",
			"revision":   "3",
			"version_id": "7",
		},
	})
	if !ok {
		t.Fatal("ReportTaskContext did not claim a report.version task")
	}
	want := map[string]any{
		"version_id": "7",
		"report_id":  "920",
		"revision":   int32(3),
	}
	if !reflect.DeepEqual(value, want) {
		t.Fatalf("ReportTaskContext(version) = %#v, want %#v", value, want)
	}
}

func TestDeliveryReportTaskContextWaveIDIsOptional(t *testing.T) {
	value, ok := ReportTaskContext(TaskRef{
		VersionID: 7,
		Type:      WorkTypeReportWave,
		Payload:   map[string]any{"report_id": "910", "revision": "2"},
	})
	if !ok {
		t.Fatal("ReportTaskContext rejected a report.wave task without wave_id")
	}
	want := map[string]any{"version_id": "7", "report_id": "910", "revision": int32(2)}
	if !reflect.DeepEqual(value, want) {
		t.Fatalf("ReportTaskContext(no wave_id) = %#v, want %#v", value, want)
	}
}

func TestDeliveryReportTaskContextRequiresReference(t *testing.T) {
	cases := []TaskRef{
		{VersionID: 7, Type: WorkTypeReportWave, Payload: map[string]any{"revision": "2"}},
		{VersionID: 7, Type: WorkTypeReportWave, Payload: map[string]any{"report_id": "910"}},
		{VersionID: 7, Type: WorkTypeReportWave, Payload: map[string]any{"report_id": "0", "revision": "2"}},
		{VersionID: 7, Type: WorkTypeReportVersion, Payload: map[string]any{"report_id": "920", "revision": "0"}},
	}
	for i, ref := range cases {
		value, ok := ReportTaskContext(ref)
		if ok || value != nil {
			t.Fatalf("ReportTaskContext(missing #%d) = %#v, %v, want nil/false", i, value, ok)
		}
	}
}

func TestDeliveryReportTaskContextIgnoresOtherTypes(t *testing.T) {
	for _, workType := range []string{WorkTypeIssueTriage, WorkTypeExecuteCase, "unknown"} {
		value, ok := ReportTaskContext(TaskRef{
			VersionID: 7,
			Type:      workType,
			Payload:   map[string]any{"report_id": "910", "revision": "1"},
		})
		if ok || value != nil {
			t.Fatalf("ReportTaskContext claimed %q: %#v", workType, value)
		}
	}
}

func TestDeliveryReportTaskContextRegistersThroughPayloadOnlyHook(t *testing.T) {
	resetDeliveryTaskContextsForTest(t)
	RegisterDeliveryTaskContext(func(_ context.Context, ref TaskRef) (any, bool) {
		return ReportTaskContext(ref)
	})

	value, ok := DeliveryTaskContext(context.Background(), TaskRef{
		VersionID: 7,
		Type:      WorkTypeReportWave,
		Payload:   map[string]any{"report_id": "910", "wave_id": "12", "revision": "2"},
	})
	if !ok {
		t.Fatal("registered report task-context provider did not claim report.wave")
	}
	want := map[string]any{"version_id": "7", "report_id": "910", "wave_id": "12", "revision": int32(2)}
	if !reflect.DeepEqual(value, want) {
		t.Fatalf("registered report task context = %#v, want %#v", value, want)
	}
}
