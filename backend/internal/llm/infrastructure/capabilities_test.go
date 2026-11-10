package llminfra

import (
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
)

// TestCapabilitySourcesJSONBRoundTrip pins the persistence half of S-02: the
// per-capability catalog|manual provenance markers live in the existing
// capabilities JSONB object, so a refresh sees them again after a reload. Rows
// stored before the markers existed keep inferring the source from the model's
// manual/catalog origin on read.
func TestCapabilitySourcesJSONBRoundTrip(t *testing.T) {
	stored := llm.Model{
		Source: llm.SourceCatalog,
		Capabilities: llm.Capabilities{
			SupportsTools:             true,
			SupportsVision:            true,
			SupportsReasoning:         false,
			SupportsPromptCache:       true,
			ContextWindow:             128000,
			SupportsToolsSource:       llm.SourceCatalog,
			SupportsVisionSource:      llm.SourceManual,
			SupportsReasoningSource:   llm.SourceCatalog,
			SupportsPromptCacheSource: llm.SourceManual,
		},
	}
	raw, err := encodeCapabilities(stored.EffectiveCapabilities())
	if err != nil {
		t.Fatalf("encodeCapabilities: %v", err)
	}
	loaded := llm.Model{Source: llm.SourceCatalog}
	if err := decodeCapabilities(raw, &loaded); err != nil {
		t.Fatalf("decodeCapabilities: %v", err)
	}
	capabilities := loaded.EffectiveCapabilities()
	if capabilities.SupportsVisionSource != llm.SourceManual ||
		capabilities.SupportsPromptCacheSource != llm.SourceManual ||
		capabilities.SupportsToolsSource != llm.SourceCatalog ||
		capabilities.SupportsReasoningSource != llm.SourceCatalog {
		t.Fatalf("round-tripped sources = %#v", capabilities)
	}

	legacy := llm.Model{Source: llm.SourceManual}
	if err := decodeCapabilities([]byte(`{"supports_tools":true,"supports_vision":true}`), &legacy); err != nil {
		t.Fatalf("decode legacy capabilities: %v", err)
	}
	legacyCapabilities := legacy.EffectiveCapabilities()
	if legacyCapabilities.SupportsToolsSource != llm.SourceManual ||
		legacyCapabilities.SupportsVisionSource != llm.SourceManual ||
		legacyCapabilities.SupportsReasoningSource != llm.SourceManual ||
		legacyCapabilities.SupportsPromptCacheSource != llm.SourceManual {
		t.Fatalf("legacy manual inference = %#v", legacyCapabilities)
	}
}
