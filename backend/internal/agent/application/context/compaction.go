package agentcontext

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
)

// Compaction formula defaults from spec R-A5-1 / A §6.1. The ratios mirror the
// seeded compaction settings (compaction.threshold_ratio=0.80,
// compaction.retain_ratio=0.16, compaction.summary_max_tokens=8192); callers
// that load settings override the policy fields and then call Validate.
const (
	// DefaultCompactionWindowTokens is the global fallback when the effective
	// model window is unknown (R-A1-5: 0 → 128000).
	DefaultCompactionWindowTokens = 128_000
	// DefaultCompactionOutputTokens is the output reserve when max_output is
	// unset.
	DefaultCompactionOutputTokens = 4_096
	// MaxCompactionOutputTokens caps the output reserve at 16000 tokens.
	MaxCompactionOutputTokens = 16_000
	// MaxCompactionSummaryTokens caps the summary output at 8192 tokens.
	MaxCompactionSummaryTokens = 8_192
	// DefaultCompactionTTL is the R-A5-10 stale-transaction TTL.
	DefaultCompactionTTL = 10 * time.Minute
	// DefaultCompactionOverflowRetries is the R-A5-2 overflow retry budget.
	DefaultCompactionOverflowRetries = 1
	// MaxCompactionOverflowRetries is the spec cap: overflow retries once.
	MaxCompactionOverflowRetries = 1
)

const (
	compactionTriggerPercent        = 80
	compactionRetainPercent         = 16
	compactionOverflowRetainPercent = 8
	compactionSummaryPercent        = 4
)

// ErrInvalidCompactionPolicy reports a policy whose retain/trigger/usable
// ordering violates R-A5-1. Loading an invalid policy is a configuration error,
// not a runtime compaction failure.
var ErrInvalidCompactionPolicy = errors.New("agentcontext: invalid compaction policy")

// CompactionPolicyError is the typed configuration error of Validate. Field
// names the offending budget so a settings loader can report it verbatim.
type CompactionPolicyError struct {
	Field   string
	Message string
}

func (e *CompactionPolicyError) Error() string {
	if e == nil {
		return ErrInvalidCompactionPolicy.Error()
	}
	return fmt.Sprintf("%s: %s %s", ErrInvalidCompactionPolicy, e.Field, e.Message)
}

// Unwrap lets errors.Is(err, ErrInvalidCompactionPolicy) match every policy
// validation failure.
func (e *CompactionPolicyError) Unwrap() error { return ErrInvalidCompactionPolicy }

func invalidCompactionPolicy(field, message string) error {
	return &CompactionPolicyError{Field: field, Message: message}
}

// CompactionPolicy is the resolved token budget of one model (R-A5-1):
//
//	W = effective_context_window(model)       0 → 128000
//	O = min(max_output or 4096, 16000)
//	usable = W - O
//	trigger = floor(0.80 * usable)
//	retain = floor(0.16 * usable)
//	summary_max = min(floor(0.04 * usable), 8192)
//
// The struct is comparable; a copy can be carried through a decision or a
// transaction without retaining mutable state.
type CompactionPolicy struct {
	Window     int
	Output     int
	Usable     int
	Trigger    int
	Retain     int
	SummaryMax int
}

// NewCompactionPolicy derives the v0.22 compaction policy from the effective
// model window and output reserve. A window of 0 (unknown model) falls back to
// 128000; a missing output falls back to 4096 and is capped at 16000. The
// constructor never fails: it normalizes the inputs, and Validate reports an
// impossible window/output pair (for example a window smaller than the reserve).
func NewCompactionPolicy(window, output int) CompactionPolicy {
	if window <= 0 {
		window = DefaultCompactionWindowTokens
	}
	if output <= 0 {
		output = DefaultCompactionOutputTokens
	}
	if output > MaxCompactionOutputTokens {
		output = MaxCompactionOutputTokens
	}
	usable := window - output
	if usable < 0 {
		usable = 0
	}
	summaryMax := usable * compactionSummaryPercent / 100
	if summaryMax > MaxCompactionSummaryTokens {
		summaryMax = MaxCompactionSummaryTokens
	}
	return CompactionPolicy{
		Window:     window,
		Output:     output,
		Usable:     usable,
		Trigger:    usable * compactionTriggerPercent / 100,
		Retain:     usable * compactionRetainPercent / 100,
		SummaryMax: summaryMax,
	}
}

// Validate enforces the spec's load-time contract retain < trigger < usable
// (R-A5-1), plus the explicit v0.22 guards that every budget is positive. The
// summary_max > 0 guard is deliberately explicit: the formula always derives a
// positive summary budget for a valid window, but a settings override must not
// be able to disable the summary cap.
func (p CompactionPolicy) Validate() error {
	switch {
	case p.Window <= 0:
		return invalidCompactionPolicy("window", "must be positive")
	case p.Output <= 0:
		return invalidCompactionPolicy("output", "must be positive")
	case p.Usable <= 0:
		return invalidCompactionPolicy("usable", "must be positive")
	case p.SummaryMax <= 0:
		// Explicit v0.22 guard (see the Validate doc).
		return invalidCompactionPolicy("summary_max", "must be positive")
	case p.Retain < 0:
		return invalidCompactionPolicy("retain", "must not be negative")
	case p.Trigger <= 0:
		return invalidCompactionPolicy("trigger", "must be positive")
	}
	if p.Retain >= p.Trigger {
		return invalidCompactionPolicy("retain", "must be less than trigger")
	}
	if p.Trigger >= p.Usable {
		return invalidCompactionPolicy("trigger", "must be less than usable")
	}
	return nil
}

// OverflowPolicy returns the forced-overflow variant of the policy: retain is
// halved from 16% to 8% of usable (R-A5-2, R-A5-11 and A §6.8). The receiver is
// not mutated.
func (p CompactionPolicy) OverflowPolicy() CompactionPolicy {
	overflow := p
	if p.Usable > 0 {
		overflow.Retain = p.Usable * compactionOverflowRetainPercent / 100
	}
	return overflow
}

// ShouldCompact reports the pressure trigger: the provider's last prompt token
// count plus the caller's estimate of the new round content reaches the trigger
// (R-A5-2). Negative inputs are clamped to zero so a provider that omits usage
// cannot suppress the boundary.
func (p CompactionPolicy) ShouldCompact(lastProviderPromptTokens, estimatedNewTokens int) bool {
	if lastProviderPromptTokens < 0 {
		lastProviderPromptTokens = 0
	}
	if estimatedNewTokens < 0 {
		estimatedNewTokens = 0
	}
	return int64(lastProviderPromptTokens)+int64(estimatedNewTokens) >= int64(p.Trigger)
}

// ShouldCompact is the package-level form of CompactionPolicy.ShouldCompact.
func ShouldCompact(policy CompactionPolicy, lastProviderPromptTokens, estimatedNewTokens int) bool {
	return policy.ShouldCompact(lastProviderPromptTokens, estimatedNewTokens)
}

// CompactionTrigger names why a compaction is attempted (R-A5-2/R-A5-3). It is
// deliberately separate from Invalidation, which only describes a prefix-cache
// boundary. v0.22 has exactly three triggers: pressure, overflow and
// stage_boundary; manual and cadence triggers are not part of this version.
type CompactionTrigger string

const (
	// CompactionTriggerPressure is the proactive per-round trigger.
	CompactionTriggerPressure CompactionTrigger = "pressure"
	// CompactionTriggerOverflow is forced by a provider context-overflow error.
	CompactionTriggerOverflow CompactionTrigger = "overflow"
	// CompactionTriggerStageBoundary is emitted when the version stage changes.
	CompactionTriggerStageBoundary CompactionTrigger = "stage_boundary"
)

// Short aliases matching the spec shorthand
// CompactionTrigger{Pressure, Overflow, StageBoundary}.
const (
	Pressure      = CompactionTriggerPressure
	Overflow      = CompactionTriggerOverflow
	StageBoundary = CompactionTriggerStageBoundary
)

// String returns the stable payload spelling of the trigger.
func (t CompactionTrigger) String() string { return string(t) }

// Valid reports whether the trigger belongs to the v0.22 set.
func (t CompactionTrigger) Valid() bool {
	switch t {
	case CompactionTriggerPressure, CompactionTriggerOverflow, CompactionTriggerStageBoundary:
		return true
	default:
		return false
	}
}

// Validate returns a typed invalid-argument error for an unknown trigger.
func (t CompactionTrigger) Validate() error {
	if !t.Valid() {
		return platform.Invalid("trigger", "must be one of pressure, overflow, stage_boundary")
	}
	return nil
}

// CompactionDecision is one policy-aware trigger decision. Policy is the
// effective policy of the attempt (overflow carries the halved retain); Retain
// is the literal retain budget so a caller does not have to re-derive it.
type CompactionDecision struct {
	Trigger    CompactionTrigger
	Policy     CompactionPolicy
	Retain     int
	Forced     bool
	MaxRetries int
}

// PressureDecision returns the proactive decision when the pressure threshold
// is reached, and ok=false below it. The normal 16% retain applies.
func (p CompactionPolicy) PressureDecision(lastProviderPromptTokens, estimatedNewTokens int) (CompactionDecision, bool) {
	if !p.ShouldCompact(lastProviderPromptTokens, estimatedNewTokens) {
		return CompactionDecision{}, false
	}
	return CompactionDecision{
		Trigger: CompactionTriggerPressure,
		Policy:  p,
		Retain:  p.Retain,
	}, true
}

// OverflowDecision returns the forced decision of a provider over-window error:
// retain is halved to 8% of usable and the retry budget is capped at one
// (R-A5-2). maxRetries < 0 uses the v0.22 default; 0 disables the retry and a
// larger configured value is capped at MaxCompactionOverflowRetries.
func (p CompactionPolicy) OverflowDecision(maxRetries int) CompactionDecision {
	policy := p.OverflowPolicy()
	return CompactionDecision{
		Trigger:    CompactionTriggerOverflow,
		Policy:     policy,
		Retain:     policy.Retain,
		Forced:     true,
		MaxRetries: normalizeOverflowRetries(maxRetries),
	}
}

// StageBoundaryDecision returns the forced decision of a stage transition. It
// is produced even when the prompt is below the pressure threshold; the runner
// wiring that acts on it is Task 36.
func (p CompactionPolicy) StageBoundaryDecision() CompactionDecision {
	return CompactionDecision{
		Trigger: CompactionTriggerStageBoundary,
		Policy:  p,
		Retain:  p.Retain,
		Forced:  true,
	}
}

func normalizeOverflowRetries(maxRetries int) int {
	if maxRetries < 0 {
		return DefaultCompactionOverflowRetries
	}
	if maxRetries > MaxCompactionOverflowRetries {
		return MaxCompactionOverflowRetries
	}
	return maxRetries
}

// EventContextCompacted is the R-A5-15 version-channel event emitted after one
// compaction transaction ends.
const EventContextCompacted = "context.compacted"

// CompactionEvent is the R-A5-15 context.compacted payload. Err is optional and
// records a failed compaction so the run keeps the original context while the
// failure stays observable.
type CompactionEvent struct {
	Trigger          CompactionTrigger
	BeforeTokens     int
	AfterTokens      int
	RetainTokens     int
	SummaryVersion   int
	CoveredMessageID int64
	CachedTokens     int
	Duration         time.Duration
	Err              string
}

// Payload renders the stable field names consumed by the frontend
// useVersionEvents hook (Task 37) and by the event store. The error key is
// present only for a failed compaction.
func (e CompactionEvent) Payload() map[string]any {
	payload := map[string]any{
		"trigger":            string(e.Trigger),
		"before_tokens":      e.BeforeTokens,
		"after_tokens":       e.AfterTokens,
		"retain_tokens":      e.RetainTokens,
		"summary_version":    e.SummaryVersion,
		"covered_message_id": e.CoveredMessageID,
		"cached_tokens":      e.CachedTokens,
		"duration_ms":        e.Duration.Milliseconds(),
	}
	if e.Err != "" {
		payload["error"] = e.Err
	}
	return payload
}

// CompactionEventPublisher is the narrow event port of the compaction core.
// Task 36 adapts the application eventing publisher to it. Implementations must
// be safe for the caller's goroutine; emission is best effort and never fails a
// run.
type CompactionEventPublisher interface {
	PublishCompaction(ctx context.Context, event CompactionEvent) error
}

// CompactionTransactionState is the R-A5-10 state machine:
// start → summary → end, with error as the failure terminal.
type CompactionTransactionState string

const (
	// CompactionStateStarted is the moment the per-conversation lock is held.
	CompactionStateStarted CompactionTransactionState = "start"
	// CompactionStateSummary is the summary-generation phase.
	CompactionStateSummary CompactionTransactionState = "summary"
	// CompactionStateEnded is the terminal success state.
	CompactionStateEnded CompactionTransactionState = "end"
	// CompactionStateFailed is the terminal error state.
	CompactionStateFailed CompactionTransactionState = "error"
)

// CompactionTransaction is one in-flight compaction of one conversation. ID
// fences the state transitions: only the holder of the transaction may advance
// or end it. MaxRetries is the overflow retry budget carried from a
// policy-aware decision (R-A5-2); it is zero for non-overflow transactions.
type CompactionTransaction struct {
	ID             string
	ConversationID int64
	Trigger        CompactionTrigger
	Policy         CompactionPolicy
	MaxRetries     int
	State          CompactionTransactionState
	StartedAt      time.Time
	UpdatedAt      time.Time
}

// CompactionOutcome is what a successful compaction reports to the transaction
// manager. RetainTokens may be zero or negative; Finish then publishes the
// transaction policy's retain budget instead. The summary/covered-message
// identifiers are produced by the Task 13 persistence path and simply carried
// through the event here.
type CompactionOutcome struct {
	BeforeTokens     int
	AfterTokens      int
	RetainTokens     int
	SummaryVersion   int
	CoveredMessageID int64
	CachedTokens     int
}

var (
	// ErrCompactionBusy reports that another live compaction transaction owns
	// the conversation. The caller keeps the original context and continues.
	ErrCompactionBusy = errors.New("agentcontext: compaction already in progress for this conversation")
	// ErrCompactionNotStarted reports a transaction that cannot advance or end
	// because it is not the live holder in the required phase.
	ErrCompactionNotStarted = errors.New("agentcontext: compaction transaction is not active")
	// ErrCompactionFailed is the sentinel wrapper of a failed (but recoverable)
	// compaction.
	ErrCompactionFailed = errors.New("agentcontext: compaction failed")
	// ErrCompactionThrashing reports two consecutive compactions that did not
	// reduce the prompt size (R-A5-11).
	ErrCompactionThrashing = errors.New("agentcontext: compaction is not reducing tokens")
	// ErrContextOverflowUnrecoverable reports R-A5-11's terminal overflow: even
	// an isolated, indivisible unit is larger than the usable window.
	ErrContextOverflowUnrecoverable = errors.New("context_overflow_unrecoverable")
)

// CompactionFailure is the recoverable failure shape: the run keeps the
// original context and continues (R-A5-10). Unrecoverable overflow is returned
// as *ContextOverflowError instead, so it is never wrapped by this type.
type CompactionFailure struct {
	Trigger        CompactionTrigger
	ConversationID int64
	Cause          error
}

func (e *CompactionFailure) Error() string {
	if e == nil {
		return ErrCompactionFailed.Error()
	}
	if e.Cause == nil {
		return ErrCompactionFailed.Error()
	}
	return fmt.Sprintf("%s: %v", ErrCompactionFailed, e.Cause)
}

// Unwrap exposes both the compaction-failed sentinel and the underlying cause
// to errors.Is/errors.As.
func (e *CompactionFailure) Unwrap() []error {
	if e == nil || e.Cause == nil {
		return []error{ErrCompactionFailed}
	}
	return []error{ErrCompactionFailed, e.Cause}
}

// IsRecoverableCompactionError reports whether err is a compaction stop the run
// may ignore while keeping its original context (R-A5-10): a *CompactionFailure,
// a busy conversation (ErrCompactionBusy) or the R-A5-11 thrashing stop
// (ErrCompactionThrashing). Unrecoverable overflow, invalid configuration and a
// canceled context are not recoverable compaction stops.
func IsRecoverableCompactionError(err error) bool {
	if err == nil || errors.Is(err, ErrContextOverflowUnrecoverable) {
		return false
	}
	if errors.Is(err, ErrCompactionBusy) || errors.Is(err, ErrCompactionThrashing) {
		return true
	}
	var failure *CompactionFailure
	return errors.As(err, &failure)
}

// ContextOverflowError is R-A5-11's terminal overflow diagnostic. The message
// carries W/O/usable/trigger/retain, the actual prompt size, the largest
// indivisible unit and the operator suggestion.
type ContextOverflowError struct {
	Policy            CompactionPolicy
	PromptTokens      int
	LargestUnitTokens int
	Suggestion        string
}

func (e *ContextOverflowError) Error() string {
	if e == nil {
		return ErrContextOverflowUnrecoverable.Error()
	}
	message := fmt.Sprintf("%s: prompt=%d usable=%d window=%d output=%d trigger=%d retain=%d largest_unit=%d",
		ErrContextOverflowUnrecoverable, e.PromptTokens, e.Policy.Usable,
		e.Policy.Window, e.Policy.Output, e.Policy.Trigger, e.Policy.Retain,
		e.LargestUnitTokens)
	if e.Suggestion != "" {
		message += "; " + e.Suggestion
	}
	return message
}

// Unwrap exposes ErrContextOverflowUnrecoverable to errors.Is.
func (e *ContextOverflowError) Unwrap() error { return ErrContextOverflowUnrecoverable }

// CompactionOperation performs the actual summarization/folding while the
// transaction owns the conversation. It receives a transaction in the summary
// state. The returned identifiers are the persistence hook Task 13/36 fills in;
// the compaction core only carries them into the event.
type CompactionOperation func(ctx context.Context, transaction CompactionTransaction) (CompactionOutcome, error)

// CompactionManagerOption customizes a CompactionManager.
type CompactionManagerOption func(*CompactionManager)

// WithCompactionClock injects the time source used for TTL, duration and
// transaction IDs. A nil clock keeps the system clock.
func WithCompactionClock(source clock.Clock) CompactionManagerOption {
	return func(manager *CompactionManager) {
		if manager == nil || source == nil {
			return
		}
		manager.now = source.Now
	}
}

// WithCompactionTTL overrides the stale-transaction TTL. Non-positive values
// keep the 10-minute spec default.
func WithCompactionTTL(ttl time.Duration) CompactionManagerOption {
	return func(manager *CompactionManager) {
		if manager == nil || ttl <= 0 {
			return
		}
		manager.ttl = ttl
	}
}

// WithCompactionPublisher wires the context.compacted port. A nil publisher
// disables emission without failing compaction.
func WithCompactionPublisher(publisher CompactionEventPublisher) CompactionManagerOption {
	return func(manager *CompactionManager) {
		if manager == nil {
			return
		}
		manager.publisher = publisher
	}
}

// WithCompactionLogger sets the logger used for best-effort event failures.
func WithCompactionLogger(logger *slog.Logger) CompactionManagerOption {
	return func(manager *CompactionManager) {
		if manager == nil || logger == nil {
			return
		}
		manager.logger = logger
	}
}

// CompactionManager owns the R-A5-10 transaction state machine: one live
// compaction per conversation, a 10-minute stale-start TTL, the
// context.compacted event port and the R-A5-11 thrashing guard.
//
// The manager is the deterministic, in-process reference implementation. Task
// 36 wires it into the runner; the manager coordinates in-process callers only,
// and R-A5-10's cross-process locking is left to that runner wiring. The
// manager itself never touches the transcript: a failed or busy compaction
// leaves the caller's original context untouched.
type CompactionManager struct {
	mu        sync.Mutex
	now       func() time.Time
	ttl       time.Duration
	publisher CompactionEventPublisher
	logger    *slog.Logger
	active    map[int64]CompactionTransaction
	recent    map[int64][]CompactionOutcome
	sequence  uint64
}

// NewCompactionManager wires the transaction manager. With no options it uses
// the system clock, the 10-minute TTL and no event publisher.
func NewCompactionManager(options ...CompactionManagerOption) *CompactionManager {
	manager := &CompactionManager{
		now:    time.Now,
		ttl:    DefaultCompactionTTL,
		active: make(map[int64]CompactionTransaction),
		recent: make(map[int64][]CompactionOutcome),
	}
	for _, option := range options {
		if option != nil {
			option(manager)
		}
	}
	return manager
}

// Start atomically begins a transaction for one conversation. It first reclaims
// stale started transactions (R-A5-10 TTL), then rejects a second live
// transaction with ErrCompactionBusy. The trigger and policy are validated on
// every Start, and an overflow trigger always runs with the halved 8% overflow
// policy and the v0.22 retry budget.
func (m *CompactionManager) Start(ctx context.Context, conversationID int64, trigger CompactionTrigger, policy CompactionPolicy) (CompactionTransaction, error) {
	return m.startTransaction(ctx, conversationID, trigger, policy, defaultCompactionRetries(trigger))
}

// StartDecision begins a transaction from a policy-aware trigger decision. It is
// the entry point for callers that already resolved a decision: an overflow
// decision carries the halved 8% retain policy (R-A5-2/R-A5-11) and its retry
// budget into the transaction, so the operation cannot accidentally run with
// the normal 16% retain. A decision with an overflow trigger is normalized to
// OverflowPolicy even when it was built from the normal policy.
func (m *CompactionManager) StartDecision(ctx context.Context, conversationID int64, decision CompactionDecision) (CompactionTransaction, error) {
	if err := decision.Trigger.Validate(); err != nil {
		return CompactionTransaction{}, err
	}
	policy := decision.Policy
	maxRetries := 0
	if decision.Trigger == CompactionTriggerOverflow {
		policy = policy.OverflowPolicy()
		maxRetries = normalizeOverflowRetries(decision.MaxRetries)
	}
	return m.startTransaction(ctx, conversationID, decision.Trigger, policy, maxRetries)
}

// defaultCompactionRetries is the retry budget of a plain Start: R-A5-2 allows
// one retry after an overflow, none for the proactive triggers.
func defaultCompactionRetries(trigger CompactionTrigger) int {
	if trigger == CompactionTriggerOverflow {
		return DefaultCompactionOverflowRetries
	}
	return 0
}

func (m *CompactionManager) startTransaction(ctx context.Context, conversationID int64, trigger CompactionTrigger, policy CompactionPolicy, maxRetries int) (CompactionTransaction, error) {
	if err := contextError(ctx); err != nil {
		return CompactionTransaction{}, err
	}
	if conversationID <= 0 {
		return CompactionTransaction{}, platform.Invalid("conversation_id", "must be a positive decimal ID")
	}
	if err := trigger.Validate(); err != nil {
		return CompactionTransaction{}, err
	}
	if trigger == CompactionTriggerOverflow {
		policy = policy.OverflowPolicy()
	}
	if err := policy.Validate(); err != nil {
		return CompactionTransaction{}, err
	}
	now := m.currentTime()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupExpiredLocked(now)
	if _, busy := m.active[conversationID]; busy {
		return CompactionTransaction{}, ErrCompactionBusy
	}
	m.sequence++
	transaction := CompactionTransaction{
		ID:             fmt.Sprintf("compaction-%d-%d-%d", conversationID, now.UnixNano(), m.sequence),
		ConversationID: conversationID,
		Trigger:        trigger,
		Policy:         policy,
		MaxRetries:     maxRetries,
		State:          CompactionStateStarted,
		StartedAt:      now,
		UpdatedAt:      now,
	}
	m.active[conversationID] = transaction
	return transaction, nil
}

// MarkSummary advances a live transaction to the summary phase. Advancing a
// transaction that is already in the summary phase is idempotent.
func (m *CompactionManager) MarkSummary(ctx context.Context, transaction CompactionTransaction) (CompactionTransaction, error) {
	if err := contextError(ctx); err != nil {
		return CompactionTransaction{}, err
	}
	now := m.currentTime()
	m.mu.Lock()
	defer m.mu.Unlock()
	active, err := m.activeLocked(transaction)
	if err != nil {
		return CompactionTransaction{}, err
	}
	switch active.State {
	case CompactionStateStarted, CompactionStateSummary:
	default:
		return CompactionTransaction{}, fmt.Errorf("%w: transaction %s is %s",
			ErrCompactionNotStarted, active.ID, active.State)
	}
	active.State = CompactionStateSummary
	active.UpdatedAt = now
	m.active[active.ConversationID] = active
	return active, nil
}

// Finish ends a successful transaction and publishes context.compacted. The
// R-A5-10 state machine is start → summary → end, so a direct start → end is
// rejected with ErrCompactionNotStarted and the live transaction is kept for the
// summary path.
//
// Finish returns ErrCompactionThrashing when this compaction is the second
// consecutive one that did not reduce the prompt size (R-A5-11). That stop is
// best effort: IsRecoverableCompactionError reports true, the lock is released,
// the event still records the attempt and the run keeps its original context.
//
// A canceled context aborts the Finish, releases the live transaction and emits
// a failure event instead of leaking the per-conversation lock until the TTL.
func (m *CompactionManager) Finish(ctx context.Context, transaction CompactionTransaction, outcome CompactionOutcome) (CompactionEvent, error) {
	if err := contextError(ctx); err != nil {
		m.abortFinish(ctx, transaction, err)
		return CompactionEvent{}, err
	}
	now := m.currentTime()
	m.mu.Lock()
	active, err := m.activeLocked(transaction)
	if err != nil {
		m.mu.Unlock()
		return CompactionEvent{}, err
	}
	if active.State != CompactionStateSummary {
		m.mu.Unlock()
		return CompactionEvent{}, fmt.Errorf("%w: transaction %s is %s, want %s",
			ErrCompactionNotStarted, active.ID, active.State, CompactionStateSummary)
	}
	active.State = CompactionStateEnded
	active.UpdatedAt = now
	delete(m.active, active.ConversationID)
	event := CompactionEvent{
		Trigger:          active.Trigger,
		BeforeTokens:     outcome.BeforeTokens,
		AfterTokens:      outcome.AfterTokens,
		RetainTokens:     publishedRetainTokens(outcome.RetainTokens, active.Policy.Retain),
		SummaryVersion:   outcome.SummaryVersion,
		CoveredMessageID: outcome.CoveredMessageID,
		CachedTokens:     outcome.CachedTokens,
		Duration:         nonNegativeDuration(now.Sub(active.StartedAt)),
	}
	thrashing := m.recordOutcomeLocked(active.ConversationID, outcome)
	m.mu.Unlock()
	m.publish(ctx, event)
	if thrashing {
		return event, fmt.Errorf("%w: conversation %d did not reduce tokens twice in a row",
			ErrCompactionThrashing, active.ConversationID)
	}
	return event, nil
}

// abortFinish ends a transaction whose Finish was aborted (for example by a
// canceled run context). It removes the live transaction so the conversation
// lock cannot leak until the TTL (R-A5-10) and emits the failure event, treating
// the abort as a failed compaction.
func (m *CompactionManager) abortFinish(ctx context.Context, transaction CompactionTransaction, cause error) {
	now := m.currentTime()
	m.mu.Lock()
	active, ok := m.active[transaction.ConversationID]
	if !ok || active.ID != transaction.ID {
		m.mu.Unlock()
		return
	}
	active.State = CompactionStateFailed
	active.UpdatedAt = now
	delete(m.active, active.ConversationID)
	event := CompactionEvent{
		Trigger:      active.Trigger,
		RetainTokens: active.Policy.Retain,
		Duration:     nonNegativeDuration(now.Sub(active.StartedAt)),
		Err:          errorText(cause),
	}
	m.mu.Unlock()
	m.publish(ctx, event)
}

// publishedRetainTokens keeps the event retain_tokens meaningful: an operation
// that does not report a positive budget falls back to the transaction policy's
// retain (the normal 16% or the overflow 8%).
func publishedRetainTokens(outcomeRetain, policyRetain int) int {
	if outcomeRetain > 0 {
		return outcomeRetain
	}
	return policyRetain
}

// Fail ends a transaction after an error, publishes the failure event and
// releases the lock. Non-overflow failures are returned as the recoverable
// *CompactionFailure (IsRecoverableCompactionError reports true); an
// unrecoverable overflow is returned unchanged so the runner can terminate the
// run.
func (m *CompactionManager) Fail(ctx context.Context, transaction CompactionTransaction, cause error) error {
	now := m.currentTime()
	m.mu.Lock()
	active, err := m.activeLocked(transaction)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	active.State = CompactionStateFailed
	active.UpdatedAt = now
	delete(m.active, active.ConversationID)
	event := CompactionEvent{
		Trigger:      active.Trigger,
		RetainTokens: active.Policy.Retain,
		Duration:     nonNegativeDuration(now.Sub(active.StartedAt)),
		Err:          errorText(cause),
	}
	m.mu.Unlock()
	m.publish(ctx, event)
	if errors.Is(cause, ErrContextOverflowUnrecoverable) {
		return cause
	}
	return &CompactionFailure{
		Trigger:        active.Trigger,
		ConversationID: active.ConversationID,
		Cause:          cause,
	}
}

// Run drives one full start → summary → end transaction around operation. A
// failed operation releases the lock and returns a recoverable
// *CompactionFailure. A busy conversation and the R-A5-11 thrashing stop are
// recoverable too (IsRecoverableCompactionError reports true), so the run keeps
// its original context and continues without compaction. Only unrecoverable
// overflow is returned as a terminal error.
func (m *CompactionManager) Run(ctx context.Context, conversationID int64, trigger CompactionTrigger, policy CompactionPolicy, operation CompactionOperation) (CompactionOutcome, error) {
	return m.RunDecision(ctx, conversationID, CompactionDecision{
		Trigger:    trigger,
		Policy:     policy,
		Retain:     policy.Retain,
		MaxRetries: defaultCompactionRetries(trigger),
	}, operation)
}

// RunDecision is the decision-aware form of Run: the transaction runs with the
// decision's effective policy (an overflow decision carries the halved 8%
// retain, R-A5-2/R-A5-11) and retry budget.
func (m *CompactionManager) RunDecision(ctx context.Context, conversationID int64, decision CompactionDecision, operation CompactionOperation) (CompactionOutcome, error) {
	transaction, err := m.StartDecision(ctx, conversationID, decision)
	if err != nil {
		if errors.Is(err, ErrCompactionBusy) {
			return CompactionOutcome{}, &CompactionFailure{
				Trigger:        decision.Trigger,
				ConversationID: conversationID,
				Cause:          err,
			}
		}
		return CompactionOutcome{}, err
	}
	return m.runTransaction(ctx, transaction, operation)
}

func (m *CompactionManager) runTransaction(ctx context.Context, transaction CompactionTransaction, operation CompactionOperation) (CompactionOutcome, error) {
	summaryTransaction, err := m.MarkSummary(ctx, transaction)
	if err != nil {
		return CompactionOutcome{}, m.Fail(ctx, transaction, err)
	}
	if operation == nil {
		return CompactionOutcome{}, m.Fail(ctx, transaction, errors.New("compaction operation is not configured"))
	}
	outcome, operationErr := operation(ctx, summaryTransaction)
	if operationErr != nil {
		return CompactionOutcome{}, m.Fail(ctx, transaction, operationErr)
	}
	if _, err := m.Finish(ctx, transaction, outcome); err != nil {
		return outcome, err
	}
	return outcome, nil
}

// CleanupExpired reclaims transactions that were started but never ended and
// are at least the TTL old. It returns the number of reclaimed transactions.
func (m *CompactionManager) CleanupExpired(ctx context.Context) int {
	if err := contextError(ctx); err != nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cleanupExpiredLocked(m.currentTime())
}

// Active returns the live transaction of one conversation, if any. Stale
// started transactions are reclaimed first, so a crash-abandoned transaction is
// never reported as live.
func (m *CompactionManager) Active(ctx context.Context, conversationID int64) (CompactionTransaction, bool) {
	if err := contextError(ctx); err != nil {
		return CompactionTransaction{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupExpiredLocked(m.currentTime())
	transaction, ok := m.active[conversationID]
	return transaction, ok
}

func (m *CompactionManager) activeLocked(transaction CompactionTransaction) (CompactionTransaction, error) {
	active, ok := m.active[transaction.ConversationID]
	if !ok || active.ID != transaction.ID {
		return CompactionTransaction{}, fmt.Errorf("%w: conversation %d", ErrCompactionNotStarted, transaction.ConversationID)
	}
	return active, nil
}

// recordOutcomeLocked records the last two completed outcomes of a
// conversation and reports R-A5-11 thrashing: both were non-reductions.
func (m *CompactionManager) recordOutcomeLocked(conversationID int64, outcome CompactionOutcome) bool {
	history := append(m.recent[conversationID], outcome)
	if len(history) > 2 {
		history = history[len(history)-2:]
	}
	m.recent[conversationID] = history
	if len(history) < 2 {
		return false
	}
	return !compactionReducedTokens(history[0]) && !compactionReducedTokens(history[1])
}

func compactionReducedTokens(outcome CompactionOutcome) bool {
	return outcome.AfterTokens < outcome.BeforeTokens
}

// cleanupExpiredLocked removes stale started transactions. A transaction is
// stale once TTL has fully elapsed, including one stuck in the summary phase
// (start without end, R-A5-10).
func (m *CompactionManager) cleanupExpiredLocked(now time.Time) int {
	removed := 0
	for conversationID, transaction := range m.active {
		if now.Sub(transaction.StartedAt) >= m.ttl {
			delete(m.active, conversationID)
			removed++
		}
	}
	return removed
}

func (m *CompactionManager) currentTime() time.Time {
	if m == nil || m.now == nil {
		return time.Now()
	}
	return m.now()
}

// publish emits context.compacted best effort. Event transport failures never
// fail the compaction or the run; they are logged for observability.
func (m *CompactionManager) publish(ctx context.Context, event CompactionEvent) {
	if m == nil || m.publisher == nil {
		return
	}
	publishContext := context.Background()
	if ctx != nil {
		publishContext = context.WithoutCancel(ctx)
	}
	if err := m.publisher.PublishCompaction(publishContext, event); err != nil {
		m.logWarn(ctx, "context compaction: publish context.compacted failed", "error", err)
	}
}

func (m *CompactionManager) logWarn(ctx context.Context, message string, args ...any) {
	logger := (*slog.Logger)(nil)
	if m != nil {
		logger = m.logger
	}
	if logger == nil {
		logger = slog.Default()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	logger.WarnContext(ctx, message, args...)
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func nonNegativeDuration(duration time.Duration) time.Duration {
	if duration < 0 {
		return 0
	}
	return duration
}

func errorText(err error) string {
	if err == nil {
		return ErrCompactionFailed.Error()
	}
	return err.Error()
}
