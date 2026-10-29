package llm

import "context"

// Repository is the persistence port for LLM configurations. Implementations
// encrypt APIKey and Headers at rest and return them decrypted for internal
// use; no HTTP layer may expose the plaintext.
type Repository interface {
	// Create inserts a configuration. When IsDefault is set it clears the
	// previous default in the same transaction.
	Create(ctx context.Context, params CreateParams) (Config, error)
	// Update applies a partial update. When IsDefault becomes true it clears
	// the previous default in the same transaction.
	Update(ctx context.Context, id int64, params UpdateParams) (Config, error)
	// Delete removes a configuration.
	Delete(ctx context.Context, id int64) error
	// Get returns one configuration by ID, decrypted.
	Get(ctx context.Context, id int64) (Config, error)
	// List returns every configuration ordered by ID, decrypted.
	List(ctx context.Context) ([]Config, error)
	// GetDefault returns the configuration marked as default. It returns
	// ErrConfigNotFound when none is configured.
	GetDefault(ctx context.Context) (Config, error)
}
