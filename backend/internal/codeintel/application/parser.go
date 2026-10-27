package application

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// AIFact is one structured fact produced by the T1 file-analysis prompt
// (design spec §6.2/§8.3). Kind and Confidence are validated against the domain
// enums; Evidence never carries a model-supplied excerpt hash because the
// server always recomputes it from commit content.
type AIFact struct {
	Kind       string
	Name       string
	Path       string
	StartLine  int
	EndLine    int
	Signature  string
	Confidence string
	Summary    string
	Evidence   []domain.Evidence
}

// AIEdgeEndpoint identifies one end of an AI-produced edge by the fact kind and
// name it references (design spec §8.3 "from"/"to").
type AIEdgeEndpoint struct {
	Kind string
	Name string
}

// AIEdge is one structured relation produced by the T1 file-analysis prompt.
type AIEdge struct {
	From       AIEdgeEndpoint
	To         AIEdgeEndpoint
	Rel        string
	Confidence string
	Evidence   []domain.Evidence
}

// FileAnalysis is the parsed output of one file-analysis LLM call.
type FileAnalysis struct {
	ChangeSummary string
	Facts         []AIFact
	Edges         []AIEdge
}

// ParseStats reports how ParseFileAnalysis applied the design spec §6.2 caps.
// TruncatedEntries is the same counter the analysis stats expose as
// stats.truncatedEntries: the number of facts, edges and evidence entries
// dropped because they exceeded MaxFactsPerFile/MaxEdgesPerFile/
// MaxEvidencePerFact.
type ParseStats struct {
	Facts             int
	Edges             int
	TruncatedFacts    int
	TruncatedEdges    int
	TruncatedEvidence int
	TruncatedEntries  int
}

// factKinds is the ai-output kind enum of design spec §6.2. "file" is a T0 git
// fact and is deliberately not accepted from the model.
var factKinds = map[string]struct{}{
	domain.KindRoute:      {},
	domain.KindPage:       {},
	domain.KindAPI:        {},
	domain.KindClientCall: {},
	domain.KindComponent:  {},
	domain.KindModule:     {},
	domain.KindSymbol:     {},
}

// factConfidences is the confidence enum of design spec §6.2.
var factConfidences = map[string]struct{}{
	domain.ConfidenceHigh:   {},
	domain.ConfidenceMedium: {},
	domain.ConfidenceLow:    {},
}

// edgeRelations is the edge relation enum of design spec §6.2/§5.1.
var edgeRelations = map[string]struct{}{
	domain.RelContains:   {},
	domain.RelImports:    {},
	domain.RelDefines:    {},
	domain.RelCalls:      {},
	domain.RelRenders:    {},
	domain.RelRouteToAPI: {},
}

// ParseFileAnalysis strictly parses one T1 LLM response (design spec §6.2):
//
//   - a Markdown code fence and any surrounding explanation are tolerated; the
//     first balanced JSON object is extracted (braces inside strings are
//     ignored);
//   - the required schema fields and their types are enforced: changeSummary,
//     facts and edges must be present, every fact needs a valid kind, name,
//     path, confidence and a positive startLine <= endLine, and every edge
//     needs valid from/to endpoints, rel and confidence;
//   - unknown fields are ignored, so a newer model output does not break the
//     parser;
//   - at most domain.MaxFactsPerFile facts, domain.MaxEdgesPerFile edges and
//     domain.MaxEvidencePerFact evidence entries per fact are kept; over-cap
//     entries are truncated in order and counted in ParseStats.
//
// A parse failure is reported as a domain invalid error with reason
// "parse_error" so the analysis can skip this file and record parse_error; the
// error never embeds code content. The stats are returned alongside the
// analysis so the caller can fold them into the run stats.
func ParseFileAnalysis(raw string) (FileAnalysis, ParseStats, error) {
	var stats ParseStats

	payload, err := extractJSONObject(raw)
	if err != nil {
		return FileAnalysis{}, stats, parseError(err.Error())
	}

	var decoded rawFileAnalysis
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		return FileAnalysis{}, stats, parseError("invalid JSON: " + err.Error())
	}
	if decoded.ChangeSummary == nil {
		return FileAnalysis{}, stats, parseError("changeSummary is required")
	}
	if decoded.Facts == nil {
		return FileAnalysis{}, stats, parseError("facts is required and must be an array")
	}
	if decoded.Edges == nil {
		return FileAnalysis{}, stats, parseError("edges is required and must be an array")
	}

	facts := make([]AIFact, 0, len(*decoded.Facts))
	for index, rawFact := range *decoded.Facts {
		if len(facts) >= domain.MaxFactsPerFile {
			stats.TruncatedFacts++
			continue
		}
		fact, err := rawFact.toAIFact(index, &stats)
		if err != nil {
			return FileAnalysis{}, stats, parseError(err.Error())
		}
		facts = append(facts, fact)
	}

	edges := make([]AIEdge, 0, len(*decoded.Edges))
	for index, rawEdge := range *decoded.Edges {
		if len(edges) >= domain.MaxEdgesPerFile {
			stats.TruncatedEdges++
			continue
		}
		edge, err := rawEdge.toAIEdge(index, &stats)
		if err != nil {
			return FileAnalysis{}, stats, parseError(err.Error())
		}
		edges = append(edges, edge)
	}

	stats.Facts = len(facts)
	stats.Edges = len(edges)
	stats.TruncatedEntries = stats.TruncatedFacts + stats.TruncatedEdges + stats.TruncatedEvidence

	return FileAnalysis{
		ChangeSummary: *decoded.ChangeSummary,
		Facts:         facts,
		Edges:         edges,
	}, stats, nil
}

// extractJSONObject returns the first balanced JSON object of raw. The scan is
// string-aware: braces and escaped quotes inside a JSON string do not affect
// the nesting depth.
func extractJSONObject(raw string) (string, error) {
	start := strings.IndexByte(raw, '{')
	if start < 0 {
		return "", fmt.Errorf("no JSON object found")
	}

	depth := 0
	inString := false
	escaped := false
	for index := start; index < len(raw); index++ {
		char := raw[index]
		if inString {
			switch {
			case escaped:
				escaped = false
			case char == '\\':
				escaped = true
			case char == '"':
				inString = false
			}
			continue
		}
		switch char {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return raw[start : index+1], nil
			}
		}
	}
	return "", fmt.Errorf("unbalanced JSON object")
}

// rawFileAnalysis mirrors the design spec §8.3 output schema with pointers so a
// missing required field is distinguishable from a zero value.
type rawFileAnalysis struct {
	ChangeSummary *string    `json:"changeSummary"`
	Facts         *[]rawFact `json:"facts"`
	Edges         *[]rawEdge `json:"edges"`
}

// rawFact is one fact of the model output. Optional fields keep their zero
// value; required ones are pointers.
type rawFact struct {
	Kind       *string       `json:"kind"`
	Name       *string       `json:"name"`
	Path       *string       `json:"path"`
	StartLine  *int          `json:"startLine"`
	EndLine    *int          `json:"endLine"`
	Signature  string        `json:"signature"`
	Confidence *string       `json:"confidence"`
	Summary    string        `json:"summary"`
	Evidence   []rawEvidence `json:"evidence"`
}

// rawEdge is one edge of the model output.
type rawEdge struct {
	From       *rawEndpoint  `json:"from"`
	To         *rawEndpoint  `json:"to"`
	Rel        *string       `json:"rel"`
	Confidence *string       `json:"confidence"`
	Evidence   []rawEvidence `json:"evidence"`
}

// rawEndpoint is one endpoint of a raw edge.
type rawEndpoint struct {
	Kind *string `json:"kind"`
	Name *string `json:"name"`
}

// rawEvidence is one raw evidence interval. A model-supplied excerptHash is
// deliberately not decoded: the server recomputes it (design spec §6.3).
type rawEvidence struct {
	File      *string `json:"file"`
	StartLine *int    `json:"startLine"`
	EndLine   *int    `json:"endLine"`
}

// toAIFact validates one raw fact and applies the evidence cap.
func (f rawFact) toAIFact(index int, stats *ParseStats) (AIFact, error) {
	if f.Kind == nil {
		return AIFact{}, fmt.Errorf("fact %d is missing kind", index)
	}
	if _, ok := factKinds[*f.Kind]; !ok {
		return AIFact{}, fmt.Errorf("fact %d has an unsupported kind", index)
	}
	if f.Name == nil || strings.TrimSpace(*f.Name) == "" {
		return AIFact{}, fmt.Errorf("fact %d is missing name", index)
	}
	if f.Path == nil || strings.TrimSpace(*f.Path) == "" {
		return AIFact{}, fmt.Errorf("fact %d is missing path", index)
	}
	if err := validateRawLines(index, "fact", f.StartLine, f.EndLine); err != nil {
		return AIFact{}, err
	}
	if f.Confidence == nil {
		return AIFact{}, fmt.Errorf("fact %d is missing confidence", index)
	}
	if _, ok := factConfidences[*f.Confidence]; !ok {
		return AIFact{}, fmt.Errorf("fact %d has an unsupported confidence", index)
	}

	evidence, err := parseEvidence(f.Evidence, stats)
	if err != nil {
		return AIFact{}, fmt.Errorf("fact %d: %w", index, err)
	}
	return AIFact{
		Kind:       *f.Kind,
		Name:       *f.Name,
		Path:       *f.Path,
		StartLine:  *f.StartLine,
		EndLine:    *f.EndLine,
		Signature:  f.Signature,
		Confidence: *f.Confidence,
		Summary:    f.Summary,
		Evidence:   evidence,
	}, nil
}

// toAIEdge validates one raw edge and applies the evidence cap.
func (e rawEdge) toAIEdge(index int, stats *ParseStats) (AIEdge, error) {
	if err := validateRawEndpoint(index, "from", e.From); err != nil {
		return AIEdge{}, err
	}
	if err := validateRawEndpoint(index, "to", e.To); err != nil {
		return AIEdge{}, err
	}
	if e.Rel == nil {
		return AIEdge{}, fmt.Errorf("edge %d is missing rel", index)
	}
	if _, ok := edgeRelations[*e.Rel]; !ok {
		return AIEdge{}, fmt.Errorf("edge %d has an unsupported rel", index)
	}
	if e.Confidence == nil {
		return AIEdge{}, fmt.Errorf("edge %d is missing confidence", index)
	}
	if _, ok := factConfidences[*e.Confidence]; !ok {
		return AIEdge{}, fmt.Errorf("edge %d has an unsupported confidence", index)
	}

	evidence, err := parseEvidence(e.Evidence, stats)
	if err != nil {
		return AIEdge{}, fmt.Errorf("edge %d: %w", index, err)
	}
	return AIEdge{
		From:       AIEdgeEndpoint{Kind: *e.From.Kind, Name: *e.From.Name},
		To:         AIEdgeEndpoint{Kind: *e.To.Kind, Name: *e.To.Name},
		Rel:        *e.Rel,
		Confidence: *e.Confidence,
		Evidence:   evidence,
	}, nil
}

// validateRawEndpoint validates one raw edge endpoint.
func validateRawEndpoint(index int, side string, endpoint *rawEndpoint) error {
	if endpoint == nil {
		return fmt.Errorf("edge %d is missing %s", index, side)
	}
	if endpoint.Kind == nil {
		return fmt.Errorf("edge %d %s is missing kind", index, side)
	}
	if _, ok := factKinds[*endpoint.Kind]; !ok {
		return fmt.Errorf("edge %d %s has an unsupported kind", index, side)
	}
	if endpoint.Name == nil || strings.TrimSpace(*endpoint.Name) == "" {
		return fmt.Errorf("edge %d %s is missing name", index, side)
	}
	return nil
}

// parseEvidence validates raw evidence intervals and applies the per-fact cap.
// Over-cap entries are dropped and counted; invalid in-cap entries fail the
// parse.
func parseEvidence(raw []rawEvidence, stats *ParseStats) ([]domain.Evidence, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	evidence := make([]domain.Evidence, 0, min(len(raw), domain.MaxEvidencePerFact))
	for index, item := range raw {
		if len(evidence) >= domain.MaxEvidencePerFact {
			stats.TruncatedEvidence++
			continue
		}
		if item.File == nil || strings.TrimSpace(*item.File) == "" {
			return nil, fmt.Errorf("evidence %d is missing file", index)
		}
		if err := validateRawLines(index, "evidence", item.StartLine, item.EndLine); err != nil {
			return nil, err
		}
		evidence = append(evidence, domain.Evidence{
			File:      *item.File,
			StartLine: *item.StartLine,
			EndLine:   *item.EndLine,
		})
	}
	return evidence, nil
}

// validateRawLines enforces the design spec §6.2 line rules: both bounds are
// required positive integers and startLine <= endLine.
func validateRawLines(index int, subject string, startLine, endLine *int) error {
	if startLine == nil || endLine == nil {
		return fmt.Errorf("%s %d is missing its line range", subject, index)
	}
	if *startLine < 1 || *endLine < 1 {
		return fmt.Errorf("%s %d has a non-positive line number", subject, index)
	}
	if *endLine < *startLine {
		return fmt.Errorf("%s %d has endLine before startLine", subject, index)
	}
	return nil
}

// parseError wraps a structural parsing failure as a domain invalid error with
// the stable parse_error reason. The detail never contains code content.
func parseError(detail string) error {
	return domain.NewInvalid("llm_response", "parse_error", "codeintel: parse file analysis: "+detail)
}
