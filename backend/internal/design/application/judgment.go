package designapp

import (
	"context"
	"errors"
	"strconv"
	"strings"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// maxReworkRoundsKey is the settings-catalog key that bounds judge-fail rework
// rounds per logical case (design v1.3.1 P14, spec SC-E4; registered by T1).
const maxReworkRoundsKey = "design.max_rework_rounds"

// MaxReworkRoundsKey exports the registered rework-bound settings key so the
// composition-root judge adapter (T13) reads the SAME bound when it derives the
// server-supplied judgment round, without duplicating the key string.
const MaxReworkRoundsKey = maxReworkRoundsKey

// Judgment payload bounds. Compile-time aliases of the DOMAIN authority
// (design.MaxJudgmentFindings / JudgmentDimensionMax / JudgmentMessageMax /
// OverrideReasonMax, where the Judgment value object lives): the application
// layer keeps its existing exported names without holding a second copy of the
// values, and the agent's judgment tool aliases the same domain constants
// (H-F10b #27, constitution V.1/V.4). Same shape as the Override conditions in
// judgment_ports.go.
const (
	// MaxJudgmentFindings bounds one submitted judgment's finding list.
	MaxJudgmentFindings = design.MaxJudgmentFindings
	// JudgmentDimensionMax bounds a finding dimension label.
	JudgmentDimensionMax = design.JudgmentDimensionMax
	// JudgmentMessageMax bounds one finding message.
	JudgmentMessageMax = design.JudgmentMessageMax
	// OverrideReasonMax bounds the human override reason.
	OverrideReasonMax = design.OverrideReasonMax
)

// SubmitJudgmentInput is one judge verdict candidate for one case version.
// The model never supplies the content hash: the server reads the current
// case version and binds its hash at settlement time (constitution III).
type SubmitJudgmentInput struct {
	CaseID        int64
	CaseVersionID int64
	// Round is supplied by the server-side scheduling path (T8 rework round).
	// 0 derives conservatively: round 1 for a first judgment, otherwise the
	// latest judged round (a host-edit rejudge never consumes rework budget).
	// An increment is only accepted after a fail verdict within budget.
	Round            int
	Verdict          string
	Findings         []design.JudgmentFinding
	ACCoverageMatrix map[string]any
}

// SubmitJudgmentResult reports the settled fact binding.
type SubmitJudgmentResult struct {
	JudgmentID    int64
	CaseID        int64
	CaseVersionID int64
	ContentHash   string
	Round         int
	Verdict       string
}

// JudgmentService settles judge verdict candidates into append-only facts,
// records human overrides and derives the review-state projection. It never
// confirms a case: confirmation stays with the single Confirm entry (T7).
type JudgmentService struct {
	repo     JudgmentRepository
	cases    JudgmentCaseReader
	ids      platformid.Generator
	clock    clock.Clock
	events   eventingapp.Publisher
	settings SettingsReader
	// graphs enumerates the version's current cases for the read report
	// (ListJudgments, T16). It is a setter because the composition root builds
	// the judgment service before it has the design graph reader in hand; an
	// unwired reader fails ListJudgments closed instead of silently answering
	// "no cases".
	graphs ReviewGraphReader
	// reworkBudget is the OPTIONAL escalation-budget read (terminal
	// design.rework work items, spec SC-E4). It is a setter for the same reason
	// as graphs: the composition root builds the judgment service before the
	// agent work-item adapter. Nil falls back to the round-derived CountReworks
	// alone (a degraded assembly has no terminal reworks).
	reworkBudget ReworkBudgetReader
}

// NewJudgmentService wires the judgment service. events is a best-effort sink
// and may be nil in unit tests; an unwired settings reader fails closed at
// read time instead of silently assuming a budget.
func NewJudgmentService(
	repo JudgmentRepository,
	cases JudgmentCaseReader,
	ids platformid.Generator,
	clk clock.Clock,
	events eventingapp.Publisher,
	settings SettingsReader,
) *JudgmentService {
	if clk == nil {
		clk = clock.System{}
	}
	return &JudgmentService{repo: repo, cases: cases, ids: ids, clock: clk, events: events, settings: settings}
}

// SubmitJudgment validates one judge candidate and appends the fact. The
// verdict schema, finding structure, case/requirement membership and the
// current-version binding are all server checks; the model only proposes.
func (s *JudgmentService) SubmitJudgment(ctx context.Context, versionID, requirementID, runID int64, in SubmitJudgmentInput) (SubmitJudgmentResult, error) {
	if err := requirePositiveID("version_id", versionID); err != nil {
		return SubmitJudgmentResult{}, err
	}
	if err := requirePositiveID("requirement_id", requirementID); err != nil {
		return SubmitJudgmentResult{}, err
	}
	if err := requirePositiveID("case_id", in.CaseID); err != nil {
		return SubmitJudgmentResult{}, err
	}
	if err := requirePositiveID("case_version_id", in.CaseVersionID); err != nil {
		return SubmitJudgmentResult{}, err
	}
	if !design.ValidVerdict(in.Verdict) {
		return SubmitJudgmentResult{}, platform.Invalid("verdict", "must be one of pass, fail")
	}
	if len(in.Findings) > MaxJudgmentFindings {
		return SubmitJudgmentResult{}, platform.Invalid("findings", "at most 50 findings are allowed")
	}
	for index, finding := range in.Findings {
		field := "findings[" + strconv.Itoa(index) + "]"
		if err := validation.String(field+".dimension", strings.TrimSpace(finding.Dimension), 1, JudgmentDimensionMax); err != nil {
			return SubmitJudgmentResult{}, err
		}
		if !design.ValidSeverity(finding.Severity) {
			return SubmitJudgmentResult{}, platform.Invalid(field+".severity", "must be one of blocker, important, minor")
		}
		if err := validation.String(field+".message", strings.TrimSpace(finding.Message), 1, JudgmentMessageMax); err != nil {
			return SubmitJudgmentResult{}, err
		}
		// Rubric consistency (spec SC-E3): blocker/important findings mean
		// fail; a pass verdict carrying them is a contradictory candidate.
		if in.Verdict == design.VerdictPass && finding.Severity != design.SeverityMinor {
			return SubmitJudgmentResult{}, platform.Invalid("verdict", "a pass verdict cannot carry blocker or important findings")
		}
	}

	data, err := s.cases.ReadCase(ctx, versionID, in.CaseID, nil)
	if err != nil {
		return SubmitJudgmentResult{}, mapDesignError(err, "case")
	}
	if data.Requirement.ID != requirementID {
		return SubmitJudgmentResult{}, platform.Invalid("case_id", "case does not belong to the requirement")
	}
	current := data.Selected
	if current.ID == 0 {
		return SubmitJudgmentResult{}, platform.StateConflict("the case has no current version to judge")
	}
	if in.CaseVersionID != current.ID {
		return SubmitJudgmentResult{}, platform.StateConflict("case_version_id is not the current version; re-read the design graph before judging")
	}
	if err := s.validateFindingCaseRefs(ctx, versionID, requirementID, in); err != nil {
		return SubmitJudgmentResult{}, err
	}
	round, err := s.resolveRound(ctx, in)
	if err != nil {
		return SubmitJudgmentResult{}, err
	}

	judgment := design.Judgment{
		ID:               s.ids.Next(),
		VersionID:        versionID,
		RequirementID:    requirementID,
		CaseID:           in.CaseID,
		CaseVersionID:    current.ID,
		ContentHash:      current.ContentHash,
		Round:            round,
		Verdict:          in.Verdict,
		Findings:         normalizeFindings(in.Findings),
		ACCoverageMatrix: in.ACCoverageMatrix,
		AgentRunID:       runID,
		CreatedAt:        s.clock.Now().UTC(),
	}
	if err := s.repo.InsertJudgment(ctx, judgment); err != nil {
		return SubmitJudgmentResult{}, mapDesignError(err, "judgment")
	}
	s.publishEvent(ctx, versionID, "design.judgment_submitted", requirementID, map[string]any{
		"requirement_id":  platformid.Format(requirementID),
		"judgment_id":     platformid.Format(judgment.ID),
		"case_id":         platformid.Format(judgment.CaseID),
		"case_version_id": platformid.Format(judgment.CaseVersionID),
		"round":           judgment.Round,
		"verdict":         judgment.Verdict,
	}, 0)
	return SubmitJudgmentResult{
		JudgmentID:    judgment.ID,
		CaseID:        judgment.CaseID,
		CaseVersionID: judgment.CaseVersionID,
		ContentHash:   judgment.ContentHash,
		Round:         judgment.Round,
		Verdict:       judgment.Verdict,
	}, nil
}

// Override records one human decision on an escalated case version. It is
// allowed only when (1) the latest fact of the CURRENT version is fail with
// the rework budget exhausted, or (2) a judge_exhausted escalation fact exists
// for the current version. The override binds the current version content; a
// later revision invalidates it (spec SC-E5). Judgment facts are never edited.
func (s *JudgmentService) Override(ctx context.Context, versionID, caseID int64, actorID int64, reason string) (design.Override, error) {
	if err := requirePositiveID("version_id", versionID); err != nil {
		return design.Override{}, err
	}
	if err := requirePositiveID("case_id", caseID); err != nil {
		return design.Override{}, err
	}
	if err := requirePositiveID("actor_id", actorID); err != nil {
		return design.Override{}, err
	}
	reason = strings.TrimSpace(reason)
	if err := validation.String("reason", reason, 1, OverrideReasonMax); err != nil {
		return design.Override{}, err
	}
	data, err := s.cases.ReadCase(ctx, versionID, caseID, nil)
	if err != nil {
		return design.Override{}, mapDesignError(err, "case")
	}
	current := data.Selected
	if current.ID == 0 {
		return design.Override{}, platform.StateConflict("the case has no current version to override")
	}
	latest, err := s.repo.LatestJudgment(ctx, caseID)
	if err != nil {
		return design.Override{}, mapDesignError(err, "judgment")
	}
	condition := ""
	var judgmentID *int64
	if design.JudgmentBoundTo(latest, current.ID) && latest.Verdict == design.VerdictFail {
		exhausted, err := s.reworkExhausted(ctx, versionID, caseID, current.ID)
		if err != nil {
			return design.Override{}, err
		}
		if exhausted {
			condition = OverrideConditionFailExhausted
			id := latest.ID
			judgmentID = &id
		}
	}
	if condition == "" {
		unavailable, err := s.repo.HasJudgeExhausted(ctx, caseID, current.ID)
		if err != nil {
			return design.Override{}, mapDesignError(err, "judge escalation fact")
		}
		if unavailable {
			condition = OverrideConditionJudgeExhausted
		}
	}
	if condition == "" {
		return design.Override{}, platform.StateConflict("override requires an exhausted fail verdict or a judge-unavailable escalation for the current case version")
	}

	override := design.Override{
		ID:            s.ids.Next(),
		CaseID:        caseID,
		CaseVersionID: current.ID,
		ContentHash:   current.ContentHash,
		JudgmentID:    judgmentID,
		Condition:     condition,
		ActorID:       actorID,
		Reason:        reason,
		CreatedAt:     s.clock.Now().UTC(),
	}
	if err := s.repo.InsertOverride(ctx, override); err != nil {
		return design.Override{}, mapDesignError(err, "judgment override")
	}
	payload := map[string]any{
		"case_id":         platformid.Format(caseID),
		"case_version_id": platformid.Format(current.ID),
		"override_id":     platformid.Format(override.ID),
		"condition":       condition,
	}
	if judgmentID != nil {
		payload["judgment_id"] = platformid.Format(*judgmentID)
	}
	s.publishEvent(ctx, versionID, "design.judgment_overridden", data.Requirement.ID, payload, actorID)
	return override, nil
}

// ReviewStateFor derives the review state of one case version from facts
// (instant projection, constitution V.11; single authority DeriveReviewState).
func (s *JudgmentService) ReviewStateFor(ctx context.Context, versionID, caseID, currentVersionID int64) (design.ReviewState, error) {
	if err := requirePositiveID("version_id", versionID); err != nil {
		return "", err
	}
	if err := requirePositiveID("case_id", caseID); err != nil {
		return "", err
	}
	if err := requirePositiveID("current_version_id", currentVersionID); err != nil {
		return "", err
	}
	latest, err := s.repo.LatestJudgment(ctx, caseID)
	if err != nil {
		return "", mapDesignError(err, "judgment")
	}
	return s.reviewStateFromLatest(ctx, versionID, caseID, currentVersionID, latest)
}

// reviewStateFromLatest derives the review state from an ALREADY READ latest
// fact. ReviewStateFor and ListJudgments share it so the report does not read
// the same case's latest judgment twice (H-F10 T16 N+1): one read per case, one
// derivation, one truth.
func (s *JudgmentService) reviewStateFromLatest(ctx context.Context, versionID, caseID, currentVersionID int64, latest *design.Judgment) (design.ReviewState, error) {
	exhausted, err := s.repo.HasJudgeExhausted(ctx, caseID, currentVersionID)
	if err != nil {
		return "", mapDesignError(err, "judge escalation fact")
	}
	reworkExhausted, err := s.reworkExhausted(ctx, versionID, caseID, currentVersionID)
	if err != nil {
		return "", err
	}
	return design.DeriveReviewState(latest, exhausted, currentVersionID, reworkExhausted), nil
}

// SetGraphReader wires the design graph read used by ListJudgments to
// enumerate the version's current cases (T16). It returns the service for
// fluent wiring, mirroring GraphService.SetJudgmentGate.
func (s *JudgmentService) SetGraphReader(graphs ReviewGraphReader) *JudgmentService {
	if s != nil {
		s.graphs = graphs
	}
	return s
}

// SetReworkBudgetReader wires the optional escalation-budget read (terminal
// design.rework work items of the logical case). It returns the service for
// fluent wiring, mirroring SetGraphReader; nil keeps the round-derived
// CountReworks as the only budget source.
func (s *JudgmentService) SetReworkBudgetReader(r ReworkBudgetReader) *JudgmentService {
	if s != nil {
		s.reworkBudget = r
	}
	return s
}

// ListJudgments builds the read-only judgment report (T16, spec SC-E5): one
// entry per current case of the version, or of one requirement when
// requirementID is non-nil. Every entry carries the SAME ReviewStateFor
// projection Confirm and the shared review gate consume, and the latest judge
// fact ONLY when it is bound to the case's current version -- a stale verdict
// (bound to a superseded revision) never masquerades as the current review.
// Cases with no matching fact still appear (review_state pending, no
// judgment) so the report doubles as the case_review/designing review-state
// view, not merely a list of judged cases.
func (s *JudgmentService) ListJudgments(ctx context.Context, versionID int64, requirementID *int64) (JudgmentReportView, error) {
	if err := requirePositiveID("version_id", versionID); err != nil {
		return JudgmentReportView{}, err
	}
	if requirementID != nil {
		if err := requirePositiveID("requirement_id", *requirementID); err != nil {
			return JudgmentReportView{}, err
		}
	}
	if s.graphs == nil {
		return JudgmentReportView{}, platform.Unavailable("the design graph reader is not wired for the judgment service; the judgment report cannot be assembled")
	}
	graph, err := s.graphs.ReadGraph(ctx, versionID)
	if err != nil {
		return JudgmentReportView{}, mapDesignError(err, "version")
	}
	view := JudgmentReportView{Cases: make([]JudgmentCaseJudgmentView, 0)}
	for _, requirement := range graph.Requirements {
		if requirementID != nil && requirement.Requirement.ID != *requirementID {
			continue
		}
		for _, scenario := range requirement.Scenarios {
			for _, item := range scenario.Cases {
				currentVersionID := derefInt64(item.Case.CurrentVersionID)
				if currentVersionID <= 0 {
					continue
				}
				// ONE latest-fact read per case feeds both the review-state
				// derivation and the report entry (H-F10 T16: the previous
				// ReviewStateFor + LatestJudgment pair read it twice).
				latest, err := s.repo.LatestJudgment(ctx, item.Case.ID)
				if err != nil {
					return JudgmentReportView{}, mapDesignError(err, "judgment")
				}
				state, err := s.reviewStateFromLatest(ctx, versionID, item.Case.ID, currentVersionID, latest)
				if err != nil {
					return JudgmentReportView{}, err
				}
				entry := JudgmentCaseJudgmentView{
					CaseID: item.Case.ID, CurrentVersionID: currentVersionID, ReviewState: state,
				}
				if design.JudgmentBoundTo(latest, currentVersionID) {
					entry.Judgment = latest
				}
				view.Cases = append(view.Cases, entry)
			}
		}
	}
	return view, nil
}

// validateFindingCaseRefs checks every non-empty finding case_ref against the
// current requirement: the submitted case itself, or a sibling case of the
// same requirement. Refs are deduplicated so one read serves repeated refs.
func (s *JudgmentService) validateFindingCaseRefs(ctx context.Context, versionID, requirementID int64, in SubmitJudgmentInput) error {
	checked := map[int64]bool{in.CaseID: true}
	for index, finding := range in.Findings {
		ref := strings.TrimSpace(finding.CaseRef)
		if ref == "" {
			continue // requirement-level finding
		}
		field := "findings[" + strconv.Itoa(index) + "].case_ref"
		refID, err := strconv.ParseInt(ref, 10, 64)
		if err != nil || refID <= 0 {
			return platform.Invalid(field, "must be a positive decimal case ID of the current requirement")
		}
		if checked[refID] {
			continue
		}
		data, err := s.cases.ReadCase(ctx, versionID, refID, nil)
		if err != nil {
			// A missing ref is a field error; any other read failure is an
			// infrastructure error and must not be misattributed to the model.
			if errors.Is(err, design.ErrNotFound) {
				return platform.Invalid(field, "must reference a case of the current requirement")
			}
			return mapDesignError(err, "case")
		}
		if data.Requirement.ID != requirementID {
			return platform.Invalid(field, "must reference a case of the current requirement")
		}
		checked[refID] = true
	}
	return nil
}

// resolveRound validates the round against the case's judgment facts. Rounds
// are monotonic, advance at most one at a time, and an increment requires a
// preceding fail verdict within the rework budget (round increments ONLY on
// rework). This is ROUND PROGRESSION, so the budget check uses CountReworks
// (max(round)-1) alone: the rework being re-judged is already terminal here, and
// counting it would reject the legitimate next round. The escalation budget is
// the separate max(CountReworks, TerminalReworkCount) read in reworkExhausted.
func (s *JudgmentService) resolveRound(ctx context.Context, in SubmitJudgmentInput) (int, error) {
	latest, err := s.repo.LatestJudgment(ctx, in.CaseID)
	if err != nil {
		return 0, mapDesignError(err, "judgment")
	}
	maxRound := 0
	if latest != nil {
		maxRound = latest.Round
	}
	round := in.Round
	if round == 0 {
		round = maxRound
		if round == 0 {
			round = 1
		}
	}
	if round < 1 {
		return 0, platform.Invalid("round", "must be at least 1")
	}
	if round < maxRound {
		return 0, platform.Invalid("round", "must not go below the latest judged round")
	}
	if round > maxRound+1 {
		return 0, platform.Invalid("round", "must advance at most one round per judgment")
	}
	if round == maxRound+1 && maxRound > 0 {
		if latest.Verdict != design.VerdictFail {
			return 0, platform.Invalid("round", "a new round requires a preceding fail verdict")
		}
		count, err := s.repo.CountReworks(ctx, in.CaseID)
		if err != nil {
			return 0, mapDesignError(err, "judgment")
		}
		maxRounds, err := s.maxReworkRounds(ctx)
		if err != nil {
			return 0, err
		}
		if !design.ReworkAllowed(count, maxRounds) {
			return 0, platform.Invalid("round", "the rework budget of this case is exhausted")
		}
	}
	return round, nil
}

// reworkExhausted reports whether the case's ESCALATION rework budget is spent,
// i.e. whether no further rework can move this failed version forward. Two
// facts, both server-derived:
//
//  1. budget: max(round-derived CountReworks, terminal design.rework work items)
//     against design.max_rework_rounds. Spec SC-E4 derives the budget from
//     terminal rework facts; a rework that died consumes budget without producing
//     a new judgment round, so the round-derived counter alone would never see it.
//     Every round increment implies a succeeded (terminal) rework, so the terminal
//     count is never behind in a reachable state; max() is defensive.
//  2. dead end: the rework responding to THIS failed version is TERMINAL (done,
//     dead, or failed at its attempt cap). The rework request key is
//     rework:<case>:<failedVersion> with no generation and the work-item store
//     never revives a terminal row, so no further rework can ever respond to this
//     fail — with budget left the budget test alone would allow a rework that can
//     never be created, a permanent pending. This covers a rework that died AND a
//     rework that completed without proposing a revision. A rework that DID
//     revise advances the current version, so the fail is no longer bound to it
//     and the normal loop never reaches this branch.
//
// Round progression (resolveRound) deliberately keeps CountReworks alone — see
// JudgmentRepository.CountReworks. An unwired reader falls back to the budget
// test; an unreadable one fails closed.
func (s *JudgmentService) reworkExhausted(ctx context.Context, versionID, caseID, currentVersionID int64) (bool, error) {
	count, err := s.repo.CountReworks(ctx, caseID)
	if err != nil {
		return false, mapDesignError(err, "judgment")
	}
	var terminalRework bool
	if s.reworkBudget != nil {
		terminal, err := s.reworkBudget.TerminalReworkCount(ctx, versionID, caseID)
		if err != nil {
			return false, mapDesignError(err, "rework budget")
		}
		if terminal > count {
			count = terminal
		}
		terminalRework, err = s.reworkBudget.HasTerminalReworkForVersion(ctx, versionID, caseID, currentVersionID)
		if err != nil {
			return false, mapDesignError(err, "rework budget")
		}
	}
	maxRounds, err := s.maxReworkRounds(ctx)
	if err != nil {
		return false, err
	}
	return !design.ReworkAllowed(count, maxRounds) || terminalRework, nil
}

// maxReworkRounds reads the configured rework bound. An unwired or invalid
// settings value fails closed; it never silently assumes a budget.
func (s *JudgmentService) maxReworkRounds(ctx context.Context) (int, error) {
	if s.settings == nil {
		return 0, platform.Unavailable("the settings reader is not wired for the judgment service")
	}
	value, err := s.settings.Int(ctx, maxReworkRoundsKey)
	if err != nil {
		return 0, err
	}
	if value < 0 {
		return 0, platform.Unavailable(maxReworkRoundsKey + " must not be negative")
	}
	return int(value), nil
}

// normalizeFindings trims the stored finding text so the persisted fact is
// canonical regardless of candidate whitespace.
func normalizeFindings(findings []design.JudgmentFinding) []design.JudgmentFinding {
	if len(findings) == 0 {
		return nil
	}
	normalized := make([]design.JudgmentFinding, 0, len(findings))
	for _, finding := range findings {
		normalized = append(normalized, design.JudgmentFinding{
			Dimension: strings.TrimSpace(finding.Dimension),
			Severity:  finding.Severity,
			Message:   strings.TrimSpace(finding.Message),
			CaseRef:   strings.TrimSpace(finding.CaseRef),
		})
	}
	return normalized
}

// JudgeExhaustedEventType is the degradation event emitted when a draft case
// version cannot get a judge verdict and is escalated to a human (constitution
// VII.6: no silent degradation). SINGLE authority for the name: both escalation
// writers publish through JudgeExhaustedEvent.
const JudgeExhaustedEventType = "design.judge_exhausted"

// JudgeExhaustedEvent builds the judge-unavailable degradation event. It is the
// SINGLE authority for the event's channel/scope/payload shape, shared by the
// two escalation writers: the composition root's judge generation budget
// (designTaskEnqueuer.EnsureJudgeGeneration) and the planner's stalled authoring
// batch (escalateStalledAuthorBatch). Two hand-written copies of one event shape
// would be a second truth (constitution V.3).
func JudgeExhaustedEvent(ctx context.Context, versionID, requirementID, caseID, caseVersionID int64) eventingapp.Event {
	version := platformid.Format(versionID)
	requirement := platformid.Format(requirementID)
	return eventingapp.Event{
		Channel: "version:" + version,
		Type:    JudgeExhaustedEventType,
		Scope: map[string]any{
			"version_id":     version,
			"requirement_id": requirement,
		},
		Payload: map[string]any{
			"version_id":      version,
			"requirement_id":  requirement,
			"case_id":         platformid.Format(caseID),
			"case_version_id": platformid.Format(caseVersionID),
		},
		RequestID: requestid.From(ctx),
	}
}

// publishEvent emits one design event on the version channel (best effort,
// mirrors GraphService.publishEvent).
func (s *JudgmentService) publishEvent(ctx context.Context, versionID int64, eventType string, requirementID int64, payload map[string]any, actorID int64) {
	publishDesignEvent(ctx, s.events, versionID, eventType, requirementID, payload, actorID)
}
