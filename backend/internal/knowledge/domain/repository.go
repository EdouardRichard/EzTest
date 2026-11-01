package knowledge

import "context"

// Filter narrows one knowledge list page. Empty string fields mean "no
// filter". BeforeID is the exclusive id-desc cursor; Limit is the requested
// page size (the application asks for one row more than it returns).
type Filter struct {
	Scope     Scope
	Status    Status
	SystemKey string
	Tag       string
	Query     string
	BeforeID  int64
	Limit     int
}

// InjectionQuery selects approved knowledge for one prompt injection. The
// adapter must return only status=approved rows whose scope is global or whose
// system_key is in SystemKeys, ordered by ts_rank desc then id desc.
type InjectionQuery struct {
	SystemKeys []string
	Query      string
	Limit      int
}

// Repository persists and reads knowledge_item/knowledge_revision rows. It is
// implemented by infrastructure/postgres; missing items are reported as
// ErrNotFound and stale expected revision/status as ErrStateConflict.
type Repository interface {
	// Insert writes the item and its initial revision in one transaction.
	Insert(ctx context.Context, item Item, revision Revision) error
	// Get returns one item without source-staleness decoration.
	Get(ctx context.Context, id int64) (Item, error)
	// List returns up to Filter.Limit rows ordered id DESC (ts_rank DESC first
	// when Filter.Query is set).
	List(ctx context.Context, filter Filter) ([]Item, error)
	// Update writes item content, its next revision and the append-only
	// revision row in one transaction. item.Revision is the new revision; the
	// adapter only succeeds when the stored revision is item.Revision-1.
	Update(ctx context.Context, item Item, revision Revision) error
	// UpdateStatus changes the review columns when the stored status still
	// equals from.
	UpdateStatus(ctx context.Context, item Item, from Status) error
	// ListRevisions returns the append-only history ordered revision DESC.
	ListRevisions(ctx context.Context, knowledgeID int64) ([]Revision, error)
	// ListForInjection returns approved candidate rows ordered by ts_rank desc
	// then id desc.
	ListForInjection(ctx context.Context, query InjectionQuery) ([]Item, error)
}
