// Package eventing adapts the delivery context's event ports (issue, report and
// analysis) to the P0 eventing use case (spec §8.1/§8.2). Every event is
// published on the fixed version channel with decimal string ids; detail carries
// ids/counts/status summaries only, never an issue body, report content, prompt
// or evidence payload. A nil publisher disables publication (the report/analysis
// services treat publication as best-effort).
package eventing

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	eventingdomain "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
)

// maxRefs is the frozen envelope's per-kind reference limit (spec §5.10).
const maxRefs = 20

// Publisher is the narrow slice of the eventing use case the adapter needs.
// *eventing/application.Service satisfies it.
type Publisher interface {
	Publish(ctx context.Context, event eventingdomain.NewEvent) (eventingdomain.Event, error)
}

// The P0 eventing use case must keep satisfying the adapter contract.
var _ Publisher = (*eventingapp.Service)(nil)

// IssuePublisher publishes delivery issue events through the P0 event bus.
type IssuePublisher struct {
	publisher Publisher
}

// NewIssuePublisher returns the issue event publisher. A nil publisher disables
// publishing.
func NewIssuePublisher(publisher Publisher) *IssuePublisher {
	return &IssuePublisher{publisher: publisher}
}

var _ deliveryapp.IssueEventPublisher = (*IssuePublisher)(nil)

// Publish implements delivery/application.IssueEventPublisher.
func (p *IssuePublisher) Publish(ctx context.Context, event deliveryapp.IssueEvent) error {
	if p == nil || p.publisher == nil {
		return nil
	}
	if event.VersionID <= 0 {
		return fmt.Errorf("delivery: issue event %q has no version id", event.Type)
	}
	detail, err := encodeDetail(event.Detail)
	if err != nil {
		return err
	}
	newEvent := eventingdomain.NewEvent{
		Channel:   versionChannel(event.VersionID),
		Type:      event.Type,
		VersionID: stringPtr(strconv.FormatInt(event.VersionID, 10)),
		Summary:   event.Summary,
		Detail:    detail,
		Refs: eventingdomain.Refs{
			Case:     formatIDs(event.RefCaseIDs),
			Evidence: formatIDs(event.RefEvidenceIDs),
		},
	}
	_, err = p.publisher.Publish(ctx, newEvent)
	return err
}

// ReportPublisher publishes delivery report events through the P0 event bus.
type ReportPublisher struct {
	publisher Publisher
}

// NewReportPublisher returns the report event publisher. A nil publisher
// disables publishing.
func NewReportPublisher(publisher Publisher) *ReportPublisher {
	return &ReportPublisher{publisher: publisher}
}

var _ deliveryapp.ReportEventPublisher = (*ReportPublisher)(nil)

// PublishReportEvent implements delivery/application.ReportEventPublisher.
func (p *ReportPublisher) PublishReportEvent(ctx context.Context, event deliveryapp.ReportEvent) error {
	if p == nil || p.publisher == nil {
		return nil
	}
	if event.VersionID <= 0 {
		return fmt.Errorf("delivery: report event %q has no version id", event.Type)
	}
	detail, err := encodeDetail(event.Detail)
	if err != nil {
		return err
	}
	newEvent := eventingdomain.NewEvent{
		Channel:   versionChannel(event.VersionID),
		Type:      event.Type,
		VersionID: stringPtr(strconv.FormatInt(event.VersionID, 10)),
		Summary:   event.Summary,
		Detail:    detail,
	}
	if event.BatchID > 0 {
		newEvent.BatchID = stringPtr(strconv.FormatInt(event.BatchID, 10))
	}
	_, err = p.publisher.Publish(ctx, newEvent)
	return err
}

// AnalysisPublisher publishes delivery analysis events through the P0 event bus.
type AnalysisPublisher struct {
	publisher Publisher
}

// NewAnalysisPublisher returns the analysis event publisher. A nil publisher
// disables publishing.
func NewAnalysisPublisher(publisher Publisher) *AnalysisPublisher {
	return &AnalysisPublisher{publisher: publisher}
}

var _ deliveryapp.AnalysisEventPublisher = (*AnalysisPublisher)(nil)

// Publish implements delivery/application.AnalysisEventPublisher.
func (p *AnalysisPublisher) Publish(ctx context.Context, event deliveryapp.AnalysisEvent) error {
	if p == nil || p.publisher == nil {
		return nil
	}
	if event.VersionID <= 0 {
		return fmt.Errorf("delivery: analysis event %q has no version id", event.Type)
	}
	detail, err := encodeDetail(event.Detail)
	if err != nil {
		return err
	}
	newEvent := eventingdomain.NewEvent{
		Channel:   versionChannel(event.VersionID),
		Type:      event.Type,
		VersionID: stringPtr(strconv.FormatInt(event.VersionID, 10)),
		Summary:   event.Summary,
		Detail:    detail,
	}
	if event.BatchID > 0 {
		newEvent.BatchID = stringPtr(strconv.FormatInt(event.BatchID, 10))
	}
	_, err = p.publisher.Publish(ctx, newEvent)
	return err
}

// versionChannel renders the frozen version channel name.
func versionChannel(versionID int64) string {
	return "version:" + strconv.FormatInt(versionID, 10)
}

// encodeDetail marshals the detail into the JSON object the envelope requires. A
// nil detail becomes the empty object, never JSON null.
func encodeDetail(detail map[string]any) (json.RawMessage, error) {
	if detail == nil {
		return json.RawMessage("{}"), nil
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		return nil, fmt.Errorf("delivery: encode event detail: %w", err)
	}
	return encoded, nil
}

// formatIDs stringifies ids in order, capped at the refs limit.
func formatIDs(ids []int64) []string {
	if len(ids) == 0 {
		return nil
	}
	if len(ids) > maxRefs {
		ids = ids[:maxRefs]
	}
	formatted := make([]string, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		formatted = append(formatted, strconv.FormatInt(id, 10))
	}
	return formatted
}

// stringPtr returns a pointer to value.
func stringPtr(value string) *string { return &value }
