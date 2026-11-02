package llm

import "time"

// Model catalog sources and statuses frozen by the P0 schema.
const (
	SourceCatalog = "catalog"
	SourceManual  = "manual"

	ModelStatusActive   = "active"
	ModelStatusDisabled = "disabled"
)

// Model is one model exposed by an LLM configuration. Catalog rows come from
// GET {base}/models; manual rows are entered by an administrator.
type Model struct {
	ID             int64
	ConfigID       int64
	Model          string
	DisplayName    string
	ContextWindow  int
	MaxOutput      int
	SupportsTools  bool
	SupportsVision bool
	Source         string
	Status         string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}
