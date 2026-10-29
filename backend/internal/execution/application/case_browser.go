package application

import "context"

// CaseBrowserController owns the per-case browser context on behalf of the case
// runner. The execution agent tools create the executor context lazily and keep
// one context per case run, so the runner must not create a second one. On an
// immediate pause the runner asks the controller to abort the in-flight browser
// action; on every teardown path it asks the controller to close and forget the
// context. Implementations act on the tools' per-case registry; a run for which
// no context was ever created is a harmless no-op.
type CaseBrowserController interface {
	// CancelCase aborts the in-flight browser action of the case run, if any.
	// It keeps the context itself alive; CloseCase still owns its disposal.
	CancelCase(ctx context.Context, caseRunID int64) error
	// CloseCase disposes the case run's browser context and forgets it. It
	// must be safe to call twice and when no context was ever created.
	CloseCase(ctx context.Context, caseRunID int64) error
}
