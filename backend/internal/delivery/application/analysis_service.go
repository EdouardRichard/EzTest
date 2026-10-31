package deliveryapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// analysisKindTriage is the only analysis kind P4 writes (spec §3.4/§5.3).
const analysisKindTriage = "triage"

// analysisErrorMax is the delivery_analysis_run.error character bound.
const analysisErrorMax = 2000

// AnalysisService owns the delivery analysis_run lifecycle used by the issue
// triage task (spec §5.3). It resolves the issue's version through the issue
// service, creates the idempotent queued triage run, and records the terminal
// output/status. It never changes the issue status: triage output is a
// proposal written to analysis_run.output and the delivery team room.
type AnalysisService struct {
	repo   AnalysisRepository
	issues *IssueService
	clock  Clock
	poster TriagePoster
}

// TriagePoster publishes the delivery team-room finding of an accepted triage
// proposal. The delivery application owns the small projection type so it does
// not depend on the agent context; the composition root adapts the team-room
// service. A nil poster leaves the analysis output as the only record.
type TriagePoster interface {
	PostTriageFinding(ctx context.Context, finding TriageFinding) error
}

// TriageFinding is one triage team-room post. IDs are internal int64; the
// adapter formats them as decimal strings on the wire.
type TriageFinding struct {
	VersionID     int64
	IssueID       int64
	AnalysisRunID int64
	AgentRunID    int64
	Content       string
	Refs          []TriageFindingRef
}

// TriageFindingRef is one logical reference attached to a triage finding.
type TriageFindingRef struct {
	Kind  string
	RefID int64
}

// NewAnalysisService wires the analysis use cases. The issue service provides
// the triage target's version (IssueRepository.GetByID); the clock defaults to
// the system clock when nil.
func NewAnalysisService(repo AnalysisRepository, issues *IssueService, clock Clock) *AnalysisService {
	if clock == nil {
		clock = systemClock{}
	}
	return &AnalysisService{repo: repo, issues: issues, clock: clock}
}

// WithTriagePoster attaches the optional team-room projection. It returns the
// service so the composition root can chain it after construction.
func (s *AnalysisService) WithTriagePoster(poster TriagePoster) *AnalysisService {
	if s != nil {
		s.poster = poster
	}
	return s
}

// StartTriage loads the issue version through IssueRepository.GetByID and
// ensures the queued triage analysis run of that issue. The active partial
// unique key makes a repeat call return the same running/queued run; a finished
// run never blocks the next one (spec §5.3).
func (s *AnalysisService) StartTriage(ctx context.Context, issueID int64) (delivery.AnalysisRun, error) {
	if issueID <= 0 {
		return delivery.AnalysisRun{}, invalidArgumentf("issue id must be a positive decimal ID")
	}
	if s.repo == nil {
		return delivery.AnalysisRun{}, issueServiceNotConfigured("analysis repository")
	}
	if s.issues == nil || s.issues.repo == nil {
		return delivery.AnalysisRun{}, issueServiceNotConfigured("issue repository")
	}
	issue, err := s.issues.repo.GetByID(ctx, issueID)
	if err != nil {
		return delivery.AnalysisRun{}, mapIssueRepositoryError("load issue for analysis", err)
	}
	if issue.VersionID <= 0 {
		return delivery.AnalysisRun{}, invalidArgumentf("issue %d has no version", issueID)
	}
	refs, err := s.issues.repo.ListRefs(ctx, issueID)
	if err != nil {
		return delivery.AnalysisRun{}, mapIssueRepositoryError("load issue refs for analysis", err)
	}
	input, err := analysisTriageInput(issue, refs)
	if err != nil {
		return delivery.AnalysisRun{}, err
	}
	now := s.nowUTC()
	run := delivery.AnalysisRun{
		VersionID: issue.VersionID,
		Kind:      analysisKindTriage,
		Status:    delivery.AnalysisStatusQueued,
		DedupeKey: "triage:" + formatIssueID(issueID),
		InputRef:  input,
		CreatedAt: now,
		UpdatedAt: now,
	}
	stored, err := s.repo.CreateQueued(ctx, run)
	if err != nil {
		return delivery.AnalysisRun{}, mapAnalysisError("start triage analysis", err)
	}
	return stored, nil
}

// MarkRunning binds one agent run to a queued analysis run. The repository
// enforces the agent_run 1:1 index.
func (s *AnalysisService) MarkRunning(ctx context.Context, id int64, agentRunID int64) error {
	if s.repo == nil {
		return issueServiceNotConfigured("analysis repository")
	}
	return mapAnalysisError("mark analysis running", s.repo.MarkRunning(ctx, id, agentRunID))
}

// Complete records the terminal status/output/error of one analysis run. The
// output must be a JSON object; the error message is bounded to 2000
// characters. An already terminal run is a state conflict.
func (s *AnalysisService) Complete(ctx context.Context, id int64, status delivery.AnalysisStatus, output json.RawMessage, errMsg string) error {
	if s.repo == nil {
		return issueServiceNotConfigured("analysis repository")
	}
	if id <= 0 || !analysisStatusTerminal(status) {
		return invalidArgumentf("analysis status must be done, failed or degraded")
	}
	if !analysisOutputObject(output) {
		return invalidArgumentf("analysis output must be a JSON object")
	}
	if len([]rune(errMsg)) > analysisErrorMax {
		return invalidArgumentf("analysis error must be at most %d characters", analysisErrorMax)
	}
	return mapAnalysisError("complete analysis", s.repo.Complete(ctx, id, status, output, errMsg))
}

// PostTriageFinding projects the triage proposal into the delivery team room.
// The projection is best effort: the analysis run already holds the committed
// output, so a team-room failure never rolls it back (matching the event/audit
// projection convention).
func (s *AnalysisService) PostTriageFinding(ctx context.Context, finding TriageFinding) {
	if s == nil || s.poster == nil {
		return
	}
	_ = s.poster.PostTriageFinding(ctx, finding)
}

// analysisTriageInput renders the analysis_run.input_ref of spec §5.3:
// {issue_id, version_id, refs}. IDs stay decimal strings (D29).
func analysisTriageInput(issue delivery.Issue, refs []delivery.IssueRef) (json.RawMessage, error) {
	refPayload := make([]map[string]any, 0, len(refs))
	for _, ref := range refs {
		entry := map[string]any{
			"kind":   ref.Kind,
			"ref_id": formatIssueID(ref.RefID),
		}
		if ref.Role != "" {
			entry["role"] = ref.Role
		}
		if ref.VersionSUTID != nil && *ref.VersionSUTID > 0 {
			entry["version_sut_id"] = formatIssueID(*ref.VersionSUTID)
		}
		refPayload = append(refPayload, entry)
	}
	input, err := json.Marshal(map[string]any{
		"issue_id":   formatIssueID(issue.ID),
		"version_id": formatIssueID(issue.VersionID),
		"refs":       refPayload,
	})
	if err != nil {
		return nil, fmt.Errorf("encode analysis input: %w", err)
	}
	return input, nil
}

func (s *AnalysisService) nowUTC() time.Time {
	if s.clock == nil {
		return time.Now().UTC()
	}
	return s.clock.Now().UTC()
}

func analysisStatusTerminal(status delivery.AnalysisStatus) bool {
	switch status {
	case delivery.AnalysisStatusDone, delivery.AnalysisStatusFailed, delivery.AnalysisStatusDegraded:
		return true
	default:
		return false
	}
}

func analysisOutputObject(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	if !json.Valid(raw) {
		return false
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && object != nil
}

// mapAnalysisError adds operation context to unexpected repository failures and
// passes the delivery sentinels through unchanged.
func mapAnalysisError(op string, err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, delivery.ErrInvalidArgument),
		errors.Is(err, delivery.ErrStateConflict),
		errors.Is(err, delivery.ErrNotFound):
		return err
	default:
		return fmt.Errorf("%s: %w", op, err)
	}
}
