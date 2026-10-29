package application

import (
	"fmt"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// validAnalysisJSON is the smallest response that satisfies the design spec
// §6.2 output schema.
func validAnalysisJSON() string {
	return `{"changeSummary":"新增订单列表","facts":[{"kind":"route","name":"/orders","path":"src/routes.ts","startLine":3,"endLine":9,"signature":"router.get('/orders')","confidence":"high","summary":"订单列表路由","evidence":[{"file":"src/routes.ts","startLine":3,"endLine":9}]}],"edges":[{"from":{"kind":"component","name":"OrderList"},"to":{"kind":"route","name":"/orders"},"rel":"renders","confidence":"medium","evidence":[{"file":"src/OrderList.tsx","startLine":1,"endLine":5}]}]}`
}

// TestParseFileAnalysisAcceptsFencedJSON pins the tolerant transport format: an
// explanation and a Markdown JSON fence around the object are accepted.
func TestParseFileAnalysisAcceptsFencedJSON(t *testing.T) {
	raw := "下面是分析结果：\n```json\n" + validAnalysisJSON() + "\n```\n"

	analysis, stats, err := ParseFileAnalysis(raw)
	if err != nil {
		t.Fatalf("ParseFileAnalysis() error = %v", err)
	}
	if analysis.ChangeSummary != "新增订单列表" {
		t.Fatalf("ChangeSummary = %q", analysis.ChangeSummary)
	}
	if len(analysis.Facts) != 1 {
		t.Fatalf("len(Facts) = %d", len(analysis.Facts))
	}
	fact := analysis.Facts[0]
	if fact.Kind != domain.KindRoute || fact.Name != "/orders" || fact.Path != "src/routes.ts" {
		t.Fatalf("fact identity = %+v", fact)
	}
	if fact.StartLine != 3 || fact.EndLine != 9 {
		t.Fatalf("fact lines = %d..%d", fact.StartLine, fact.EndLine)
	}
	if fact.Confidence != domain.ConfidenceHigh || fact.Summary != "订单列表路由" || fact.Signature != "router.get('/orders')" {
		t.Fatalf("fact fields = %+v", fact)
	}
	if len(fact.Evidence) != 1 || fact.Evidence[0].File != "src/routes.ts" {
		t.Fatalf("fact evidence = %+v", fact.Evidence)
	}
	if len(analysis.Edges) != 1 {
		t.Fatalf("len(Edges) = %d", len(analysis.Edges))
	}
	edge := analysis.Edges[0]
	if edge.From.Kind != domain.KindComponent || edge.From.Name != "OrderList" {
		t.Fatalf("edge from = %+v", edge.From)
	}
	if edge.To.Kind != domain.KindRoute || edge.To.Name != "/orders" {
		t.Fatalf("edge to = %+v", edge.To)
	}
	if edge.Rel != domain.RelRenders || edge.Confidence != domain.ConfidenceMedium {
		t.Fatalf("edge rel/confidence = %q/%q", edge.Rel, edge.Confidence)
	}
	if stats.Facts != 1 || stats.Edges != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	if stats.TruncatedEntries != 0 {
		t.Fatalf("stats.TruncatedEntries = %d, want 0", stats.TruncatedEntries)
	}
}

// TestParseFileAnalysisIgnoresModelHash proves the parser never trusts a hash
// supplied by the model: the excerpt hash is always recomputed server-side
// against commit content (design spec §6.3).
func TestParseFileAnalysisIgnoresModelHash(t *testing.T) {
	raw := strings.Replace(validAnalysisJSON(),
		`{"file":"src/routes.ts","startLine":3,"endLine":9}`,
		`{"file":"src/routes.ts","startLine":3,"endLine":9,"excerptHash":"model-supplied"}`, 1)

	analysis, _, err := ParseFileAnalysis(raw)
	if err != nil {
		t.Fatalf("ParseFileAnalysis() error = %v", err)
	}
	if got := analysis.Facts[0].Evidence[0].ExcerptHash; got != "" {
		t.Fatalf("ExcerptHash = %q, want empty (model hash must be dropped)", got)
	}
}

// TestParseFileAnalysisRejectsInvalidPayload pins the strict schema rules from
// design spec §6.2: required fields and types, enum values and positive lines.
func TestParseFileAnalysisRejectsInvalidPayload(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"no JSON object", "the model refused to answer"},
		{"unbalanced object", `{"changeSummary":"x","facts":[]`},
		{"missing changeSummary", `{"facts":[],"edges":[]}`},
		{"missing facts", `{"changeSummary":"x","edges":[]}`},
		{"missing edges", `{"changeSummary":"x","facts":[]}`},
		{"facts wrong type", `{"changeSummary":"x","facts":{},"edges":[]}`},
		{"facts null", `{"changeSummary":"x","facts":null,"edges":[]}`},
		{"unknown fact kind", `{"changeSummary":"x","facts":[{"kind":"file","name":"a","path":"a.ts","startLine":1,"endLine":1,"confidence":"high"}],"edges":[]}`},
		{"empty fact name", `{"changeSummary":"x","facts":[{"kind":"route","name":"","path":"a.ts","startLine":1,"endLine":1,"confidence":"high"}],"edges":[]}`},
		{"missing fact path", `{"changeSummary":"x","facts":[{"kind":"route","name":"/a","startLine":1,"endLine":1,"confidence":"high"}],"edges":[]}`},
		{"zero start line", `{"changeSummary":"x","facts":[{"kind":"route","name":"/a","path":"a.ts","startLine":0,"endLine":1,"confidence":"high"}],"edges":[]}`},
		{"negative start line", `{"changeSummary":"x","facts":[{"kind":"route","name":"/a","path":"a.ts","startLine":-2,"endLine":1,"confidence":"high"}],"edges":[]}`},
		{"end before start", `{"changeSummary":"x","facts":[{"kind":"route","name":"/a","path":"a.ts","startLine":4,"endLine":2,"confidence":"high"}],"edges":[]}`},
		{"unknown confidence", `{"changeSummary":"x","facts":[{"kind":"route","name":"/a","path":"a.ts","startLine":1,"endLine":1,"confidence":"certain"}],"edges":[]}`},
		{"missing confidence", `{"changeSummary":"x","facts":[{"kind":"route","name":"/a","path":"a.ts","startLine":1,"endLine":1}],"edges":[]}`},
		{"bad evidence line", `{"changeSummary":"x","facts":[{"kind":"route","name":"/a","path":"a.ts","startLine":1,"endLine":1,"confidence":"high","evidence":[{"file":"a.ts","startLine":0,"endLine":1}]}],"edges":[]}`},
		{"edge unknown rel", `{"changeSummary":"x","facts":[],"edges":[{"from":{"kind":"component","name":"A"},"to":{"kind":"route","name":"/a"},"rel":"owns","confidence":"high"}]}`},
		{"edge missing endpoint", `{"changeSummary":"x","facts":[],"edges":[{"from":{"kind":"component","name":"A"},"rel":"calls","confidence":"high"}]}`},
		{"edge missing rel", `{"changeSummary":"x","facts":[],"edges":[{"from":{"kind":"component","name":"A"},"to":{"kind":"route","name":"/a"},"confidence":"high"}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := ParseFileAnalysis(tt.raw); err == nil {
				t.Fatalf("ParseFileAnalysis(%q) error = nil, want error", tt.raw)
			} else if domain.Reason(err) != "parse_error" {
				t.Fatalf("Reason(err) = %q, want parse_error", domain.Reason(err))
			}
		})
	}
}

// TestParseFileAnalysisAllowsOptionalFields proves the optional parts of the
// schema (signature, summary, edges' evidence) can be omitted.
func TestParseFileAnalysisAllowsOptionalFields(t *testing.T) {
	raw := `{"changeSummary":"x","facts":[{"kind":"module","name":"OrderMod","path":"src/order.ts","startLine":1,"endLine":2,"confidence":"low"}],"edges":[{"from":{"kind":"component","name":"OrderList"},"to":{"kind":"module","name":"OrderMod"},"rel":"imports","confidence":"low"}]}`
	analysis, _, err := ParseFileAnalysis(raw)
	if err != nil {
		t.Fatalf("ParseFileAnalysis() error = %v", err)
	}
	if len(analysis.Facts) != 1 || len(analysis.Edges) != 1 {
		t.Fatalf("facts/edges = %d/%d", len(analysis.Facts), len(analysis.Edges))
	}
}

// TestParseFileAnalysisIgnoresUnknownFields pins forward compatibility: unknown
// fields are ignored rather than rejected.
func TestParseFileAnalysisIgnoresUnknownFields(t *testing.T) {
	raw := `{"changeSummary":"x","model":"deepseek-flash","usage":{"tokens":3},"facts":[{"kind":"route","name":"/a","path":"a.ts","startLine":1,"endLine":1,"confidence":"high","extra":{"nested":true}}],"edges":[],"unknown":1}`
	analysis, _, err := ParseFileAnalysis(raw)
	if err != nil {
		t.Fatalf("ParseFileAnalysis() error = %v", err)
	}
	if len(analysis.Facts) != 1 {
		t.Fatalf("len(Facts) = %d", len(analysis.Facts))
	}
}

// TestParseFileAnalysisExtractsFirstBalancedObject proves the scanner ignores
// braces inside JSON strings and stops at the first balanced object.
func TestParseFileAnalysisExtractsFirstBalancedObject(t *testing.T) {
	raw := `分析结果：{"changeSummary":"返回值含 } 与 \" 引号","facts":[],"edges":[]} 后续 {"changeSummary":"second","facts":[],"edges":[]}`
	analysis, _, err := ParseFileAnalysis(raw)
	if err != nil {
		t.Fatalf("ParseFileAnalysis() error = %v", err)
	}
	if analysis.ChangeSummary != `返回值含 } 与 " 引号` {
		t.Fatalf("ChangeSummary = %q", analysis.ChangeSummary)
	}
}

// TestParseFileAnalysisTruncatesOverCapFacts proves the per-file caps of design
// spec §6.2: over-cap entries are truncated in order and counted in
// stats.truncatedEntries.
func TestParseFileAnalysisTruncatesOverCapFacts(t *testing.T) {
	factCount := domain.MaxFactsPerFile + 2
	edgeCount := domain.MaxEdgesPerFile + 1

	var b strings.Builder
	b.WriteString(`{"changeSummary":"bulk","facts":[`)
	for i := 0; i < factCount; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"kind":"symbol","name":"Sym%d","path":"src/a.go","startLine":1,"endLine":2,"confidence":"high"}`, i)
	}
	b.WriteString(`],"edges":[`)
	for i := 0; i < edgeCount; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"from":{"kind":"symbol","name":"Sym%d"},"to":{"kind":"route","name":"/a"},"rel":"calls","confidence":"high"}`, i)
	}
	b.WriteString(`]}`)

	analysis, stats, err := ParseFileAnalysis(b.String())
	if err != nil {
		t.Fatalf("ParseFileAnalysis() error = %v", err)
	}
	if len(analysis.Facts) != domain.MaxFactsPerFile {
		t.Fatalf("len(Facts) = %d, want %d", len(analysis.Facts), domain.MaxFactsPerFile)
	}
	if len(analysis.Edges) != domain.MaxEdgesPerFile {
		t.Fatalf("len(Edges) = %d, want %d", len(analysis.Edges), domain.MaxEdgesPerFile)
	}
	if stats.Facts != domain.MaxFactsPerFile || stats.Edges != domain.MaxEdgesPerFile {
		t.Fatalf("stats kept counts = %+v", stats)
	}
	if stats.TruncatedFacts != factCount-domain.MaxFactsPerFile {
		t.Fatalf("stats.TruncatedFacts = %d, want %d", stats.TruncatedFacts, factCount-domain.MaxFactsPerFile)
	}
	if stats.TruncatedEdges != edgeCount-domain.MaxEdgesPerFile {
		t.Fatalf("stats.TruncatedEdges = %d, want %d", stats.TruncatedEdges, edgeCount-domain.MaxEdgesPerFile)
	}
	wantEntries := (factCount - domain.MaxFactsPerFile) + (edgeCount - domain.MaxEdgesPerFile)
	if stats.TruncatedEntries != wantEntries {
		t.Fatalf("stats.TruncatedEntries = %d, want %d", stats.TruncatedEntries, wantEntries)
	}
}

// TestParseFileAnalysisTruncatesOverCapEvidence proves one fact keeps at most
// MaxEvidencePerFact evidence entries and that the dropped entries are counted.
func TestParseFileAnalysisTruncatesOverCapEvidence(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"changeSummary":"evidence","facts":[{"kind":"route","name":"/a","path":"a.ts","startLine":1,"endLine":1,"confidence":"high","evidence":[`)
	for i := 0; i < domain.MaxEvidencePerFact+2; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"file":"a.ts","startLine":%d,"endLine":%d}`, i+1, i+1)
	}
	b.WriteString(`]}],"edges":[]}`)

	analysis, stats, err := ParseFileAnalysis(b.String())
	if err != nil {
		t.Fatalf("ParseFileAnalysis() error = %v", err)
	}
	if got := len(analysis.Facts[0].Evidence); got != domain.MaxEvidencePerFact {
		t.Fatalf("len(Evidence) = %d, want %d", got, domain.MaxEvidencePerFact)
	}
	if stats.TruncatedEvidence != 2 || stats.TruncatedEntries != 2 {
		t.Fatalf("stats = %+v, want 2 truncated evidence entries", stats)
	}
}
