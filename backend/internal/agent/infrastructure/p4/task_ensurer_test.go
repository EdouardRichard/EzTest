package p4

import (
	"context"
	"testing"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

type fakeQueue struct {
	items []agent.NewWorkItem
}

func (f *fakeQueue) Ensure(_ context.Context, in agent.NewWorkItem) (agent.WorkItem, bool, error) {
	f.items = append(f.items, in)
	return agent.WorkItem{ID: int64(len(f.items)), VersionID: in.VersionID, Type: in.Type}, true, nil
}

type fakeReports struct {
	reports map[int64]delivery.Report
}

func (f *fakeReports) GetReportByID(_ context.Context, reportID int64) (delivery.Report, error) {
	report, ok := f.reports[reportID]
	if !ok {
		return delivery.Report{}, delivery.ErrNotFound
	}
	return report, nil
}

type fakeRequirementVersions struct {
	versions map[int64]int64
}

func (f *fakeRequirementVersions) GetRequirementVersionID(_ context.Context, requirementID int64) (int64, error) {
	versionID, ok := f.versions[requirementID]
	if !ok {
		return 0, delivery.ErrNotFound
	}
	return versionID, nil
}

func testRequirementVersions() *fakeRequirementVersions {
	return &fakeRequirementVersions{versions: map[int64]int64{101: 7}}
}

func TestTaskEnsurerIssueTriage(t *testing.T) {
	queue := &fakeQueue{}
	ensurer := NewTaskEnsurer(queue, &fakeReports{}, testRequirementVersions())
	if err := ensurer.EnsureIssueTriage(context.Background(), 9001, 7); err != nil {
		t.Fatalf("EnsureIssueTriage: %v", err)
	}
	if len(queue.items) != 1 {
		t.Fatalf("work items = %+v", queue.items)
	}
	item := queue.items[0]
	if item.VersionID != 7 || item.Team != "delivery" || item.Type != "issue.triage" || item.DedupeKey != "triage:9001" {
		t.Fatalf("item = %+v", item)
	}
	payload, ok := item.Payload.(map[string]any)
	if !ok || payload["issue_id"] != "9001" {
		t.Fatalf("payload = %#v", item.Payload)
	}
}

func TestTaskEnsurerReportTasks(t *testing.T) {
	waveID := int64(11)
	queue := &fakeQueue{}
	reports := &fakeReports{reports: map[int64]delivery.Report{
		100: {ID: 100, VersionID: 7, WaveID: &waveID, Kind: delivery.ReportKindWave},
		200: {ID: 200, VersionID: 7, Kind: delivery.ReportKindVersion},
	}}
	ensurer := NewTaskEnsurer(queue, reports, testRequirementVersions())

	if err := ensurer.EnsureReportTask(context.Background(), 100, 2); err != nil {
		t.Fatalf("EnsureReportTask(wave): %v", err)
	}
	if err := ensurer.EnsureReportTask(context.Background(), 200, 3); err != nil {
		t.Fatalf("EnsureReportTask(version): %v", err)
	}
	if len(queue.items) != 2 {
		t.Fatalf("work items = %+v", queue.items)
	}
	wave := queue.items[0]
	if wave.VersionID != 7 || wave.Team != "delivery" || wave.Type != "report.wave" || wave.DedupeKey != "report:100:r2" {
		t.Fatalf("wave item = %+v", wave)
	}
	wavePayload := wave.Payload.(map[string]any)
	if wavePayload["report_id"] != "100" || wavePayload["wave_id"] != "11" || wavePayload["revision"] != int32(2) {
		t.Fatalf("wave payload = %#v", wavePayload)
	}
	version := queue.items[1]
	if version.Type != "report.version" || version.DedupeKey != "report:200:r3" {
		t.Fatalf("version item = %+v", version)
	}
	versionPayload := version.Payload.(map[string]any)
	if versionPayload["report_id"] != "200" || versionPayload["version_id"] != "7" || versionPayload["revision"] != int32(3) {
		t.Fatalf("version payload = %#v", versionPayload)
	}
}

func TestTaskEnsurerReportTaskMissingReport(t *testing.T) {
	ensurer := NewTaskEnsurer(&fakeQueue{}, &fakeReports{}, testRequirementVersions())
	if err := ensurer.EnsureReportTask(context.Background(), 999, 1); err == nil {
		t.Fatal("EnsureReportTask(missing report) = nil error")
	}
}

func TestTaskEnsurerStewardTasks(t *testing.T) {
	queue := &fakeQueue{}
	ensurer := NewTaskEnsurer(queue, &fakeReports{}, testRequirementVersions())
	if err := ensurer.EnsureRequirementClose(context.Background(), 101, 1001, 2); err != nil {
		t.Fatalf("EnsureRequirementClose: %v", err)
	}
	if err := ensurer.EnsureVersionClose(context.Background(), 7, 42, "f00d"); err != nil {
		t.Fatalf("EnsureVersionClose: %v", err)
	}
	if len(queue.items) != 2 {
		t.Fatalf("work items = %+v", queue.items)
	}
	requirement := queue.items[0]
	if requirement.VersionID != 7 || requirement.Team != "steward" || requirement.Type != "requirement.close" ||
		requirement.DedupeKey != "req-close:101:1001:2" || requirement.MaxAttempts != requirementCloseMaxAttempts {
		t.Fatalf("requirement item = %+v", requirement)
	}
	requirementPayload := requirement.Payload.(map[string]any)
	if requirementPayload["requirement_id"] != "101" || requirementPayload["requirement_version_id"] != "1001" ||
		requirementPayload["close_round"] != int32(2) {
		t.Fatalf("requirement payload = %#v", requirementPayload)
	}
	version := queue.items[1]
	if version.VersionID != 7 || version.Team != "steward" || version.Type != "version.close" ||
		version.DedupeKey != "ver-close:7:42:f00d" {
		t.Fatalf("version item = %+v", version)
	}
	versionPayload := version.Payload.(map[string]any)
	if versionPayload["version_id"] != "7" || versionPayload["report_revision_id"] != "42" ||
		versionPayload["predicate_fingerprint"] != "f00d" {
		t.Fatalf("version payload = %#v", versionPayload)
	}
}

func TestTaskEnsurerRejectsInvalidIDs(t *testing.T) {
	ensurer := NewTaskEnsurer(&fakeQueue{}, &fakeReports{}, testRequirementVersions())
	if err := ensurer.EnsureIssueTriage(context.Background(), 0, 7); err == nil {
		t.Fatal("EnsureIssueTriage(0) = nil error")
	}
	if err := ensurer.EnsureIssueTriage(context.Background(), 1, 0); err == nil {
		t.Fatal("EnsureIssueTriage(version 0) = nil error")
	}
	if err := ensurer.EnsureRequirementClose(context.Background(), 0, 1, 1); err == nil {
		t.Fatal("EnsureRequirementClose(0) = nil error")
	}
	if err := ensurer.EnsureVersionClose(context.Background(), 0, 1, "x"); err == nil {
		t.Fatal("EnsureVersionClose(0) = nil error")
	}
}
