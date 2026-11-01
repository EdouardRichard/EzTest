package codeintellm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// Extractor implements codeintelapp.FactExtractor over the shared ChatGateway.
type Extractor struct {
	chat llmapp.ChatGateway
}

// New builds the T1 extractor. A nil gateway is reported as ai_unavailable on
// every call instead of panicking.
func New(chat llmapp.ChatGateway) *Extractor {
	return &Extractor{chat: chat}
}

var _ codeintelapp.FactExtractor = (*Extractor)(nil)

// Model resolves the catalog model name recorded on the analysis row/facts.
func (e *Extractor) Model(ctx context.Context) (string, error) {
	if e == nil || e.chat == nil {
		return "", platform.AIUnavailable("llm chat gateway is not configured")
	}
	info, err := e.chat.ResolveModel(ctx, "")
	if err != nil {
		return "", err
	}
	return info.Model, nil
}

// Extract performs one per-file completion and parses the strict JSON response.
func (e *Extractor) Extract(ctx context.Context, in codeintelapp.FactExtractInput) (codeintelapp.FactExtractOutput, error) {
	if e == nil || e.chat == nil {
		return codeintelapp.FactExtractOutput{}, platform.AIUnavailable("llm chat gateway is not configured")
	}
	response, err := e.chat.Chat(ctx, llmapp.ChatRequest{Messages: ExtractionMessages(in)})
	if err != nil {
		return codeintelapp.FactExtractOutput{}, err
	}
	return parseExtraction(response.Content, in)
}

// Impact performs the single AI impact inference call of one analysis.
func (e *Extractor) Impact(ctx context.Context, in codeintelapp.ImpactInferenceInput) (codeintelapp.ImpactInferenceOutput, error) {
	if e == nil || e.chat == nil {
		return codeintelapp.ImpactInferenceOutput{}, platform.AIUnavailable("llm chat gateway is not configured")
	}
	response, err := e.chat.Chat(ctx, llmapp.ChatRequest{Messages: ImpactMessages(in)})
	if err != nil {
		return codeintelapp.ImpactInferenceOutput{}, err
	}
	return parseImpact(response.Content, in)
}

type rawEvidence struct {
	File      string `json:"file"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

type rawFact struct {
	Kind       string          `json:"kind"`
	Name       string          `json:"name"`
	Signature  string          `json:"signature"`
	Path       string          `json:"path"`
	StartLine  int             `json:"start_line"`
	EndLine    int             `json:"end_line"`
	Confidence string          `json:"confidence"`
	Body       json.RawMessage `json:"body"`
	Evidence   []rawEvidence   `json:"evidence"`
}

type rawEdge struct {
	SrcKind    string        `json:"src_kind"`
	SrcName    string        `json:"src_name"`
	DstKind    string        `json:"dst_kind"`
	DstName    string        `json:"dst_name"`
	Rel        string        `json:"rel"`
	Confidence string        `json:"confidence"`
	Evidence   []rawEvidence `json:"evidence"`
}

type extractionResponse struct {
	Facts []rawFact `json:"facts"`
	Edges []rawEdge `json:"edges"`
}

func parseExtraction(content string, in codeintelapp.FactExtractInput) (codeintelapp.FactExtractOutput, error) {
	var response extractionResponse
	if err := decodeJSONObject(content, &response); err != nil {
		return codeintelapp.FactExtractOutput{}, fmt.Errorf("extract facts: %w", err)
	}
	maxFacts := in.MaxFacts
	if maxFacts <= 0 || maxFacts > codeintel.MaxFactsPerFile {
		maxFacts = codeintel.MaxFactsPerFile
	}
	maxEdges := in.MaxEdges
	if maxEdges <= 0 || maxEdges > codeintel.MaxEdgesPerFile {
		maxEdges = codeintel.MaxEdgesPerFile
	}

	output := codeintelapp.FactExtractOutput{
		Facts: make([]codeintelapp.FactDraft, 0, len(response.Facts)),
		Edges: make([]codeintelapp.EdgeDraft, 0, len(response.Edges)),
	}
	for _, raw := range response.Facts {
		if len(output.Facts) >= maxFacts {
			output.InvalidFacts++
			continue
		}
		kind := codeintel.EntityKind(strings.ToLower(strings.TrimSpace(raw.Kind)))
		if !kind.Valid() || kind == codeintel.EntityFile {
			output.InvalidFacts++
			continue
		}
		name := strings.TrimSpace(raw.Name)
		if name == "" || strings.ContainsAny(name, "\x00\r\n") {
			output.InvalidFacts++
			continue
		}
		if raw.Path != "" {
			normalized, err := codeintel.NormalizeRepoPath(raw.Path)
			if err != nil || normalized != in.Path {
				output.InvalidFacts++
				continue
			}
		}
		if _, ok := codeintel.FactKey(kind, in.Path, name, "", ""); !ok {
			output.InvalidFacts++
			continue
		}
		evidence := make([]codeintelapp.EvidenceDraft, 0, len(raw.Evidence))
		for _, item := range raw.Evidence {
			if len(evidence) >= codeintel.MaxEvidencePerFact {
				break
			}
			evidence = append(evidence, codeintelapp.EvidenceDraft{
				File:      in.Path,
				StartLine: item.StartLine,
				EndLine:   item.EndLine,
			})
		}
		output.Facts = append(output.Facts, codeintelapp.FactDraft{
			Kind:       kind,
			Name:       truncateRunes(name, 512),
			Signature:  truncateRunes(strings.TrimSpace(raw.Signature), 2000),
			Path:       in.Path,
			StartLine:  raw.StartLine,
			EndLine:    raw.EndLine,
			Body:       validObject(raw.Body),
			Confidence: normalizeConfidence(raw.Confidence),
			Evidence:   evidence,
		})
	}
	for _, raw := range response.Edges {
		if len(output.Edges) >= maxEdges {
			break
		}
		rel := codeintel.EdgeRel(strings.ToLower(strings.TrimSpace(raw.Rel)))
		if !rel.Valid() {
			continue
		}
		srcKey, srcOK := rawEdgeKey(raw.SrcKind, raw.SrcName, in.Path)
		dstKey, dstOK := rawEdgeKey(raw.DstKind, raw.DstName, in.Path)
		if !srcOK || !dstOK || srcKey == dstKey {
			continue
		}
		evidence := make([]codeintelapp.EvidenceDraft, 0, len(raw.Evidence))
		for _, item := range raw.Evidence {
			if len(evidence) >= codeintel.MaxEvidencePerFact {
				break
			}
			evidence = append(evidence, codeintelapp.EvidenceDraft{File: in.Path, StartLine: item.StartLine, EndLine: item.EndLine})
		}
		output.Edges = append(output.Edges, codeintelapp.EdgeDraft{
			SrcFactKey: srcKey,
			DstFactKey: dstKey,
			Rel:        rel,
			Confidence: normalizeConfidence(raw.Confidence),
			Evidence:   evidence,
		})
	}
	return output, nil
}

type rawCandidate struct {
	SubjectKind string          `json:"subject_kind"`
	SubjectID   json.RawMessage `json:"subject_id"`
	Reason      string          `json:"reason"`
	Confidence  string          `json:"confidence"`
	Evidence    []rawEvidence   `json:"evidence"`
}

type impactResponse struct {
	Candidates []rawCandidate `json:"candidates"`
}

func parseImpact(content string, in codeintelapp.ImpactInferenceInput) (codeintelapp.ImpactInferenceOutput, error) {
	var response impactResponse
	if err := decodeJSONObject(content, &response); err != nil {
		return codeintelapp.ImpactInferenceOutput{}, fmt.Errorf("infer impact: %w", err)
	}
	output := codeintelapp.ImpactInferenceOutput{Candidates: make([]codeintelapp.ImpactDraft, 0, len(response.Candidates))}
	for _, raw := range response.Candidates {
		kind := codeintel.SubjectKind(strings.ToLower(strings.TrimSpace(raw.SubjectKind)))
		if !kind.Valid() {
			output.InvalidCandidates++
			continue
		}
		subjectID, err := parseID(raw.SubjectID)
		if err != nil || subjectID <= 0 {
			output.InvalidCandidates++
			continue
		}
		evidence := make([]codeintelapp.EvidenceDraft, 0, len(raw.Evidence))
		for _, item := range raw.Evidence {
			if len(evidence) >= 5 {
				break
			}
			file := strings.TrimSpace(item.File)
			if file == "" {
				output.InvalidCandidates++
				continue
			}
			evidence = append(evidence, codeintelapp.EvidenceDraft{File: file, StartLine: item.StartLine, EndLine: item.EndLine})
		}
		output.Candidates = append(output.Candidates, codeintelapp.ImpactDraft{
			SubjectKind: kind,
			SubjectID:   subjectID,
			Reason:      truncateRunes(strings.TrimSpace(raw.Reason), codeintel.MaxReasonChars),
			Confidence:  normalizeConfidence(raw.Confidence),
			Evidence:    evidence,
		})
	}
	return output, nil
}

// rawEdgeKey builds the canonical fact key of an edge endpoint.
func rawEdgeKey(rawKind, rawName, filePath string) (string, bool) {
	kind := codeintel.EntityKind(strings.ToLower(strings.TrimSpace(rawKind)))
	if !kind.Valid() || kind == codeintel.EntityFile {
		return "", false
	}
	name := strings.TrimSpace(rawName)
	if name == "" {
		return "", false
	}
	return codeintel.FactKey(kind, filePath, name, "", "")
}

// decodeJSONObject parses strict JSON, tolerating a markdown fence or leading
// prose around the object while still rejecting malformed documents.
func decodeJSONObject(content string, target any) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return errors.New("empty model response")
	}
	if direct := json.Unmarshal([]byte(content), target); direct == nil {
		return nil
	}
	fenced := content
	if strings.HasPrefix(fenced, "```") {
		if newline := strings.IndexByte(fenced, '\n'); newline >= 0 {
			fenced = fenced[newline+1:]
		}
		fenced = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(fenced), "```"))
		if direct := json.Unmarshal([]byte(fenced), target); direct == nil {
			return nil
		}
	}
	start := strings.IndexByte(fenced, '{')
	end := strings.LastIndexByte(fenced, '}')
	if start >= 0 && end > start {
		if direct := json.Unmarshal([]byte(fenced[start:end+1]), target); direct == nil {
			return nil
		}
	}
	return errors.New("model response is not a JSON object")
}

// parseID accepts decimal strings and JSON numbers for subject IDs.
func parseID(raw json.RawMessage) (int64, error) {
	if len(raw) == 0 {
		return 0, errors.New("missing subject id")
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strconv.ParseInt(strings.TrimSpace(text), 10, 64)
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		return number.Int64()
	}
	return 0, errors.New("subject id must be a decimal string or number")
}

func normalizeConfidence(raw string) codeintel.Confidence {
	confidence := codeintel.Confidence(strings.ToLower(strings.TrimSpace(raw)))
	if confidence.Valid() {
		return confidence
	}
	return codeintel.ConfidenceMedium
}

func validObject(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
