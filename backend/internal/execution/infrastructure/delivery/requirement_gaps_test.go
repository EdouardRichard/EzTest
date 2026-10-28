package delivery

import (
	"context"
	"errors"
	"reflect"
	"testing"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	deliverydomain "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
)

// fakeRequirementGapWriter records the delivery call of the execution adapter.
type fakeRequirementGapWriter struct {
	calls   int
	err     error
	version int64
	batch   int64
	actor   int64
	gaps    []deliveryapp.RequirementGapEscalation
}

func (w *fakeRequirementGapWriter) ApplyRequirementGaps(_ context.Context, versionID, batchID, actor int64,
	escalations []deliveryapp.RequirementGapEscalation) ([]deliverydomain.Issue, error) {
	w.calls++
	w.version, w.batch, w.actor = versionID, batchID, actor
	w.gaps = escalations
	if w.err != nil {
		return nil, w.err
	}
	return []deliverydomain.Issue{{ID: 1}}, nil
}

// TestRequirementGapIssuesProjectsEscalations pins the adapter projection: the
// execution view maps one-to-one onto the delivery escalation payload and the
// issue count is returned.
func TestRequirementGapIssuesProjectsEscalations(t *testing.T) {
	writer := &fakeRequirementGapWriter{}
	port := NewRequirementGapIssues(writer)

	created, err := port.CreateRequirementGapIssues(context.Background(), 7, 41,
		[]executionapp.RequirementGapEscalation{{
			Title:          "gap",
			Detail:         "detail",
			RequirementIDs: []int64{21, 22},
		}}, 42)
	if err != nil || created != 1 {
		t.Fatalf("created=%d err=%v want 1,nil", created, err)
	}
	if writer.calls != 1 || writer.version != 7 || writer.batch != 41 || writer.actor != 42 {
		t.Fatalf("call=%d scope=%d/%d/%d want 1/7/41/42",
			writer.calls, writer.version, writer.batch, writer.actor)
	}
	want := []deliveryapp.RequirementGapEscalation{{Title: "gap", Detail: "detail", RequirementIDs: []int64{21, 22}}}
	if !reflect.DeepEqual(writer.gaps, want) {
		t.Fatalf("gaps=%+v want %+v", writer.gaps, want)
	}
}

// TestRequirementGapIssuesEmptyAndUnwired covers the no-op and the fail-closed
// adapter states.
func TestRequirementGapIssuesEmptyAndUnwired(t *testing.T) {
	writer := &fakeRequirementGapWriter{}
	port := NewRequirementGapIssues(writer)
	if created, err := port.CreateRequirementGapIssues(context.Background(), 7, 41, nil, 0); err != nil || created != 0 {
		t.Fatalf("empty escalation created=%d err=%v want 0,nil", created, err)
	}
	if writer.calls != 0 {
		t.Fatalf("empty escalation called delivery %d times", writer.calls)
	}

	unwired := NewRequirementGapIssues(nil)
	if _, err := unwired.CreateRequirementGapIssues(context.Background(), 7, 41,
		[]executionapp.RequirementGapEscalation{{Title: "gap", RequirementIDs: []int64{21}}}, 0); err == nil {
		t.Fatal("want an error for an unwired issue service")
	}
}

// TestRequirementGapIssuesPropagatesDeliveryError pins the worker-facing error:
// the adapter wraps the delivery failure so the worker logs and degrades.
func TestRequirementGapIssuesPropagatesDeliveryError(t *testing.T) {
	writer := &fakeRequirementGapWriter{err: errors.New("issue store unavailable")}
	port := NewRequirementGapIssues(writer)
	if _, err := port.CreateRequirementGapIssues(context.Background(), 7, 41,
		[]executionapp.RequirementGapEscalation{{Title: "gap", RequirementIDs: []int64{21}}}, 0); err == nil {
		t.Fatal("want the delivery error")
	}
}
