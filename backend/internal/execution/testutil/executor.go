// Package testutil provides in-memory test doubles for the execution context.
// It is test support code and must never be imported by production packages.
package testutil

import (
	"context"
	"fmt"
	"sync"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/application"
)

// FakeCallKind identifies one recorded fake executor invocation.
type FakeCallKind string

const (
	// FakeCallHealth is a Health call.
	FakeCallHealth FakeCallKind = "health"
	// FakeCallNewContext is a NewContext call.
	FakeCallNewContext FakeCallKind = "new_context"
	// FakeCallAction is an Action call.
	FakeCallAction FakeCallKind = "action"
	// FakeCallCancel is a Cancel call.
	FakeCallCancel FakeCallKind = "cancel"
	// FakeCallClose is a Close call.
	FakeCallClose FakeCallKind = "close"
)

// FakeCall is one recorded invocation of FakeBrowserExecutor. Fields not
// relevant to Kind are zero.
type FakeCall struct {
	Kind   FakeCallKind
	Spec   application.ContextSpec
	Ref    application.ContextRef
	Action application.Action
}

// FakeActionHandler produces an action result when no scripted step is queued.
// It is called without the fake's mutex held, so it may safely call back into the
// fake.
type FakeActionHandler func(ctx context.Context, ref application.ContextRef, action application.Action) (application.ActionResult, error)

// fakeActionStep is one queued scripted action outcome.
type fakeActionStep struct {
	result application.ActionResult
	err    error
}

// FakeBrowserExecutor is an in-memory BrowserExecutor for tests. NewContext
// returns sequential ids ("fake-ctx-1", ...). Action outcomes are scripted FIFO,
// first per context and then globally; an injected action error applies when no
// scripted step is queued, and a handler is the final fallback. Every call is
// recorded, including calls that fail. All methods are safe for concurrent use.
type FakeBrowserExecutor struct {
	mu sync.Mutex

	healthErr     error
	newContextErr error
	actionErr     error
	cancelErr     error
	closeErr      error

	nextContext int
	contexts    map[string]application.ContextSpec

	calls           []FakeCall
	scriptedActions map[string][]fakeActionStep
	globalActions   []fakeActionStep
	actionHandler   FakeActionHandler

	canceledRefs []application.ContextRef
	closedRefs   []application.ContextRef
}

var _ application.BrowserExecutor = (*FakeBrowserExecutor)(nil)

// NewFakeBrowserExecutor returns an empty fake. The zero value is also usable.
func NewFakeBrowserExecutor() *FakeBrowserExecutor {
	return &FakeBrowserExecutor{
		contexts:        make(map[string]application.ContextSpec),
		scriptedActions: make(map[string][]fakeActionStep),
	}
}

// SetHealthError makes Health return err.
func (f *FakeBrowserExecutor) SetHealthError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.healthErr = err
}

// SetNewContextError makes NewContext return err without creating a context.
func (f *FakeBrowserExecutor) SetNewContextError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.newContextErr = err
}

// SetActionError makes Action return err when no scripted step is queued.
func (f *FakeBrowserExecutor) SetActionError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actionErr = err
}

// SetCancelError makes Cancel return err.
func (f *FakeBrowserExecutor) SetCancelError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelErr = err
}

// SetCloseError makes Close return err without forgetting the context.
func (f *FakeBrowserExecutor) SetCloseError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeErr = err
}

// SetActionHandler installs the fallback used when no scripted step is queued
// and no action error is injected. It may be called at any time.
func (f *FakeBrowserExecutor) SetActionHandler(handler FakeActionHandler) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actionHandler = handler
}

// ScriptAction queues result for the next Action call, regardless of context.
func (f *FakeBrowserExecutor) ScriptAction(result application.ActionResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureLocked()
	f.globalActions = append(f.globalActions, fakeActionStep{result: result})
}

// ScriptActionFor queues result for the next Action call on ref.
func (f *FakeBrowserExecutor) ScriptActionFor(ref application.ContextRef, result application.ActionResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureLocked()
	f.scriptedActions[ref.ID] = append(f.scriptedActions[ref.ID], fakeActionStep{result: result})
}

// ScriptActionError queues err for the next Action call, regardless of context.
func (f *FakeBrowserExecutor) ScriptActionError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureLocked()
	f.globalActions = append(f.globalActions, fakeActionStep{err: err})
}

// ScriptActionErrorFor queues err for the next Action call on ref.
func (f *FakeBrowserExecutor) ScriptActionErrorFor(ref application.ContextRef, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureLocked()
	f.scriptedActions[ref.ID] = append(f.scriptedActions[ref.ID], fakeActionStep{err: err})
}

// Health implements application.BrowserExecutor.
func (f *FakeBrowserExecutor) Health(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, FakeCall{Kind: FakeCallHealth})
	return f.healthErr
}

// NewContext implements application.BrowserExecutor.
func (f *FakeBrowserExecutor) NewContext(ctx context.Context, spec application.ContextSpec) (application.ContextRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureLocked()
	f.calls = append(f.calls, FakeCall{Kind: FakeCallNewContext, Spec: spec})
	if f.newContextErr != nil {
		return application.ContextRef{}, f.newContextErr
	}
	f.nextContext++
	ref := application.ContextRef{ID: fmt.Sprintf("fake-ctx-%d", f.nextContext)}
	f.contexts[ref.ID] = spec
	return ref, nil
}

// Action implements application.BrowserExecutor.
func (f *FakeBrowserExecutor) Action(ctx context.Context, ref application.ContextRef, action application.Action) (application.ActionResult, error) {
	if err := ctx.Err(); err != nil {
		return application.ActionResult{}, err
	}
	f.mu.Lock()
	f.calls = append(f.calls, FakeCall{Kind: FakeCallAction, Ref: ref, Action: action})
	var (
		step    fakeActionStep
		hasStep bool
	)
	if queued := f.scriptedActions[ref.ID]; len(queued) > 0 {
		step, hasStep = queued[0], true
		f.scriptedActions[ref.ID] = queued[1:]
	} else if len(f.globalActions) > 0 {
		step, hasStep = f.globalActions[0], true
		f.globalActions = f.globalActions[1:]
	}
	handler := f.actionHandler
	injectedErr := f.actionErr
	f.mu.Unlock()

	if hasStep {
		return step.result, step.err
	}
	if injectedErr != nil {
		return application.ActionResult{}, injectedErr
	}
	if handler != nil {
		return handler(ctx, ref, action)
	}
	return application.ActionResult{}, nil
}

// Cancel implements application.BrowserExecutor.
func (f *FakeBrowserExecutor) Cancel(ctx context.Context, ref application.ContextRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, FakeCall{Kind: FakeCallCancel, Ref: ref})
	f.canceledRefs = append(f.canceledRefs, ref)
	return f.cancelErr
}

// Close implements application.BrowserExecutor. A successful Close forgets the
// context; an injected error leaves it tracked.
func (f *FakeBrowserExecutor) Close(ctx context.Context, ref application.ContextRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureLocked()
	f.calls = append(f.calls, FakeCall{Kind: FakeCallClose, Ref: ref})
	f.closedRefs = append(f.closedRefs, ref)
	if f.closeErr != nil {
		return f.closeErr
	}
	delete(f.contexts, ref.ID)
	return nil
}

// Calls returns a copy of every recorded call in invocation order.
func (f *FakeBrowserExecutor) Calls() []FakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]FakeCall(nil), f.calls...)
}

// Contexts returns a copy of the live context specs keyed by context id.
func (f *FakeBrowserExecutor) Contexts() map[string]application.ContextSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	contexts := make(map[string]application.ContextSpec, len(f.contexts))
	for id, spec := range f.contexts {
		contexts[id] = spec
	}
	return contexts
}

// Canceled returns a copy of the references passed to Cancel in call order.
func (f *FakeBrowserExecutor) Canceled() []application.ContextRef {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]application.ContextRef(nil), f.canceledRefs...)
}

// Closed returns a copy of the references passed to Close in call order.
func (f *FakeBrowserExecutor) Closed() []application.ContextRef {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]application.ContextRef(nil), f.closedRefs...)
}

// ensureLocked initializes the maps of a zero-value fake.
func (f *FakeBrowserExecutor) ensureLocked() {
	if f.contexts == nil {
		f.contexts = make(map[string]application.ContextSpec)
	}
	if f.scriptedActions == nil {
		f.scriptedActions = make(map[string][]fakeActionStep)
	}
}
