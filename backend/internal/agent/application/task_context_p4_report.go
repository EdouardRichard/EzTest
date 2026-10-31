package agentapp

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// ReportTaskContext is the payload-only P4 task-context provider for the
// report.wave/report.version work items (contracts/task-types.md §6). It derives
// its result from the TaskRef alone — the version from the work item, the
// report/revision/wave from the raw payload — so it needs no application
// services and can be registered from the composition root (Task 23) with
// RegisterP4TaskContext:
//
//	RegisterP4TaskContext(func(_ context.Context, ref TaskRef) (any, bool) {
//		return ReportTaskContext(ref)
//	})
//
// The report statistics/coverage summary is loaded by the scoped
// report.data_read tool during the run; this payload only identifies the task.
// A report task without a positive report_id/revision (or a wave task without a
// positive wave_id) is not claimed: the runner must not build a malformed pack.
func ReportTaskContext(ref TaskRef) (any, bool) {
	if ref.Type != WorkTypeReportWave && ref.Type != WorkTypeReportVersion {
		return nil, false
	}
	reportID, ok := taskRefPositiveID(ref.Payload["report_id"])
	if !ok {
		return nil, false
	}
	revision, ok := taskRefPositiveInt32(ref.Payload["revision"])
	if !ok {
		return nil, false
	}
	details := make(map[string]any, 4)
	if ref.VersionID > 0 {
		details["version_id"] = idText(ref.VersionID)
	}
	details["report_id"] = idText(reportID)
	details["revision"] = revision
	if ref.Type == WorkTypeReportWave {
		// wave_id is optional in the provider contract: the report row itself
		// remains the authority for its wave, and report.data_read resolves it.
		if waveID, ok := taskRefPositiveID(ref.Payload["wave_id"]); ok {
			details["wave_id"] = idText(waveID)
		}
	}
	return details, true
}

// taskRefPositiveID parses a payload ID that may arrive as a decimal string or
// a JSON number (the raw work-item payload is decoded before the provider runs).
func taskRefPositiveID(value any) (int64, bool) {
	switch typed := value.(type) {
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err != nil || parsed <= 0 {
			return 0, false
		}
		return parsed, true
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || typed != math.Trunc(typed) ||
			typed > math.MaxInt64 || typed < 1 {
			return 0, false
		}
		return int64(typed), true
	case int:
		if typed <= 0 {
			return 0, false
		}
		return int64(typed), true
	case int64:
		if typed <= 0 {
			return 0, false
		}
		return typed, true
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil || parsed <= 0 {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

// taskRefPositiveInt32 parses a revision/round payload value.
func taskRefPositiveInt32(value any) (int32, bool) {
	parsed, ok := taskRefPositiveID(value)
	if !ok || parsed > math.MaxInt32 {
		return 0, false
	}
	return int32(parsed), true
}
