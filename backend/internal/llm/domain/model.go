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
type Capabilities struct {
	SupportsTools       bool `json:"supports_tools"`
	SupportsVision      bool `json:"supports_vision"`
	SupportsReasoning   bool `json:"supports_reasoning"`
	SupportsPromptCache bool `json:"supports_prompt_cache"`
	ContextWindow       int  `json:"context_window"`
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
	Status         string
	CreatedAt      time.Time
	UpdatedAt      time.Time
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
	return capabilities
}

// Selectable reports whether the model may be resolved for a run: deprecated
// and disabled rows are excluded from default/catalog selection.
func (m Model) Selectable() bool {
	return !m.Deprecated && m.Status != ModelStatusDisabled
}
