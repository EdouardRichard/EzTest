// Package codeintellm adapts the shared ChatGateway to the codeintel
// FactExtractor port (spec §5.2/§5.4). Prompts demand one strict JSON object;
// the model never supplies excerpt hashes because the application recomputes
// them from the commit content.
package codeintellm

import (
	"fmt"
	"strings"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

// ExtractionSystemPrompt is the fixed T1 extraction instruction. The prompt
// version is recorded per fact from codeintel.prompt_version (settings), not
// from this constant.
const ExtractionSystemPrompt = `You extract structured code facts from one source file of a Git repository.
Return exactly one JSON object and nothing else (no prose, no markdown fence).
The object shape is:
{"facts":[{"kind":"route|page|api|client_call|component|symbol|module","name":"string","signature":"string","start_line":1,"end_line":1,"confidence":"high|medium|low","body":{},"evidence":[{"start_line":1,"end_line":1}]}],
 "edges":[{"src_kind":"string","src_name":"string","dst_kind":"string","dst_name":"string","rel":"contains|imports|defines|calls|renders|route_to_api","confidence":"high|medium|low","evidence":[{"start_line":1,"end_line":1}]}]}
Rules:
- Use only facts that are directly supported by the file content.
- api and client_call names MUST be "<HTTP_METHOD> <path>" (for example "POST /api/refund").
- route and page names are URL paths (for example "/orders").
- component and symbol names are the identifier and body must describe the symbol.
- All line numbers are 1-based, closed intervals inside this file.
- Evidence excerpts must quote existing lines of this file and are at most the stated excerpt limit.
- Never invent evidence, files, hashes, credentials or paths.
- body must be a JSON object with structured fields (method/path/route/props/...).`

// ImpactSystemPrompt is the single AI impact call instruction (spec §5.4).
const ImpactSystemPrompt = `You reason about the impact of a code change on a test-management version.
Return exactly one JSON object and nothing else (no prose, no markdown fence).
The object shape is:
{"candidates":[{"subject_kind":"requirement|requirement_version|case_version|sut_page|sut_api","subject_id":"decimal string","reason":"short explanation","confidence":"high|medium|low","evidence":[{"file":"repo-relative path","start_line":1,"end_line":1}]}]}
Rules:
- Only propose subjects from the provided lists; use their decimal IDs exactly.
- Evidence must refer to files and 1-based line ranges from the changed file list.
- Do not invent IDs, files or line numbers.
- Order candidates by decreasing confidence.`

// ExtractionMessages builds the per-file extraction transcript.
func ExtractionMessages(in codeintelapp.FactExtractInput) []llmapp.ChatMessage {
	maxFacts := in.MaxFacts
	if maxFacts <= 0 {
		maxFacts = 200
	}
	maxEdges := in.MaxEdges
	if maxEdges <= 0 {
		maxEdges = 200
	}
	maxExcerpt := in.MaxExcerptLines
	if maxExcerpt <= 0 {
		maxExcerpt = 40
	}

	var builder strings.Builder
	builder.WriteString("Analyze exactly this file and return the JSON object.\n")
	fmt.Fprintf(&builder, "Repository: %s\nCommit: %s\nFile: %s\n", in.Repo.Name, in.CommitSHA, in.Path)
	fmt.Fprintf(&builder, "Limits: at most %d facts, at most %d edges, evidence at most %d lines each.\n", maxFacts, maxEdges, maxExcerpt)
	fmt.Fprintf(&builder, "Prompt version: %s\n", in.PromptVersion)
	builder.WriteString("File content (numbered lines, 1-based):\n")
	for index, line := range in.Lines {
		fmt.Fprintf(&builder, "%d: %s\n", index+1, line)
	}
	return []llmapp.ChatMessage{
		{Role: "system", Content: ExtractionSystemPrompt},
		{Role: "user", Content: builder.String()},
	}
}

// ImpactMessages builds the one-shot impact inference transcript.
func ImpactMessages(in codeintelapp.ImpactInferenceInput) []llmapp.ChatMessage {
	var builder strings.Builder
	builder.WriteString("Infer which requirements, case versions, pages and APIs are affected by this change.\n")
	fmt.Fprintf(&builder, "Repository: %s\nCommit: %s\nPrompt version: %s\n", in.Repo.Name, in.CommitSHA, in.PromptVersion)
	builder.WriteString("Changed files:\n")
	for _, file := range in.ChangedFiles {
		fmt.Fprintf(&builder, "- %s (%s)\n", file.Path, file.Status)
	}
	builder.WriteString("Extracted facts:\n")
	for _, fact := range in.Facts {
		fmt.Fprintf(&builder, "- %s | kind=%s | path=%s | name=%s\n", fact.FactKey, fact.Kind, fact.Path, fact.Name)
	}
	builder.WriteString("Requirements of this version (id | title):\n")
	maxRequirements := in.MaxRequirements
	if maxRequirements <= 0 {
		maxRequirements = 100
	}
	for index, requirement := range in.Requirements {
		if index >= maxRequirements {
			break
		}
		fmt.Fprintf(&builder, "- %d | %s\n", requirement.ID, requirement.Title)
	}
	if len(in.Selected) > 0 {
		builder.WriteString("Already selected deterministic candidates:\n")
		for _, candidate := range in.Selected {
			fmt.Fprintf(&builder, "- %s:%d | source=%s | confidence=%s\n",
				candidate.SubjectKind, candidate.SubjectID, candidate.Source, candidate.Confidence)
		}
	}
	return []llmapp.ChatMessage{
		{Role: "system", Content: ImpactSystemPrompt},
		{Role: "user", Content: builder.String()},
	}
}
