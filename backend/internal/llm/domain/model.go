package llm

import "time"

// Model catalog sources and statuses frozen by the P0 schema.
const (
	SourceCatalog = "catalog"
	SourceManual  = "manual"

	ModelStatusActive   = "active"
	ModelStatusDisabled = "disabled"
)

// Capabilities is the 0.2.1 model-capability set (spec §7.3). SupportsTools,
// SupportsVision and ContextWindow mirror the legacy llm_model columns;
// SupportsReasoning and SupportsPromptCache are stored in the capabilities
// JSONB object. Readers merge both sources.
//
// The Supports*Source fields record where each boolean capability came from
// (catalog refresh or an administrator edit, see S-02). They live in the same
// JSONB object as the values, so no migration column is needed; a refresh keeps
// a manual value and only updates a catalog-sourced one. Rows written before
// these keys existed infer the source from Model.Source (manual rows read as
// manual, catalog rows as catalog). That inference is a one-time compatibility
// caveat: a pre-0.2.3 catalog row whose capability was edited by hand cannot be
// distinguished from an untouched one and is treated as catalog on the next
// refresh. Editing the capability through the 0.2.3 editor records the manual
// source and is preserved from then on.
//
// CatalogContextWindow/CatalogMaxOutput keep the most recent upstream values of
// the two numeric parameters inside the same JSONB object (A §4.4). They are
// written on every catalog refresh so "reset to upstream" still works when an
// administrator has overridden the legacy columns; no dedicated migration
// columns are added (llm_0004 only carries the source markers).
type Capabilities struct {
	SupportsTools       bool `json:"supports_tools"`
	SupportsVision      bool `json:"supports_vision"`
	SupportsReasoning   bool `json:"supports_reasoning"`
	SupportsPromptCache bool `json:"supports_prompt_cache"`
	// Capability provenance. Values are SourceCatalog or SourceManual; empty
	// values are normalized by EffectiveCapabilities.
	SupportsToolsSource       string `json:"supports_tools_source,omitempty"`
	SupportsVisionSource      string `json:"supports_vision_source,omitempty"`
	SupportsReasoningSource   string `json:"supports_reasoning_source,omitempty"`
	SupportsPromptCacheSource string `json:"supports_prompt_cache_source,omitempty"`
	ContextWindow             int    `json:"context_window"`
	CatalogContextWindow      int    `json:"catalog_context_window,omitempty"`
	CatalogMaxOutput          int    `json:"catalog_max_output,omitempty"`
}

// Model is one model exposed by an LLM configuration. Catalog rows come from
// GET {base}/models; manual rows are entered by an administrator. Deprecated
// catalog rows are kept for display but never selected for a run.
type Model struct {
	ID             int64
	ConfigID       int64
	Model          string
	DisplayName    string
	ContextWindow  int
	MaxOutput      int
	SupportsTools  bool
	SupportsVision bool
	Capabilities   Capabilities
	Deprecated     bool
	Source         string
	// WindowSource/OutputSource record where ContextWindow/MaxOutput came from:
	// SourceCatalog (upstream, refresh may update it) or SourceManual (an
	// administrator set it, refresh must preserve it). See llm_0004 and A §4.2.
	WindowSource string
	OutputSource string
	// MaxInput is a derived, read-only view value (effective window minus
	// effective output). It is never persisted; the application layer fills it
	// before returning a model to the HTTP layer.
	MaxInput  int64
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// EffectiveCapabilities merges the structured capability set with the legacy
// boolean/context columns so rows written before the capabilities JSONB existed
// and test fixtures that only set the legacy fields still validate correctly.
func (m Model) EffectiveCapabilities() Capabilities {
	capabilities := m.Capabilities
	capabilities.SupportsTools = capabilities.SupportsTools || m.SupportsTools
	capabilities.SupportsVision = capabilities.SupportsVision || m.SupportsVision
	if capabilities.ContextWindow <= 0 {
		capabilities.ContextWindow = m.ContextWindow
	}
	// Legacy rows carry no capability source keys. A row that was created as a
	// manual model is the only safe manual evidence we have; everything else
	// reads as catalog (documented one-time caveat above).
	capabilities.SupportsToolsSource = effectiveCapabilitySource(capabilities.SupportsToolsSource, m.Source)
	capabilities.SupportsVisionSource = effectiveCapabilitySource(capabilities.SupportsVisionSource, m.Source)
	capabilities.SupportsReasoningSource = effectiveCapabilitySource(capabilities.SupportsReasoningSource, m.Source)
	capabilities.SupportsPromptCacheSource = effectiveCapabilitySource(capabilities.SupportsPromptCacheSource, m.Source)
	return capabilities
}

// effectiveCapabilitySource normalizes a capability source marker. An explicit
// catalog/manual value wins; a missing marker falls back to the model's origin
// so pre-0.2.3 rows keep a sensible provenance.
func effectiveCapabilitySource(source, modelSource string) string {
	switch source {
	case SourceManual:
		return SourceManual
	case SourceCatalog:
		return SourceCatalog
	}
	if modelSource == SourceManual {
		return SourceManual
	}
	return SourceCatalog
}

// EffectiveWindowSource returns the stored provenance of ContextWindow. Rows
// written before llm_0004 (or fixtures that never set the field) read as
// catalog, which is also the migration default.
func (m Model) EffectiveWindowSource() string {
	if m.WindowSource == "" {
		return SourceCatalog
	}
	return m.WindowSource
}

// EffectiveOutputSource returns the stored provenance of MaxOutput; see
// EffectiveWindowSource.
func (m Model) EffectiveOutputSource() string {
	if m.OutputSource == "" {
		return SourceCatalog
	}
	return m.OutputSource
}

// Selectable reports whether the model may be resolved for a run: deprecated
// and disabled rows are excluded from default/catalog selection.
func (m Model) Selectable() bool {
	return !m.Deprecated && m.Status != ModelStatusDisabled
}
