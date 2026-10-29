// Package eventing adapts the execution context's EventPublisher port to the P0
// eventing use case (spec §5.10). Every execution event is published on the
// fixed channel version:<version id> with decimal string ids, and every id ref is
// mapped onto the frozen envelope's refs object (evidence/case/requirement/code).
// Detail carries ids, counts and status summaries only; this adapter copies and
// encodes the caller's detail but never adds case bodies, prompts or secrets.
package eventing

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	eventingdomain "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
)

// maxRefs is the frozen envelope's per-kind reference limit (spec §5.10).
const maxRefs = 20

// Publisher is the narrow slice of the eventing use case the adapter needs.
// *eventing/application.Service satisfies it.
type Publisher interface {
	Publish(ctx context.Context, event eventingdomain.NewEvent) (eventingdomain.Event, error)
}

// Adapter publishes execution events through the P0 event bus.
type Adapter struct {
	publisher Publisher
}

// New returns the adapter. A nil publisher disables publishing (used by
// partially wired processes and tests), matching the other P6 publishers.
func New(publisher Publisher) *Adapter {
	return &Adapter{publisher: publisher}
}

var _ executionapp.EventPublisher = (*Adapter)(nil)

// The P0 eventing use case must keep satisfying the adapter contract.
var _ Publisher = (*eventingapp.Service)(nil)

// Publish maps one execution event onto the frozen V1 envelope and publishes it.
func (a *Adapter) Publish(ctx context.Context, event executionapp.Event) error {
	if a == nil || a.publisher == nil {
		return nil
	}
	if event.VersionID <= 0 {
		return fmt.Errorf("execution: event %q has no version id", event.Type)
	}
	detail, err := encodeDetail(event.Detail)
	if err != nil {
		return err
	}
	versionID := strconv.FormatInt(event.VersionID, 10)
	newEvent := eventingdomain.NewEvent{
		Channel:   "version:" + versionID,
		Type:      event.Type,
		VersionID: &versionID,
		Summary:   event.Summary,
		Detail:    detail,
		Refs:      mapRefs(event.Refs),
	}
	if event.BatchID > 0 {
		batchID := strconv.FormatInt(event.BatchID, 10)
		newEvent.BatchID = &batchID
	}
	if event.CaseID > 0 {
		caseID := strconv.FormatInt(event.CaseID, 10)
		newEvent.CaseID = &caseID
	}
	if event.StepID > 0 {
		stepID := strconv.FormatInt(event.StepID, 10)
		newEvent.StepID = &stepID
	}
	if event.AgentRole != "" {
		agentRole := event.AgentRole
		newEvent.AgentRole = &agentRole
	}
	if event.SessionID != "" {
		sessionID := event.SessionID
		newEvent.SessionID = &sessionID
	}
	_, err = a.publisher.Publish(ctx, newEvent)
	return err
}

// encodeDetail marshals the detail into the JSON object the envelope requires. A
// nil detail becomes the empty object, never JSON null.
func encodeDetail(detail map[string]any) (json.RawMessage, error) {
	if detail == nil {
		return json.RawMessage("{}"), nil
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		return nil, fmt.Errorf("execution: encode event detail: %w", err)
	}
	return encoded, nil
}

// mapRefs maps the execution id refs onto the frozen envelope's string refs,
// capping each kind at the envelope limit.
func mapRefs(refs executionapp.EventRefs) eventingdomain.Refs {
	return eventingdomain.Refs{
		Evidence:    formatIDs(refs.EvidenceIDs),
		Case:        formatIDs(refs.CaseIDs),
		Requirement: formatIDs(refs.RequirementIDs),
		Code:        capStrings(refs.Code),
	}
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
		formatted = append(formatted, strconv.FormatInt(id, 10))
	}
	return formatted
}

// capStrings copies code refs in order, capped at the refs limit.
func capStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	if len(values) > maxRefs {
		values = values[:maxRefs]
	}
	return append([]string(nil), values...)
}
