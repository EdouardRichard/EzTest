// Package resource adapts the resource context's session and catalog services
// to the execution context's SessionPort (spec §4.2, §5.9). Acquire internally
// performs selection, auto-login and takeover creation; execution never drives
// the session state machine. Errors are translated into the execution context's
// sentinels and typed reasons so callers never import resource errors. No
// resource infrastructure is imported.
package resource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	executiondomain "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	resourcedomain "github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

const (
	// takeoverPageSize bounds one page of the takeover-id lookup.
	takeoverPageSize = 100
	// maxTakeoverPages bounds the takeover-id lookup so a busy resource context
	// cannot stall an acquisition indefinitely.
	maxTakeoverPages = 20
)

// errNotConfigured is returned when a required dependency is missing.
var errNotConfigured = errors.New("execution: resource adapter is not configured")

// errTakeoverNotFound reports that a waiting takeover id could not be resolved.
var errTakeoverNotFound = errors.New("execution: waiting takeover not found")

// Sessions is the narrow resource session application slice the adapter needs.
// *resource/application.SessionService satisfies it.
type Sessions interface {
	AcquireForExecution(ctx context.Context, environmentID, systemID int64, holder string, ttl time.Duration) (resourceapp.AcquiredSession, error)
	ReleaseLease(ctx context.Context, sessionID int64, holder, reason string) error
	MarkInvalid(ctx context.Context, sessionID int64, reason string) error
	Get(ctx context.Context, id int64) (*resourcedomain.Session, *resourcedomain.Snapshot, *resourcedomain.Lease, *resourcedomain.Takeover, error)
	ListTakeovers(ctx context.Context, filter resourcedomain.TakeoverFilter) ([]resourcedomain.Takeover, bool, error)
}

// Catalog is the narrow resource catalog slice the adapter needs.
// *resource/application.CatalogService satisfies it.
type Catalog interface {
	GetEnvironment(ctx context.Context, id int64) (*resourcedomain.Environment, error)
	GetSystem(ctx context.Context, id int64) (*resourcedomain.System, error)
}

// Adapter implements application.SessionPort over the resource services.
type Adapter struct {
	sessions Sessions
	catalog  Catalog
}

// New returns the adapter. Missing dependencies fail closed on the call that
// needs them.
func New(sessions Sessions, catalog Catalog) *Adapter {
	return &Adapter{sessions: sessions, catalog: catalog}
}

var _ executionapp.SessionPort = (*Adapter)(nil)

// The production resource services must keep satisfying the adapter contracts; a
// signature drift fails the build here rather than at the composition root.
var (
	_ Sessions = (*resourceapp.SessionService)(nil)
	_ Catalog  = (*resourceapp.CatalogService)(nil)
)

// SessionUnavailableError is the typed session-unavailable failure. It unwraps
// to application.ErrSessionUnavailable so the scheduler blocks the case as
// blocked(session), and carries the resource-side reason for diagnostics.
type SessionUnavailableError struct {
	Reason string
}

// Error implements error without ever including a credential.
func (e *SessionUnavailableError) Error() string {
	if e == nil {
		return ""
	}
	if e.Reason == "" {
		return "execution: no session available"
	}
	return "execution: no session available (" + e.Reason + ")"
}

// Unwrap exposes the scheduler sentinel.
func (e *SessionUnavailableError) Unwrap() error { return executionapp.ErrSessionUnavailable }

// NeedsManualError is the typed manual-takeover failure. It carries the waiting
// takeover so the scheduler can surface the gate, and unwraps to
// application.ErrSessionNeedsManual.
type NeedsManualError struct {
	Reason   string
	Takeover *executionapp.TakeoverRef
}

// Error implements error without ever including a credential.
func (e *NeedsManualError) Error() string {
	if e == nil {
		return ""
	}
	if e.Reason == "" {
		return "execution: session needs manual takeover"
	}
	return "execution: session needs manual takeover (" + e.Reason + ")"
}

// Unwrap exposes the scheduler sentinel.
func (e *NeedsManualError) Unwrap() error { return executionapp.ErrSessionNeedsManual }

// ErrTakeoverUnavailable reports a needs-manual acquisition whose waiting
// takeover row could not be resolved. Without the waiting session id the
// scheduler cannot poll the gate, so the error unwraps to
// application.ErrSessionUnavailable and the case is blocked(session) without a
// fake takeover ref or a SessionRef.SessionID of 0 standing in for a session.
type ErrTakeoverUnavailable struct {
	// TakeoverID is the waiting takeover reported by the resource context.
	TakeoverID int64
	// Reason is the needs-manual reason reported by the resource context.
	Reason string
	// Cause is the failed lookup, if any; it is nil when the row was absent.
	Cause error
}

// Error implements error without ever including a credential.
func (e *ErrTakeoverUnavailable) Error() string {
	if e == nil {
		return ""
	}
	if e.Reason == "" {
		return fmt.Sprintf("execution: takeover %d is unavailable", e.TakeoverID)
	}
	return fmt.Sprintf("execution: takeover %d is unavailable (%s)", e.TakeoverID, e.Reason)
}

// Unwrap exposes the scheduler sentinel and the lookup failure.
func (e *ErrTakeoverUnavailable) Unwrap() []error {
	if e == nil {
		return nil
	}
	if e.Cause == nil {
		return []error{executionapp.ErrSessionUnavailable}
	}
	return []error{executionapp.ErrSessionUnavailable, e.Cause}
}

// Acquire leases a session of the environment for one execution holder and maps
// it onto SessionRef. A token/apikey/basic session is HTTP-only: its headers are
// exposed for http.request and its token document is never projected as browser
// storage state (spec §5.9).
func (a *Adapter) Acquire(ctx context.Context, environmentID, systemID int64, holder string, ttl time.Duration) (executionapp.SessionRef, error) {
	if a == nil || a.sessions == nil {
		return executionapp.SessionRef{}, errNotConfigured
	}
	acquired, err := a.sessions.AcquireForExecution(ctx, environmentID, systemID, holder, ttl)
	if err != nil {
		return a.mapAcquireError(ctx, err)
	}
	kind := acquired.StorageStateKind
	ref := executionapp.SessionRef{
		SessionID:        acquired.SessionID,
		SnapshotID:       acquired.SnapshotID,
		StorageStateKind: kind,
		Headers:          copyHeaders(acquired.Headers),
		ExpiresAt:        copyTime(acquired.ExpiresAt),
	}
	// A token/apikey/basic session is HTTP-only: its kind is projected so
	// downstream can skip the browser context, and its token document is never
	// exposed as browser state (spec §5.9).
	if kind != resourcedomain.StorageStateKindToken {
		ref.StorageState = copyRaw(acquired.StorageState)
		ref.SessionStorage = copyRaw(acquired.SessionStorage)
	}
	return ref, nil
}

// mapAcquireError translates the resource acquisition contract into the
// execution context's typed session errors.
func (a *Adapter) mapAcquireError(ctx context.Context, err error) (executionapp.SessionRef, error) {
	var needsManual *resourceapp.ErrNeedsManual
	if errors.As(err, &needsManual) {
		takeover, lookupErr := a.lookupTakeover(ctx, needsManual.TakeoverID)
		if lookupErr != nil {
			return executionapp.SessionRef{}, &ErrTakeoverUnavailable{
				TakeoverID: needsManual.TakeoverID,
				Reason:     needsManual.Reason,
				Cause:      lookupErr,
			}
		}
		if takeover.Reason == "" {
			takeover.Reason = needsManual.Reason
		}
		return executionapp.SessionRef{SessionID: takeover.SessionID},
			&NeedsManualError{Reason: needsManual.Reason, Takeover: takeover}
	}

	var unavailable *resourceapp.ErrSessionUnavailable
	if errors.As(err, &unavailable) {
		return executionapp.SessionRef{}, &SessionUnavailableError{Reason: unavailable.Reason}
	}

	var noSession *resourceapp.NoSessionError
	if errors.As(err, &noSession) {
		return executionapp.SessionRef{}, &SessionUnavailableError{Reason: noSession.Reason}
	}

	// Every other acquisition failure is still a session failure, not
	// infrastructure: route a resource-domain error through mapResourceError for
	// its stable reason and classify it as session-unavailable so the scheduler
	// blocks the case instead of retrying it as infra.
	reason := "internal"
	var reasonErr *executiondomain.ReasonError
	if mapped := mapResourceError(err); errors.As(mapped, &reasonErr) {
		reason = reasonErr.Reason
		if reason == "" {
			reason = reasonErr.Code
		}
	}
	return executionapp.SessionRef{}, &SessionUnavailableError{Reason: reason}
}

// lookupTakeover resolves the waiting takeover row by id so the waiting session
// id and its UI hints reach the scheduler gate. Rows are ordered by increasing
// id, so the scan stops as soon as a row exceeds the target; the page bound only
// protects against a broken cursor. A lookup failure is reported so the caller
// can classify the case without inventing a takeover ref.
func (a *Adapter) lookupTakeover(ctx context.Context, takeoverID int64) (*executionapp.TakeoverRef, error) {
	if takeoverID <= 0 || a.sessions == nil {
		return nil, errTakeoverNotFound
	}
	afterID := int64(0)
	for page := 0; page < maxTakeoverPages; page++ {
		takeovers, more, err := a.sessions.ListTakeovers(ctx, resourcedomain.TakeoverFilter{
			Status: resourcedomain.TakeoverStatusWaiting,
			Page:   resourcedomain.Page{AfterID: afterID, Limit: takeoverPageSize},
		})
		if err != nil {
			return nil, err
		}
		for _, takeover := range takeovers {
			if takeover.ID == takeoverID {
				return mapTakeover(takeover), nil
			}
			if takeover.ID > takeoverID {
				return nil, errTakeoverNotFound
			}
		}
		if !more || len(takeovers) == 0 {
			return nil, errTakeoverNotFound
		}
		next := takeovers[len(takeovers)-1].ID
		if next <= afterID {
			return nil, errTakeoverNotFound
		}
		afterID = next
	}
	return nil, errTakeoverNotFound
}

// Release releases one session lease.
func (a *Adapter) Release(ctx context.Context, sessionID int64, holder, reason string) error {
	if a == nil || a.sessions == nil {
		return errNotConfigured
	}
	return mapResourceError(a.sessions.ReleaseLease(ctx, sessionID, holder, reason))
}

// MarkInvalid marks a session invalid and releases its leases.
func (a *Adapter) MarkInvalid(ctx context.Context, sessionID int64, reason string) error {
	if a == nil || a.sessions == nil {
		return errNotConfigured
	}
	return mapResourceError(a.sessions.MarkInvalid(ctx, sessionID, reason))
}

// PendingTakeover returns the waiting takeover of a session, or nil when none is
// waiting. It uses the session's latest takeover relation; a missing session is
// reported as no takeover.
func (a *Adapter) PendingTakeover(ctx context.Context, sessionID int64) (*executionapp.TakeoverRef, error) {
	if a == nil || a.sessions == nil {
		return nil, errNotConfigured
	}
	_, _, _, takeover, err := a.sessions.Get(ctx, sessionID)
	if err != nil {
		if isResourceNotFound(err) {
			return nil, nil
		}
		return nil, mapResourceError(err)
	}
	if takeover == nil || takeover.Status != resourcedomain.TakeoverStatusWaiting {
		return nil, nil
	}
	return mapTakeover(*takeover), nil
}

// Environment returns the environment projection used by a case run.
func (a *Adapter) Environment(ctx context.Context, environmentID int64) (executionapp.EnvironmentRef, error) {
	if a == nil || a.catalog == nil {
		return executionapp.EnvironmentRef{}, errNotConfigured
	}
	environment, err := a.catalog.GetEnvironment(ctx, environmentID)
	if err != nil {
		return executionapp.EnvironmentRef{}, mapResourceError(err)
	}
	if environment == nil {
		return executionapp.EnvironmentRef{}, errors.New("execution: resource environment is unavailable")
	}
	return executionapp.EnvironmentRef{
		ID:           environment.ID,
		SystemID:     environment.SystemID,
		Name:         environment.Name,
		BaseURL:      environment.BaseURL,
		AllowedHosts: append([]string(nil), environment.AllowedHosts...),
	}, nil
}

// mapTakeover projects one resource takeover row.
func mapTakeover(takeover resourcedomain.Takeover) *executionapp.TakeoverRef {
	ref := &executionapp.TakeoverRef{
		ID:           takeover.ID,
		Reason:       takeover.Reason,
		Instructions: copyRaw(takeover.Instructions),
		ExpiresAt:    copyTime(takeover.ExpiresAt),
	}
	if takeover.SessionID != nil {
		ref.SessionID = *takeover.SessionID
	}
	return ref
}

// mapResourceError translates a resource domain error into the execution
// context's ReasonError. Unknown errors are returned unchanged.
func mapResourceError(err error) error {
	if err == nil {
		return nil
	}
	var domainErr *resourcedomain.Error
	if !errors.As(err, &domainErr) {
		return err
	}
	switch domainErr.Kind {
	case resourcedomain.KindInvalid:
		return executiondomain.NewReasonError("invalid_argument", domainErr.Reason, domainErr.Message, false, nil)
	case resourcedomain.KindNotFound:
		return executiondomain.NewReasonError("not_found", "not_found", domainErr.Message, false, nil)
	default:
		reason := domainErr.Reason
		if reason == "" {
			reason = string(domainErr.Kind)
		}
		return executiondomain.NewReasonError(reason, reason, domainErr.Message, false, nil)
	}
}

// isResourceNotFound reports whether err is a resource not-found error.
func isResourceNotFound(err error) bool {
	var domainErr *resourcedomain.Error
	return errors.As(err, &domainErr) && domainErr.Kind == resourcedomain.KindNotFound
}

// copyRaw returns an owned copy of a raw JSON document.
func copyRaw(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

// copyHeaders returns an owned copy of an HTTP header bundle.
func copyHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	copied := make(map[string]string, len(headers))
	for name, value := range headers {
		copied[name] = value
	}
	return copied
}

// copyTime returns an owned copy of an optional timestamp.
func copyTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
