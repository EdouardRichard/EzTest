// Package designadapter adapts the design bounded context's application
// services to the agent tools.DesignPort. It is the only bridge between the
// agent tool layer and design/**.
//
// The adapter renders tool results as the snake_case design view shapes defined
// by spec §7.2 (IDs are decimal strings), so the model sees the same graph
// vocabulary as the HTTP API. Optional IDs are exposed as null/omitted by using
// pointers on the way in and skipping nil pointers in the rendered payload.
package designadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// GraphGateway is the design application surface consumed by the agent tools.
// *designapp.GraphService satisfies it; tests use fakes.
type GraphGateway interface {
	ReadGraph(ctx context.Context, versionID int64) (designapp.GraphView, error)
	GetCase(ctx context.Context, versionID, caseID int64, caseVersionID *int64) (designapp.CaseDetailView, error)
	Propose(ctx context.Context, versionID int64, in designapp.ProposeInput, actorID, runID int64) (designapp.ProposeResult, error)
	SubmitReview(ctx context.Context, versionID, requirementID, runID int64, in designapp.ReviewInput) (designapp.ReviewResult, error)
	SubmitDataPlan(ctx context.Context, versionID, caseVersionID, runID int64, in designapp.DataPlanInput) (designapp.DataPlanResult, error)
	// CaseAuthorFacts returns the bounded, credential-free discovery facts the
	// case author reads before proposing cases (Task 37). The read is best
	// effort; an error yields an empty list on the rendered graph.
	CaseAuthorFacts(ctx context.Context, versionID int64) ([]designapp.DiscoveryFact, error)
}

// Adapter implements tools.DesignPort on top of the design application
// services.
type Adapter struct {
	graph GraphGateway
	// judgment settles judge verdict candidates (T6 JudgmentService). rounds and
	// judgeSettings derive the server-supplied judgment round from the append-only
	// facts (T13). All three are nil until SetJudgment wires them; SubmitJudgment
	// then fails closed as an internal configuration error.
	judgment      JudgmentSubmitter
	rounds        JudgmentRoundReader
	judgeSettings JudgeSettingsReader
	// reworkProvenance is the optional T14 refinement (SC-E4): it distinguishes
	// a judge-triggered rework from a Host edit, which the judgment facts alone
	// cannot (case_version.trigger has no "rework" value). Nil keeps the
	// pre-T14 heuristic (any new version after a fail within budget increments).
	reworkProvenance JudgmentReworkProvenanceReader
}

// New wires the design adapter. main passes the design GraphService; a nil
// gateway makes every tool fail as an internal configuration error.
func New(graph GraphGateway) *Adapter {
	return &Adapter{graph: graph}
}

// JudgmentSubmitter is the design application's judge settlement surface
// (*designapp.JudgmentService satisfies it). The adapter maps the tool input into
// designapp.SubmitJudgmentInput and injects the server-supplied round.
type JudgmentSubmitter interface {
	SubmitJudgment(ctx context.Context, versionID, requirementID, runID int64, in designapp.SubmitJudgmentInput) (designapp.SubmitJudgmentResult, error)
}

// JudgmentRoundReader reads the append-only judgment facts the round derivation
// needs (*designpg.JudgmentStore satisfies it): the latest fact of a logical case
// and its fact-derived rework count.
type JudgmentRoundReader interface {
	LatestJudgment(ctx context.Context, caseID int64) (*design.Judgment, error)
	CountReworks(ctx context.Context, caseID int64) (int, error)
}

// JudgeSettingsReader reads the rework bound (*settingsapp.Service satisfies it).
type JudgeSettingsReader interface {
	Int(ctx context.Context, key string) (int64, error)
}

// JudgmentReworkProvenanceReader reports when the design.rework responding to
// ONE SPECIFIC FAIL (caseID, caseVersionID = the failed version being reworked
// from) was requested, or the zero time if none exists (v0.2.4-E T14 fix
// round 1, SC-E4). The composition root satisfies it by reading the
// design.rework work_item rows (agent context), whose payload binds the
// from-version; status is irrelevant because the request exists at creation
// and the judge may settle while the rework item is still running. The design
// context's own judgment facts cannot distinguish a rework-produced draft from
// a Host edit (case_version.trigger only records normal/discovery_delta/rollback).
type JudgmentReworkProvenanceReader interface {
	ReworkRequestedAt(ctx context.Context, versionID, caseID, caseVersionID int64) (time.Time, error)
}

// SetJudgment wires the judge settlement surface and the round-derivation reads.
// It is a setter because the composition root builds the judgment service after
// the design graph adapter (T13). A nil submitter makes SubmitJudgment fail
// closed; nil rounds/settings fall back to the conservative round-0 derivation
// (the service then derives round 1 / the latest judged round).
func (a *Adapter) SetJudgment(submitter JudgmentSubmitter, rounds JudgmentRoundReader, settings JudgeSettingsReader) *Adapter {
	if a != nil {
		a.judgment = submitter
		a.rounds = rounds
		a.judgeSettings = settings
	}
	return a
}

// SetReworkProvenance wires the optional T14 rework-provenance reader. It is a
// separate setter (not a SetJudgment parameter) so every existing caller keeps
// compiling unchanged; nil preserves the pre-T14 round heuristic.
func (a *Adapter) SetReworkProvenance(provenance JudgmentReworkProvenanceReader) *Adapter {
	if a != nil {
		a.reworkProvenance = provenance
	}
	return a
}

var _ tools.DesignPort = (*Adapter)(nil)

// The design GraphService is asserted at compile time; the assembly root passes
// it directly to New.
var _ GraphGateway = (*designapp.GraphService)(nil)
var _ JudgmentSubmitter = (*designapp.JudgmentService)(nil)

// ReadGraph returns the version graph snapshot, optionally narrowed to one
// requirement (requirement_id) or to one case's current version detail
// (case_id), matching the design.graph_read contract.
func (a *Adapter) ReadGraph(ctx context.Context, versionID, requirementID, caseID int64) (any, error) {
	if a.graph == nil {
		return nil, fmt.Errorf("designadapter: design graph service is not configured")
	}
	if caseID > 0 {
		detail, err := a.graph.GetCase(ctx, versionID, caseID, nil)
		if err != nil {
			return nil, err
		}
		return caseDetailPayload(detail), nil
	}
	graph, err := a.graph.ReadGraph(ctx, versionID)
	if err != nil {
		return nil, err
	}
	// The exploration facts are case-author evidence. A fact read failure must
	// not fail the design graph read: the graph is still usable without them.
	discovery := []designapp.DiscoveryFact{}
	if facts, factsErr := a.graph.CaseAuthorFacts(ctx, versionID); factsErr == nil {
		discovery = facts
	}
	return graphPayload(graph, requirementID, discovery), nil
}

// ProposeGraph maps the tool-validated proposal into the design application
// input (optional IDs as pointers) and returns the created IDs as decimal
// strings plus the new graph hash.
func (a *Adapter) ProposeGraph(ctx context.Context, versionID int64, in tools.GraphProposal, actorID, runID int64) (any, error) {
	if a.graph == nil {
		return nil, fmt.Errorf("designadapter: design graph service is not configured")
	}
	result, err := a.graph.Propose(ctx, versionID, proposeInput(in), actorID, runID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"scenario_ids":     idStrings(result.ScenarioIDs),
		"case_ids":         idStrings(result.CaseIDs),
		"case_version_ids": idStrings(result.CaseVersionIDs),
		"graph_hash":       result.GraphHash,
	}, nil
}

// SubmitReview maps a Critic review into the design application input.
func (a *Adapter) SubmitReview(ctx context.Context, versionID, requirementID int64, runID int64, in tools.ReviewInput) (any, error) {
	if a.graph == nil {
		return nil, fmt.Errorf("designadapter: design graph service is not configured")
	}
	findings := make([]designapp.ReviewFinding, 0, len(in.Findings))
	for _, finding := range in.Findings {
		findings = append(findings, designapp.ReviewFinding{
			Severity:      finding.Severity,
			Message:       finding.Message,
			RequirementID: optionalID(finding.RequirementID),
			CaseID:        optionalID(finding.CaseID),
		})
	}
	result, err := a.graph.SubmitReview(ctx, versionID, requirementID, runID, designapp.ReviewInput{
		Summary:  in.Summary,
		Findings: findings,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"posted": result.Posted, "count": result.Count}, nil
}

// SubmitDataPlan maps the Data Planner input into the design application input.
func (a *Adapter) SubmitDataPlan(ctx context.Context, versionID, caseVersionID int64, runID int64, in tools.DataPlanInput) (any, error) {
	if a.graph == nil {
		return nil, fmt.Errorf("designadapter: design graph service is not configured")
	}
	result, err := a.graph.SubmitDataPlan(ctx, versionID, caseVersionID, runID, designapp.DataPlanInput{
		RequirementID: in.RequirementID,
		Preconditions: in.Preconditions,
		TestData:      append(json.RawMessage(nil), in.TestData...),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"case_id":         formatID(result.CaseID),
		"case_version_id": formatID(result.CaseVersionID),
		"revision":        result.Revision,
	}, nil
}

// SubmitJudgment maps one tool-validated judge candidate into the design
// application input and settles it. The round is server-supplied: the adapter
// derives it from the append-only judgment facts (never from the model), and the
// service re-validates it as the authority. The result is the settled fact
// binding rendered as the snake_case design view (decimal-string IDs).
func (a *Adapter) SubmitJudgment(ctx context.Context, versionID, requirementID, runID int64, in tools.JudgmentInput) (any, error) {
	if a.judgment == nil {
		return nil, fmt.Errorf("designadapter: design judgment service is not configured")
	}
	round, err := a.judgeRound(ctx, versionID, in.CaseID, in.CaseVersionID)
	if err != nil {
		return nil, err
	}
	findings := make([]design.JudgmentFinding, 0, len(in.Findings))
	for _, finding := range in.Findings {
		findings = append(findings, design.JudgmentFinding{
			Dimension: finding.Dimension,
			Severity:  finding.Severity,
			Message:   finding.Message,
			CaseRef:   finding.CaseRef,
		})
	}
	result, err := a.judgment.SubmitJudgment(ctx, versionID, requirementID, runID, designapp.SubmitJudgmentInput{
		CaseID:           in.CaseID,
		CaseVersionID:    in.CaseVersionID,
		Round:            round,
		Verdict:          in.Verdict,
		Findings:         findings,
		ACCoverageMatrix: in.ACCoverageMatrix,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"judgment_id":     formatID(result.JudgmentID),
		"case_id":         formatID(result.CaseID),
		"case_version_id": formatID(result.CaseVersionID),
		"content_hash":    result.ContentHash,
		"round":           result.Round,
		"verdict":         result.Verdict,
	}, nil
}

// judgeRound derives the server-supplied judgment round for one case version
// from the append-only facts (constitution III: the model never picks a round).
//
//   - no prior fact, the same version already judged, or a latest pass verdict:
//     round 0 — the service derives conservatively (round 1 for a first judgment,
//     the latest judged round for a host-edit rejudge, which never consumes
//     rework budget).
//   - a latest FAIL bound to an OLDER version while the rework budget still
//     allows another round: latest.Round + 1 (the rework rejudge increments the
//     round so CountReworks = max(round)-1 advances and the rework loop closes).
//   - a latest fail with the budget already spent: round 0 (a host redesign after
//     exhaustion is judged at the same round, never a false judge_exhausted).
//
// v0.2.4-E T14 refinement (SC-E4, fix round 1): with a reworkProvenance reader
// wired, the FAIL-older-version branch additionally requires a design.rework
// request BOUND TO THE LATEST FAIL'S VERSION (the payload's from-version
// binding; status irrelevant — the request exists at creation, and the judge
// may settle while the rework item is still running). A Host edit that lands a
// new draft with no rework responding to this fail keeps round 0 instead of
// consuming budget; a rework bound to an OLDER fail never answers for this one.
// Without the reader (nil), the pre-T14 heuristic above is preserved unchanged.
//
// The service's resolveRound re-validates every increment (defense in depth).
func (a *Adapter) judgeRound(ctx context.Context, versionID, caseID, caseVersionID int64) (int, error) {
	if a.rounds == nil || a.judgeSettings == nil {
		return 0, nil
	}
	latest, err := a.rounds.LatestJudgment(ctx, caseID)
	if err != nil {
		return 0, err
	}
	if latest == nil || latest.CaseVersionID == caseVersionID || latest.Verdict != design.VerdictFail {
		return 0, nil
	}
	count, err := a.rounds.CountReworks(ctx, caseID)
	if err != nil {
		return 0, err
	}
	maxRounds, err := a.judgeSettings.Int(ctx, designapp.MaxReworkRoundsKey)
	if err != nil {
		return 0, err
	}
	// H-F10b #30: a NEGATIVE configured bound is an invalid setting, not "budget
	// spent". The design service fails closed on it
	// (JudgmentService.maxReworkRounds -> platform.Unavailable); answering round 0
	// here would silently judge at the wrong round while the service refuses the
	// submission. One rule on both sides (constitution VII.6, no silent fallback).
	if maxRounds < 0 {
		return 0, platform.Unavailable(designapp.MaxReworkRoundsKey + " must not be negative")
	}
	if !design.ReworkAllowed(count, int(maxRounds)) {
		return 0, nil
	}
	if a.reworkProvenance != nil {
		reworkAt, err := a.reworkProvenance.ReworkRequestedAt(ctx, versionID, caseID, latest.CaseVersionID)
		if err != nil {
			return 0, err
		}
		if !reworkAt.After(latest.CreatedAt) {
			// No rework responds to this fail (zero), or the matched request
			// predates it (impossible via the production writer — kept as a
			// secondary sanity guard): the new version is a Host edit, not a
			// rework response. It rejudges at the same round.
			return 0, nil
		}
	}
	return latest.Round + 1, nil
}

// ---------- tools -> design application ------------------------------------

func proposeInput(in tools.GraphProposal) designapp.ProposeInput {
	proposal := designapp.ProposeInput{
		RequirementID:        in.RequirementID,
		RequirementVersionID: in.RequirementVersionID,
		BaseGraphHash:        in.BaseGraphHash,
		Scenarios:            make([]designapp.ScenarioInput, 0, len(in.Scenarios)),
		Cases:                make([]designapp.CaseInput, 0, len(in.Cases)),
	}
	for _, scenario := range in.Scenarios {
		proposal.Scenarios = append(proposal.Scenarios, designapp.ScenarioInput{
			Ref:         scenario.Ref,
			ID:          optionalID(scenario.ID),
			Name:        scenario.Name,
			Description: scenario.Description,
			SortOrder:   scenario.SortOrder,
		})
	}
	for _, item := range in.Cases {
		mapped := designapp.CaseInput{
			Ref:            item.Ref,
			ID:             optionalID(item.ID),
			ScenarioRef:    item.ScenarioRef,
			Title:          item.Title,
			Description:    item.Description,
			SutDescription: item.SutDescription,
			Preconditions:  item.Preconditions,
			CoveredACs:     append([]string(nil), item.CoveredACs...),
			TestData:       append(json.RawMessage(nil), item.TestData...),
			Priority:       item.Priority,
			Steps:          make([]designapp.StepInput, 0, len(item.Steps)),
			Assertions:     make([]designapp.AssertionInput, 0, len(item.Assertions)),
			Sources:        make([]designapp.SourceInput, 0, len(item.Sources)),
		}
		for _, step := range item.Steps {
			mapped.Steps = append(mapped.Steps, designapp.StepInput{
				Seq:               step.Seq,
				Action:            step.Action,
				Expected:          step.Expected,
				TargetDescription: step.TargetDescription,
				AccountRole:       step.AccountRole,
				Data:              append(json.RawMessage(nil), step.Data...),
			})
		}
		for _, assertion := range item.Assertions {
			mapped.Assertions = append(mapped.Assertions, designapp.AssertionInput{
				StepSeq:  assertion.StepSeq,
				Kind:     assertion.Kind,
				Target:   assertion.Target,
				Expected: assertion.Expected,
				SUTID:    optionalID(assertion.SUTID),
			})
		}
		for _, source := range item.Sources {
			mapped.Sources = append(mapped.Sources, designapp.SourceInput{
				Kind:  source.Kind,
				RefID: source.RefID,
				Path:  source.Path,
				Line:  source.Line,
				SHA:   source.SHA,
			})
		}
		proposal.Cases = append(proposal.Cases, mapped)
	}
	return proposal
}

// ---------- design application -> tool result payloads ---------------------

func graphPayload(view designapp.GraphView, requirementID int64, discovery []designapp.DiscoveryFact) map[string]any {
	requirements := make([]any, 0, len(view.Requirements))
	for _, requirement := range view.Requirements {
		if requirementID > 0 && requirement.RequirementID != requirementID {
			continue
		}
		requirements = append(requirements, requirementPayload(requirement))
	}
	return map[string]any{
		"version_id":                   formatID(view.VersionID),
		"generated_at":                 formatTime(view.GeneratedAt),
		"graph_hash":                   view.GraphHash,
		"pending_design_count":         view.PendingDesignCount,
		"auto_design_new_requirements": view.AutoDesignNewRequirements,
		"stats": map[string]any{
			"requirements": view.Stats.Requirements,
			"scenarios":    view.Stats.Scenarios,
			"cases":        view.Stats.Cases,
			"draft":        view.Stats.Draft,
			"confirmed":    view.Stats.Confirmed,
			"stale":        view.Stats.Stale,
		},
		"requirements":    requirements,
		"discovery_facts": discoveryFactPayloads(discovery),
	}
}

// discoveryFactPayloads renders the case-author discovery facts with decimal
// string ids, mirroring the rest of the tool payload vocabulary.
func discoveryFactPayloads(facts []designapp.DiscoveryFact) []any {
	payloads := make([]any, 0, len(facts))
	for _, fact := range facts {
		payload := map[string]any{
			"fact_id": formatID(fact.ID),
			"kind":    fact.Kind,
			"key":     fact.Key,
			"summary": fact.Summary,
		}
		if fact.Confidence != nil {
			payload["confidence"] = *fact.Confidence
		}
		payloads = append(payloads, payload)
	}
	return payloads
}

func requirementPayload(view designapp.RequirementGraphView) map[string]any {
	scenarios := make([]any, 0, len(view.Scenarios))
	for _, scenario := range view.Scenarios {
		cases := make([]any, 0, len(scenario.Cases))
		for _, item := range scenario.Cases {
			cases = append(cases, map[string]any{
				"case_id":            formatID(item.CaseID),
				"current_version_id": formatID(item.CurrentVersionID),
				"revision":           item.Revision,
				"title":              item.Title,
				"priority":           item.Priority,
				"status":             item.Status,
				// v0.2.4-B: the case card names the system under test in prose and
				// lists the acceptance criteria it covers, instead of resource ids.
				"sut_description": item.SutDescription,
				"covered_acs":     append([]string(nil), item.CoveredACs...),
				"step_count":      item.StepCount,
				"assertion_count": item.AssertionCount,
				"created_at":      formatTime(item.CreatedAt),
			})
		}
		scenarios = append(scenarios, map[string]any{
			"id":                     formatID(scenario.ID),
			"requirement_version_id": formatID(scenario.RequirementVersionID),
			"name":                   scenario.Name,
			"description":            scenario.Description,
			"sort":                   scenario.SortOrder,
			"cases":                  cases,
		})
	}
	return map[string]any{
		"requirement_id":         formatID(view.RequirementID),
		"requirement_version_id": formatID(view.RequirementVersionID),
		"revision":               view.Revision,
		"title":                  view.Title,
		"status":                 view.Status,
		"design_status":          view.DesignStatus,
		"scenarios":              scenarios,
	}
}

func caseDetailPayload(view designapp.CaseDetailView) map[string]any {
	versions := make([]any, 0, len(view.Versions))
	for _, item := range view.Versions {
		payload := map[string]any{
			"case_version_id": formatID(item.CaseVersionID),
			"revision":        item.Revision,
			"status":          item.Status,
			"title":           item.Title,
			"created_at":      formatTime(item.CreatedAt),
			"confirmed_at":    nil,
		}
		if item.ConfirmedAt != nil {
			payload["confirmed_at"] = formatTime(*item.ConfirmedAt)
		}
		versions = append(versions, payload)
	}
	selected := view.Selected

	steps := make([]any, 0, len(selected.Steps))
	for _, step := range selected.Steps {
		steps = append(steps, map[string]any{
			"seq":                step.Seq,
			"action":             step.Action,
			"expected":           step.Expected,
			"target_description": step.TargetDescription,
			"account_role":       step.AccountRole,
			"data":               rawObject(step.Data),
		})
	}
	assertions := make([]any, 0, len(selected.Assertions))
	for _, assertion := range selected.Assertions {
		assertions = append(assertions, map[string]any{
			"step_seq": assertion.StepSeq,
			"kind":     assertion.Kind,
			"target":   assertion.Target,
			"expected": assertion.Expected,
			"sut_id":   formatOptionalID(assertion.SUTID),
		})
	}
	sources := make([]any, 0, len(selected.Sources))
	for _, source := range selected.Sources {
		sources = append(sources, map[string]any{
			"kind":   source.Kind,
			"ref_id": source.RefID,
			"path":   source.Path,
			"line":   optionalInt(source.Line),
			"sha":    source.SHA,
		})
	}
	return map[string]any{
		"case_id":                formatID(view.CaseID),
		"status":                 view.Status,
		"current_version_id":     formatOptionalID(view.CurrentVersionID),
		"requirement_id":         formatID(view.RequirementID),
		"requirement_version_id": formatID(view.RequirementVersionID),
		"scenario_id":            formatID(view.ScenarioID),
		"scenario_name":          view.ScenarioName,
		"versions":               versions,
		"confirmable":            view.Confirmable,
		"selected": map[string]any{
			"case_version_id": formatID(selected.CaseVersionID),
			"revision":        selected.Revision,
			"title":           selected.Title,
			"description":     selected.Description,
			"sut_description": selected.SutDescription,
			"preconditions":   selected.Preconditions,
			"covered_acs":     append([]string(nil), selected.CoveredACs...),
			"test_data":       rawObject(selected.TestData),
			"priority":        selected.Priority,
			"status":          selected.Status,
			"content_hash":    selected.ContentHash,
			"created_at":      formatTime(selected.CreatedAt),
			"steps":           steps,
			"assertions":      assertions,
			"sources":         sources,
		},
	}
}

// ---------- small conversions ----------------------------------------------

func optionalID(value int64) *int64 {
	if value <= 0 {
		return nil
	}
	copied := value
	return &copied
}

func formatID(value int64) string { return strconv.FormatInt(value, 10) }

func formatOptionalID(value *int64) any {
	if value == nil || *value <= 0 {
		return nil
	}
	return formatID(*value)
}

func optionalInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func idStrings(ids []int64) []string {
	values := make([]string, 0, len(ids))
	for _, id := range ids {
		values = append(values, formatID(id))
	}
	return values
}

func rawObject(raw json.RawMessage) any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	return raw
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
}
