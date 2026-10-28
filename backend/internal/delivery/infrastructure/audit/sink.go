// Package audit adapts the delivery context's audit ports (issue, report and
// analysis) to the P0 audit recorder (spec §11.2). Entries carry ids, status,
// counts and durations only: never an issue body, report content, prompt or
// evidence payload. P0 request metadata is stamped when the call happens inside
// an HTTP request; an asynchronous worker falls back to the request id copied
// onto its context.
package audit

import (
	"context"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	auditdomain "github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// Recorder is the narrow slice of the audit application recorder the sinks
// need. *audit/application.Service satisfies it.
type Recorder interface {
	Record(ctx context.Context, entry auditdomain.Entry) error
}

// The P0 audit recorder must keep satisfying the adapter contract.
var _ Recorder = (*auditapp.Service)(nil)

// IssueSink records delivery issue audit entries (delivery.issue.*).
type IssueSink struct {
	recorder Recorder
}

// NewIssueSink returns the issue audit sink. A nil recorder disables auditing.
func NewIssueSink(recorder Recorder) *IssueSink {
	return &IssueSink{recorder: recorder}
}

var _ deliveryapp.IssueAuditRecorder = (*IssueSink)(nil)

// Record implements delivery/application.IssueAuditRecorder.
func (s *IssueSink) Record(ctx context.Context, event deliveryapp.IssueAuditEvent) error {
	if s == nil || s.recorder == nil {
		return nil
	}
	return record(ctx, s.recorder, event.ActorID, event.Action, event.TargetType, event.TargetID, event.Metadata)
}

// ReportSink records delivery report audit entries (delivery.report.*).
type ReportSink struct {
	recorder Recorder
}

// NewReportSink returns the report audit sink. A nil recorder disables
// auditing.
func NewReportSink(recorder Recorder) *ReportSink {
	return &ReportSink{recorder: recorder}
}

var _ deliveryapp.ReportAuditRecorder = (*ReportSink)(nil)

// RecordReportAudit implements delivery/application.ReportAuditRecorder.
func (s *ReportSink) RecordReportAudit(ctx context.Context, event deliveryapp.ReportAuditEvent) error {
	if s == nil || s.recorder == nil {
		return nil
	}
	return record(ctx, s.recorder, event.ActorID, event.Action, event.TargetType, event.TargetID, event.Metadata)
}

// AnalysisSink records delivery analysis audit entries (delivery.analysis.*).
type AnalysisSink struct {
	recorder Recorder
}

// NewAnalysisSink returns the analysis audit sink. A nil recorder disables
// auditing.
func NewAnalysisSink(recorder Recorder) *AnalysisSink {
	return &AnalysisSink{recorder: recorder}
}

var _ deliveryapp.AnalysisAuditRecorder = (*AnalysisSink)(nil)

// Record implements delivery/application.AnalysisAuditRecorder.
func (s *AnalysisSink) Record(ctx context.Context, event deliveryapp.AnalysisAuditEvent) error {
	if s == nil || s.recorder == nil {
		return nil
	}
	return record(ctx, s.recorder, event.ActorID, event.Action, event.TargetType, event.TargetID, event.Metadata)
}

// record appends one audit entry with the request correlation of the P0 log.
func record(ctx context.Context, recorder Recorder, actor int64, action, targetType, targetID string, metadata map[string]any) error {
	entry := auditdomain.Entry{
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		Metadata:   metadata,
	}
	if actor > 0 {
		actorID := actor
		entry.ActorUserID = &actorID
	} else if contextActor, ok := auditapp.ActorIDFromContext(ctx); ok && contextActor > 0 {
		actorID := contextActor
		entry.ActorUserID = &actorID
	}
	if meta := auditapp.MetaFromContext(ctx); meta != (auditapp.RequestMeta{}) {
		entry.RequestID = meta.RequestID
		entry.IP = meta.IP
		entry.UserAgent = meta.UserAgent
	}
	if entry.RequestID == "" {
		entry.RequestID = platform.RequestID(ctx)
	}
	return recorder.Record(ctx, entry)
}
