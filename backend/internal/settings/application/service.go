package settingsapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"sync"

	"github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	"github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
	"github.com/EdouardRichard/EzTest/backend/internal/settings/domain"
)

// Service reads settings from a hot cache and persists typed updates.
type Service struct {
	repo        settings.Repository
	definitions []settings.Definition
	byKey       map[string]settings.Definition
	ids         id.Generator
	clock       clock.Clock
	audit       auditapp.Recorder
	events      eventingapp.Publisher

	mu     sync.RWMutex
	cache  map[string]settings.Setting
	loaded bool
}

// New wires a settings service. audit and events may be nil in unit tests.
func New(
	repo settings.Repository,
	definitions []settings.Definition,
	ids id.Generator,
	clk clock.Clock,
	audit auditapp.Recorder,
	events eventingapp.Publisher,
) *Service {
	byKey := make(map[string]settings.Definition, len(definitions))
	for _, def := range definitions {
		byKey[def.Key] = def
	}
	return &Service{
		repo:        repo,
		definitions: definitions,
		byKey:       byKey,
		ids:         ids,
		clock:       clk,
		audit:       audit,
		events:      events,
	}
}

// SeedDefaults inserts missing catalog rows and loads the cache. It is
// idempotent and never overwrites an existing value.
func (s *Service) SeedDefaults(ctx context.Context) error {
	if err := s.repo.Seed(ctx, s.definitions); err != nil {
		return err
	}
	return s.Reload(ctx)
}

// Reload refreshes the hot cache from the repository.
func (s *Service) Reload(ctx context.Context) error {
	if s.repo == nil {
		return platform.Unavailable("settings repository is not configured")
	}
	list, err := s.repo.List(ctx)
	if err != nil {
		return err
	}
	cache := make(map[string]settings.Setting, len(list))
	for _, item := range list {
		cache[item.Key] = item
	}
	s.mu.Lock()
	s.cache = cache
	s.loaded = true
	s.mu.Unlock()
	return nil
}

// List returns every setting in key order.
func (s *Service) List(ctx context.Context) ([]settings.Setting, error) {
	if err := s.ensureLoaded(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	list := make([]settings.Setting, 0, len(s.cache))
	for _, item := range s.cache {
		list = append(list, item)
	}
	s.mu.RUnlock()
	sort.Slice(list, func(i, j int) bool { return list[i].Key < list[j].Key })
	return list, nil
}

// Get returns one setting or platform.NotFound.
func (s *Service) Get(ctx context.Context, key string) (settings.Setting, error) {
	if err := s.ensureLoaded(ctx); err != nil {
		return settings.Setting{}, err
	}
	s.mu.RLock()
	item, ok := s.cache[key]
	s.mu.RUnlock()
	if !ok {
		return settings.Setting{}, platform.NotFound("setting")
	}
	return item, nil
}

// Update validates a value against the catalog, writes it with a revision,
// refreshes the cache and emits audit + event records.
func (s *Service) Update(ctx context.Context, key string, value any, actorID int64) (settings.Setting, error) {
	def, ok := s.byKey[key]
	if !ok {
		return settings.Setting{}, platform.NotFound("setting")
	}
	encoded, err := encodeValue(def, value)
	if err != nil {
		return settings.Setting{}, err
	}

	at := s.clock.Now()
	updated, err := s.repo.Update(ctx, key, encoded, s.ids.Next(), actorID, at)
	if err != nil {
		return settings.Setting{}, err
	}

	s.mu.Lock()
	if s.cache == nil {
		s.cache = map[string]settings.Setting{}
	}
	s.cache[key] = updated
	s.loaded = true
	s.mu.Unlock()

	requestID := requestid.From(ctx)
	if s.audit != nil {
		_ = s.audit.Record(ctx, auditapp.Event{
			ActorID:    &actorID,
			Action:     "settings.updated",
			TargetKind: "setting",
			TargetID:   key,
			RequestID:  requestID,
			Metadata:   map[string]any{"key": key, "category": def.Category},
		})
	}
	if s.events != nil {
		_ = s.events.Publish(ctx, eventingapp.Event{
			Channel:   "system",
			Type:      "settings.updated",
			Scope:     map[string]any{},
			Payload:   map[string]any{"key": key, "category": def.Category},
			ActorID:   &actorID,
			RequestID: requestID,
		})
	}
	return updated, nil
}

// Revisions returns append-only change history for one key.
func (s *Service) Revisions(ctx context.Context, key string, limit int, beforeID *int64) ([]settings.Revision, error) {
	if _, ok := s.byKey[key]; !ok {
		return nil, platform.NotFound("setting")
	}
	if limit <= 0 {
		limit = 20
	}
	return s.repo.Revisions(ctx, key, limit, beforeID)
}

// String returns a typed string value.
func (s *Service) String(ctx context.Context, key string) (string, error) {
	item, err := s.Get(ctx, key)
	if err != nil {
		return "", err
	}
	var value string
	if err := json.Unmarshal(item.Value, &value); err != nil {
		return "", fmt.Errorf("setting %s is not a string: %w", key, err)
	}
	return value, nil
}

// Int returns a typed integer value.
func (s *Service) Int(ctx context.Context, key string) (int64, error) {
	item, err := s.Get(ctx, key)
	if err != nil {
		return 0, err
	}
	var value int64
	if err := json.Unmarshal(item.Value, &value); err != nil {
		return 0, fmt.Errorf("setting %s is not an integer: %w", key, err)
	}
	return value, nil
}

// Float returns a typed numeric value (integers are accepted and widened).
func (s *Service) Float(ctx context.Context, key string) (float64, error) {
	item, err := s.Get(ctx, key)
	if err != nil {
		return 0, err
	}
	var value float64
	if err := json.Unmarshal(item.Value, &value); err != nil {
		return 0, fmt.Errorf("setting %s is not a number: %w", key, err)
	}
	return value, nil
}

// Bool returns a typed boolean value.
func (s *Service) Bool(ctx context.Context, key string) (bool, error) {
	item, err := s.Get(ctx, key)
	if err != nil {
		return false, err
	}
	var value bool
	if err := json.Unmarshal(item.Value, &value); err != nil {
		return false, fmt.Errorf("setting %s is not a boolean: %w", key, err)
	}
	return value, nil
}

func (s *Service) ensureLoaded(ctx context.Context) error {
	s.mu.RLock()
	loaded := s.loaded
	s.mu.RUnlock()
	if loaded {
		return nil
	}
	return s.Reload(ctx)
}

func encodeValue(def settings.Definition, value any) (json.RawMessage, error) {
	if value == nil {
		return nil, platform.Invalid(def.Key, "value must not be null")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, platform.Invalid(def.Key, "value must be JSON-encodable")
	}

	switch def.Type {
	case settings.TypeString:
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, platform.Invalid(def.Key, "value must be a string")
		}
		return raw, nil
	case settings.TypeBool:
		var flag bool
		if err := json.Unmarshal(raw, &flag); err != nil {
			return nil, platform.Invalid(def.Key, "value must be a boolean")
		}
		return raw, nil
	case settings.TypeNumber:
		number, err := decodeNumber(raw)
		if err != nil {
			return nil, platform.Invalid(def.Key, "value must be a number")
		}
		if def.Integer && number != math.Trunc(number) {
			return nil, platform.Invalid(def.Key, "value must be an integer")
		}
		if def.Min != nil && number < *def.Min {
			return nil, platform.Invalid(def.Key, "value must be at least "+formatFloat(*def.Min))
		}
		if def.Max != nil && number > *def.Max {
			return nil, platform.Invalid(def.Key, "value must be at most "+formatFloat(*def.Max))
		}
		return raw, nil
	case settings.TypeJSON:
		return raw, nil
	default:
		return nil, platform.NewError(500, "internal", "unknown setting type")
	}
}

func decodeNumber(raw json.RawMessage) (float64, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return 0, err
	}
	number, ok := value.(json.Number)
	if !ok {
		return 0, errors.New("not a number")
	}
	return number.Float64()
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}
