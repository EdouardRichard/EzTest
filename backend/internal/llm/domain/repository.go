package llm

import (
	"context"
	"errors"
	"time"
)

// Repository persistence errors. Adapters translate unique-violation and
// foreign-key failures into these sentinels; the application maps them to
// platform 404/409 errors.
var (
	ErrConfigNotFound = errors.New("llm config not found")
	ErrNameTaken      = errors.New("llm config name already exists")
	ErrModelNotFound  = errors.New("llm model not found")
	ErrModelTaken     = errors.New("llm model already exists for this config")
)

// Repository stores LLM configurations and their model catalog.
//
// CreateConfig and UpdateConfig must enforce "at most one default config"
// atomically: when the incoming config has IsDefault=true, every other default
// row is cleared in the same transaction.
type Repository interface {
	ListConfigs(ctx context.Context) ([]Config, error)
	GetConfig(ctx context.Context, id int64) (Config, error)
	CreateConfig(ctx context.Context, config Config) (Config, error)
	UpdateConfig(ctx context.Context, config Config) (Config, error)
	DeleteConfig(ctx context.Context, id int64) error

	ListModels(ctx context.Context) ([]Model, error)
	ListModelsByConfig(ctx context.Context, configID int64) ([]Model, error)
	// UpsertModels inserts new models and updates existing rows matched by
	// (config_id, model), preserving the original ID and created_at. It returns
	// every model of the config in ID order.
	UpsertModels(ctx context.Context, configID int64, models []Model, at time.Time) ([]Model, error)
	InsertModel(ctx context.Context, model Model) (Model, error)
	DeleteModel(ctx context.Context, id int64) error
}
